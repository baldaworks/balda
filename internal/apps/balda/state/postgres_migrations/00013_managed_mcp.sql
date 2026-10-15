-- +goose Up
CREATE TABLE balda_mcp_connections (
    connection_id TEXT PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    source TEXT NOT NULL CHECK (source IN ('config', 'managed')),
    current_revision_id TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    deleted INTEGER NOT NULL CHECK (deleted IN (0, 1)),
    version BIGINT NOT NULL CHECK (version > 0),
    published_version BIGINT NOT NULL DEFAULT 0 CHECK (published_version >= 0 AND published_version <= version),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (deleted = 0 OR enabled = 0)
);

CREATE TABLE balda_mcp_revisions (
    connection_id TEXT NOT NULL REFERENCES balda_mcp_connections (connection_id),
    revision_id TEXT NOT NULL,
    definition_json TEXT NOT NULL,
    protected_values BYTEA NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (connection_id, revision_id)
);

ALTER TABLE balda_mcp_connections ADD CONSTRAINT balda_mcp_current_revision
    FOREIGN KEY (connection_id, current_revision_id)
    REFERENCES balda_mcp_revisions (connection_id, revision_id) DEFERRABLE INITIALLY DEFERRED;
