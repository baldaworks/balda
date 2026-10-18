package handlersfx

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
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
	if prepared.Ready && prepared.Activated {
		locator := inboundLocator(inbound)
		locator.SessionID = prepared.SessionID
		_ = sendMarkdown(ctx, h.actorDispatcher, serverActorAddress, locator, sessionPreparationWelcome(prepared, inbound.Direct))
		if prepared.StartupNotice != "" {
			_ = sendPlain(ctx, h.actorDispatcher, serverActorAddress, locator, prepared.StartupNotice)
		}
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
	if prepared.Ready && prepared.Activated {
		locator := inboundLocator(inbound)
		locator.SessionID = prepared.SessionID
		_ = h.sendPlain(ctx, locator, sessionPreparationWelcome(prepared, inbound.Direct))
		if prepared.StartupNotice != "" {
			_ = h.sendPlain(ctx, locator, prepared.StartupNotice)
		}
	}
	return prepared, nil
}

func sessionPreparationWelcome(prepared ingressapp.SessionPreparation, direct bool) string {
	label := "balda"
	if direct {
		label = prepared.SessionLabel
	}
	return welcome.BuildAgentWelcomeMessage(label, prepared.SessionID, prepared.AgentType, prepared.Model, prepared.ReasoningEffort, prepared.MCPServers)
}
