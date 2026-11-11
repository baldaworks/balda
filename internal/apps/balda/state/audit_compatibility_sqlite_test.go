//go:build integration && sqlite

package state

import (
	"github.com/pressly/goose/v3"
	"io/fs"
	"testing"
)

func TestSQLiteEmbeddedAuditCompatibility(t *testing.T) {
	db, _ := newScheduleRunCloseMigrationDB(t)
	base, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, auditCompatibilityMigrations(t, base, 73))
	if err != nil {
		t.Fatal(err)
	}
	checkAuditCompatibility(t, db, p, 72, newSQLiteUserStore(db))
}
