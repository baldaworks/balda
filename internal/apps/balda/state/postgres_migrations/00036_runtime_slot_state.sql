-- +goose Up
ALTER TABLE balda_session_metadata ADD COLUMN runtime_generation BIGINT NOT NULL DEFAULT 0 CHECK(runtime_generation >= 0);
ALTER TABLE balda_session_metadata ADD COLUMN effective_model TEXT NOT NULL DEFAULT '' CHECK(octet_length(effective_model) <= 512);
ALTER TABLE balda_session_metadata ADD COLUMN runtime_outcome TEXT NOT NULL DEFAULT '' CHECK(octet_length(runtime_outcome) <= 64);

CREATE TABLE balda_runtime_operations (
 operation_id TEXT PRIMARY KEY CHECK(octet_length(operation_id) BETWEEN 1 AND 256),
 session_id TEXT NOT NULL CHECK(octet_length(session_id) BETWEEN 1 AND 512),
 input_hash TEXT NOT NULL CHECK(input_hash ~ '^[0-9a-f]{64}$'),
 kind TEXT NOT NULL CHECK(octet_length(kind) BETWEEN 1 AND 64),
 state TEXT NOT NULL CHECK(state IN ('prepared','applying','terminal')),
 expected_archive_id TEXT NOT NULL CHECK(octet_length(expected_archive_id) <= 512),
 expected_generation BIGINT NOT NULL CHECK(expected_generation >= 0),
 target_archive_id TEXT NOT NULL DEFAULT '' CHECK(octet_length(target_archive_id) <= 512),
 target_snapshot_id TEXT NOT NULL DEFAULT '' CHECK(octet_length(target_snapshot_id) <= 512),
 target_provider_binding_json TEXT NOT NULL DEFAULT '' CHECK(octet_length(target_provider_binding_json) <= 65536 AND (target_provider_binding_json = '' OR jsonb_typeof(target_provider_binding_json::jsonb) = 'object')),
 target_model TEXT NOT NULL DEFAULT '' CHECK(octet_length(target_model) <= 512),
 outcome TEXT NOT NULL DEFAULT '' CHECK(octet_length(outcome) <= 64),
 reason_code TEXT NOT NULL DEFAULT '' CHECK(octet_length(reason_code) <= 64),
 created_at TEXT NOT NULL CHECK(octet_length(created_at) BETWEEN 1 AND 64),
 updated_at TEXT NOT NULL CHECK(octet_length(updated_at) BETWEEN 1 AND 64),
 terminal_at TEXT NULL CHECK(terminal_at IS NULL OR octet_length(terminal_at) BETWEEN 1 AND 64),
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
