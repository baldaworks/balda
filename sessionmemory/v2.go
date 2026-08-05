package sessionmemory

import (
	"encoding/binary"
	"strings"
	"time"
)

const (
	// MemorySchemaVersionV2 identifies the portable immutable-memory contract.
	MemorySchemaVersionV2 = "session-memory/v2"
	maxMemoryTextBytes    = 32 * 1024
)

// MemoryKind distinguishes stable state from a distinct episodic event.
type MemoryKind string

const (
	MemoryKindState MemoryKind = "state"
	MemoryKindEvent MemoryKind = "event"
)

// AssertionMode identifies the evidence authority used for a memory revision.
type AssertionMode string

const (
	AssertionModeUser        AssertionMode = "user"
	AssertionModeTrustedTool AssertionMode = "trusted_tool"
)

// Sensitivity controls whether data is eligible for durable retention.
type Sensitivity string

const (
	SensitivityStandard  Sensitivity = "standard"
	SensitivitySensitive Sensitivity = "sensitive"
)

// Temporal records the interpreted time semantics without discarding source text.
type Temporal struct {
	ObservedAt        time.Time  `json:"observed_at"`
	ValidFrom         *time.Time `json:"valid_from,omitempty"`
	ValidUntil        *time.Time `json:"valid_until,omitempty"`
	EventAt           *time.Time `json:"event_at,omitempty"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	TemporalText      string     `json:"temporal_text,omitempty"`
	TemporalPrecision string     `json:"temporal_precision,omitempty"`
	Timezone          string     `json:"timezone,omitempty"`
}

// EvidenceRef pinpoints a UTF-8 byte span in an immutable message.
type EvidenceRef struct {
	SourceID      string        `json:"source_id"`
	MessageID     string        `json:"message_id"`
	Role          MessageRole   `json:"role"`
	StartByte     uint32        `json:"start_byte"`
	EndByte       uint32        `json:"end_byte"`
	AssertionMode AssertionMode `json:"assertion_mode"`
}

// PayloadRef separates structural records from encrypted content storage.
type PayloadRef struct {
	KeyID    string `json:"key_id"`
	Digest   string `json:"digest"`
	ByteSize uint32 `json:"byte_size"`
}

// MemoryItem is an engine-owned logical identity. Callers never supply its ID.
type MemoryItem struct {
	ItemID    string     `json:"item_id"`
	Scope     Scope      `json:"scope"`
	Kind      MemoryKind `json:"kind"`
	MemoryKey string     `json:"memory_key,omitempty"`
}

// MemoryRevision is immutable canonical metadata for one memory change.
type MemoryRevision struct {
	SchemaVersion string        `json:"schema_version"`
	RevisionID    string        `json:"revision_id"`
	ItemID        string        `json:"item_id"`
	Revision      uint64        `json:"revision"`
	Temporal      Temporal      `json:"temporal"`
	Evidence      []EvidenceRef `json:"evidence"`
	Sensitivity   Sensitivity   `json:"sensitivity"`
	Payload       PayloadRef    `json:"payload"`
}

// Validate verifies a portable v2 item does not accept model-owned identity.
func (i MemoryItem) Validate() error {
	if !isCanonicalID(i.ItemID) {
		return invalidDerived("memory item id is required")
	}
	if err := i.Scope.Validate(); err != nil {
		return err
	}
	switch i.Kind {
	case MemoryKindState:
		if !isCanonicalID(i.MemoryKey) {
			return invalidDerived("state memory key is required")
		}
	case MemoryKindEvent:
		if i.MemoryKey != "" {
			return invalidDerived("event memory cannot have a stable memory key")
		}
	default:
		return invalidDerived("unsupported memory kind")
	}
	return nil
}

// Validate verifies temporal ranges and their source representation.
func (t Temporal) Validate() error {
	if t.ObservedAt.IsZero() {
		return invalidDerived("observed time is required")
	}
	if t.ValidFrom != nil && t.ValidUntil != nil && t.ValidUntil.Before(*t.ValidFrom) {
		return invalidDerived("valid interval is inverted")
	}
	if len(t.TemporalText) > maxMemoryTextBytes || strings.TrimSpace(t.TemporalText) != t.TemporalText {
		return invalidDerived("temporal text is invalid")
	}
	return nil
}

// Validate verifies one evidence span and its source authority.
func (e EvidenceRef) Validate() error {
	if !isCanonicalID(e.SourceID) || !isCanonicalID(e.MessageID) || e.EndByte <= e.StartByte {
		return invalidDerived("evidence reference is invalid")
	}
	if e.Role != MessageRoleUser {
		return invalidDerived("personal memory evidence must be user or trusted tool")
	}
	if e.AssertionMode != AssertionModeUser && e.AssertionMode != AssertionModeTrustedTool {
		return invalidDerived("unsupported evidence assertion mode")
	}
	return nil
}

// Validate verifies immutable v2 revision metadata.
func (r MemoryRevision) Validate() error {
	if r.SchemaVersion != MemorySchemaVersionV2 || !isCanonicalID(r.RevisionID) || !isCanonicalID(r.ItemID) || r.Revision == 0 {
		return invalidDerived("memory revision identity is invalid")
	}
	if err := r.Temporal.Validate(); err != nil {
		return err
	}
	if len(r.Evidence) == 0 || len(r.Evidence) > MaxSourcesPerRevision {
		return invalidDerived("memory revision evidence is invalid")
	}
	for _, evidence := range r.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	if r.Sensitivity != SensitivityStandard && r.Sensitivity != SensitivitySensitive {
		return invalidDerived("unsupported memory sensitivity")
	}
	if !isCanonicalID(r.Payload.KeyID) || !isCanonicalID(r.Payload.Digest) || r.Payload.ByteSize == 0 {
		return invalidDerived("memory payload reference is invalid")
	}
	return nil
}

// TupleKey encodes application-owned components without backend-native keys.
func TupleKey(namespace byte, version byte, components ...string) ([]byte, error) {
	key := []byte{namespace, version}
	for _, component := range components {
		if !isCanonicalID(component) {
			return nil, invalidDerived("tuple key component is invalid")
		}
		if len(component) > 65535 {
			return nil, limitExceeded("tuple key component is too large")
		}
		var length [2]byte
		binary.BigEndian.PutUint16(length[:], uint16(len(component)))
		key = append(key, length[:]...)
		key = append(key, component...)
	}
	return key, nil
}
