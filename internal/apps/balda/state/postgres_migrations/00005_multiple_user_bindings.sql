-- +goose Up
ALTER TABLE balda_user_bindings DROP CONSTRAINT balda_user_bindings_user_id_key;
CREATE INDEX idx_balda_user_bindings_user ON balda_user_bindings(user_id, channel_type, principal);

-- +goose Down
-- A single-binding schema cannot represent users with multiple bindings.
-- Restore a pre-migration database backup when rolling back the binary.
