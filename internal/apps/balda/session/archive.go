package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/v2/session"
)

const archiveStateAuthor = "system"

// ArchiveLifecycle is the mandatory production journal for conversational lifetimes.
// It is independent of optional semantic-memory boundary observers.
type ArchiveLifecycle interface {
	ReconcileMemory(ctx context.Context, bootID string) error
	Begin(ctx context.Context, intent chatarchivecmd.Intent) (chatarchivecmd.Intent, error)
	Activate(ctx context.Context, token string, live *baldastate.SessionRecord) (chatarchivecmd.Session, error)
	Complete(ctx context.Context, token, outcome string) error
	Pending(ctx context.Context, channel, address string) (chatarchivecmd.Intent, error)
	Get(ctx context.Context, id string) (chatarchivecmd.Session, error)
}

func (m *Manager) beginArchiveActivation(ctx context.Context, loc SessionLocator, runtimeID, label string, persisted *baldastate.SessionRecord) (*chatarchivecmd.Intent, bool, error) {
	if m.archive == nil {
		return nil, false, nil
	}
	pending, err := m.archive.Pending(ctx, loc.ChannelType, loc.AddressKey)
	if err == nil {
		return &pending, true, nil
	}
	if !errors.Is(err, chatarchivecmd.ErrNotFound) {
		return nil, false, err
	}
	if persisted == nil {
		saved, found, err := m.sessionStore.GetByAddress(ctx, loc.ChannelType, loc.AddressKey)
		if err != nil {
			return nil, false, err
		}
		if found && saved.ArchiveID != "" {
			old, err := m.archive.Get(ctx, saved.ArchiveID)
			if err != nil {
				return nil, false, err
			}
			if old.Status == chatarchivecmd.StatusEnded {
				persisted = &saved
			}
		}
	}
	now := m.currentTime()
	bootID := m.bootID
	if bootID == "" {
		bootID = uuid.NewString()
		m.bootID = bootID
	}
	persistence := chatarchivecmd.PersistenceMemory
	if m.sessionsPersistent {
		persistence = chatarchivecmd.PersistenceDurable
	}
	rec := chatarchivecmd.Session{ID: uuid.NewString(), StableID: loc.SessionID, RuntimeID: runtimeID, Persistence: persistence, BootID: bootID, Label: label, StartedAt: now, ObservedAt: now, Location: chatarchivecmd.Location{Channel: loc.ChannelType, AddressKey: loc.AddressKey, Locator: loc.ChannelType + ":" + loc.AddressKey, Audience: "unknown", Subdivision: "unknown"}}
	kind := chatarchivecmd.KindFresh
	if persisted != nil {
		if persisted.ArchiveID != "" {
			rec, err = m.archive.Get(ctx, persisted.ArchiveID)
			if err != nil {
				return nil, false, err
			}
			if rec.Status == chatarchivecmd.StatusEnded {
				predecessor := rec.ID
				rec = chatarchivecmd.Session{ID: uuid.NewString(), StableID: loc.SessionID, RuntimeID: runtimeID, Persistence: persistence, BootID: bootID, Label: label, StartedAt: now, ObservedAt: now, Location: rec.Location, PredecessorID: predecessor}
			} else {
				if rec.Persistence != persistence || rec.RuntimeID != runtimeID {
					return nil, false, chatarchivecmd.ErrConflict
				}
				kind = chatarchivecmd.KindExact
			}
		} else {
			kind = chatarchivecmd.KindAdopt
		}
	}
	intent, err := m.archive.Begin(ctx, chatarchivecmd.Intent{Token: uuid.NewString(), Kind: kind, Session: rec, At: now})
	if err != nil {
		return nil, false, err
	}
	return &intent, false, nil
}

func archiveStateString(sess adksession.Session, key string) string {
	if sess == nil || sess.State() == nil {
		return ""
	}
	value, err := sess.State().Get(key)
	if err != nil {
		return ""
	}
	result, _ := value.(string)
	return result
}

func (m *Manager) markArchiveReady(ctx context.Context, ts *TopicSession, intent *chatarchivecmd.Intent) error {
	if intent == nil {
		return nil
	}
	value, ok := ts.runtimeValue()
	if !ok || value.runtime.SessionSvc == nil || value.session == nil {
		return ErrRuntimeUnavailable
	}
	rec := m.sessionRecordFromValue(ts, baldastate.SessionStatusActive, value)
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	event := adksession.NewEvent(ctx, intent.Token)
	event.Author = archiveStateAuthor
	event.Actions.StateDelta = map[string]any{chatarchivecmd.EpochStateKey: intent.Session.ID, chatarchivecmd.ReadyStateKey: intent.Token, chatarchivecmd.BindingStateKey: string(body)}
	if err := value.runtime.SessionSvc.AppendEvent(ctx, value.session, event); err != nil {
		return fmt.Errorf("persist archive readiness: %w", err)
	}
	return nil
}

func (m *Manager) recoverArchiveActivation(ctx context.Context, root *BuiltRuntime, builder AgentBuilder, intent *chatarchivecmd.Intent, userID, provider string) (*TopicSession, error) {
	appName := strings.TrimSpace(root.AppName)
	if appName == "" {
		appName = baldaRuntimeAppName
	}
	response, err := root.SessionSvc.Get(ctx, &adksession.GetRequest{AppName: appName, UserID: userID, SessionID: intent.Session.RuntimeID})
	if err != nil {
		return nil, fmt.Errorf("read interrupted archive runtime: %w", err)
	}
	if response == nil || response.Session == nil {
		return nil, fmt.Errorf("interrupted archive runtime unavailable")
	}
	current := response.Session
	if archiveStateString(current, chatarchivecmd.EpochStateKey) != intent.Session.ID || archiveStateString(current, chatarchivecmd.ReadyStateKey) != intent.Token {
		return nil, fmt.Errorf("archive activation readiness unproven: %w", chatarchivecmd.ErrConflict)
	}
	var live baldastate.SessionRecord
	if json.Unmarshal([]byte(archiveStateString(current, chatarchivecmd.BindingStateKey)), &live) != nil || live.ArchiveID != intent.Session.ID || live.SessionID != intent.Session.StableID || live.UserID != userID || live.ChannelType != intent.Session.Location.Channel || live.AddressKey != intent.Session.Location.AddressKey {
		return nil, fmt.Errorf("archive activation binding conflicts: %w", chatarchivecmd.ErrConflict)
	}
	loc, err := LocatorFromRecord(live)
	if err != nil {
		return nil, err
	}
	runtime, err := m.bindSessionRuntime(ctx, root, loc, userID, intent.Session.RuntimeID, live.WorkspaceDir, live.RuntimeSnapshotID, live.ProviderBindingJSON)
	if err != nil {
		return nil, err
	}
	sess, err := builder.OpenExistingRuntimeSession(ctx, runtime, provider, userID, intent.Session.RuntimeID, live.WorkspaceDir, RuntimeSessionContext{BaldaSessionID: live.SessionID, SessionBranch: live.BranchName, ArchiveID: live.ArchiveID})
	if err != nil {
		closeBuiltRuntime(runtime)
		return nil, err
	}
	if _, err := m.archive.Activate(ctx, intent.Token, &live); err != nil {
		closeBuiltRuntime(runtime)
		return nil, err
	}
	ts := &TopicSession{sessionID: live.SessionID, userID: userID, locator: loc, agentName: live.AgentName, workspaceDir: live.WorkspaceDir, branchName: live.BranchName, hooksStarted: m.hooks != nil}
	ts.runtime = newPublishedRuntimeSlot(runtimeSlotConfig{
		runtime:        runtime,
		session:        sess,
		userID:         userID,
		sessionID:      intent.Session.RuntimeID,
		archiveID:      live.ArchiveID,
		generation:     live.RuntimeGeneration,
		effectiveModel: live.EffectiveModel,
		outcome:        live.RuntimeOutcome,
	})
	return ts, nil
}

// A failed constructor may already have persisted a runtime. Absence is the
// only evidence allowing a fresh reservation to be abandoned without repair.
func (m *Manager) failArchiveActivation(ctx context.Context, root *BuiltRuntime, intent *chatarchivecmd.Intent, userID string) {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	outcome := chatarchivecmd.StatusUncertain
	appName := root.AppName
	if appName == "" {
		appName = baldaRuntimeAppName
	}
	if _, err := root.SessionSvc.Get(recoveryCtx, &adksession.GetRequest{AppName: appName, UserID: userID, SessionID: intent.Session.RuntimeID}); errors.Is(err, adksession.ErrNotFound) {
		outcome = chatarchivecmd.OutcomeAborted
	}
	if intent.Kind == chatarchivecmd.KindExact {
		outcome = chatarchivecmd.OutcomeAborted
	}
	if err := m.archive.Complete(recoveryCtx, intent.Token, outcome); err != nil {
		m.logger.Warn().Str("archive_id", intent.Session.ID).Msg("archive activation outcome pending")
	}
}

// beginArchiveDestruction runs under the canonical operation lock, before any
// runtime mutation. Optional boundary observers cannot supply this evidence.
func (m *Manager) beginArchiveDestruction(ctx context.Context, id string, reason BoundaryReason) (*chatarchivecmd.Intent, error) {
	if m.archive == nil || id == "" {
		return nil, nil
	}
	rec, err := m.archive.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	pending, err := m.archive.Pending(ctx, rec.Location.Channel, rec.Location.AddressKey)
	if err == nil {
		if pending.Kind != chatarchivecmd.KindDestroy || pending.OldID != id {
			return nil, chatarchivecmd.ErrConflict
		}
		return &pending, nil
	}
	if !errors.Is(err, chatarchivecmd.ErrNotFound) {
		return nil, err
	}
	intent, err := m.archive.Begin(ctx, chatarchivecmd.Intent{Token: uuid.NewString(), Kind: chatarchivecmd.KindDestroy, Session: rec, OldID: id, Reason: string(reason), At: m.currentTime()})
	return &intent, err
}

func (m *Manager) completeArchiveDeletion(ctx context.Context, intent *chatarchivecmd.Intent, svc adksession.Service, appName, userID string, deleteErr error) (bool, error) {
	if intent == nil {
		return deleteErr == nil, deleteErr
	}
	// A failed delete may have committed before its context expired. Give the
	// outcome read and durable completion one independent, bounded budget.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	outcome := chatarchivecmd.OutcomeDestroyed
	if deleteErr != nil {
		outcome = chatarchivecmd.StatusUncertain
		response, err := svc.Get(ctx, &adksession.GetRequest{AppName: appName, UserID: userID, SessionID: intent.Session.RuntimeID})
		if errors.Is(err, adksession.ErrNotFound) {
			outcome = chatarchivecmd.OutcomeDestroyed
		} else if err == nil && response != nil && archiveStateString(response.Session, chatarchivecmd.EpochStateKey) == intent.OldID {
			outcome = chatarchivecmd.OutcomeRetained
		}
	}
	completionErr := m.archive.Complete(ctx, intent.Token, outcome)
	if outcome == chatarchivecmd.OutcomeDestroyed {
		// Authoritative destruction supersedes the failed delete receipt, but a
		// failed journal write still prevents exposing a replacement.
		return true, completionErr
	}
	return false, errors.Join(deleteErr, completionErr)
}

func (m *Manager) reconcileArchiveDestruction(ctx context.Context, loc SessionLocator, userID string) error {
	if m.archive == nil {
		return nil
	}
	intent, err := m.archive.Pending(ctx, loc.ChannelType, loc.AddressKey)
	if errors.Is(err, chatarchivecmd.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if intent.Kind != chatarchivecmd.KindDestroy {
		return nil
	}
	root, err := m.runtimeManager.Runtime(ctx)
	if err != nil {
		return err
	}
	if root == nil || root.SessionSvc == nil {
		return chatarchivecmd.ErrUnavailable
	}
	app := root.AppName
	if app == "" {
		app = baldaRuntimeAppName
	}
	response, err := root.SessionSvc.Get(ctx, &adksession.GetRequest{AppName: app, UserID: userID, SessionID: intent.Session.RuntimeID})
	outcome := chatarchivecmd.StatusUncertain
	if errors.Is(err, adksession.ErrNotFound) {
		outcome = chatarchivecmd.OutcomeDestroyed
	} else if err == nil && response != nil && archiveStateString(response.Session, chatarchivecmd.EpochStateKey) == intent.OldID {
		outcome = chatarchivecmd.OutcomeRetained
	}
	if err := m.archive.Complete(ctx, intent.Token, outcome); err != nil {
		return err
	}
	if outcome == chatarchivecmd.OutcomeDestroyed {
		m.mu.Lock()
		old := m.sessions[intent.Session.StableID]
		if old != nil && old.GetArchiveID() == intent.OldID {
			delete(m.sessions, intent.Session.StableID)
		} else {
			old = nil
		}
		m.mu.Unlock()
		if old != nil {
			old.runtimeDeleted = true
			if err := m.cleanupTopicSession(ctx, old, sessionCleanupOptions{}); err != nil {
				return err
			}
		}
	}
	if outcome == chatarchivecmd.StatusUncertain {
		return fmt.Errorf("runtime destruction evidence unproven: %w", chatarchivecmd.ErrConflict)
	}
	return nil
}

func runtimeAppName(value runtimeSlotValue) string {
	if value.appName != "" {
		return value.appName
	}
	return baldaRuntimeAppName
}
