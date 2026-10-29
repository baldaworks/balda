package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/statusapp"
	"github.com/baldaworks/balda/internal/apps/balda/statusfmt"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const statusTestModel = "gpt-6"

type statusSessions struct {
	info    session.TopicSessionInfo
	infoErr error
	calls   int
}

func (s *statusSessions) GetSessionInfo(context.Context, string) (session.TopicSessionInfo, error) {
	s.calls++
	return s.info, s.infoErr
}

type statusTurns struct {
	turn appports.CurrentTurn
	ok   bool
}

type statusCoordinator struct{ snapshot statusapp.Snapshot }

func (s statusCoordinator) Snapshot(context.Context, deliverycmd.Locator) statusapp.Snapshot {
	return s.snapshot
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
	requestedModel := statusTestModel
	sessions := &statusSessions{
		info: session.TopicSessionInfo{Status: "active", ProviderID: "codex", RequestedModel: &requestedModel, ActiveModel: statusTestModel, ActiveModelKind: session.ModelObservationProviderObserved},
	}
	turns := statusTurns{ok: true, turn: appports.CurrentTurn{StartedAt: startedAt, PendingTurns: 2, Text: strings.Repeat("я", 1001)}}
	registry, dispatcher := &statusRegistry{}, &statusDispatcher{}
	coordinator := statusCoordinator{snapshot: statusapp.Snapshot{State: statusapp.StateWorking, HasTurn: true, Turn: turnstatuscmd.Snapshot{State: turnstatuscmd.StateActive, StartedAt: startedAt, LastActivityAt: startedAt.Add(time.Second), ActivityKind: turnstatuscmd.ActivityProvider}}}
	handler := New(sessions, turns, coordinator, registry, dispatcher, zerolog.Nop())
	payload := statusPayload()

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-1"}, payload); err != nil {
		t.Fatal(err)
	}
	if registry.transport != "telegram" || len(dispatcher.envelopes) != 1 {
		t.Fatalf("transport=%q deliveries=%d", registry.transport, len(dispatcher.envelopes))
	}
	got := registry.response
	if got.ProviderID != "codex" || got.ProviderType != unavailableValue || got.RequestedModel != statusTestModel || !got.HasRequestedModel || got.ActiveModel != statusTestModel || got.ActiveModelKind != string(session.ModelObservationProviderObserved) || got.Model != "" || got.ReasoningEffort != unavailableValue {
		t.Errorf("provider status = %+v", got)
	}
	if got.SessionState != "active" || !got.TurnActive || got.TurnStartedAt != startedAt || got.PendingTurns != 2 {
		t.Errorf("runtime status = %+v", got)
	}
	if got.ConversationState != "working" || got.ActivityKind != "provider" {
		t.Errorf("conversation status = %+v", got)
	}
	if len([]rune(got.RequestExcerpt)) != maxUserTextRunes || !got.RequestTruncated {
		t.Errorf("request runes=%d truncated=%v", len([]rune(got.RequestExcerpt)), got.RequestTruncated)
	}
	if dispatcher.envelopes[0].DedupeKey != "op-1:delivery:status-result" {
		t.Errorf("dedupe key = %q", dispatcher.envelopes[0].DedupeKey)
	}
}

func TestHandlerFailsClosedWhenRendererIsUnavailable(t *testing.T) {
	wantErr := errors.New("renderer unavailable")
	handler := New(&statusSessions{}, statusTurns{}, nil, &statusRegistry{err: wantErr}, &statusDispatcher{}, zerolog.Nop())

	err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-2"}, statusPayload())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Handle() error = %v, want %v", err, wantErr)
	}
}

func TestHandlerReportsNoActiveTurn(t *testing.T) {
	registry := &statusRegistry{}
	handler := New(&statusSessions{infoErr: errors.New("not found")}, statusTurns{}, nil, registry, &statusDispatcher{}, zerolog.Nop())

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-3"}, statusPayload()); err != nil {
		t.Fatal(err)
	}
	if registry.response.SessionState != unavailableValue || registry.response.ProviderType != unavailableValue || registry.response.HasRequestedModel || registry.response.ActiveModel != "" || registry.response.Model != "" || registry.response.ReasoningEffort != unavailableValue || registry.response.TurnActive || registry.response.RequestExcerpt != "" {
		t.Fatalf("response = %+v", registry.response)
	}
}

func TestHandlerProjectsOnlyBoundedCommittedRecoveryStatus(t *testing.T) {
	const secret = "binding-secret-do-not-expose"
	requested := "model-requested"
	registry := &statusRegistry{}
	handler := New(
		&statusSessions{info: session.TopicSessionInfo{
			Status:          "recovery_required",
			ProviderID:      "provider-a",
			RequestedModel:  &requested,
			ActiveModel:     "model-incumbent",
			ActiveModelKind: session.ModelObservationLocallyExecuted,
			Recovery:        "ambiguous",
			ArchiveID:       secret,
			Outcome:         secret,
			WorkspaceDir:    secret,
			BranchName:      secret,
			UserID:          secret,
		}},
		statusTurns{},
		nil,
		registry,
		&statusDispatcher{},
		zerolog.Nop(),
	)

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-recovery"}, statusPayload()); err != nil {
		t.Fatal(err)
	}
	got := registry.response
	if got.SessionState != "recovery_required" || got.ProviderID != "provider-a" || got.RequestedModel != requested || !got.HasRequestedModel || got.ActiveModel != "model-incumbent" || got.ActiveModelKind != string(session.ModelObservationLocallyExecuted) || got.Model != "" {
		t.Fatalf("bounded recovery response = %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "ambiguous") {
		t.Fatalf("protected recovery evidence leaked: %s", encoded)
	}
}

func TestHandlerBoundsSessionMetadataFailureLog(t *testing.T) {
	const secret = "store-password=do-not-log"
	var logs bytes.Buffer
	registry := &statusRegistry{}
	handler := New(
		&statusSessions{infoErr: errors.New(secret)},
		statusTurns{},
		nil,
		registry,
		&statusDispatcher{},
		zerolog.New(&logs),
	)

	if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-secret"}, statusPayload()); err != nil {
		t.Fatal(err)
	}
	if registry.response.SessionState != unavailableValue || registry.response.ActiveModel != "" || registry.response.HasRequestedModel {
		t.Fatalf("response = %+v", registry.response)
	}

	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event); err != nil {
		t.Fatalf("decode log: %v; output=%q", err, logs.String())
	}
	if _, ok := event["error"]; ok {
		t.Fatalf("raw error field leaked: %v", event)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("secret leaked in log: %q", logs.String())
	}
	if event["component"] != "balda.actors.command.status" ||
		event["error_class"] != "session_metadata_unavailable" ||
		event["message"] != "failed to read status session metadata" {
		t.Fatalf("bounded log = %v", event)
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
			sessions := &statusSessions{}
			handler := New(sessions, statusTurns{}, nil, registry, dispatcher, zerolog.Nop())
			if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "op-4"}, test.payload); err != nil {
				t.Fatal(err)
			}
			if sessions.calls != 0 || registry.transport != "" || len(dispatcher.envelopes) != 1 {
				t.Fatalf("session calls=%d renderer transport=%q deliveries=%d", sessions.calls, registry.transport, len(dispatcher.envelopes))
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
