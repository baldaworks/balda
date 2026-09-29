-- +goose Up
-- Preserve the primary administrator's ID, credential, role, and bindings.
UPDATE balda_users
SET username = 'superuser',
    normalized_username = 'superuser',
    display_name = 'superuser',
    version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE is_primary = 1 AND (
    username <> 'superuser' OR normalized_username <> 'superuser' OR display_name <> 'superuser'
);

-- +goose Down
-- The prior primary login and display names cannot be recovered.
