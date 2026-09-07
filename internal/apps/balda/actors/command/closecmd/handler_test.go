package closecmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type fakeCloseSessions struct {
	resetCalls  []session.SessionLocator
	resetReason session.BoundaryReason
	resetErr    error
}

func (s *fakeCloseSessions) ResetSessionWithReason(_ context.Context, locator session.SessionLocator, reason session.BoundaryReason) error {
	if s.resetErr != nil {
		return s.resetErr
	}
	s.resetCalls = append(s.resetCalls, locator)
	s.resetReason = reason
	return nil
}

type fakeCloseCanceller struct {
	cancelCalls []session.SessionLocator
	actor       string
	reason      string
}

func (c *fakeCloseCanceller) CancelWork(_ context.Context, locator session.SessionLocator, actor string, reason string) error {
	c.cancelCalls = append(c.cancelCalls, locator)
	c.actor = actor
	c.reason = reason
	return nil
}

type fakeCloseControl struct {
	cancelSessionCalls []deliverycmd.Locator
}

func (c *fakeCloseControl) CancelSession(_ context.Context, locator deliverycmd.Locator, _ string, _ string, _ bool) error {
	c.cancelSessionCalls = append(c.cancelSessionCalls, locator)
	return nil
}

type fakeCloseTopicCloser struct {
	isTopic        bool
	closedLocators []deliverycmd.Locator
}

func (c *fakeCloseTopicCloser) CloseTopic(_ context.Context, locator deliverycmd.Locator) error {
	c.closedLocators = append(c.closedLocators, locator)
	return nil
}

func (c *fakeCloseTopicCloser) IsTopic(_ deliverycmd.Locator) bool {
	return c.isTopic
}

type recordingCloseDispatcher struct {
	dispatched []actorlayer.Envelope
}

func (r *recordingCloseDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	r.dispatched = append(r.dispatched, env)
	return &actortransport.DispatchReceipt{}, nil
}

func lastDispatchedText(t *testing.T, rd *recordingCloseDispatcher) string {
	t.Helper()
	if len(rd.dispatched) == 0 {
		t.Fatalf("no envelopes dispatched")
	}
	env := rd.dispatched[len(rd.dispatched)-1]
	var payload actors.DeliveryPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal delivery payload: %v", err)
	}
	return payload.Text
}

func TestCloseHandler_Unauthorized(t *testing.T) {
	sessions := &fakeCloseSessions{}
	h := New(sessions, nil, nil, nil, &recordingCloseDispatcher{}, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:   commandcmd.SchemaVersion,
		Name:      "close",
		Transport: "telegram",
		Locator:   deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-0", AddressKey: "1:0"},
		Principal: "tg-1",
		Access:    commandcmd.Access{SessionCommands: false},
	}
	dispatcher := &recordingCloseDispatcher{}
	h.dispatcher = dispatcher
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-1"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Only the bot owner or collaborators can use this command.") {
		t.Errorf("got %q, want permission denied", got)
	}
}

func TestCloseHandler_GroupChat_Rejects(t *testing.T) {
	sessions := &fakeCloseSessions{}
	dispatcher := &recordingCloseDispatcher{}
	h := New(sessions, nil, nil, nil, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "close",
		Transport:    "telegram",
		Locator:      deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg--100-33", AddressKey: "-100:33"},
		Principal:    "tg-1",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: false},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-2"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "This command is only available in direct messages.") {
		t.Errorf("got %q, want DM only message", got)
	}
}

func TestCloseHandler_Args_ShowsUsage(t *testing.T) {
	sessions := &fakeCloseSessions{}
	dispatcher := &recordingCloseDispatcher{}
	h := New(sessions, nil, nil, nil, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "close",
		Args:         "extra",
		Transport:    "telegram",
		Locator:      deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-0", AddressKey: "1:0"},
		Principal:    "tg-1",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-3"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Usage: /close") {
		t.Errorf("got %q, want usage", got)
	}
}

func TestCloseHandler_CloseTopic(t *testing.T) {
	sessions := &fakeCloseSessions{}
	canceller := &fakeCloseCanceller{}
	control := &fakeCloseControl{}
	closer := &fakeCloseTopicCloser{isTopic: true}
	dispatcher := &recordingCloseDispatcher{}
	h := New(sessions, canceller, control, closer, dispatcher, zerolog.Nop())

	loc := deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-9001-123", AddressKey: "9001:123"}
	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "close",
		Transport:    "telegram",
		Locator:      loc,
		Principal:    "tg-101",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-4"}, payload); err != nil {
		t.Fatal(err)
	}
	if len(canceller.cancelCalls) != 1 || canceller.actor != "command.close" {
		t.Errorf("canceller calls = %+v", canceller.cancelCalls)
	}
	if len(control.cancelSessionCalls) != 1 || control.cancelSessionCalls[0].SessionID != loc.SessionID {
		t.Errorf("control calls = %+v", control.cancelSessionCalls)
	}
	if len(sessions.resetCalls) != 1 || sessions.resetReason != session.BoundaryReasonClose {
		t.Errorf("sessions resetCalls = %+v, reason = %v", sessions.resetCalls, sessions.resetReason)
	}
	if len(closer.closedLocators) != 1 || closer.closedLocators[0].SessionID != loc.SessionID {
		t.Errorf("closed locators = %+v", closer.closedLocators)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Closing this topic and resetting session history.") {
		t.Errorf("got %q, want Closing this topic", got)
	}
}

func TestCloseHandler_CloseRoot(t *testing.T) {
	sessions := &fakeCloseSessions{}
	canceller := &fakeCloseCanceller{}
	control := &fakeCloseControl{}
	closer := &fakeCloseTopicCloser{isTopic: false}
	dispatcher := &recordingCloseDispatcher{}
	h := New(sessions, canceller, control, closer, dispatcher, zerolog.Nop())

	loc := deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-9001-0", AddressKey: "9001:0"}
	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "close",
		Transport:    "telegram",
		Locator:      loc,
		Principal:    "tg-101",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-5"}, payload); err != nil {
		t.Fatal(err)
	}
	if len(canceller.cancelCalls) != 1 {
		t.Errorf("canceller calls = %+v", canceller.cancelCalls)
	}
	if len(control.cancelSessionCalls) != 1 {
		t.Errorf("control calls = %+v", control.cancelSessionCalls)
	}
	if len(sessions.resetCalls) != 1 || sessions.resetReason != session.BoundaryReasonClose {
		t.Errorf("sessions resetCalls = %+v", sessions.resetCalls)
	}
	if len(closer.closedLocators) != 0 {
		t.Errorf("expected no CloseTopic calls for root, got %d", len(closer.closedLocators))
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Session history reset.") {
		t.Errorf("got %q, want Session history reset.", got)
	}
}

func TestCloseHandler_ResetError(t *testing.T) {
	sessions := &fakeCloseSessions{resetErr: errors.New("reset fail")}
	closer := &fakeCloseTopicCloser{isTopic: true}
	dispatcher := &recordingCloseDispatcher{}
	h := New(sessions, nil, nil, closer, dispatcher, zerolog.Nop())

	loc := deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-9001-123", AddressKey: "9001:123"}
	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "close",
		Transport:    "telegram",
		Locator:      loc,
		Principal:    "tg-101",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-6"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Could not close this topic.") {
		t.Errorf("got %q, want Could not close this topic.", got)
	}
}
