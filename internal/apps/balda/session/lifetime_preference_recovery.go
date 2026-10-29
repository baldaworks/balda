package session

import (
	"context"
	"errors"
	"strings"
	"time"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	adksession "google.golang.org/adk/v2/session"
)

// recoverApplyingLifetimePreferenceOperation forward-completes only the
// narrow restore-fallback transition. Repeating delete/create is intentional:
// the stable ADK identity cannot distinguish predecessor from an already-empty
// target after a crash, while replacement is idempotently context-destructive.
func (m *Manager) recoverApplyingLifetimePreferenceOperation(ctx context.Context, operation baldastate.RuntimeOperationRecord) error {
	m.fenceRuntimeRecovery(operation.SessionID)
	if operation.State != baldastate.RuntimeOperationApplying ||
		operation.Kind != string(baldastate.RuntimeOperationKindLifetimePreference) ||
		m.activeTopicSession(operation.SessionID) != nil {
		return ErrRuntimeRecoveryRequired
	}
	record, found, err := m.sessionStore.GetBySessionID(ctx, operation.SessionID)
	if err != nil || !found || !lifetimePreferenceIncumbentMatches(record, operation) ||
		m.runtimeManager == nil || m.agentBuilder == nil {
		return ErrRuntimeRecoveryRequired
	}
	locator, err := LocatorFromRecord(record)
	if err != nil {
		return ErrRuntimeRecoveryRequired
	}
	workspaceDir := strings.TrimSpace(record.WorkspaceDir)
	if workspaceDir == "" && !m.workspaceEnabled {
		workspaceDir = m.workingDir
	}
	runtime, err := m.bindLifetimePreferenceRecoveryRuntime(ctx, locator, record, operation, workspaceDir)
	if err != nil {
		return lifetimePreferenceRecoveryError(ctx)
	}
	defer closeBuiltRuntime(runtime)
	if runtime.RecipeModel != operation.TargetModel || runtime.SessionSvc == nil {
		return ErrRuntimeRecoveryRequired
	}
	appName := strings.TrimSpace(runtime.AppName)
	if appName == "" {
		appName = baldaRuntimeAppName
	}
	if err := m.notifyBoundary(ctx, SessionBoundary{
		Locator: locator, SessionID: record.SessionID, AgentSessionID: record.SessionID, Reason: BoundaryReasonRotation,
		TransitionID: operation.OperationID, OccurredAt: operation.CreatedAt,
	}); err != nil {
		return lifetimePreferenceRecoveryError(ctx)
	}
	deleteErr := runtime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{
		AppName: appName, UserID: record.UserID, SessionID: record.SessionID,
	})
	if deleteErr != nil && !errors.Is(deleteErr, adksession.ErrNotFound) {
		return lifetimePreferenceRecoveryError(ctx)
	}
	agentName := strings.TrimSpace(record.AgentName)
	if agentName == "" {
		agentName = defaultSessionLabel
	}
	sess, err := m.agentBuilder.CreateFreshRuntimeSession(
		ctx, runtime, agentName, record.UserID, record.SessionID, workspaceDir,
		RuntimeSessionContext{ArchiveID: record.ArchiveID, BaldaSessionID: record.SessionID, SessionBranch: record.BranchName},
	)
	if err != nil || sess == nil || sess.AppName() != appName || sess.UserID() != record.UserID || sess.ID() != record.SessionID {
		return lifetimePreferenceRecoveryError(ctx)
	}
	at := time.Now().UTC()
	if operation.UpdatedAt.After(at) {
		at = operation.UpdatedAt
	}
	terminal, err := m.runtimeOperations.Finalize(ctx, baldastate.RuntimeOperationFinalization{
		OperationID: operation.OperationID, InputHash: operation.InputHash,
		Outcome: baldastate.RuntimeOperationOutcomeContextReset, At: at,
	})
	if err != nil || terminal.State != baldastate.RuntimeOperationTerminal ||
		terminal.Outcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		return lifetimePreferenceRecoveryError(ctx)
	}
	committed, found, err := m.sessionStore.GetBySessionID(ctx, record.SessionID)
	if err != nil || !found || !lifetimePreferenceTargetMatches(committed, operation) {
		return ErrRuntimeRecoveryRequired
	}
	return nil
}

func (m *Manager) bindLifetimePreferenceRecoveryRuntime(
	ctx context.Context,
	locator SessionLocator,
	record baldastate.SessionRecord,
	operation baldastate.RuntimeOperationRecord,
	workspaceDir string,
) (*BuiltRuntime, error) {
	if scoped, ok := m.runtimeManager.(SessionRuntimeManager); ok {
		runtime, err := scoped.RuntimeForSession(ctx, SessionRuntimeRequest{
			Locator: locator, UserID: record.UserID, AgentSessionID: record.SessionID,
			WorkspaceDir: workspaceDir, RuntimeSnapshotID: operation.TargetSnapshotID,
			ProviderBindingJSON: operation.TargetProviderBindingJSON,
			ModelOverride:       cloneOptionalModel(operation.TargetRequestedModel),
		})
		if err != nil {
			return nil, err
		}
		return validateSessionRuntimeBinding(runtime, operation.TargetSnapshotID, operation.TargetProviderBindingJSON)
	}
	root, err := m.runtimeManager.Runtime(ctx)
	if err != nil {
		return nil, err
	}
	return m.bindSessionRuntime(
		ctx, root, locator, record.UserID, record.SessionID, workspaceDir,
		operation.TargetSnapshotID, operation.TargetProviderBindingJSON, operation.TargetRequestedModel,
	)
}

func lifetimePreferenceIncumbentMatches(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	return record.SessionID == operation.SessionID && record.ArchiveID == operation.ExpectedArchiveID &&
		record.RuntimeGeneration == operation.ExpectedGeneration && operation.TargetArchiveID == operation.ExpectedArchiveID &&
		operation.TargetSnapshotID != "" && sameOptionalModel(record.RequestedModel, operation.TargetRequestedModel) &&
		(operation.TargetRequestedModel == nil || *operation.TargetRequestedModel == operation.TargetModel)
}

func lifetimePreferenceTargetMatches(record baldastate.SessionRecord, operation baldastate.RuntimeOperationRecord) bool {
	return record.SessionID == operation.SessionID && record.ArchiveID == operation.TargetArchiveID &&
		record.RuntimeGeneration == operation.ExpectedGeneration+1 && record.RuntimeSnapshotID == operation.TargetSnapshotID &&
		record.ProviderBindingJSON == operation.TargetProviderBindingJSON &&
		sameOptionalModel(record.RequestedModel, operation.TargetRequestedModel) &&
		record.EffectiveModel == operation.TargetModel &&
		record.RuntimeOutcome == string(baldastate.RuntimeOperationOutcomeContextReset)
}

func lifetimePreferenceRecoveryError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrRuntimeRecoveryRequired
}
