package permissions

import (
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_permissions",
	fx.Provide(
		func(params struct {
			fx.In

			Config     Config
			Questions  *questions.Service
			Dispatcher actortransport.Dispatcher
			Structured deliveryfmt.StructuredMessageRegistry
			Logger     zerolog.Logger
		}) *Service {
			return New(params.Config, params.Questions, params.Dispatcher, params.Structured, params.Logger)
		},
	),
)
