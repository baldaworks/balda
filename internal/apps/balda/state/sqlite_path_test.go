//go:build integration && sqlite

package state

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSQLiteConnectionString(t *testing.T) {
	const memoryPath = ":memory:"
	for _, path := range []string{memoryPath, filepath.Join(t.TempDir(), "state # %.db")} {
		u, err := url.Parse(sqliteConnectionString(path))
		if err != nil {
			t.Fatalf("parse SQLite URI for %q: %v", path, err)
		}
		if u.Scheme != "file" || u.Host != "" || u.Fragment != "" {
			t.Fatalf("SQLite URI for %q = %v, want local file URI without fragment", path, u)
		}
		if path == memoryPath {
			if u.Opaque != ":memory:" {
				t.Fatalf("memory SQLite URI = %v, want file::memory:", u)
			}
		} else if u.Opaque != "" || !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, `\`) {
			t.Fatalf("SQLite URI for native absolute path %q = %v, want absolute slash path", path, u)
		}
		if got := u.Query().Get("_txlock"); got != "immediate" {
			t.Errorf("transaction lock for %q = %q, want immediate", path, got)
		}
		if got := u.Query()["_pragma"]; !slices.Equal(got, []string{"foreign_keys(1)", "busy_timeout(5000)"}) {
			t.Errorf("connection pragmas for %q = %v", path, got)
		}
	}
}

func TestSQLiteNativePathPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state # %.db")
	provider, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if err := provider.AppKV().Set(t.Context(), "native-path", "provider write"); err != nil {
		t.Fatal(err)
	}
	closeProvider(t, provider)
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Opening the native filename directly must reach the provider's database.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var stored string
	if err := db.QueryRowContext(t.Context(), `SELECT value_json FROM balda_app_kv
		WHERE namespace = ? AND key = ?`, NamespaceApp, "native-path").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `"provider write"` {
		t.Fatalf("native file value = %q, want provider write", stored)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE balda_app_kv SET value_json = ?
		WHERE namespace = ? AND key = ?`, `"native write"`, NamespaceApp, "native-path"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, found, err := reopened.AppKV().Get(t.Context(), "native-path")
	if err != nil || !found || got != "native write" {
		t.Fatalf("reopened native file value = %q, found=%t, err=%v", got, found, err)
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(original, current) {
		t.Fatal("reopening SQLite replaced the native file")
	}

	// Replace the constructor's connection so these pragmas come from the URI.
	reopenedDB := reopened.(*sqliteProvider).db
	reopenedDB.SetMaxIdleConns(0)
	tx, err := reopenedDB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	for _, pragma := range []struct {
		name string
		want int
	}{
		{"foreign_keys", 1},
		{"busy_timeout", 5000},
	} {
		var got int
		if err := tx.QueryRowContext(t.Context(), "PRAGMA "+pragma.name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != pragma.want {
			t.Errorf("transaction %s = %d, want %d", pragma.name, got, pragma.want)
		}
	}
}
