-- +goose Up
-- +goose StatementBegin
ALTER TABLE relay_session_metadata ADD COLUMN user_id TEXT NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE relay_session_metadata DROP COLUMN user_id;
-- +goose StatementEnd
