//go:build integration && sqlite

package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"

	"testing"

	_ "modernc.org/sqlite"
)

const expectedSQLiteMigrationVersion = 36

func TestSQLiteProvider_SessionStoreUpsert_PopulatesTelegramAddressColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	provider, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}

	record := SessionRecord{
		SessionID:    "tg--1002667079342-8939",
		UserID:       "tg-101",
		ChannelType:  ChannelTypeTelegram,
		AddressKey:   "-1002667079342:8939",
		AddressJSON:  `{"chat_id":-1002667079342,"topic_id":8939}`,
		AgentName:    "agent",
		WorkspaceDir: "/tmp/ws",
		BranchName:   "norma/balda/tg--1002667079342-8939",
		Status:       SessionStatusActive,
	}
	if err := provider.Sessions().Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	closeProvider(t, provider)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	var chatID, topicID int64
	if err := db.QueryRowContext(ctx, `
		SELECT chat_id, topic_id
		FROM balda_session_metadata
		WHERE session_id = ?`,
		record.SessionID,
	).Scan(&chatID, &topicID); err != nil {
		t.Fatalf("query telegram address columns: %v", err)
	}
	if chatID != -1002667079342 || topicID != 8939 {
		t.Fatalf("telegram address columns = %d/%d, want -1002667079342/8939", chatID, topicID)
	}
}

func TestSQLiteProvider_WritesSchemaMigrationVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	provider, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	closeProvider(t, provider)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	assertGooseVersion(t, ctx, db, expectedSQLiteMigrationVersion)
	assertRequiredBaldaSQLiteTables(t, ctx, db)
	assertSessionMetadataHasNoChatTopicUnique(t, ctx, db)
}

func TestJobEventOutboxMigrationRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	upTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(up) error = %v", err)
	}
	if err := up00024JobEventOutbox(ctx, upTx); err != nil {
		_ = upTx.Rollback()
		t.Fatalf("up00024JobEventOutbox() error = %v", err)
	}
	if err := upTx.Commit(); err != nil {
		t.Fatalf("Commit(up) error = %v", err)
	}
	if exists, err := sqliteTableExists(ctx, db, "execution_job_event_outbox"); err != nil || !exists {
		t.Fatalf("job event outbox after up exists=%v err=%v, want true", exists, err)
	}

	downTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(down) error = %v", err)
	}
	if err := down00024JobEventOutbox(ctx, downTx); err != nil {
		_ = downTx.Rollback()
		t.Fatalf("down00024JobEventOutbox() error = %v", err)
	}
	if err := downTx.Commit(); err != nil {
		t.Fatalf("Commit(down) error = %v", err)
	}
	if exists, err := sqliteTableExists(ctx, db, "execution_job_event_outbox"); err != nil || exists {
		t.Fatalf("job event outbox after down exists=%v err=%v, want false", exists, err)
	}
}

func TestSQLiteProvider_MigratesPreviousSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	seedPreviousSchemaDB(t, dbPath)

	provider, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer closeProvider(t, provider)

	offset, err := provider.PollingOffsetStore().Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if offset != 321 {
		t.Fatalf("offset = %d, want 321", offset)
	}

	store := provider.Sessions()
	record, ok, err := store.GetByAddress(ctx, ChannelTypeTelegram, "1:2")
	if err != nil {
		t.Fatalf("GetByAddress() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByAddress() found = false, want true after migration")
	}
	if record.SessionID != "tg-1-2" {
		t.Fatalf("session_id after migration = %q, want tg-1-2", record.SessionID)
	}
	if record.BranchName != "norma/balda/tg-1-2" {
		t.Fatalf("branch_name after migration = %q, want norma/balda/tg-1-2", record.BranchName)
	}
	if record.AddressJSON != `{"chat_id":1,"topic_id":2}` {
		t.Fatalf("address_json after migration = %q, want telegram address json", record.AddressJSON)
	}
	if record.UserID != "" {
		t.Fatalf("user_id after migration = %q, want empty for pre-migration rows", record.UserID)
	}
	if record.RuntimeSnapshotID != "" {
		t.Fatalf("runtime_snapshot_id after migration = %q, want empty for legacy row", record.RuntimeSnapshotID)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	assertGooseVersion(t, ctx, db, expectedSQLiteMigrationVersion)
	assertRequiredBaldaSQLiteTables(t, ctx, db)
}

func TestSQLiteProvider_MigratesPreviousSchemaAtVersion8(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	seedPreviousSchemaAtVersion8(t, dbPath)

	provider, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	defer closeProvider(t, provider)

	offset, err := provider.PollingOffsetStore().Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if offset != 321 {
		t.Fatalf("offset = %d, want 321", offset)
	}

	store := provider.Sessions()
	record, ok, err := store.GetByAddress(ctx, ChannelTypeTelegram, "1:2")
	if err != nil {
		t.Fatalf("GetByAddress() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByAddress() found = false, want true after migration")
	}
	if record.SessionID != "tg-1-2" {
		t.Fatalf("session_id after migration = %q, want tg-1-2", record.SessionID)
	}

	ownerValue, ok, err := provider.AppKV().GetJSON(ctx, "owner")
	if err != nil {
		t.Fatalf("GetJSON(owner) error = %v", err)
	}
	if !ok {
		t.Fatal("GetJSON(owner) found = false, want true after namespace migration")
	}
	owner, ok := ownerValue.(map[string]any)
	if !ok {
		t.Fatalf("GetJSON(owner) type = %T, want map[string]any", ownerValue)
	}
	if owner["username"] != "metalagman" {
		t.Fatalf("owner username after namespace migration = %v, want metalagman", owner["username"])
	}

	token, ok, err := provider.AppKV().Get(ctx, "owner_auth_token")
	if err != nil {
		t.Fatalf("Get(owner_auth_token) error = %v", err)
	}
	if !ok {
		t.Fatal("Get(owner_auth_token) found = false, want true after namespace migration")
	}
	if token != "balda-token" {
		t.Fatalf("owner_auth_token after namespace migration = %q, want balda-token", token)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	assertRequiredBaldaSQLiteTables(t, ctx, db)
	assertSessionMetadataHasNoChatTopicUnique(t, ctx, db)

	var appName string
	if err := db.QueryRowContext(ctx, `SELECT app_name FROM balda_runtime_sessions WHERE user_id = 'tg-101'`).Scan(&appName); err != nil {
		t.Fatalf("query migrated adk session app_name: %v", err)
	}
	if appName != "norma-balda" {
		t.Fatalf("app_name after migration = %q, want norma-balda", appName)
	}

	var jobID, content string
	if err := db.QueryRowContext(ctx, `
		SELECT job_id, content
		FROM balda_scheduled_jobs
		WHERE job_id = 'previous-daily-review'`,
	).Scan(&jobID, &content); err != nil {
		t.Fatalf("query migrated scheduled job: %v", err)
	}
	if jobID != "previous-daily-review" || content != "Review previous queue" {
		t.Fatalf("migrated scheduled job = %q/%q, want previous-daily-review/Review previous queue", jobID, content)
	}

	assertGooseVersion(t, ctx, db, expectedSQLiteMigrationVersion)
}

func TestSQLiteProvider_Migration11BackfillsBuggyTelegramAddressColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	ctx := context.Background()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	seedBaldaDBAtVersion10WithBuggyZeroSession(t, db)

	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}

	var chatID, topicID int64
	if err := db.QueryRowContext(ctx, `
		SELECT chat_id, topic_id
		FROM balda_session_metadata
		WHERE session_id = 'tg--1002667079342-8939'`,
	).Scan(&chatID, &topicID); err != nil {
		t.Fatalf("query migrated telegram address columns: %v", err)
	}
	if chatID != -1002667079342 || topicID != 8939 {
		t.Fatalf("migrated telegram address columns = %d/%d, want -1002667079342/8939", chatID, topicID)
	}
	assertSessionMetadataHasNoChatTopicUnique(t, ctx, db)
}

func newTestProvider(t *testing.T) Provider {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "state.db")
	provider, err := NewSQLiteProvider(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	return provider
}

func closeProvider(t *testing.T, provider Provider) {
	t.Helper()
	if err := provider.Close(); err != nil {
		t.Fatalf("provider.Close() error = %v", err)
	}
}

func seedPreviousSchemaDB(t *testing.T, dbPath string) {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	previousSchema := []string{
		`CREATE TABLE relay_app_kv (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			value_json TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (namespace, key)
		);`,
		`CREATE TABLE relay_session_metadata (
			session_id TEXT PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			topic_id INTEGER NOT NULL,
			agent_name TEXT NOT NULL,
			workspace_dir TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			status TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (chat_id, topic_id)
		);`,
		`CREATE INDEX idx_relay_session_metadata_status ON relay_session_metadata(status);`,
		`INSERT INTO relay_session_metadata (
			session_id, chat_id, topic_id, agent_name, workspace_dir, branch_name, status, updated_at
		)
		 VALUES ('relay-1-2', 1, 2, 'agent', '/tmp/ws', 'norma/balda/relay-1-2', 'active', '2026-01-01T00:00:00Z');`,
		`CREATE TABLE relay_telegram_offsets (
			bot_key TEXT PRIMARY KEY,
			offset INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`INSERT INTO relay_telegram_offsets (bot_key, offset, updated_at)
		 VALUES ('relay-default', 321, '2026-01-01T00:00:00Z');`,
	}

	for _, stmt := range previousSchema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed previous schema db stmt failed: %v\nstmt: %s", err, stmt)
		}
	}
}

func seedPreviousSchemaAtVersion8(t *testing.T, dbPath string) {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	stmts := []string{
		`CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		);`,
		`INSERT INTO schema_migrations(version, applied_at)
		 VALUES(8, datetime('now'));`,
		`CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT (datetime('now'))
		);`,
		`INSERT INTO goose_db_version(version_id, is_applied)
		 VALUES(0, 1), (1, 1), (2, 1), (3, 1), (4, 1), (5, 1), (6, 1), (7, 1), (8, 1);`,
		`CREATE TABLE relay_app_kv (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			value_json TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			expires_at TEXT,
			PRIMARY KEY (namespace, key)
		);`,
		`INSERT INTO relay_app_kv (namespace, key, value_json, updated_at)
		 VALUES
			(
				'relay.app',
				'owner',
				'{"user_id":2317500,"chat_id":2317500,"username":"metalagman","first_name":"Alexey","last_name":"Samoylov","has_topics_enabled":false,"registered_at":"2026-04-22T19:00:07.616352877+06:00"}',
				'2026-01-01T00:00:00Z'
			),
			('relay.app', 'owner_auth_token', '"relay-token"', '2026-01-01T00:00:00Z'),
			('balda.app', 'owner_auth_token', '"balda-token"', '2026-01-01T00:00:00Z');`,
		`CREATE TABLE relay_session_metadata (
			session_id TEXT PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			topic_id INTEGER NOT NULL,
			agent_name TEXT NOT NULL,
			workspace_dir TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			status TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			channel_type TEXT NOT NULL DEFAULT 'telegram',
			address_key TEXT NOT NULL DEFAULT '',
			address_json TEXT NOT NULL DEFAULT '{}',
			user_id TEXT NOT NULL DEFAULT '',
			UNIQUE (chat_id, topic_id)
		);`,
		`CREATE INDEX idx_relay_session_metadata_status ON relay_session_metadata(status);`,
		`CREATE UNIQUE INDEX idx_relay_session_metadata_channel_address
		 ON relay_session_metadata(channel_type, address_key);`,
		`INSERT INTO relay_session_metadata (
			session_id, user_id, chat_id, topic_id, channel_type, address_key, address_json,
			agent_name, workspace_dir, branch_name, status, updated_at
		)
		 VALUES (
			'tg-1-2', '', 1, 2, 'telegram', '1:2', '{"chat_id":1,"topic_id":2}',
			'agent', '/tmp/ws', 'norma/balda/tg-1-2', 'active', '2026-01-01T00:00:00Z'
		);`,
		`CREATE TABLE relay_telegram_offsets (
			bot_key TEXT PRIMARY KEY,
			offset INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`INSERT INTO relay_telegram_offsets (bot_key, offset, updated_at)
		 VALUES ('relay-default', 321, '2026-01-01T00:00:00Z');`,
		`CREATE TABLE relay_collaborators (
			user_id TEXT PRIMARY KEY,
			username TEXT NOT NULL DEFAULT '',
			first_name TEXT NOT NULL DEFAULT '',
			added_by TEXT NOT NULL,
			added_at TEXT NOT NULL
		);`,
		`CREATE TABLE relay_adk_app_state (
			app_name TEXT PRIMARY KEY,
			state_json TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`INSERT INTO relay_adk_app_state (app_name, state_json, updated_at)
		 VALUES ('norma-relay', '{}', '2026-01-01T00:00:00Z');`,
		`CREATE TABLE relay_adk_user_state (
			app_name TEXT NOT NULL,
			user_id TEXT NOT NULL,
			state_json TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (app_name, user_id)
		);`,
		`INSERT INTO relay_adk_user_state (app_name, user_id, state_json, updated_at)
		 VALUES ('norma-relay', 'tg-101', '{}', '2026-01-01T00:00:00Z');`,
		`CREATE TABLE relay_adk_sessions (
			app_name TEXT NOT NULL,
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			state_json TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (app_name, user_id, session_id)
		);`,
		`INSERT INTO relay_adk_sessions (app_name, user_id, session_id, state_json, updated_at)
		 VALUES ('norma-relay', 'tg-101', 'tg-1-2', '{}', '2026-01-01T00:00:00Z');`,
		`CREATE TABLE relay_adk_events (
			app_name TEXT NOT NULL,
			user_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			event_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			timestamp TEXT NOT NULL,
			event_json TEXT NOT NULL,
			PRIMARY KEY (app_name, user_id, session_id, event_id),
			FOREIGN KEY (app_name, user_id, session_id)
				REFERENCES relay_adk_sessions(app_name, user_id, session_id)
				ON DELETE CASCADE
		);`,
		`CREATE INDEX idx_relay_adk_sessions_app_user
		 ON relay_adk_sessions(app_name, user_id);`,
		`CREATE INDEX idx_relay_adk_events_session_order
		 ON relay_adk_events(app_name, user_id, session_id, timestamp, ordinal);`,
		`INSERT INTO relay_adk_events (
			app_name, user_id, session_id, event_id, ordinal, timestamp, event_json
		)
		 VALUES (
			'norma-relay', 'tg-101', 'tg-1-2', 'event-1', 1, '2026-01-01T00:00:00Z', '{}'
		);`,
		`CREATE TABLE relay_scheduled_jobs (
			job_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			channel_type TEXT NOT NULL,
			address_key TEXT NOT NULL,
			address_json TEXT NOT NULL,
			prompt TEXT NOT NULL,
			schedule_spec TEXT NOT NULL,
			timezone TEXT NOT NULL DEFAULT 'UTC',
			status TEXT NOT NULL DEFAULT 'active',
			max_retries INTEGER NOT NULL DEFAULT 3,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_dispatch_key TEXT NOT NULL DEFAULT '',
			next_run_at TEXT NOT NULL,
			last_run_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE INDEX idx_relay_scheduled_jobs_due
		 ON relay_scheduled_jobs(status, next_run_at);`,
		`CREATE INDEX idx_relay_scheduled_jobs_locator
		 ON relay_scheduled_jobs(channel_type, address_key);`,
		`INSERT INTO relay_scheduled_jobs (
			job_id, session_id, channel_type, address_key, address_json, prompt, schedule_spec, timezone, status,
			max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		)
		 VALUES (
			'previous-daily-review', 'tg-1-2', 'telegram', '1:2', '{"chat_id":1,"topic_id":2}',
			'Review previous queue', '0 9 * * *', 'UTC', 'active',
			3, 0, '', '2026-01-02T09:00:00Z', '', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'
		);`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed relay version 8 db stmt failed: %v\nstmt: %s", err, stmt)
		}
	}
}

func seedBaldaDBAtVersion10WithBuggyZeroSession(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		);`,
		`INSERT INTO schema_migrations(version, applied_at)
		 VALUES(10, datetime('now'));`,
		`CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT (datetime('now'))
		);`,
		`INSERT INTO goose_db_version(version_id, is_applied)
		 VALUES(0, 1), (1, 1), (2, 1), (3, 1), (4, 1), (5, 1), (6, 1), (7, 1), (8, 1), (9, 1), (10, 1);`,
		`CREATE TABLE balda_app_kv (id INTEGER);`,
		`CREATE TABLE balda_session_metadata (
			session_id TEXT PRIMARY KEY,
			chat_id INTEGER NOT NULL,
			topic_id INTEGER NOT NULL,
			agent_name TEXT NOT NULL,
			workspace_dir TEXT NOT NULL,
			branch_name TEXT NOT NULL,
			status TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			channel_type TEXT NOT NULL DEFAULT 'telegram',
			address_key TEXT NOT NULL DEFAULT '',
			address_json TEXT NOT NULL DEFAULT '{}',
			user_id TEXT NOT NULL DEFAULT '',
			UNIQUE (chat_id, topic_id)
		);`,
		`CREATE INDEX idx_balda_session_metadata_status
		 ON balda_session_metadata(status);`,
		`CREATE UNIQUE INDEX idx_balda_session_metadata_channel_address
		 ON balda_session_metadata(channel_type, address_key);`,
		`INSERT INTO balda_session_metadata (
			session_id, user_id, chat_id, topic_id, channel_type, address_key, address_json,
			agent_name, workspace_dir, branch_name, status, updated_at
		)
		 VALUES (
			'tg--1002667079342-8939', 'tg-101', 0, 0, 'telegram', '-1002667079342:8939',
			'{"chat_id":-1002667079342,"topic_id":8939}',
			'agent', '/tmp/ws', 'norma/balda/tg--1002667079342-8939', 'active', '2026-01-01T00:00:00Z'
		);`,
		`CREATE TABLE balda_telegram_offsets (id INTEGER);`,
		`CREATE TABLE balda_collaborators (id INTEGER);`,
		`CREATE TABLE balda_runtime_app_state (id INTEGER);`,
		`CREATE TABLE balda_runtime_user_state (id INTEGER);`,
		`CREATE TABLE balda_runtime_sessions (id INTEGER);`,
		`CREATE TABLE balda_runtime_events (id INTEGER);`,
		`CREATE TABLE balda_scheduled_jobs (
			job_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			channel_type TEXT NOT NULL,
			address_key TEXT NOT NULL,
			address_json TEXT NOT NULL,
			prompt TEXT NOT NULL,
			schedule_spec TEXT NOT NULL,
			timezone TEXT NOT NULL DEFAULT 'UTC',
			status TEXT NOT NULL DEFAULT 'active',
			max_retries INTEGER NOT NULL DEFAULT 3,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_dispatch_key TEXT NOT NULL DEFAULT '',
			next_run_at TEXT NOT NULL,
			last_run_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed balda version 10 db stmt failed: %v\nstmt: %s", err, stmt)
		}
	}
}

func assertGooseVersion(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil {
		t.Fatalf("query goose_db_version version: %v", err)
	}
	if version != want {
		t.Fatalf("goose_db_version max(version_id) = %d, want %d", version, want)
	}
}

func assertRequiredBaldaSQLiteTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	for _, name := range requiredBaldaStateTables {
		exists, err := sqliteTableExists(ctx, db, name)
		if err != nil {
			t.Fatalf("sqliteTableExists(%q) error = %v", name, err)
		}
		if !exists {
			t.Fatalf("sqliteTableExists(%q) = false, want true", name)
		}
	}
}

func assertSessionMetadataHasNoChatTopicUnique(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()

	var createSQL string
	if err := db.QueryRowContext(ctx, `
		SELECT sql
		FROM sqlite_master
		WHERE type = 'table' AND name = 'balda_session_metadata'`,
	).Scan(&createSQL); err != nil {
		t.Fatalf("query balda_session_metadata schema: %v", err)
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(createSQL), " "))
	if strings.Contains(normalized, "unique (chat_id, topic_id)") {
		t.Fatalf("balda_session_metadata still has chat/topic uniqueness: %s", createSQL)
	}
}

func TestPollingOffsetStore_DecoupledFromVendor(t *testing.T) {
	t.Parallel()

	// Compile-time checks that state.Provider returns a neutral PollingOffsetStore.
	var _ PollingOffsetStore = (*sqliteOffsetStore)(nil)

	provider := newTestProvider(t)
	defer closeProvider(t, provider)

	ctx := context.Background()
	store := provider.PollingOffsetStore()
	if err := store.Save(ctx, 12345); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != 12345 {
		t.Fatalf("Load() = %d, want 12345", got)
	}
}
