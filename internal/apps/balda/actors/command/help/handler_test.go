package help_test

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command/help"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type mockDispatcher struct {
	dispatched []actorlayer.Envelope
}

func (m *mockDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	m.dispatched = append(m.dispatched, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestHelpHandlerRejectsArguments(t *testing.T) {
	d := &mockDispatcher{}
	h := help.New(d)
	payload := commandcmd.Payload{
		Name:    "help",
		Args:    "extra",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
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
	if !strings.Contains(deliveryPayload.Text, "Usage: /help") {
		t.Errorf("expected usage message, got %q", deliveryPayload.Text)
	}
}

func TestHelpHandlerRendersForOwner(t *testing.T) {
	d := &mockDispatcher{}
	h := help.New(d)
	payload := commandcmd.Payload{
		Name:    "help",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access: commandcmd.Access{
			SessionCommands: true,
			Owner:           true,
		},
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
	text := deliveryPayload.Text
	if !strings.Contains(text, "/topic") || !strings.Contains(text, "/user add") || !strings.Contains(text, plugincmd.HelpMarkdown()) {
		t.Errorf("expected full owner help, got %q", text)
	}
}
