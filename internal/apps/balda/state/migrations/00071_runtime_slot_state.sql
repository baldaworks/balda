-- +goose Up
ALTER TABLE balda_session_metadata ADD COLUMN runtime_generation BIGINT NOT NULL DEFAULT 0 CHECK(runtime_generation >= 0);
ALTER TABLE balda_session_metadata ADD COLUMN effective_model TEXT NOT NULL DEFAULT '' CHECK(length(CAST(effective_model AS BLOB)) <= 512);
ALTER TABLE balda_session_metadata ADD COLUMN runtime_outcome TEXT NOT NULL DEFAULT '' CHECK(length(CAST(runtime_outcome AS BLOB)) <= 64);

CREATE TABLE balda_runtime_operations (
 operation_id TEXT PRIMARY KEY CHECK(length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 256),
 session_id TEXT NOT NULL CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 512),
 input_hash TEXT NOT NULL CHECK(length(CAST(input_hash AS BLOB)) = 64 AND input_hash NOT GLOB '*[^0-9a-f]*'),
 kind TEXT NOT NULL CHECK(length(CAST(kind AS BLOB)) BETWEEN 1 AND 64),
 state TEXT NOT NULL CHECK(state IN ('prepared','applying','terminal')),
 expected_archive_id TEXT NOT NULL CHECK(length(CAST(expected_archive_id AS BLOB)) <= 512),
 expected_generation BIGINT NOT NULL CHECK(expected_generation >= 0),
 target_archive_id TEXT NOT NULL DEFAULT '' CHECK(length(CAST(target_archive_id AS BLOB)) <= 512),
 target_snapshot_id TEXT NOT NULL DEFAULT '' CHECK(length(CAST(target_snapshot_id AS BLOB)) <= 512),
 target_provider_binding_json TEXT NOT NULL DEFAULT '' CHECK(length(CAST(target_provider_binding_json AS BLOB)) <= 65536 AND (target_provider_binding_json = '' OR (json_valid(target_provider_binding_json) AND json_type(target_provider_binding_json) = 'object'))),
 target_model TEXT NOT NULL DEFAULT '' CHECK(length(CAST(target_model AS BLOB)) <= 512),
 outcome TEXT NOT NULL DEFAULT '' CHECK(length(CAST(outcome AS BLOB)) <= 64),
 reason_code TEXT NOT NULL DEFAULT '' CHECK(length(CAST(reason_code AS BLOB)) <= 64),
 created_at TEXT NOT NULL CHECK(length(CAST(created_at AS BLOB)) BETWEEN 1 AND 64),
 updated_at TEXT NOT NULL CHECK(length(CAST(updated_at AS BLOB)) BETWEEN 1 AND 64),
 terminal_at TEXT NULL CHECK(terminal_at IS NULL OR length(CAST(terminal_at AS BLOB)) BETWEEN 1 AND 64),
 CHECK((state = 'terminal') = (terminal_at IS NOT NULL))
);
CREATE UNIQUE INDEX balda_runtime_operation_unresolved
 ON balda_runtime_operations(session_id)
 WHERE state IN ('prepared','applying');
CREATE INDEX balda_runtime_operation_retention
 ON balda_runtime_operations(state,terminal_at,operation_id);

-- +goose Down
-- Runtime transition evidence is forward-only. Restore a validated backup to downgrade.
SELECT * FROM balda_runtime_operation_state_downgrade_requires_database_backup;
