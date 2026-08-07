package balda

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/sessionmemory"
	badgerstore "github.com/normahq/balda/sessionmemory/store/badger"
)

func TestCanonicalSessionMemoryRuntimeMigratesAndReopensGroupedStore(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	legacyCanonicalPath := filepath.Join(stateDir, "session-memory.badger")
	legacyProjectionPath := filepath.Join(stateDir, "session-memory-bleve")

	legacyStore, err := badgerstore.OpenBadgerSessionMemoryStore(legacyCanonicalPath)
	if err != nil {
		t.Fatalf("open legacy canonical store: %v", err)
	}
	scope, operationID := seedCanonicalOperation(t, legacyStore)
	if err := legacyStore.Close(); err != nil {
		t.Fatalf("close legacy canonical store: %v", err)
	}
	if err := os.MkdirAll(legacyProjectionPath, 0o700); err != nil {
		t.Fatalf("create legacy projection root: %v", err)
	}

	runtime, err := newCanonicalSessionMemoryRuntime(SessionMemoryConfig{Enabled: true}, &baldaagent.Builder{}, "provider", t.TempDir(), stateDir)
	if err != nil {
		t.Fatalf("newCanonicalSessionMemoryRuntime() migration error = %v", err)
	}
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("portable runtime Start() error = %v", err)
	}
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("portable runtime Close() error = %v", err)
	}

	paths := baldastate.SessionMemoryPaths(stateDir)
	assertSessionMemoryPathMissing(t, legacyCanonicalPath)
	assertSessionMemoryPathMissing(t, legacyProjectionPath)
	if _, err := os.Stat(paths.Canonical); err != nil {
		t.Fatalf("grouped canonical path: %v", err)
	}
	if _, err := os.Stat(paths.Projection); err != nil {
		t.Fatalf("grouped projection path: %v", err)
	}

	groupedStore, err := badgerstore.OpenBadgerSessionMemoryStore(paths.Canonical)
	if err != nil {
		t.Fatalf("reopen grouped canonical store: %v", err)
	}
	operation, found, err := groupedStore.LoadCanonicalOperation(ctx, scope, operationID)
	if err != nil {
		_ = groupedStore.Close()
		t.Fatalf("LoadCanonicalOperation() error = %v", err)
	}
	if !found || operation.Fingerprint != operationID+"-fingerprint" {
		_ = groupedStore.Close()
		t.Fatalf("reopened canonical operation = %#v, found = %t", operation, found)
	}
	if err := groupedStore.Close(); err != nil {
		t.Fatalf("close grouped canonical store: %v", err)
	}

	secondRuntime, err := newCanonicalSessionMemoryRuntime(SessionMemoryConfig{Enabled: true}, &baldaagent.Builder{}, "provider", t.TempDir(), stateDir)
	if err != nil {
		t.Fatalf("second newCanonicalSessionMemoryRuntime() error = %v", err)
	}
	if err := secondRuntime.Start(ctx); err != nil {
		t.Fatalf("second portable runtime Start() error = %v", err)
	}
	if err := secondRuntime.Close(ctx); err != nil {
		t.Fatalf("second portable runtime Close() error = %v", err)
	}
}

func TestCanonicalSessionMemoryRuntimeDisabledDoesNotCreateStorage(t *testing.T) {
	stateDir := t.TempDir()
	runtime, err := newCanonicalSessionMemoryRuntime(SessionMemoryConfig{}, &baldaagent.Builder{}, "provider", t.TempDir(), stateDir)
	if err != nil {
		t.Fatalf("newCanonicalSessionMemoryRuntime() disabled error = %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("disabled portable runtime Close() error = %v", err)
	}
	assertSessionMemoryPathMissing(t, baldastate.SessionMemoryRootPath(stateDir))
}

func seedCanonicalOperation(t *testing.T, store *badgerstore.BadgerSessionMemoryStore) (sessionmemory.Scope, string) {
	t.Helper()
	scope := sessionmemory.Scope{Key: "migration:scope", Kind: sessionmemory.ScopeKindPersonal}
	operationID := "migration-operation"
	mutation := sessionmemory.CanonicalMutation{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1,
		Scope:         scope,
		Operation: sessionmemory.OperationRecord{
			OperationID: operationID,
			Fingerprint: operationID + "-fingerprint",
			CommittedAt: time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC),
		},
		Items: []sessionmemory.MemoryItem{{
			ItemID: "migration-item",
			Scope:  scope,
			Kind:   sessionmemory.MemoryKindEvent,
		}},
		Revisions: []sessionmemory.MemoryRevision{{
			SchemaVersion: sessionmemory.MemorySchemaVersionV2,
			RevisionID:    "migration-revision",
			ItemID:        "migration-item",
			Revision:      1,
			Temporal:      sessionmemory.Temporal{ObservedAt: time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)},
			Evidence: []sessionmemory.EvidenceRef{{
				SourceID:      "migration-source",
				MessageID:     "migration-message",
				Role:          sessionmemory.MessageRoleUser,
				StartByte:     0,
				EndByte:       8,
				AssertionMode: sessionmemory.AssertionModeUser,
			}},
			Sensitivity: sessionmemory.SensitivityStandard,
			Retention:   sessionmemory.RetentionClassStandard,
			Payload:     sessionmemory.PayloadRef{ID: "migration-payload", Digest: "migration-digest", ByteSize: 1},
		}},
		Heads: []sessionmemory.ItemHead{{ItemID: "migration-item", RevisionID: "migration-revision"}},
	}
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err != nil {
		t.Fatalf("seed ApplyCanonicalMutation() error = %v", err)
	}
	return scope, operationID
}

func assertSessionMemoryPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %q error = %v, want not exist", path, err)
	}
}
