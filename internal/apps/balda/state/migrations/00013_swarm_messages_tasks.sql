-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_balda_mailbox_messages_status;
DROP INDEX IF EXISTS idx_balda_mailbox_messages_pending;
DROP INDEX IF EXISTS idx_balda_mailbox_messages_idempotency;
DROP TABLE IF EXISTS balda_mailbox_messages;

CREATE TABLE IF NOT EXISTS swarm_messages (
    id TEXT PRIMARY KEY,
    mailbox TEXT NOT NULL,
    namespace TEXT NOT NULL,
    kind TEXT NOT NULL,

    from_addr TEXT NOT NULL,
    to_addr TEXT NOT NULL,

    session_id TEXT,
    task_id TEXT,
    correlation_id TEXT,
    causation_id TEXT,

    priority INTEGER NOT NULL DEFAULT 0,
    dedupe_key TEXT,

    status TEXT NOT NULL DEFAULT 'queued',
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,

    not_before TEXT,
    expires_at TEXT,

    lease_owner TEXT,
    lease_until TEXT,

    payload_json TEXT NOT NULL,
    meta_json TEXT,

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    error TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '';

CREATE INDEX IF NOT EXISTS idx_swarm_messages_claim
ON swarm_messages(mailbox, status, priority, not_before, created_at);

CREATE INDEX IF NOT EXISTS idx_swarm_messages_task
ON swarm_messages(task_id, created_at);

CREATE INDEX IF NOT EXISTS idx_swarm_messages_correlation
ON swarm_messages(correlation_id, created_at);

CREATE TABLE IF NOT EXISTS swarm_tasks (
    id TEXT PRIMARY KEY,
    session_id TEXT,
    parent_task_id TEXT,

    title TEXT,
    objective TEXT NOT NULL,

    status TEXT NOT NULL DEFAULT 'queued',
    owner_actor TEXT,
    assigned_actor TEXT,

    priority INTEGER NOT NULL DEFAULT 0,

    created_by TEXT,
    created_from TEXT,

    plan_json TEXT,
    result_json TEXT,
    error TEXT,

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    canceled_at TEXT
);

CREATE TABLE IF NOT EXISTS swarm_task_events (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    actor TEXT,
    message_id TEXT,
    payload_json TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_swarm_task_events_task
ON swarm_task_events(task_id, created_at);

INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES(13, datetime('now'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM schema_migrations WHERE version = 13;

DROP INDEX IF EXISTS idx_swarm_task_events_task;
DROP TABLE IF EXISTS swarm_task_events;
DROP TABLE IF EXISTS swarm_tasks;
DROP INDEX IF EXISTS idx_swarm_messages_correlation;
DROP INDEX IF EXISTS idx_swarm_messages_task;
DROP INDEX IF EXISTS idx_swarm_messages_claim;
DROP INDEX IF EXISTS idx_swarm_messages_dedupe;
DROP TABLE IF EXISTS swarm_messages;

CREATE TABLE IF NOT EXISTS balda_mailbox_messages (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL UNIQUE,
    mailbox_id TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_key TEXT NOT NULL,
    subject TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    status TEXT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    available_at TEXT NOT NULL,
    claimed_at TEXT NOT NULL DEFAULT '',
    completed_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_balda_mailbox_messages_idempotency
    ON balda_mailbox_messages(mailbox_id, idempotency_key)
    WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS idx_balda_mailbox_messages_pending
    ON balda_mailbox_messages(mailbox_id, status, available_at, sequence);

CREATE INDEX IF NOT EXISTS idx_balda_mailbox_messages_status
    ON balda_mailbox_messages(status, updated_at);
-- +goose StatementEnd
