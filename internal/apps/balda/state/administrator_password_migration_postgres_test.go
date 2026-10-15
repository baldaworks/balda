//go:build integration && postgres

package state

import (
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestPostgresAdministratorPasswordMigration(t *testing.T) {
	for _, credentialState := range []string{"disabled", "active", "temporary"} {
		t.Run(credentialState, func(t *testing.T) {
			db := newPostgresTestDB(t)
			migrations, err := fs.Sub(postgresMigrationsFS, "postgres_migrations")
			if err != nil {
				t.Fatal(err)
			}
			provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations, goose.WithDisableGlobalRegistry(true))
			if err != nil {
				t.Fatal(err)
			}
			checkAdministratorPasswordMigration(t, db, provider, 26, postgresBind, credentialState)
		})
	}
}
