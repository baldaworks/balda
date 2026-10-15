-- +goose Up
ALTER TABLE balda_webhook_admissions ADD COLUMN raw_body TEXT;
ALTER TABLE balda_webhook_admissions ADD COLUMN source TEXT NOT NULL DEFAULT 'external'
    CHECK (source IN ('external', 'test'));

-- Earlier UTC RFC3339Nano values had variable fractional width, which does not
-- sort chronologically as text. Preserve their precision with fixed-width UTC.
UPDATE balda_webhook_admissions SET created_at =
    CASE WHEN instr(created_at, '.') = 0
        THEN substr(created_at, 1, 19) || '.000000000Z'
        ELSE substr(created_at, 1, 19) || '.' ||
            substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z'
    END
    WHERE created_at LIKE '%Z';

CREATE INDEX idx_balda_webhook_admissions_history
    ON balda_webhook_admissions (route_name, created_at DESC, job_id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_balda_webhook_admissions_history;
ALTER TABLE balda_webhook_admissions DROP COLUMN source;
ALTER TABLE balda_webhook_admissions DROP COLUMN raw_body;
