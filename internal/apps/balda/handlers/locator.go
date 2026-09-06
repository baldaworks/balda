package handlers

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// LocatorResponseRenderer renders the public locator response for its transport.
type LocatorResponseRenderer interface {
	Render(ctx context.Context, locator deliverycmd.Locator) (deliveryfmt.StructuredPresentation, error)
}
