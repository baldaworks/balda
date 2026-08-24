package slackagentfx

import (
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"go.uber.org/fx"
)

var Module = fx.Module(
	"balda_channel_slackagent_fx",
	fx.Provide(
		slackagent.NewServer,
		fx.Annotate(
			func() structuredRegistryRegistrar { return registerStructuredRenderers },
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func() sessionturnapp.ProgressTransportHook { return progressTransportHook{} },
		),
	),
)
