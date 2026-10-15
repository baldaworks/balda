package zulipfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
)

// NewGatewayCallbackProvider adapts Zulip's checked webhook receiver.
func NewGatewayCallbackProvider(server *zulip.Server) httpfx.GatewayCallbackProvider {
	return func(context.Context) ([]httpfx.GatewayCallback, error) {
		handler, _, err := server.HTTPCallback()
		if err != nil || handler == nil {
			return nil, err
		}
		return []httpfx.GatewayCallback{{
			Owner: "zulip webhook", Transport: "zulip", Endpoint: "webhook",
			Handler: handler, ReadTimeout: zulip.HTTPCallbackReadTimeout, WriteTimeout: zulip.HTTPCallbackWriteTimeout,
		}}, nil
	}
}
