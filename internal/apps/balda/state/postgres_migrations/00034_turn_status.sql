-- +goose Up
CREATE TABLE balda_turn_status (
    session_id TEXT PRIMARY KEY REFERENCES balda_session_metadata(session_id) ON DELETE CASCADE,
    archive_id TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('active', 'terminal')),
    started_at TEXT NOT NULL,
    last_activity_at TEXT NOT NULL,
    activity_kind TEXT NOT NULL,
    completed_at TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT '',
    boot_id TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TEXT NOT NULL
);
CREATE INDEX balda_turn_status_archive_idx ON balda_turn_status(session_id, archive_id);

-- +goose Down
DROP TABLE balda_turn_status;
