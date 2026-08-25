package deliveryfx

import (
	baldachannel "github.com/baldaworks/balda/internal/apps/balda/channel"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/permissionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/progressfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questionfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_deliveryfx",
	fx.Provide(
		newMessageFormatRegistry,
		newStructuredMessageRegistry,
		fx.Annotate(
			func() StructuredRegistryRegistrar { return permissionfmt.RegisterStructuredRenderers },
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func() StructuredRegistryRegistrar { return progressfmt.RegisterStructuredRenderers },
			fx.ResultTags(`group:"balda_delivery_structured_registrar"`),
		),
		fx.Annotate(
			func() StructuredRegistryRegistrar { return questionfmt.RegisterStructuredRenderers },
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
		func(params channelAdapterBindingsParams) *baldachannel.Router {
			adapters := make(map[string]deliverycmd.Adapter, len(params.Bindings))
			for _, binding := range params.Bindings {
				adapters[binding.ChannelType] = binding.Adapter
			}
			return baldachannel.NewRouter(adapters)
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
