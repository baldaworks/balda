package session

import (
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
)

// EnsureResult is the authoritative outcome of one conversational session
// ensure. Activation remains pending until the caller acknowledges durable
// turn admission.
type EnsureResult struct {
	*TopicSession
	Activation *sessioncmd.Activation
}

func (m *Manager) buildActivation(
	topic *TopicSession,
	kind sessioncmd.ActivationKind,
	warnings []sessioncmd.WarningCode,
) (*sessioncmd.Activation, error) {
	if topic == nil {
		return nil, fmt.Errorf("activation topic session is required")
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		return nil, err
	}
	defer lease.Release()

	activation := &sessioncmd.Activation{
		Kind:      kind,
		SessionID: topic.GetSessionID(),
		Label:     strings.TrimSpace(topic.GetAgentName()),
		Runtime: sessioncmd.RuntimeSummary{
			PublicRuntimeMetadata: lease.PublicRuntimeMetadata(),
			EffectiveModel:        strings.TrimSpace(lease.EffectiveModel()),
		},
		Warnings: append([]sessioncmd.WarningCode(nil), warnings...),
	}
	if err := activation.Validate(); err != nil {
		return nil, fmt.Errorf("build session activation: %w", err)
	}
	return activation, nil
}

// AcknowledgeActivation clears a pending activation only when it equals the
// snapshot admitted on the ordinary session turn.
func (m *Manager) AcknowledgeActivation(sessionID string, admitted sessioncmd.Activation) bool {
	if m == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || admitted.SessionID != sessionID {
		return false
	}
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()
	m.mu.RLock()
	topic := m.sessions[sessionID]
	m.mu.RUnlock()
	return topic.acknowledgeActivation(admitted)
}
