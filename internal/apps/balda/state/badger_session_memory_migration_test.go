package state

import (
	"bytes"
	"context"
	"reflect"
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

func TestBadgerSessionMemoryStorePreservesV1OperationOutcomes(t *testing.T) {
	ctx := context.Background()
	scope := sessionmemory.Scope{Key: "telegram:operation-migration", Kind: sessionmemory.ScopeKindPersonal}
	legacy := sessionmemory.OperationOutcome{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		OperationID:   "legacy-operation-1",
		Stage:         sessionmemory.OperationStageAtoms,
		Scope:         scope,
		ScopeVersion:  17,
		Revisions:     []sessionmemory.RevisionRef{{ItemID: "legacy-item-1", RevisionID: "legacy-revision-1"}},
	}
	legacySecond := sessionmemory.OperationOutcome{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		OperationID:   "legacy-operation-2",
		Stage:         sessionmemory.OperationStageProfile,
		Scope:         scope,
		ScopeVersion:  18,
	}
	directory := t.TempDir() + "/memory.badger"
	store, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	snapshot := sessionmemory.ScopeSnapshot{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       17,
	}
	operations := []sessionmemory.OperationOutcome{legacy, legacySecond}
	if _, err := sessionmemory.MigrateV1ScopeSnapshot(ctx, store, snapshot, sessionmemory.CanonicalMigrationConfig{
		SkipSourceRecords: true,
		SkipAtomRecords:   true,
		OperationLimit:    1,
		LegacyOperations:  operations,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("MigrateV1ScopeSnapshot(operation batch) error = %v", err)
	}
	checkpoint, found, err := store.LoadCanonicalMigrationCheckpoint(ctx, scope, snapshot.Version)
	if err != nil || !found || checkpoint.NextOperationOffset != 1 || checkpoint.Completed {
		t.Fatalf("operation checkpoint after first batch = %+v, found %v, error %v", checkpoint, found, err)
	}
	if _, err := sessionmemory.MigrateV1ScopeSnapshot(ctx, store, snapshot, sessionmemory.CanonicalMigrationConfig{
		SkipSourceRecords: true,
		SkipAtomRecords:   true,
		OperationOffset:   1,
		OperationLimit:    1,
		LegacyOperations:  operations,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("MigrateV1ScopeSnapshot(operation resume) error = %v", err)
	}
	checkpoint, found, err = store.LoadCanonicalMigrationCheckpoint(ctx, scope, snapshot.Version)
	if err != nil || !found || checkpoint.NextOperationOffset != 2 || !checkpoint.Completed {
		t.Fatalf("operation checkpoint after resume = %+v, found %v, error %v", checkpoint, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	imported, found, err := reopened.LoadCanonicalImportedOperation(ctx, scope, legacy.OperationID)
	if err != nil || !found {
		t.Fatalf("LoadCanonicalImportedOperation() = %+v, found %v, error %v", imported, found, err)
	}
	if imported.Outcome.OperationID != legacy.OperationID || imported.Outcome.Stage != legacy.Stage || imported.Outcome.Scope != legacy.Scope || imported.Outcome.ScopeVersion != legacy.ScopeVersion || len(imported.Outcome.Revisions) != 1 || imported.Outcome.Revisions[0] != legacy.Revisions[0] {
		t.Fatalf("imported operation = %+v, want exact legacy outcome", imported)
	}
	importedSecond, found, err := reopened.LoadCanonicalImportedOperation(ctx, scope, legacySecond.OperationID)
	if err != nil || !found || importedSecond.Outcome.OperationID != legacySecond.OperationID || importedSecond.Outcome.Stage != legacySecond.Stage || importedSecond.Outcome.ScopeVersion != legacySecond.ScopeVersion {
		t.Fatalf("second imported operation = %+v, found %v, error %v", importedSecond, found, err)
	}
	var exported bytes.Buffer
	if err := reopened.ExportCanonicalLogical(ctx, &exported); err != nil {
		t.Fatalf("ExportCanonicalLogical() error = %v", err)
	}
	destination, err := OpenBadgerSessionMemoryStore(t.TempDir() + "/destination.badger")
	if err != nil {
		t.Fatalf("destination OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = destination.Close() })
	if err := destination.ImportCanonicalLogical(ctx, bytes.NewReader(exported.Bytes())); err != nil {
		t.Fatalf("ImportCanonicalLogical() error = %v", err)
	}
	imported, found, err = destination.LoadCanonicalImportedOperation(ctx, scope, legacy.OperationID)
	if err != nil || !found || !reflect.DeepEqual(imported.Outcome, legacy) {
		t.Fatalf("imported logical operation = %+v, found %v, error %v; want %+v", imported, found, err, legacy)
	}
	importedSecond, found, err = destination.LoadCanonicalImportedOperation(ctx, scope, legacySecond.OperationID)
	if err != nil || !found || !reflect.DeepEqual(importedSecond.Outcome, legacySecond) {
		t.Fatalf("second imported logical operation = %+v, found %v, error %v; want %+v", importedSecond, found, err, legacySecond)
	}
}

func TestBadgerSessionMemoryStorePersistsMigrationReadiness(t *testing.T) {
	ctx := context.Background()
	scope := sessionmemory.Scope{Key: "telegram:ready", Kind: sessionmemory.ScopeKindPersonal}
	directory := t.TempDir() + "/memory.badger"
	store, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	readiness := sessionmemory.CanonicalMigrationReadiness{SchemaVersion: sessionmemory.CanonicalMigrationReadinessSchemaVersion, Scope: scope, SnapshotVersion: 7, ReadyAt: time.Date(2026, time.August, 6, 15, 0, 0, 0, time.UTC)}
	if err := store.SaveCanonicalMigrationReadiness(ctx, readiness); err != nil {
		t.Fatalf("SaveCanonicalMigrationReadiness() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, found, err := reopened.LoadCanonicalMigrationReadiness(ctx, scope)
	if err != nil || !found || got != readiness {
		t.Fatalf("LoadCanonicalMigrationReadiness() = %#v, found %v, error %v; want %#v, true", got, found, err, readiness)
	}
}
