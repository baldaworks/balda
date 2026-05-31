package actors

import (
	"context"
	"iter"
	"strings"
	"testing"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	"github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/agent"
	adkrunner "google.golang.org/adk/runner"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestGoalkeeperActorCompletesPassingRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bus, dispatcher, tasks, _ := newTaskActorSwarmServices(t, ctx)
	locator := session.SessionLocator{SessionID: "tg-101-202", AddressKey: "101"}
	ts := newBaldaTopicSession(t, locator.SessionID)
	setUnexportedField(t, ts, "userID", "101")
	setUnexportedField(t, ts, "agentSessionID", "adk-session-1")
	setUnexportedField(t, ts, "workspaceDir", t.TempDir())
	manager := newBaldaSessionManagerWithSession(t, locator, ts)
	actor := &goalkeeperActor{
		tasks:          tasks,
		dispatcher:     dispatcher,
		sessions:       manager,
		runtimeBuilder: &fakeGoalRuntimeBuilder{t: t, finalValidatorText: "verdict: pass\nvalidated"},
		taskRuns:       NewTaskRunRegistry(),
		maxIters:       3,
		logger:         zerolog.Nop(),
	}
	env, err := GoalTaskEnvelope(locator, "ship release", "101", 3)
	if err != nil {
		t.Fatalf("GoalTaskEnvelope() error = %v", err)
	}
	if err := actor.Handle(ctx, env); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	task, ok, err := tasks.Get(ctx, env.TaskID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatalf("task %q not found", env.TaskID)
	}
	if task.Status != baldastate.SwarmTaskStatusCompleted {
		t.Fatalf("task status = %q, want %q", task.Status, baldastate.SwarmTaskStatusCompleted)
	}
	if !strings.Contains(task.ResultJSON, `"goal_reached":true`) {
		t.Fatalf("task result = %s, want goal_reached true", task.ResultJSON)
	}
	if got := lastPublishedCommandTo(t, bus, swarm.ActorTypeDelivery, locator.AddressKey); got.Kind != taskPayloadKindDelivery {
		t.Fatalf("last delivery = %+v, want delivery command", got)
	}
}

type fakeGoalRuntimeBuilder struct {
	t                  *testing.T
	finalValidatorText string
}

func (b *fakeGoalRuntimeBuilder) BuildGoalRuntime(ctx context.Context, cfg baldaagent.GoalRuntimeConfig) (*baldaagent.GoalRuntime, error) {
	b.t.Helper()
	if cfg.UserID == "" || cfg.SessionID == "" || cfg.WorkspaceDir == "" {
		b.t.Fatalf("BuildGoalRuntime() cfg = %+v, want user/session/workspace", cfg)
	}
	svc := adksession.InMemoryService()
	if _, err := svc.Create(ctx, &adksession.CreateRequest{
		AppName:   "goalkeeper-test",
		UserID:    cfg.UserID,
		SessionID: cfg.SessionID,
	}); err != nil {
		return nil, err
	}
	ag, err := adkagent.New(adkagent.Config{
		Name:        "Goalkeeper",
		Description: "test goalkeeper",
		Run: func(inv adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
			return func(yield func(*adksession.Event, error) bool) {
				workerStarted := goalkeeperTestMetadataEvent(inv.InvocationID(), goalkeeperWorkerStep, goalkeeperStepStarted)
				if !yield(workerStarted, nil) {
					return
				}
				if !yield(goalkeeperTestTextEvent(inv.InvocationID(), "worker completed"), nil) {
					return
				}
				if !yield(goalkeeperTestMetadataEvent(inv.InvocationID(), goalkeeperWorkerStep, goalkeeperStepCompleted), nil) {
					return
				}
				if !yield(goalkeeperTestMetadataEvent(inv.InvocationID(), goalkeeperValidatorStep, goalkeeperStepStarted), nil) {
					return
				}
				if !yield(goalkeeperTestTextEvent(inv.InvocationID(), b.finalValidatorText), nil) {
					return
				}
				completed := goalkeeperTestMetadataEvent(inv.InvocationID(), goalkeeperValidatorStep, goalkeeperStepCompleted)
				completed.TurnComplete = true
				yield(completed, nil)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	r, err := adkrunner.New(adkrunner.Config{AppName: "goalkeeper-test", Agent: ag, SessionService: svc})
	if err != nil {
		return nil, err
	}
	return &baldaagent.GoalRuntime{Agent: ag, Runner: r}, nil
}

func goalkeeperTestMetadataEvent(invocationID string, step string, eventType string) *adksession.Event {
	ev := adksession.NewEvent(invocationID)
	ev.CustomMetadata = map[string]any{
		goalkeeperMetadataEventKey: eventType,
		goalkeeperMetadataStepKey:  step,
	}
	return ev
}

func goalkeeperTestTextEvent(invocationID string, text string) *adksession.Event {
	ev := adksession.NewEvent(invocationID)
	ev.Content = genai.NewContentFromText(text, genai.RoleModel)
	return ev
}
