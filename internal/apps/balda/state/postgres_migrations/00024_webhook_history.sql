-- +goose Up
ALTER TABLE balda_webhook_admissions ADD COLUMN raw_body TEXT;
ALTER TABLE balda_webhook_admissions ADD COLUMN source TEXT NOT NULL DEFAULT 'external'
    CHECK (source IN ('external', 'test'));

-- Earlier UTC RFC3339Nano values had variable fractional width, which does not
-- sort chronologically as text. Preserve their precision with fixed-width UTC.
UPDATE balda_webhook_admissions SET created_at =
    CASE WHEN position('.' IN created_at) = 0
        THEN left(created_at, 19) || '.000000000Z'
        ELSE left(created_at, 19) || '.' ||
            rpad(split_part(split_part(created_at, '.', 2), 'Z', 1), 9, '0') || 'Z'
    END
    WHERE created_at LIKE '%Z';

CREATE INDEX idx_balda_webhook_admissions_history
    ON balda_webhook_admissions (route_name, created_at DESC, job_id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_balda_webhook_admissions_history;
ALTER TABLE balda_webhook_admissions DROP COLUMN source;
ALTER TABLE balda_webhook_admissions DROP COLUMN raw_body;
