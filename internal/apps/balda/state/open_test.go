package state

import (
	"context"
	"errors"
	"testing"
)

func TestOpenProviderDispatch(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"sqlite", "postgres", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			var sqliteCalls, postgresCalls int
			failure := errors.New("selected backend unavailable")
			cfg := DatabaseConfig{Type: kind, SQLite: SQLiteConfig{Path: "/data/state.db"}, Postgres: PostgresConfig{Host: "db"}}
			_, err := openProvider(t.Context(), cfg,
				func(_ context.Context, path string) (Provider, error) {
					sqliteCalls++
					if path != cfg.SQLite.Path {
						t.Errorf("path = %q, want %q", path, cfg.SQLite.Path)
					}
					return nil, failure
				},
				func(_ context.Context, pg PostgresConfig) (Provider, error) {
					postgresCalls++
					if pg != cfg.Postgres {
						t.Error("PostgreSQL constructor received different configuration")
					}
					return nil, failure
				},
			)
			if err == nil {
				t.Fatal("Open succeeded despite constructor failure")
			}
			switch kind {
			case "sqlite":
				if sqliteCalls != 1 || postgresCalls != 0 || !errors.Is(err, failure) {
					t.Fatal("SQLite selection retried or lost the original failure")
				}
			case "postgres":
				if postgresCalls != 1 || sqliteCalls != 0 || !errors.Is(err, failure) {
					t.Fatal("PostgreSQL selection fell back or lost the original failure")
				}
			default:
				if sqliteCalls != 0 || postgresCalls != 0 {
					t.Fatal("invalid backend opened a database")
				}
			}
		})
	}
}
