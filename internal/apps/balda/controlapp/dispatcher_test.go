package controlapp

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type recordingDispatcher struct {
	dispatched []actorlayer.Envelope
}

func (r *recordingDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	r.dispatched = append(r.dispatched, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestCommandDispatcherActions(t *testing.T) {
	tests := []struct {
		name       string
		invoke     func(d *CommandDispatcher, loc deliverycmd.Locator) error
		wantAction string
		sessionID  string
		user       string
	}{
		{
			name: "cancel turn",
			invoke: func(d *CommandDispatcher, loc deliverycmd.Locator) error {
				return d.CancelTurn(context.Background(), loc, "user-1", "user requested cancel", true)
			},
			wantAction: controlcmd.ActionCancelTurn,
			sessionID:  "s-123",
			user:       "user-1",
		},
		{
			name: "clear goal",
			invoke: func(d *CommandDispatcher, loc deliverycmd.Locator) error {
				return d.ClearGoal(context.Background(), loc, "user-2", "user requested goal clear", true)
			},
			wantAction: controlcmd.ActionClearGoal,
			sessionID:  "s-456",
			user:       "user-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rd := &recordingDispatcher{}
			d := NewCommandDispatcher(rd)
			loc := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: tt.sessionID}

			if err := tt.invoke(d, loc); err != nil {
				t.Fatalf("invoke error = %v", err)
			}
			if len(rd.dispatched) != 1 {
				t.Fatalf("dispatched count = %d, want 1", len(rd.dispatched))
			}
			var payload controlcmd.Payload
			if err := actorlayer.UnmarshalPayload(rd.dispatched[0].Payload, &payload); err != nil {
				t.Fatalf("UnmarshalPayload error = %v", err)
			}
			if payload.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", payload.Action, tt.wantAction)
			}
			if payload.SessionID != tt.sessionID || payload.RequestedBy != tt.user || !payload.Notify {
				t.Errorf("payload = %+v, unexpected fields", payload)
			}
		})
	}
}

func TestCommandDispatcherNilDispatcher(t *testing.T) {
	d := NewCommandDispatcher(nil)
	locator := deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:1", SessionID: "s-789"}

	if err := d.CancelTurn(context.Background(), locator, "u", "r", false); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("expected unavailable error, got %v", err)
	}
	if err := d.ClearGoal(context.Background(), locator, "u", "r", false); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("expected unavailable error, got %v", err)
	}
}
