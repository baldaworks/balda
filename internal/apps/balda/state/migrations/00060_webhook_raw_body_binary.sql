-- +goose Up
-- Preserve admissions written after 00059 while allowing opaque request bytes.
ALTER TABLE balda_webhook_admissions RENAME COLUMN raw_body TO raw_body_text;
ALTER TABLE balda_webhook_admissions ADD COLUMN raw_body BLOB;
UPDATE balda_webhook_admissions SET raw_body = CAST(raw_body_text AS BLOB)
    WHERE raw_body_text IS NOT NULL;
ALTER TABLE balda_webhook_admissions DROP COLUMN raw_body_text;

-- +goose Down
ALTER TABLE balda_webhook_admissions RENAME COLUMN raw_body TO raw_body_blob;
ALTER TABLE balda_webhook_admissions ADD COLUMN raw_body TEXT;
UPDATE balda_webhook_admissions SET raw_body = CAST(raw_body_blob AS TEXT)
    WHERE raw_body_blob IS NOT NULL;
ALTER TABLE balda_webhook_admissions DROP COLUMN raw_body_blob;
