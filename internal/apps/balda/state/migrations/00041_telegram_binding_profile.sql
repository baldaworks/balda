-- +goose Up
ALTER TABLE balda_user_bindings ADD COLUMN provider_username TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_user_bindings ADD COLUMN provider_first_name TEXT NOT NULL DEFAULT '';

UPDATE balda_user_bindings AS b
SET provider_username = COALESCE(
        (SELECT username FROM balda_collaborators WHERE user_id = 'telegram:' || b.principal),
        (SELECT username FROM balda_collaborators WHERE user_id = b.principal), ''),
    provider_first_name = COALESCE(
        (SELECT first_name FROM balda_collaborators WHERE user_id = 'telegram:' || b.principal),
        (SELECT first_name FROM balda_collaborators WHERE user_id = b.principal), '')
WHERE b.channel_type = 'telegram' AND b.provenance = 'legacy-collaborator';

-- +goose Down
-- Profile columns are retained to preserve verified provider metadata.
