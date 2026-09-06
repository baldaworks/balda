// Package locator owns the transport-neutral locator command.
package locator

import (
	"context"
	"fmt"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type Handler struct {
	registry   deliveryfmt.StructuredMessageRegistry
	dispatcher actortransport.Dispatcher
}

func New(registry deliveryfmt.StructuredMessageRegistry, dispatcher actortransport.Dispatcher) *Handler {
	return &Handler{registry: registry, dispatcher: dispatcher}
}
func (h *Handler) Name() string { return "locator" }
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Access.SessionCommands && !p.Access.Owner && !p.Access.Collaborator && !p.Access.WorkspaceMember {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Only the bot owner or collaborators can use this command.", "locator-denied")
	}
	if strings.TrimSpace(p.Args) != "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Usage: "+usage(p), "locator-usage")
	}
	presentation, err := h.render(ctx, p.Locator)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	return commandactor.SendStructured(ctx, h.dispatcher, env.ID, p.Locator, presentation, "locator-result")
}
func usage(p commandcmd.Payload) string {
	if root := strings.TrimSpace(p.Invocation.Root); root != "" {
		return root + " locator"
	}
	return "/locator"
}
func (h *Handler) render(ctx context.Context, l deliverycmd.Locator) (deliveryfmt.StructuredPresentation, error) {
	if h.registry == nil {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("structured message registry is required")
	}
	transport, ref := strings.ToLower(strings.TrimSpace(l.ChannelType)), locatorref.Format(l)
	parsed, err := locatorref.Parse(ref)
	if transport == "" || ref == "" || err != nil || locatorref.Format(parsed) != ref || strings.ContainsAny(ref, "`\r\n") {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("canonical locator is required")
	}
	msg := deliveryfmt.StructuredEnvelope[locatorfmt.Response]{Descriptor: locatorfmt.ResponseDescriptor, Body: locatorfmt.Response{Transport: transport, Locator: ref}}
	return h.registry.RenderStructured(ctx, transport, locatorfmt.ResponseDescriptor.Type, msg)
}
