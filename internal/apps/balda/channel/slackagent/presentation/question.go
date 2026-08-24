package presentation

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
)

func RegisterQuestionRenderer(reg *deliveryfmt.StructuredRegistry) error {
	return deliveryfmt.RegisterStructuredRenderer(
		reg,
		deliveryfmt.TransportSlackAgent,
		questionfmt.RequestDescriptor,
		questionRenderer{},
	)
}

type questionRenderer struct{}

func (questionRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[questionfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return deliveryfmt.StructuredPresentation{
		Text:           questionfmt.RenderMarkdownOptions(env.Body),
		DeliveryFormat: deliveryfmt.DeliveryFormatMrkdwn,
	}, nil
}
