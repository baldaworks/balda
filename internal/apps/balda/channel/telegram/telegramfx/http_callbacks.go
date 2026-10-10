package telegramfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/tgbotkit/runtime"
)

// NewGatewayCallbackProvider adapts Telegram's token-checked webhook source.
func NewGatewayCallbackProvider(source runtime.UpdateSource) httpfx.GatewayCallbackProvider {
	return func(context.Context) ([]httpfx.GatewayCallback, error) {
		webhookSource, ok := source.(tgbotkit.SharedWebhookSource)
		if !ok {
			return nil, nil
		}
		handler, _ := webhookSource.HTTPCallback()
		return []httpfx.GatewayCallback{{
			Owner: "telegram webhook", Transport: "telegram", Endpoint: "webhook",
			Handler: handler, ReadTimeout: tgbotkit.HTTPCallbackReadTimeout, WriteTimeout: tgbotkit.HTTPCallbackWriteTimeout,
		}}, nil
	}
}
