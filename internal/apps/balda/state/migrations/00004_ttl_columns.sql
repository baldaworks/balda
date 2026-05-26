-- +goose Up
-- +goose StatementBegin
ALTER TABLE relay_app_kv ADD COLUMN expires_at TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE relay_app_kv DROP COLUMN expires_at;
-- +goose StatementEnd