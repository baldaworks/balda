//go:build integration && (sqlite || postgres)

package state

import (
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
)

func checkWebhookHistoryMigration(t *testing.T, db *sql.DB, migrations *goose.Provider, previousVersion int64, bind func(string) string) {
	t.Helper()
	if _, err := migrations.UpTo(t.Context(), previousVersion); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_webhook_admissions
		(route_name, dedupe_key, request_id, prompt, job_id, session_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`), "archived", "old-key", "old-request", "rendered input",
		"webhook-old", "wh-old", "2026-10-09T12:00:00.5Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var rawBody sql.NullString
	var source, createdAt string
	if err := db.QueryRowContext(t.Context(), bind(`SELECT raw_body, source, created_at FROM balda_webhook_admissions
		WHERE job_id = ?`), "webhook-old").Scan(&rawBody, &source, &createdAt); err != nil {
		t.Fatal(err)
	}
	if rawBody.Valid || source != WebhookHistorySourceExternal || createdAt != "2026-10-09T12:00:00.500000000Z" {
		t.Fatalf("upgraded admission raw body = %+v, source = %q, time = %q", rawBody, source, createdAt)
	}
	store := &sqlWebhookAdmissionStore{db: db, bind: bind}
	history, found, err := store.GetHistory(t.Context(), "archived", "webhook-old")
	if err != nil || !found || history.RawBody != nil || history.Source != WebhookHistorySourceExternal {
		t.Fatalf("upgraded history = %+v found=%t err=%v", history, found, err)
	}
}
