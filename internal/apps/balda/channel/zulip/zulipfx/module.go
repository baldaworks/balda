package zulipfx

import (
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module(
	"balda_channel_zulip_fx",
	fx.Provide(
		func(client *zulip.Client, logger zerolog.Logger) *zulip.Adapter {
			adapter := zulip.NewAdapter(client, logger)
			adapter.SetTypingThrottleInterval(4 * time.Second)
			return adapter
		},
		fx.Annotate(
			func(adapter *zulip.Adapter) deliveryfx.ChannelAdapterBinding {
				return deliveryfx.ChannelAdapterBinding{
					ChannelType: zulip.ChannelType,
					Adapter:     adapter,
				}
			},
			fx.ResultTags(`group:"balda_delivery_channel_adapter"`),
		),
	),
)
