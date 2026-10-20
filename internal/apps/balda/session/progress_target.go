package session

import (
	"context"
	"fmt"
	"strings"
)

// ProgressTarget is the server-owned routing context for an active session.
type ProgressTarget struct {
	Locator    SessionLocator
	SessionID  string
	Kind       Kind
	OwnerJobID string
}

// ResolveProgressTarget resolves a canonical locator to its exact active
// session. Policy fields always come from the active TopicSession.
func (m *Manager) ResolveProgressTarget(_ context.Context, locator SessionLocator) (ProgressTarget, error) {
	sessionID := strings.TrimSpace(locator.SessionID)
	channelType := strings.ToLower(strings.TrimSpace(locator.ChannelType))
	addressKey := strings.TrimSpace(locator.AddressKey)
	if sessionID == "" || channelType == "" || addressKey == "" {
		return ProgressTarget{}, fmt.Errorf("complete canonical session locator is required")
	}

	m.mu.RLock()
	active := m.sessions[sessionID]
	m.mu.RUnlock()
	if active == nil {
		return ProgressTarget{}, fmt.Errorf("active session %q not found", sessionID)
	}
	if strings.ToLower(strings.TrimSpace(active.locator.ChannelType)) != channelType ||
		strings.TrimSpace(active.locator.AddressKey) != addressKey ||
		strings.TrimSpace(active.locator.SessionID) != sessionID {
		return ProgressTarget{}, fmt.Errorf("session locator does not match active target")
	}
	ownerJobID := strings.TrimSpace(active.ownerJobID)
	if err := validateExecutionContext(active.kind, ownerJobID); err != nil {
		return ProgressTarget{}, fmt.Errorf("active session has invalid execution context: %w", err)
	}
	return ProgressTarget{
		Locator:    active.locator,
		SessionID:  active.sessionID,
		Kind:       active.kind,
		OwnerJobID: ownerJobID,
	}, nil
}
