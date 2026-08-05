package sessionmemory

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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

// MemoryKey is an engine-derived stable semantic identity for state memory.
type MemoryKey string

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

// RetentionClass controls lifecycle handling for stored payloads.
type RetentionClass string

const (
	RetentionClassStandard  RetentionClass = "standard"
	RetentionClassEphemeral RetentionClass = "ephemeral"
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

// SourceRecordV2 identifies one durable source independently of its payload.
type SourceRecordV2 struct {
	SourceID    string         `json:"source_id"`
	Scope       Scope          `json:"scope"`
	Sensitivity Sensitivity    `json:"sensitivity"`
	Retention   RetentionClass `json:"retention"`
	Payload     PayloadRef     `json:"payload"`
}

// MessageRecord identifies one role-bearing UTF-8 message in a source.
type MessageRecord struct {
	MessageID string      `json:"message_id"`
	SourceID  string      `json:"source_id"`
	Role      MessageRole `json:"role"`
	Payload   PayloadRef  `json:"payload"`
}

// LifecycleEventType records immutable item lifecycle transitions.
type LifecycleEventType string

const (
	LifecycleEventActivate   LifecycleEventType = "activate"
	LifecycleEventSupersede  LifecycleEventType = "supersede"
	LifecycleEventInvalidate LifecycleEventType = "invalidate"
	LifecycleEventForget     LifecycleEventType = "forget"
)

// LifecycleEvent records an engine-owned lifecycle change for one revision.
type LifecycleEvent struct {
	EventID    string             `json:"event_id"`
	RevisionID string             `json:"revision_id"`
	Type       LifecycleEventType `json:"type"`
	OccurredAt time.Time          `json:"occurred_at"`
}

// MemoryCandidate is untrusted derivation output. It deliberately has no
// persistent item, revision, scope, or lifecycle identifiers.
type MemoryCandidate struct {
	Kind        MemoryKind     `json:"kind"`
	Statement   string         `json:"statement"`
	Temporal    Temporal       `json:"temporal"`
	Evidence    []EvidenceRef  `json:"evidence"`
	Sensitivity Sensitivity    `json:"sensitivity"`
	Retention   RetentionClass `json:"retention"`
}

// RecordEnvelope is the deterministic protobuf-wire envelope for a portable
// v2 record. Payload encoding remains owned by the corresponding contract.
type RecordEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	RecordType    string `json:"record_type"`
	PayloadSHA256 string `json:"payload_sha256"`
	Payload       []byte `json:"payload"`
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
	MemoryKey MemoryKey  `json:"memory_key,omitempty"`
}

// MemoryRevision is immutable canonical metadata for one memory change.
type MemoryRevision struct {
	SchemaVersion string         `json:"schema_version"`
	RevisionID    string         `json:"revision_id"`
	ItemID        string         `json:"item_id"`
	Revision      uint64         `json:"revision"`
	Parents       []string       `json:"parents,omitempty"`
	Temporal      Temporal       `json:"temporal"`
	Evidence      []EvidenceRef  `json:"evidence"`
	Sensitivity   Sensitivity    `json:"sensitivity"`
	Retention     RetentionClass `json:"retention"`
	Payload       PayloadRef     `json:"payload"`
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
		if !isCanonicalID(string(i.MemoryKey)) {
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
	if e.AssertionMode == AssertionModeUser && e.Role != MessageRoleUser {
		return invalidDerived("user evidence must have user role")
	}
	if e.AssertionMode == AssertionModeTrustedTool && e.Role != MessageRoleTool {
		return invalidDerived("trusted tool evidence role is invalid")
	}
	if e.AssertionMode != AssertionModeUser && e.AssertionMode != AssertionModeTrustedTool {
		return invalidDerived("unsupported evidence assertion mode")
	}
	return nil
}

func (s SourceRecordV2) Validate() error {
	if !isCanonicalID(s.SourceID) {
		return invalidDerived("source id is required")
	}
	if err := s.Scope.Validate(); err != nil {
		return err
	}
	if err := validateSensitivityRetention(s.Sensitivity, s.Retention); err != nil {
		return err
	}
	return s.Payload.Validate()
}

func (m MessageRecord) Validate() error {
	if !isCanonicalID(m.MessageID) || !isCanonicalID(m.SourceID) {
		return invalidDerived("message identity is invalid")
	}
	if m.Role != MessageRoleUser && m.Role != MessageRoleAssistant && m.Role != MessageRoleTool {
		return invalidDerived("message role is invalid")
	}
	return m.Payload.Validate()
}

func (l LifecycleEvent) Validate() error {
	if !isCanonicalID(l.EventID) || !isCanonicalID(l.RevisionID) || l.OccurredAt.IsZero() {
		return invalidDerived("lifecycle event identity is invalid")
	}
	switch l.Type {
	case LifecycleEventActivate, LifecycleEventSupersede, LifecycleEventInvalidate, LifecycleEventForget:
		return nil
	default:
		return invalidDerived("unsupported lifecycle event")
	}
}

func (c MemoryCandidate) Validate() error {
	if c.Kind != MemoryKindState && c.Kind != MemoryKindEvent {
		return invalidDerived("unsupported memory candidate kind")
	}
	if strings.TrimSpace(c.Statement) != c.Statement || c.Statement == "" || len(c.Statement) > maxMemoryTextBytes {
		return invalidDerived("memory candidate statement is invalid")
	}
	if err := c.Temporal.Validate(); err != nil {
		return err
	}
	if len(c.Evidence) == 0 || len(c.Evidence) > MaxSourcesPerRevision {
		return invalidDerived("memory candidate evidence is invalid")
	}
	if err := validateEvidenceRefs(c.Evidence); err != nil {
		return err
	}
	return validateSensitivityRetention(c.Sensitivity, c.Retention)
}

func (p PayloadRef) Validate() error {
	if !isCanonicalID(p.KeyID) || !isCanonicalID(p.Digest) || p.ByteSize == 0 {
		return invalidDerived("memory payload reference is invalid")
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
	if err := validateEvidenceRefs(r.Evidence); err != nil {
		return err
	}
	if err := validateUniqueMemoryIDs(r.Parents, "memory revision parent"); err != nil {
		return err
	}
	for _, parent := range r.Parents {
		if parent == r.RevisionID {
			return invalidDerived("memory revision cannot parent itself")
		}
	}
	if err := validateSensitivityRetention(r.Sensitivity, r.Retention); err != nil {
		return err
	}
	return r.Payload.Validate()
}

func validateUniqueMemoryIDs(ids []string, name string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !isCanonicalID(id) {
			return invalidDerived(name + " is invalid")
		}
		if _, exists := seen[id]; exists {
			return invalidDerived(name + " is duplicated")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateSensitivityRetention(sensitivity Sensitivity, retention RetentionClass) error {
	if sensitivity != SensitivityStandard && sensitivity != SensitivitySensitive {
		return invalidDerived("unsupported memory sensitivity")
	}
	if retention != RetentionClassStandard && retention != RetentionClassEphemeral {
		return invalidDerived("unsupported memory retention")
	}
	return nil
}

func validateEvidenceRefs(evidence []EvidenceRef) error {
	seen := make(map[EvidenceRef]struct{}, len(evidence))
	for _, ref := range evidence {
		if err := ref.Validate(); err != nil {
			return err
		}
		if _, exists := seen[ref]; exists {
			return invalidDerived("memory evidence contains a duplicate reference")
		}
		seen[ref] = struct{}{}
	}
	return nil
}

// MarshalRecordEnvelope returns a deterministic protobuf-wire envelope.
func MarshalRecordEnvelope(recordType string, payload []byte) ([]byte, error) {
	if !isCanonicalID(recordType) || len(payload) == 0 {
		return nil, invalidDerived("record envelope is invalid")
	}
	digest := sha256.Sum256(payload)
	envelope := RecordEnvelope{
		SchemaVersion: MemorySchemaVersionV2,
		RecordType:    recordType,
		PayloadSHA256: hex.EncodeToString(digest[:]),
		Payload:       payload,
	}
	return marshalEnvelope(envelope), nil
}

// UnmarshalRecordEnvelope validates and decodes the portable protobuf-wire envelope.
func UnmarshalRecordEnvelope(encoded []byte) (RecordEnvelope, error) {
	fields := make(map[uint64][]byte, 4)
	for offset := 0; offset < len(encoded); {
		tag, next, ok := readProtoVarint(encoded, offset)
		if !ok || tag>>3 < 1 || tag>>3 > 4 || tag&7 != 2 {
			return RecordEnvelope{}, invalidDerived("record envelope is malformed")
		}
		length, valueOffset, ok := readProtoVarint(encoded, next)
		if !ok || length > uint64(len(encoded)-valueOffset) {
			return RecordEnvelope{}, invalidDerived("record envelope payload is malformed")
		}
		field := tag >> 3
		if _, exists := fields[field]; exists {
			return RecordEnvelope{}, invalidDerived("record envelope contains duplicate field")
		}
		end := valueOffset + int(length)
		fields[field] = append([]byte(nil), encoded[valueOffset:end]...)
		offset = end
	}
	envelope := RecordEnvelope{
		SchemaVersion: string(fields[1]),
		RecordType:    string(fields[2]),
		PayloadSHA256: string(fields[3]),
		Payload:       fields[4],
	}
	if envelope.SchemaVersion != MemorySchemaVersionV2 || !isCanonicalID(envelope.RecordType) || len(envelope.Payload) == 0 {
		return RecordEnvelope{}, invalidDerived("record envelope is invalid")
	}
	want := sha256.Sum256(envelope.Payload)
	if envelope.PayloadSHA256 != hex.EncodeToString(want[:]) {
		return RecordEnvelope{}, invalidDerived("record envelope checksum does not match payload")
	}
	return envelope, nil
}

func marshalEnvelope(envelope RecordEnvelope) []byte {
	encoded := make([]byte, 0, len(envelope.Payload)+128)
	encoded = appendProtoBytes(encoded, 1, []byte(envelope.SchemaVersion))
	encoded = appendProtoBytes(encoded, 2, []byte(envelope.RecordType))
	encoded = appendProtoBytes(encoded, 3, []byte(envelope.PayloadSHA256))
	encoded = appendProtoBytes(encoded, 4, envelope.Payload)
	return encoded
}

func appendProtoBytes(dst []byte, field byte, value []byte) []byte {
	dst = append(dst, field<<3|2)
	for length := uint64(len(value)); ; length >>= 7 {
		part := byte(length & 0x7f)
		if length < 0x80 {
			dst = append(dst, part)
			break
		}
		dst = append(dst, part|0x80)
	}
	return append(dst, value...)
}

func readProtoVarint(encoded []byte, offset int) (uint64, int, bool) {
	var value uint64
	for shift := uint(0); offset < len(encoded) && shift < 64; shift += 7 {
		part := encoded[offset]
		offset++
		value |= uint64(part&0x7f) << shift
		if part < 0x80 {
			return value, offset, true
		}
	}
	return 0, offset, false
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
