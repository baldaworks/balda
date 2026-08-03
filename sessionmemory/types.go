package sessionmemory

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	// SchemaVersionV1 identifies the first portable session-memory schema.
	SchemaVersionV1 = "session-memory/v1"

	// DefaultSearchLimit is used when a search request omits its limit.
	DefaultSearchLimit = 10
	// MaxSearchLimit bounds the number of results requested from a provider.
	MaxSearchLimit = 100
	// MaxSearchQueryBytes bounds a UTF-8 search query at the provider boundary.
	MaxSearchQueryBytes = 4096
)

// ScopeKind classifies a locator without changing its isolation boundary.
type ScopeKind string

const (
	// ScopeKindPersonal is a direct or private conversation scope.
	ScopeKindPersonal ScopeKind = "personal"
	// ScopeKindGroup is a shared group, channel, or stream scope. A topic or
	// thread may still have this kind when its parent audience is shared.
	ScopeKindGroup ScopeKind = "group"
)

// Scope identifies the exact locator partition used for export and recall.
type Scope struct {
	Key  string    `json:"key"`
	Kind ScopeKind `json:"kind"`
}

// SessionRef identifies the Balda session and its provider-runtime lineage.
type SessionRef struct {
	SessionID         string `json:"session_id"`
	AgentSessionID    string `json:"agent_session_id"`
	LineageID         string `json:"lineage_id,omitempty"`
	PreviousSessionID string `json:"previous_session_id,omitempty"`
}

// MessageRole identifies one eligible conversational message role.
type MessageRole string

const (
	// MessageRoleUser identifies normalized user-visible input.
	MessageRoleUser MessageRole = "user"
	// MessageRoleAssistant identifies final visible assistant output.
	MessageRoleAssistant MessageRole = "assistant"
)

// Message is one text-only conversational message in a completed turn.
type Message struct {
	Role MessageRole `json:"role"`
	Text string      `json:"text"`
}

// Turn is an idempotent completed-turn export.
type Turn struct {
	SchemaVersion string     `json:"schema_version"`
	ExportID      string     `json:"export_id"`
	Scope         Scope      `json:"scope"`
	Session       SessionRef `json:"session"`
	SourceTurnID  string     `json:"source_turn_id"`
	CompletedAt   time.Time  `json:"completed_at"`
	Messages      []Message  `json:"messages"`
}

// BoundaryReason identifies a session lifecycle transition.
type BoundaryReason string

const (
	// BoundaryReasonReset marks a history reset within an existing locator.
	BoundaryReasonReset BoundaryReason = "reset"
	// BoundaryReasonClose marks an explicitly closed session.
	BoundaryReasonClose BoundaryReason = "close"
	// BoundaryReasonRotation marks replacement by a new session lineage.
	BoundaryReasonRotation BoundaryReason = "rotation"
	// BoundaryReasonShutdown marks bounded application shutdown extraction.
	BoundaryReasonShutdown BoundaryReason = "shutdown"
)

// Boundary is an idempotent session lifecycle export.
type Boundary struct {
	SchemaVersion string         `json:"schema_version"`
	ExportID      string         `json:"export_id"`
	Scope         Scope          `json:"scope"`
	Session       SessionRef     `json:"session"`
	TransitionID  string         `json:"transition_id"`
	Reason        BoundaryReason `json:"reason"`
	OccurredAt    time.Time      `json:"occurred_at"`
}

// Provider synchronizes completed conversation data and lifecycle boundaries.
// Native retrieval and forgetting are exposed through the derived application
// port rather than a remote-provider compatibility contract.
type Provider interface {
	SyncTurn(ctx context.Context, turn Turn) error
	OnSessionBoundary(ctx context.Context, boundary Boundary) error
	Close(ctx context.Context) error
}

// NewTurn builds and validates an idempotent completed-turn export.
func NewTurn(scope Scope, session SessionRef, sourceTurnID string, completedAt time.Time, userText, assistantText string) (Turn, error) {
	exportID, err := TurnExportID(scope, session, sourceTurnID)
	if err != nil {
		return Turn{}, err
	}
	turn := Turn{
		SchemaVersion: SchemaVersionV1,
		ExportID:      exportID,
		Scope:         scope,
		Session:       session,
		SourceTurnID:  strings.TrimSpace(sourceTurnID),
		CompletedAt:   completedAt,
		Messages: []Message{
			{Role: MessageRoleUser, Text: strings.TrimSpace(userText)},
			{Role: MessageRoleAssistant, Text: strings.TrimSpace(assistantText)},
		},
	}
	if err := turn.Validate(); err != nil {
		return Turn{}, err
	}
	return turn, nil
}

// NewBoundary builds and validates an idempotent lifecycle export.
func NewBoundary(scope Scope, session SessionRef, transitionID string, reason BoundaryReason, occurredAt time.Time) (Boundary, error) {
	exportID, err := BoundaryExportID(scope, session, transitionID)
	if err != nil {
		return Boundary{}, err
	}
	boundary := Boundary{
		SchemaVersion: SchemaVersionV1,
		ExportID:      exportID,
		Scope:         scope,
		Session:       session,
		TransitionID:  strings.TrimSpace(transitionID),
		Reason:        reason,
		OccurredAt:    occurredAt,
	}
	if err := boundary.Validate(); err != nil {
		return Boundary{}, err
	}
	return boundary, nil
}

// TurnExportID derives a stable idempotency key for one source turn.
func TurnExportID(scope Scope, session SessionRef, sourceTurnID string) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if err := session.Validate(); err != nil {
		return "", err
	}
	sourceTurnID = strings.TrimSpace(sourceTurnID)
	if sourceTurnID == "" {
		return "", PermanentError(CodeInvalidSession, "source turn id is required", nil)
	}
	return stableExportID("turn", scope.Key, session.SessionID, sourceTurnID), nil
}

// BoundaryExportID derives a stable idempotency key for one lifecycle transition.
func BoundaryExportID(scope Scope, session SessionRef, transitionID string) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	if err := session.Validate(); err != nil {
		return "", err
	}
	transitionID = strings.TrimSpace(transitionID)
	if transitionID == "" {
		return "", PermanentError(CodeInvalidSession, "transition id is required", nil)
	}
	return stableExportID("boundary", scope.Key, session.SessionID, transitionID), nil
}

func stableExportID(kind string, parts ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(SchemaVersionV1))
	writeHashPart(hash, kind)
	for _, part := range parts {
		writeHashPart(hash, part)
	}
	return fmt.Sprintf("session-memory:v1:%s:%s", kind, hex.EncodeToString(hash.Sum(nil)))
}

type hashWriter interface {
	Write(data []byte) (int, error)
}

func writeHashPart(dst hashWriter, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = dst.Write(size[:])
	_, _ = dst.Write([]byte(value))
}
