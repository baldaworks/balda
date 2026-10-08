-- +goose Up
ALTER TABLE balda_schedule_runs ADD COLUMN report_locator_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE balda_schedule_runs DROP COLUMN report_locator_ref;
