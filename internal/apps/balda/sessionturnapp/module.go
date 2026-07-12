package sessionturnapp

import (
	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/memory"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/sessionturn"
	"go.uber.org/fx"
)

var Module = fx.Module("balda_sessionturnapp",
	fx.Provide(
		NewTurnExecutionService,
		fx.Annotate(NewProviderTurnExecutorFromService, fx.As(new(sessionturn.Executor))),
		func(m *baldasession.Manager) sessionturn.SessionAccessor {
			return NewSessionAccessor(m)
		},
		func(store *memory.Store) sessionturn.MemoryStateProvider {
			return NewMemoryStateProvider(store)
		},
		sessionturn.NewRunner,
		fx.Annotate(func(r *sessionturn.Runner) appports.SessionTurnRunner { return r }),
	),
)
