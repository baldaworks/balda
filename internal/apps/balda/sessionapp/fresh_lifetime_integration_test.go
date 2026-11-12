package sessionapp

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/lifetimeapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const freshLifetimeIntegrationProviderID = "provider"
const freshLifetimeCurrentSnapshot = "catalog-B"

type freshLifetimeRuntimeManager struct {
	runtime         *baldasession.BuiltRuntime
	currentSnapshot string
	selections      atomic.Int32
	calls           atomic.Int32
	builds          atomic.Int32
}

type freshLifetimeCandidate struct {
	runtime  *baldasession.BuiltRuntime
	claimed  bool
	promoted bool
}

type followingRuntimeManager struct {
	runtime       *baldasession.BuiltRuntime
	configuration sessioncmd.ConfigurationSummary
}

func (m followingRuntimeManager) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	return m.runtime, nil
}

func (m followingRuntimeManager) ProviderID() string { return freshLifetimeIntegrationProviderID }

func (m followingRuntimeManager) DescribeSessionConfiguration(context.Context, baldasession.SessionRuntimeRequest) (sessioncmd.ConfigurationSummary, error) {
	return m.configuration.Clone(), nil
}

func (m followingRuntimeManager) RuntimeForSession(_ context.Context, request baldasession.SessionRuntimeRequest) (*baldasession.BuiltRuntime, error) {
	runtime := *m.runtime
	runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
	runtime.ProviderBindingJSON = request.ProviderBindingJSON
	if request.ModelOverride != nil {
		runtime.RecipeModel = *request.ModelOverride
	}
	return &runtime, nil
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

func (m *freshLifetimeRuntimeManager) CurrentSessionSnapshot(context.Context, string) (string, error) {
	m.selections.Add(1)
	if m.currentSnapshot != "" {
		return m.currentSnapshot, nil
	}
	return m.runtime.RuntimeSnapshotID, nil
}

func (*freshLifetimeRuntimeManager) DescribeSessionConfiguration(context.Context, baldasession.SessionRuntimeRequest) (sessioncmd.ConfigurationSummary, error) {
	return sessioncmd.ConfigurationSummary{
		ProviderID: freshLifetimeIntegrationProviderID, ProviderType: "codex", ConfiguredModel: "model-current",
		ReasoningEffort: "high", MCPServers: []string{"grafana"},
	}, nil
}

func (m *freshLifetimeRuntimeManager) BuildUnpublishedSessionRuntime(_ context.Context, request baldasession.SessionRuntimeRequest) (baldasession.UnpublishedSessionRuntimeCandidate, error) {
	m.builds.Add(1)
	runtime := *m.runtime
	runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
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

func (m *freshLifetimeRuntimeManager) RuntimeForSession(_ context.Context, request baldasession.SessionRuntimeRequest) (*baldasession.BuiltRuntime, error) {
	m.builds.Add(1)
	runtime := *m.runtime
	if request.RuntimeSnapshotID != "" {
		runtime.RuntimeSnapshotID = request.RuntimeSnapshotID
	}
	if request.ProviderBindingJSON != "" {
		runtime.ProviderBindingJSON = request.ProviderBindingJSON
	}
	if request.ModelOverride != nil {
		runtime.RecipeModel = *request.ModelOverride
	}
	return &runtime, nil
}

func (*freshLifetimeRuntimeManager) ProviderID() string { return freshLifetimeIntegrationProviderID }

func TestColdFreshLifetimeUsesProductionAdapterWithoutProviderRuntime(t *testing.T) {
	sessionService := &integrationSessionService{Service: adksession.InMemoryService()}
	agent, err := adkagent.New(adkagent.Config{Name: "incumbent-agent"})
	if err != nil {
		t.Fatal(err)
	}
	control := &integrationModelControl{currentModel: "model-current", unavailable: true}
	product, err := baldaagent.NewRuntimeProduct(agent, control, nil)
	if err != nil {
		t.Fatal(err)
	}
	builder := baldaagent.NewBuilder(baldaagent.BuilderParams{
		ScopedFactory:  &integrationProductFactory{product: product},
		WorkingDir:     t.TempDir(),
		SessionService: sessionService,
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
	agentRuntime.ProviderID = freshLifetimeIntegrationProviderID
	t.Cleanup(func() { _ = agentRuntime.Close() })

	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtimeManager := &freshLifetimeRuntimeManager{runtime: sessionRuntime(agentRuntime), currentSnapshot: freshLifetimeCurrentSnapshot}
	newManager := func() *baldasession.Manager {
		manager, managerErr := baldasession.NewManager(baldasession.ManagerParams{
			Archive: provider.ChatArchive(), AgentBuilder: SessionAgentBuilderAdapter{Builder: builder},
			RuntimeManager: runtimeManager, WorkingDir: t.TempDir(), SessionsPersistent: true,
			SessionStore: provider.Sessions(), RuntimeOperations: provider.RuntimeOperations(), Logger: zerolog.Nop(),
		})
		if managerErr != nil {
			t.Fatal(managerErr)
		}
		return manager
	}
	locator, err := baldasession.NewSessionLocator("telegram", "chat:cold", `{}`, "session")
	if err != nil {
		t.Fatal(err)
	}
	initial := newManager()
	if err := initial.CreateSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "user", Kind: baldasession.KindChat}, "display-label"); err != nil {
		t.Fatal(err)
	}
	before, found, err := provider.Sessions().GetBySessionID(t.Context(), locator.SessionID)
	if err != nil || !found {
		t.Fatalf("initial record = %+v, %t, %v", before, found, err)
	}
	if err := initial.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	sessionService.createCalls.Store(0)
	runtimeManager.builds.Store(0)
	runtimeCalls := runtimeManager.calls.Load()

	cold := newManager()
	t.Cleanup(func() { _ = cold.Stop(context.Background()) })
	request := baldasession.FreshLifetimeRequest{
		OperationID: "cold-production-reset", SessionID: locator.SessionID, InputHash: strings.Repeat("a", 64),
	}
	prepared, result, err := cold.PrepareFreshLifetime(t.Context(), request)
	if err != nil || prepared == nil {
		t.Fatalf("PrepareFreshLifetime() = %p, %+v, %v", prepared, result, err)
	}
	result, err = cold.PublishFreshLifetime(t.Context(), prepared)
	if err != nil || result.Status != baldasession.FreshLifetimeReset {
		t.Fatalf("PublishFreshLifetime() = %+v, %v", result, err)
	}
	if result.Activation == nil || !reflect.DeepEqual(result.Activation.Runtime, sessioncmd.RuntimeSummary{}) {
		t.Fatalf("activation = %#v, want empty runtime summary", result.Activation)
	}
	if _, err := cold.GetSession(locator); err == nil {
		t.Fatal("cold reset eagerly published a TopicSession")
	}
	after, found, err := provider.Sessions().GetBySessionID(t.Context(), locator.SessionID)
	if err != nil || !found || after.ArchiveID == before.ArchiveID || after.ProviderBindingJSON != before.ProviderBindingJSON || after.RuntimeSnapshotID != freshLifetimeCurrentSnapshot {
		t.Fatalf("reset record = %+v, %t, %v", after, found, err)
	}
	if sessionService.createCalls.Load() != 1 || runtimeManager.builds.Load() != 0 || runtimeManager.calls.Load() != runtimeCalls {
		t.Fatalf("local/provider calls = create %d, builds %d, runtime %d/%d",
			sessionService.createCalls.Load(), runtimeManager.builds.Load(), runtimeManager.calls.Load(), runtimeCalls)
	}
	replayManager := newManager()
	t.Cleanup(func() { _ = replayManager.Stop(context.Background()) })
	_, replay, err := replayManager.PrepareFreshLifetime(t.Context(), request)
	if err != nil || !reflect.DeepEqual(replay, result) {
		t.Fatalf("terminal replay = %+v, %v; want %+v", replay, err, result)
	}
	alternateRequest := baldasession.ModelChangeRequest{
		OperationID: "cold-production-alternate-model", SessionID: locator.SessionID,
		InputHash: strings.Repeat("c", 64), TargetModel: "model-next",
	}
	alternateResult, err := cold.ChangeModel(t.Context(), alternateRequest)
	if err != nil || alternateResult.Status != baldasession.ModelChangeUnavailable {
		t.Fatalf("alternate ChangeModel() = %+v, %v", alternateResult, err)
	}
	unchanged, found, err := provider.Sessions().GetBySessionID(t.Context(), locator.SessionID)
	if err != nil || !found || !reflect.DeepEqual(unchanged, after) {
		t.Fatalf("record after alternate target = %+v, %t, %v; want %+v", unchanged, found, err, after)
	}
	if _, found, err := provider.RuntimeOperations().Get(t.Context(), alternateRequest.OperationID); err != nil || found {
		t.Fatalf("alternate operation found=%t err=%v", found, err)
	}
	badModel := "model-next"
	now := time.Now().UTC()
	poison := baldastate.RuntimeOperationRecord{
		OperationID: "cold-production-poison", SessionID: locator.SessionID, InputHash: strings.Repeat("d", 64),
		Kind: string(baldastate.RuntimeOperationKindModel), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: after.ArchiveID, ExpectedGeneration: after.RuntimeGeneration,
		TargetArchiveID: after.ArchiveID, TargetSnapshotID: after.RuntimeSnapshotID,
		TargetProviderBindingJSON: after.ProviderBindingJSON, TargetModel: badModel, TargetRequestedModel: &badModel,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := provider.RuntimeOperations().Begin(t.Context(), poison); err != nil {
		t.Fatal(err)
	}
	if err := provider.RuntimeOperations().MarkApplying(t.Context(), poison.OperationID, poison.InputHash); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RuntimeOperations().Finalize(t.Context(), baldastate.RuntimeOperationFinalization{
		OperationID: poison.OperationID, InputHash: poison.InputHash,
		Outcome: baldastate.RuntimeOperationOutcomeContextReset, ReasonCode: baldastate.RuntimeOperationReasonChangedReplacement,
		At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	poisoned, found, err := provider.Sessions().GetBySessionID(t.Context(), locator.SessionID)
	if err != nil || !found || poisoned.RequestedModel == nil || poisoned.EffectiveModel != badModel {
		t.Fatalf("poisoned record = %+v, %t, %v", poisoned, found, err)
	}
	poisonBuilder := baldaagent.NewBuilder(baldaagent.BuilderParams{
		ScopedFactory: &integrationProductFactory{product: product}, WorkingDir: poisoned.WorkspaceDir, SessionService: sessionService,
	})
	poisonRuntime, err := poisonBuilder.BuildRuntimeWithCapabilities(
		t.Context(), freshLifetimeIntegrationProviderID, poisoned.WorkspaceDir, nil, nil,
		baldaagent.SkillMetadataProjection{}, baldaagent.SessionInstructionContext{SessionID: "session"},
	)
	if err != nil {
		t.Fatal(err)
	}
	poisonRuntime.RuntimeSnapshotID = poisoned.RuntimeSnapshotID
	poisonRuntime.ProviderBindingJSON = poisoned.ProviderBindingJSON
	poisonRuntime.ProviderID = freshLifetimeIntegrationProviderID
	configuration := sessioncmd.ConfigurationSummary{
		ProviderID: freshLifetimeIntegrationProviderID, ProviderType: "codex", ConfiguredModel: "model-current",
		ReasoningEffort: "high", MCPServers: []string{"grafana"},
	}
	poisonManager, err := baldasession.NewManager(baldasession.ManagerParams{
		Archive: provider.ChatArchive(), AgentBuilder: SessionAgentBuilderAdapter{Builder: poisonBuilder},
		RuntimeManager: followingRuntimeManager{runtime: sessionRuntime(poisonRuntime), configuration: configuration},
		WorkingDir:     poisoned.WorkspaceDir, SessionsPersistent: true,
		SessionStore: provider.Sessions(), RuntimeOperations: provider.RuntimeOperations(), Logger: zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = poisonManager.Stop(context.Background()) })
	if _, err := poisonManager.EnsureSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "user", Kind: baldasession.KindChat}, "display-label"); err != nil {
		t.Fatal(err)
	}
	modelRequest := baldasession.ModelChangeRequest{
		OperationID: "cold-production-model", SessionID: locator.SessionID,
		InputHash: strings.Repeat("b", 64), TargetModel: "model-current",
	}
	modelResult, err := poisonManager.ChangeModel(t.Context(), modelRequest)
	if err != nil || modelResult.Status != baldasession.ModelChangePending || modelResult.EffectiveModel != "" {
		t.Fatalf("ChangeModel() = %+v, %v", modelResult, err)
	}
	if _, err := poisonManager.GetSession(locator); err == nil {
		t.Fatal("repaired poisoned runtime remained published")
	}
	changed, found, err := provider.Sessions().GetBySessionID(t.Context(), locator.SessionID)
	if err != nil || !found || changed.ArchiveID != after.ArchiveID || changed.RuntimeGeneration != after.RuntimeGeneration+2 ||
		changed.RequestedModel != nil || changed.EffectiveModel != "" ||
		changed.RuntimeOutcome != string(baldastate.RuntimeOperationOutcomeContextReset) {
		t.Fatalf("model preference record = %+v, %t, %v", changed, found, err)
	}
	if control.validateCalls.Load() != 1 || control.changeCalls.Load() != 0 {
		t.Fatalf("provider control after poison repair: validate=%d change=%d", control.validateCalls.Load(), control.changeCalls.Load())
	}
	modelReplayManager := newManager()
	t.Cleanup(func() { _ = modelReplayManager.Stop(context.Background()) })
	modelReplay, err := modelReplayManager.ChangeModel(t.Context(), modelRequest)
	if err != nil || !reflect.DeepEqual(modelReplay, modelResult) {
		t.Fatalf("model replay = %+v, %v; want %+v", modelReplay, err, modelResult)
	}
	if control.validateCalls.Load() != 1 || control.changeCalls.Load() != 0 {
		t.Fatal("model replay performed provider work")
	}
	var finalResponses atomic.Int32
	respondingAgent, err := adkagent.New(adkagent.Config{
		Name: "responding-agent",
		Run: func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				event := adksession.NewEvent(ctx, ctx.InvocationID())
				event.Author = "responding-agent"
				event.Content = genai.NewContentFromText("configured response", genai.RoleModel)
				event.TurnComplete = true
				finalResponses.Add(1)
				yield(event, nil)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	respondingProduct, err := baldaagent.NewRuntimeProduct(respondingAgent, control, nil)
	if err != nil {
		t.Fatal(err)
	}
	followBuilder := baldaagent.NewBuilder(baldaagent.BuilderParams{
		ScopedFactory: &integrationProductFactory{product: respondingProduct}, WorkingDir: changed.WorkspaceDir, SessionService: sessionService,
		RunnerFactory: baldaagent.NewRunnerFactory(baldaagent.RunnerFactoryParams{Host: &integrationHostFactory{}}),
	})
	followRuntime, err := followBuilder.BuildRuntimeWithCapabilities(
		t.Context(), freshLifetimeIntegrationProviderID, changed.WorkspaceDir, nil, nil,
		baldaagent.SkillMetadataProjection{}, baldaagent.SessionInstructionContext{SessionID: "session"},
	)
	if err != nil {
		t.Fatal(err)
	}
	followRuntime.RuntimeSnapshotID = freshLifetimeCurrentSnapshot
	followRuntime.ProviderBindingJSON = integrationProviderBindingJSON
	followRuntime.ProviderID = freshLifetimeIntegrationProviderID
	followingRuntime := sessionRuntime(followRuntime)
	following, err := baldasession.NewManager(baldasession.ManagerParams{
		Archive: provider.ChatArchive(), AgentBuilder: SessionAgentBuilderAdapter{Builder: followBuilder},
		RuntimeManager: followingRuntimeManager{runtime: followingRuntime, configuration: configuration}, WorkingDir: changed.WorkspaceDir, SessionsPersistent: true,
		SessionStore: provider.Sessions(), RuntimeOperations: provider.RuntimeOperations(), Logger: zerolog.Nop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = following.Stop(context.Background()) })
	followingTopic, err := following.EnsureSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "user", Kind: baldasession.KindChat}, "display-label")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := followingTopic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	executor := sessionturnapp.NewProviderTurnExecutor(nil, nil, zerolog.Nop())
	err = executor.ExecuteSessionTurn(t.Context(), sessionturn.Request{
		Session: followingTopic, Runtime: lease,
		Payload: turncmd.SessionTurnPayload{
			ArchiveID: lease.ArchiveID(), DedupeKey: "following-turn", Text: "next turn", Source: turncmd.SourceTelegram,
			Locator: locator,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalResponses.Load() != 1 {
		t.Fatalf("following final responses = %d, want 1", finalResponses.Load())
	}
}

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
	if sessionService.createCalls.Load() != 0 {
		t.Fatalf("local candidate creates before boundary = %d, want 0", sessionService.createCalls.Load())
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
	if sessionService.createCalls.Load() != 0 || sessionService.deleteCalls.Load() != 0 {
		t.Fatalf("foreign operation mutated target: Create=%d Delete=%d", sessionService.createCalls.Load(), sessionService.deleteCalls.Load())
	}
	duplicate, _, err := application.Submit(t.Context(), lifetimeapp.Request{OperationID: "fresh-operation", SessionID: "session", Command: "reset"})
	if err != nil {
		t.Fatal(err)
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
	if _, found, err := provider.RuntimeOperations().Get(t.Context(), "fresh-operation"); err != nil || found {
		t.Fatalf("pre-boundary operation found=%v err=%v", found, err)
	}
	if runtimeManager.selections.Load() != 0 {
		t.Fatalf("early catalog selections=%d", runtimeManager.selections.Load())
	}
	before, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if before.SessionID() != oldSessionID || before.ArchiveID() != oldArchiveID || before.Generation() != oldGeneration || before.RuntimeSnapshotID() != oldSnapshot {
		t.Fatal("incumbent changed before FIFO boundary")
	}
	before.Release()
	if _, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error { return nil }}); !errors.Is(err, appports.ErrTurnReplacementPending) {
		t.Fatalf("later work error = %v, want replacement pending", err)
	}

	runtimeManager.currentSnapshot = freshLifetimeCurrentSnapshot
	close(releaseActive)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	completed := <-completion
	if completed.Result.Status != lifetimeapp.StatusReset || completed.Result.Generation != oldGeneration+1 {
		t.Fatalf("completion = %+v", completed)
	}
	if got := <-duplicate; !reflect.DeepEqual(got.Result, completed.Result) {
		t.Fatalf("attached duplicate=%+v, want %+v", got, completed)
	}
	runtimeManager.currentSnapshot = "catalog-C"
	current, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Release()
	if current.SessionID() == oldSessionID || current.SessionID() != current.ArchiveID() ||
		current.Generation() != oldGeneration+1 || current.RuntimeSnapshotID() != freshLifetimeCurrentSnapshot ||
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
	if got := <-replay; !reflect.DeepEqual(got.Result, completed.Result) {
		t.Fatalf("terminal replay = %+v, want %+v", got, completed)
	}
	if runtimeManager.selections.Load() != 1 || sessionService.createCalls.Load() != 1 || runtimeManager.calls.Load() != runtimeCalls ||
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
