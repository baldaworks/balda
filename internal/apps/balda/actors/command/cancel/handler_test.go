package cancel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command/cancel"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type mockDispatcher struct {
	dispatched []actorlayer.Envelope
}

func (m *mockDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	m.dispatched = append(m.dispatched, env)
	return &actortransport.DispatchReceipt{}, nil
}

type mockCanceller struct {
	lastLocator     deliverycmd.Locator
	lastRequestedBy string
	lastReason      string
	lastNotify      bool
	err             error
	calls           int
}

func (m *mockCanceller) CancelTurn(_ context.Context, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error {
	m.calls++
	m.lastLocator = locator
	m.lastRequestedBy = requestedBy
	m.lastReason = reason
	m.lastNotify = notify
	return m.err
}

func TestCancelHandlerAccessDenied(t *testing.T) {
	d := &mockDispatcher{}
	c := &mockCanceller{}
	h := cancel.New(c, d, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "cancel",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if c.calls != 0 {
		t.Fatalf("canceller calls = %d, want 0", c.calls)
	}
	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(d.dispatched))
	}
	var deliveryPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &deliveryPayload); err != nil {
		t.Fatalf("UnmarshalPayload error = %v", err)
	}
	if !strings.Contains(deliveryPayload.Text, "Only the bot owner or collaborators") {
		t.Errorf("expected access denied message, got %q", deliveryPayload.Text)
	}
}

func TestCancelHandlerRejectsExtraArgs(t *testing.T) {
	d := &mockDispatcher{}
	c := &mockCanceller{}
	h := cancel.New(c, d, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "cancel",
		Args:    "something",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if c.calls != 0 {
		t.Fatalf("canceller calls = %d, want 0", c.calls)
	}
	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(d.dispatched))
	}
	var deliveryPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &deliveryPayload); err != nil {
		t.Fatalf("UnmarshalPayload error = %v", err)
	}
	if !strings.Contains(deliveryPayload.Text, "Usage: /cancel") {
		t.Errorf("expected usage message, got %q", deliveryPayload.Text)
	}
}

func TestCancelHandlerCancellerUnavailable(t *testing.T) {
	d := &mockDispatcher{}
	h := cancel.New(nil, d, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "cancel",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(d.dispatched))
	}
	var deliveryPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &deliveryPayload); err != nil {
		t.Fatalf("UnmarshalPayload error = %v", err)
	}
	if !strings.Contains(deliveryPayload.Text, "Cancel is unavailable right now.") {
		t.Errorf("expected unavailable message, got %q", deliveryPayload.Text)
	}
}

func TestCancelHandlerCancellerError(t *testing.T) {
	d := &mockDispatcher{}
	c := &mockCanceller{err: errors.New("boom")}
	h := cancel.New(c, d, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:      "cancel",
		Locator:   deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Principal: "telegram:user:42",
		Access:    commandcmd.Access{SessionCommands: true},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if c.calls != 1 {
		t.Fatalf("canceller calls = %d, want 1", c.calls)
	}
	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(d.dispatched))
	}
	var deliveryPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &deliveryPayload); err != nil {
		t.Fatalf("UnmarshalPayload error = %v", err)
	}
	if !strings.Contains(deliveryPayload.Text, "Could not request cancel.") {
		t.Errorf("expected failure message, got %q", deliveryPayload.Text)
	}
}

func TestCancelHandlerSuccessAndSessionIsolation(t *testing.T) {
	d := &mockDispatcher{}
	c := &mockCanceller{}
	h := cancel.New(c, d, zerolog.Nop())

	targetLocator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "chat-99:topic-7", SessionID: "session-xyz-123"}
	payload := commandcmd.Payload{
		Name:      "cancel",
		Locator:   targetLocator,
		Principal: "telegram:user:42",
		Access:    commandcmd.Access{Collaborator: true},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if c.calls != 1 {
		t.Fatalf("canceller calls = %d, want 1", c.calls)
	}
	// Verify session isolation: the canceller was called strictly for targetLocator.SessionID
	if c.lastLocator.SessionID != "session-xyz-123" {
		t.Errorf("canceller session ID = %q, want session-xyz-123", c.lastLocator.SessionID)
	}
	if c.lastLocator.AddressKey != "chat-99:topic-7" {
		t.Errorf("canceller address key = %q, want chat-99:topic-7", c.lastLocator.AddressKey)
	}
	if c.lastRequestedBy != "telegram:user:42" {
		t.Errorf("canceller requestedBy = %q, want telegram:user:42", c.lastRequestedBy)
	}
	if !c.lastNotify {
		t.Errorf("canceller notify = false, want true")
	}

	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(d.dispatched))
	}
	var deliveryPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &deliveryPayload); err != nil {
		t.Fatalf("UnmarshalPayload error = %v", err)
	}
	if !strings.Contains(deliveryPayload.Text, "Cancel requested.") {
		t.Errorf("expected success message, got %q", deliveryPayload.Text)
	}
}
