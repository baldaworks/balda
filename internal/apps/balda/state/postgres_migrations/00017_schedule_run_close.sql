-- +goose Up
ALTER TABLE balda_schedule_runs ADD COLUMN closed_at TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_balda_schedule_runs_unclosed ON balda_schedule_runs (dispatch_state, closed_at);

-- +goose Down
DROP INDEX IF EXISTS idx_balda_schedule_runs_unclosed;
ALTER TABLE balda_schedule_runs DROP COLUMN closed_at;
