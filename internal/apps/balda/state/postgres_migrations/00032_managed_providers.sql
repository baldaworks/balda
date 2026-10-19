-- +goose Up
CREATE TABLE balda_provider_state (
 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
 state_version BIGINT NOT NULL CHECK (state_version >= 0)
);
INSERT INTO balda_provider_state(singleton, state_version) VALUES (1, 0);

CREATE TABLE balda_provider_records (
 provider_id TEXT PRIMARY KEY,
 current_revision_id TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
 deleted INTEGER NOT NULL CHECK (deleted IN (0, 1))
);

CREATE TABLE balda_provider_revisions (
 provider_id TEXT NOT NULL REFERENCES balda_provider_records(provider_id),
 revision_id TEXT NOT NULL,
 driver_type TEXT NOT NULL,
 contract_version BIGINT NOT NULL CHECK (contract_version > 0),
 format_version BIGINT NOT NULL CHECK (format_version > 0),
 definition_json TEXT NOT NULL,
 protected_values BYTEA NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY (provider_id, revision_id)
);

ALTER TABLE balda_session_metadata ADD COLUMN provider_binding_json TEXT NULL;

-- +goose Down
ALTER TABLE balda_session_metadata DROP COLUMN provider_binding_json;
DROP TABLE balda_provider_revisions;
DROP TABLE balda_provider_records;
DROP TABLE balda_provider_state;
