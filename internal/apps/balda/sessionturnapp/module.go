package sessionturnapp

import (
	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/sessionturn"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_sessionturnapp",
	fx.Provide(
		fx.Annotate(NewTurnExecutionService, fx.ParamTags(``, ``, ``, ``, `name:"balda_automode_max_turns"`)),
		fx.Annotate(NewProviderTurnExecutorFromService, fx.As(new(sessionturn.Executor))),
		NewSessionAccessor,
		NewMemoryStateProvider,
		sessionturn.NewRunner,
		fx.Annotate(func(r *sessionturn.Runner) appports.SessionTurnRunner { return r }),
	),
)
