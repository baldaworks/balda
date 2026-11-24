-- +goose Up
ALTER TABLE balda_scheduled_jobs
    ADD COLUMN keep_session INTEGER NOT NULL DEFAULT 0 CHECK (keep_session IN (0, 1));

-- +goose Down
ALTER TABLE balda_scheduled_jobs DROP COLUMN keep_session;
