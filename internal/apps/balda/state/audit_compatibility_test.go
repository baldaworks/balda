//go:build integration && (sqlite || postgres)

package state

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/pressly/goose/v3"
	"golang.org/x/crypto/bcrypt"
)

// The historical rename is test data, not an application migration. The current
// embedded application supports the lower floor; operators use migration-bearing
// tooling for exactly this reversible compatibility boundary.
func auditCompatibilityMigrations(t *testing.T, base fs.FS, version int64) fs.FS {
	t.Helper()
	files := fstest.MapFS{}
	if err := fs.WalkDir(base, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(base, path)
		if err != nil {
			return err
		}
		files[path] = &fstest.MapFile{Data: data}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%05d_audit_actor.sql", version)
	data, err := os.ReadFile("testdata/audit_compatibility/" + name)
	if err != nil {
		t.Fatal(err)
	}
	files[name] = &fstest.MapFile{Data: data}
	return files
}

func checkAuditCompatibility(t *testing.T, db *sql.DB, migrations *goose.Provider, floor int64, store *sqlUserStore) {
	t.Helper()
	if _, err := migrations.UpTo(t.Context(), floor); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	password := []byte("synthetic-compatible-password")
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := contractUser("compat-admin", "compat.admin", true, now)
	if err := store.CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: string(hash)}, contractAudit("before-rename", usercmd.AuditActionCredentialChanged, user.ID, now)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	family := contractSessionFamily(user.ID, now)
	// Seed security and product state outside the renamed column as well: opaque
	// protected bytes, immutable catalog capture, archive history and preference.
	blob := []byte{1, 7, 19, 37, 61, 89}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO balda_mfa_credentials (factor_id,user_id,rp_id,credential_id,public_key,credential_data,sign_count,backup_eligible,backup_state,created_at) VALUES ('factor',?,'example.test',?,?,?,0,0,0,?)`, []any{user.ID, blob, blob, blob, now.Format(time.RFC3339)}},
		{`INSERT INTO balda_mfa_profiles (user_id,enabled,active_factor_id,version) VALUES (?,1,'factor',2)`, []any{user.ID}},
		{`INSERT INTO balda_app_kv (namespace,key,value_json,updated_at) VALUES ('catalog','captured','{"snapshot":"immutable"}',?)`, []any{now.Format(time.RFC3339)}},
		{`INSERT INTO balda_archive_sessions (archive_id,channel_type,address_key,status,observed_at,record_json) VALUES ('archive','telegram','101:0','active',?,'{"history":"retained"}')`, []any{now.Format(time.RFC3339)}},
		{`INSERT INTO balda_archive_messages (archive_id,sequence,source_key,role,fingerprint,record_json) VALUES ('archive',1,'input','user','fingerprint','{"text":"retained conversation"}')`, nil},
		{`INSERT INTO balda_session_metadata (session_id,channel_type,address_key,address_json,agent_name,workspace_dir,branch_name,status,updated_at,archive_id,requested_model,runtime_generation,effective_model,runtime_outcome) VALUES ('session','telegram','101:0','{}','fixture','','','active',?,'archive','preferred-model',2,'observed-model','context_preserved')`, []any{now.Format(time.RFC3339)}},
		{`INSERT INTO balda_runtime_operations (operation_id,session_id,input_hash,kind,state,expected_archive_id,expected_generation,target_archive_id,target_snapshot_id,target_provider_binding_json,target_model,target_requested_model,created_at,updated_at) VALUES ('replacement','session',?,'lifetime_preference','applying','archive',2,'next-archive','next-snapshot','{}','recipe-model','preferred-model',?,?)`, []any{fmt.Sprintf("%064x", 1), now.Format(time.RFC3339), now.Format(time.RFC3339)}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), store.bind(statement.query), statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	family.WebAuthnVerifiedAt, family.MFAFactorID = now, "factor"
	if err := store.CreateSession(t.Context(), family, contractAudit("before-login", usercmd.AuditActionLoginSucceeded, family.ID, now)); err != nil {
		t.Fatalf("seed browser family: %v", err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), store.bind(`INSERT INTO balda_mcp_connections (connection_id,public_id,source,current_revision_id,enabled,deleted,version,created_at,updated_at) VALUES ('mcp','tools','managed','revision',1,0,1,?,?)`), now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), store.bind(`INSERT INTO balda_mcp_revisions (connection_id,revision_id,definition_json,protected_values,created_at) VALUES ('mcp','revision','{}',?,?)`), blob, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), store.bind(`INSERT INTO balda_mcp_grants (grant_id,connection_id,resource,issuer,client_id,generation,status,scopes_json,token_endpoint_auth_method,client_secret_expires_at,access_expires_at,protected_values,created_at,updated_at) VALUES ('grant','mcp','https://resource.example.test','https://issuer.example.test','client',3,'authorized','["resource"]','none','','',?,?,?)`), blob, now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.UpTo(t.Context(), floor+1); err != nil {
		t.Fatal(err)
	}
	// Include an immutable audit row written after extraction's rename.
	if _, err := db.ExecContext(t.Context(), store.bind(`INSERT INTO balda_security_audit_events (event_id,actor_web_auth_session_id,action,target_type,target_id,outcome,source,occurred_at) VALUES ('after-rename',?,'web_auth.login.succeeded','web_auth_session',?,'succeeded','compatibility',?)`), family.ID, family.ID, now.Add(time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	tables := []string{"balda_users", "balda_backoffice_sessions", "balda_backoffice_refresh_tokens", "balda_mfa_credentials", "balda_mfa_profiles", "balda_security_audit_events", "balda_mcp_connections", "balda_mcp_revisions", "balda_mcp_grants", "balda_app_kv", "balda_archive_sessions", "balda_archive_messages", "balda_session_metadata", "balda_runtime_operations"}
	before := auditCompatibilityRows(t, db, tables)
	if _, err := migrations.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	if version, err := migrations.GetDBVersion(t.Context()); err != nil || version != floor {
		t.Fatalf("compatibility floor=%d,%v", version, err)
	}
	if after := auditCompatibilityRows(t, db, tables); !reflect.DeepEqual(before, after) {
		t.Fatal("audit Down changed security/product rows")
	}
	secret, found, err := store.GetCredentialSecret(t.Context(), user.ID)
	if err != nil || !found || bcrypt.CompareHashAndPassword([]byte(secret.PasswordHash), password) != nil {
		t.Fatal("credential unusable after Down")
	}
	access, found, err := store.GetSessionByAccessSelector(t.Context(), family.Access.Selector)
	if err != nil || !found || access.Family.ID != family.ID || access.Family.MFAFactorID != "factor" {
		t.Fatalf("retained browser authority=%v,%t,%v", access, found, err)
	}
	audit, err := store.ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 20})
	if err != nil || len(audit.Events) != 3 {
		t.Fatalf("audit read=%+v,%v", audit, err)
	}
	for _, event := range audit.Events {
		if event.ID == "after-rename" && (event.ActorSessionID != family.ID || string(event.Action) != "web_auth.login.succeeded" || string(event.TargetType) != "web_auth_session" || !event.OccurredAt.Equal(now.Add(time.Minute))) {
			t.Fatal("restored audit reader changed historical event values")
		}
	}
	user.DisplayName = "guarded write after compatibility"
	user.Version++
	if err := store.UpdateUser(t.Context(), user, user.Version-1, contractAudit("after-down", usercmd.AuditActionUserUpdated, user.ID, now.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	afterWrite := auditCompatibilityRows(t, db, tables)
	if _, err := migrations.UpTo(t.Context(), floor+1); err != nil {
		t.Fatal(err)
	}
	if after := auditCompatibilityRows(t, db, tables); !reflect.DeepEqual(afterWrite, after) {
		t.Fatal("audit Up changed preserved rows")
	}
	if version, err := migrations.GetDBVersion(t.Context()); err != nil || version != floor+1 {
		t.Fatalf("forward compatibility version=%d,%v", version, err)
	}
	var actor, action, target, occurred string
	if err := db.QueryRowContext(t.Context(), `SELECT actor_web_auth_session_id,action,target_type,occurred_at FROM balda_security_audit_events WHERE event_id='after-rename'`).Scan(&actor, &action, &target, &occurred); err != nil {
		t.Fatal(err)
	}
	if actor != family.ID || action != "web_auth.login.succeeded" || target != "web_auth_session" || occurred != now.Add(time.Minute).Format(time.RFC3339) {
		t.Fatal("forward audit contract changed historical row")
	}
	if _, err := migrations.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Logf("audit compatibility %d -> %d -> %d preserved %d populated security/product tables", floor+1, floor, floor+1, len(tables))
}

func auditCompatibilityRows(t *testing.T, db *sql.DB, tables []string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := db.QueryContext(t.Context(), "SELECT * FROM "+table+" ORDER BY 1") //nolint:unqueryvet // Compare all stored bytes across a column rename, including future additive fields.
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], fmt.Sprint(values))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if len(out[table]) == 0 {
			t.Fatal("unpopulated continuity table: " + table)
		}
	}
	return out
}
