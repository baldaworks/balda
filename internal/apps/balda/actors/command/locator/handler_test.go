package locator

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type locatorRegistry struct{ transport string }

func (r *locatorRegistry) RenderStructured(_ context.Context, transport string, _ deliveryfmt.MessageType, _ any) (deliveryfmt.StructuredPresentation, error) {
	r.transport = transport
	return deliveryfmt.StructuredPresentation{Text: "formatted", DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown}, nil
}

type locatorDispatcher struct{ envelopes []actorlayer.Envelope }

func (d *locatorDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestHandlerUsesStructuredTransportRenderer(t *testing.T) {
	registry, dispatcher := &locatorRegistry{}, &locatorDispatcher{}
	h := New(registry, dispatcher)
	locator := deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:C", AddressJSON: `{}`, SessionID: "slackagent-c-T-C"}
	payload := commandcmd.Payload{Version: commandcmd.SchemaVersion, Name: "locator", Locator: locator, Transport: "slackagent", Principal: "slackagent:T:U", Access: commandcmd.Access{WorkspaceMember: true}, Invocation: commandcmd.Invocation{Root: "/balda"}}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-2"}, payload); err != nil {
		t.Fatal(err)
	}
	if registry.transport != "slackagent" || len(dispatcher.envelopes) != 1 {
		t.Fatalf("transport=%q deliveries=%d", registry.transport, len(dispatcher.envelopes))
	}
	if dispatcher.envelopes[0].DedupeKey != "op-2:delivery:locator-result" {
		t.Fatalf("dedupe=%q", dispatcher.envelopes[0].DedupeKey)
	}
}
