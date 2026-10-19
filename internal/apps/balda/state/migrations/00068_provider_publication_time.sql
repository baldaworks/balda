-- +goose Up
ALTER TABLE balda_questions ADD COLUMN provider_created_at TEXT NOT NULL DEFAULT '';
ALTER TABLE execution_delivery_outbox ADD COLUMN provider_created_at TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE execution_delivery_outbox DROP COLUMN provider_created_at;
ALTER TABLE balda_questions DROP COLUMN provider_created_at;
