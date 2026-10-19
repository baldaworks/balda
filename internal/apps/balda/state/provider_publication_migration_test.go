package state

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestSQLiteProviderPublicationUpgradePreservesHistory(t *testing.T) {
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
	checkProviderPublicationUpgrade(t, db, provider, 66)
}

func checkProviderPublicationUpgrade(t *testing.T, db *sql.DB, provider *goose.Provider, previous int64) {
	t.Helper()
	if _, err := provider.UpTo(t.Context(), previous); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO balda_questions (question_id,session_id,channel_kind,address_key,address_json,prompt,status,interaction_json,resume_json,request_json,created_at,updated_at) VALUES ('question','session','mattermost','conversation','{}','Allow?','pending','{}','{}','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO execution_delivery_outbox (id,delivery_key,channel,address_key,kind,payload_json,payload_hash,status,provider_message_id,created_at,updated_at) VALUES ('delivery','delivery-key','mattermost','conversation','delivery','{}','hash','sent','root','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, publishedAt string
	if err := db.QueryRowContext(t.Context(), `SELECT status,provider_created_at FROM balda_questions WHERE question_id='question'`).Scan(&status, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || publishedAt != "" {
		t.Fatalf("question changed during upgrade: status=%q publication=%q", status, publishedAt)
	}
	var messageID string
	if err := db.QueryRowContext(t.Context(), `SELECT provider_message_id,provider_created_at FROM execution_delivery_outbox WHERE id='delivery'`).Scan(&messageID, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if messageID != "root" || publishedAt != "" {
		t.Fatalf("delivery changed during upgrade: message=%q publication=%q", messageID, publishedAt)
	}
}
