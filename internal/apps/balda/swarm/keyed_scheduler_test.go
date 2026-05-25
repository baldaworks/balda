package swarm

import (
	"context"
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
		{name: "agent", env: Envelope{Namespace: NamespaceAgentCommand, To: ActorAddress{Target: ActorTypeAgent, Key: "executor"}}, want: "agent:executor"},
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
