package chatfx

import (
	"context"
	"errors"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
)

const autoSessionLabel = "auto"

// SessionAdapter adapts baldasession.Manager to chatapp.SessionPreparer.
type SessionAdapter struct {
	sessions *baldasession.Manager
}

// NewSessionAdapter constructs a SessionAdapter backed by baldasession.Manager.
func NewSessionAdapter(sessions *baldasession.Manager) *SessionAdapter {
	return &SessionAdapter{sessions: sessions}
}

// Prepare ensures or restores a topic session and returns preparation metadata.
func (a *SessionAdapter) Prepare(ctx context.Context, inbound chatapp.InboundContext) (chatapp.SessionPreparation, error) {
	if a == nil || a.sessions == nil {
		return chatapp.SessionPreparation{}, actorlayer.TransientError(fmt.Errorf("session manager is unavailable"))
	}
	locator := baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
	topicSession, err := a.getOrCreateSession(ctx, locator, inbound.UserID)
	if err != nil {
		return chatapp.SessionPreparation{}, err
	}
	return chatapp.SessionPreparation{
		Ready:           true,
		UserID:          topicSession.GetUserID(),
		RequesterUserID: inbound.UserID,
		AgentSessionID:  topicSession.GetAgentSessionID(),
		TopicID:         inbound.TopicID,
	}, nil
}

func (a *SessionAdapter) getOrCreateSession(ctx context.Context, locator baldasession.SessionLocator, subject string) (*baldasession.TopicSession, error) {
	if existing, _ := a.sessions.GetSession(locator); existing != nil {
		return existing, nil
	}
	topicSession, err := a.sessions.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject})
	if err == nil && topicSession != nil {
		return topicSession, nil
	}
	if err != nil && !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, err
	}
	return a.sessions.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: subject}, autoSessionLabel)
}
