-- +goose Up
-- +goose StatementBegin
-- Collaborators are stored in relay_app_kv with prefix "collaborator:userID"
-- No additional schema needed - uses existing KV store

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- +goose StatementEnd