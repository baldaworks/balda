package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
)

func (s *Server) bootstrapOwnerSession(ctx context.Context, ownerID, chatID int64) (*baldasession.TopicSession, error) {
	if ownerID == 0 {
		return nil, fmt.Errorf("telegram owner id is required")
	}
	if chatID == 0 {
		return nil, fmt.Errorf("telegram owner chat id is required")
	}
	if s.sessionManager == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()

	providerName := s.getProviderName()
	if providerName == "" {
		return nil, fmt.Errorf("balda provider is not configured")
	}

	locator := NewLocator(chatID, 0)
	ts, err := s.sessionManager.GetSession(locator)
	if err == nil {
		return ts, nil
	}

	ts, err = s.sessionManager.RestoreSession(ctx, baldasession.SessionContext{
		Locator: locator,
		UserID:  UserID(ownerID),
	})
	if err != nil {
		if !errors.Is(err, baldasession.ErrNoPersistedSession) {
			return nil, fmt.Errorf("restore owner session: %w", err)
		}
		ts, err = s.sessionManager.EnsureSession(ctx, baldasession.SessionContext{
			Locator: locator,
			UserID:  UserID(ownerID),
		}, ownerSessionLabel)
		if err != nil {
			return nil, fmt.Errorf("create owner session: %w", err)
		}
	}

	metadata := s.sessionManager.GetAgentMetadata(providerName)
	welcomeMessage := welcome.BuildAgentWelcomeMessage(ownerSessionLabel, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
	if err := sendMarkdown(ctx, s.actorDispatcher, serverActorAddress, locator, welcomeMessage); err != nil {
		s.logger.Warn().Err(err).Str("session_id", ts.GetSessionID()).Msg("failed to send owner session welcome")
	}
	s.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())

	s.logger.Info().
		Int64("owner_id", ownerID).
		Int64("chat_id", chatID).
		Str("agent", providerName).
		Msg("owner session bootstrapped")
	return ts, nil
}

func (s *Server) getProviderName() string {
	if s.sessionManager == nil {
		return ""
	}
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
