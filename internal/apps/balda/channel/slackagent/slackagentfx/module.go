package slackagentfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"go.uber.org/fx"
)

var Module = fx.Module(
	"balda_channel_slackagent_fx",
	fx.Provide(
		func(adapter *slackagent.Adapter) slackagent.SessionLifecycle { return adapter },
		fx.Annotate(
			newInboundProcessor,
			fx.As(new(slackagent.InboundProcessor)),
		),
		fx.Annotate(
			newTurnCanceller,
			fx.As(new(slackagent.TurnCanceller)),
		),
		fx.Annotate(
			newBoundaryObserver,
			fx.As(new(baldasession.BoundaryObserver)),
			fx.ResultTags(`group:"balda_session_boundary_observer"`),
		),
		slackagent.NewServer,
		func(client *slackagent.Client) slackagent.MessageClient { return client },
		func(client *slackagent.Client) threadHistoryReader { return client },
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
		fx.Annotate(
			func(server *slackagent.Server) appports.TransportLifecycleStage {
				return appports.TransportLifecycleStage{
					Name:  "slack agent ingress",
					Start: server.Start,
					Stop:  server.Stop,
				}
			},
			fx.ResultTags(`group:"balda_transport_lifecycle_stage"`),
		),
	),
)
