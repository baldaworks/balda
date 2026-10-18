-- +goose Up
ALTER TABLE balda_session_metadata ADD COLUMN archive_id TEXT NOT NULL DEFAULT '';
CREATE TABLE balda_archive_sessions (
 archive_id TEXT PRIMARY KEY,
 channel_type TEXT NOT NULL,
 address_key TEXT NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('pending','active','ended','uncertain')),
 observed_at TEXT NOT NULL,
 next_sequence BIGINT NOT NULL DEFAULT 0 CHECK(next_sequence >= 0),
 record_json TEXT NOT NULL
);
CREATE INDEX balda_archive_recent ON balda_archive_sessions(observed_at DESC, archive_id DESC);
CREATE INDEX balda_archive_binding ON balda_archive_sessions(channel_type, address_key);
CREATE TABLE balda_archive_messages (
 archive_id TEXT NOT NULL REFERENCES balda_archive_sessions(archive_id),
 sequence BIGINT NOT NULL CHECK(sequence > 0),
 source_key TEXT NOT NULL UNIQUE,
 role TEXT NOT NULL CHECK(role IN ('user','agent')),
 fingerprint TEXT NOT NULL,
 record_json TEXT NOT NULL,
 PRIMARY KEY(archive_id, sequence)
);
CREATE TABLE balda_archive_lifecycle_intents (
 token TEXT PRIMARY KEY,
 channel_type TEXT NOT NULL,
 address_key TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('pending','completed','retained','destroyed','uncertain','aborted')),
 record_json TEXT NOT NULL
);
CREATE UNIQUE INDEX balda_archive_unresolved ON balda_archive_lifecycle_intents(channel_type,address_key) WHERE outcome IN ('pending','uncertain');

-- +goose Down
-- Archive retention is forward-only. Restore a validated backup to downgrade.
SELECT * FROM balda_archive_downgrade_requires_database_backup;
