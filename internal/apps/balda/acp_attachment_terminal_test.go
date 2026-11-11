package balda

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturn"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/rs/zerolog"
)

// The composition owns the concrete provider; the actor/executor consumes only
// the existing generic Agent/event and delivery-acceptance contracts.
func TestACPIndeterminateAttachmentSettlesOrdinaryTurn(t *testing.T) {
	cfg, norma, trace := startupRegressionConfig(t, "60s", "hang-session/new")
	fallbackTrace := filepath.Join(t.TempDir(), "fallback.jsonl")
	fallback := norma.Providers["fixture"]
	fallback.GenericACP = &agentconfig.ACPConfig{Cmd: append([]string(nil), fallback.GenericACP.Cmd...)}
	fallback.GenericACP.Cmd[len(fallback.GenericACP.Cmd)-3] = "0s"
	fallback.GenericACP.Cmd[len(fallback.GenericACP.Cmd)-2] = fallbackTrace
	norma.Providers["fallback"] = fallback
	norma.Providers["pool"] = agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"fixture", "fallback"}}}
	cfg.Balda.Provider = "pool"
	f := newStartupComposition(t, cfg, norma)
	locator := telegram.NewLocator(101, 77)
	sess, err := f.sessions.EnsureSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "fixture-user"}, "attachment")
	if err != nil {
		t.Fatal(err)
	}
	bus := &attachmentFinalBus{}
	service := sessionturnapp.NewTurnExecutionServiceWithJobEvents(bus, nil, f.sessions, zerolog.Nop(), automode.DefaultMaxTurns)
	service.SetFinalArchive(chatarchiveapp.New(f.provider.ChatArchive()))
	registry := deliveryfmt.NewStructuredRegistry()
	if err := deliveryfmt.RegisterStructuredRenderer(registry, deliveryfmt.TransportTelegram, deliveryfmt.TurnFailureDescriptor, attachmentFailureRenderer{}); err != nil {
		t.Fatal(err)
	}
	service.SetStructuredMessageRegistry(registry)
	queued := sessionturn.New(sessionturnapp.NewSessionAccessor(f.sessions), sessionturnapp.NewProviderTurnExecutorFromService(service), nil, zerolog.Nop())
	turns := actors.NewTurnDispatcher(zerolog.Nop())
	defer func() {
		if err := turns.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	actor := actors.NewSessionActor(actors.SessionActorConfig{Turns: turns, Runner: queued, Dispatcher: bus})
	payload := turncmd.SessionTurnPayload{Text: "ordinary input", UserID: sess.GetUserID(), MessageID: 1, Source: turncmd.SourceTelegram, Deliver: true, DedupeKey: "ordinary-attachment", Locator: deliverycmd.Locator{SessionID: sess.GetSessionID(), ChannelType: locator.ChannelType, AddressKey: locator.AddressKey, AddressJSON: locator.AddressJSON}}
	env, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	env.ID, env.Attempt, env.MaxAttempts = "ordinary-attachment", 0, 5
	if err := actor.Handle(t.Context(), env); err != nil {
		t.Fatalf("ordinary attempt0 did not settle accepted failed final: %v", err)
	}
	finals := bus.accepted()
	if len(finals) != 1 || finals[0].Locator != payload.Locator || !strings.Contains(finals[0].Text, "Reset or start a new conversation") {
		t.Fatalf("accepted failed final=%+v", finals)
	}
	counts := map[string]int{}
	for _, r := range readStartupTrace(t, trace) {
		counts[r.Method]++
	}
	if counts["launch"] != 1 || counts[startupInitialize] != 1 || counts["session/new"] != 1 || counts[startupResume] != 0 || counts["session/prompt"] != 0 {
		t.Fatalf("provider RPC counts=%v", counts)
	}
	if _, err := os.Stat(fallbackTrace); !os.IsNotExist(err) {
		t.Fatalf("session-scoped failed final selected another pool member: %v", err)
	}
}

type attachmentFinalBus struct {
	mu         sync.Mutex
	deliveries []deliverycmd.Payload
}

func (b *attachmentFinalBus) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var p deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &p); err != nil {
		return nil, err
	}
	if p.Mode == deliverycmd.ModeProgress {
		return &actortransport.DispatchReceipt{}, nil
	}
	b.deliveries = append(b.deliveries, p)
	return &actortransport.DispatchReceipt{}, nil
}

func (b *attachmentFinalBus) accepted() []deliverycmd.Payload {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]deliverycmd.Payload(nil), b.deliveries...)
}

type attachmentFailureRenderer struct{}

func (attachmentFailureRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[deliveryfmt.TurnFailure]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{Text: env.Body.Text, DeliveryFormat: deliveryfmt.DeliveryFormatNone}, nil
}
