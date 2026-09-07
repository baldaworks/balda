package usage_test

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors/command/usage"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usageview"
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

func TestUsageDenied(t *testing.T) {
	d := &mockDispatcher{}
	s := &mockSessionReader{}
	h := usage.New(s, d, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "usage",
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

func TestUsageEmpty(t *testing.T) {
	d := &mockDispatcher{}
	s := &mockSessionReader{}
	h := usage.New(s, d, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "usage",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	_ = h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)

	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched = %d, want 1", len(d.dispatched))
	}
	var dp deliverycmd.Payload
	_ = actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &dp)
	if !strings.Contains(dp.Text, "No provider usage has been recorded") {
		t.Errorf("got %q", dp.Text)
	}
}

func TestUsageSnapshot(t *testing.T) {
	d := &mockDispatcher{}
	s := &mockSessionReader{
		values: map[string]any{
			usageview.UsageStateKey: map[string]any{
				"total_token_count": 42,
			},
		},
	}
	h := usage.New(s, d, zerolog.Nop())

	payload := commandcmd.Payload{
		Name:    "usage",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-1"},
		Access:  commandcmd.Access{SessionCommands: true},
	}
	_ = h.Handle(context.Background(), actorlayer.Envelope{ID: "env-1"}, payload)

	if len(d.dispatched) != 1 {
		t.Fatalf("dispatched = %d, want 1", len(d.dispatched))
	}
	var dp deliverycmd.Payload
	_ = actorlayer.UnmarshalPayload(d.dispatched[0].Payload, &dp)
	if !strings.Contains(dp.Text, "Total tokens: 42") {
		t.Errorf("got %q", dp.Text)
	}
}
