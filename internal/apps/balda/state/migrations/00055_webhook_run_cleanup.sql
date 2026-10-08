-- +goose Up
ALTER TABLE execution_jobs ADD COLUMN private_run_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_jobs ADD COLUMN private_run_closed_at TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_execution_jobs_private_run_cleanup
    ON execution_jobs (private_run_kind, private_run_closed_at, created_at, id);

-- +goose Down
DROP INDEX idx_execution_jobs_private_run_cleanup;
ALTER TABLE execution_jobs DROP COLUMN private_run_closed_at;
ALTER TABLE execution_jobs DROP COLUMN private_run_kind;
