package sessionmemory

import (
	"fmt"
	"strings"
)

// Validate verifies that scope is an exact canonical locator-shaped key.
func (s Scope) Validate() error {
	if strings.TrimSpace(s.Key) != s.Key || s.Key == "" {
		return PermanentError(CodeInvalidScope, "canonical scope key is required", nil)
	}
	channel, address, ok := strings.Cut(s.Key, ":")
	if !ok || channel == "" || address == "" || address != strings.TrimSpace(address) ||
		channel != strings.ToLower(channel) || strings.ContainsAny(channel, " \t\r\n") {
		return PermanentError(CodeInvalidScope, "scope key must be canonical <channel_type>:<address_key>", nil)
	}
	switch s.Kind {
	case ScopeKindPersonal, ScopeKindGroup:
		return nil
	default:
		return PermanentError(CodeUnsupportedScope, "scope kind must be personal or group", nil)
	}
}

// Validate verifies stable Balda and provider-runtime session identity.
func (s SessionRef) Validate() error {
	if !isCanonicalID(s.SessionID) {
		return PermanentError(CodeInvalidSession, "session id is required", nil)
	}
	if !isCanonicalID(s.AgentSessionID) {
		return PermanentError(CodeInvalidSession, "agent session id is required", nil)
	}
	if s.LineageID != "" && !isCanonicalID(s.LineageID) {
		return PermanentError(CodeInvalidSession, "lineage id must be canonical", nil)
	}
	if s.PreviousSessionID != "" && !isCanonicalID(s.PreviousSessionID) {
		return PermanentError(CodeInvalidSession, "previous session id must be canonical", nil)
	}
	return nil
}

// Validate verifies a terminal, text-only, idempotent turn export.
func (t Turn) Validate() error {
	if t.SchemaVersion != SchemaVersionV1 {
		return PermanentError(CodePermanent, "unsupported turn schema version", nil)
	}
	if err := t.Scope.Validate(); err != nil {
		return err
	}
	if err := t.Session.Validate(); err != nil {
		return err
	}
	expectedID, err := TurnExportID(t.Scope, t.Session, t.SourceTurnID)
	if err != nil {
		return err
	}
	if t.ExportID != expectedID {
		return PermanentError(CodePermanent, "turn export id does not match its stable identity", nil)
	}
	if t.SourceID != "" && t.SourceID != TurnSourceID(t.ExportID) {
		return PermanentError(CodePermanent, "turn source id does not match turn identity", nil)
	}
	if t.CompletedAt.IsZero() {
		return PermanentError(CodePermanent, "turn completion time is required", nil)
	}
	status := t.TerminalStatus
	if status == "" {
		status = TurnTerminalStatusSuccess
	}
	switch status {
	case TurnTerminalStatusSuccess, TurnTerminalStatusFailed, TurnTerminalStatusInterrupted:
	default:
		return PermanentError(CodePermanent, "turn terminal status is invalid", nil)
	}
	if len(t.Messages) == 0 {
		return PermanentError(CodePermanent, "turn requires a user message", nil)
	}
	if err := validateMessage(t.Messages[0], t.ExportID, MessageRoleUser); err != nil {
		return err
	}
	next := 1
	if next < len(t.Messages) && t.Messages[next].Role == MessageRoleAssistant {
		if err := validateMessage(t.Messages[next], t.ExportID, MessageRoleAssistant); err != nil {
			return err
		}
		next++
	} else if status == TurnTerminalStatusSuccess {
		return PermanentError(CodePermanent, "successful turn requires assistant text", nil)
	}
	seenTools := make(map[string]struct{}, len(t.Messages)-next)
	if len(t.Messages)-next > maxTurnToolMessages {
		return PermanentError(CodePermanent, "turn tool message count exceeds the limit", nil)
	}
	for ; next < len(t.Messages); next++ {
		message := t.Messages[next]
		if err := validateToolMessage(message, t.ExportID); err != nil {
			return err
		}
		if _, exists := seenTools[message.MessageID]; exists {
			return PermanentError(CodePermanent, "turn tool message identity is duplicated", nil)
		}
		seenTools[message.MessageID] = struct{}{}
	}
	return nil
}

// Validate verifies an idempotent session lifecycle export.
func (b Boundary) Validate() error {
	if b.SchemaVersion != SchemaVersionV1 {
		return PermanentError(CodePermanent, "unsupported boundary schema version", nil)
	}
	if err := b.Scope.Validate(); err != nil {
		return err
	}
	if err := b.Session.Validate(); err != nil {
		return err
	}
	expectedID, err := BoundaryExportID(b.Scope, b.Session, b.TransitionID)
	if err != nil {
		return err
	}
	if b.ExportID != expectedID {
		return PermanentError(CodePermanent, "boundary export id does not match its stable identity", nil)
	}
	switch b.Reason {
	case BoundaryReasonReset, BoundaryReasonClose, BoundaryReasonRotation, BoundaryReasonShutdown:
	default:
		return PermanentError(CodePermanent, "unsupported boundary reason", nil)
	}
	if b.OccurredAt.IsZero() {
		return PermanentError(CodePermanent, "boundary occurrence time is required", nil)
	}
	return nil
}

func validateMessage(message Message, exportID string, role MessageRole) error {
	if message.Role != role {
		return PermanentError(CodePermanent, fmt.Sprintf("turn message role must be %s", role), nil)
	}
	if strings.TrimSpace(message.Text) == "" {
		return PermanentError(CodePermanent, fmt.Sprintf("%s message text is required", role), nil)
	}
	if message.MessageID != "" && message.MessageID != TurnMessageID(exportID, role) {
		return PermanentError(CodePermanent, fmt.Sprintf("%s message id does not match turn identity", role), nil)
	}
	if message.ToolName != "" || message.ToolCallID != "" {
		return PermanentError(CodePermanent, "non-tool message contains tool identity", nil)
	}
	return nil
}

func validateToolMessage(message Message, exportID string) error {
	if message.Role != MessageRoleTool || strings.TrimSpace(message.Text) == "" || !isCanonicalID(message.ToolName) || !isCanonicalID(message.ToolCallID) {
		return PermanentError(CodePermanent, "turn tool message is invalid", nil)
	}
	if message.MessageID != TurnToolMessageID(exportID, message.ToolName, message.ToolCallID) {
		return PermanentError(CodePermanent, "tool message id does not match turn identity", nil)
	}
	return nil
}

func isCanonicalID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}
