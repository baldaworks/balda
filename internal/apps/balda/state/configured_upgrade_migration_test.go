package state

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestSQLiteConfiguredSnapshotUpgradeMigration(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registerBaldaGoMigrations()
	migrations, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(t.Context(), 49); err != nil {
		t.Fatal(err)
	}
	checkConfiguredSnapshotUpgradeMigration(t, db, func(query string) string { return query }, func() error { return migrate(t.Context(), db) })
}

func checkConfiguredSnapshotUpgradeMigration(t *testing.T, db *sql.DB, bind func(string) string, upgrade func() error) {
	t.Helper()
	const prefix = "runtime_catalog_configured_upgrade:"
	digest := strings.Repeat("a", 64)
	var snapshots = make(map[string]string)
	for _, tc := range []struct {
		name      string
		transport string
		change    func(map[string]any)
	}{
		{name: "stdio", transport: "stdio"},
		{name: "http", transport: "streamable-http"},
		{name: "sse", transport: "sse"},
		{name: "known-current", transport: "stdio", change: func(d map[string]any) { d["targeting_known"] = true }},
		{name: "explicit-unknown-current", transport: "stdio", change: func(d map[string]any) { d["targeting_known"] = false }},
		{name: "unknown-with-targets", transport: "stdio", change: func(d map[string]any) { d["target_provider_ids"] = []string{"alpha"} }},
		{name: "captured", transport: "streamable-http", change: func(d map[string]any) { d["config_ref"] = "config:captured" }},
		{name: "different-source", transport: "stdio", change: func(d map[string]any) { d["id"].(map[string]any)["source"].(map[string]any)["kind"] = "plugin" }},
		{name: "mismatched-name", transport: "stdio", change: func(d map[string]any) { d["id"].(map[string]any)["name"] = "foreign" }},
		{name: "extra-field", transport: "stdio", change: func(d map[string]any) { d["extra"] = "value" }},
		{name: "malformed-revision", transport: "stdio", change: func(d map[string]any) { d["revision"] = "missing" }},
		{name: "malformed-id", transport: "stdio", change: func(d map[string]any) { d["id"] = "invalid" }},
	} {
		descriptor := map[string]any{"id": map[string]any{"source": map[string]any{"kind": "configured-mcp", "name": tc.name}, "kind": "mcp-server", "name": tc.name}, "revision": digest, "name": tc.name, "transport": tc.transport}
		if tc.change != nil {
			tc.change(descriptor)
		}
		data, err := json.Marshal(map[string]any{"id": tc.name, "scope": map[string]string{"kind": "application"}, "mcp_servers": []any{descriptor}})
		if err != nil {
			t.Fatal(err)
		}
		key := "runtime_catalog_snapshot:" + tc.name
		snapshots[key] = string(data)
		if _, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_app_kv (namespace, key, value_json, updated_at) VALUES ('balda.app', ?, ?, '2026-09-01T00:00:00Z')`), key, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	// A malformed pair with the same identity/digest and conflicting transports
	// must not acquire a marker depending on database traversal order.
	for _, transport := range []string{"stdio", "sse"} {
		descriptor := map[string]any{"id": map[string]any{"source": map[string]any{"kind": "configured-mcp", "name": "ambiguous"}, "kind": "mcp-server", "name": "ambiguous"}, "revision": digest, "name": "ambiguous", "transport": transport}
		data, err := json.Marshal(map[string]any{"mcp_servers": []any{descriptor}})
		if err != nil {
			t.Fatal(err)
		}
		key := "runtime_catalog_snapshot:ambiguous-" + transport
		snapshots[key] = string(data)
		if _, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_app_kv (namespace, key, value_json, updated_at) VALUES ('balda.app', ?, ?, '2026-09-01T00:00:00Z')`), key, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	// Duplicate retained catalogs must not produce conflicting marker aliases.
	if _, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_app_kv (namespace, key, value_json, updated_at) VALUES ('balda.app', ?, ?, '2026-09-01T00:00:00Z')`), "runtime_catalog_snapshot:duplicate", snapshots["runtime_catalog_snapshot:stdio"]); err != nil {
		t.Fatal(err)
	}
	snapshots["runtime_catalog_snapshot:duplicate"] = snapshots["runtime_catalog_snapshot:stdio"]
	if err := upgrade(); err != nil {
		t.Fatal(err)
	}
	var first map[string]string
	for range 2 {
		rows, err := db.QueryContext(t.Context(), `SELECT key, value_json FROM balda_app_kv WHERE namespace = 'balda.app' ORDER BY key`)
		if err != nil {
			t.Fatal(err)
		}
		markers := make(map[string]string)
		for rows.Next() {
			var key, raw string
			if err := rows.Scan(&key, &raw); err != nil {
				t.Fatal(err)
			}
			if before, ok := snapshots[key]; ok && raw != before {
				t.Errorf("migration changed original snapshot bytes for %s", key)
			}
			if strings.HasPrefix(key, prefix) {
				markers[key] = raw
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if len(markers) != 3 {
			t.Fatalf("SQL migration markers = %+v, want exactly the three valid historical transport pins", markers)
		}
		for _, name := range []string{"stdio", "http", "sse"} {
			var marker struct {
				Version    int            `json:"version"`
				Descriptor map[string]any `json:"descriptor"`
			}
			if err := json.Unmarshal([]byte(markers[prefix+name+":"+digest]), &marker); err != nil || marker.Version != 1 || marker.Descriptor["name"] != name {
				t.Fatalf("historical %s marker malformed: %+v / %v", name, marker, err)
			}
		}
		if first != nil && !reflect.DeepEqual(first, markers) {
			t.Fatal("repeated provider migrations changed durable markers")
		}
		first = markers
		if err := upgrade(); err != nil {
			t.Fatal(err)
		}
	}
}
