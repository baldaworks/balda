package commandfx

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type recordingDispatcher struct {
	receipt   *actortransport.DispatchReceipt
	err       error
	envelopes []actorlayer.Envelope
}

type fixedSnapshotResolver struct {
	id       runtimecatalogcmd.SnapshotID
	err      error
	requests []SnapshotRequest
}

func (r *fixedSnapshotResolver) ResolveEffectiveSnapshot(_ context.Context, request SnapshotRequest) (runtimecatalogcmd.SnapshotID, error) {
	r.requests = append(r.requests, request)
	return r.id, r.err
}

func (d *recordingDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, env)
	return d.receipt, d.err
}

func TestCommandIngress_PublishCommand(t *testing.T) {
	t.Parallel()
	const invocationID = "inv-1"

	dispatcher := &recordingDispatcher{
		receipt: &actortransport.DispatchReceipt{MsgID: "cmd-1"},
	}
	resolver := &fixedSnapshotResolver{id: "snapshot-1"}
	ingress := NewCommandIngress(dispatcher, resolver)

	req := commandcmd.Request{
		InvocationID: invocationID,
		Payload: commandcmd.Payload{
			Version:    commandcmd.LegacySchemaVersion,
			Name:       "help",
			SnapshotID: "untrusted-snapshot",
			Transport:  "telegram",
			Principal:  "tg:101",
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
	if env.ID != invocationID {
		t.Fatalf("env.ID = %q, want inv-1", env.ID)
	}
	if env.From.Target != "ingress" || env.From.Key != "telegram" {
		t.Fatalf("env.From = %+v, want ingress:telegram", env.From)
	}
	if env.DedupeKey != invocationID || env.CorrelationID != invocationID || env.CausationID != "" {
		t.Fatalf("envelope durable identity = id:%q dedupe:%q correlation:%q causation:%q", env.ID, env.DedupeKey, env.CorrelationID, env.CausationID)
	}
	payload, err := commandcmd.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Version != commandcmd.SchemaVersion || payload.SnapshotID != "snapshot-1" {
		t.Fatalf("durable payload version/snapshot = %d/%q", payload.Version, payload.SnapshotID)
	}
	if len(resolver.requests) != 1 || resolver.requests[0] != (SnapshotRequest{SessionID: "s-1"}) {
		t.Fatalf("snapshot resolver request = %+v", resolver.requests)
	}
}

func TestCommandIngress_PublishCommand_Errors(t *testing.T) {
	t.Parallel()

	// Nil dispatcher
	nilIngress := NewCommandIngress(nil, &fixedSnapshotResolver{id: "snapshot-1"})
	if err := nilIngress.PublishCommand(context.Background(), commandcmd.Request{}); err == nil {
		t.Fatal("expected error with nil dispatcher")
	}

	// Dispatch failure
	dispatcher := &recordingDispatcher{
		err: errors.New("dispatch failed"),
	}
	ingress := NewCommandIngress(dispatcher, &fixedSnapshotResolver{id: "snapshot-1"})
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

func TestCommandIngressFailsClosedBeforeDispatchWithoutSnapshot(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{}
	lookupErr := errors.New("temporary catalog error")
	ingress := NewCommandIngress(dispatcher, &fixedSnapshotResolver{err: lookupErr})
	req := commandcmd.Request{InvocationID: "inv", Payload: commandcmd.Payload{
		Name: "help", Transport: "telegram", Principal: "tg:101",
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "9001:0", SessionID: "s-1"},
	}}
	err := ingress.PublishCommand(context.Background(), req)
	if !errors.Is(err, lookupErr) || len(dispatcher.envelopes) != 0 {
		t.Fatalf("PublishCommand() error/envelopes = %v/%d", err, len(dispatcher.envelopes))
	}
}
