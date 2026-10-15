-- +goose Up
CREATE TABLE balda_webhook_admissions (
    route_name TEXT NOT NULL,
    dedupe_key TEXT NOT NULL,
    request_id TEXT NOT NULL,
    prompt TEXT NOT NULL,
    job_id TEXT NOT NULL UNIQUE,
    session_id TEXT NOT NULL UNIQUE,
    report_locator_json TEXT,
    created_at TEXT NOT NULL,
    message_id TEXT NOT NULL DEFAULT '',
    stream TEXT NOT NULL DEFAULT '',
    sequence INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (route_name, dedupe_key)
);

-- +goose Down
DROP TABLE IF EXISTS balda_webhook_admissions;
