-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_swarm_messages_shadow_dedupe;
DROP INDEX IF EXISTS idx_swarm_messages_dedupe;
DROP INDEX IF EXISTS idx_swarm_messages_claim;
DROP INDEX IF EXISTS idx_swarm_messages_task;
DROP INDEX IF EXISTS idx_swarm_messages_correlation;
DROP TABLE IF EXISTS swarm_messages;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

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
WHERE dedupe_key IS NOT NULL AND dedupe_key != '' AND status != 'shadow';

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_shadow_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '' AND status = 'shadow';

CREATE INDEX IF NOT EXISTS idx_swarm_messages_claim
ON swarm_messages(mailbox, status, priority, not_before, created_at);

CREATE INDEX IF NOT EXISTS idx_swarm_messages_task
ON swarm_messages(task_id, created_at);

CREATE INDEX IF NOT EXISTS idx_swarm_messages_correlation
ON swarm_messages(correlation_id, created_at);
-- +goose StatementEnd
