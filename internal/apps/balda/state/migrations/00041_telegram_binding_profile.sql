-- +goose Up
ALTER TABLE balda_user_bindings ADD COLUMN provider_username TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_user_bindings ADD COLUMN provider_first_name TEXT NOT NULL DEFAULT '';

-- +goose Down
-- Profile columns are retained to preserve verified provider metadata.
