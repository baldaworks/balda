-- +goose Up
CREATE TABLE IF NOT EXISTS session_memory_ingress_outbox (
    export_id TEXT PRIMARY KEY,
    scope_key TEXT NOT NULL,
    scope_kind TEXT NOT NULL,
    scope_sequence INTEGER NOT NULL,
    subject TEXT NOT NULL,
    envelope_json TEXT NOT NULL,
    state TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    published_at TEXT NOT NULL DEFAULT '',
    UNIQUE (scope_key, scope_sequence)
);

CREATE INDEX IF NOT EXISTS idx_session_memory_ingress_claim
    ON session_memory_ingress_outbox(state, lease_until, scope_key, scope_sequence);

-- +goose Down
DROP TABLE IF EXISTS session_memory_ingress_outbox;
