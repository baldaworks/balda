package permissions

import (
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/questions"
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
			Logger     zerolog.Logger
		}) *Service {
			return New(params.Config, params.Questions, params.Dispatcher, params.Logger)
		},
	),
)
