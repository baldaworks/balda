package sessionapp

import (
	"context"
	"iter"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/modelapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/agentproduct"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/plugin"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const integrationRuntimeSnapshotID = "snapshot"

type integrationSessionService struct {
	adksession.Service
	createCalls atomic.Int32
	deleteCalls atomic.Int32
}

func (s *integrationSessionService) Create(ctx context.Context, request *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	s.createCalls.Add(1)
	return s.Service.Create(ctx, request)
}

func (s *integrationSessionService) Delete(ctx context.Context, request *adksession.DeleteRequest) error {
	s.deleteCalls.Add(1)
	return s.Service.Delete(ctx, request)
}

type integrationModelControl struct {
	replacement   *agentproduct.Product
	currentModel  string
	inspectCalls  atomic.Int32
	validateCalls atomic.Int32
	changeCalls   atomic.Int32
}

func (c *integrationModelControl) Inspect(context.Context, adksession.Session) agentproduct.SessionControlSnapshot {
	c.inspectCalls.Add(1)
	return agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady, CurrentModel: c.currentModel}
}

func (c *integrationModelControl) ValidateModel(_ context.Context, _ adksession.Session, model string) agentproduct.ModelValidation {
	c.validateCalls.Add(1)
	return agentproduct.ModelValidation{Status: agentproduct.ModelSupported, Model: model}
}

func (c *integrationModelControl) ChangeModel(_ context.Context, _ adksession.Session, model string) agentproduct.ModelChange {
	c.changeCalls.Add(1)
	return agentproduct.ModelChange{Status: agentproduct.ModelReplacement, EffectiveModel: model, Replacement: c.replacement}
}

func (*integrationModelControl) FastForward(context.Context, adksession.Session, agentproduct.ContextPackage) agentproduct.ContextChange {
	return agentproduct.UnsupportedFastForward()
}

type integrationProductFactory struct {
	product *baldaagent.RuntimeProduct
}

func (f *integrationProductFactory) BuildScoped(context.Context, agentfactory.BuildRequest, map[string][]string) (adkagent.Agent, error) {
	return f.product.Agent, nil
}

func (f *integrationProductFactory) BuildSessionProduct(context.Context, agentfactory.BuildRequest, map[string][]string, string) (*baldaagent.RuntimeProduct, error) {
	return f.product, nil
}

func (f *integrationProductFactory) WithProviders(map[string]agentconfig.Config) baldaagent.ScopedRuntimeFactory {
	return f
}

type integrationRuntimeManager struct{ runtime *baldasession.BuiltRuntime }

func (m integrationRuntimeManager) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	return m.runtime, nil
}

func (integrationRuntimeManager) ProviderID() string { return "provider" }

type integrationScopedRuntimeManager struct {
	runtime    *baldaagent.BuiltRuntime
	closeCalls atomic.Int32
	mu         sync.Mutex
	requests   []baldasession.SessionRuntimeRequest
}

func (m *integrationScopedRuntimeManager) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	return sessionRuntime(m.runtime), nil
}

func (*integrationScopedRuntimeManager) ProviderID() string { return "provider" }

func (m *integrationScopedRuntimeManager) RuntimeForSession(_ context.Context, request baldasession.SessionRuntimeRequest) (*baldasession.BuiltRuntime, error) {
	m.mu.Lock()
	m.requests = append(m.requests, request)
	m.mu.Unlock()
	runtime := sessionRuntime(m.runtime)
	runtime.Close = func() error {
		m.closeCalls.Add(1)
		return nil
	}
	return runtime, nil
}

func (m *integrationScopedRuntimeManager) sessionRequests() []baldasession.SessionRuntimeRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]baldasession.SessionRuntimeRequest(nil), m.requests...)
}

type integrationHostFactory struct{ calls atomic.Int32 }

func (f *integrationHostFactory) NewToolPlugin(baldaagent.ToolHookScope) (*plugin.Plugin, error) {
	f.calls.Add(1)
	return plugin.New(plugin.Config{Name: "integration-host"})
}

func TestQueuedHostedModelChangeUsesProductionBoundaries(t *testing.T) {
	baseService := adksession.InMemoryService()
	service := &integrationSessionService{Service: baseService}
	var sawPriorEvent atomic.Bool
	var sawPriorState atomic.Bool
	replacementAgent, err := adkagent.New(adkagent.Config{
		Name: "replacement-agent",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				if value, stateErr := ctx.Session().State().Get("retained"); stateErr == nil && value == "state" {
					sawPriorState.Store(true)
				}
				for event := range ctx.Session().Events().All() {
					if event.Content != nil && len(event.Content.Parts) > 0 && event.Content.Parts[0].Text == "prior event" {
						sawPriorEvent.Store(true)
					}
				}
				response := adksession.NewEvent(ctx, ctx.InvocationID())
				response.Author = "replacement-agent"
				response.Content = genai.NewContentFromText("replacement response", genai.RoleModel)
				yield(response, nil)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := agentproduct.New(replacementAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	incumbentAgent, err := adkagent.New(adkagent.Config{Name: "incumbent-agent"})
	if err != nil {
		t.Fatal(err)
	}
	control := &integrationModelControl{replacement: replacement}
	product, err := baldaagent.NewRuntimeProduct(incumbentAgent, control, nil)
	if err != nil {
		t.Fatal(err)
	}
	host := &integrationHostFactory{}
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{
		ScopedFactory: &integrationProductFactory{product: product},
		WorkingDir:    t.TempDir(), SessionService: service,
		RunnerFactory: baldaagent.NewRunnerFactory(baldaagent.RunnerFactoryParams{Host: host}),
	})
	agentRuntime, err := builder.BuildRuntimeWithCapabilities(
		t.Context(), "provider", t.TempDir(), nil, nil,
		baldaagent.SkillMetadataProjection{}, baldaagent.SessionInstructionContext{SessionID: "session"},
	)
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime.RuntimeSnapshotID = integrationRuntimeSnapshotID
	agentRuntime.ProviderBindingJSON = `{"RootProviderID":"provider","Entries":[{"ProviderID":"provider","Source":"managed","RevisionID":"revision"}],"Reproducible":true,"Warnings":null}`
	t.Cleanup(func() { _ = agentRuntime.Close() })

	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	manager, err := baldasession.NewManager(baldasession.ManagerParams{
		AgentBuilder:       SessionAgentBuilderAdapter{Builder: builder},
		RuntimeManager:     integrationRuntimeManager{runtime: sessionRuntime(agentRuntime)},
		WorkingDir:         t.TempDir(),
		SessionsPersistent: true,
		SessionStore:       provider.Sessions(),
		RuntimeOperations:  provider.RuntimeOperations(),
		Logger:             zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	locator, err := baldasession.NewSessionLocator("telegram", "chat:topic", `{}`, "session")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CreateSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "user", Kind: baldasession.KindChat}, "label"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background()) })
	created, err := service.Get(t.Context(), &adksession.GetRequest{AppName: agentRuntime.AppName, UserID: "user", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	prior := adksession.NewEvent(t.Context(), "prior-invocation")
	prior.Author = "incumbent-agent"
	prior.Content = genai.NewContentFromText("prior event", genai.RoleModel)
	prior.Actions.StateDelta = map[string]any{"retained": "state"}
	if err := service.AppendEvent(t.Context(), created.Session, prior); err != nil {
		t.Fatal(err)
	}
	service.createCalls.Store(0)
	service.deleteCalls.Store(0)
	topic, err := manager.GetSession(locator)
	if err != nil {
		t.Fatal(err)
	}
	incumbentLease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	wantSession := incumbentLease.Session()
	incumbentLease.Release()

	dispatcher := actors.NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = dispatcher.Shutdown(ctx)
	})
	application := modelapp.New(dispatcher, manager)
	completion, _, err := application.Submit(t.Context(), modelapp.Request{
		OperationID: "operation", SessionID: "session", Model: "model-next",
	})
	if err != nil {
		t.Fatal(err)
	}
	completed := <-completion
	if completed.Err != nil || completed.Result.Status != baldasession.ModelChangeChangedReplacement {
		t.Fatalf("completion = %+v", completed)
	}
	if service.createCalls.Load() != 0 || service.deleteCalls.Load() != 0 {
		t.Fatalf("model change session mutations: Create=%d Delete=%d", service.createCalls.Load(), service.deleteCalls.Load())
	}
	if host.calls.Load() != 2 {
		t.Fatalf("RunnerFactory host calls = %d, want initial+replacement", host.calls.Load())
	}
	replay, _, err := application.Submit(t.Context(), modelapp.Request{
		OperationID: "operation", SessionID: "session", Model: "model-next",
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed := <-replay
	if replayed.Err != nil || replayed.Result != completed.Result {
		t.Fatalf("terminal replay = %+v, want %+v", replayed, completed)
	}
	if control.validateCalls.Load() != 1 || control.changeCalls.Load() != 1 || host.calls.Load() != 2 {
		t.Fatalf("replay repeated work: validate=%d change=%d runner=%d", control.validateCalls.Load(), control.changeCalls.Load(), host.calls.Load())
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if lease.SessionService() != service || lease.Session() != wantSession || lease.AppName() != agentRuntime.AppName ||
		lease.UserID() != "user" || lease.SessionID() != "session" {
		t.Fatal("published replacement changed exact ADK identity")
	}
	for _, runErr := range lease.Runner().Run(t.Context(), "user", "session",
		genai.NewContentFromText("next turn", genai.RoleUser), adkagent.RunConfig{}) {
		if runErr != nil {
			t.Fatal(runErr)
		}
	}
	if !sawPriorEvent.Load() || !sawPriorState.Load() {
		t.Fatalf("replacement saw prior event/state = %t/%t", sawPriorEvent.Load(), sawPriorState.Load())
	}
}

func TestApplyingModelRecoveryUsesProductionInspectAdapter(t *testing.T) {
	service := &integrationSessionService{Service: adksession.InMemoryService()}
	incumbentAgent, err := adkagent.New(adkagent.Config{Name: "incumbent-agent"})
	if err != nil {
		t.Fatal(err)
	}
	control := &integrationModelControl{currentModel: "model-next"}
	product, err := baldaagent.NewRuntimeProduct(incumbentAgent, control, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{
		ScopedFactory: &integrationProductFactory{product: product},
		WorkingDir:    t.TempDir(), SessionService: service,
		RunnerFactory: baldaagent.NewRunnerFactory(baldaagent.RunnerFactoryParams{Host: &integrationHostFactory{}}),
	})
	agentRuntime, err := builder.BuildRuntimeWithCapabilities(
		t.Context(), "provider", t.TempDir(), nil, nil,
		baldaagent.SkillMetadataProjection{}, baldaagent.SessionInstructionContext{SessionID: "session"},
	)
	if err != nil {
		t.Fatal(err)
	}
	agentRuntime.RuntimeSnapshotID = integrationRuntimeSnapshotID
	agentRuntime.ProviderBindingJSON = `{"RootProviderID":"provider","Entries":[{"ProviderID":"provider","Source":"managed","RevisionID":"revision"}],"Reproducible":true,"Warnings":null}`
	t.Cleanup(func() { _ = agentRuntime.Close() })
	runtimeManager := &integrationScopedRuntimeManager{runtime: agentRuntime}
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	workspaceDir := t.TempDir()
	newManager := func() *baldasession.Manager {
		manager, managerErr := baldasession.NewManager(baldasession.ManagerParams{
			AgentBuilder: SessionAgentBuilderAdapter{Builder: builder}, RuntimeManager: runtimeManager,
			WorkingDir: workspaceDir, SessionsPersistent: true, SessionStore: provider.Sessions(),
			RuntimeOperations: provider.RuntimeOperations(), Logger: zerolog.Nop(),
		})
		if managerErr != nil {
			t.Fatal(managerErr)
		}
		return manager
	}
	locator, err := baldasession.NewSessionLocator("telegram", "chat:topic", `{}`, "session")
	if err != nil {
		t.Fatal(err)
	}
	wantSession, err := builder.CreateFreshRuntimeSession(
		t.Context(), agentRuntime, "provider", "user", "session", workspaceDir,
		baldaagent.RuntimeSessionContext{BaldaSessionID: "session"},
	)
	if err != nil {
		t.Fatal(err)
	}
	prior := adksession.NewEvent(t.Context(), "prior-invocation")
	prior.Author = "incumbent-agent"
	prior.Content = genai.NewContentFromText("prior recovery event", genai.RoleModel)
	prior.Actions.StateDelta = map[string]any{"retained": "state"}
	if err := service.AppendEvent(t.Context(), wantSession, prior); err != nil {
		t.Fatal(err)
	}
	if err := provider.Sessions().Upsert(t.Context(), baldastate.SessionRecord{
		SessionID: "session", UserID: "user", ChannelType: locator.ChannelType, AddressKey: locator.AddressKey,
		AddressJSON: locator.AddressJSON, AgentName: "provider", WorkspaceDir: workspaceDir,
		RuntimeSnapshotID: integrationRuntimeSnapshotID, Status: baldastate.SessionStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	operationAt := time.Now().UTC().Add(-time.Minute)
	intent := baldastate.RuntimeOperationRecord{
		OperationID: "operation", SessionID: "session", InputHash: strings.Repeat("a", 64),
		Kind: string(baldastate.RuntimeOperationKindModel), State: baldastate.RuntimeOperationPrepared,
		TargetSnapshotID: integrationRuntimeSnapshotID, TargetProviderBindingJSON: agentRuntime.ProviderBindingJSON, TargetModel: "model-next",
		CreatedAt: operationAt, UpdatedAt: operationAt,
	}
	if _, err := provider.RuntimeOperations().Begin(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := provider.RuntimeOperations().MarkApplying(t.Context(), intent.OperationID, intent.InputHash); err != nil {
		t.Fatal(err)
	}
	service.createCalls.Store(0)
	service.deleteCalls.Store(0)
	runtimeManager.closeCalls.Store(0)

	restarted := newManager()
	t.Cleanup(func() { _ = restarted.Stop(context.Background()) })
	restored, err := restarted.RestoreSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "user"})
	if err != nil || restored == nil {
		t.Fatalf("RestoreSession() = %p, %v", restored, err)
	}
	restoredLease, err := restored.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer restoredLease.Release()
	if restoredLease.SessionService() != service || restoredLease.AppName() != agentRuntime.AppName ||
		restoredLease.UserID() != "user" || restoredLease.SessionID() != "session" ||
		restoredLease.Generation() != 1 || restoredLease.EffectiveModel() != "model-next" ||
		restoredLease.RuntimeSnapshotID() != integrationRuntimeSnapshotID || restoredLease.ProviderBindingJSON() != agentRuntime.ProviderBindingJSON {
		t.Fatalf("restored runtime identity/generation/model = %q/%q/%q/%d/%q", restoredLease.AppName(), restoredLease.UserID(), restoredLease.SessionID(), restoredLease.Generation(), restoredLease.EffectiveModel())
	}
	stateValue, stateErr := restoredLease.Session().State().Get("retained")
	if stateErr != nil || stateValue != "state" {
		t.Fatalf("restored state = %v, %v", stateValue, stateErr)
	}
	foundPrior := false
	for event := range restoredLease.Session().Events().All() {
		if event.Content != nil && len(event.Content.Parts) > 0 && event.Content.Parts[0].Text == "prior recovery event" {
			foundPrior = true
		}
	}
	if !foundPrior {
		t.Fatal("restored session lost prior event")
	}
	if control.inspectCalls.Load() != 1 || control.validateCalls.Load() != 0 || control.changeCalls.Load() != 0 {
		t.Fatalf("control calls inspect/validate/change = %d/%d/%d", control.inspectCalls.Load(), control.validateCalls.Load(), control.changeCalls.Load())
	}
	if service.createCalls.Load() != 0 || service.deleteCalls.Load() != 0 {
		t.Fatalf("recovery session mutations: Create=%d Delete=%d", service.createCalls.Load(), service.deleteCalls.Load())
	}
	if runtimeManager.closeCalls.Load() != 1 {
		t.Fatalf("private recovery runtime closes = %d, want 1", runtimeManager.closeCalls.Load())
	}
	for _, request := range runtimeManager.sessionRequests() {
		if request.RuntimeSnapshotID != integrationRuntimeSnapshotID || request.ProviderBindingJSON != agentRuntime.ProviderBindingJSON {
			t.Fatalf("legacy RuntimeForSession() request = snapshot %q binding %q", request.RuntimeSnapshotID, request.ProviderBindingJSON)
		}
	}
}
