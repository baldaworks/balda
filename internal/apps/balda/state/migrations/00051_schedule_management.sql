-- +goose Up
ALTER TABLE balda_scheduled_jobs ADD COLUMN source TEXT NOT NULL DEFAULT 'internal'
    CHECK (source IN ('config', 'managed', 'internal'));
ALTER TABLE balda_scheduled_jobs ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1
    CHECK (enabled IN (0, 1));
ALTER TABLE balda_scheduled_jobs ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0
    CHECK (deleted IN (0, 1));
ALTER TABLE balda_scheduled_jobs ADD COLUMN definition_version INTEGER NOT NULL DEFAULT 1
    CHECK (definition_version > 0);
ALTER TABLE balda_scheduled_jobs ADD COLUMN target_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_scheduled_jobs ADD COLUMN target_key TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_scheduled_jobs ADD COLUMN report_to_target_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_scheduled_jobs ADD COLUMN report_to_target_key TEXT NOT NULL DEFAULT '';

UPDATE balda_scheduled_jobs
SET source = CASE WHEN lower(trim(schedule_spec)) = '@once' THEN 'internal' ELSE 'config' END,
    target_kind = 'locator',
    target_key = channel_type || ':' || address_key,
    report_to_target_kind = CASE WHEN report_to_enabled = 1 THEN 'locator' ELSE '' END,
    report_to_target_key = CASE WHEN report_to_enabled = 1
        THEN report_to_channel_type || ':' || report_to_address_key ELSE '' END;

CREATE TABLE balda_schedule_runs (
    run_id TEXT PRIMARY KEY,
    schedule_id TEXT NOT NULL,
    trigger TEXT NOT NULL CHECK (trigger IN ('cron', 'manual')),
    trigger_key TEXT NOT NULL,
    definition_version INTEGER NOT NULL CHECK (definition_version > 0),
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    requested_at TEXT NOT NULL,
    due_at TEXT NOT NULL DEFAULT '',
    dispatch_state TEXT NOT NULL DEFAULT 'pending'
        CHECK (dispatch_state IN ('pending', 'retrying', 'dispatched', 'failed', 'canceled')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at TEXT NOT NULL DEFAULT '',
    safe_failure_code TEXT NOT NULL DEFAULT '',
    dispatched_at TEXT NOT NULL DEFAULT '',
    execution_job_id TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (schedule_id, trigger_key)
);

CREATE INDEX idx_balda_schedule_runs_history
    ON balda_schedule_runs (schedule_id, requested_at DESC, run_id DESC);
CREATE INDEX idx_balda_schedule_runs_pending
    ON balda_schedule_runs (dispatch_state, next_attempt_at);

-- +goose Down
DROP INDEX IF EXISTS idx_balda_schedule_runs_pending;
DROP INDEX IF EXISTS idx_balda_schedule_runs_history;
DROP TABLE IF EXISTS balda_schedule_runs;
ALTER TABLE balda_scheduled_jobs DROP COLUMN report_to_target_key;
ALTER TABLE balda_scheduled_jobs DROP COLUMN report_to_target_kind;
ALTER TABLE balda_scheduled_jobs DROP COLUMN target_key;
ALTER TABLE balda_scheduled_jobs DROP COLUMN target_kind;
ALTER TABLE balda_scheduled_jobs DROP COLUMN definition_version;
ALTER TABLE balda_scheduled_jobs DROP COLUMN deleted;
ALTER TABLE balda_scheduled_jobs DROP COLUMN enabled;
ALTER TABLE balda_scheduled_jobs DROP COLUMN source;
