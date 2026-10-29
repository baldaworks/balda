package sessionapp

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/lifetimeapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
)

const freshLifetimeIntegrationProviderID = "provider"

type freshLifetimeRuntimeManager struct {
	runtime *baldasession.BuiltRuntime
	calls   atomic.Int32
}

type freshLifetimeCandidate struct {
	runtime  *baldasession.BuiltRuntime
	claimed  bool
	promoted bool
}

func (c *freshLifetimeCandidate) Runtime() *baldasession.BuiltRuntime { return c.runtime }
func (*freshLifetimeCandidate) ExpectedBaseToken() uint64             { return 0 }
func (c *freshLifetimeCandidate) ClaimForPublication() error          { c.claimed = true; return nil }
func (c *freshLifetimeCandidate) Promote() error {
	if !c.claimed {
		return errors.New("not claimed")
	}
	c.promoted = true
	return nil
}
func (*freshLifetimeCandidate) MarkPublished()             {}
func (*freshLifetimeCandidate) CompletePublication() error { return nil }
func (c *freshLifetimeCandidate) Abort(context.Context) error {
	if c.promoted {
		return errors.New("published")
	}
	return nil
}

func (m *freshLifetimeRuntimeManager) BuildUnpublishedSessionRuntime(_ context.Context, request baldasession.SessionRuntimeRequest) (baldasession.UnpublishedSessionRuntimeCandidate, error) {
	runtime := *m.runtime
	runtime.RuntimeBaseToken = 1
	runtime.ProviderID = freshLifetimeIntegrationProviderID
	if request.ModelOverride != nil {
		runtime.RecipeModel = *request.ModelOverride
	}
	return &freshLifetimeCandidate{runtime: &runtime}, nil
}

func (*freshLifetimeRuntimeManager) FenceSessionRuntimeBase(string) error                { return nil }
func (*freshLifetimeRuntimeManager) CompleteSessionRuntimeRecovery(string, uint64) error { return nil }
func (*freshLifetimeRuntimeManager) AdoptPromotedSessionRuntime(string, uint64, baldasession.SessionRuntimeRequest) (baldasession.UnpublishedSessionRuntimeCandidate, bool) {
	return nil, false
}

func (m *freshLifetimeRuntimeManager) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	m.calls.Add(1)
	return m.runtime, nil
}

func (*freshLifetimeRuntimeManager) ProviderID() string { return freshLifetimeIntegrationProviderID }

func TestQueuedFreshLifetimeUsesProductionBoundariesWithoutProviderControl(t *testing.T) {
	sessionService := &integrationSessionService{Service: adksession.InMemoryService()}
	agent, err := adkagent.New(adkagent.Config{Name: "incumbent-agent"})
	if err != nil {
		t.Fatal(err)
	}
	control := &integrationModelControl{currentModel: "model-current"}
	product, err := baldaagent.NewRuntimeProduct(agent, control, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := &integrationHostFactory{}
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{
		ScopedFactory:  &integrationProductFactory{product: product},
		WorkingDir:     t.TempDir(),
		SessionService: sessionService,
		RunnerFactory:  baldaagent.NewRunnerFactory(baldaagent.RunnerFactoryParams{Host: host}),
	})
	agentRuntime, err := builder.BuildRuntimeWithCapabilities(
		t.Context(), freshLifetimeIntegrationProviderID, t.TempDir(), nil, nil,
		baldaagent.SkillMetadataProjection{}, baldaagent.SessionInstructionContext{SessionID: "session"},
	)
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime.RuntimeSnapshotID = integrationRuntimeSnapshotID
	agentRuntime.ProviderBindingJSON = integrationProviderBindingJSON
	t.Cleanup(func() { _ = agentRuntime.Close() })

	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtimeManager := &freshLifetimeRuntimeManager{runtime: sessionRuntime(agentRuntime)}
	manager, err := baldasession.NewManager(baldasession.ManagerParams{
		Archive:            provider.ChatArchive(),
		AgentBuilder:       SessionAgentBuilderAdapter{Builder: builder},
		RuntimeManager:     runtimeManager,
		WorkingDir:         t.TempDir(),
		SessionsPersistent: true,
		SessionStore:       provider.Sessions(),
		RuntimeOperations:  provider.RuntimeOperations(),
		Logger:             zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	locator, err := baldasession.NewSessionLocator("telegram", "chat:topic", "{}", "session")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CreateSession(t.Context(), baldasession.SessionContext{
		Locator: locator,
		UserID:  "user",
		Kind:    baldasession.KindChat,
	}, "display-label"); err != nil {
		t.Fatal(err)
	}
	topic, err := manager.GetSession(locator)
	if err != nil {
		t.Fatal(err)
	}
	incumbent, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	oldSessionID := incumbent.SessionID()
	oldArchiveID := incumbent.ArchiveID()
	oldGeneration := incumbent.Generation()
	oldSnapshot := incumbent.RuntimeSnapshotID()
	oldBinding := incumbent.ProviderBindingJSON()
	oldModel := incumbent.EffectiveModel()
	oldAgent := incumbent.Agent()
	oldRunner := incumbent.Runner()
	oldService := incumbent.SessionService()
	oldControl := incumbent.SessionControl()
	incumbent.Release()
	sessionService.createCalls.Store(0)
	sessionService.deleteCalls.Store(0)
	runtimeCalls := runtimeManager.calls.Load()
	hostCalls := host.calls.Load()
	control.inspectCalls.Store(0)
	control.validateCalls.Store(0)
	control.changeCalls.Store(0)

	dispatcher := actors.NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	})
	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: "session",
		Run: func(context.Context) error {
			close(activeStarted)
			<-releaseActive
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-activeStarted

	application := lifetimeapp.New(dispatcher, manager)
	completion, position, err := application.Submit(t.Context(), lifetimeapp.Request{
		OperationID: "fresh-operation",
		SessionID:   "session",
		Command:     "reset",
	})
	if err != nil || position != 1 {
		t.Fatalf("Submit() position=%d err=%v", position, err)
	}
	if sessionService.createCalls.Load() != 1 {
		t.Fatalf("local candidate creates before boundary = %d, want 1", sessionService.createCalls.Load())
	}
	conflict, _, err := application.Submit(t.Context(), lifetimeapp.Request{
		OperationID: "foreign-operation",
		SessionID:   "session",
		Command:     "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-conflict; got.Result.Status != lifetimeapp.StatusConflict {
		t.Fatalf("foreign pending completion = %+v", got)
	}
	if _, found, getErr := provider.RuntimeOperations().Get(t.Context(), "foreign-operation"); getErr != nil || found {
		t.Fatalf("foreign operation mutated journal: found=%t err=%v", found, getErr)
	}
	if sessionService.createCalls.Load() != 1 || sessionService.deleteCalls.Load() != 0 {
		t.Fatalf("foreign operation mutated target: Create=%d Delete=%d", sessionService.createCalls.Load(), sessionService.deleteCalls.Load())
	}
	sessionService.failGet.Store(true)
	failedPreparation, _, err := application.Submit(t.Context(), lifetimeapp.Request{
		OperationID: "fresh-operation",
		SessionID:   "session",
		Command:     "reset",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-failedPreparation; got.Result.Status != lifetimeapp.StatusUnavailable {
		t.Fatalf("replay preparation failure = %+v", got)
	}
	sessionService.failGet.Store(false)
	if sessionService.deleteCalls.Load() != 0 {
		t.Fatalf("failed replay preparation deleted owner target %d times", sessionService.deleteCalls.Load())
	}

	rejectedReplay := lifetimeapp.New(rejectingFreshLifetimeQueue{err: appports.ErrTurnQueueFull}, manager)
	replayFailure, _, err := rejectedReplay.Submit(t.Context(), lifetimeapp.Request{
		OperationID: "fresh-operation",
		SessionID:   "session",
		Command:     "reset",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-replayFailure; got.Result.Status != lifetimeapp.StatusConflict {
		t.Fatalf("rejected replay completion = %+v", got)
	}
	operation, found, err := provider.RuntimeOperations().Get(t.Context(), "fresh-operation")
	if err != nil || !found || operation.State != baldastate.RuntimeOperationPrepared {
		t.Fatalf("owner operation after rejected replay = %+v found=%t err=%v", operation, found, err)
	}
	if _, err := sessionService.Get(t.Context(), &adksession.GetRequest{
		AppName: agentRuntime.AppName, UserID: "user", SessionID: operation.TargetArchiveID,
	}); err != nil {
		t.Fatalf("owner target after rejected replay: %v", err)
	}
	if _, err := provider.ChatArchive().Pending(t.Context(), locator.ChannelType, locator.AddressKey); err != nil {
		t.Fatalf("owner archive intent after rejected replay: %v", err)
	}
	if sessionService.deleteCalls.Load() != 0 {
		t.Fatalf("rejected replay deleted owner target %d times", sessionService.deleteCalls.Load())
	}
	before, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if before.SessionID() != oldSessionID || before.ArchiveID() != oldArchiveID || before.Generation() != oldGeneration {
		t.Fatal("incumbent changed before FIFO boundary")
	}
	before.Release()
	if _, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error { return nil }}); !errors.Is(err, appports.ErrTurnReplacementPending) {
		t.Fatalf("later work error = %v, want replacement pending", err)
	}

	close(releaseActive)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	completed := <-completion
	if completed.Result.Status != lifetimeapp.StatusReset || completed.Result.Generation != oldGeneration+1 {
		t.Fatalf("completion = %+v", completed)
	}
	current, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Release()
	if current.SessionID() == oldSessionID || current.SessionID() != current.ArchiveID() ||
		current.Generation() != oldGeneration+1 || current.RuntimeSnapshotID() != oldSnapshot ||
		current.ProviderBindingJSON() != oldBinding || current.EffectiveModel() != oldModel {
		t.Fatalf("published identity = runtime %q archive %q generation %d snapshot %q binding %q model %q",
			current.SessionID(), current.ArchiveID(), current.Generation(), current.RuntimeSnapshotID(),
			current.ProviderBindingJSON(), current.EffectiveModel())
	}
	if current.Agent() != oldAgent || current.Runner() != oldRunner || current.SessionService() != oldService ||
		current.SessionControl() != oldControl || current.Session().Events().Len() != 0 {
		t.Fatal("fresh lifetime rebuilt provider runtime or retained local context")
	}
	if runtimeManager.calls.Load() != runtimeCalls || host.calls.Load() != hostCalls {
		t.Fatalf("provider construction calls changed: runtime=%d/%d host=%d/%d",
			runtimeManager.calls.Load(), runtimeCalls, host.calls.Load(), hostCalls)
	}
	if control.inspectCalls.Load() != 0 || control.validateCalls.Load() != 0 || control.changeCalls.Load() != 0 {
		t.Fatalf("provider controls called: inspect=%d validate=%d change=%d",
			control.inspectCalls.Load(), control.validateCalls.Load(), control.changeCalls.Load())
	}
	if sessionService.deleteCalls.Load() != 0 {
		t.Fatalf("local candidate deletes = %d", sessionService.deleteCalls.Load())
	}

	replay, _, err := application.Submit(t.Context(), lifetimeapp.Request{
		OperationID: "fresh-operation",
		SessionID:   "session",
		Command:     "reset",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-replay; got.Result != completed.Result {
		t.Fatalf("terminal replay = %+v, want %+v", got, completed)
	}
	if sessionService.createCalls.Load() != 1 || runtimeManager.calls.Load() != runtimeCalls ||
		host.calls.Load() != hostCalls || sessionService.deleteCalls.Load() != 0 {
		t.Fatal("terminal replay repeated local or provider work")
	}
}

type rejectingFreshLifetimeQueue struct {
	err error
}

func (q rejectingFreshLifetimeQueue) EnqueueReplacement(context.Context, appports.ReplacementTask) (<-chan error, int, error) {
	return nil, 0, q.err
}
