-- +goose Up
-- The old SQLite session-memory domain is no longer supported. Canonical
-- Badger state is authoritative; rows removed here are not recoverable.
-- Ingress outbox and audit tables (00030-00032) are intentionally untouched.
DROP TABLE IF EXISTS session_memory_forgets;
DROP TABLE IF EXISTS session_memory_operations;
DROP TABLE IF EXISTS session_memory_provenance;
DROP TABLE IF EXISTS session_memory_revisions;
DROP TABLE IF EXISTS session_memory_sources;
DROP TABLE IF EXISTS session_memory_scopes;

-- +goose Down
-- Binary rollback recreates an empty legacy schema only. Dropped rows cannot
-- be restored by this migration.
CREATE TABLE IF NOT EXISTS session_memory_scopes (
    scope_key TEXT PRIMARY KEY,
    scope_kind TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 0,
    snapshot_json TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS session_memory_sources (
    scope_key TEXT NOT NULL,
    export_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    source_turn_id TEXT NOT NULL,
    state TEXT NOT NULL,
    turn_json TEXT NOT NULL DEFAULT '',
    forgotten_at TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (scope_key, export_id),
    FOREIGN KEY (scope_key) REFERENCES session_memory_scopes(scope_key) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_memory_revisions (
    scope_key TEXT NOT NULL,
    item_id TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    state TEXT NOT NULL,
    revision INTEGER NOT NULL,
    operation_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    normalized_text TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY (scope_key, revision_id),
    UNIQUE (scope_key, item_id, revision_id),
    FOREIGN KEY (scope_key) REFERENCES session_memory_scopes(scope_key) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_memory_provenance (
    scope_key TEXT NOT NULL,
    revision_id TEXT NOT NULL,
    source_export_id TEXT NOT NULL DEFAULT '',
    source_session_id TEXT NOT NULL DEFAULT '',
    source_turn_id TEXT NOT NULL DEFAULT '',
    parent_item_id TEXT NOT NULL DEFAULT '',
    parent_revision_id TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (scope_key, revision_id) REFERENCES session_memory_revisions(scope_key, revision_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_session_memory_sources_scope_state
    ON session_memory_sources(scope_key, state);
CREATE INDEX IF NOT EXISTS idx_session_memory_revisions_scope_state
    ON session_memory_revisions(scope_key, state, kind);
CREATE INDEX IF NOT EXISTS idx_session_memory_provenance_source
    ON session_memory_provenance(scope_key, source_export_id);
CREATE INDEX IF NOT EXISTS idx_session_memory_provenance_parent
    ON session_memory_provenance(scope_key, parent_item_id, parent_revision_id);

CREATE TABLE IF NOT EXISTS session_memory_operations (
    scope_key TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    stage TEXT NOT NULL,
    request_json TEXT NOT NULL,
    outcome_json TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    PRIMARY KEY (scope_key, operation_id),
    FOREIGN KEY (scope_key) REFERENCES session_memory_scopes(scope_key) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_memory_forgets (
    scope_key TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    request_json TEXT NOT NULL,
    outcome_json TEXT NOT NULL,
    committed_at TEXT NOT NULL,
    PRIMARY KEY (scope_key, operation_id),
    FOREIGN KEY (scope_key) REFERENCES session_memory_scopes(scope_key) ON DELETE CASCADE
);
