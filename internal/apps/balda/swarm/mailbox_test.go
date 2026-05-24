package swarm

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type recordingWakeBus struct {
	publishCalls atomic.Int64
}

func (b *recordingWakeBus) Publish(context.Context, ActorAddress) error {
	b.publishCalls.Add(1)
	return nil
}

func (*recordingWakeBus) Subscribe(context.Context, MessageHandler) error { return nil }

func (*recordingWakeBus) Close() error { return nil }

func TestMailboxService_PublishShadowPersistsWithoutWake(t *testing.T) {
	ctx := context.Background()
	provider, service, bus := newShadowTestMailboxService(t, ctx)

	env := shadowTestEnvelope("shadow-1", ActorAddress{Target: ActorTypeSession, Key: "s-1"})
	submitted, err := service.PublishShadow(ctx, env)
	if err != nil {
		t.Fatalf("PublishShadow() error = %v", err)
	}
	if !submitted.Published {
		t.Fatal("PublishShadow() Published = false, want true")
	}
	if got, want := submitted.MailboxID, "session:s-1"; got != want {
		t.Fatalf("MailboxID = %q, want %q", got, want)
	}
	if got := bus.publishCalls.Load(); got != 0 {
		t.Fatalf("wake publish calls = %d, want 0", got)
	}

	got, ok, err := provider.Swarm().GetMessage(ctx, env.ID)
	if err != nil {
		t.Fatalf("GetMessage() error = %v", err)
	}
	if !ok || got.Status != baldastate.SwarmMessageStatusShadow {
		t.Fatalf("message = %+v, found=%v, want status shadow", got, ok)
	}
	claimed, err := provider.Swarm().Claim(ctx, "session:s-1", "worker-1", 8, DefaultLeaseDuration)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed messages = %+v, want none", claimed)
	}

	snapshot := service.ShadowMetricsSnapshot()
	if got := snapshot[MetricShadowEnvelopesTotal]; got != 1 {
		t.Fatalf("%s = %d, want 1", MetricShadowEnvelopesTotal, got)
	}
	if got := snapshot[MetricShadowMissingSessionTotal]; got != 0 {
		t.Fatalf("%s = %d, want 0", MetricShadowMissingSessionTotal, got)
	}
	if got := snapshot[MetricShadowDedupeHitsTotal]; got != 0 {
		t.Fatalf("%s = %d, want 0", MetricShadowDedupeHitsTotal, got)
	}
}

func TestMailboxService_PublishShadowTracksDedupeAndMissingSession(t *testing.T) {
	ctx := context.Background()
	_, service, _ := newShadowTestMailboxService(t, ctx)

	first := shadowTestEnvelope("shadow-first", ActorAddress{Target: ActorTypeSession, Key: "s-2"})
	first.SessionID = ""
	first.DedupeKey = "webhook:req-1"
	if _, err := service.PublishShadow(ctx, first); err != nil {
		t.Fatalf("PublishShadow(first) error = %v", err)
	}
	duplicate := shadowTestEnvelope("shadow-duplicate", ActorAddress{Target: ActorTypeSession, Key: "s-2"})
	duplicate.SessionID = ""
	duplicate.DedupeKey = "webhook:req-1"
	submitted, err := service.PublishShadow(ctx, duplicate)
	if err != nil {
		t.Fatalf("PublishShadow(duplicate) error = %v", err)
	}
	if submitted.Published {
		t.Fatal("PublishShadow(duplicate) Published = true, want false")
	}

	service.RecordShadowDispatch()
	snapshot := service.ShadowMetricsSnapshot()
	if got := snapshot[MetricShadowEnvelopesTotal]; got != 2 {
		t.Fatalf("%s = %d, want 2", MetricShadowEnvelopesTotal, got)
	}
	if got := snapshot[MetricShadowMissingSessionTotal]; got != 2 {
		t.Fatalf("%s = %d, want 2", MetricShadowMissingSessionTotal, got)
	}
	if got := snapshot[MetricShadowDedupeHitsTotal]; got != 1 {
		t.Fatalf("%s = %d, want 1", MetricShadowDedupeHitsTotal, got)
	}
	if got := snapshot[MetricShadowDispatchTotal]; got != 1 {
		t.Fatalf("%s = %d, want 1", MetricShadowDispatchTotal, got)
	}
}

func newShadowTestMailboxService(
	t *testing.T,
	ctx context.Context,
) (baldastate.Provider, *MailboxService, *recordingWakeBus) {
	t.Helper()

	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	bus := &recordingWakeBus{}
	service := &MailboxService{
		store:   provider.Swarm(),
		bus:     bus,
		cfg:     Config{Enabled: true, Mode: ModeShadow, Shadow: ShadowConfig{Enabled: true}},
		metrics: NewShadowMetrics(),
	}
	return provider, service, bus
}

func shadowTestEnvelope(id string, to ActorAddress) Envelope {
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
