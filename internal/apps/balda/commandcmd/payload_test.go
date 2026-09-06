package commandcmd

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	p := Payload{Version: SchemaVersion, Name: "locator", Transport: "slackagent", Principal: "slackagent:T:U", Locator: deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:C", AddressJSON: `{}`, SessionID: "slackagent-c-T-C"}}
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
	p := Payload{Version: SchemaVersion, Name: "reset", Transport: "telegram", Principal: "tg-1", Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{}`, SessionID: "tg-1-0"}}
	env, err := NewEnvelope(p, EnvelopeOptions{ID: "telegram:command:1:2", From: actorlayer.ActorAddress{Target: "ingress", Key: "telegram"}})
	if err != nil {
		t.Fatal(err)
	}
	env.Kind = "wrong"
	if _, err := Decode(env); err == nil {
		t.Fatal("Decode accepted wrong taxonomy")
	}
}
