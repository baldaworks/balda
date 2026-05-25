-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS swarm_delivery_outbox (
    id TEXT PRIMARY KEY,
    delivery_key TEXT NOT NULL UNIQUE,
    task_id TEXT,
    session_id TEXT,
    channel TEXT NOT NULL,
    address_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    provider_message_id TEXT,
    sent_at TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_swarm_delivery_outbox_task
ON swarm_delivery_outbox(task_id, created_at);

INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES(17, datetime('now'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM schema_migrations WHERE version = 17;

DROP INDEX IF EXISTS idx_swarm_delivery_outbox_task;
DROP TABLE IF EXISTS swarm_delivery_outbox;
-- +goose StatementEnd
