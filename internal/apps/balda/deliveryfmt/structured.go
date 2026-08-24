package deliveryfmt

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// MessageType identifies a stable structured message contract.
type MessageType string

const (
	MessageTypePermissionRequest MessageType = "balda.permission.request.v1"
)

// Descriptor ties a typed payload to a stable structured message contract.
type Descriptor[T any] struct {
	Type MessageType
}

// StructuredEnvelope carries a typed system-authored message body.
type StructuredEnvelope[T any] struct {
	Descriptor Descriptor[T]
	Body       T
}

func (e StructuredEnvelope[T]) Type() MessageType {
	return e.Descriptor.Type
}

// StructuredPresentation is the deterministic result of a structured renderer.
type StructuredPresentation struct {
	Text           string
	DeliveryFormat DeliveryFormat
}

// StructuredRenderer renders a typed system-authored message for one transport.
type StructuredRenderer[T any] interface {
	RenderStructured(ctx context.Context, env StructuredEnvelope[T]) (StructuredPresentation, error)
}

var (
	ErrStructuredRendererNotFound = errors.New("structured renderer not found")
	ErrStructuredEnvelopeMismatch = errors.New("structured envelope type mismatch")
)

type structuredRendererAny interface {
	renderStructuredAny(ctx context.Context, env any) (StructuredPresentation, error)
}

type structuredRendererAdapter[T any] struct {
	inner StructuredRenderer[T]
}

func (a structuredRendererAdapter[T]) renderStructuredAny(ctx context.Context, env any) (StructuredPresentation, error) {
	typed, ok := env.(StructuredEnvelope[T])
	if !ok {
		return StructuredPresentation{}, ErrStructuredEnvelopeMismatch
	}
	return a.inner.RenderStructured(ctx, typed)
}

type structuredKey struct {
	transport string
	typ       MessageType
}

// StructuredRegistry resolves deterministic structured renderers by transport
// and stable message type.
type StructuredRegistry struct {
	renderers map[structuredKey]structuredRendererAny
}

func NewStructuredRegistry() *StructuredRegistry {
	return &StructuredRegistry{
		renderers: make(map[structuredKey]structuredRendererAny),
	}
}

func RegisterStructuredRenderer[T any](
	reg *StructuredRegistry,
	transport string,
	desc Descriptor[T],
	renderer StructuredRenderer[T],
) error {
	if reg == nil {
		return fmt.Errorf("register structured renderer: registry is required")
	}
	if renderer == nil {
		return fmt.Errorf("register structured renderer: renderer is required")
	}
	if err := validateIdentifier("transport", transport); err != nil {
		return fmt.Errorf("register structured renderer: %w", err)
	}
	if err := validateMessageType(desc.Type); err != nil {
		return fmt.Errorf("register structured renderer for transport %q: %w", transport, err)
	}
	key := structuredKey{transport: transport, typ: desc.Type}
	if _, ok := reg.renderers[key]; ok {
		return fmt.Errorf("register structured renderer for %s/%s: duplicate renderer", transport, desc.Type)
	}
	reg.renderers[key] = structuredRendererAdapter[T]{inner: renderer}
	return nil
}

func RenderStructured[T any](
	ctx context.Context,
	reg *StructuredRegistry,
	transport string,
	env StructuredEnvelope[T],
) (StructuredPresentation, error) {
	if reg == nil {
		return StructuredPresentation{}, fmt.Errorf("render structured: registry is required")
	}
	if err := validateIdentifier("transport", transport); err != nil {
		return StructuredPresentation{}, fmt.Errorf("render structured: %w", err)
	}
	key := structuredKey{transport: transport, typ: env.Type()}
	renderer, ok := reg.renderers[key]
	if !ok {
		return StructuredPresentation{}, fmt.Errorf("%w: transport %q message type %q", ErrStructuredRendererNotFound, transport, env.Type())
	}
	return renderer.renderStructuredAny(ctx, env)
}

func (r *StructuredRegistry) RenderStructured(ctx context.Context, transport string, typ MessageType, env any) (StructuredPresentation, error) {
	if r == nil {
		return StructuredPresentation{}, fmt.Errorf("render structured: registry is required")
	}
	if err := validateIdentifier("transport", transport); err != nil {
		return StructuredPresentation{}, fmt.Errorf("render structured: %w", err)
	}
	key := structuredKey{transport: transport, typ: typ}
	renderer, ok := r.renderers[key]
	if !ok {
		return StructuredPresentation{}, fmt.Errorf("%w: transport %q message type %q", ErrStructuredRendererNotFound, transport, typ)
	}
	return renderer.renderStructuredAny(ctx, env)
}

func validateMessageType(value MessageType) error {
	raw := string(value)
	if raw == "" {
		return fmt.Errorf("message type is required")
	}
	if strings.TrimSpace(raw) != raw || strings.ToLower(raw) != raw {
		return fmt.Errorf("message type %q must be normalized", raw)
	}
	parts := strings.Split(raw, ".")
	if len(parts) < 3 {
		return fmt.Errorf("message type %q is malformed", raw)
	}
	for _, part := range parts {
		if err := validateIdentifier("message type segment", part); err != nil {
			return fmt.Errorf("message type %q is malformed", raw)
		}
	}
	return nil
}
