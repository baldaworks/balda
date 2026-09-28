-- +goose Up
-- Preserve the primary administrator's identity, binding, credential, and sessions.
UPDATE balda_users
SET username = 'superuser',
    normalized_username = 'superuser',
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE is_primary = 1 AND normalized_username <> 'superuser';

-- +goose Down
-- The prior login name cannot be recovered after this forward data migration.
