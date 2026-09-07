package auto_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command/auto"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/automodecmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
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

type mockSessionReader struct {
	values map[string]any
}

func (m *mockSessionReader) RuntimeStateValue(_ context.Context, _ deliverycmd.Locator, key string) (any, bool, error) {
	val, ok := m.values[key]
	return val, ok, nil
}

func TestAutoDenied(t *testing.T) {
	d := &mockDispatcher{}
	s := &mockSessionReader{}
	h := auto.New(s, d, 10, nil, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "auto",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
	}
	_ = h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)

	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched = %d, want 1", len(d.dispatched))
	}
	var dp deliverycmd.Payload
	_ = actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &dp)
	if !strings.Contains(dp.Text, "Only the bot owner or collaborators") {
		t.Errorf("got %q", dp.Text)
	}
}

func TestAutoStatusDefault(t *testing.T) {
	d := &mockDispatcher{}
	s := &mockSessionReader{}
	h := auto.New(s, d, 10, nil, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "auto",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	_ = h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)

	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched = %d, want 1", len(d.dispatched))
	}
	var dp deliverycmd.Payload
	_ = actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &dp)
	if !strings.Contains(dp.Text, "Auto mode") || !strings.Contains(dp.Text, "off") {
		t.Errorf("got %q", dp.Text)
	}
}

func TestAutoEnable(t *testing.T) {
	d := &mockDispatcher{}
	s := &mockSessionReader{}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	h := auto.New(s, d, 10, func() time.Time { return now }, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "auto",
		Args:    "on",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	_ = h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)

	if len(d.dispatched) != 2 {
		t.Fatalf("dispatched = %d, want 2 (state update + reply)", len(d.dispatched))
	}
	// Verify state update envelope
	stateEnv := d.dispatched[0]
	if stateEnv.Namespace != actorcmd.NamespaceAutoModeCommand {
		t.Errorf("expected auto_mode namespace, got %s", stateEnv.Namespace)
	}
	var statePayload automodecmd.Payload
	_ = actorlayer.UnmarshalPayload(stateEnv.Payload, &statePayload)
	if statePayload.State[automode.StateKeyEnabled] != true {
		t.Errorf("expected auto enabled in state, got %v", statePayload.State)
	}

	// Verify reply
	replyEnv := d.dispatched[1]
	var dp deliverycmd.Payload
	_ = actorlayer.UnmarshalPayload(replyEnv.Payload, &dp)
	if !strings.Contains(dp.Text, "Auto mode") || !strings.Contains(dp.Text, "`on`") {
		t.Errorf("got reply %q", dp.Text)
	}
}

func TestAutoDisable(t *testing.T) {
	d := &mockDispatcher{}
	s := &mockSessionReader{}
	h := auto.New(s, d, 10, nil, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "auto",
		Args:    "off",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	_ = h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)

	if len(d.dispatched) != 2 {
		t.Fatalf("dispatched = %d, want 2 (state update + reply)", len(d.dispatched))
	}
	stateEnv := d.dispatched[0]
	var statePayload automodecmd.Payload
	_ = actorlayer.UnmarshalPayload(stateEnv.Payload, &statePayload)
	if statePayload.State[automode.StateKeyEnabled] != false {
		t.Errorf("expected auto disabled in state, got %v", statePayload.State)
	}
}
