package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adksession "google.golang.org/adk/session"
	_ "modernc.org/sqlite"
)

func TestSQLiteProvider_KVRoundTrip(t *testing.T) {
	provider := newTestProvider(t)
	defer closeProvider(t, provider)

	ctx := context.Background()
	store := provider.SessionMCPKV()

	if err := store.Set(ctx, "alpha", "one"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	got, ok, err := store.Get(ctx, "alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() found = false, want true")
	}
	if got != "one" {
		t.Fatalf("Get() value = %q, want %q", got, "one")
	}

	if err := store.SetJSON(ctx, "json", map[string]any{"count": 2}); err != nil {
		t.Fatalf("SetJSON() error = %v", err)
	}
	merged, err := store.MergeJSON(ctx, "json", map[string]any{"name": "balda"})
	if err != nil {
		t.Fatalf("MergeJSON() error = %v", err)
	}
	if merged["count"] != float64(2) {
		t.Fatalf("merged[count] = %v, want 2", merged["count"])
	}
	if merged["name"] != "balda" {
		t.Fatalf("merged[name] = %v, want balda", merged["name"])
	}
}

func TestSQLiteProvider_SessionStoreRoundTrip(t *testing.T) {
	provider := newTestProvider(t)
	defer closeProvider(t, provider)

	ctx := context.Background()
	store := provider.Sessions()

	record := SessionRecord{
		SessionID:    "tg-1-2",
		UserID:       "tg-101",
		ChannelType:  ChannelTypeTelegram,
		AddressKey:   "1:2",
		AddressJSON:  `{"chat_id":1,"topic_id":2}`,
		AgentName:    "agent",
		WorkspaceDir: "/tmp/ws",
		BranchName:   "norma/balda/tg-1-2",
		Status:       SessionStatusActive,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, ok, err := store.GetByAddress(ctx, ChannelTypeTelegram, "1:2")
	if err != nil {
		t.Fatalf("GetByAddress() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByAddress() found = false, want true")
	}
	if got.SessionID != record.SessionID {
		t.Fatalf("session_id = %q, want %q", got.SessionID, record.SessionID)
	}
	if got.AgentName != record.AgentName {
		t.Fatalf("agent_name = %q, want %q", got.AgentName, record.AgentName)
	}
	if got.UserID != record.UserID {
		t.Fatalf("user_id = %q, want %q", got.UserID, record.UserID)
	}
}

func TestSQLiteProvider_SessionStoreUpsert_DoesNotDecodeAddressJSON(t *testing.T) {
	provider := newTestProvider(t)
	defer closeProvider(t, provider)

	ctx := context.Background()
	store := provider.Sessions()

	record := SessionRecord{
		SessionID:    "tg-9-9",
		UserID:       "tg-900",
		ChannelType:  ChannelTypeTelegram,
		AddressKey:   "9:9",
		AddressJSON:  "{",
		AgentName:    "agent",
		WorkspaceDir: "/tmp/ws",
		BranchName:   "norma/balda/tg-9-9",
		Status:       SessionStatusActive,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, ok, err := store.GetByAddress(ctx, ChannelTypeTelegram, "9:9")
	if err != nil {
		t.Fatalf("GetByAddress() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByAddress() found = false, want true")
	}
	if got.AddressJSON != record.AddressJSON {
		t.Fatalf("address_json = %q, want %q", got.AddressJSON, record.AddressJSON)
	}
}

func TestSQLiteProvider_ScheduledJobStoreRoundTrip(t *testing.T) {
	provider := newTestProvider(t)
	defer closeProvider(t, provider)

	ctx := context.Background()
	store := provider.ScheduledJobs()
	nextRunAt := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)

	record := ScheduledJobRecord{
		JobID:        "job-1",
		SessionID:    "tg-1-2",
		ChannelType:  ChannelTypeTelegram,
		AddressKey:   "1:2",
		AddressJSON:  `{"chat_id":1,"topic_id":2}`,
		Prompt:       "check deployment",
		ScheduleSpec: "*/5 * * * *",
		Timezone:     "UTC",
		Status:       ScheduledJobStatusActive,
		MaxRetries:   4,
		NextRunAt:    nextRunAt,
	}
	if err := store.Upsert(ctx, record); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, ok, err := store.GetByID(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !ok {
		t.Fatal("GetByID() found = false, want true")
	}
	if got.Prompt != record.Prompt {
		t.Fatalf("prompt = %q, want %q", got.Prompt, record.Prompt)
	}
	if got.ScheduleSpec != record.ScheduleSpec {
		t.Fatalf("schedule_spec = %q, want %q", got.ScheduleSpec, record.ScheduleSpec)
	}
	allJobs, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(allJobs) != 1 || allJobs[0].JobID != "job-1" {
		t.Fatalf("List() = %#v, want single job-1", allJobs)
	}

	dueJobs, err := store.ListDue(ctx, nextRunAt.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("ListDue() error = %v", err)
	}
	if len(dueJobs) != 1 || dueJobs[0].JobID != "job-1" {
		t.Fatalf("ListDue() = %#v, want single job-1", dueJobs)
	}

	if err := store.Delete(ctx, "job-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	_, ok, err = store.GetByID(ctx, "job-1")
	if err != nil {
		t.Fatalf("GetByID(after delete) error = %v", err)
	}
	if ok {
		t.Fatal("GetByID(after delete) found = true, want false")
	}
}

func TestSQLiteProvider_OffsetPersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "balda.db")
	ctx := context.Background()

	providerA, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(A) error = %v", err)
	}
	if err := providerA.PollingOffsetStore().Save(ctx, 99); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	closeProvider(t, providerA)

	providerB, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(B) error = %v", err)
	}
	defer closeProvider(t, providerB)

	offset, err := providerB.PollingOffsetStore().Load(ctx)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if offset != 99 {
		t.Fatalf("offset = %d, want 99", offset)
	}
}

func TestSQLiteProvider_WritesSchemaMigrationVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "balda.db")
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

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations version: %v", err)
	}
	if version != 8 {
		t.Fatalf("schema_migrations version = %d, want 8", version)
	}
}

func TestSQLiteProvider_ADKSessionPersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "balda.db")
	ctx := context.Background()

	providerA, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(A) error = %v", err)
	}
	svcA := providerA.ADKSessions()
	created, err := svcA.Create(ctx, &adksession.CreateRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-1-2",
		State: map[string]any{
			"cwd":        "/workspace",
			"app:shared": "app-value",
			"user:name":  "owner",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	event := adksession.NewEvent("invocation-1")
	event.Author = "user"
	event.Actions.StateDelta = map[string]any{
		"cwd":       "/workspace/session",
		"temp:skip": "drop",
	}
	if err := svcA.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	closeProvider(t, providerA)

	providerB, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider(B) error = %v", err)
	}
	defer closeProvider(t, providerB)

	got, err := providerB.ADKSessions().Get(ctx, &adksession.GetRequest{
		AppName:   "norma-balda",
		UserID:    "tg-101",
		SessionID: "tg-1-2",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Session.Events().Len() != 1 {
		t.Fatalf("events len = %d, want 1", got.Session.Events().Len())
	}
	if value, err := got.Session.State().Get("cwd"); err != nil || value != "/workspace/session" {
		t.Fatalf("state cwd = %v, err = %v, want /workspace/session", value, err)
	}
	if value, err := got.Session.State().Get("app:shared"); err != nil || value != "app-value" {
		t.Fatalf("state app:shared = %v, err = %v, want app-value", value, err)
	}
	if _, err := got.Session.State().Get("temp:skip"); err == nil {
		t.Fatal("temp state key persisted, want missing key")
	}
}

func TestSQLiteProvider_AdoptsExistingLegacySchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "balda.db")
	ctx := context.Background()

	seedLegacyBaldaDB(t, dbPath)

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
		t.Fatalf("user_id after migration = %q, want empty for legacy rows", record.UserID)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	var version int
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("query schema_migrations version: %v", err)
	}
	if version != 8 {
		t.Fatalf("schema_migrations version = %d, want 8", version)
	}
}

func TestSQLiteProvider_RejectsObsoletePreBaldaSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "balda.db")
	ctx := context.Background()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
		INSERT INTO schema_migrations(version, applied_at)
		VALUES(8, datetime('now'));`); err != nil {
		t.Fatalf("seed obsolete schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	_, err = NewSQLiteProvider(ctx, dbPath)
	if err == nil {
		t.Fatal("NewSQLiteProvider() error = nil, want obsolete schema error")
	}
	if !strings.Contains(err.Error(), "obsolete pre-Balda state schema") {
		t.Fatalf("NewSQLiteProvider() error = %q, want obsolete pre-Balda state schema", err)
	}
}

func newTestProvider(t *testing.T) Provider {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "balda.db")
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

func seedLegacyBaldaDB(t *testing.T, dbPath string) {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	legacySchema := []string{
		`CREATE TABLE balda_app_kv (
			namespace TEXT NOT NULL,
			key TEXT NOT NULL,
			value_json TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (namespace, key)
		);`,
		`CREATE TABLE balda_session_metadata (
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
		`CREATE INDEX idx_balda_session_metadata_status ON balda_session_metadata(status);`,
		`INSERT INTO balda_session_metadata (
			session_id, chat_id, topic_id, agent_name, workspace_dir, branch_name, status, updated_at
		)
		 VALUES ('balda-1-2', 1, 2, 'agent', '/tmp/ws', 'norma/balda/balda-1-2', 'active', '2026-01-01T00:00:00Z');`,
		`CREATE TABLE balda_telegram_offsets (
			bot_key TEXT PRIMARY KEY,
			offset INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`INSERT INTO balda_telegram_offsets (bot_key, offset, updated_at)
		 VALUES ('balda-default', 321, '2026-01-01T00:00:00Z');`,
	}

	for _, stmt := range legacySchema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy balda db stmt failed: %v\nstmt: %s", err, stmt)
		}
	}
}
