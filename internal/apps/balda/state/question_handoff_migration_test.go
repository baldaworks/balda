package state

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestSQLiteQuestionAnswerHandoffUpgradePreservesSettledHistory(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteConnectionString(filepath.Join(t.TempDir(), "state.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	registerBaldaGoMigrations()
	migrations, err := fs.Sub(baldaMigrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatal(err)
	}
	checkQuestionHandoffUpgrade(t, db, p, 62, func(q string) string { return q })
}

func checkQuestionHandoffUpgrade(t *testing.T, db *sql.DB, p *goose.Provider, previous int64, bind func(string) string) {
	t.Helper()
	if _, err := p.UpTo(t.Context(), previous); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"pending", "answered"} {
		_, err := db.ExecContext(t.Context(), bind(`INSERT INTO balda_questions (question_id,session_id,channel_kind,address_key,address_json,prompt,status,interaction_json,resume_json,request_json,answer_json,created_at,updated_at) VALUES (?, 'session','telegram','1:0','{}','Prompt',?,'{}','{}','{}','saved answer','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z')`), status, status)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(t.Context(), `SELECT answer_json, answer_continuation_pending, answer_controls_pending FROM balda_questions`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		count++
		var answer string
		var pending, controls bool
		if err := rows.Scan(&answer, &pending, &controls); err != nil {
			t.Fatal(err)
		}
		if pending || controls || answer != "saved answer" {
			t.Fatal("upgrade queued historical answers or changed data")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("upgrade retained %d questions, want 2", count)
	}
}
