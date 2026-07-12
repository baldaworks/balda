package actors

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryworkflow"
	"go.uber.org/fx"
)

type deliveryWorkflowService interface {
	Handle(ctx context.Context, env actorlayer.Envelope, payload deliverycmd.Payload) error
}

type jobDeliveryActor struct {
	service deliveryWorkflowService
}

type jobDeliveryActorParams struct {
	fx.In

	Dispatcher deliveryworkflow.Dispatcher
	Outbox     deliveryworkflow.DeliveryStore
	Events     deliveryworkflow.JobEvents
	Service    deliveryWorkflowService
}

func (a *jobDeliveryActor) Address() string {
	return actorlayer.WildcardAddress(baldaexecution.ActorTypeDelivery)
}

func (a *jobDeliveryActor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	if strings.TrimSpace(env.Kind) != jobPayloadKindDelivery {
		return actorlayer.PolicyError(fmt.Errorf("unsupported delivery kind %q", env.Kind))
	}
	var payload DeliveryPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		return actorlayer.PermanentError(fmt.Errorf("decode job delivery payload: %w", err))
	}
	if a.service == nil {
		return actorlayer.TransientError(fmt.Errorf("delivery workflow service is required"))
	}
	return a.service.Handle(ctx, env, payload)
}
