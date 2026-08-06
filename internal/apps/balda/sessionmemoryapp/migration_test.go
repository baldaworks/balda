package sessionmemoryapp

import (
	"context"
	"reflect"
	"testing"
	"time"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/sessionmemory"
)

type migrationFixtureStore struct {
	snapshot   sessionmemory.ScopeSnapshot
	operations []sessionmemory.OperationOutcome
}

func (s *migrationFixtureStore) LookupOperation(context.Context, sessionmemory.OperationLookup) (sessionmemory.OperationLookupResult, error) {
	return sessionmemory.OperationLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration fixture does not process operations", nil)
}

func (s *migrationFixtureStore) LookupForget(context.Context, sessionmemory.ForgetLookup) (sessionmemory.ForgetLookupResult, error) {
	return sessionmemory.ForgetLookupResult{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration fixture does not process forgets", nil)
}

func (s *migrationFixtureStore) LoadScope(context.Context, sessionmemory.Scope) (sessionmemory.ScopeSnapshot, error) {
	return s.snapshot, nil
}

func (s *migrationFixtureStore) Commit(context.Context, sessionmemory.CommitRequest) (sessionmemory.OperationOutcome, error) {
	return sessionmemory.OperationOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration fixture does not commit", nil)
}

func (s *migrationFixtureStore) ForgetSource(context.Context, sessionmemory.ForgetSourceRequest) (sessionmemory.ForgetOutcome, error) {
	return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration fixture does not forget", nil)
}

func (s *migrationFixtureStore) ForgetScope(context.Context, sessionmemory.ForgetScopeRequest) (sessionmemory.ForgetOutcome, error) {
	return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration fixture does not forget", nil)
}

func (s *migrationFixtureStore) Search(context.Context, sessionmemory.DerivedSearchRequest) ([]sessionmemory.SearchHit, error) {
	return nil, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration fixture does not search", nil)
}

func (s *migrationFixtureStore) Trace(context.Context, sessionmemory.TraceRequest) (sessionmemory.TraceGraph, error) {
	return sessionmemory.TraceGraph{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "migration fixture does not trace", nil)
}

func (s *migrationFixtureStore) LoadOperationOutcomes(context.Context, sessionmemory.Scope) ([]sessionmemory.OperationOutcome, error) {
	return append([]sessionmemory.OperationOutcome(nil), s.operations...), nil
}

func TestMigrationCoordinatorResumesAndPersistsReadiness(t *testing.T) {
	ctx := context.Background()
	scope := sessionmemory.Scope{Key: "migration:coordinator", Kind: sessionmemory.ScopeKindPersonal}
	completedAt := time.Date(2026, time.August, 6, 5, 6, 7, 0, time.UTC)
	activeTurn, err := sessionmemory.NewTerminalTurn(scope, sessionmemory.SessionRef{SessionID: "migration-session", AgentSessionID: "migration-agent"}, "migration-turn", completedAt, "migrated source", "ack", sessionmemory.TurnTerminalStatusSuccess)
	if err != nil {
		t.Fatalf("NewTerminalTurn() error = %v", err)
	}
	activeRef := sessionmemory.SourceRef{Scope: scope, ExportID: activeTurn.ExportID, SessionID: activeTurn.Session.SessionID, SourceTurnID: activeTurn.SourceTurnID}
	forgottenRef := sessionmemory.SourceRef{Scope: scope, ExportID: "forgotten-export", SessionID: "forgotten-session", SourceTurnID: "forgotten-turn"}
	forgottenAt := completedAt.Add(time.Minute)
	legacyOperation := sessionmemory.OperationOutcome{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, OperationID: "migrated-operation", Stage: sessionmemory.OperationStageAtoms, Scope: scope, ScopeVersion: 1}
	legacy := &migrationFixtureStore{
		snapshot: sessionmemory.ScopeSnapshot{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
			Scope:         scope,
			Version:       1,
			Sources: []sessionmemory.SourceRecord{
				{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Ref: activeRef, State: sessionmemory.SourceStateActive, Turn: &activeTurn},
				{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Ref: forgottenRef, State: sessionmemory.SourceStateForgotten, ForgottenAt: &forgottenAt},
			},
		},
		operations: []sessionmemory.OperationOutcome{legacyOperation},
	}
	directory := t.TempDir() + "/canonical"
	canonical, err := baldastate.OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	coordinator, err := NewMigrationCoordinator(legacy, canonical, canonical)
	if err != nil {
		_ = canonical.Close()
		t.Fatalf("NewMigrationCoordinator() error = %v", err)
	}
	if ready, readyErr := coordinator.IsReady(ctx, scope); readyErr != nil || ready {
		t.Fatalf("IsReady(before migration) = %v, error %v", ready, readyErr)
	}
	if err := coordinator.MigrateScope(ctx, scope); err != nil {
		_ = canonical.Close()
		t.Fatalf("MigrateScope() error = %v", err)
	}
	readiness, found, err := canonical.LoadCanonicalMigrationReadiness(ctx, scope)
	if err != nil || !found || readiness.SnapshotVersion != legacy.snapshot.Version {
		t.Fatalf("migration readiness = %#v, found %v, error %v", readiness, found, err)
	}
	refs, next, err := canonical.ListCanonicalSourceRefs(ctx, scope, "", 10)
	if err != nil || next != "" || len(refs) != 1 || refs[0] != activeRef {
		t.Fatalf("canonical source refs = %#v, next %q, error %v; want only active source", refs, next, err)
	}
	imported, found, err := canonical.LoadCanonicalImportedOperation(ctx, scope, legacyOperation.OperationID)
	if err != nil || !found || !reflect.DeepEqual(imported.Outcome, legacyOperation) {
		t.Fatalf("migrated operation = %#v, found %v, error %v; want %#v", imported, found, err, legacyOperation)
	}
	if err := canonical.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := baldastate.OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen canonical store error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedCoordinator, err := NewMigrationCoordinator(legacy, reopened, reopened)
	if err != nil {
		t.Fatalf("NewMigrationCoordinator(reopened) error = %v", err)
	}
	if ready, readyErr := reopenedCoordinator.IsReady(ctx, scope); readyErr != nil || !ready {
		t.Fatalf("IsReady(after reopen) = %v, error %v", ready, readyErr)
	}
	if err := reopenedCoordinator.MigrateScope(ctx, scope); err != nil {
		t.Fatalf("MigrateScope(replay) error = %v", err)
	}
}
