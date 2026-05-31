package agent

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/normahq/norma/pkg/goalkeeper"
	adkagent "google.golang.org/adk/agent"
	adkrunner "google.golang.org/adk/runner"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestGoalkeeperChildBuildRequest_SetsOutputKeyAndInstructions(t *testing.T) {
	t.Parallel()

	builder := &Builder{workingDir: "/repo"}
	cfg := goalChildAgentConfig{
		ProviderID:        "provider",
		Name:              goalWorkerName,
		Description:       "Goalkeeper worker agent",
		SessionID:         "tg-1-2",
		SessionBranch:     "norma/balda/tg-1-2",
		WorkspaceDir:      "/tmp/workspace",
		RepoBranchAtStart: "main",
		RoleInstruction:   "worker role instruction",
		OutputKey:         "  goalkeeper_worker_output  ",
		MCPServerIDs:      []string{"balda"},
	}

	req := builder.goalChildBuildRequest(cfg)
	if req.OutputKey != goalWorkerOutputStateKey {
		t.Fatalf("req.OutputKey = %q, want %q", req.OutputKey, goalWorkerOutputStateKey)
	}
	if !strings.Contains(req.Instruction, "worker role instruction") {
		t.Fatalf("req.Instruction = %q, want role instruction", req.Instruction)
	}
	if !strings.Contains(req.Instruction, "Workspace settings:") {
		t.Fatalf("req.Instruction = %q, want Balda base instruction", req.Instruction)
	}
}

func TestGoalkeeperValidatorInstruction_DoesNotUseWorkerOutputPlaceholder(t *testing.T) {
	t.Parallel()

	got := goalValidatorInstruction()
	if strings.Contains(got, "{goalkeeper_worker_output?}") {
		t.Fatalf("goalValidatorInstruction() = %q, should not include worker output placeholder", got)
	}
	if !strings.Contains(got, "shared ADK session context") {
		t.Fatalf("goalValidatorInstruction() = %q, want shared session validation guidance", got)
	}
}

func TestGoalkeeperValidatorWrapperUsesLatestWorkerOutputEachInvocation(t *testing.T) {
	t.Parallel()

	var workerRuns int
	worker := mustNewGoalkeeperTestAgent(t, "worker", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			workerRuns++
			workerOutput := "first output"
			if workerRuns == 2 {
				workerOutput = "second output"
			}
			if err := ctx.Session().State().Set(goalWorkerOutputStateKey, workerOutput); err != nil {
				yield(nil, err)
				return
			}
			yield(goalkeeperTestTextEvent(ctx.InvocationID(), workerOutput), nil)
		}
	})
	var validatorRuns int
	inner := mustNewGoalkeeperTestAgent(t, "validator", func(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
		return func(yield func(*adksession.Event, error) bool) {
			validatorRuns++
			result := "verdict: fail\n" + visibleContentText(ctx.UserContent())
			if validatorRuns == 2 {
				result = "verdict: pass\n" + visibleContentText(ctx.UserContent())
			}
			yield(goalkeeperTestTextEvent(ctx.InvocationID(), result), nil)
		}
	})
	wrapped, err := wrapGoalValidatorWithWorkerOutput(inner, goalWorkerOutputStateKey)
	if err != nil {
		t.Fatalf("wrapGoalValidatorWithWorkerOutput() error = %v", err)
	}
	workflow, err := goalkeeper.New(goalkeeper.NewOptions(worker, wrapped, goalkeeper.WithMaxIterations(2)))
	if err != nil {
		t.Fatalf("goalkeeper.New() error = %v", err)
	}

	sessionService := adksession.InMemoryService()
	r, err := adkrunner.New(adkrunner.Config{
		AppName:        "goalkeeper-wrapper-test",
		Agent:          workflow,
		SessionService: sessionService,
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	created, err := sessionService.Create(context.Background(), &adksession.CreateRequest{
		AppName: "goalkeeper-wrapper-test",
		UserID:  "tg-101",
	})
	if err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	got := runGoalkeeperAgentOnce(t, r, "tg-101", created.Session.ID(), "Goal:\ntest")
	if !strings.Contains(got, "Worker result:\nsecond output") {
		t.Fatalf("final validator text = %q, want latest worker output", got)
	}
	if strings.Contains(got, "Worker result:\nfirst output") {
		t.Fatalf("final validator text = %q, contains stale worker output", got)
	}
	if workerRuns != 2 || validatorRuns != 2 {
		t.Fatalf("workerRuns, validatorRuns = %d, %d; want 2, 2", workerRuns, validatorRuns)
	}
}

func mustNewGoalkeeperTestAgent(
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

func runGoalkeeperAgentOnce(
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

func goalkeeperTestTextEvent(invocationID string, text string) *adksession.Event {
	ev := adksession.NewEvent(invocationID)
	ev.Content = genai.NewContentFromText(text, genai.RoleModel)
	return ev
}

func TestBuildGoalkeeperValidatorPromptIncludesMissingWorkerResultMarker(t *testing.T) {
	t.Parallel()

	prompt := buildGoalValidatorPrompt(genai.NewContentFromText("Goal:\ntest", genai.RoleUser), "")
	if !strings.Contains(prompt, "Worker result:\n(none)") {
		t.Fatalf("buildGoalValidatorPrompt() = %q, want explicit missing worker result marker", prompt)
	}
}
