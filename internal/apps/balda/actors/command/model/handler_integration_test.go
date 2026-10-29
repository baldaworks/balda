package model

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/modelapp"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	"github.com/baldaworks/runtime/v2/agentproduct"
	"github.com/rs/zerolog"
	adksession "google.golang.org/adk/v2/session"
)

const integrationProviderBinding = `{"RootProviderID":"provider","Entries":[{"ProviderID":"provider","Source":"managed","RevisionID":"revision"}],"Reproducible":true,"Warnings":null}`

type integrationBuilder struct {
	service adksession.Service
	creates atomic.Int32
}

func (b *integrationBuilder) CreateRuntimeSession(ctx context.Context, runtime *session.BuiltRuntime, _ string, userID, sessionID, _ string, _ session.RuntimeSessionContext) (adksession.Session, error) {
	return b.create(ctx, runtime, userID, sessionID)
}

func (b *integrationBuilder) CreateFreshRuntimeSession(ctx context.Context, runtime *session.BuiltRuntime, _ string, userID, sessionID, _ string, _ session.RuntimeSessionContext) (adksession.Session, error) {
	return b.create(ctx, runtime, userID, sessionID)
}

func (b *integrationBuilder) OpenExistingRuntimeSession(ctx context.Context, runtime *session.BuiltRuntime, _ string, userID, sessionID, _ string, _ session.RuntimeSessionContext) (adksession.Session, error) {
	result, err := runtime.SessionSvc.Get(ctx, &adksession.GetRequest{AppName: runtime.AppName, UserID: userID, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return result.Session, nil
}

func (b *integrationBuilder) GetAgentMetadata(string) session.AgentMetadata {
	return session.AgentMetadata{Model: "model-current"}
}

func (b *integrationBuilder) create(ctx context.Context, runtime *session.BuiltRuntime, userID, sessionID string) (adksession.Session, error) {
	b.creates.Add(1)
	result, err := runtime.SessionSvc.Create(ctx, &adksession.CreateRequest{AppName: runtime.AppName, UserID: userID, SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	return result.Session, nil
}

type integrationRuntimeManager struct{ runtime *session.BuiltRuntime }

func (m integrationRuntimeManager) Runtime(context.Context) (*session.BuiltRuntime, error) {
	return m.runtime, nil
}

func (integrationRuntimeManager) ProviderID() string { return "provider" }

func (m integrationRuntimeManager) RuntimeForSession(context.Context, session.SessionRuntimeRequest) (*session.BuiltRuntime, error) {
	runtime := *m.runtime
	return &runtime, nil
}

type integrationModelController struct {
	mu            sync.Mutex
	order         []string
	validateCalls atomic.Int32
	changeCalls   atomic.Int32
}

func (*integrationModelController) InspectSessionControl(context.Context, session.RuntimeSessionIdentity) agentproduct.SessionControlSnapshot {
	return agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: "model-next"}
}

func (c *integrationModelController) ValidateModel(_ context.Context, _ session.RuntimeSessionIdentity, model string) agentproduct.ModelValidation {
	c.validateCalls.Add(1)
	return agentproduct.ModelValidation{Status: agentproduct.ModelSupported, Model: model}
}

func (c *integrationModelController) ChangeModel(_ context.Context, _ session.RuntimeSessionIdentity, model string) session.RuntimeModelChange {
	c.changeCalls.Add(1)
	c.appendOrder("model")
	return session.RuntimeModelChange{Status: agentproduct.ModelApplied, EffectiveModel: model}
}

func (c *integrationModelController) appendOrder(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order = append(c.order, value)
}

func (c *integrationModelController) snapshotOrder() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

type observingSubmitter struct {
	service   *modelapp.Service
	submitted chan struct{}
	once      sync.Once
	mu        sync.Mutex
	requests  []modelapp.Request
}

func (s *observingSubmitter) Submit(ctx context.Context, request modelapp.Request) (<-chan modelapp.Completion, int, error) {
	completion, position, err := s.service.Submit(ctx, request)
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	s.once.Do(func() { close(s.submitted) })
	return completion, position, err
}

func (s *observingSubmitter) snapshot() []modelapp.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]modelapp.Request(nil), s.requests...)
}

func TestHandlerUsesProductionModelFIFOAndIdempotentManager(t *testing.T) {
	const targetModel = "model-next"

	manager, builder, control := integrationManager(t)
	dispatcher := actors.NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: testSessionID,
		Run: func(context.Context) error {
			close(activeStarted)
			<-releaseActive
			control.appendOrder("active")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-activeStarted

	models := &observingSubmitter{service: modelapp.New(dispatcher, manager), submitted: make(chan struct{})}
	delivery := &testDispatcher{}
	handler := New(manager, models, delivery)
	payload := modelPayload(targetModel)
	done := make(chan error, 1)
	go func() {
		done <- handler.Handle(context.Background(), actorEnvelope("model-command"), payload)
	}()
	<-models.submitted
	later, position, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: testSessionID,
		Run: func(context.Context) error {
			control.appendOrder("later")
			return nil
		},
	})
	if err != nil || position != 2 {
		t.Fatalf("enqueue later position=%d err=%v", position, err)
	}
	close(releaseActive)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-later; err != nil {
		t.Fatal(err)
	}
	if got := control.snapshotOrder(); !reflect.DeepEqual(got, []string{"active", "model", "later"}) {
		t.Fatalf("execution order = %v", got)
	}
	if control.validateCalls.Load() != 1 || control.changeCalls.Load() != 1 {
		t.Fatalf("provider calls validate=%d change=%d", control.validateCalls.Load(), control.changeCalls.Load())
	}
	if builder.creates.Load() != 1 {
		t.Fatalf("runtime session creates = %d, want initial create only", builder.creates.Load())
	}

	const replays = 24
	var wg sync.WaitGroup
	for range replays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := handler.Handle(context.Background(), actorEnvelope("model-command"), payload); err != nil {
				t.Errorf("replay Handle() = %v", err)
			}
		}()
	}
	wg.Wait()
	if control.validateCalls.Load() != 1 || control.changeCalls.Load() != 1 || builder.creates.Load() != 1 {
		t.Fatalf("replay repeated work: validate=%d change=%d create=%d", control.validateCalls.Load(), control.changeCalls.Load(), builder.creates.Load())
	}
	for _, request := range models.snapshot() {
		if request.SessionID != testSessionID || request.Model != targetModel || request.OperationID != modelOperationID("model-command") {
			t.Fatalf("cross-session or unstable request = %+v", request)
		}
	}
	info, err := manager.GetSessionInfo(t.Context(), testSessionID)
	if err != nil || info.Model != targetModel || info.RequestedModel == nil || *info.RequestedModel != targetModel || info.Generation != 1 {
		t.Fatalf("committed model info = %+v, %v", info, err)
	}
}

func integrationManager(t *testing.T) (*session.Manager, *integrationBuilder, *integrationModelController) {
	t.Helper()
	provider, err := baldastate.NewSQLiteProvider(t.Context(), t.TempDir()+"/state.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	service := adksession.InMemoryService()
	control := &integrationModelController{}
	runtime := &session.BuiltRuntime{
		ModelController:     control,
		SessionSvc:          service,
		AppName:             "balda-test",
		RuntimeSnapshotID:   "snapshot",
		ProviderBindingJSON: integrationProviderBinding,
	}
	builder := &integrationBuilder{service: service}
	manager, err := session.NewManager(session.ManagerParams{
		AgentBuilder:       builder,
		RuntimeManager:     integrationRuntimeManager{runtime: runtime},
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
	locator, err := session.NewSessionLocator("telegram", "chat:topic", `{}`, testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CreateSession(t.Context(), session.SessionContext{Locator: locator, UserID: "user", Kind: session.KindChat}, "balda"); err != nil {
		t.Fatal(err)
	}
	return manager, builder, control
}

func actorEnvelope(id string) actorlayer.Envelope { return actorlayer.Envelope{ID: id} }
