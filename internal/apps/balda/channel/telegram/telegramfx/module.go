package telegramfx

import (
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"go.uber.org/fx"
)

var Module = fx.Module(
	"balda_channel_telegram_fx",
	fx.Provide(
		fx.Annotate(
			func(
				tgClient client.ClientWithResponsesInterface,
				logger zerolog.Logger,
				formattingMode string,
			) *telegram.Messenger {
				m := telegram.NewMessenger(tgClient, logger)
				m.SetAgentReplyFormattingMode(formattingMode)
				return m
			},
			fx.ParamTags(``, ``, `name:"balda_telegram_formatting_mode"`),
		),
		fx.Annotate(
			func(m *telegram.Messenger) telegram.TelegramMessenger { return m },
		),
		func(params telegram.AdapterParams) *telegram.Adapter {
			adapter := telegram.NewAdapter(params)
			adapter.SetTypingThrottleInterval(4 * time.Second)
			return adapter
		},
		fx.Annotate(
			func(adapter *telegram.Adapter) deliveryfx.ChannelAdapterBinding {
				return deliveryfx.ChannelAdapterBinding{
					ChannelType: telegram.ChannelType,
					Adapter:     adapter,
				}
			},
			fx.ResultTags(`group:"balda_delivery_channel_adapter"`),
		),
		fx.Annotate(
			NewPromptRegistryContribution,
			fx.ResultTags(`group:"balda_delivery_prompt_contribution"`),
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
	),
)
