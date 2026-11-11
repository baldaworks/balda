//go:build integration && postgres

package state

import (
	"github.com/pressly/goose/v3"
	"io/fs"
	"testing"
)

func TestPostgresEmbeddedAuditCompatibility(t *testing.T) {
	db := newPostgresTestDB(t)
	// Model the required stopped-writer/reopen boundary: prepared statements
	// from the old schema must not survive a column rename.
	db.SetMaxIdleConns(0)
	base, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, auditCompatibilityMigrations(t, base, 38), goose.WithDisableGlobalRegistry(true), goose.WithGoMigrations(goose.NewGoMigration(10, &goose.GoFunc{RunTx: up00010PostgresUserConversion}, &goose.GoFunc{RunTx: downUserConversion})))
	if err != nil {
		t.Fatal(err)
	}
	checkAuditCompatibility(t, db, p, 37, newPostgresUserStore(db))
}
