package handlersfx

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/go-actorlayer"
)

// These adapters only present the neutral outcome. Session preparation itself
// is supplied by the same application adapter for every conversational inlet.
func (h *telegramInboundHandler) prepareTelegramSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	if h.sessionPreparer == nil {
		return ingressapp.SessionPreparation{}, actorlayer.TransientError(fmt.Errorf("session preparer is unavailable"))
	}
	prepared, err := h.sessionPreparer.Prepare(ctx, inbound)
	if err != nil {
		return prepared, err
	}
	return prepared, nil
}

func (h *zulipInboundHandler) prepareZulipSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	if h.sessionPreparer == nil {
		return ingressapp.SessionPreparation{}, actorlayer.TransientError(fmt.Errorf("session preparer is unavailable"))
	}
	prepared, err := h.sessionPreparer.Prepare(ctx, inbound)
	if err != nil {
		return prepared, err
	}
	return prepared, nil
}
