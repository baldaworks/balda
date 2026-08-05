-- +goose Up
ALTER TABLE session_memory_ingress_outbox ADD COLUMN next_attempt_at TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_session_memory_ingress_retry
    ON session_memory_ingress_outbox(state, next_attempt_at, scope_key, scope_sequence);

-- +goose Down
DROP INDEX IF EXISTS idx_session_memory_ingress_retry;
ALTER TABLE session_memory_ingress_outbox DROP COLUMN next_attempt_at;
