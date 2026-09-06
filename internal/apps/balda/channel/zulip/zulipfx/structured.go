package zulipfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip/presentation"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
)

type zulipLocatorRenderer struct{}

func (zulipLocatorRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[locatorfmt.Response]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           presentation.RenderLocator(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown,
	}, nil
}

// NewLocatorStructuredRegistrar registers Zulip locator response presentation.
func NewLocatorStructuredRegistrar() deliveryfx.StructuredRegistryRegistrar {
	return deliveryfx.NewStructuredRegistrar(deliveryfmt.TransportZulip, locatorfmt.ResponseDescriptor, zulipLocatorRenderer{})
}
