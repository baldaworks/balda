//go:build integration && postgres

package state

import (
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestPostgresSessionChannelInstructionUpgradePreservesRuntimeState(t *testing.T) {
	db := newPostgresTestDB(t)
	migrations, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	checkSessionChannelInstructionUpgrade(t, db, p, 28, postgresBind)
}
