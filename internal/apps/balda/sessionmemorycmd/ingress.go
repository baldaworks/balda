package sessionmemorycmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

// IngressSchemaVersionV1 identifies the durable producer-local export spool.
const IngressSchemaVersionV1 = "session-memory-ingress/v1"

// IngressState is the delivery lifecycle of one durable export.
type IngressState string

const (
	IngressStatePending   IngressState = "pending"
	IngressStateLeased    IngressState = "leased"
	IngressStatePublished IngressState = "published"
	IngressStateTerminal  IngressState = "terminal"
)

// IngressRecord is a transport-neutral, producer-owned durable export. The
// export ID is the idempotency key used for both local persistence and
// JetStream publication; ScopeSequence preserves FIFO within one scope.
type IngressRecord struct {
	SchemaVersion string       `json:"schema_version"`
	Export        Export       `json:"export"`
	ScopeSequence uint64       `json:"scope_sequence"`
	State         IngressState `json:"state"`
	Attempts      uint32       `json:"attempts"`
	LeaseOwner    string       `json:"lease_owner,omitempty"`
	LeaseUntil    *time.Time   `json:"lease_until,omitempty"`
	NextAttemptAt *time.Time   `json:"next_attempt_at,omitempty"`
	LastError     string       `json:"last_error,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	PublishedAt   *time.Time   `json:"published_at,omitempty"`
}

// IngressOutboxStats is the bounded operational view of a producer-local
// outbox. It deliberately exposes no export payloads.
type IngressOutboxStats struct {
	PendingCount     uint64
	TerminalCount    uint64
	OldestPendingAt  *time.Time
	OldestPendingAge time.Duration
}

// NewIngressRecord creates the pending durable handoff for one validated
// export. The storage adapter assigns ScopeSequence atomically per scope.
func NewIngressRecord(export Export, createdAt time.Time) (IngressRecord, error) {
	if err := export.Validate(); err != nil {
		return IngressRecord{}, err
	}
	if createdAt.IsZero() {
		return IngressRecord{}, sessionmemory.PermanentError(sessionmemory.CodePermanent, "ingress record timestamp is required", nil)
	}
	createdAt = createdAt.UTC()
	return IngressRecord{
		SchemaVersion: IngressSchemaVersionV1,
		Export:        export,
		State:         IngressStatePending,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}, nil
}

// ExportID returns the stable idempotency key of this record's export.
func (r IngressRecord) ExportID() string { return r.Export.ExportID() }

// Scope returns the exact scope carried by the export itself.
func (r IngressRecord) Scope() (sessionmemory.Scope, error) {
	switch r.Export.Kind {
	case KindTurn:
		if r.Export.Turn == nil {
			return sessionmemory.Scope{}, sessionmemory.PermanentError(sessionmemory.CodePermanent, "ingress turn export is missing", nil)
		}
		return r.Export.Turn.Scope, nil
	case KindBoundary:
		if r.Export.Boundary == nil {
			return sessionmemory.Scope{}, sessionmemory.PermanentError(sessionmemory.CodePermanent, "ingress boundary export is missing", nil)
		}
		return r.Export.Boundary.Scope, nil
	default:
		return sessionmemory.Scope{}, sessionmemory.PermanentError(sessionmemory.CodePermanent, "ingress export kind is invalid", nil)
	}
}

// Validate rejects ambiguous or unsafe durable lifecycle records. LastError is
// a redacted diagnostic field and cannot contain control characters.
func (r IngressRecord) Validate() error {
	if r.SchemaVersion != IngressSchemaVersionV1 {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "unsupported ingress record schema version", nil)
	}
	if err := r.Export.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.ExportID()) == "" || r.ScopeSequence == 0 || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "ingress record identity is invalid", nil)
	}
	if _, err := r.Scope(); err != nil {
		return err
	}
	if len(r.LastError) > 512 || strings.ContainsAny(r.LastError, "\r\n") {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "ingress record diagnostic is invalid", nil)
	}
	switch r.State {
	case IngressStatePending:
		if r.LeaseOwner != "" || r.LeaseUntil != nil || r.PublishedAt != nil {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "pending ingress record has a lease or publish time", nil)
		}
		if r.NextAttemptAt != nil && r.NextAttemptAt.IsZero() {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "pending ingress record has an invalid retry time", nil)
		}
	case IngressStateLeased:
		if strings.TrimSpace(r.LeaseOwner) == "" || r.LeaseUntil == nil || r.LeaseUntil.IsZero() || r.NextAttemptAt != nil || r.PublishedAt != nil {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "leased ingress record is invalid", nil)
		}
	case IngressStatePublished:
		if r.LeaseOwner != "" || r.LeaseUntil != nil || r.NextAttemptAt != nil || r.PublishedAt == nil || r.PublishedAt.IsZero() {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "published ingress record is invalid", nil)
		}
	case IngressStateTerminal:
		if r.LeaseOwner != "" || r.LeaseUntil != nil || r.NextAttemptAt != nil || r.PublishedAt != nil || strings.TrimSpace(r.LastError) == "" {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "terminal ingress record is invalid", nil)
		}
	default:
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, fmt.Sprintf("unsupported ingress record state %q", r.State), nil)
	}
	return nil
}
