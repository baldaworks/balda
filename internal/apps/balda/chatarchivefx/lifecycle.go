// Package chatarchivefx binds archive ports to the selected shared database.
package chatarchivefx

import (
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

// Lifecycle embeds only the storage capability required by session lifecycle.
type Lifecycle struct{ state.ChatArchiveStore }

// Module makes archive journaling mandatory in the production session graph.
var Module = fx.Module("balda_chatarchive", fx.Provide(func(p state.Provider) session.ArchiveLifecycle { return Lifecycle{ChatArchiveStore: p.ChatArchive()} }))
