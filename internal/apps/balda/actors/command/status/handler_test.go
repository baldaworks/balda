package status

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type statusSessions struct {
	info    session.TopicSessionInfo
	infoErr error
	meta    session.AgentMetadata
}

func (s *statusSessions) GetSessionInfo(context.Context, string) (session.TopicSessionInfo, error) {
	return s.info, s.infoErr
}
func (*statusSessions) BaldaProviderID() string                         { return "codex" }
func (s *statusSessions) GetAgentMetadata(string) session.AgentMetadata { return s.meta }

type statusTurns struct {
	turn appports.CurrentTurn
	ok   bool
}

func (s statusTurns) CurrentTurn(string) (appports.CurrentTurn, bool) { return s.turn, s.ok }

type statusRegistry struct {
	transport string
	response  statusfmt.Response
	err       error
}

func (r *statusRegistry) RenderStructured(_ context.Context, transport string, typ deliveryfmt.MessageType, env any) (deliveryfmt.StructuredPresentation, error) {
	r.transport = transport
	if typ != statusfmt.ResponseDescriptor.Type {
		return deliveryfmt.StructuredPresentation{}, errors.New("unexpected message type")
	}
	typed, ok := env.(deliveryfmt.StructuredEnvelope[statusfmt.Response])
	if !ok {
		return deliveryfmt.StructuredPresentation{}, errors.New("unexpected envelope type")
	}
	r.response = typed.Body
	if r.err != nil {
		return deliveryfmt.StructuredPresentation{}, r.err
	}
	return deliveryfmt.StructuredPresentation{Text: "formatted", DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown}, nil
}

type statusDispatcher struct{ envelopes []actorlayer.Envelope }

func (d *statusDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestHandlerUsesStructuredStatusRenderer(t *testing.T) {
	startedAt := time.Date(2026, time.October, 21, 12, 30, 0, 0, time.UTC)
	sessions := &statusSessions{
		info: session.TopicSessionInfo{Status: "active"},
		meta: session.AgentMetadata{Type: "openai", Model: "gpt-6", ReasoningEffort: "high"},
	}
	turns := statusTurns{ok: true, turn: appports.CurrentTurn{StartedAt: startedAt, PendingTurns: 2, Text: strings.Repeat("я", 1001)}}
	registry, dispatcher := &statusRegistry{}, &statusDispatcher{}
	handler := New(sessions, turns, registry, dispatcher, zerolog.Nop())
	payload := statusPayload()

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-1"}, payload); err != nil {
		t.Fatal(err)
	}
	if registry.transport != "telegram" || len(dispatcher.envelopes) != 1 {
		t.Fatalf("transport=%q deliveries=%d", registry.transport, len(dispatcher.envelopes))
	}
	got := registry.response
	if got.ProviderID != "codex" || got.ProviderType != "openai" || got.Model != "gpt-6" || got.ReasoningEffort != "high" {
		t.Errorf("provider status = %+v", got)
	}
	if got.SessionState != "active" || !got.TurnActive || got.TurnStartedAt != startedAt || got.PendingTurns != 2 {
		t.Errorf("runtime status = %+v", got)
	}
	if len([]rune(got.UserText)) != maxUserTextRunes || !got.UserTextTruncated {
		t.Errorf("user text runes=%d truncated=%v", len([]rune(got.UserText)), got.UserTextTruncated)
	}
	if dispatcher.envelopes[0].DedupeKey != "op-1:delivery:status-result" {
		t.Errorf("dedupe key = %q", dispatcher.envelopes[0].DedupeKey)
	}
}

func TestHandlerFailsClosedWhenRendererIsUnavailable(t *testing.T) {
	wantErr := errors.New("renderer unavailable")
	handler := New(&statusSessions{}, statusTurns{}, &statusRegistry{err: wantErr}, &statusDispatcher{}, zerolog.Nop())

	err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-2"}, statusPayload())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Handle() error = %v, want %v", err, wantErr)
	}
}

func TestHandlerReportsNoActiveTurn(t *testing.T) {
	registry := &statusRegistry{}
	handler := New(&statusSessions{infoErr: errors.New("not found")}, statusTurns{}, registry, &statusDispatcher{}, zerolog.Nop())

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-3"}, statusPayload()); err != nil {
		t.Fatal(err)
	}
	if registry.response.SessionState != unavailableValue || registry.response.ProviderType != unavailableValue || registry.response.Model != unavailableValue || registry.response.ReasoningEffort != unavailableValue || registry.response.TurnActive || registry.response.UserText != "" {
		t.Fatalf("response = %+v", registry.response)
	}
}

func TestHandlerRejectsUnauthorizedAndInvalidRequestsBeforeRendering(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload commandcmd.Payload
	}{
		{name: "unauthorized", payload: func() commandcmd.Payload { p := statusPayload(); p.Access = commandcmd.Access{}; return p }()},
		{name: "arguments", payload: func() commandcmd.Payload { p := statusPayload(); p.Args = "extra"; return p }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, dispatcher := &statusRegistry{}, &statusDispatcher{}
			handler := New(&statusSessions{}, statusTurns{}, registry, dispatcher, zerolog.Nop())
			if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-4"}, test.payload); err != nil {
				t.Fatal(err)
			}
			if registry.transport != "" || len(dispatcher.envelopes) != 1 {
				t.Fatalf("renderer transport=%q deliveries=%d", registry.transport, len(dispatcher.envelopes))
			}
		})
	}
}

func statusPayload() commandcmd.Payload {
	return commandcmd.Payload{
		Name:      "status",
		Locator:   deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:2", SessionID: "tg-1-2"},
		Transport: "telegram",
		Principal: "tg-3",
		Access:    commandcmd.Access{WorkspaceMember: true},
	}
}
