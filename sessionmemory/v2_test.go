package sessionmemory

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestV2CandidateAndEnvelopeAreDeterministic(t *testing.T) {
	t.Parallel()

	candidate := MemoryCandidate{
		Kind:      MemoryKindState,
		Statement: "Uses Go",
		Temporal:  Temporal{ObservedAt: time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)},
		Evidence: []EvidenceRef{{
			SourceID: "source-1", MessageID: "message-1", Role: MessageRoleUser,
			StartByte: 1, EndByte: 4, AssertionMode: AssertionModeUser,
		}},
		Sensitivity: SensitivityStandard,
		Retention:   RetentionClassStandard,
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("candidate.Validate() error = %v", err)
	}
	first, err := MarshalRecordEnvelope("memory_revision", []byte("payload"))
	if err != nil {
		t.Fatalf("MarshalRecordEnvelope() error = %v", err)
	}
	second, err := MarshalRecordEnvelope("memory_revision", []byte("payload"))
	if err != nil {
		t.Fatalf("MarshalRecordEnvelope() second error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("envelope is not deterministic: %x != %x", first, second)
	}
	envelope, err := UnmarshalRecordEnvelope(first)
	if err != nil {
		t.Fatalf("UnmarshalRecordEnvelope() error = %v", err)
	}
	if envelope.RecordType != "memory_revision" || !bytes.Equal(envelope.Payload, []byte("payload")) {
		t.Fatalf("decoded envelope = %+v", envelope)
	}
	first[len(first)-1] ^= 1
	if _, err := UnmarshalRecordEnvelope(first); err == nil {
		t.Fatal("tampered envelope decoded successfully")
	}
}

func TestTupleKeyUsesUnambiguousLengthPrefixes(t *testing.T) {
	t.Parallel()

	left, err := TupleKey(1, 1, "ab", "c")
	if err != nil {
		t.Fatalf("TupleKey() left error = %v", err)
	}
	right, err := TupleKey(1, 1, "a", "bc")
	if err != nil {
		t.Fatalf("TupleKey() right error = %v", err)
	}
	if bytes.Equal(left, right) {
		t.Fatalf("ambiguous tuple keys: %x", left)
	}
	if got, want := left, []byte{1, 1, 0, 2, 'a', 'b', 0, 1, 'c'}; !bytes.Equal(got, want) {
		t.Fatalf("tuple key = %x, want %x", got, want)
	}
}

func TestV2RecordsRoundTripJSONAndRejectDuplicateEvidence(t *testing.T) {
	t.Parallel()

	revision := MemoryRevision{
		SchemaVersion: MemorySchemaVersionV2,
		RevisionID:    "revision-1",
		ItemID:        "item-1",
		Revision:      1,
		Temporal:      Temporal{ObservedAt: time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)},
		Evidence: []EvidenceRef{{
			SourceID: "source-1", MessageID: "message-1", Role: MessageRoleUser,
			StartByte: 1, EndByte: 4, AssertionMode: AssertionModeUser,
		}},
		Sensitivity: SensitivityStandard,
		Retention:   RetentionClassStandard,
		Payload:     PayloadRef{ID: "payload-1", Digest: "digest-1", ByteSize: 7},
	}
	encoded, err := json.Marshal(revision)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded MemoryRevision
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded.Validate() error = %v", err)
	}
	decoded.Evidence = append(decoded.Evidence, decoded.Evidence[0])
	if err := decoded.Validate(); err == nil {
		t.Fatal("duplicate evidence validated")
	}
	decoded.Evidence = decoded.Evidence[:1]
	decoded.Parents = []string{decoded.RevisionID}
	if err := decoded.Validate(); err == nil {
		t.Fatal("self-parenting revision validated")
	}
	decoded.Parents = []string{"parent-1", "parent-1"}
	if err := decoded.Validate(); err == nil {
		t.Fatal("duplicate revision parents validated")
	}
}

func TestV2TrustedToolEvidenceRequiresToolRole(t *testing.T) {
	t.Parallel()

	evidence := EvidenceRef{
		SourceID: "source-1", MessageID: "message-1", Role: MessageRoleTool,
		StartByte: 1, EndByte: 4, AssertionMode: AssertionModeTrustedTool,
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("tool evidence.Validate() error = %v", err)
	}
	evidence.Role = MessageRoleAssistant
	if err := evidence.Validate(); err == nil {
		t.Fatal("assistant evidence was accepted as a trusted tool")
	}
}

func TestNewEvidenceRefUsesUTF8ByteOffsetsAndMessageDigest(t *testing.T) {
	text := "Привет world"
	evidence, err := NewEvidenceRef("source-1", "message-1", MessageRoleUser, text, 0, 12, AssertionModeUser)
	if err != nil {
		t.Fatalf("NewEvidenceRef() error = %v", err)
	}
	if evidence.TextDigest == "" || evidence.StartByte != 0 || evidence.EndByte != 12 {
		t.Fatalf("evidence = %+v", evidence)
	}
	if _, err := NewEvidenceRef("source-1", "message-1", MessageRoleUser, text, 1, 12, AssertionModeUser); err == nil {
		t.Fatal("NewEvidenceRef() accepted a byte offset inside a UTF-8 rune")
	}
	if _, err := NewEvidenceRef("source-1", "message-1", MessageRoleUser, text, 0, 99, AssertionModeUser); err == nil {
		t.Fatal("NewEvidenceRef() accepted an out-of-range byte span")
	}
}

func FuzzTupleKey(f *testing.F) {
	f.Add("scope-1", "item-1")
	f.Add("", "item-1")
	f.Fuzz(func(t *testing.T, first, second string) {
		key, err := TupleKey(1, 1, first, second)
		if err != nil {
			return
		}
		if len(key) < 6 || key[0] != 1 || key[1] != 1 {
			t.Fatalf("tuple key = %x", key)
		}
	})
}
