package commandcmd

import (
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/go-actorlayer"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	p := Payload{Version: SchemaVersion, Name: "locator", SnapshotID: "snapshot-1", Transport: "slackagent", Principal: "slackagent:T:U", Locator: deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:C", AddressJSON: `{}`, SessionID: "slackagent-c-T-C"}}
	env, err := NewEnvelope(p, EnvelopeOptions{ID: "slackagent:command:abc", From: actorlayer.ActorAddress{Target: "ingress", Key: "slackagent"}})
	if err != nil {
		t.Fatal(err)
	}
	if env.To.Target != "command" || env.To.Key != p.Locator.SessionID {
		t.Fatalf("address = %+v", env.To)
	}
	got, err := Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != p.Name || got.Locator.AddressKey != p.Locator.AddressKey {
		t.Fatalf("payload = %#v", got)
	}
}

func TestDecodeRejectsWrongTaxonomy(t *testing.T) {
	p := Payload{Version: SchemaVersion, Name: "reset", SnapshotID: "snapshot-1", Transport: "telegram", Principal: "tg-1", Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{}`, SessionID: "tg-1-0"}}
	env, err := NewEnvelope(p, EnvelopeOptions{ID: "telegram:command:1:2", From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"}})
	if err != nil {
		t.Fatal(err)
	}
	env.Kind = "wrong"
	if _, err := Decode(env); err == nil {
		t.Fatal("Decode accepted wrong taxonomy")
	}
}

func TestNewEnvelopeRequiresPinnedSchemaV2(t *testing.T) {
	t.Parallel()

	p := Payload{Version: LegacySchemaVersion, Name: "reset", Transport: "telegram", Principal: "tg-1", Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", SessionID: "tg-1-0"}}
	if _, err := NewEnvelope(p, EnvelopeOptions{ID: "legacy", From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"}}); err == nil {
		t.Fatal("NewEnvelope() accepted legacy unpinned publication")
	}
	p.Version = SchemaVersion
	if _, err := NewEnvelope(p, EnvelopeOptions{ID: "unpinned", From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"}}); err == nil {
		t.Fatal("NewEnvelope() accepted schema v2 without snapshot")
	}
	p.SnapshotID = runtimecatalogcmd.SnapshotID("snapshot-1")
	if _, err := NewEnvelope(p, EnvelopeOptions{ID: "pinned", From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"}}); err != nil {
		t.Fatalf("NewEnvelope(pinned) error = %v", err)
	}
}

func TestDecodeBoundsLegacyPayload(t *testing.T) {
	t.Parallel()

	env := actorlayer.Envelope{
		Namespace: "chat.command", Kind: "execute",
		To:      actorlayer.ActorAddress{Target: "command", Key: "tg-1-0"},
		Payload: actorlayer.Payload{Encoding: actorlayer.EncodingJSON, Data: make([]byte, maxPayloadBytes+1)},
	}
	if _, err := Decode(env); err == nil {
		t.Fatal("Decode() accepted oversized legacy payload")
	}
}

func TestNewEnvelopeRejectsDurablePoisonPayload(t *testing.T) {
	t.Parallel()

	p := Payload{
		Version: SchemaVersion, Name: "help", SnapshotID: "snapshot-1",
		Transport: "telegram", Principal: strings.Repeat("p", maxPayloadBytes),
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", SessionID: "tg-1-0"},
	}
	if _, err := NewEnvelope(p, EnvelopeOptions{ID: "oversized", From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"}}); err == nil {
		t.Fatal("NewEnvelope() accepted payload Decode would reject")
	}
}
