package commandfx

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type deduplicatingDispatcher struct {
	receipt   *actortransport.DispatchReceipt
	envelopes map[string]actorlayer.Envelope
}

func (d *deduplicatingDispatcher) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if _, exists := d.envelopes[envelope.DedupeKey]; !exists {
		d.envelopes[envelope.DedupeKey] = envelope
	}
	return d.receipt, nil
}

func TestPluginTurnExecutorPublishesPinnedDeduplicatedNormalTurn(t *testing.T) {
	t.Parallel()

	dispatcher := &deduplicatingDispatcher{
		receipt: &actortransport.DispatchReceipt{MsgID: "turn"}, envelopes: make(map[string]actorlayer.Envelope),
	}
	executor := NewPluginTurnExecutor(dispatcher)
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}
	descriptor := runtimecatalogcmd.CommandDescriptor{
		Revision: "revision-one",
		Skill:    &runtimecatalogcmd.SkillRef{Source: source, Revision: "revision-one", Name: "deploy"},
	}
	payload := commandcmd.Payload{
		Version: commandcmd.SchemaVersion, Name: "deploy", SnapshotID: "snapshot-one", Args: "production --safe",
		Locator:   deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", SessionID: "session"},
		Transport: "telegram", Principal: "telegram:42", Invocation: commandcmd.Invocation{Root: "/"},
		Presentation: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown, ProgressPolicy: deliveryfmt.ProgressPolicy{Typing: true}},
	}
	parent := actorlayer.Envelope{ID: "command-id", DedupeKey: "command-dedupe", CorrelationID: "correlation"}
	if err := executor.ExecutePluginCommand(context.Background(), parent, payload, descriptor); err != nil {
		t.Fatal(err)
	}
	if err := executor.ExecutePluginCommand(context.Background(), parent, payload, descriptor); err != nil {
		t.Fatal(err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("durable child turns after redelivery = %d", len(dispatcher.envelopes))
	}
	for _, envelope := range dispatcher.envelopes {
		if envelope.DedupeKey != "command-dedupe:plugin-turn" || envelope.CorrelationID != "correlation" || envelope.CausationID != "command-id" {
			t.Fatalf("child identity = %+v", envelope)
		}
		var turn turncmd.SessionTurnPayload
		if err := actorlayer.UnmarshalPayload(envelope.Payload, &turn); err != nil {
			t.Fatal(err)
		}
		if turn.Text != "/deploy production --safe" || turn.Skill == nil || turn.Skill.Snapshot != "snapshot-one" || turn.Skill.Ref.Revision != "revision-one" {
			t.Fatalf("turn payload = %+v", turn)
		}
		if turn.UserID != payload.Principal || turn.DeliveryFormat != payload.Presentation.DeliveryFormat || !turn.Deliver {
			t.Fatalf("turn context = %+v", turn)
		}
	}
}
