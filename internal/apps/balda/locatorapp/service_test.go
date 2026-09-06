package locatorapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

type recordingRegistry struct {
	presentation deliveryfmt.StructuredPresentation
	err          error
	transport    string
	typ          deliveryfmt.MessageType
	env          any
}

func (r *recordingRegistry) RenderStructured(_ context.Context, transport string, typ deliveryfmt.MessageType, env any) (deliveryfmt.StructuredPresentation, error) {
	r.transport = transport
	r.typ = typ
	r.env = env
	return r.presentation, r.err
}

func TestServiceRenderProjectsCanonicalLocator(t *testing.T) {
	t.Parallel()

	registry := &recordingRegistry{presentation: deliveryfmt.StructuredPresentation{
		Text:           "formatted locator",
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}}
	service := New(registry)
	got, err := service.Render(context.Background(), deliverycmd.Locator{
		ChannelType: " slackagent ",
		AddressKey:  " c:T123:C456 ",
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if got != registry.presentation {
		t.Fatalf("Render() = %+v, want %+v", got, registry.presentation)
	}
	if registry.transport != deliveryfmt.TransportSlackAgent || registry.typ != locatorfmt.ResponseDescriptor.Type {
		t.Fatalf("registry lookup = %q/%q", registry.transport, registry.typ)
	}
	env, ok := registry.env.(deliveryfmt.StructuredEnvelope[locatorfmt.Response])
	if !ok {
		t.Fatalf("registry envelope type = %T", registry.env)
	}
	if env.Descriptor != locatorfmt.ResponseDescriptor {
		t.Fatalf("descriptor = %+v, want %+v", env.Descriptor, locatorfmt.ResponseDescriptor)
	}
	if env.Body.Transport != "slackagent" || env.Body.Locator != "slackagent:c:T123:C456" {
		t.Fatalf("body = %+v", env.Body)
	}
}

func TestServiceRenderFailsClosed(t *testing.T) {
	t.Parallel()

	renderErr := errors.New("renderer unavailable")
	tests := []struct {
		name     string
		service  *Service
		locator  deliverycmd.Locator
		contains string
	}{
		{name: "registry unavailable", service: New(nil), locator: deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:C"}, contains: "registry is required"},
		{name: "canonical locator unavailable", service: New(&recordingRegistry{}), locator: deliverycmd.Locator{ChannelType: "slackagent"}, contains: "canonical locator is required"},
		{name: "unsafe locator", service: New(&recordingRegistry{}), locator: deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:`C"}, contains: "not safe for formatted presentation"},
		{name: "renderer error", service: New(&recordingRegistry{err: renderErr}), locator: deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:C"}, contains: "renderer unavailable"},
		{name: "empty text", service: New(&recordingRegistry{presentation: deliveryfmt.StructuredPresentation{DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn}}), locator: deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:C"}, contains: "empty text"},
		{name: "empty format", service: New(&recordingRegistry{presentation: deliveryfmt.StructuredPresentation{Text: "locator"}}), locator: deliverycmd.Locator{ChannelType: "slackagent", AddressKey: "c:T:C"}, contains: "empty delivery format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := test.service.Render(context.Background(), test.locator)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Render() error = %v, want substring %q", err, test.contains)
			}
			if got != (deliveryfmt.StructuredPresentation{}) {
				t.Fatalf("Render() = %+v, want zero presentation", got)
			}
		})
	}
}
