package catalogapp

import (
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func TestMaintenanceRuntimeLoadsReadModelWithoutPersistence(t *testing.T) {
	stateDir := t.TempDir()
	t.Cleanup(func() { makeWritable(stateDir) })
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime, err := NewMaintenanceRuntime(stateDir, provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !runtime.maintenance || runtime.mcp != nil || runtime.ads != nil {
		t.Fatalf("maintenance projections enabled: %+v", runtime)
	}
	if err := runtime.LoadMaintenanceSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := provider.AppKV().GetJSON(t.Context(), snapshotKeyPrefix+string(snapshot.ID)); err != nil || found {
		t.Fatalf("read snapshot persisted: found=%v err=%v", found, err)
	}
}
