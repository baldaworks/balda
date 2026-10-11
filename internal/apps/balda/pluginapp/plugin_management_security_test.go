package pluginapp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type pluginAcceptanceHook struct {
	state.PluginManagementStore
	before func(*state.PluginCommit)
}

func (s pluginAcceptanceHook) CommitPlugin(ctx context.Context, m state.PluginCommit) error {
	s.before(&m)
	return s.PluginManagementStore.CommitPlugin(ctx, m)
}

func reviewedManagementInstall(t *testing.T, s *Service, a plugincmd.Authority, root string) plugincmd.PluginMutation {
	t.Helper()
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a}); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.ManagementCandidate(t.Context(), "demo", "market", a)
	if err != nil {
		t.Fatal(err)
	}
	return plugincmd.PluginMutation{Operation: plugincmd.Install, PluginName: "demo", MarketplaceName: "market", CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState, Authority: a}
}

func TestGuardedLifecycleLateAuthorityAndAuditFailure(t *testing.T) {
	for _, failure := range []string{"revoked", "audit"} {
		t.Run(failure, func(t *testing.T) {
			s, p, a, root := newMarketplaceManagementTestService(t)
			t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
			request := reviewedManagementInstall(t, s, a, root)
			before, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			hook := pluginAcceptanceHook{PluginManagementStore: p.PluginManagement(), before: func(m *state.PluginCommit) {
				reached = true
				if failure == "audit" {
					m.AuditID = managementFixtureLoginAuditID
					return
				}
				now := time.Now().UTC()
				event := usercmd.AuditEvent{ID: "revoke-install", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: a.SessionID, Source: "test", OccurredAt: now}
				if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, now, "test", event); err != nil {
					t.Fatal(err)
				}
			}}
			activator := &recordingActivator{}
			s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, hook)
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.ManagementMutatePlugin(t.Context(), request)
			want := plugincmd.ErrUnavailable
			if failure == "revoked" {
				want = plugincmd.ErrForbidden
			}
			if !reached || !errors.Is(err, want) || result.Accepted {
				t.Fatalf("late %s reached=%v outcome=%+v error=%v", failure, reached, result, err)
			}
			after, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			if before.Token() != after.Token() {
				t.Fatal("rejected install changed revision/install/intent/generation/origin")
			}
			if len(activator.compiled) == 0 || len(activator.published) != 0 {
				t.Fatalf("prepare=%d publish=%d", len(activator.compiled), len(activator.published))
			}
		})
	}
}

type failingPluginCompletion struct {
	PluginStore
	fail bool
}

func (s *failingPluginCompletion) CompletePluginActivation(ctx context.Context, id string, at time.Time) error {
	if s.fail {
		return errors.New("synthetic completion failure")
	}
	return s.PluginStore.CompletePluginActivation(ctx, id, at)
}

func TestGuardedLifecycleAcceptedRecoveryPending(t *testing.T) {
	for _, failure := range []string{"publish", "complete"} {
		t.Run(failure, func(t *testing.T) {
			s, p, a, root := newMarketplaceManagementTestService(t)
			t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
			request := reviewedManagementInstall(t, s, a, root)
			activator := &recordingActivator{}
			store := &failingPluginCompletion{PluginStore: p.Plugins(), fail: failure == "complete"}
			if failure == "publish" {
				activator.publishErr = errors.New("synthetic publication failure")
			}
			var err error
			s, err = NewManaged(s.stateDir, p.AppKV(), store, activator, p.PluginManagement())
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.ManagementMutatePlugin(t.Context(), request)
			if !errors.Is(err, plugincmd.ErrRecoveryPending) || !result.Accepted || !result.RecoveryPending {
				t.Fatalf("outcome=%+v error=%v", result, err)
			}
			accepted, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			if accepted.Install == nil || len(accepted.Revisions) != 1 || len(accepted.Pending) != 1 {
				t.Fatalf("accepted state=%+v", accepted)
			}
			// Repeating the old request cannot publish the pending work or accept twice.
			published := len(activator.published)
			_, err = s.ManagementMutatePlugin(t.Context(), request)
			if !errors.Is(err, plugincmd.ErrConflict) || len(activator.published) != published {
				t.Fatalf("stale request recovered pending work: %v", err)
			}
			freshState, err := s.ManagementExpectedState(t.Context(), "demo", a)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			revoke := usercmd.AuditEvent{ID: "revoke-pending", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: a.SessionID, Source: "test", OccurredAt: now}
			if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, now, "test", revoke); err != nil {
				t.Fatal(err)
			}
			_, err = s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Disable, PluginName: "demo", ExpectedState: freshState, Confirm: true, Authority: a})
			if !errors.Is(err, plugincmd.ErrForbidden) || len(activator.published) != published {
				t.Fatalf("revoked fresh request recovered pending work: %v", err)
			}
			stillPending, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
			if err != nil || stillPending.Token() != accepted.Token() {
				t.Fatal("revoked fresh request changed accepted plugin state")
			}
			// A new database handle and owner perform ordinary startup recovery.
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			p, err = state.NewSQLiteProvider(t.Context(), filepath.Join(s.stateDir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			activator.publishErr = nil
			s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, p.PluginManagement())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			recovered, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Install == nil || recovered.Install.ActiveRevisionID != accepted.Install.ActiveRevisionID || len(recovered.Revisions) != 1 || len(recovered.Pending) != 0 || len(activator.published) == 0 {
				t.Fatalf("recovered state=%+v published=%d", recovered, len(activator.published))
			}
		})
	}
}

// waitingPluginActivator exposes the real catalog-lock wait without bypassing it.
type waitingPluginActivator struct {
	*recordingActivator
	waiting chan struct{}
}

func (a *waitingPluginActivator) LockCatalogMutation(ctx context.Context) (func(), error) {
	close(a.waiting)
	return a.recordingActivator.LockCatalogMutation(ctx)
}

func TestGuardedLifecycleRevokedWhileWaitingForCatalog(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	request := reviewedManagementInstall(t, s, a, root)
	activator := &waitingPluginActivator{recordingActivator: &recordingActivator{}, waiting: make(chan struct{})}
	var err error
	s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	activator.mu.Lock()
	released := false
	defer func() {
		if !released {
			activator.mu.Unlock()
		}
	}()
	result := make(chan error, 1)
	go func() { _, err := s.ManagementMutatePlugin(t.Context(), request); result <- err }()
	select {
	case <-activator.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("mutation did not reach catalog wait")
	}
	now := time.Now().UTC()
	event := usercmd.AuditEvent{ID: "revoke-waiting-install", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: a.SessionID, Source: "test", OccurredAt: now}
	if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, now, "test", event); err != nil {
		t.Fatal(err)
	}
	activator.mu.Unlock()
	released = true
	select {
	case err := <-result:
		if !errors.Is(err, plugincmd.ErrForbidden) {
			t.Fatalf("revoked waiting mutation=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mutation did not finish after catalog release")
	}
	observed, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if observed.Install != nil || observed.Generation != 0 || len(observed.Revisions) != 0 || len(observed.Pending) != 0 || len(activator.compiled) != 0 || len(activator.published) != 0 {
		t.Fatalf("revoked waiting mutation changed state: %+v", observed)
	}
}

func TestGuardedLifecyclePreparationFailureDoesNotAccept(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	request := reviewedManagementInstall(t, s, a, root)
	before, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	activator := &recordingActivator{compileErr: errors.New("synthetic catalog preparation failure")}
	s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ManagementMutatePlugin(t.Context(), request)
	if !errors.Is(err, plugincmd.ErrUnavailable) || result.Accepted || result.RecoveryPending {
		t.Fatalf("preparation failure outcome=%+v error=%v", result, err)
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token() != before.Token() || len(activator.published) != 0 {
		t.Fatal("catalog preparation failure accepted partial plugin state")
	}
}

type pluginObservationHook struct {
	state.PluginManagementStore
	observeCount int
	beforeSecond func()
}

func (s *pluginObservationHook) ObservePlugin(ctx context.Context, name string) (state.PluginObservation, error) {
	s.observeCount++
	if s.observeCount == 2 {
		s.beforeSecond()
	}
	return s.PluginManagementStore.ObservePlugin(ctx, name)
}

func TestGuardedLifecycleRevokedDuringObservationCannotRecover(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	request := reviewedManagementInstall(t, s, a, root)
	activator := &recordingActivator{publishErr: errors.New("synthetic publication failure")}
	var err error
	s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := s.ManagementMutatePlugin(t.Context(), request); !errors.Is(err, plugincmd.ErrRecoveryPending) || !result.Accepted {
		t.Fatalf("install=%+v error=%v", result, err)
	}
	expected, err := s.ManagementExpectedState(t.Context(), "demo", a)
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	published := len(activator.published)
	activator.publishErr = nil
	hook := &pluginObservationHook{PluginManagementStore: p.PluginManagement(), beforeSecond: func() {
		now := time.Now().UTC()
		event := usercmd.AuditEvent{ID: "revoke-during-observation", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: a.SessionID, Source: "test", OccurredAt: now}
		if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, now, "test", event); err != nil {
			t.Fatal(err)
		}
	}}
	s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, hook)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Disable, PluginName: "demo", ExpectedState: expected, Confirm: true, Authority: a})
	if !errors.Is(err, plugincmd.ErrForbidden) || result.Accepted {
		t.Fatalf("disable=%+v error=%v", result, err)
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token() != before.Token() || len(activator.published) != published {
		t.Fatal("revoked browser recovered pending publication")
	}
}

func TestGuardedLifecycleRejectsInterveningDurableWriter(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	install := reviewedManagementInstall(t, s, a, root)
	if _, err := s.ManagementMutatePlugin(t.Context(), install); err != nil {
		t.Fatal(err)
	}
	expected, err := s.ManagementExpectedState(t.Context(), "demo", a)
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	var writerState state.PluginObservation
	hook := &pluginObservationHook{PluginManagementStore: p.PluginManagement(), beforeSecond: func() {
		changed := *before.Install
		changed.Enabled = false
		now := time.Now().UTC()
		changed.UpdatedAt = now
		intent := state.PluginActivationIntent{IntentID: "concurrent-disable", PluginID: "demo", FromRevisionID: changed.ActiveRevisionID, ToRevisionID: changed.ActiveRevisionID, Operation: "disable", State: state.PluginActivationIntentPending, CreatedAt: now, UpdatedAt: now}
		mutation := state.PluginCommit{PluginID: "demo", ExpectedState: before.Token(), Install: &changed, Intent: intent, AuditID: "concurrent-disable-audit"}
		if err := p.PluginManagement().CommitPluginTrusted(t.Context(), mutation); err != nil {
			t.Fatal(err)
		}
		var err error
		writerState, err = p.PluginManagement().ObservePlugin(t.Context(), "demo")
		if err != nil {
			t.Fatal(err)
		}
	}}
	activator := &recordingActivator{}
	s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), activator, hook)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Disable, PluginName: "demo", ExpectedState: expected, Confirm: true, Authority: a})
	if !errors.Is(err, plugincmd.ErrConflict) || result.Accepted {
		t.Fatalf("intervening writer outcome=%+v error=%v", result, err)
	}
	after, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if after.Token() != writerState.Token() || len(activator.compiled) != 0 || len(activator.published) != 0 {
		t.Fatal("stale browser request recovered or changed intervening writer")
	}
}

func TestGuardedLifecycleBrowserOperationsPreserveSelection(t *testing.T) {
	s, p, a, root := newMarketplaceManagementTestService(t)
	t.Cleanup(func() { _ = makeTreeWritable(s.stateDir) })
	request := reviewedManagementInstall(t, s, a, root)
	if result, err := s.ManagementMutatePlugin(t.Context(), request); err != nil || !result.Accepted {
		t.Fatalf("install=%+v %v", result, err)
	}
	installed := func() state.PluginInstallRecord {
		t.Helper()
		record, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
		if err != nil || !found {
			t.Fatalf("installed=%v %v", found, err)
		}
		return record
	}
	first := installed()
	mutate := func(operation plugincmd.Operation, revision string) {
		t.Helper()
		expected, err := s.ManagementExpectedState(t.Context(), "demo", a)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: operation, PluginName: "demo", ExpectedState: expected, Revision: revision, Confirm: true, Authority: a})
		if err != nil || !result.Accepted || result.RecoveryPending {
			t.Fatalf("%s=%+v %v", operation, result, err)
		}
	}
	mutate(plugincmd.Disable, "")
	expected, err := s.ManagementExpectedState(t.Context(), "demo", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Disable, PluginName: "demo", ExpectedState: expected, Confirm: true, Authority: a}); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("no-op disable=%v", err)
	}
	writeManagedPlugin(t, root, "2.0.0", true)
	marketplace, err := s.ManagementMarketplace(t.Context(), "market", a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: marketplace.ExpectedState, Authority: a}); err != nil {
		t.Fatal(err)
	}
	candidate, err := s.ManagementCandidate(t.Context(), "demo", "", a)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ManagementMutatePlugin(t.Context(), plugincmd.PluginMutation{Operation: plugincmd.Upgrade, PluginName: "demo", CandidateToken: candidate.CandidateToken, ExpectedState: candidate.ExpectedState, Authority: a})
	if err != nil || !result.Accepted {
		t.Fatalf("upgrade=%+v %v", result, err)
	}
	second := installed()
	if second.Enabled || second.ActiveRevisionID == first.ActiveRevisionID || second.Version != "2.0.0" {
		t.Fatalf("disabled upgrade=%+v", second)
	}
	mutate(plugincmd.Rollback, first.ActiveRevisionID)
	rolledBack := installed()
	if rolledBack.Enabled || rolledBack.ActiveRevisionID != first.ActiveRevisionID {
		t.Fatalf("disabled rollback=%+v", rolledBack)
	}
	mutate(plugincmd.Enable, "")
	if enabled := installed(); !enabled.Enabled || enabled.ActiveRevisionID != first.ActiveRevisionID {
		t.Fatalf("enable=%+v", enabled)
	}
	mutate(plugincmd.Remove, "")
	observed, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if observed.Install != nil || len(observed.Revisions) != 2 || len(observed.Pending) != 0 {
		t.Fatalf("removed state=%+v", observed)
	}
	page, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[usercmd.AuditAction]int{}
	for _, event := range page.Events {
		if event.TargetType == usercmd.AuditTargetPlugin {
			counts[event.Action]++
			if event.Source != "backoffice" || event.ActorSessionID != a.SessionID {
				t.Fatalf("browser audit=%+v", event)
			}
		}
	}
	for _, action := range []usercmd.AuditAction{usercmd.AuditActionPluginInstalled, usercmd.AuditActionPluginDisabled, usercmd.AuditActionPluginUpgraded, usercmd.AuditActionPluginRolledBack, usercmd.AuditActionPluginEnabled, usercmd.AuditActionPluginRemoved} {
		if counts[action] != 1 {
			t.Errorf("%s audits=%d", action, counts[action])
		}
	}
}
