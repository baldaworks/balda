-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS swarm_agent_steps (
    id TEXT PRIMARY KEY,
    step_key TEXT NOT NULL UNIQUE,
    task_id TEXT NOT NULL,
    agent_name TEXT NOT NULL,
    role TEXT NOT NULL,
    iteration INTEGER NOT NULL,
    payload_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    result_json TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_swarm_agent_steps_task
ON swarm_agent_steps(task_id, created_at);

INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES(18, datetime('now'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM schema_migrations WHERE version = 18;

DROP INDEX IF EXISTS idx_swarm_agent_steps_task;
DROP TABLE IF EXISTS swarm_agent_steps;
-- +goose StatementEnd
