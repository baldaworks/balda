package session

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

type archiveBuilder struct {
	sqliteAgentBuilder
	fail           bool
	service        adksession.Service
	model          string
	freshProviders []string
	beforeFresh    func()
}

func (b *archiveBuilder) CreateFreshRuntimeSession(ctx context.Context, runtime *BuiltRuntime, providerID, userID, sessionID, _ string, sessionCtx RuntimeSessionContext) (adksession.Session, error) {
	b.freshCalls++
	b.freshProviders = append(b.freshProviders, providerID)
	if b.beforeFresh != nil {
		b.beforeFresh()
	}
	if b.fail {
		return nil, errors.New("build failed")
	}
	result, err := runtime.SessionSvc.Create(ctx, &adksession.CreateRequest{AppName: runtime.AppName, UserID: userID, SessionID: sessionID, State: map[string]any{
		chatarchivecmd.EpochStateKey: sessionCtx.ArchiveID,
		baldaSessionIDStateKey:       sessionCtx.BaldaSessionID,
	}})
	if err != nil {
		return nil, err
	}
	return result.Session, nil
}
func (b *archiveBuilder) OpenExistingRuntimeSession(ctx context.Context, runtime *BuiltRuntime, agent, userID, sessionID, dir string, sessionCtx RuntimeSessionContext) (adksession.Session, error) {
	sess, err := b.sqliteAgentBuilder.OpenExistingRuntimeSession(ctx, runtime, agent, userID, sessionID, dir, sessionCtx)
	if err != nil {
		return nil, err
	}
	if sessionCtx.ArchiveID != "" && archiveStateString(sess, chatarchivecmd.EpochStateKey) != sessionCtx.ArchiveID {
		return nil, errors.New("epoch mismatch")
	}
	return sess, nil
}

func (b *archiveBuilder) RuntimeSessionService() adksession.Service { return b.service }

type archiveFixture struct {
	provider baldastate.Provider
	runtime  *BuiltRuntime
	builder  *archiveBuilder
	dir      string
}

type blockingAddressSessionStore struct {
	baldastate.SessionStore
	entered chan struct{}
	release chan struct{}
	calls   int
}

func waitSessionOperationRefs(t *testing.T, manager *Manager, sessionID string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		manager.operationMu.Lock()
		refs := 0
		if operation := manager.operations[sessionID]; operation != nil {
			refs = operation.refs
		}
		manager.operationMu.Unlock()
		if refs == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session operation refs = %d, want %d", refs, want)
		}
		runtime.Gosched()
	}
}

func (s *blockingAddressSessionStore) GetByAddress(ctx context.Context, channelType, addressKey string) (baldastate.SessionRecord, bool, error) {
	s.calls++
	if s.calls == 2 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return baldastate.SessionRecord{}, false, ctx.Err()
		}
	}
	return s.SessionStore.GetByAddress(ctx, channelType, addressKey)
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	p, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	service := p.RuntimeSessions()
	return &archiveFixture{
		provider: p,
		runtime:  &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: currentSnapshotID},
		builder:  &archiveBuilder{service: service},
		dir:      t.TempDir(),
	}
}
func (f *archiveFixture) manager() *Manager {
	return &Manager{runtimeManager: &fakeScopedRuntimeManager{fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: f.runtime}, sessionRuntime: f.runtime}, agentBuilder: f.builder, workingDir: f.dir, logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: f.provider.Sessions(), sessionsPersistent: true, archive: f.provider.ChatArchive(), bootID: "test-boot"}
}
func archiveInput() SessionContext {
	return SessionContext{Locator: testTelegramLocator(10, 0), UserID: ensureTestUserID}
}
func TestArchiveFreshAndExactRestore(t *testing.T) {
	f := newArchiveFixture(t)
	first, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if first.GetArchiveID() == "" {
		t.Fatal("fresh lifetime has no archive identity")
	}
	second, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetArchiveID() != first.GetArchiveID() {
		t.Fatal("exact restore split archive identity")
	}
	rows, err := f.provider.ChatArchive().List(t.Context(), chatarchivecmd.Filter{Limit: 50})
	if err != nil || len(rows) != 1 || rows[0].Status != chatarchivecmd.StatusActive {
		t.Fatalf("inventory = %#v, %v", rows, err)
	}
}

func TestEnsureSessionRetainsActivationUntilAdmissionAcknowledged(t *testing.T) {
	f := newArchiveFixture(t)
	manager := f.manager()
	first, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
	if err != nil || first.Activation == nil || first.Activation.Kind != sessioncmd.ActivationCreated {
		t.Fatalf("first ensure = %+v, %v", first.Activation, err)
	}

	retry, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
	if err != nil || retry.Activation == nil || !reflect.DeepEqual(retry.Activation, first.Activation) {
		t.Fatalf("pending replay = %+v, %v; want %+v", retry.Activation, err, first.Activation)
	}
	wrong := *first.Activation.Clone()
	wrong.Kind = sessioncmd.ActivationRestored
	if manager.AcknowledgeActivation(first.GetSessionID(), wrong) {
		t.Fatal("mismatched activation acknowledgment succeeded")
	}
	if !manager.AcknowledgeActivation(first.GetSessionID(), *first.Activation) {
		t.Fatal("activation acknowledgment failed")
	}

	reused, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
	if err != nil || reused.Activation != nil || reused.TopicSession != first.TopicSession {
		t.Fatalf("active reuse = %+v, %v", reused, err)
	}
}

func TestNonConversationalEnsureDoesNotCreateOrClearActivation(t *testing.T) {
	for _, caller := range []string{"bootstrap", "skill"} {
		t.Run(caller+" before ordinary input", func(t *testing.T) {
			f := newArchiveFixture(t)
			manager := f.manager()
			topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved")
			if err != nil || topic == nil {
				t.Fatalf("non-conversational ensure = %p, %v", topic, err)
			}
			ordinary, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
			if err != nil || ordinary.TopicSession != topic || ordinary.Activation != nil {
				t.Fatalf("ordinary reuse after %s = %+v, %v; want nil activation", caller, ordinary, err)
			}
		})
	}

	t.Run("non-conversational ensure preserves failed ordinary admission", func(t *testing.T) {
		f := newArchiveFixture(t)
		manager := f.manager()
		first, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
		if err != nil || first.Activation == nil {
			t.Fatalf("ordinary ensure = %+v, %v", first, err)
		}
		if topic, err := manager.EnsureSession(t.Context(), archiveInput(), "saved"); err != nil || topic != first.TopicSession {
			t.Fatalf("intervening non-conversational ensure = %p, %v", topic, err)
		}
		retry, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
		if err != nil || !reflect.DeepEqual(retry.Activation, first.Activation) {
			t.Fatalf("ordinary retry = %+v, %v; want %+v", retry.Activation, err, first.Activation)
		}
	})
}

func TestEnsureSessionSnapshotsActivationBeforeConcurrentAcknowledge(t *testing.T) {
	f := newArchiveFixture(t)
	manager := f.manager()
	first, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
	if err != nil || first.Activation == nil {
		t.Fatalf("first ensure = %+v, %v", first, err)
	}
	store := &blockingAddressSessionStore{
		SessionStore: manager.sessionStore,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	manager.sessionStore = store
	ensureDone := make(chan EnsureResult, 1)
	ensureErr := make(chan error, 1)
	go func() {
		result, ensureError := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
		ensureDone <- result
		ensureErr <- ensureError
	}()
	<-store.entered
	ackDone := make(chan bool, 1)
	go func() { ackDone <- manager.AcknowledgeActivation(first.GetSessionID(), *first.Activation) }()
	waitSessionOperationRefs(t, manager, first.GetSessionID(), 2)
	close(store.release)
	result := <-ensureDone
	if err := <-ensureErr; err != nil || !reflect.DeepEqual(result.Activation, first.Activation) {
		t.Fatalf("concurrent ensure = %+v, %v", result, err)
	}
	if acknowledged := <-ackDone; !acknowledged {
		t.Fatal("queued acknowledgment failed")
	}
}

func TestEnsureSessionSnapshotsActivationBeforeConcurrentReset(t *testing.T) {
	f := newArchiveFixture(t)
	manager := f.manager()
	first, err := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
	if err != nil || first.Activation == nil {
		t.Fatalf("first ensure = %+v, %v", first, err)
	}
	store := &blockingAddressSessionStore{
		SessionStore: manager.sessionStore,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	manager.sessionStore = store
	ensureDone := make(chan EnsureResult, 1)
	ensureErr := make(chan error, 1)
	go func() {
		result, ensureError := manager.EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
		ensureDone <- result
		ensureErr <- ensureError
	}()
	<-store.entered
	resetDone := make(chan error, 1)
	go func() { resetDone <- manager.ResetSession(t.Context(), archiveInput().Locator) }()
	waitSessionOperationRefs(t, manager, first.GetSessionID(), 2)
	close(store.release)
	result := <-ensureDone
	if err := <-ensureErr; err != nil || !reflect.DeepEqual(result.Activation, first.Activation) {
		t.Fatalf("concurrent ensure = %+v, %v", result, err)
	}
	if err := <-resetDone; err != nil {
		t.Fatalf("queued reset failed: %v", err)
	}
}

func TestEnsureSessionActivationUsesPublishedRuntimeReceipt(t *testing.T) {
	f := newArchiveFixture(t)
	f.runtime.RecipeModel = "model-exact"
	f.runtime.PublicRuntimeMetadata = sessioncmd.PublicRuntimeMetadata{
		ProviderID:      "provider-exact",
		ProviderType:    "codex-acp",
		ReasoningEffort: "high",
		MCPServers:      []string{"alpha", "beta"},
	}

	result, err := f.manager().EnsureSessionForTurn(t.Context(), archiveInput(), "saved")
	if err != nil || result.Activation == nil {
		t.Fatalf("EnsureSession() = %+v, %v", result, err)
	}
	f.runtime.PublicRuntimeMetadata.ProviderID = "mutated-global-provider"
	f.runtime.PublicRuntimeMetadata.MCPServers[0] = "mutated-global-mcp"
	want := sessioncmd.RuntimeSummary{
		PublicRuntimeMetadata: sessioncmd.PublicRuntimeMetadata{
			ProviderID:      "provider-exact",
			ProviderType:    "codex-acp",
			ReasoningEffort: "high",
			MCPServers:      []string{"alpha", "beta"},
		},
		EffectiveModel: "model-exact",
	}
	if !reflect.DeepEqual(result.Activation.Runtime, want) {
		t.Fatalf("activation runtime = %+v, want %+v", result.Activation.Runtime, want)
	}
}
func TestArchiveLazyAdoption(t *testing.T) {
	f := newArchiveFixture(t)
	old := f.manager()
	old.archive = nil
	prior, err := old.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.provider.ChatArchive().List(t.Context(), chatarchivecmd.Filter{Limit: 50})
	if err != nil || len(rows) != 0 {
		t.Fatalf("unprepared inventory = %#v, %v", rows, err)
	}
	adopted, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.GetAgentSessionID() != prior.GetAgentSessionID() || f.builder.freshCalls != 1 {
		t.Fatal("adoption replaced existing runtime")
	}
	row, err := f.provider.ChatArchive().Get(t.Context(), adopted.GetArchiveID())
	if err != nil || !row.Partial {
		t.Fatalf("adopted row = %#v, %v", row, err)
	}
	restored, err := f.manager().EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil || restored.GetArchiveID() != adopted.GetArchiveID() {
		t.Fatalf("adopted restore = %#v, %v", restored, err)
	}
}

type archiveFault struct {
	ArchiveLifecycle
	beginErr, commitErr error
}

func (a *archiveFault) Begin(ctx context.Context, i chatarchivecmd.Intent) (chatarchivecmd.Intent, error) {
	if a.beginErr != nil {
		return chatarchivecmd.Intent{}, a.beginErr
	}
	return a.ArchiveLifecycle.Begin(ctx, i)
}
func (a *archiveFault) Activate(ctx context.Context, token string, r *baldastate.SessionRecord) (chatarchivecmd.Session, error) {
	if a.commitErr != nil {
		return chatarchivecmd.Session{}, a.commitErr
	}
	return a.ArchiveLifecycle.Activate(ctx, token, r)
}

type archiveHooks struct {
	calls int
	fail  bool
}

func (h *archiveHooks) Run(_ context.Context, e hookcmd.Event) (hookcmd.Result, error) {
	if e.Name == hookcmd.SessionStart {
		h.calls++
		if h.fail {
			return hookcmd.Result{}, errors.New("hook failed")
		}
	}
	return hookcmd.Result{}, nil
}
func TestArchiveCommitRecoveryDoesNotRepeatHooks(t *testing.T) {
	f := newArchiveFixture(t)
	hooks := &archiveHooks{}
	fault := &archiveFault{ArchiveLifecycle: f.provider.ChatArchive(), commitErr: chatarchivecmd.ErrUnavailable}
	manager := f.manager()
	manager.archive = fault
	manager.hooks = hooks
	if _, err := manager.EnsureSession(t.Context(), archiveInput(), "saved"); err == nil {
		t.Fatal("commit failure accepted")
	}
	if _, err := manager.GetSession(archiveInput().Locator); err == nil {
		t.Fatal("uncommitted runtime exposed")
	}
	pending, err := f.provider.ChatArchive().Pending(t.Context(), "telegram", "10:0")
	if err != nil {
		t.Fatal(err)
	}
	next := f.manager()
	next.hooks = hooks
	recovered, err := next.EnsureSession(t.Context(), archiveInput(), "saved")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.GetArchiveID() != pending.Session.ID || hooks.calls != 1 || f.builder.freshCalls != 1 {
		t.Fatalf("recovery identity/hooks/creates = %q/%d/%d", recovered.GetArchiveID(), hooks.calls, f.builder.freshCalls)
	}
}

func TestArchiveFailureDoesNotActivate(t *testing.T) {
	for _, kind := range []string{"reservation", "build", "hook"} {
		t.Run(kind, func(t *testing.T) {
			f := newArchiveFixture(t)
			manager := f.manager()
			switch kind {
			case "reservation":
				manager.archive = &archiveFault{ArchiveLifecycle: manager.archive, beginErr: chatarchivecmd.ErrUnavailable}
			case "build":
				f.builder.fail = true
			case "hook":
				manager.hooks = &archiveHooks{fail: true}
			}
			if _, err := manager.EnsureSession(t.Context(), archiveInput(), "saved"); err == nil {
				t.Fatal("failed activation accepted")
			}
			rows, err := f.provider.ChatArchive().List(t.Context(), chatarchivecmd.Filter{Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.Status == chatarchivecmd.StatusActive {
					t.Fatalf("failed activation active: %#v", row)
				}
			}
			if kind == "reservation" && f.builder.freshCalls != 0 {
				t.Fatal("runtime created without journal")
			}
		})
	}
}
func TestArchiveRecoveryRequiresReadyMatchingEpoch(t *testing.T) {
	for _, key := range []string{chatarchivecmd.EpochStateKey, chatarchivecmd.ReadyStateKey} {
		t.Run(key, func(t *testing.T) {
			f := newArchiveFixture(t)
			hooks := &archiveHooks{}
			manager := f.manager()
			manager.hooks = hooks
			manager.archive = &archiveFault{ArchiveLifecycle: manager.archive, commitErr: chatarchivecmd.ErrUnavailable}
			if _, err := manager.EnsureSession(t.Context(), archiveInput(), "saved"); err == nil {
				t.Fatal("commit failure accepted")
			}
			runtime, err := f.runtime.SessionSvc.Get(t.Context(), &adksession.GetRequest{AppName: baldaRuntimeAppName, UserID: ensureTestUserID, SessionID: archiveInput().Locator.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			event := adksession.NewEvent(t.Context(), "contradiction")
			event.Author = archiveStateAuthor
			event.Actions.StateDelta = map[string]any{key: "unproven"}
			if err := f.runtime.SessionSvc.AppendEvent(t.Context(), runtime.Session, event); err != nil {
				t.Fatal(err)
			}
			next := f.manager()
			next.hooks = hooks
			if _, err := next.EnsureSession(t.Context(), archiveInput(), "saved"); err == nil {
				t.Fatal("unproven readiness accepted")
			}
			if f.builder.freshCalls != 1 || hooks.calls != 1 {
				t.Fatal("repair repeated runtime creation or hooks")
			}
		})
	}
}

func TestArchiveFailedResumeHookDoesNotReplay(t *testing.T) {
	for _, adopt := range []bool{false, true} {
		t.Run(fmt.Sprintf("adopt=%t", adopt), func(t *testing.T) {
			f := newArchiveFixture(t)
			original := f.manager()
			if adopt {
				original.archive = nil
			}
			if _, err := original.EnsureSession(t.Context(), archiveInput(), "saved"); err != nil {
				t.Fatal(err)
			}
			hooks := &archiveHooks{fail: true}
			manager := f.manager()
			manager.hooks = hooks
			if _, err := manager.EnsureSession(t.Context(), archiveInput(), "saved"); err == nil {
				t.Fatal("failed resume hook accepted")
			}
			retry := f.manager()
			retry.hooks = hooks
			if _, err := retry.EnsureSession(t.Context(), archiveInput(), "saved"); err == nil {
				t.Fatal("uncertain resume hook accepted on retry")
			}
			if hooks.calls != 1 {
				t.Fatalf("resume hook repeated %d times", hooks.calls)
			}
		})
	}
}
