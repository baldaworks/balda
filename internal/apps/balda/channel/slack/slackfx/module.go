package slackfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/slack"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"go.uber.org/fx"
)

var Module = fx.Module(
	"balda_channel_slack_fx",
	fx.Provide(
		slack.NewAdapter,
		fx.Annotate(
			func(adapter *slack.Adapter) deliveryfx.ChannelAdapterBinding {
				return deliveryfx.ChannelAdapterBinding{
					ChannelType: slack.ChannelType,
					Adapter:     adapter,
				}
			},
			fx.ResultTags(`group:"balda_delivery_channel_adapter"`),
		),
	),
)
