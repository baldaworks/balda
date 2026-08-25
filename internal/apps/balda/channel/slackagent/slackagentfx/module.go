package slackagentfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"go.uber.org/fx"
)

var Module = fx.Module(
	"balda_channel_slackagent_fx",
	fx.Provide(
		slackagent.NewServer,
		func(client *slackagent.Client) slackagent.MessageClient { return client },
		slackagent.NewAdapter,
		fx.Annotate(
			func(adapter *slackagent.Adapter) deliveryfx.ChannelAdapterBinding {
				return deliveryfx.ChannelAdapterBinding{
					ChannelType: slackagent.ChannelType,
					Adapter:     adapter,
				}
			},
			fx.ResultTags(`group:"balda_delivery_channel_adapter"`),
		),
		fx.Annotate(
			NewQuestionStructuredRegistrar,
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			NewPermissionStructuredRegistrar,
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			NewProgressStructuredRegistrar,
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func() sessionturnapp.ProgressTransportHook { return progressTransportHook{} },
		),
	),
)
