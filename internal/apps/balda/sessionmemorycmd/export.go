package sessionmemorycmd

import (
	"encoding/json"
	"fmt"

	"github.com/normahq/balda/sessionmemory"
)

const (
	// SubjectTurn carries completed-turn exports on the dedicated memory stream.
	SubjectTurn = "balda.v1.session_memory.turn"
	// SubjectBoundary carries lifecycle exports on the dedicated memory stream.
	SubjectBoundary = "balda.v1.session_memory.boundary"
	// SubjectAll selects every session-memory export subject.
	SubjectAll = "balda.v1.session_memory.>"
)

// Kind identifies one portable export envelope variant.
type Kind string

const (
	// KindTurn identifies a completed-turn export.
	KindTurn Kind = "turn.v1"
	// KindBoundary identifies a session-boundary export.
	KindBoundary Kind = "boundary.v1"
)

// Export is the versioned wire envelope persisted by a durable transport.
type Export struct {
	SchemaVersion string                  `json:"schema_version"`
	Kind          Kind                    `json:"kind"`
	Turn          *sessionmemory.Turn     `json:"turn,omitempty"`
	Boundary      *sessionmemory.Boundary `json:"boundary,omitempty"`
}

// NewTurn creates a validated completed-turn export envelope.
func NewTurn(turn sessionmemory.Turn) (Export, error) {
	if err := turn.Validate(); err != nil {
		return Export{}, err
	}
	export := Export{SchemaVersion: sessionmemory.SchemaVersionV1, Kind: KindTurn, Turn: &turn}
	return export, nil
}

// NewBoundary creates a validated lifecycle export envelope.
func NewBoundary(boundary sessionmemory.Boundary) (Export, error) {
	if err := boundary.Validate(); err != nil {
		return Export{}, err
	}
	export := Export{SchemaVersion: sessionmemory.SchemaVersionV1, Kind: KindBoundary, Boundary: &boundary}
	return export, nil
}

// Validate verifies envelope variant and payload consistency.
func (e Export) Validate() error {
	if e.SchemaVersion != sessionmemory.SchemaVersionV1 {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "unsupported export envelope schema version", nil)
	}
	switch e.Kind {
	case KindTurn:
		if e.Turn == nil || e.Boundary != nil {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "turn envelope must contain only a turn", nil)
		}
		return e.Turn.Validate()
	case KindBoundary:
		if e.Boundary == nil || e.Turn != nil {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "boundary envelope must contain only a boundary", nil)
		}
		return e.Boundary.Validate()
	default:
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "unsupported export envelope kind", nil)
	}
}

// ExportID returns the idempotency key carried by the active variant.
func (e Export) ExportID() string {
	switch {
	case e.Kind == KindTurn && e.Turn != nil:
		return e.Turn.ExportID
	case e.Kind == KindBoundary && e.Boundary != nil:
		return e.Boundary.ExportID
	default:
		return ""
	}
}

// Subject returns the dedicated stream subject for the active variant.
func (e Export) Subject() string {
	switch e.Kind {
	case KindTurn:
		return SubjectTurn
	case KindBoundary:
		return SubjectBoundary
	default:
		return ""
	}
}

// Marshal validates and serializes an export envelope.
func Marshal(export Export) ([]byte, error) {
	if err := export.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(export)
	if err != nil {
		return nil, fmt.Errorf("marshal session-memory export: %w", err)
	}
	return data, nil
}

// Unmarshal decodes and validates an export envelope.
func Unmarshal(data []byte) (Export, error) {
	var export Export
	if err := json.Unmarshal(data, &export); err != nil {
		return Export{}, sessionmemory.PermanentError(sessionmemory.CodePermanent, "decode session-memory export", err)
	}
	if err := export.Validate(); err != nil {
		return Export{}, err
	}
	return export, nil
}
