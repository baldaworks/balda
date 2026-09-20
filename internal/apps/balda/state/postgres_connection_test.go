package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresConnectionSettings(t *testing.T) {
	t.Parallel()
	if db, err := openPostgresDB(PostgresConfig{}); err == nil || db != nil {
		t.Fatal("invalid PostgreSQL settings opened a pool")
	}
	db, err := openPostgresDB(PostgresConfig{Host: "localhost", Port: 5432, Name: "name with spaces", User: "user@name", Password: "quote' and /?@", SSLMode: "require"})
	if err != nil {
		t.Fatal(err)
	}
	if db.Stats().OpenConnections != 0 {
		t.Error("configuration parsing unexpectedly opened a connection")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresErrorRedaction(t *testing.T) {
	t.Parallel()
	const secret = "postgres-error-secret-canary"
	for _, cause := range []error{
		errors.New(secret),
		&pgconn.PgError{Code: "23505", Message: secret, Detail: secret},
		fmt.Errorf("%s: %w", secret, context.Canceled),
	} {
		err := redactPostgresError(cause)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("error redaction failed: %v", err)
		}
		if errors.Is(cause, context.Canceled) && !errors.Is(err, context.Canceled) {
			t.Fatal("redaction lost cancellation identity")
		}
	}
}
