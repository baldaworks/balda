//go:build integration && postgres

package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresProviderRedactsDatabaseErrors(t *testing.T) {
	db := newPostgresTestDB(t)
	p, err := initializePostgresProvider(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	// PostgreSQL DETAIL and MESSAGE must not reach caller-facing errors.
	_, err = db.ExecContext(t.Context(), `
		CREATE FUNCTION secret_failure() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'canary-stored-secret'; END $$;
		CREATE TRIGGER reject_plugin_update BEFORE UPDATE ON balda_plugin_installs
		FOR EACH STATEMENT EXECUTE FUNCTION secret_failure();
	`)
	if err != nil {
		t.Fatal(err)
	}
	err = p.Plugins().SetPluginEnabled(t.Context(), "test-plugin", true, time.Now())
	if err == nil || strings.Contains(err.Error(), "canary-stored-secret") {
		t.Fatalf("plugin error was not redacted: %v", err)
	}
}

func TestPostgresInitializationCancellationClosesDatabase(t *testing.T) {
	db := newPostgresTestDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, err := initializePostgresProvider(ctx, db)
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled initialization = %v, %v", p, err)
	}
	if err := db.PingContext(t.Context()); err == nil {
		t.Fatal("failed initialization left its database open")
	}
}

func TestPostgresProviderContract(t *testing.T) {
	runProviderContract(t, func(t *testing.T) contractOpener {
		cfg := newPostgresTestConfig(t)
		return func(ctx context.Context, _ string) (Provider, error) {
			db := stdlib.OpenDB(*cfg)
			db.SetMaxOpenConns(8)
			t.Cleanup(func() { _ = db.Close() })
			return initializePostgresProvider(ctx, db)
		}
	})
}
