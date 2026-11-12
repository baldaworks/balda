package session

import (
	"encoding/json"
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

func encodeConfigurationSummary(summary sessioncmd.ConfigurationSummary) (string, error) {
	if err := summary.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeConfigurationSummary(raw string, requested *string) (sessioncmd.ConfigurationSummary, error) {
	if strings.TrimSpace(raw) == "" {
		return sessioncmd.ConfigurationSummary{}, nil
	}
	var summary sessioncmd.ConfigurationSummary
	if err := json.Unmarshal([]byte(raw), &summary); err != nil || summary.Validate() != nil {
		return sessioncmd.ConfigurationSummary{}, ErrRuntimePublicationInvalid
	}
	canonical, err := json.Marshal(summary)
	if err != nil || string(canonical) != raw || !sameOptionalModel(summary.RequestedModel, requested) {
		return sessioncmd.ConfigurationSummary{}, ErrRuntimePublicationInvalid
	}
	return summary, nil
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
	return buildActivationFromReceipt(
		topic,
		kind,
		lease.PublicRuntimeMetadata(),
		lease.EffectiveModel(),
		sessioncmd.ConfigurationSummary{},
		warnings,
	)
}

func buildActivationFromReceipt(
	topic *TopicSession,
	kind sessioncmd.ActivationKind,
	metadata sessioncmd.PublicRuntimeMetadata,
	effectiveModel string,
	configuration sessioncmd.ConfigurationSummary,
	warnings []sessioncmd.WarningCode,
) (*sessioncmd.Activation, error) {
	activation := &sessioncmd.Activation{
		Kind:      kind,
		SessionID: topic.GetSessionID(),
		Label:     strings.TrimSpace(topic.GetAgentName()),
		Runtime: sessioncmd.RuntimeSummary{
			PublicRuntimeMetadata: metadata.Clone(),
			EffectiveModel:        strings.TrimSpace(effectiveModel),
		},
		Configuration: configuration.Clone(),
		Warnings:      append([]sessioncmd.WarningCode(nil), warnings...),
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
