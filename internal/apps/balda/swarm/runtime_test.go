package swarm

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type testWakeBus struct{}

func (testWakeBus) Publish(context.Context, ActorAddress) error     { return nil }
func (testWakeBus) Subscribe(context.Context, MessageHandler) error { return nil }
func (testWakeBus) Close() error                                    { return nil }

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

func TestRuntime_UnknownActorDeadLetters(t *testing.T) {
	ctx := context.Background()
	provider, service := newRuntimeTestMailboxService(t, ctx)
	env := runtimeTestEnvelope("unknown", ActorAddress{Target: ActorTypeSession, Key: "s-1"})
	published, err := service.Publish(ctx, env)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	runtime := newRuntimeForTest(service, NewRegistry())
	runtime.runMailbox(ctx, published.MailboxID)

	got, ok, err := provider.Swarm().GetMessage(ctx, env.ID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if !ok || got.Status != baldastate.SwarmMessageStatusDead {
		t.Fatalf("message = %+v, found=%v, want dead", got, ok)
	}
}

func TestRuntime_ActorSuccessAcks(t *testing.T) {
	ctx := context.Background()
	provider, service := newRuntimeTestMailboxService(t, ctx)
	actor := &testActor{address: WildcardAddress(ActorTypeSession)}
	registry := NewRegistry()
	if err := registry.Register(actor); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	env := runtimeTestEnvelope("ok", ActorAddress{Target: ActorTypeSession, Key: "s-1"})
	published, err := service.Publish(ctx, env)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	newRuntimeForTest(service, registry).runMailbox(ctx, published.MailboxID)

	got, ok, err := provider.Swarm().GetMessage(ctx, env.ID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if actor.calls != 1 {
		t.Fatalf("actor calls = %d, want 1", actor.calls)
	}
	if !ok || got.Status != baldastate.SwarmMessageStatusAcked {
		t.Fatalf("message = %+v, found=%v, want acked", got, ok)
	}
}

func TestRuntime_ActorErrorsSettleByKind(t *testing.T) {
	ctx := context.Background()
	provider, service := newRuntimeTestMailboxService(t, ctx)
	registry := NewRegistry()
	actor := &testActor{address: WildcardAddress(ActorTypeSession), err: TransientError(fmt.Errorf("temporary"))}
	if err := registry.Register(actor); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	env := runtimeTestEnvelope("retry", ActorAddress{Target: ActorTypeSession, Key: "s-1"})
	env.MaxAttempts = 2
	published, err := service.Publish(ctx, env)
	if err != nil {
		t.Fatalf("Publish(retry) error = %v", err)
	}
	newRuntimeForTest(service, registry).runMailbox(ctx, published.MailboxID)
	got, ok, err := provider.Swarm().GetMessage(ctx, env.ID)
	if err != nil {
		t.Fatalf("GetMessage(retry) error = %v", err)
	}
	if !ok || got.Status != baldastate.SwarmMessageStatusRetry {
		t.Fatalf("message = %+v, found=%v, want retry", got, ok)
	}

	actor.err = PermanentError(fmt.Errorf("permanent"))
	deadEnv := runtimeTestEnvelope("dead", ActorAddress{Target: ActorTypeSession, Key: "s-1"})
	published, err = service.Publish(ctx, deadEnv)
	if err != nil {
		t.Fatalf("Publish(dead) error = %v", err)
	}
	newRuntimeForTest(service, registry).runMailbox(ctx, published.MailboxID)
	got, ok, err = provider.Swarm().GetMessage(ctx, deadEnv.ID)
	if err != nil {
		t.Fatalf("GetMessage(dead) error = %v", err)
	}
	if !ok || got.Status != baldastate.SwarmMessageStatusDead {
		t.Fatalf("message = %+v, found=%v, want dead", got, ok)
	}
}

func newRuntimeTestMailboxService(t *testing.T, ctx context.Context) (baldastate.Provider, *MailboxService) {
	t.Helper()
	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider, &MailboxService{store: provider.Swarm(), bus: testWakeBus{}, cfg: Config{Enabled: true, Mode: ModeMailbox}}
}

func newRuntimeForTest(service *MailboxService, registry ActorRegistry) *Runtime {
	return &Runtime{
		mailboxes: service,
		registry:  registry,
		workerID:  "test-worker",
		draining:  make(map[string]struct{}),
	}
}

func runtimeTestEnvelope(id string, to ActorAddress) Envelope {
	return Envelope{
		ID:          id,
		Namespace:   NamespaceHumanInbound,
		Kind:        KindMessage,
		From:        ActorAddress{Target: "test", Key: "source"},
		To:          to,
		SessionID:   to.Key,
		PayloadJSON: `{"ok":true}`,
	}
}
