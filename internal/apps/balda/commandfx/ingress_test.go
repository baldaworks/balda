package commandfx

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type recordingDispatcher struct {
	receipt   *actortransport.DispatchReceipt
	err       error
	envelopes []actorlayer.Envelope
}

func (d *recordingDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, env)
	return d.receipt, d.err
}

func TestCommandIngress_PublishCommand(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{
		receipt: &actortransport.DispatchReceipt{MsgID: "cmd-1"},
	}
	ingress := NewCommandIngress(dispatcher)

	req := commandcmd.Request{
		InvocationID: "inv-1",
		Payload: commandcmd.Payload{
			Version:   commandcmd.SchemaVersion,
			Name:      "help",
			Transport: "telegram",
			Principal: "tg:101",
			Locator: deliverycmd.Locator{
				ChannelType: "telegram",
				AddressKey:  "9001:0",
				SessionID:   "s-1",
			},
		},
	}

	if err := ingress.PublishCommand(context.Background(), req); err != nil {
		t.Fatalf("PublishCommand() error: %v", err)
	}

	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("envelopes = %d, want 1", len(dispatcher.envelopes))
	}
	env := dispatcher.envelopes[0]
	if env.ID != "inv-1" {
		t.Fatalf("env.ID = %q, want inv-1", env.ID)
	}
	if env.From.Target != "ingress" || env.From.Key != "telegram" {
		t.Fatalf("env.From = %+v, want ingress:telegram", env.From)
	}
}

func TestCommandIngress_PublishCommand_Errors(t *testing.T) {
	t.Parallel()

	// Nil dispatcher
	nilIngress := NewCommandIngress(nil)
	if err := nilIngress.PublishCommand(context.Background(), commandcmd.Request{}); err == nil {
		t.Fatal("expected error with nil dispatcher")
	}

	// Dispatch failure
	dispatcher := &recordingDispatcher{
		err: errors.New("dispatch failed"),
	}
	ingress := NewCommandIngress(dispatcher)
	req := commandcmd.Request{
		InvocationID: "inv-err",
		Payload: commandcmd.Payload{
			Version:   commandcmd.SchemaVersion,
			Name:      "help",
			Transport: "telegram",
			Locator: deliverycmd.Locator{
				ChannelType: "telegram",
				AddressKey:  "9001:0",
				SessionID:   "s-1",
			},
		},
	}
	if err := ingress.PublishCommand(context.Background(), req); err == nil {
		t.Fatal("expected error on dispatch failure")
	}
}
