-- +goose Up
ALTER TABLE balda_webhook_admissions ALTER COLUMN raw_body TYPE BYTEA
    USING convert_to(raw_body, 'UTF8');

-- +goose Down
ALTER TABLE balda_webhook_admissions ALTER COLUMN raw_body TYPE TEXT
    USING convert_from(raw_body, 'UTF8');
