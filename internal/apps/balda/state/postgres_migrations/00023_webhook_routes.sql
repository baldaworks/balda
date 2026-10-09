-- +goose Up
CREATE TABLE balda_webhook_routes (
    name TEXT PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('config', 'managed')),
    path TEXT NOT NULL,
    prompt_template TEXT NOT NULL,
    report_to_kind TEXT NOT NULL DEFAULT '',
    report_to_key TEXT NOT NULL DEFAULT '',
    ack_on_delivery BIGINT NOT NULL DEFAULT 0 CHECK (ack_on_delivery IN (0, 1)),
    dedupe_source TEXT NOT NULL DEFAULT 'request_id',
    dedupe_header TEXT NOT NULL DEFAULT '',
    auth_type TEXT NOT NULL DEFAULT 'none',
    auth_header TEXT NOT NULL DEFAULT '',
    secret_verifier TEXT NOT NULL DEFAULT '',
    enabled BIGINT NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    deleted BIGINT NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
    definition_version BIGINT NOT NULL DEFAULT 1 CHECK (definition_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (source = 'managed' OR secret_verifier = ''),
    CHECK (source = 'config' OR (auth_type = 'header' AND auth_header <> '' AND secret_verifier <> ''))
);

CREATE UNIQUE INDEX idx_balda_webhook_routes_active_path
    ON balda_webhook_routes (path) WHERE enabled = 1 AND deleted = 0;

-- +goose Down
DROP TABLE IF EXISTS balda_webhook_routes;
