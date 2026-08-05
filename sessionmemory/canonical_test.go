package sessionmemory

import (
	"testing"
	"time"
)

func TestCanonicalMutationValidation(t *testing.T) {
	scope := Scope{Key: "canonical:scope", Kind: ScopeKindPersonal}
	mutation := CanonicalMutation{
		SchemaVersion: CanonicalSchemaVersionV1,
		Scope:         scope,
		Operation: OperationRecord{
			OperationID: "operation-1",
			Fingerprint: "derivation-v2-sha256",
			CommittedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC),
		},
		Heads: []ItemHead{{ItemID: "item-1", RevisionID: "revision-1"}},
	}
	if err := mutation.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	mutation.Operation.Fingerprint = " fingerprint"
	if err := mutation.Validate(); err == nil {
		t.Fatal("Validate() accepted a non-canonical operation fingerprint")
	}
}

func TestCanonicalMutationValidationRejectsForeignScope(t *testing.T) {
	mutation := CanonicalMutation{
		SchemaVersion: CanonicalSchemaVersionV1,
		Scope:         Scope{Key: "canonical:scope", Kind: ScopeKindPersonal},
		Operation: OperationRecord{
			OperationID: "operation-1",
			Fingerprint: "derivation-v2-sha256",
			CommittedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC),
		},
		Items: []MemoryItem{{
			ItemID:    "item-1",
			Scope:     Scope{Key: "foreign:scope", Kind: ScopeKindPersonal},
			Kind:      MemoryKindState,
			MemoryKey: "memory-key-1",
		}},
	}
	if err := mutation.Validate(); err == nil {
		t.Fatal("Validate() accepted a foreign item scope")
	}
}

func TestCanonicalMutationValidationRejectsDuplicateHead(t *testing.T) {
	mutation := CanonicalMutation{
		SchemaVersion: CanonicalSchemaVersionV1,
		Scope:         Scope{Key: "canonical:scope", Kind: ScopeKindPersonal},
		Operation: OperationRecord{
			OperationID: "operation-1",
			Fingerprint: "derivation-v2-sha256",
			CommittedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC),
		},
		Heads: []ItemHead{
			{ItemID: "item-1", RevisionID: "revision-1"},
			{ItemID: "item-1", RevisionID: "revision-2"},
		},
	}
	if err := mutation.Validate(); err == nil {
		t.Fatal("Validate() accepted duplicate item heads")
	}
}

func TestCanonicalMutationOutcomeValidation(t *testing.T) {
	outcome := CanonicalMutationOutcome{ScopeVersion: 1, ChangeSeq: 1, RevisionIDs: []string{"revision-1"}}
	if err := outcome.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}
