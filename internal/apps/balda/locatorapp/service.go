// Package locatorapp owns transport-neutral locator response orchestration.
package locatorapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorfmt"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
)

// Renderer produces the structured presentation for the current public locator.
type Renderer interface {
	Render(ctx context.Context, locator deliverycmd.Locator) (deliveryfmt.StructuredPresentation, error)
}

// Service resolves public locator data through the structured message registry.
type Service struct {
	registry deliveryfmt.StructuredMessageRegistry
}

// New constructs a locator presentation service backed by registry.
func New(registry deliveryfmt.StructuredMessageRegistry) *Service {
	return &Service{registry: registry}
}

// Render derives the public locator from the delivery locator and resolves the
// deterministic presentation registered for its transport.
func (s *Service) Render(ctx context.Context, locator deliverycmd.Locator) (deliveryfmt.StructuredPresentation, error) {
	if s == nil || s.registry == nil {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("render locator response: structured message registry is required")
	}
	transport := strings.ToLower(strings.TrimSpace(locator.ChannelType))
	ref := locatorref.Format(locator)
	if transport == "" || ref == "" {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("render locator response: canonical locator is required")
	}
	parsed, err := locatorref.Parse(ref)
	if err != nil || locatorref.Format(parsed) != ref {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("render locator response: locator %q is not canonical", ref)
	}
	if strings.ContainsAny(ref, "`\r\n") {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("render locator response: locator %q is not safe for formatted presentation", ref)
	}
	env := deliveryfmt.StructuredEnvelope[locatorfmt.Response]{
		Descriptor: locatorfmt.ResponseDescriptor,
		Body: locatorfmt.Response{
			Transport: transport,
			Locator:   ref,
		},
	}
	presentation, err := s.registry.RenderStructured(ctx, transport, locatorfmt.ResponseDescriptor.Type, env)
	if err != nil {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("render locator response for transport %q: %w", transport, err)
	}
	if strings.TrimSpace(presentation.Text) == "" {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("render locator response for transport %q: renderer returned empty text", transport)
	}
	if deliveryfmt.NormalizeDeliveryFormat(presentation.DeliveryFormat) == "" {
		return deliveryfmt.StructuredPresentation{}, fmt.Errorf("render locator response for transport %q: renderer returned empty delivery format", transport)
	}
	return presentation, nil
}
