package presentation

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
)

func RegisterPermissionRenderer(reg *deliveryfmt.StructuredRegistry) error {
	return deliveryfmt.RegisterStructuredRenderer(
		reg,
		deliveryfmt.TransportSlackAgent,
		permissionfmt.RequestDescriptor,
		permissionRenderer{},
	)
}

type permissionRenderer struct{}

func (permissionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[permissioncmd.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           permissionfmt.RenderMarkdown(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}
