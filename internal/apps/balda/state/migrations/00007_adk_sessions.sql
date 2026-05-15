-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS balda_adk_app_state (
    app_name TEXT PRIMARY KEY,
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS balda_adk_user_state (
    app_name TEXT NOT NULL,
    user_id TEXT NOT NULL,
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (app_name, user_id)
);

CREATE TABLE IF NOT EXISTS balda_adk_sessions (
    app_name TEXT NOT NULL,
    user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    state_json TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (app_name, user_id, session_id)
);

CREATE TABLE IF NOT EXISTS balda_adk_events (
    app_name TEXT NOT NULL,
    user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    timestamp TEXT NOT NULL,
    event_json TEXT NOT NULL,
    PRIMARY KEY (app_name, user_id, session_id, event_id),
    FOREIGN KEY (app_name, user_id, session_id)
        REFERENCES balda_adk_sessions(app_name, user_id, session_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_balda_adk_sessions_app_user
    ON balda_adk_sessions(app_name, user_id);

CREATE INDEX IF NOT EXISTS idx_balda_adk_events_session_order
    ON balda_adk_events(app_name, user_id, session_id, timestamp, ordinal);

INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES(7, datetime('now'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM schema_migrations WHERE version = 7;
DROP INDEX IF EXISTS idx_balda_adk_events_session_order;
DROP INDEX IF EXISTS idx_balda_adk_sessions_app_user;
DROP TABLE IF EXISTS balda_adk_events;
DROP TABLE IF EXISTS balda_adk_sessions;
DROP TABLE IF EXISTS balda_adk_user_state;
DROP TABLE IF EXISTS balda_adk_app_state;
-- +goose StatementEnd
