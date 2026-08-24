package deliveryfx

import (
	"time"

	baldachannel "github.com/baldaworks/balda/internal/apps/balda/channel"
	baldaslack "github.com/baldaworks/balda/internal/apps/balda/channel/slack"
	baldaslackagent "github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	baldazulip "github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/messenger"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_deliveryfx",
	fx.Provide(
		newMessageFormatRegistry,
		newStructuredMessageRegistry,
		fx.Annotate(
			func() structuredRegistryRegistrar { return permissionfmt.RegisterStructuredRenderers },
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func() structuredRegistryRegistrar { return progressfmt.RegisterStructuredRenderers },
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func() structuredRegistryRegistrar { return questionfmt.RegisterStructuredRenderers },
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func(reg *deliveryfmt.Registry) deliveryfmt.PromptRegistry { return reg },
			fx.As(new(deliveryfmt.PromptRegistry)),
		),
		fx.Annotate(
			func(reg *deliveryfmt.StructuredRegistry) deliveryfmt.StructuredMessageRegistry { return reg },
			fx.As(new(deliveryfmt.StructuredMessageRegistry)),
		),
		fx.Annotate(
			func(
				tgClient client.ClientWithResponsesInterface,
				logger zerolog.Logger,
				formattingMode string,
			) *messenger.Messenger {
				m := messenger.NewMessenger(tgClient, logger)
				m.SetAgentReplyFormattingMode(formattingMode)
				return m
			},
			fx.ParamTags(``, ``, `name:"balda_telegram_formatting_mode"`),
		),
		fx.Annotate(
			func(m *messenger.Messenger) baldatelegram.TelegramMessenger { return m },
		),
		func(params baldatelegram.AdapterParams) *baldatelegram.Adapter {
			adapter := baldatelegram.NewAdapter(params)
			adapter.SetTypingThrottleInterval(4 * time.Second)
			return adapter
		},
		func(client *baldazulip.Client, logger zerolog.Logger) *baldazulip.Adapter {
			adapter := baldazulip.NewAdapter(client, logger)
			adapter.SetTypingThrottleInterval(4 * time.Second)
			return adapter
		},
		baldaslack.NewAdapter,
		baldaslackagent.NewAdapter,
		func(tg *baldatelegram.Adapter, zu *baldazulip.Adapter, sl *baldaslack.Adapter, sla *baldaslackagent.Adapter) *baldachannel.Router {
			return baldachannel.NewRouter(map[string]deliverycmd.Adapter{
				string(deliverycmd.ChannelTypeTelegram):   tg,
				string(deliverycmd.ChannelTypeZulip):      zu,
				string(deliverycmd.ChannelTypeSlackChat):  sl,
				string(deliverycmd.ChannelTypeSlackAgent): sla,
			})
		},
		NewChannelDispatcher,
		fx.Annotate(
			func(dispatcher actortransport.Dispatcher) questions.ControlPublisher {
				return questionControlPublisher{dispatcher: dispatcher}
			},
			fx.As(new(questions.ControlPublisher)),
		),
	),
)
