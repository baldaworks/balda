package goalkeeper_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command/goalkeeper"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/goalkeepercmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type mockDispatcher struct {
	dispatched []actorlayer.Envelope
	err        error
}

func (m *mockDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.dispatched = append(m.dispatched, env)
	return &actortransport.DispatchReceipt{}, nil
}

type mockClearer struct {
	lastLocator     deliverycmd.Locator
	lastRequestedBy string
	lastReason      string
	lastNotify      bool
	err             error
	calls           int
}

func (m *mockClearer) ClearGoal(_ context.Context, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error {
	m.calls++
	m.lastLocator = locator
	m.lastRequestedBy = requestedBy
	m.lastReason = reason
	m.lastNotify = notify
	return m.err
}

type mockChecker struct {
	active bool
	err    error
	calls  int
}

func (m *mockChecker) HasActiveGoalJob(_ context.Context, _ string) (bool, error) {
	m.calls++
	return m.active, m.err
}

func TestGoalkeeperHandlerAccessDenied(t *testing.T) {
	d := &mockDispatcher{}
	h := goalkeeper.New(nil, nil, d, 20, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "goalkeeper",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{},
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
	if !strings.Contains(deliveryPayload.Text, "Only the bot owner or collaborators") {
		t.Errorf("expected access denied message, got %q", deliveryPayload.Text)
	}
}

func TestGoalkeeperHandlerUsageOnEmptyArgs(t *testing.T) {
	d := &mockDispatcher{}
	h := goalkeeper.New(nil, nil, d, 20, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "goalkeeper",
		Args:    "",
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
	if !strings.Contains(deliveryPayload.Text, "Usage:\n/goalkeeper <objective>\n/goalkeeper clear") {
		t.Errorf("expected usage message, got %q", deliveryPayload.Text)
	}
}

func TestGoalkeeperHandlerClearSuccess(t *testing.T) {
	d := &mockDispatcher{}
	c := &mockClearer{}
	h := goalkeeper.New(c, nil, d, 20, zerolog.Nop())

	targetLocator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "chat-1:topic-2", SessionID: "s-123"}
	payload := commandcmd.Payload{
		Name:      "goalkeeper",
		Args:      "clear",
		Locator:   targetLocator,
		Principal: "telegram:user:77",
		Access:    commandcmd.Access{Collaborator: true},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if c.calls != 1 {
		t.Fatalf("clearer calls = %d, want 1", c.calls)
	}
	if c.lastLocator.SessionID != "s-123" {
		t.Errorf("clearer session id = %q, want s-123", c.lastLocator.SessionID)
	}
	if c.lastRequestedBy != "telegram:user:77" {
		t.Errorf("clearer requested by = %q, want telegram:user:77", c.lastRequestedBy)
	}
	if !c.lastNotify {
		t.Errorf("clearer notify = false, want true")
	}
	// On success, no error message is dispatched
	if len(d.dispatched) != 0 {
		t.Errorf("dispatched count = %d, want 0", len(d.dispatched))
	}
}

func TestGoalkeeperHandlerClearError(t *testing.T) {
	d := &mockDispatcher{}
	c := &mockClearer{err: errors.New("cannot clear")}
	h := goalkeeper.New(c, nil, d, 20, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:      "goalkeeper",
		Args:      "clear",
		Locator:   deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Principal: "telegram:user:77",
		Access:    commandcmd.Access{SessionCommands: true},
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
	if !strings.Contains(deliveryPayload.Text, "Could not clear goal run.") {
		t.Errorf("expected failure message, got %q", deliveryPayload.Text)
	}
}

func TestGoalkeeperHandlerStartAlreadyActive(t *testing.T) {
	d := &mockDispatcher{}
	chk := &mockChecker{active: true}
	h := goalkeeper.New(nil, chk, d, 20, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "goalkeeper",
		Args:    "deploy production",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if chk.calls != 1 {
		t.Fatalf("checker calls = %d, want 1", chk.calls)
	}
	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(d.dispatched))
	}
	var deliveryPayload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &deliveryPayload); err != nil {
		t.Fatalf("UnmarshalPayload error = %v", err)
	}
	if !strings.Contains(deliveryPayload.Text, "A goal run is already active for this session.") {
		t.Errorf("expected already active message, got %q", deliveryPayload.Text)
	}
}

func TestGoalkeeperHandlerStartSuccess(t *testing.T) {
	d := &mockDispatcher{}
	chk := &mockChecker{active: false}
	h := goalkeeper.New(nil, chk, d, 15, zerolog.Nop())

	targetLocator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "chat-10:topic-5", SessionID: "s-456"}
	payload := commandcmd.Payload{
		Name:         "goalkeeper",
		Args:         "build feature X",
		Locator:      targetLocator,
		Principal:    "telegram:user:88",
		Access:       commandcmd.Access{Owner: true},
		Presentation: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown},
	}
	err := h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1 (the goalkeeper job envelope)", len(d.dispatched))
	}
	// Verify that the dispatched envelope is indeed the GoalKeeper job start envelope
	env := d.dispatched[0]
	var envPayload goalkeepercmd.EnvelopePayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &envPayload); err != nil {
		t.Fatalf("UnmarshalPayload into EnvelopePayload error = %v", err)
	}
	if envPayload.Goal == nil {
		t.Fatalf("envPayload.Goal is nil")
	}
	jobPayload := envPayload.Goal
	if jobPayload.Objective != "build feature X" {
		t.Errorf("job objective = %q, want 'build feature X'", jobPayload.Objective)
	}
	if jobPayload.Locator.SessionID != "s-456" {
		t.Errorf("job session id = %q, want 's-456'", jobPayload.Locator.SessionID)
	}
	if jobPayload.TransportUserID != "telegram:user:88" {
		t.Errorf("job transport user id = %q, want 'telegram:user:88'", jobPayload.TransportUserID)
	}
	if jobPayload.MaxIterations != 15 {
		t.Errorf("job max iterations = %d, want 15", jobPayload.MaxIterations)
	}
}
