package slackagentfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
)

// NewGatewayCallbackProvider adapts Slack Agent's checked handlers for shared HTTP wiring.
func NewGatewayCallbackProvider(server *slackagent.Server) httpfx.GatewayCallbackProvider {
	return func(context.Context) ([]httpfx.GatewayCallback, error) {
		callbacks, err := server.HTTPCallbacks()
		if err != nil {
			return nil, err
		}
		if callbacks.Events == nil {
			return nil, nil
		}
		return []httpfx.GatewayCallback{
			{Owner: "slack agent events", Transport: "slack", Endpoint: "events", LegacyPath: callbacks.EventsLegacyPath, Handler: callbacks.Events, ReadTimeout: slackagent.HTTPCallbackReadTimeout, WriteTimeout: slackagent.HTTPCallbackWriteTimeout},
			{Owner: "slack agent commands", Transport: "slack", Endpoint: "commands", LegacyPath: callbacks.CommandsLegacyPath, Handler: callbacks.Commands, ReadTimeout: slackagent.HTTPCallbackReadTimeout, WriteTimeout: slackagent.HTTPCallbackWriteTimeout},
		}, nil
	}
}
