package slackagentfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"go.uber.org/fx"
)

var Module = fx.Module(
	"balda_channel_slackagent_fx",
	fx.Provide(
		fx.Annotate(
			func() sessionmemoryapp.ScopeClassifierContribution {
				return sessionmemoryapp.ScopeClassifierContribution{ChannelType: slackagent.ChannelType, Classifier: slackagent.ClassifyLocatorScope}
			},
			fx.ResultTags(`group:"balda_session_memory_scope_classifier"`),
		),
		func(adapter *slackagent.Adapter) slackagent.SessionLifecycle { return adapter },
		fx.Annotate(
			func(cfg slackagent.Config) commandcmd.Advertisement {
				return commandcmd.Advertisement{Transport: slackagent.ChannelType, Enabled: cfg.Enabled, Names: slackagent.SupportedCommands()}
			},
			fx.ResultTags(`group:"balda_command_advertisements"`),
		),
		newInboundProcessor,
		slackagent.NewCurrentFileIngestor,
		slackagent.NewHistoricalContextHydrator,
		fx.Annotate(
			newTurnCanceller,
			fx.As(new(slackagent.TurnCanceller)),
		),
		fx.Annotate(
			newBoundaryObserver,
			fx.As(new(baldasession.BoundaryObserver)),
			fx.ResultTags(`group:"balda_session_boundary_observer"`),
		),
		newBindingServer,
		fx.Annotate(
			NewGatewayCallbackProvider,
			fx.ResultTags(`group:"balda_http_gateway_callback_providers"`),
		),
		func(client *slackagent.Client) slackagent.MessageClient { return client },
		func(client *slackagent.Client) slackagent.ThreadHistoryReader { return client },
		func(client *slackagent.Client) slackagent.FileClient { return client },
		func(client *slackagent.Client) slackagent.MediaUploadClient { return client },
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
			NewLocatorStructuredRegistrar,
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			NewGoalProgressStructuredRegistrar,
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			NewGoalOutcomeStructuredRegistrar,
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func() sessionturnapp.ProgressTransportHook { return progressTransportHook{} },
		),
	),
)
