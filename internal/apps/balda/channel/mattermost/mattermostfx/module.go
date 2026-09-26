// Package mattermostfx is the dependency-injection boundary for the Mattermost
// transport. It is the only package outside channel/mattermost that knows the
// transport exists; shared packages stay transport-neutral (see
// docs/architecture/transport-presentation-boundary.md).
package mattermostfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfx"
	"go.uber.org/fx"
)

// Module registers every Mattermost transport capability.
var Module = fx.Module(
	"balda_channel_mattermost_fx",
	fx.Provide(
		// Transport command advertisement. The list mirrors the shared command
		// contract so every documented Balda command is reachable from MM.
		fx.Annotate(
			func(enabled bool) commandcmd.Advertisement {
				return commandcmd.Advertisement{
					Transport: mattermost.ChannelType,
					Enabled:   enabled,
					Names: []string{
						"locator", "reset", "usage", "auto", "cancel",
						"goal", "goalkeeper", "topic", "close", "start",
						"user", "skill",
					},
				}
			},
			fx.ParamTags(`name:"balda_mattermost_enabled"`),
			fx.ResultTags(`group:"balda_command_advertisements"`),
		),

		// REST client, configured from balda.mattermost.* .
		fx.Annotate(
			NewClient,
			fx.ParamTags(
				`name:"balda_mattermost_server_url"`,
				`name:"balda_mattermost_token"`,
				`name:"balda_mattermost_bot_user_id"`,
				`name:"balda_mattermost_enabled"`,
				``,
			),
		),

		// Delivery adapter.
		// Mattermost bots cannot emit typing events, so the adapter keeps the
		// shared progress surface as a no-op for interface parity.
		mattermost.NewAdapter,
		fx.Annotate(
			func(adapter *mattermost.Adapter) deliveryfx.ChannelAdapterBinding {
				return deliveryfx.ChannelAdapterBinding{
					ChannelType: mattermost.ChannelType,
					Adapter:     adapter,
				}
			},
			fx.ResultTags(`group:"balda_delivery_channel_adapter"`),
		),

		// Prompt formatting contribution: teach the model Mattermost Markdown.
		fx.Annotate(
			NewPromptRegistryContribution,
			fx.ResultTags(`group:"balda_delivery_prompt_contribution"`),
		),

		// Structured renderers for system-authored service messages.
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

		// Inbound websocket ingress. The processor is optional so the transport
		// can be wired in isolation (tests, tooling) without the full app.
		NewIngress,
		fx.Annotate(
			func(ingress *mattermost.Ingress) appports.TransportLifecycleStage {
				return appports.TransportLifecycleStage{
					Name:  "mattermost ingress",
					Start: ingress.Start,
					Stop:  ingress.Stop,
				}
			},
			fx.ResultTags(`group:"balda_transport_lifecycle_stage"`),
		),
	),
)
