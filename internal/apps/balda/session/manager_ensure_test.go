package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/agentcmd"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

const (
	ensureTestUserID = "tg-101"
	ensureTestAuthor = "user"
)

func TestCreateSessionRejectsServiceKindWithoutOwnerBeforeRuntime(t *testing.T) {
	t.Parallel()

	builder := &fakeAgentBuilder{}
	manager := &Manager{
		agentBuilder: builder,
		logger:       zerolog.Nop(),
		sessions:     make(map[string]*TopicSession),
	}
	err := manager.CreateSession(t.Context(), SessionContext{
		Locator: testTelegramLocator(10, 0),
		UserID:  ensureTestUserID,
		Kind:    KindSchedule,
	}, "balda")
	if err == nil || !strings.Contains(err.Error(), "service session requires an owner job") {
		t.Fatalf("error = %v, want missing service owner", err)
	}
	if builder.freshRuntimeSessionCalls != 0 {
		t.Fatal("created runtime after rejecting execution context")
	}
}

func TestEnsureSessionRejectsUnavailableProviderBeforeDurableMutation(t *testing.T) {
	t.Parallel()

	runtimeManager := &fakeAvailabilityRuntimeManager{
		fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{runtime: &BuiltRuntime{}},
		availabilityErr:         errors.Join(agentcmd.ErrProviderUnavailable, errors.New("managed provider invalid")),
	}
	workspaces := &mutationRecordingWorkspace{}
	archive := &mutationRecordingArchive{}
	store := &fakeSessionStore{}
	manager := &Manager{
		archive:          archive,
		agentBuilder:     &fakeAgentBuilder{},
		runtimeManager:   runtimeManager,
		workingDir:       t.TempDir(),
		workspaces:       workspaces,
		workspaceEnabled: true,
		sessionStore:     store,
		logger:           zerolog.Nop(),
		sessions:         make(map[string]*TopicSession),
	}

	_, err := manager.EnsureSession(t.Context(), SessionContext{
		Locator: testTelegramLocator(10, 0),
		UserID:  ensureTestUserID,
	}, "balda")
	if !errors.Is(err, agentcmd.ErrProviderUnavailable) {
		t.Fatalf("EnsureSession() error = %v, want provider unavailable", err)
	}
	if runtimeManager.availabilityCalls != 1 || runtimeManager.runtimeCalls != 0 {
		t.Fatalf("availability/runtime calls = %d/%d, want 1/0", runtimeManager.availabilityCalls, runtimeManager.runtimeCalls)
	}
	if workspaces.mutations != 0 || archive.mutations != 0 || len(store.upsertedRecords) != 0 || len(manager.sessions) != 0 {
		t.Fatalf("mutations: workspace=%d archive=%d records=%d sessions=%d", workspaces.mutations, archive.mutations, len(store.upsertedRecords), len(manager.sessions))
	}
}

type fakeAvailabilityRuntimeManager struct {
	fakeBaldaRuntimeManager
	availabilityErr   error
	availabilityCalls int
}

func (m *fakeAvailabilityRuntimeManager) EnsureProviderAvailable(context.Context) error {
	m.availabilityCalls++
	return m.availabilityErr
}

type mutationRecordingWorkspace struct{ mutations int }

func (m *mutationRecordingWorkspace) CanonicalWorkspaceDir(string) string { return "workspace" }
func (m *mutationRecordingWorkspace) ForceRemountCanonicalWorkspace(context.Context, string, string) (EnsureWorkspaceResult, error) {
	m.mutations++
	return EnsureWorkspaceResult{}, nil
}
func (m *mutationRecordingWorkspace) EnsureWorkspace(context.Context, string, string, string) (EnsureWorkspaceResult, error) {
	m.mutations++
	return EnsureWorkspaceResult{}, nil
}
func (m *mutationRecordingWorkspace) Import(context.Context, string) error { m.mutations++; return nil }
func (m *mutationRecordingWorkspace) Export(context.Context, string, string, string) error {
	m.mutations++
	return nil
}
func (m *mutationRecordingWorkspace) CleanupWorkspace(context.Context, string) error {
	m.mutations++
	return nil
}
func (m *mutationRecordingWorkspace) DeleteBranch(context.Context, string) error {
	m.mutations++
	return nil
}

type mutationRecordingArchive struct{ mutations int }

func (*mutationRecordingArchive) ReconcileMemory(context.Context, string) error { return nil }
func (a *mutationRecordingArchive) Begin(context.Context, chatarchivecmd.Intent) (chatarchivecmd.Intent, error) {
	a.mutations++
	return chatarchivecmd.Intent{}, nil
}
func (a *mutationRecordingArchive) Activate(context.Context, string, *baldastate.SessionRecord) (chatarchivecmd.Session, error) {
	a.mutations++
	return chatarchivecmd.Session{}, nil
}
func (a *mutationRecordingArchive) Complete(context.Context, string, string) error {
	a.mutations++
	return nil
}
func (a *mutationRecordingArchive) Pending(context.Context, string, string) (chatarchivecmd.Intent, error) {
	a.mutations++
	return chatarchivecmd.Intent{}, chatarchivecmd.ErrNotFound
}
func (a *mutationRecordingArchive) Get(context.Context, string) (chatarchivecmd.Session, error) {
	a.mutations++
	return chatarchivecmd.Session{}, chatarchivecmd.ErrNotFound
}

func TestEnsureSessionRestoresSQLiteHistoryAfterRestart(t *testing.T) {
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime := &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: provider.RuntimeSessions(), RuntimeSnapshotID: currentSnapshotID}
	builder := &sqliteAgentBuilder{}
	runtimeManager := &fakeScopedRuntimeManager{fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: runtime}, sessionRuntime: runtime}
	workingDir := t.TempDir()
	newManager := func() *Manager {
		return &Manager{runtimeManager: runtimeManager, agentBuilder: builder, workingDir: workingDir, logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: provider.Sessions(), sessionsPersistent: true}
	}
	locator := testTelegramLocator(10, 0)
	first, err := newManager().EnsureSession(t.Context(), SessionContext{Locator: locator, UserID: ensureTestUserID}, "saved-label")
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "saved-turn")
	event.Author = ensureTestAuthor
	event.Actions.StateDelta = map[string]any{"saved_value": "keep"}
	if err := runtime.SessionSvc.AppendEvent(t.Context(), testRuntimeSession(first), event); err != nil {
		t.Fatal(err)
	}
	inbound := locator
	restored, err := newManager().EnsureSession(t.Context(), SessionContext{Locator: inbound, UserID: "tg-requester"}, "new-label")
	if err != nil {
		t.Fatal(err)
	}
	if restored.GetSessionID() != locator.SessionID || restored.GetUserID() != ensureTestUserID || restored.GetAgentName() != "saved-label" {
		t.Fatalf("restored identity = %q/%q/%q", restored.GetSessionID(), restored.GetUserID(), restored.GetAgentName())
	}
	if restored.GetAgentSessionID() != first.GetAgentSessionID() {
		t.Fatalf("restored provider identity = %q, want original %q", restored.GetAgentSessionID(), first.GetAgentSessionID())
	}
	if restored.GetRuntimeSnapshotID() != currentSnapshotID {
		t.Fatalf("snapshot = %q", restored.GetRuntimeSnapshotID())
	}
	if got := testRuntimeSession(restored).Events().Len(); got != 1 {
		t.Fatalf("history events = %d, want 1", got)
	}
	if got, err := testRuntimeSession(restored).State().Get("saved_value"); err != nil || got != "keep" {
		t.Fatalf("saved state = %v, %v", got, err)
	}
	inbound.SessionID = "new-inbound-id"
	canonical, err := newManager().EnsureSession(t.Context(), SessionContext{Locator: inbound, UserID: "tg-requester"}, "new-label")
	if err != nil {
		t.Fatal(err)
	}
	if canonical.GetSessionID() != locator.SessionID || testRuntimeSession(canonical).Events().Len() != 1 {
		t.Fatal("saved identity/history lost for changed inbound ID")
	}

	if builder.freshCalls != 1 {
		t.Fatalf("fresh calls = %d, want initial create only", builder.freshCalls)
	}
}

func TestEnsureSessionRejectsMetadataFailure(t *testing.T) {
	storeErr := errors.New("storage unavailable")
	builder := &fakeAgentBuilder{}
	manager := &Manager{logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: &fakeSessionStore{getByAddressErr: storeErr}, agentBuilder: builder}
	_, err := manager.EnsureSession(t.Context(), SessionContext{Locator: testTelegramLocator(10, 0), UserID: ensureTestUserID}, "balda")
	if !errors.Is(err, storeErr) {
		t.Fatalf("error = %v, want storage failure", err)
	}
	if builder.freshRuntimeSessionCalls != 0 {
		t.Fatal("created session after metadata read failure")
	}
}

func TestEnsureSessionConcurrentSQLiteCreation(t *testing.T) {
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime := &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: provider.RuntimeSessions(), RuntimeSnapshotID: currentSnapshotID}
	builder := &sqliteAgentBuilder{}
	manager := &Manager{runtimeManager: &fakeScopedRuntimeManager{fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: runtime}, sessionRuntime: runtime}, agentBuilder: builder, workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: provider.Sessions(), sessionsPersistent: true}
	const callers = 8
	start := make(chan struct{})
	results := make(chan *TopicSession, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			<-start
			sess, err := manager.EnsureSession(t.Context(), SessionContext{Locator: testTelegramLocator(10, 0), UserID: ensureTestUserID}, "balda")
			results <- sess
			errs <- err
		})
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	var first *TopicSession
	for sess := range results {
		if first == nil {
			first = sess
		}
		if sess != first {
			t.Errorf("session = %p, want %p", sess, first)
		}
	}
	if builder.freshCalls != 1 {
		t.Fatalf("fresh calls = %d, want 1", builder.freshCalls)
	}
}

func TestEnsureSessionRecoversFailedSQLiteRestore(t *testing.T) {
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	service := provider.RuntimeSessions()
	locator := testTelegramLocator(10, 0)
	old, err := service.Create(t.Context(), &adksession.CreateRequest{AppName: baldaRuntimeAppName, UserID: ensureTestUserID, SessionID: locator.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "old-turn")
	event.Author = ensureTestAuthor
	if err := service.AppendEvent(t.Context(), old.Session, event); err != nil {
		t.Fatal(err)
	}
	record := baldastate.SessionRecord{SessionID: locator.SessionID, UserID: ensureTestUserID, ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON, AgentName: "saved", RuntimeSnapshotID: "unavailable-snapshot", Status: baldastate.SessionStatusActive}
	if err := provider.Sessions().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	runtime := &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: service, RuntimeSnapshotID: currentSnapshotID}
	builder := &sqliteAgentBuilder{}
	observer := &fakeBoundaryObserver{}
	manager := &Manager{runtimeManager: &fakeScopedRuntimeManager{fakeBaldaRuntimeManager: fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: runtime}, runtimeForSession: func(request SessionRuntimeRequest) (*BuiltRuntime, error) {
		if request.RuntimeSnapshotID != "" {
			return nil, errors.New("saved capabilities unavailable")
		}
		return runtime, nil
	}}, agentBuilder: builder, workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: provider.Sessions(), sessionsPersistent: true, boundaryObserver: observer}
	prepared, err := manager.EnsureSession(t.Context(), SessionContext{Locator: locator, UserID: ensureTestUserID}, "new")
	if err != nil {
		t.Fatal(err)
	}
	if testRuntimeSession(prepared).Events().Len() != 0 || prepared.GetRuntimeSnapshotID() != currentSnapshotID {
		t.Fatal("fallback did not create fresh current runtime")
	}
	saved, found, err := provider.Sessions().GetByAddress(t.Context(), locator.ChannelType, locator.AddressKey)
	if err != nil || !found || saved.RuntimeSnapshotID != currentSnapshotID {
		t.Fatalf("saved fallback = %+v, %v, %v", saved, found, err)
	}
	if len(observer.boundaries) != 1 || observer.boundaries[0].Reason != BoundaryReasonRotation {
		t.Fatalf("boundaries = %+v", observer.boundaries)
	}
}

func TestEnsureSessionRejectsCanceledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	manager := &Manager{logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: &fakeSessionStore{}}
	if _, err := manager.EnsureSession(ctx, SessionContext{Locator: testTelegramLocator(10, 0), UserID: ensureTestUserID}, "balda"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
}

func TestEnsureSessionMemoryRestoreUsesPreparedADKIdentity(t *testing.T) {
	store := &fakeSessionStore{recordsByAddress: make(map[string]baldastate.SessionRecord)}
	newManager := func() *Manager {
		runtime := &BuiltRuntime{AppName: baldaRuntimeAppName, SessionSvc: adksession.InMemoryService(), RuntimeSnapshotID: currentSnapshotID}
		return &Manager{runtimeManager: &fakeBaldaRuntimeManager{providerID: "balda-provider", runtime: runtime}, agentBuilder: &sqliteAgentBuilder{}, workingDir: t.TempDir(), logger: zerolog.Nop(), sessions: make(map[string]*TopicSession), sessionStore: store}
	}
	locator := testTelegramLocator(10, 0)
	if _, err := newManager().EnsureSession(t.Context(), SessionContext{Locator: locator, UserID: ensureTestUserID}, "balda"); err != nil {
		t.Fatal(err)
	}
	restored, err := newManager().EnsureSession(t.Context(), SessionContext{Locator: locator, UserID: ensureTestUserID}, "balda")
	if err != nil {
		t.Fatal(err)
	}
	if restored.GetAgentSessionID() != testRuntimeSession(restored).ID() {
		t.Fatalf("published ADK identity = %q, actual = %q", restored.GetAgentSessionID(), testRuntimeSession(restored).ID())
	}
}
