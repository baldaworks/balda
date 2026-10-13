-- +goose Up
-- Bootstrap only the primary administrator whose browser credential is disabled.
-- The initial password is superusersuperuser; restricted login requires replacement.
INSERT INTO balda_security_audit_events
    (event_id, action, target_type, target_id, outcome, reason, source, occurred_at)
SELECT 'administrator-password-migration:' || user_id, 'user.credential.changed', 'user', user_id,
    'succeeded', 'initial temporary administrator password', 'administrator-password-migration', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
FROM balda_users
WHERE is_primary = 1 AND role = 'administrator' AND status = 'active' AND credential_state = 'disabled';

UPDATE balda_users
SET password_hash = '$2b$12$KOpef/bDTHXwKzrcXhJeh.bYRrsGF2MBA6MuQVyak6MO0ZUy7qyZm',
    credential_state = 'temporary', must_change = 1,
    credential_version = credential_version + 1, version = version + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE is_primary = 1 AND role = 'administrator' AND status = 'active' AND credential_state = 'disabled';

-- +goose Down
-- Credential changes are forward-only. Restore a database backup to downgrade.
SELECT * FROM balda_administrator_password_downgrade_requires_database_backup;
