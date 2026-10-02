package state

import (
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/pressly/goose/v3"
)

type conversionTestDatabase struct {
	db      *sql.DB
	bind    func(string) string
	upgrade func() (usercmd.Store, error)
}

func TestSQLiteUserConversion(t *testing.T) {
	runUserConversion(t, newSQLiteConversionTestDatabase)
}

func newSQLiteConversionTestDatabase(t *testing.T) conversionTestDatabase {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", sqliteConnectionString(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registerBaldaGoMigrations()
	migrations, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(t.Context(), 44); err != nil {
		t.Fatal(err)
	}
	return conversionTestDatabase{
		db: db, bind: func(query string) string { return query },
		upgrade: func() (usercmd.Store, error) {
			provider, err := NewSQLiteProvider(t.Context(), path)
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { _ = provider.Close() })
			return provider.Users(), nil
		},
	}
}

func runUserConversion(t *testing.T, factory func(*testing.T) conversionTestDatabase) {
	t.Helper()
	t.Run("UpgradeAndReopen", func(t *testing.T) {
		fixture := factory(t)
		fixture.owner(t, `{"user_id":101,"bindings":["slackagent:T1:U1","zulip:303"]}`)
		fixture.exec(t, `INSERT INTO balda_collaborators (user_id, username, first_name, added_by, added_at)
			VALUES ('202', 'operator_handle', 'Op', '101', '2026-09-23T00:00:00Z')`)
		store, err := fixture.upgrade()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []struct {
			channel, principal, id string
			role                   usercmd.Role
			primary                bool
		}{
			{"telegram", "101", "22dc524a-ab0d-5502-b60b-ea29f4f61ef0", usercmd.RoleAdministrator, true},
			{"telegram", "202", "1f07ebb3-07c8-5c47-876c-dc46249e11a0", usercmd.RoleOperator, false},
			{"slackagent", "T1:U1", "8615c5d8-4eae-5718-bac8-79c54fe8043b", usercmd.RoleAdministrator, false},
			{"zulip", "303", "cc27c4c1-5b1c-5472-bb99-a34ab39cbf09", usercmd.RoleAdministrator, false},
		} {
			user, found, err := store.GetUserByBinding(t.Context(), want.channel, want.principal)
			if err != nil || !found {
				t.Fatalf("binding %s:%s: found=%t, err=%v", want.channel, want.principal, found, err)
			}
			if user.ID != want.id || user.Role != want.role || user.Primary != want.primary || user.Status != usercmd.StatusActive ||
				user.Credential.State != usercmd.CredentialStateDisabled || user.Credential.MustChange {
				t.Errorf("converted %s:%s = %+v", want.channel, want.principal, user)
			}
			if user.Primary && (user.Username != "superuser" || user.DisplayName != "superuser") {
				t.Errorf("primary identity = %q/%q", user.Username, user.DisplayName)
			}
			if want.principal == "202" && (user.Binding.ProviderUsername != "operator_handle" || user.Binding.ProviderFirstName != "Op") {
				t.Errorf("collaborator profile = %+v", user.Binding)
			}
			secret, found, err := store.GetCredentialSecret(t.Context(), user.ID)
			if err != nil || !found || secret.PasswordHash != "" {
				t.Errorf("converted credential: found=%t hash=%q err=%v", found, secret.PasswordHash, err)
			}
		}
		fixture.count(t, "balda_users", 4)
		fixture.count(t, "balda_user_bindings", 4)
		fixture.count(t, "balda_security_audit_events", 8)
		fixture.count(t, "balda_user_migrations", 1)
		before := fixture.snapshot(t)
		if _, err := fixture.upgrade(); err != nil {
			t.Fatal(err)
		}
		if after := fixture.snapshot(t); !reflect.DeepEqual(before, after) {
			t.Fatal("reopening changed converted data")
		}
	})
	t.Run("EmptyDatabase", func(t *testing.T) {
		fixture := factory(t)
		if _, err := fixture.upgrade(); err != nil {
			t.Fatal(err)
		}
		fixture.count(t, "balda_users", 0)
		fixture.count(t, "balda_user_migrations", 0)
	})
	t.Run("NewestCollaboratorProfile", func(t *testing.T) {
		fixture := factory(t)
		fixture.owner(t, `{"user_id":101}`)
		fixture.exec(t, `INSERT INTO balda_collaborators (user_id, username, first_name, added_by, added_at)
			VALUES ('202', 'older', 'Older', '101', '2026-09-22T00:00:00Z'),
			       ('telegram:202', 'newest', 'Latest', '101', '2026-09-23T00:00:00Z')`)
		store, err := fixture.upgrade()
		if err != nil {
			t.Fatal(err)
		}
		user, found, err := store.GetUserByBinding(t.Context(), "telegram", "202")
		if err != nil || !found {
			t.Fatalf("collaborator: found=%t err=%v", found, err)
		}
		if user.DisplayName != "Latest newest" || user.Binding.ProviderUsername != "newest" || user.Binding.ProviderFirstName != "Latest" {
			t.Fatalf("collaborator profile = %q/%q/%q", user.DisplayName, user.Binding.ProviderUsername, user.Binding.ProviderFirstName)
		}
		fixture.count(t, "balda_users", 2)
		fixture.count(t, "balda_user_bindings", 2)
	})
	t.Run("RollbackAfterWriteFailure", func(t *testing.T) {
		fixture := factory(t)
		fixture.owner(t, `{"user_id":101}`)
		// Collide with this snapshot's audit ID after its user and binding inserts.
		fixture.exec(t, `INSERT INTO balda_security_audit_events
			(event_id, action, target_type, target_id, outcome, source, occurred_at)
			VALUES ('8052e0f1-339c-5399-b6b2-6768c6a1b425', 'user.migrated', 'user', 'snapshot',
			'succeeded', 'fixture', '2026-09-23T00:00:00Z')`)
		before := fixture.snapshot(t)
		if _, err := fixture.upgrade(); err == nil {
			t.Fatal("upgrade accepted conflicting audit")
		}
		if after := fixture.snapshot(t); !reflect.DeepEqual(before, after) {
			t.Fatal("write failure did not roll back converted data and Goose version")
		}
		fixture.exec(t, `DELETE FROM balda_security_audit_events WHERE source = 'fixture'`)
		if _, err := fixture.upgrade(); err != nil {
			t.Fatalf("retry after fixing conflict: %v", err)
		}
		fixture.count(t, "balda_users", 1)
	})
	t.Run("PreserveCompletedConversion", func(t *testing.T) {
		fixture := factory(t)
		fixture.owner(t, `{"user_id":101}`)
		fixture.canonicalUser(t)
		fixture.exec(t, `INSERT INTO balda_user_migrations
			(migration_id, source_fingerprint, generated_user_count, generated_binding_count, primary_user_id, completed_at)
			VALUES ('completed', 'existing-fingerprint', 1, 0, 'existing-admin', '2026-09-23T00:00:00Z')`)
		fixture.exec(t, `INSERT INTO balda_backoffice_sessions
			(session_id, user_id, assurance, credential_version, access_selector, access_verifier_digest,
			 csrf_verifier_digest, created_at, last_seen_at, access_expires_at, refresh_expires_at, version)
			VALUES ('session', 'existing-admin', 'normal', 3, 'access', ?, ?,
			 '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z', '2026-09-23T00:15:00Z', '2026-09-23T12:00:00Z', 2)`,
			[]byte("access-digest"), []byte("csrf-digest"))
		fixture.exec(t, `INSERT INTO balda_backoffice_refresh_tokens
			(session_id, selector, verifier_digest, generation, state, issued_at, expires_at)
			VALUES ('session', 'refresh', ?, 1, 'active', '2026-09-23T00:00:00Z', '2026-09-23T12:00:00Z')`, []byte("refresh-digest"))
		before := fixture.snapshot(t)
		if _, err := fixture.upgrade(); err != nil {
			t.Fatal(err)
		}
		after := fixture.snapshot(t)
		// A successful upgrade advances Goose history while retaining user state.
		delete(before, "goose_db_version")
		delete(after, "goose_db_version")
		if !reflect.DeepEqual(before, after) {
			t.Fatal("upgrade changed completed users, credentials or browser sessions")
		}
	})
	for _, test := range []struct {
		name, owner, collaborator string
		canonical                 bool
	}{
		{name: "MalformedOwner", owner: `{"user_id":`},
		{name: "NullOwner", owner: `null`},
		{name: "InvalidPrincipal", owner: `{"subject":"telegram:invalid"}`},
		{name: "ConflictingRoles", owner: `{"user_id":101}`, collaborator: "101"},
		{name: "CollaboratorWithoutOwner", collaborator: "202"},
		{name: "UnmarkedMixedState", owner: `{"user_id":101}`, canonical: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := factory(t)
			if test.owner != "" {
				fixture.owner(t, test.owner)
			}
			if test.collaborator != "" {
				fixture.exec(t, `INSERT INTO balda_collaborators (user_id, added_by, added_at)
					VALUES (?, '101', '2026-09-23T00:00:00Z')`, test.collaborator)
			}
			if test.canonical {
				fixture.canonicalUser(t)
			}
			before := fixture.snapshot(t)
			if _, err := fixture.upgrade(); err == nil {
				t.Fatal("upgrade accepted invalid or conflicting input")
			}
			if after := fixture.snapshot(t); !reflect.DeepEqual(before, after) {
				t.Fatal("failed upgrade changed data or migration history")
			}
		})
	}
}

func (f conversionTestDatabase) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.db.ExecContext(t.Context(), f.bind(query), args...); err != nil {
		t.Fatal(err)
	}
}

func (f conversionTestDatabase) owner(t *testing.T, owner string) {
	t.Helper()
	f.exec(t, `INSERT INTO balda_app_kv (namespace, key, value_json, updated_at)
		VALUES ('balda.app', 'owner', ?, '2026-09-23T00:00:00Z')`, owner)
}

func (f conversionTestDatabase) canonicalUser(t *testing.T) {
	t.Helper()
	f.exec(t, `INSERT INTO balda_users (user_id, display_name, username, normalized_username, status, role,
		password_hash, credential_state, must_change, is_primary, credential_version, version, created_at, updated_at)
		VALUES ('existing-admin', 'superuser', 'superuser', 'superuser', 'active', 'administrator',
		'existing-password-hash', 'active', 0, 1, 3, 5, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`)
}

func (f conversionTestDatabase) count(t *testing.T, table string, want int) {
	t.Helper()
	var got int
	if err := f.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("%s rows = %d, want %d", table, got, want)
	}
}

func (f conversionTestDatabase) snapshot(t *testing.T) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	for _, table := range []string{"balda_users", "balda_user_bindings", "balda_security_audit_events", "balda_user_migrations",
		"balda_backoffice_sessions", "balda_backoffice_refresh_tokens", "goose_db_version"} {
		projection := "*"
		if table == "balda_backoffice_sessions" {
			// Compare the session contract present before this additive schema upgrade.
			projection = "session_id, user_id, assurance, credential_version, access_selector, access_verifier_digest, csrf_verifier_digest, created_at, last_seen_at, access_expires_at, refresh_expires_at, revoked_at, revocation_reason, version, device_label, connection_peer"
		}
		rows, err := f.db.QueryContext(t.Context(), "SELECT "+projection+" FROM "+table+" ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			result[table] = append(result[table], fmt.Sprint(values))
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
