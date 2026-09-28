-- +goose Up
-- Preserve the primary administrator's identity, binding, credential, and sessions.
UPDATE balda_users
SET username = 'superuser',
    normalized_username = 'superuser',
    version = version + 1,
    updated_at = to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
WHERE is_primary = 1 AND normalized_username <> 'superuser';

-- +goose Down
-- The prior login name cannot be recovered after this forward data migration.
