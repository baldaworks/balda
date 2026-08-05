-- +goose Up
CREATE TABLE IF NOT EXISTS session_memory_ingress_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    export_id TEXT NOT NULL,
    action TEXT NOT NULL,
    actor TEXT NOT NULL,
    reason TEXT NOT NULL,
    occurred_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_memory_ingress_audit_export
    ON session_memory_ingress_audit(export_id, id);

-- +goose Down
DROP TABLE IF EXISTS session_memory_ingress_audit;
