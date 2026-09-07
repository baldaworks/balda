package actorsfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	"github.com/baldaworks/go-actorlayer"
)

type deliveryWorkflowAdapter struct {
	service *deliveryworkflow.Service
}

func (a deliveryWorkflowAdapter) Handle(ctx context.Context, env actorlayer.Envelope, payload deliverycmd.Payload) error {
	return a.service.Handle(ctx, env, payload)
}
