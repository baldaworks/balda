package state

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/pressly/goose/v3"
)

func TestSQLiteAdministratorPasswordMigration(t *testing.T) {
	for _, credentialState := range []string{"disabled", "active", "temporary"} {
		t.Run(credentialState, func(t *testing.T) {
			db, err := sql.Open("sqlite", sqliteConnectionString(filepath.Join(t.TempDir(), "state.db")))
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
			checkAdministratorPasswordMigration(t, db, provider, 61, func(q string) string { return q }, credentialState)
		})
	}
}

func checkAdministratorPasswordMigration(t *testing.T, db *sql.DB, provider *goose.Provider, previousVersion int64, bind func(string) string, credentialState string) {
	t.Helper()
	if _, err := provider.UpTo(t.Context(), previousVersion); err != nil {
		t.Fatal(err)
	}
	hash, mustChange := "existing-hash", 0
	if credentialState == "disabled" {
		hash = ""
	}
	if credentialState == "temporary" {
		mustChange = 1
	}
	if _, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_users
  (user_id, display_name, username, normalized_username, status, role, password_hash,
   credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
  VALUES ('primary', 'superuser', 'superuser', 'superuser', 'active', 'administrator', ?, ?, ?, 1, 3, 7,
   '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`), hash, credentialState, mustChange); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var gotHash, gotState string
	var gotChange, credentialVersion, version int
	if err := db.QueryRowContext(t.Context(), `SELECT password_hash, credential_state, must_change, credential_version, version FROM balda_users WHERE user_id = 'primary'`).Scan(&gotHash, &gotState, &gotChange, &credentialVersion, &version); err != nil {
		t.Fatal(err)
	}
	if credentialState == "disabled" {
		if !userpassword.Verify(gotHash, []byte("superusersuperuser")) || gotState != "temporary" || gotChange != 1 || credentialVersion != 4 || version != 8 {
			t.Fatal("disabled primary did not receive a usable temporary credential")
		}
	} else if gotHash != hash || gotState != credentialState || gotChange != mustChange || credentialVersion != 3 || version != 7 {
		t.Fatal("migration changed an existing usable credential")
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var reopenedHash string
	var reopenedVersion int
	if err := db.QueryRowContext(t.Context(), `SELECT password_hash, credential_version FROM balda_users WHERE user_id = 'primary'`).Scan(&reopenedHash, &reopenedVersion); err != nil {
		t.Fatal(err)
	}
	if reopenedHash != gotHash || reopenedVersion != credentialVersion {
		t.Fatal("repeat upgrade changed the administrator password")
	}
}
