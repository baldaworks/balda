package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestBadgerSessionMemoryStoreResumesV1MigrationFromCheckpoint(t *testing.T) {
	ctx := context.Background()
	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	completedAt := time.Date(2026, time.August, 6, 3, 4, 5, 0, time.UTC)
	turn, err := sessionmemory.NewTerminalTurn(scope, sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"}, "turn-1", completedAt, "remember this", "done", sessionmemory.TurnTerminalStatusSuccess)
	if err != nil {
		t.Fatalf("NewTerminalTurn() error = %v", err)
	}
	sourceRef := sessionmemory.SourceRef{Scope: scope, ExportID: turn.ExportID, SessionID: turn.Session.SessionID, SourceTurnID: turn.SourceTurnID}
	itemID, err := sessionmemory.AtomItemID(scope, sessionmemory.AtomCategoryFact, "remember this")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	provenance := sessionmemory.Provenance{RawSources: []sessionmemory.SourceRef{sourceRef}}
	revisionID, err := sessionmemory.DerivedRevisionID(scope, itemID, "legacy-operation", []string{string(sessionmemory.AtomCategoryFact), "remember this", string(sessionmemory.CandidateRelationNew)}, provenance, nil)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	snapshot := sessionmemory.ScopeSnapshot{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       9,
		Sources:       []sessionmemory.SourceRecord{{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Ref: sourceRef, State: sessionmemory.SourceStateActive, Turn: &turn}},
		Atoms: []sessionmemory.Atom{{
			Meta: sessionmemory.RevisionMeta{
				SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
				Kind:          sessionmemory.DerivedKindAtom,
				ItemID:        itemID,
				RevisionID:    revisionID,
				Revision:      1,
				OperationID:   "legacy-operation",
				Scope:         scope,
				State:         sessionmemory.RevisionStateActive,
				Provenance:    provenance,
				CreatedAt:     completedAt,
			},
			Category: sessionmemory.AtomCategoryFact,
			Text:     "remember this",
			Relation: sessionmemory.CandidateRelationNew,
		}},
	}
	store, err := OpenBadgerSessionMemoryStore(t.TempDir() + "/memory.badger")
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := sessionmemory.MigrateV1ScopeSnapshot(ctx, store, snapshot, sessionmemory.CanonicalMigrationConfig{
		Sealer:             migrationTestSealer{},
		SourceLimit:        1,
		SkipAtomRecords:    true,
		MaxMutationRecords: 128,
	}); err != nil {
		t.Fatalf("MigrateV1ScopeSnapshot(source batch) error = %v", err)
	}
	checkpoint, found, err := store.LoadCanonicalMigrationCheckpoint(ctx, scope, snapshot.Version)
	if err != nil || !found {
		t.Fatalf("LoadCanonicalMigrationCheckpoint(after source) = %+v, found %v, error %v", checkpoint, found, err)
	}
	if checkpoint.Completed || checkpoint.NextSourceOffset != 1 || checkpoint.NextAtomOffset != 0 {
		t.Fatalf("source checkpoint = %+v", checkpoint)
	}
	active, err := store.ScanActiveMemory(ctx, sessionmemory.ActiveMemoryScanRequest{Scope: scope, Limit: 10})
	if err != nil {
		t.Fatalf("ScanActiveMemory(after source batch) error = %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("source batch wrote atom records before their checkpoint: %+v", active)
	}
	if _, err := sessionmemory.MigrateV1ScopeSnapshot(ctx, store, snapshot, sessionmemory.CanonicalMigrationConfig{
		Sealer:             migrationTestSealer{},
		SourceOffset:       1,
		SkipSourceRecords:  true,
		AtomLimit:          1,
		MaxMutationRecords: 128,
	}); err != nil {
		t.Fatalf("MigrateV1ScopeSnapshot(atom batch) error = %v", err)
	}
	checkpoint, found, err = store.LoadCanonicalMigrationCheckpoint(ctx, scope, snapshot.Version)
	if err != nil || !found || !checkpoint.Completed {
		t.Fatalf("LoadCanonicalMigrationCheckpoint(after atom) = %+v, found %v, error %v", checkpoint, found, err)
	}
	active, err = store.ScanActiveMemory(ctx, sessionmemory.ActiveMemoryScanRequest{Scope: scope, Limit: 10})
	if err != nil {
		t.Fatalf("ScanActiveMemory() error = %v", err)
	}
	if len(active) != 1 || active[0].RevisionID == "" {
		t.Fatalf("active migrated memory = %+v", active)
	}
}

type migrationTestSealer struct{}

func (migrationTestSealer) SealCanonicalPayload(_ context.Context, payloadID string, plaintext []byte) (sessionmemory.CanonicalPayload, error) {
	digest := sha256.Sum256(plaintext)
	ref := sessionmemory.PayloadRef{ID: payloadID, KeyID: "migration-test-key", Digest: hex.EncodeToString(digest[:]), ByteSize: uint32(len(plaintext))}
	return sessionmemory.CanonicalPayload{
		Ref: ref,
		Encrypted: sessionmemory.EncryptedPayload{
			KeyID:       ref.KeyID,
			PayloadHash: ref.Digest,
			Nonce:       []byte{1},
			Ciphertext:  []byte{2},
			DEKNonce:    []byte{3},
			WrappedDEK:  []byte{4},
		},
	}, nil
}
