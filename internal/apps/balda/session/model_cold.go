package session

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
)

// openExactModelRuntime restores the persisted runtime without selecting a
// replacement provider, snapshot, or history identity.
func (m *Manager) openExactModelRuntime(
	ctx context.Context,
	record baldastate.SessionRecord,
	snapshotID string,
	providerBindingJSON string,
	modelOverride *string,
	recoveryRequired bool,
) (*TopicSession, error) {
	if m.runtimeManager == nil || m.agentBuilder == nil || m.archive == nil {
		return nil, ErrRuntimeUnavailable
	}
	scoped, ok := m.runtimeManager.(UnpublishedSessionRuntimeManager)
	if !ok {
		return nil, ErrRuntimeUnavailable
	}
	locator, err := LocatorFromRecord(record)
	if err != nil {
		return nil, err
	}
	archived, err := m.archive.Get(ctx, record.ArchiveID)
	if err != nil || archived.Status != chatarchivecmd.StatusActive || archived.StableID != record.SessionID ||
		strings.TrimSpace(archived.RuntimeID) == "" || archived.Location.Channel != record.ChannelType ||
		archived.Location.AddressKey != record.AddressKey {
		return nil, ErrRuntimeUnavailable
	}
	workspaceDir := strings.TrimSpace(record.WorkspaceDir)
	if workspaceDir == "" && !m.workspaceEnabled {
		workspaceDir = m.workingDir
	}
	request := SessionRuntimeRequest{
		Locator: locator, UserID: record.UserID, AgentSessionID: archived.RuntimeID,
		WorkspaceDir: workspaceDir, RuntimeSnapshotID: snapshotID,
		ProviderBindingJSON: providerBindingJSON, ModelOverride: cloneOptionalModel(modelOverride),
		RecoveryFence: recoveryRequired,
	}
	if recoveryRequired && scoped.FenceSessionRuntimeBase(record.SessionID) != nil {
		return nil, ErrRuntimeRecoveryRequired
	}
	candidate, err := scoped.BuildUnpublishedSessionRuntime(ctx, request)
	if err != nil || candidate == nil || candidate.Runtime() == nil {
		return nil, err
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = candidate.Abort(context.WithoutCancel(ctx))
		}
	}()
	runtime := candidate.Runtime()
	runtime, err = validateSessionRuntimeBinding(runtime, snapshotID, providerBindingJSON)
	if err != nil {
		return nil, err
	}
	agentName := strings.TrimSpace(record.AgentName)
	if agentName == "" {
		agentName = defaultSessionLabel
	}
	sess, err := m.agentBuilder.OpenExistingRuntimeSession(
		ctx, runtime, agentName, record.UserID, archived.RuntimeID, workspaceDir,
		RuntimeSessionContext{ArchiveID: record.ArchiveID, BaldaSessionID: record.SessionID, SessionBranch: record.BranchName},
	)
	if err != nil {
		closeBuiltRuntime(runtime)
		return nil, err
	}
	sessionCtx, err := m.resolveExecutionContext(SessionContext{Locator: locator, UserID: record.UserID})
	if err != nil {
		closeBuiltRuntime(runtime)
		return nil, err
	}
	topic := &TopicSession{
		sessionID: record.SessionID, userID: record.UserID, locator: locator,
		kind: sessionCtx.Kind, ownerJobID: strings.TrimSpace(sessionCtx.OwnerJobID),
		agentName: agentName, workspaceDir: workspaceDir, branchName: record.BranchName,
		recoveryRequired: recoveryRequired,
	}
	topic.runtime = newPublishedRuntimeSlot(runtimeSlotConfig{
		runtime: runtime, session: sess, providerID: runtime.ProviderID,
		archiveID: record.ArchiveID, generation: record.RuntimeGeneration,
		effectiveModel: record.EffectiveModel, outcome: record.RuntimeOutcome,
	})
	if err := m.startSessionHooksForRuntime(ctx, topic, archived.RuntimeID, runtime.ProviderID, sessionHookResume); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.sessions[record.SessionID] != nil {
		m.mu.Unlock()
		m.endSessionHooksForRuntime(context.WithoutCancel(ctx), topic, archived.RuntimeID, runtime.ProviderID)
		return nil, ErrRuntimeUnavailable
	}
	if candidate.ClaimForPublication() != nil || candidate.Promote() != nil {
		m.mu.Unlock()
		m.endSessionHooksForRuntime(context.WithoutCancel(ctx), topic, archived.RuntimeID, runtime.ProviderID)
		return nil, ErrRuntimeUnavailable
	}
	promoted = true
	m.sessions[record.SessionID] = topic
	m.mu.Unlock()
	candidate.MarkPublished()
	if err := candidate.CompletePublication(); err != nil {
		topic.fenceRuntimeRecovery()
		return nil, ErrRuntimeRecoveryRequired
	}
	return topic, nil
}
