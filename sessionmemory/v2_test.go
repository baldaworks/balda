package sessionmemory

import (
	"bytes"
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
}
