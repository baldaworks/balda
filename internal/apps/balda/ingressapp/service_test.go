package ingressapp

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/attachment"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
)

type stubAuthorizer struct {
	result Authorization
	err    error
	calls  int
}

func (s *stubAuthorizer) Authorize(context.Context, InboundContext) (Authorization, error) {
	s.calls++
	return s.result, s.err
}

type stubSessionPreparer struct {
	result SessionPreparation
	err    error
	calls  int
}

func (s *stubSessionPreparer) Prepare(context.Context, InboundContext) (SessionPreparation, error) {
	s.calls++
	return s.result, s.err
}

type recordingDispatcher struct {
	receipt   *actortransport.DispatchReceipt
	err       error
	envelopes []actorlayer.Envelope
}

func (d *recordingDispatcher) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, envelope)
	return d.receipt, d.err
}

func TestProcessSettlementBeforeDurableAcceptance(t *testing.T) {
	t.Parallel()

	transientErr := actorlayer.TransientError(errors.New("temporarily unavailable"))
	permanentErr := actorlayer.PolicyError(errors.New("policy denied"))
	tests := []struct {
		name               string
		inbound            turncmd.NormalizedInbound
		authorization      Authorization
		authorizationErr   error
		preparation        SessionPreparation
		preparationErr     error
		dispatchErr        error
		receipt            *actortransport.DispatchReceipt
		wantOutcome        turncmd.InboundOutcome
		wantAuthorizeCalls int
		wantPrepareCalls   int
		wantDispatchCalls  int
	}{
		{
			name:        "invalid inbound terminates",
			inbound:     turncmd.NormalizedInbound{},
			wantOutcome: turncmd.InboundTerminal,
		},
		{
			name:               "empty inbound terminates",
			inbound:            emptyInbound("empty"),
			wantOutcome:        turncmd.InboundTerminal,
			wantAuthorizeCalls: 0,
		},
		{
			name:               "authorization rejection terminates",
			inbound:            validInbound("denied"),
			authorization:      Authorization{Allowed: false},
			wantOutcome:        turncmd.InboundTerminal,
			wantAuthorizeCalls: 1,
		},
		{
			name:               "authorization infrastructure failure retries",
			inbound:            validInbound("auth-retry"),
			authorizationErr:   transientErr,
			wantOutcome:        turncmd.InboundRetry,
			wantAuthorizeCalls: 1,
		},
		{
			name:               "authorization policy error terminates",
			inbound:            validInbound("auth-terminal"),
			authorizationErr:   permanentErr,
			wantOutcome:        turncmd.InboundTerminal,
			wantAuthorizeCalls: 1,
		},
		{
			name:               "session rejection terminates",
			inbound:            validInbound("session-terminal"),
			authorization:      Authorization{Allowed: true},
			preparation:        SessionPreparation{Ready: false},
			wantOutcome:        turncmd.InboundTerminal,
			wantAuthorizeCalls: 1,
			wantPrepareCalls:   1,
		},
		{
			name:               "session infrastructure failure retries",
			inbound:            validInbound("session-retry"),
			authorization:      Authorization{Allowed: true},
			preparationErr:     transientErr,
			wantOutcome:        turncmd.InboundRetry,
			wantAuthorizeCalls: 1,
			wantPrepareCalls:   1,
		},
		{
			name:               "dispatch failure retries",
			inbound:            validInbound("dispatch-retry"),
			authorization:      Authorization{Allowed: true},
			preparation:        SessionPreparation{Ready: true},
			dispatchErr:        transientErr,
			wantOutcome:        turncmd.InboundRetry,
			wantAuthorizeCalls: 1,
			wantPrepareCalls:   1,
			wantDispatchCalls:  1,
		},
		{
			name:               "dispatch policy failure terminates",
			inbound:            validInbound("dispatch-terminal"),
			authorization:      Authorization{Allowed: true},
			preparation:        SessionPreparation{Ready: true},
			dispatchErr:        permanentErr,
			wantOutcome:        turncmd.InboundTerminal,
			wantAuthorizeCalls: 1,
			wantPrepareCalls:   1,
			wantDispatchCalls:  1,
		},
		{
			name:               "missing receipt retries",
			inbound:            validInbound("missing-receipt"),
			authorization:      Authorization{Allowed: true},
			preparation:        SessionPreparation{Ready: true},
			wantOutcome:        turncmd.InboundRetry,
			wantAuthorizeCalls: 1,
			wantPrepareCalls:   1,
			wantDispatchCalls:  1,
		},
		{
			name:               "receipt accepts inbound",
			inbound:            validInbound("accepted"),
			authorization:      Authorization{Allowed: true},
			preparation:        SessionPreparation{Ready: true},
			receipt:            &actortransport.DispatchReceipt{MsgID: "accepted"},
			wantOutcome:        turncmd.InboundAccepted,
			wantAuthorizeCalls: 1,
			wantPrepareCalls:   1,
			wantDispatchCalls:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			authorizer := &stubAuthorizer{result: test.authorization, err: test.authorizationErr}
			sessions := &stubSessionPreparer{result: test.preparation, err: test.preparationErr}
			dispatcher := &recordingDispatcher{receipt: test.receipt, err: test.dispatchErr}
			service, err := New(authorizer, sessions, dispatcher)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			result, _ := service.Process(context.Background(), test.inbound)
			if result.Settlement.Outcome != test.wantOutcome {
				t.Fatalf("settlement outcome = %q, want %q", result.Settlement.Outcome, test.wantOutcome)
			}
			if authorizer.calls != test.wantAuthorizeCalls {
				t.Errorf("authorize calls = %d, want %d", authorizer.calls, test.wantAuthorizeCalls)
			}
			if sessions.calls != test.wantPrepareCalls {
				t.Errorf("prepare calls = %d, want %d", sessions.calls, test.wantPrepareCalls)
			}
			if len(dispatcher.envelopes) != test.wantDispatchCalls {
				t.Errorf("dispatch calls = %d, want %d", len(dispatcher.envelopes), test.wantDispatchCalls)
			}
		})
	}
}

func TestProcessPublishesOneSessionActorEnvelopeWithStableIdentity(t *testing.T) {
	t.Parallel()

	const stableID = "provider:group-17"
	authorizer := &stubAuthorizer{result: Authorization{Allowed: true}}
	sessions := &stubSessionPreparer{result: SessionPreparation{
		Ready:           true,
		UserID:          "runtime-user",
		RequesterUserID: "transport-user",
		AgentSessionID:  "agent-session",
		TopicID:         77,
	}}
	dispatcher := &recordingDispatcher{receipt: &actortransport.DispatchReceipt{MsgID: stableID}}
	service, err := New(authorizer, sessions, dispatcher)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	inbound := validInbound(" " + stableID + " ")
	inbound.Attachments = []attachment.Descriptor{
		{Kind: attachment.KindDocument, FileID: "first"},
		{Kind: attachment.KindDocument, FileID: "second"},
	}

	result, err := service.Process(context.Background(), inbound)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result.Settlement.Outcome != turncmd.InboundAccepted || result.InboundID != turncmd.InboundID(stableID) || result.DedupeKey != stableID {
		t.Fatalf("result = %+v, want accepted stable identity", result)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatch calls = %d, want 1", len(dispatcher.envelopes))
	}
	envelope := dispatcher.envelopes[0]
	if envelope.To.Target != "session" || envelope.To.Key != inbound.Locator.SessionID {
		t.Fatalf("destination = %+v, want SessionActor %q", envelope.To, inbound.Locator.SessionID)
	}
	if envelope.DedupeKey != stableID {
		t.Fatalf("envelope dedupe key = %q, want stable inbound identity", envelope.DedupeKey)
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(envelope.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.UserID != "runtime-user" || payload.RequesterUserID != "transport-user" || payload.AgentSessionID != "agent-session" {
		t.Fatalf("prepared identity = %+v", payload)
	}
	if payload.DeliveryFormat != deliveryfmt.DeliveryFormatRichMarkdown {
		t.Fatalf("delivery format = %q, want %q", payload.DeliveryFormat, deliveryfmt.DeliveryFormatRichMarkdown)
	}
	if len(payload.Attachments) != 2 || payload.Attachments[0].FileID != "first" || payload.Attachments[1].FileID != "second" {
		t.Fatalf("attachments = %+v, want stable order", payload.Attachments)
	}
}

func TestProcessDuplicateReceiptStillAcceptsStableIdentity(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{receipt: &actortransport.DispatchReceipt{MsgID: "duplicate", Duplicate: true}}
	service, err := New(
		&stubAuthorizer{result: Authorization{Allowed: true}},
		&stubSessionPreparer{result: SessionPreparation{Ready: true}},
		dispatcher,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := service.Process(context.Background(), validInbound("provider:duplicate"))
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if result.Settlement.Outcome != turncmd.InboundAccepted || result.Receipt == nil || !result.Receipt.Duplicate {
		t.Fatalf("result = %+v, want accepted duplicate receipt", result)
	}
	if len(dispatcher.envelopes) != 1 || dispatcher.envelopes[0].DedupeKey != "provider:duplicate" {
		t.Fatalf("dispatched envelopes = %+v, want one stable identity", dispatcher.envelopes)
	}
}

func validInbound(id string) turncmd.NormalizedInbound {
	return turncmd.NormalizedInbound{
		ID:             turncmd.InboundID(id),
		Text:           "hello",
		Locator:        baldasession.SessionLocator{SessionID: "tg-9001-77", ChannelType: deliveryfmt.TransportTelegram, AddressKey: "9001:77"},
		UserID:         "transport-user",
		DeliveryFormat: deliveryfmt.DeliveryFormatRichMarkdown,
		Source:         turncmd.SourceTelegram,
	}
}

func emptyInbound(id string) turncmd.NormalizedInbound {
	inbound := validInbound(id)
	inbound.Text = ""
	return inbound
}
