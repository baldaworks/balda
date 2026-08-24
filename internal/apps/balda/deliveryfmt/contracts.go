package deliveryfmt

import "context"

// PromptRegistry resolves channel-specific prompt formatting contracts for
// model-authored text.
type PromptRegistry interface {
	Resolve(transport string, format DeliveryFormat) (Name, Format, Formatter, error)
}

// StructuredMessageRegistry resolves deterministic channel presentation for
// typed system-authored messages.
type StructuredMessageRegistry interface {
	RenderStructured(ctx context.Context, transport string, typ MessageType, env any) (StructuredPresentation, error)
}
