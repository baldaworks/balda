// Package chatarchivefx binds archive ports to the selected shared database.
package chatarchivefx

import (
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/chatarchiveapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

// Lifecycle embeds only the storage capability required by session lifecycle.
type Lifecycle struct{ state.ChatArchiveStore }

// Module makes archive journaling mandatory in the production session graph.
var Module = fx.Module("balda_chatarchive", fx.Invoke(func(service *sessionturnapp.TurnExecutionService, archive *chatarchiveapp.Service) {
	service.SetFinalArchive(archive)
}), fx.Provide(
	func(s *chatarchiveapp.Service, m *session.Manager) commandfx.SkillAdmission {
		return &skillAdmission{inputs: s, sessions: m}
	},
	func(s *chatarchiveapp.Service, p state.Provider) questions.Archive {
		return &questionArchive{inputs: s, sessions: p.Sessions()}
	},
	func(p state.Provider) *chatarchiveapp.Service { return chatarchiveapp.New(p.ChatArchive()) },
	func(s *chatarchiveapp.Service) chatapp.InputRecorder { return s },
	func(s *chatarchiveapp.Service) ingressapp.InputRecorder { return s },
	func(p state.Provider) session.ArchiveLifecycle { return Lifecycle{ChatArchiveStore: p.ChatArchive()} }))
