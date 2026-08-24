package presentation

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
)

func RegisterProgressRenderer(reg *deliveryfmt.StructuredRegistry) error {
	return deliveryfmt.RegisterStructuredRenderer(
		reg,
		deliveryfmt.TransportSlackAgent,
		progressfmt.RequestDescriptor,
		progressRenderer{},
	)
}

type progressRenderer struct{}

func (progressRenderer) RenderStructured(_ context.Context, env deliveryfmt.StructuredEnvelope[progressfmt.Request]) (deliveryfmt.StructuredPresentation, error) {
	return progressfmt.RenderPlain(env.Body), nil
}
