package mattermostfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
)

// NewGatewayCallbackProvider adapts the checked Mattermost command receiver.
func NewGatewayCallbackProvider(server *mattermost.CommandServer) httpfx.GatewayCallbackProvider {
	return func(ctx context.Context) ([]httpfx.GatewayCallback, error) {
		handler, _, err := server.HTTPCallback(ctx)
		if err != nil || handler == nil {
			return nil, err
		}
		return []httpfx.GatewayCallback{{
			Owner: "mattermost commands", Transport: "mattermost", Endpoint: "commands",
			Handler: handler, ReadTimeout: mattermost.HTTPCallbackReadTimeout, WriteTimeout: mattermost.HTTPCallbackWriteTimeout,
		}}, nil
	}
}
