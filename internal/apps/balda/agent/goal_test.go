package agent

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/goalkeeperworkflow"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	adkagent "google.golang.org/adk/agent"
	adkrunner "google.golang.org/adk/runner"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func visibleContentText(content *genai.Content) string {
	return extractGoalPromptText(content)
}

func TestWrapGoalPromptAgentPrefixesPromptAndReturnsBaseOutput(t *testing.T) {
	t.Parallel()

	var prompts []string
	base := mustNewGoalTestAgent(t, "shared", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			prompts = append(prompts, visibleContentText(ctx.UserContent()))
			yield(goalTestTextEvent(ctx.InvocationID(), "worker summary"), nil)
		}
	})
	wrapped, err := wrapGoalPromptAgent(base, goalPromptAgentConfig{
		Name:        goalWorkerName,
		Description: "Goal worker agent",
		OutputKey:   goalWorkerOutputStateKey,
		BuildPrompt: func(ctx adkagent.InvocationContext) (string, error) {
			return joinGoalPromptSections(goalWorkerInstruction(), extractGoalPromptText(ctx.UserContent())), nil
		},
	})
	if err != nil {
		t.Fatalf("wrapGoalPromptAgent() error = %v", err)
	}

	sessionService := adksession.InMemoryService()
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goal-output-state-test",
		Agent:          wrapped,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goal-output-state-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	got := runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\ntest")
	if got != "worker summary" {
		t.Fatalf("runGoalAgentOnce() = %q, want %q", got, "worker summary")
	}
	if len(prompts) != 1 {
		t.Fatalf("base agent runs = %d, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "You are the goal worker agent.") {
		t.Fatalf("worker prompt = %q, want worker instruction", prompts[0])
	}
	if !strings.Contains(prompts[0], "Goal:\ntest") {
		t.Fatalf("worker prompt = %q, want original goal prompt", prompts[0])
	}
}

func TestGoalValidatorInstruction_DoesNotUseWorkerOutputPlaceholder(t *testing.T) {
	t.Parallel()

	got := goalValidatorInstruction()
	if strings.Contains(got, "{goal_worker_output?}") {
		t.Fatalf("goalValidatorInstruction() = %q, should not include worker output placeholder", got)
	}
	if !strings.Contains(got, "shared runtime session context") {
		t.Fatalf("goalValidatorInstruction() = %q, want shared session validation guidance", got)
	}
}

func TestGoalValidatorWrapperUsesLatestWorkerOutputEachInvocation(t *testing.T) {
	t.Parallel()

	var workerRuns int
	workerBase := mustNewGoalTestAgent(t, "worker", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			workerRuns++
			workerOutput := "first output"
			if workerRuns == 2 {
				workerOutput = "second output"
			}
			yield(goalTestTextEvent(ctx.InvocationID(), workerOutput), nil)
		}
	})
	worker, err := wrapGoalPromptAgent(workerBase, goalPromptAgentConfig{
		Name:        goalWorkerName,
		Description: "Goal worker agent",
		OutputKey:   goalWorkerOutputStateKey,
		BuildPrompt: func(ctx adkagent.InvocationContext) (string, error) {
			return extractGoalPromptText(ctx.UserContent()), nil
		},
	})
	if err != nil {
		t.Fatalf("wrapGoalPromptAgent() error = %v", err)
	}
	var validatorRuns int
	inner := mustNewGoalTestAgent(t, "validator", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			validatorRuns++
			result := "verdict: fail\n" + visibleContentText(ctx.UserContent())
			if validatorRuns == 2 {
				result = "verdict: pass\n" + visibleContentText(ctx.UserContent())
			}
			yield(goalTestTextEvent(ctx.InvocationID(), result), nil)
		}
	})
	wrapped, err := wrapGoalValidatorWithWorkerOutput(inner, goalWorkerOutputStateKey, "")
	if err != nil {
		t.Fatalf("wrapGoalValidatorWithWorkerOutput() error = %v", err)
	}
	workflow, err := goalkeeperworkflow.New(worker, wrapped, 2)
	if err != nil {
		t.Fatalf("goalkeeperworkflow.New() error = %v", err)
	}

	sessionService := newGoalSQLiteSessionService(t)
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goal-wrapper-test",
		Agent:          workflow,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goal-wrapper-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	got := runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\ntest")
	if !strings.Contains(got, "Worker result:\nsecond output") {
		t.Fatalf("final validator text = %q, want latest worker output", got)
	}
	if strings.Contains(got, "Worker result:\nfirst output") {
		t.Fatalf("final validator text = %q, contains earlier worker output", got)
	}
	if workerRuns != 2 || validatorRuns != 2 {
		t.Fatalf("workerRuns, validatorRuns = %d, %d; want 2, 2", workerRuns, validatorRuns)
	}
}

func TestBuildGoalWorkflow_UsesGoalKeeperRootName(t *testing.T) {
	t.Parallel()

	workflow, err := goalkeeperworkflow.New(
		mustNewGoalTestAgent(t, "worker", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				yield(goalTestTextEvent(ctx.InvocationID(), "worker"), nil)
			}
		}),
		mustNewGoalTestAgent(t, "validator", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				yield(goalTestTextEvent(ctx.InvocationID(), "verdict: pass\nok"), nil)
			}
		}),
		1,
	)
	if err != nil {
		t.Fatalf("goalkeeperworkflow.New() error = %v", err)
	}
	if got := workflow.Name(); got != goalkeeperworkflow.RootAgentName {
		t.Fatalf("workflow.Name() = %q, want %q", got, goalkeeperworkflow.RootAgentName)
	}
}

func TestClosableGoalWorkflowPreservesGoalSubAgents(t *testing.T) {
	t.Parallel()

	worker := mustNewGoalTestAgent(t, goalWorkerName, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			yield(goalTestTextEvent(ctx.InvocationID(), "worker"), nil)
		}
	})
	validator := mustNewGoalTestAgent(t, goalValidatorName, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			yield(goalTestTextEvent(ctx.InvocationID(), "verdict: pass\nok"), nil)
		}
	})

	workflow, err := goalkeeperworkflow.New(worker, validator, 1)
	if err != nil {
		t.Fatalf("goalkeeperworkflow.New() error = %v", err)
	}

	wrapped := &closableGoalWorkflow{Agent: workflow, base: workflow}
	subAgents := wrapped.SubAgents()
	if len(subAgents) != 2 {
		t.Fatalf("len(SubAgents()) = %d, want 2", len(subAgents))
	}
	if got := subAgents[0].Name(); got != goalWorkerName {
		t.Fatalf("SubAgents()[0].Name() = %q, want %q", got, goalWorkerName)
	}
	if got := subAgents[1].Name(); got != goalValidatorName {
		t.Fatalf("SubAgents()[1].Name() = %q, want %q", got, goalValidatorName)
	}
}

func TestBuildGoalWorkflowUsesSharedBaseAgentForWorkerAndValidator(t *testing.T) {
	t.Parallel()

	var prompts []string
	base := mustNewGoalTestAgent(t, "shared", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			prompt := visibleContentText(ctx.UserContent())
			prompts = append(prompts, prompt)
			switch {
			case strings.Contains(prompt, "You are the goal validator agent."):
				yield(goalTestTextEvent(ctx.InvocationID(), "verdict: pass\nvalidated"), nil)
			default:
				yield(goalTestTextEvent(ctx.InvocationID(), "worker summary"), nil)
			}
		}
	})
	workflow, err := (&Builder{}).BuildGoalWorkflow(context.Background(), GoalBuildConfig{
		BaseAgent:     base,
		ProviderID:    "shared-provider",
		SessionID:     "goal-session",
		WorkspaceDir:  t.TempDir(),
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("BuildGoalWorkflow() error = %v", err)
	}

	sessionService := adksession.InMemoryService()
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goal-shared-runtime-test",
		Agent:          workflow,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goal-shared-runtime-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	got := runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\nship release")
	if got != "verdict: pass\nvalidated" {
		t.Fatalf("runGoalAgentOnce() = %q, want validator result", got)
	}
	if len(prompts) != 2 {
		t.Fatalf("base agent runs = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[0], "You are the goal worker agent.") {
		t.Fatalf("worker prompt = %q, want worker role instruction", prompts[0])
	}
	if !strings.Contains(prompts[1], "You are the goal validator agent.") {
		t.Fatalf("validator prompt = %q, want validator role instruction", prompts[1])
	}
	if !strings.Contains(prompts[1], "Worker result:\nworker summary") {
		t.Fatalf("validator prompt = %q, want shared worker output", prompts[1])
	}
}

func TestBuildGoalWorkflowCarriesWorkerOutputFromPartialEventIntoValidator(t *testing.T) {
	t.Parallel()

	var prompts []string
	base := mustNewGoalTestAgent(t, "shared", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			prompt := visibleContentText(ctx.UserContent())
			prompts = append(prompts, prompt)
			switch {
			case strings.Contains(prompt, "You are the goal validator agent."):
				yield(goalTestTextEvent(ctx.InvocationID(), "verdict: pass\nvalidated"), nil)
			default:
				yield(goalTestPartialTextEvent(ctx.InvocationID(), "worker summary"), nil)
				yield(goalTestEmptyEvent(ctx.InvocationID()), nil)
			}
		}
	})
	workflow, err := (&Builder{}).BuildGoalWorkflow(context.Background(), GoalBuildConfig{
		BaseAgent:     base,
		ProviderID:    "shared-provider",
		SessionID:     "goal-session",
		WorkspaceDir:  t.TempDir(),
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("BuildGoalWorkflow() error = %v", err)
	}

	sessionService := newGoalSQLiteSessionService(t)
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goal-shared-runtime-partial-test",
		Agent:          workflow,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goal-shared-runtime-partial-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	got := runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\nship release")
	if got != "verdict: pass\nvalidated" {
		t.Fatalf("runGoalAgentOnce() = %q, want validator result", got)
	}
	if len(prompts) != 2 {
		t.Fatalf("base agent runs = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "Worker result:\nworker summary") {
		t.Fatalf("validator prompt = %q, want worker output from partial worker event", prompts[1])
	}
}

func TestClosableGoalWorkflowRunnerDoesNotLogUnknownGoalAgents(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})

	worker := mustNewGoalTestAgent(t, goalWorkerName, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			yield(goalTestTextEvent(ctx.InvocationID(), "worker"), nil)
		}
	})
	validator := mustNewGoalTestAgent(t, goalValidatorName, func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			yield(goalTestTextEvent(ctx.InvocationID(), "verdict: pass\nok"), nil)
		}
	})

	workflow, err := goalkeeperworkflow.New(worker, validator, 1)
	if err != nil {
		t.Fatalf("goalkeeperworkflow.New() error = %v", err)
	}

	sessionService := adksession.InMemoryService()
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goal-wrapper-runner-tree-test",
		Agent:          &closableGoalWorkflow{Agent: workflow, base: workflow},
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goal-wrapper-runner-tree-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\ntest")
	runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\ntest again")

	if got := logBuf.String(); strings.Contains(got, "unknown agent") {
		t.Fatalf("runner log = %q, want no unknown-agent messages", got)
	}
}

func TestBuildGoalCommitAgentUsesSharedBaseAgent(t *testing.T) {
	t.Parallel()

	var prompts []string
	base := mustNewGoalTestAgent(t, "shared", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			prompts = append(prompts, visibleContentText(ctx.UserContent()))
			yield(goalTestTextEvent(ctx.InvocationID(), "fix(goal): share runtime"), nil)
		}
	})
	agent, err := buildGoalCommitAgent(base)
	if err != nil {
		t.Fatalf("buildGoalCommitAgent() error = %v", err)
	}

	sessionService := adksession.InMemoryService()
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goal-commit-agent-test",
		Agent:          agent,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goal-commit-agent-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	got := runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal objective:\nshare runtime")
	if got != "fix(goal): share runtime" {
		t.Fatalf("runGoalAgentOnce() = %q, want commit subject", got)
	}
	if len(prompts) != 1 {
		t.Fatalf("base agent runs = %d, want 1", len(prompts))
	}
	if !strings.Contains(prompts[0], "You generate a Conventional Commit subject for goal export.") {
		t.Fatalf("commit prompt = %q, want committer instruction", prompts[0])
	}
	if !strings.Contains(prompts[0], "Goal objective:\nshare runtime") {
		t.Fatalf("commit prompt = %q, want objective content", prompts[0])
	}
}

func mustNewGoalTestAgent(
	t *testing.T,
	name string,
	run func(adkagent.InvocationContext) iter.Seq2[*adksession.Event, error],
) adkagent.Agent {
	t.Helper()

	ag, err := adkagent.New(adkagent.Config{
		Name:        name,
		Description: name + " test agent",
		Run:         run,
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	return ag
}

func runGoalAgentOnce(
	t *testing.T,
	r *adkrunner.Runner,
	userID string,
	sessionID string,
	prompt string,
) string {
	t.Helper()

	var out string
	for ev, err := range r.Run(
		context.Background(),
		userID,
		sessionID,
		genai.NewContentFromText(prompt, genai.RoleUser),
		adkagent.RunConfig{},
	) {
		if err != nil {
			t.Fatalf("runner.Run() error = %v", err)
		}
		text := visibleContentText(ev.Content)
		if text != "" {
			out = text
		}
	}
	return out
}

func goalTestTextEvent(invocationID string, text string) *adksession.Event {
	ev := adksession.NewEvent(invocationID)
	ev.Content = genai.NewContentFromText(text, genai.RoleModel)
	return ev
}

func goalTestPartialTextEvent(invocationID string, text string) *adksession.Event {
	ev := goalTestTextEvent(invocationID, text)
	ev.Partial = true
	return ev
}

func goalTestEmptyEvent(invocationID string) *adksession.Event {
	return adksession.NewEvent(invocationID)
}

func newGoalSQLiteSessionService(t *testing.T) adksession.Service {
	t.Helper()

	provider, err := baldastate.NewSQLiteProvider(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider.RuntimeSessions()
}

func TestGoalValidatorWrapperIncludesMissingWorkerResultMarker(t *testing.T) {
	t.Parallel()

	inner := mustNewGoalTestAgent(t, "validator", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			yield(goalTestTextEvent(ctx.InvocationID(), visibleContentText(ctx.UserContent())), nil)
		}
	})
	wrapped, err := wrapGoalValidatorWithWorkerOutput(inner, goalWorkerOutputStateKey, "")
	if err != nil {
		t.Fatalf("wrapGoalValidatorWithWorkerOutput() error = %v", err)
	}

	sessionService := adksession.InMemoryService()
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goal-wrapper-missing-output-test",
		Agent:          wrapped,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goal-wrapper-missing-output-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}

	got := runGoalAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\ntest")
	if !strings.Contains(got, "Worker result:\n(none)") {
		t.Fatalf("validator wrapper output = %q, want explicit missing worker result marker", got)
	}
}

func TestClosableGoalWorkflowCloseDoesNotCloseSharedBaseAgent(t *testing.T) {
	t.Parallel()

	closed := 0
	base := closeTrackingGoalAgent{
		Agent: mustNewGoalTestAgent(t, "shared", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				yield(goalTestTextEvent(ctx.InvocationID(), "worker"), nil)
			}
		}),
		closeFn: func() error {
			closed++
			return nil
		},
	}
	workflow, err := (&Builder{}).BuildGoalWorkflow(context.Background(), GoalBuildConfig{
		BaseAgent:     base,
		ProviderID:    "shared-provider",
		SessionID:     "goal-session",
		WorkspaceDir:  t.TempDir(),
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("BuildGoalWorkflow() error = %v", err)
	}

	if closer, ok := workflow.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatalf("workflow.Close() error = %v", err)
		}
	}
	if closed != 0 {
		t.Fatalf("shared base close calls = %d, want 0", closed)
	}
}

type closeTrackingGoalAgent struct {
	adkagent.Agent
	closeFn func() error
}

func (a closeTrackingGoalAgent) Close() error {
	if a.closeFn == nil {
		return nil
	}
	return a.closeFn()
}

func (a closeTrackingGoalAgent) String() string {
	return fmt.Sprintf("closeTrackingGoalAgent(%s)", a.Name())
}
