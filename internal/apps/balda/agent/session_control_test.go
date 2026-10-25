package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/agentproduct"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

type productTestAgent struct {
	adkagent.Agent
	closeCalls int
	closeErr   error
}

func (a *productTestAgent) Close() error {
	a.closeCalls++
	return a.closeErr
}

type productTestControl struct {
	validateCalls int
	changeCalls   int
	replacement   *agentproduct.Product
}

func (c *productTestControl) Inspect(context.Context, adksession.Session) agentproduct.SessionControlSnapshot {
	return agentproduct.SessionControlSnapshot{Status: agentproduct.ControlReady}
}

func (c *productTestControl) ValidateModel(_ context.Context, _ adksession.Session, model string) agentproduct.ModelValidation {
	c.validateCalls++
	return agentproduct.ModelValidation{Status: agentproduct.ModelSupported, Model: model}
}

func (c *productTestControl) ChangeModel(_ context.Context, _ adksession.Session, model string) agentproduct.ModelChange {
	c.changeCalls++
	return agentproduct.ModelChange{
		Status: agentproduct.ModelReplacement, EffectiveModel: model, Replacement: c.replacement,
	}
}

func (*productTestControl) FastForward(context.Context, adksession.Session, agentproduct.ContextPackage) agentproduct.ContextChange {
	return agentproduct.UnsupportedFastForward()
}

type trackingSessionService struct {
	adksession.Service
	getCalls    int
	createCalls int
	deleteCalls int
}

func (s *trackingSessionService) Get(ctx context.Context, request *adksession.GetRequest) (*adksession.GetResponse, error) {
	s.getCalls++
	return s.Service.Get(ctx, request)
}

func (s *trackingSessionService) Create(ctx context.Context, request *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	s.createCalls++
	return s.Service.Create(ctx, request)
}

func (s *trackingSessionService) Delete(ctx context.Context, request *adksession.DeleteRequest) error {
	s.deleteCalls++
	return s.Service.Delete(ctx, request)
}

func TestBuiltRuntimeVerifiesIdentityAndBuildsIndependentReplacement(t *testing.T) {
	baseService := adksession.InMemoryService()
	created, err := baseService.Create(t.Context(), &adksession.CreateRequest{
		AppName: "norma-balda", UserID: "user-1", SessionID: "session-1", State: map[string]any{"history": "retained"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	service := &trackingSessionService{Service: baseService}
	incumbentAgent := mustProductTestAgent(t, "incumbent")
	replacementAgent := mustProductTestAgent(t, "replacement")
	replacement, err := agentproduct.New(replacementAgent, nil)
	if err != nil {
		t.Fatalf("agentproduct.New() error = %v", err)
	}
	control := &productTestControl{replacement: replacement}
	host := &recordingHostPluginFactory{name: "balda.tool-hooks"}
	runnerFactory := NewRunnerFactory(RunnerFactoryParams{Host: host})
	incumbent := &BuiltRuntime{
		Agent:               incumbentAgent,
		SessionControl:      control,
		SessionSvc:          service,
		AppName:             "norma-balda",
		RuntimeSnapshotID:   "snapshot-retained",
		ProviderBindingJSON: `{"provider":"retained"}`,
		runnerScope:         ToolHookScope{Enabled: true, Provider: "hosted", CWD: t.TempDir()},
		newRunner:           runnerFactory.New,
	}
	identity := SessionIdentity{
		SessionService: service, AppName: "norma-balda", UserID: "user-1", SessionID: "session-1",
	}

	validation := incumbent.ValidateModel(t.Context(), identity, "model-next")
	if validation.Status != agentproduct.ModelSupported || control.validateCalls != 1 {
		t.Fatalf("ValidateModel() = %+v, calls = %d, want supported/1", validation, control.validateCalls)
	}
	change := incumbent.ChangeModel(t.Context(), identity, "model-next")
	if change.Status != agentproduct.ModelReplacement || change.Candidate == nil {
		t.Fatalf("ChangeModel() = %+v, want replacement candidate", change)
	}
	if change.Identity.SessionService != service || change.Identity.AppName != identity.AppName ||
		change.Identity.UserID != identity.UserID || change.Identity.SessionID != identity.SessionID {
		t.Fatalf("verified identity = %+v, want exact incumbent identity", change.Identity)
	}
	if change.Candidate.SessionSvc != service || change.Candidate.AppName != identity.AppName {
		t.Fatalf("candidate binding = %p/%q, want %p/%q", change.Candidate.SessionSvc, change.Candidate.AppName, service, identity.AppName)
	}
	if change.Candidate.RuntimeSnapshotID != incumbent.RuntimeSnapshotID ||
		change.Candidate.ProviderBindingJSON != incumbent.ProviderBindingJSON {
		t.Fatalf("candidate metadata = %q/%q, want %q/%q", change.Candidate.RuntimeSnapshotID,
			change.Candidate.ProviderBindingJSON, incumbent.RuntimeSnapshotID, incumbent.ProviderBindingJSON)
	}
	if len(host.scopes) != 1 || host.scopes[0] != incumbent.runnerScope {
		t.Fatalf("RunnerFactory scopes = %+v, want incumbent scope %+v", host.scopes, incumbent.runnerScope)
	}
	if service.createCalls != 0 || service.deleteCalls != 0 {
		t.Fatalf("candidate session mutations: Create=%d Delete=%d, want zero", service.createCalls, service.deleteCalls)
	}
	got, err := service.Get(t.Context(), &adksession.GetRequest{AppName: identity.AppName, UserID: identity.UserID, SessionID: identity.SessionID})
	if err != nil {
		t.Fatalf("Get() after candidate error = %v", err)
	}
	state, err := got.Session.State().Get("history")
	if err != nil || state != "retained" || got.Session.ID() != created.Session.ID() {
		t.Fatalf("candidate changed existing session history/state: state=%v ID=%q error=%v", state, got.Session.ID(), err)
	}

	if err := change.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
	if err := change.Abort(); err != nil {
		t.Fatalf("second Abort() error = %v", err)
	}
	if replacementAgent.closeCalls != 1 || incumbentAgent.closeCalls != 0 {
		t.Fatalf("close calls replacement/incumbent = %d/%d, want 1/0", replacementAgent.closeCalls, incumbentAgent.closeCalls)
	}
	if got := incumbent.ValidateModel(t.Context(), identity, "model-next"); got.Status != agentproduct.ModelSupported {
		t.Fatalf("incumbent after candidate abort = %+v, want usable", got)
	}
}

func TestBuiltRuntimeRejectsBindingMismatchBeforeControl(t *testing.T) {
	service := &trackingSessionService{Service: adksession.InMemoryService()}
	control := &productTestControl{}
	runtime := &BuiltRuntime{SessionControl: control, SessionSvc: service, AppName: "norma-balda"}

	validation := runtime.ValidateModel(t.Context(), SessionIdentity{
		SessionService: adksession.InMemoryService(), AppName: "norma-balda", UserID: "user-1", SessionID: "session-1",
	}, "model-next")
	if validation.Status != agentproduct.ModelFailed || validation.Reason != agentproduct.ReasonBindingMismatch {
		t.Fatalf("ValidateModel() = %+v, want binding mismatch", validation)
	}
	if control.validateCalls != 0 || service.getCalls != 0 {
		t.Fatalf("mismatched validation calls control/Get = %d/%d, want zero", control.validateCalls, service.getCalls)
	}
}

func TestReplacementRunnerUsesExistingSessionEventsAndState(t *testing.T) {
	baseService := adksession.InMemoryService()
	created, err := baseService.Create(t.Context(), &adksession.CreateRequest{
		AppName: "norma-balda", UserID: "user-1", SessionID: "session-1", State: map[string]any{"retained": "state"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	prior := adksession.NewEvent(t.Context(), "prior-invocation")
	prior.Author = "prior-agent"
	prior.Content = genai.NewContentFromText("prior visible event", genai.RoleModel)
	if err := baseService.AppendEvent(t.Context(), created.Session, prior); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	service := &trackingSessionService{Service: baseService}

	var sawPriorEvent bool
	var retainedState any
	replacementAgent, err := adkagent.New(adkagent.Config{
		Name: "replacement-runner-agent",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				retainedState, _ = ctx.Session().State().Get("retained")
				for event := range ctx.Session().Events().All() {
					if event.Content != nil && len(event.Content.Parts) > 0 && event.Content.Parts[0].Text == "prior visible event" {
						sawPriorEvent = true
					}
				}
				response := adksession.NewEvent(ctx, ctx.InvocationID())
				response.Author = "replacement-runner-agent"
				response.Content = genai.NewContentFromText("replacement response", genai.RoleModel)
				yield(response, nil)
			}
		},
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	replacement, err := agentproduct.New(replacementAgent, nil)
	if err != nil {
		t.Fatalf("agentproduct.New() error = %v", err)
	}
	incumbent := &BuiltRuntime{
		SessionControl: &productTestControl{replacement: replacement},
		SessionSvc:     service,
		AppName:        "norma-balda",
		newRunner: func(config runner.Config, _ ToolHookScope) (*runner.Runner, error) {
			return runner.New(config)
		},
	}
	identity := SessionIdentity{
		SessionService: service, AppName: "norma-balda", UserID: "user-1", SessionID: "session-1",
	}
	change := incumbent.ChangeModel(t.Context(), identity, "model-next")
	if change.Candidate == nil {
		t.Fatalf("ChangeModel() = %+v, want candidate", change)
	}
	for _, runErr := range change.Candidate.Runner.Run(t.Context(), identity.UserID, identity.SessionID,
		genai.NewContentFromText("next turn", genai.RoleUser), adkagent.RunConfig{}) {
		if runErr != nil {
			t.Fatalf("candidate Runner.Run() error = %v", runErr)
		}
	}
	if !sawPriorEvent || retainedState != "state" {
		t.Fatalf("candidate observed prior event/state = %t/%v, want true/state", sawPriorEvent, retainedState)
	}
	if service.createCalls != 0 || service.deleteCalls != 0 {
		t.Fatalf("candidate Runner session mutations: Create=%d Delete=%d, want zero", service.createCalls, service.deleteCalls)
	}
	if err := change.Abort(); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}
}

type productAwareFactory struct {
	product    *agentproduct.Product
	buildCalls int
}

func (f *productAwareFactory) Build(context.Context, agentfactory.BuildRequest) (adkagent.Agent, error) {
	f.buildCalls++
	return f.product.Agent, nil
}

func (f *productAwareFactory) BuildProduct(context.Context, agentfactory.BuildRequest) (*agentproduct.Product, error) {
	return f.product, nil
}

func TestBuilderUsesProductControlAndLegacyFallback(t *testing.T) {
	control := &productTestControl{}
	ownedAgent := mustProductTestAgent(t, "product")
	product, err := agentproduct.New(ownedAgent, control)
	if err != nil {
		t.Fatalf("agentproduct.New() error = %v", err)
	}
	factory := &productAwareFactory{product: product}
	productRuntime, err := (&Builder{factory: factory}).BuildRuntimeWithMCPServerIDs(t.Context(), "provider", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("BuildRuntimeWithMCPServerIDs(product) error = %v", err)
	}
	if productRuntime.SessionControl == nil || factory.buildCalls != 0 {
		t.Fatalf("product runtime control/build calls = %v/%d, want present/0", productRuntime.SessionControl, factory.buildCalls)
	}
	if err := productRuntime.Close(); err != nil {
		t.Fatalf("product runtime Close() error = %v", err)
	}
	if ownedAgent.closeCalls != 1 {
		t.Fatalf("product Agent close calls = %d, want 1", ownedAgent.closeCalls)
	}

	legacy := &recordingRuntimeFactory{}
	legacyRuntime, err := (&Builder{factory: legacy}).BuildRuntimeWithMCPServerIDs(t.Context(), "legacy", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("BuildRuntimeWithMCPServerIDs(legacy) error = %v", err)
	}
	if legacyRuntime.SessionControl != nil {
		t.Fatal("legacy runtime unexpectedly advertised SessionControl")
	}
	if got := legacyRuntime.ValidateModel(t.Context(), SessionIdentity{}, "model"); got.Status != agentproduct.ModelUnsupported {
		t.Fatalf("legacy ValidateModel() = %+v, want unsupported", got)
	}
	if err := legacyRuntime.Close(); err != nil {
		t.Fatalf("legacy runtime Close() error = %v", err)
	}
}

func TestBuilderPreservesRunnerErrorAndBoundsProductCleanupFailure(t *testing.T) {
	runnerErr := errors.New("runner construction failed")
	cleanupErr := errors.New("private provider cleanup detail")
	ag := mustProductTestAgent(t, "cleanup-failure")
	ag.closeErr = cleanupErr
	product, err := agentproduct.New(ag, nil)
	if err != nil {
		t.Fatalf("agentproduct.New() error = %v", err)
	}
	builder := &Builder{
		factory:       &productAwareFactory{product: product},
		runnerFactory: NewRunnerFactory(RunnerFactoryParams{Host: &recordingHostPluginFactory{err: runnerErr}}),
	}
	_, err = builder.BuildRuntimeWithMCPServerIDs(t.Context(), "provider", t.TempDir(), nil, nil)
	if !errors.Is(err, runnerErr) {
		t.Fatalf("BuildRuntimeWithMCPServerIDs() error = %v, want primary runner error", err)
	}
	if !errors.Is(err, ErrRuntimeProductClose) {
		t.Fatalf("BuildRuntimeWithMCPServerIDs() error = %v, want bounded cleanup classification", err)
	}
	if strings.Contains(err.Error(), cleanupErr.Error()) {
		t.Fatalf("BuildRuntimeWithMCPServerIDs() leaked cleanup detail: %v", err)
	}
	if ag.closeCalls != 1 {
		t.Fatalf("product close calls = %d, want 1", ag.closeCalls)
	}
}

func mustProductTestAgent(t *testing.T, name string) *productTestAgent {
	t.Helper()
	ag, err := adkagent.New(adkagent.Config{Name: name})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	return &productTestAgent{Agent: ag}
}
