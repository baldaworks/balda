package telegram

import (
	"context"
	"strings"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

func (s *Server) getProviderName() string {
	providerName := strings.TrimSpace(s.sessionManager.BaldaProviderID())
	if providerName == "" {
		s.mu.RLock()
		defer s.mu.RUnlock()
		providerName = strings.TrimSpace(s.baldaProviderName)
	}
	return providerName
}

func (s *Server) sendSessionStartupNotice(ctx context.Context, locator baldasession.SessionLocator, sessionID string) {
	if s.sessionManager == nil {
		return
	}
	notice := strings.TrimSpace(s.sessionManager.TakeStartupNotice(sessionID))
	if notice == "" {
		return
	}
	if err := sendPlain(ctx, s.actorDispatcher, serverActorAddress, locator, notice); err != nil {
		s.logger.Warn().Err(err).Str("session_id", sessionID).Msg("failed to send session startup notice")
	}
}
