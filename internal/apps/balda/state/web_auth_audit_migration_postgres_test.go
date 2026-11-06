//go:build integration && postgres

package state

import (
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestPostgresWebAuthAuditActorMigrationPreservesValues(t *testing.T) {
	db := newPostgresTestDB(t)
	migrations, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(goose.NewGoMigration(10,
			&goose.GoFunc{RunTx: up00010PostgresUserConversion}, &goose.GoFunc{RunTx: downUserConversion})))
	if err != nil {
		t.Fatal(err)
	}
	checkWebAuthAuditActorMigration(t, db, provider, 37, 38, postgresBind)
}
