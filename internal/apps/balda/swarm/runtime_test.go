package swarm

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type testActor struct {
	address string
	err     error
	calls   int
}

func (a *testActor) Address() string { return a.address }
func (a *testActor) Handle(context.Context, Envelope) error {
	a.calls++
	return a.err
}

type testCommandMessage struct {
	env           Envelope
	numDelivered  int
	maxDeliveries int
}

func (m testCommandMessage) Envelope() Envelope               { return m.env }
func (m testCommandMessage) Subject() string                  { return SubjectForEnvelope(m.env) }
func (m testCommandMessage) InProgress(context.Context) error { return nil }
func (m testCommandMessage) DeliveryAttempt() int {
	if m.numDelivered <= 0 {
		return 1
	}
	return m.numDelivered
}
func (m testCommandMessage) MaxDeliveries() int { return m.maxDeliveries }

type recordingCommandBus struct{ events []string }

func (b *recordingCommandBus) PublishCommand(context.Context, Envelope) (*CommandPublishResult, error) {
	return nil, nil
}
func (b *recordingCommandBus) PublishEvent(_ context.Context, subject string, _ Envelope) error {
	b.events = append(b.events, subject)
	return nil
}
func (b *recordingCommandBus) PublishDLQ(context.Context, Envelope, string) error { return nil }
func (b *recordingCommandBus) RunCommandConsumer(ctx context.Context, _ CommandHandler) error {
	<-ctx.Done()
	return ctx.Err()
}
func (b *recordingCommandBus) Drain(context.Context) error { return nil }

func TestRuntime_HandleCommandDispatchesActor(t *testing.T) {
	bus := &recordingCommandBus{}
	actor := &testActor{address: WildcardAddress(ActorTypeSession)}
	registry := NewRegistry()
	if err := registry.Register(actor); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	runtime := newRuntimeForTest(bus, registry)
	if err := runtime.HandleCommand(context.Background(), testCommandMessage{env: runtimeTestEnvelope("ok", ActorAddress{Target: ActorTypeSession, Key: "s-1"})}); err != nil {
		t.Fatalf("HandleCommand() error = %v", err)
	}
	if actor.calls != 1 {
		t.Fatalf("actor calls = %d, want 1", actor.calls)
	}
}

func TestRuntime_UnknownActorReturnsPermanent(t *testing.T) {
	runtime := newRuntimeForTest(&recordingCommandBus{}, NewRegistry())
	err := runtime.HandleCommand(context.Background(), testCommandMessage{env: runtimeTestEnvelope("unknown", ActorAddress{Target: ActorTypeSession, Key: "s-1"})})
	if ClassifyError(err) != ErrorKindPermanent {
		t.Fatalf("ClassifyError(%v) = %s, want permanent", err, ClassifyError(err))
	}
}

func TestRuntime_ActorErrorPropagatesForJetStreamSettlement(t *testing.T) {
	actor := &testActor{address: WildcardAddress(ActorTypeSession), err: TransientError(fmt.Errorf("temporary"))}
	registry := NewRegistry()
	if err := registry.Register(actor); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	runtime := newRuntimeForTest(&recordingCommandBus{}, registry)
	err := runtime.HandleCommand(context.Background(), testCommandMessage{env: runtimeTestEnvelope("retry", ActorAddress{Target: ActorTypeSession, Key: "s-1"})})
	if ClassifyError(err) != ErrorKindTransient {
		t.Fatalf("ClassifyError(%v) = %s, want transient", err, ClassifyError(err))
	}
}

func TestRuntime_RetryExhaustionMarksTaskDeadlettered(t *testing.T) {
	ctx := context.Background()
	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	tasks, err := NewTaskService(taskServiceParams{StateProvider: provider, Bus: &recordingCommandBus{}})
	if err != nil {
		t.Fatalf("NewTaskService() error = %v", err)
	}
	_, err = tasks.Create(ctx, baldastate.SwarmTaskRecord{
		ID:        "task-retry",
		SessionID: "s-1",
		Objective: "retry",
		Status:    baldastate.SwarmTaskStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create task: %v", err)
	}
	actor := &testActor{address: WildcardAddress(ActorTypeSession), err: TransientError(fmt.Errorf("temporary"))}
	registry := NewRegistry()
	if err := registry.Register(actor); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	runtime := newRuntimeForTest(&recordingCommandBus{}, registry)
	runtime.tasks = tasks
	env := runtimeTestEnvelope("retry-exhausted", ActorAddress{Target: ActorTypeSession, Key: "s-1"})
	env.TaskID = "task-retry"
	err = runtime.HandleCommand(ctx, testCommandMessage{env: env, numDelivered: 5, maxDeliveries: 5})
	if ClassifyError(err) != ErrorKindTransient {
		t.Fatalf("ClassifyError(%v) = %s, want transient", err, ClassifyError(err))
	}
	task, ok, err := tasks.Get(ctx, "task-retry")
	if err != nil {
		t.Fatalf("Get task: %v", err)
	}
	if !ok || task.Status != baldastate.SwarmTaskStatusDeadLettered {
		t.Fatalf("task = %+v found=%v, want deadlettered", task, ok)
	}
}

func newRuntimeForTest(bus CommandBus, registry ActorRegistry) *Runtime {
	return &Runtime{bus: bus, registry: registry, scheduler: NewKeyedActorScheduler()}
}

func runtimeTestEnvelope(id string, to ActorAddress) Envelope {
	return Envelope{ID: id, Namespace: NamespaceHumanInbound, Kind: KindMessage, From: ActorAddress{Target: "test", Key: "source"}, To: to, SessionID: to.Key, PayloadJSON: `{"ok":true}`}
}
