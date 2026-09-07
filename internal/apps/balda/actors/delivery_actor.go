package actors

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

type DeliveryWorkflowService interface {
	Handle(ctx context.Context, env actorlayer.Envelope, payload deliverycmd.Payload) error
}

type JobDeliveryActor struct {
	service DeliveryWorkflowService
}

func NewJobDeliveryActor(service DeliveryWorkflowService) *JobDeliveryActor {
	return &JobDeliveryActor{service: service}
}

func (a *JobDeliveryActor) Address() string {
	return actorlayer.WildcardAddress(baldaexecution.ActorTypeDelivery)
}

func (a *JobDeliveryActor) Handle(ctx context.Context, env actorlayer.Envelope) error {
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
