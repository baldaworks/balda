-- +goose Up
CREATE TABLE balda_mcp_grants (
    grant_id TEXT PRIMARY KEY,
    connection_id TEXT NOT NULL REFERENCES balda_mcp_connections (connection_id),
    resource TEXT NOT NULL,
    issuer TEXT NOT NULL,
    client_id TEXT NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    status TEXT NOT NULL CHECK (status IN ('auth_required', 'authorized', 'disconnected')),
    scopes_json TEXT NOT NULL,
    token_endpoint_auth_method TEXT NOT NULL CHECK (token_endpoint_auth_method IN ('none', 'client_secret_basic', 'client_secret_post')),
    client_secret_expires_at TEXT NOT NULL,
    access_expires_at TEXT NOT NULL,
    protected_values BYTEA NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (connection_id, resource, issuer, client_id)
);
