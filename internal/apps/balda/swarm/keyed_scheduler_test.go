package swarm

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestActorKeyMapsSessionTaskAndAgent(t *testing.T) {
	tests := []struct {
		name string
		env  Envelope
		want string
	}{
		{name: "session", env: Envelope{Namespace: NamespaceWebhookInbound, SessionID: "s-1", To: ActorAddress{Target: ActorTypeTask, Key: "task-1"}}, want: "session:s-1"},
		{name: "task control", env: Envelope{Namespace: NamespaceTaskControl, TaskID: "task-1", To: ActorAddress{Target: ActorTypeTask, Key: "task-1"}}, want: "task:task-1"},
		{name: "agent task lane", env: Envelope{Namespace: NamespaceAgentCommand, TaskID: "task-1", To: ActorAddress{Target: ActorTypeAgent, Key: "executor"}}, want: "task:task-1"},
		{name: "agent result task lane", env: Envelope{Namespace: NamespaceAgentResult, TaskID: "task-1", To: ActorAddress{Target: ActorTypeTask, Key: "task-1"}}, want: "task:task-1"},
		{name: "agent result delivery lane", env: Envelope{Namespace: NamespaceAgentResult, TaskID: "task-1", To: ActorAddress{Target: ActorTypeDelivery, Key: "tg-9001"}}, want: "delivery:tg-9001"},
		{name: "webhook task lane", env: Envelope{Namespace: NamespaceWebhookInbound, SessionID: "s-1", TaskID: "task-1", To: ActorAddress{Target: ActorTypeTask, Key: "task-1"}}, want: "task:task-1"},
		{name: "schedule task lane", env: Envelope{Namespace: NamespaceScheduleInbound, SessionID: "s-1", TaskID: "task-1", To: ActorAddress{Target: ActorTypeTask, Key: "task-1"}}, want: "task:task-1"},
		{name: "agent fallback", env: Envelope{Namespace: NamespaceAgentCommand, To: ActorAddress{Target: ActorTypeAgent, Key: "executor"}}, want: "agent:executor"},
		{name: "fallback", env: Envelope{Namespace: NamespaceTelemetry, To: ActorAddress{Target: ActorTypeDelivery, Key: "tg"}}, want: "delivery:tg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := actorKey(tt.env); got != tt.want {
				t.Fatalf("actorKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKeyedActorSchedulerRunsDifferentTasksConcurrently(t *testing.T) {
	scheduler := NewKeyedActorScheduler()
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 2)

	run := func(taskID string) {
		env := Envelope{ID: taskID, Namespace: NamespaceAgentCommand, Kind: KindGoal, From: ActorAddress{Target: ActorTypeTask, Key: taskID}, To: ActorAddress{Target: ActorTypeAgent, Key: AgentNameExecutor}, TaskID: taskID, PayloadJSON: `{}`}
		done <- scheduler.Dispatch(context.Background(), env, func(context.Context, Envelope) error {
			started <- taskID
			<-release
			return nil
		})
	}

	go run("task-a")
	if got := waitStarted(t, started); got != "task-a" {
		t.Fatalf("first started = %q, want task-a", got)
	}
	go run("task-b")
	if got := waitStarted(t, started); got != "task-b" {
		t.Fatalf("second started = %q, want task-b", got)
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("Dispatch() error = %v", err)
		}
	}
}

func TestKeyedActorSchedulerSerializesSameTaskAgents(t *testing.T) {
	scheduler := NewKeyedActorScheduler()
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	run := func(id string) {
		env := Envelope{ID: id, Namespace: NamespaceAgentCommand, Kind: KindGoal, From: ActorAddress{Target: ActorTypeTask, Key: "task-a"}, To: ActorAddress{Target: ActorTypeAgent, Key: AgentNameExecutor}, TaskID: "task-a", PayloadJSON: `{}`}
		done <- scheduler.Dispatch(context.Background(), env, func(context.Context, Envelope) error {
			started <- id
			if id == "first" {
				<-release
			}
			return nil
		})
	}
	go run("first")
	if got := waitStarted(t, started); got != "first" {
		t.Fatalf("first started = %q, want first", got)
	}
	go run("second")
	select {
	case got := <-started:
		t.Fatalf("same-task lane started %q before first released", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if got := waitStarted(t, started); got != "second" {
		t.Fatalf("second started = %q, want second", got)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("Dispatch() error = %v", err)
		}
	}
}

func TestKeyedActorSchedulerSerializesTaskLifecycleNamespaces(t *testing.T) {
	scheduler := NewKeyedActorScheduler()
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 2)

	firstEnv := Envelope{
		ID:          "webhook-1",
		Namespace:   NamespaceWebhookInbound,
		TaskID:      "task-1",
		SessionID:   "s-1",
		To:          ActorAddress{Target: ActorTypeTask, Key: "task-1"},
		PayloadJSON: `{}`,
	}
	secondEnv := Envelope{
		ID:          "cancel-1",
		Namespace:   NamespaceTaskControl,
		TaskID:      "task-1",
		To:          ActorAddress{Target: ActorTypeTask, Key: "task-1"},
		PayloadJSON: `{}`,
	}

	go func() {
		done <- scheduler.Dispatch(context.Background(), firstEnv, func(context.Context, Envelope) error {
			started <- firstEnv.ID
			<-release
			return nil
		})
	}()
	if got := waitStarted(t, started); got != firstEnv.ID {
		t.Fatalf("first started = %q, want %q", got, firstEnv.ID)
	}
	go func() {
		done <- scheduler.Dispatch(context.Background(), secondEnv, func(context.Context, Envelope) error {
			started <- secondEnv.ID
			return nil
		})
	}()
	select {
	case got := <-started:
		t.Fatalf("task lifecycle dispatch started %q before first released", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if got := waitStarted(t, started); got != secondEnv.ID {
		t.Fatalf("second started = %q, want %q", got, secondEnv.ID)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("Dispatch() error = %v", err)
		}
	}
}

func TestKeyedActorSchedulerSerializesDeliveryAddressAcrossTasks(t *testing.T) {
	scheduler := NewKeyedActorScheduler()
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 2)

	firstEnv := Envelope{
		ID:          "delivery-1",
		Namespace:   NamespaceAgentResult,
		Kind:        "task_delivery",
		TaskID:      "task-1",
		To:          ActorAddress{Target: ActorTypeDelivery, Key: "tg-9001"},
		PayloadJSON: `{}`,
	}
	secondEnv := Envelope{
		ID:          "delivery-2",
		Namespace:   NamespaceAgentResult,
		Kind:        "task_delivery",
		TaskID:      "task-2",
		To:          ActorAddress{Target: ActorTypeDelivery, Key: "tg-9001"},
		PayloadJSON: `{}`,
	}

	go func() {
		done <- scheduler.Dispatch(context.Background(), firstEnv, func(context.Context, Envelope) error {
			started <- firstEnv.ID
			<-release
			return nil
		})
	}()
	if got := waitStarted(t, started); got != firstEnv.ID {
		t.Fatalf("first started = %q, want %q", got, firstEnv.ID)
	}
	go func() {
		done <- scheduler.Dispatch(context.Background(), secondEnv, func(context.Context, Envelope) error {
			started <- secondEnv.ID
			return nil
		})
	}()
	select {
	case got := <-started:
		t.Fatalf("delivery dispatch started %q before first released", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if got := waitStarted(t, started); got != secondEnv.ID {
		t.Fatalf("second started = %q, want %q", got, secondEnv.ID)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("Dispatch() error = %v", err)
		}
	}
}

func waitStarted(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduler handler")
		return ""
	}
}

func TestKeyedActorSchedulerPrunesIdleLanes(t *testing.T) {
	scheduler := NewKeyedActorScheduler()
	if err := scheduler.Dispatch(context.Background(), Envelope{ID: "one", Namespace: NamespaceHumanInbound, Kind: KindMessage, From: ActorAddress{Target: "test", Key: "user"}, To: ActorAddress{Target: ActorTypeSession, Key: "s-1"}, SessionID: "s-1", PayloadJSON: `{}`}, func(context.Context, Envelope) error {
		return nil
	}); err != nil {
		t.Fatalf("Dispatch(first) error = %v", err)
	}
	scheduler.mu.Lock()
	if lane := scheduler.lanes["session:s-1"]; lane != nil {
		lane.lastUsed = time.Now().Add(-2 * actorLaneIdleTTL)
	}
	scheduler.mu.Unlock()
	if err := scheduler.Dispatch(context.Background(), Envelope{ID: "two", Namespace: NamespaceHumanInbound, Kind: KindMessage, From: ActorAddress{Target: "test", Key: "user"}, To: ActorAddress{Target: ActorTypeSession, Key: "s-2"}, SessionID: "s-2", PayloadJSON: `{}`}, func(context.Context, Envelope) error {
		return nil
	}); err != nil {
		t.Fatalf("Dispatch(second) error = %v", err)
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	if _, ok := scheduler.lanes["session:s-1"]; ok {
		t.Fatalf("idle lane session:s-1 still present")
	}
	if _, ok := scheduler.lanes["session:s-2"]; !ok {
		t.Fatalf("active lane session:s-2 missing")
	}
}

func TestKeyedActorSchedulerRaceSameLane(t *testing.T) {
	scheduler := NewKeyedActorScheduler()
	env := Envelope{
		ID:          "race-same-lane",
		Namespace:   NamespaceAgentCommand,
		Kind:        KindGoal,
		From:        ActorAddress{Target: ActorTypeTask, Key: "task-race"},
		To:          ActorAddress{Target: ActorTypeAgent, Key: AgentNameExecutor},
		TaskID:      "task-race",
		PayloadJSON: `{}`,
	}

	const workers = 32
	const iterationsPerWorker = 20

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterationsPerWorker)

	var inFlight int64
	var maxInFlight int64

	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := range iterationsPerWorker {
				callEnv := env
				callEnv.ID = fmt.Sprintf("%s-%d-%d", env.ID, worker, i)
				callEnv.CausationID = "worker-" + strconv.Itoa(worker)
				callEnv.CorrelationID = "iter-" + strconv.Itoa(i)
				err := scheduler.Dispatch(context.Background(), callEnv, func(context.Context, Envelope) error {
					current := atomic.AddInt64(&inFlight, 1)
					for {
						seen := atomic.LoadInt64(&maxInFlight)
						if current <= seen || atomic.CompareAndSwapInt64(&maxInFlight, seen, current) {
							break
						}
					}
					time.Sleep(time.Microsecond)
					atomic.AddInt64(&inFlight, -1)
					return nil
				})
				if err != nil {
					errCh <- err
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got := atomic.LoadInt64(&maxInFlight); got > 1 {
		t.Fatalf("same actor lane ran concurrently: max_in_flight=%d, want 1", got)
	}
}

func TestKeyedActorSchedulerRaceDifferentLanes(t *testing.T) {
	scheduler := NewKeyedActorScheduler()
	const workers = 24
	const iterationsPerWorker = 16

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterationsPerWorker)

	var inFlight int64
	var maxInFlight int64

	for worker := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for i := range iterationsPerWorker {
				taskID := "task-race-" + strconv.Itoa(worker%12)
				env := Envelope{
					ID:          fmt.Sprintf("race-different-lanes-%d-%d", worker, i),
					Namespace:   NamespaceAgentCommand,
					Kind:        KindGoal,
					From:        ActorAddress{Target: ActorTypeTask, Key: taskID},
					To:          ActorAddress{Target: ActorTypeAgent, Key: AgentNameExecutor},
					TaskID:      taskID,
					PayloadJSON: `{}`,
				}
				err := scheduler.Dispatch(context.Background(), env, func(context.Context, Envelope) error {
					current := atomic.AddInt64(&inFlight, 1)
					for {
						seen := atomic.LoadInt64(&maxInFlight)
						if current <= seen || atomic.CompareAndSwapInt64(&maxInFlight, seen, current) {
							break
						}
					}
					time.Sleep(time.Microsecond)
					atomic.AddInt64(&inFlight, -1)
					return nil
				})
				if err != nil {
					errCh <- err
					return
				}
				if i%4 == 0 {
					runtime.Gosched()
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got := atomic.LoadInt64(&maxInFlight); got <= 1 {
		t.Fatalf("different actor lanes never overlapped: max_in_flight=%d, want >1", got)
	}
}
