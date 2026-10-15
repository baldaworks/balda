package balda

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/memory"
	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
)

func TestMemorySnapshotReaderPreservesCurrentBoundary(t *testing.T) {
	stateDir := t.TempDir()
	database, err := (Config{}).Balda.Database.Resolve(stateDir, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := openBaldaStateProvider(t.Context(), stateDir, database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	store := memory.NewStore(provider.AppKV(), "", true)
	reader := memorySnapshotReaderAdapter{store: store}
	for _, record := range []memorycmd.Record{
		{Version: 1, UpdatedAt: "2026-10-11T06:00:00Z", Entries: []memorycmd.Entry{{Version: 1, CreatedAt: "2026-10-11T06:00:00Z", Fact: "synthetic current fact"}}},
		{Version: 2, UpdatedAt: "2026-10-11T06:00:01Z", Entries: []memorycmd.Entry{}},
	} {
		if err := provider.AppKV().SetJSON(t.Context(), memorycmd.Key, record); err != nil {
			t.Fatal(err)
		}
		want, err := store.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		got, err := reader.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got.Content != want.Content || got.Version != want.Version || got.UpdatedAt != want.UpdatedAt {
			t.Fatalf("provider memory boundary = %+v, want content=%q version=%d updated_at=%q", got, want.Content, want.Version, want.UpdatedAt)
		}
	}
}
