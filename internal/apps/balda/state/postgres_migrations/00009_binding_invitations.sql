-- +goose Up
CREATE TABLE balda_binding_invitations (
    invitation_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES balda_users(user_id) ON DELETE CASCADE,
    channel_type TEXT NOT NULL CHECK (channel_type IN ('telegram', 'slackagent', 'zulip', 'mattermost')),
    integration_key TEXT NOT NULL CHECK (integration_key <> ''),
    token_digest BYTEA NOT NULL UNIQUE CHECK (length(token_digest) = 32),
    issued_by TEXT NOT NULL REFERENCES balda_users(user_id),
    created_at TEXT NOT NULL CHECK (created_at <> ''),
    expires_at TEXT NOT NULL CHECK (expires_at <> ''),
    consumed_at TEXT NOT NULL DEFAULT '',
    revoked_at TEXT NOT NULL DEFAULT '',
    revocation_reason TEXT NOT NULL DEFAULT '',
    version BIGINT NOT NULL CHECK (version > 0),
    CHECK (consumed_at = '' OR revoked_at = ''),
    CHECK ((revoked_at = '' AND revocation_reason = '') OR (revoked_at <> '' AND revocation_reason <> ''))
);
CREATE UNIQUE INDEX idx_balda_binding_invitation_current
    ON balda_binding_invitations(user_id, channel_type, integration_key)
    WHERE consumed_at = '' AND revoked_at = '';

-- +goose Down
-- Forward-only: prior binaries ignore this additive table. Preserve confirmed bindings.
