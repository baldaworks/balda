package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

// CommandIngress is the command ingress boundary: validate and publish once.
type CommandIngress struct {
	dispatcher actortransport.Dispatcher
}

func NewCommandIngress(dispatcher actortransport.Dispatcher) *CommandIngress {
	return &CommandIngress{dispatcher: dispatcher}
}

func (h *CommandIngress) PublishCommand(ctx context.Context, req commandcmd.Request) error {
	if h == nil || h.dispatcher == nil {
		return fmt.Errorf("command dispatcher is required")
	}
	id := strings.TrimSpace(req.InvocationID)
	env, err := commandcmd.NewEnvelope(req.Payload, commandcmd.EnvelopeOptions{
		ID: id, DedupeKey: id, CorrelationID: id,
		From: actorlayer.ActorAddress{Target: "ingress", Key: strings.TrimSpace(req.Payload.Transport)},
	})
	if err != nil {
		return fmt.Errorf("build command envelope: %w", err)
	}
	if _, err := h.dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("dispatch command envelope: %w", err)
	}
	return nil
}
