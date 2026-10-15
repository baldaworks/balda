-- +goose Up
CREATE TABLE balda_mfa_credentials (
    factor_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES balda_users(user_id) ON DELETE CASCADE,
    rp_id TEXT NOT NULL CHECK (rp_id <> ''),
    credential_id BYTEA NOT NULL UNIQUE CHECK (octet_length(credential_id) BETWEEN 1 AND 1024),
    public_key BYTEA NOT NULL CHECK (octet_length(public_key) BETWEEN 1 AND 4096),
    credential_data BYTEA NOT NULL CHECK (octet_length(credential_data) BETWEEN 1 AND 65536),
    sign_count BIGINT NOT NULL CHECK (sign_count BETWEEN 0 AND 4294967295),
    backup_eligible BIGINT NOT NULL CHECK (backup_eligible IN (0,1)),
    backup_state BIGINT NOT NULL CHECK (backup_state IN (0,1)),
    created_at TEXT NOT NULL CHECK (created_at <> ''),
    last_used_at TEXT NOT NULL DEFAULT '',
    invalidated_at TEXT NOT NULL DEFAULT '',
    CHECK (backup_state = 0 OR backup_eligible = 1)
);
CREATE UNIQUE INDEX idx_balda_mfa_active_key ON balda_mfa_credentials(user_id) WHERE invalidated_at = '';
CREATE TABLE balda_mfa_profiles (
    user_id TEXT PRIMARY KEY REFERENCES balda_users(user_id) ON DELETE CASCADE,
    enabled BIGINT NOT NULL CHECK (enabled IN (0,1)),
    active_factor_id TEXT REFERENCES balda_mfa_credentials(factor_id),
    version BIGINT NOT NULL CHECK (version > 0),
    CHECK ((enabled = 0 AND active_factor_id IS NULL) OR (enabled = 1 AND active_factor_id IS NOT NULL))
);
CREATE TABLE balda_mfa_ceremonies (
    token_digest BYTEA PRIMARY KEY CHECK (octet_length(token_digest) = 32),
    browser_digest BYTEA NOT NULL CHECK (octet_length(browser_digest) = 32),
    csrf_digest BYTEA NOT NULL CHECK (octet_length(csrf_digest) = 32),
    user_id TEXT NOT NULL REFERENCES balda_users(user_id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('enable','login','step_up','replace','disable')),
    session_id TEXT NOT NULL DEFAULT '',
    user_version BIGINT NOT NULL CHECK (user_version > 0),
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    mfa_version BIGINT NOT NULL CHECK (mfa_version >= 0),
    ceremony_data BYTEA NOT NULL CHECK (octet_length(ceremony_data) BETWEEN 1 AND 32768),
    created_at TEXT NOT NULL CHECK (created_at <> ''),
    expires_at TEXT NOT NULL CHECK (expires_at <> ''),
    consumed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_balda_mfa_ceremony_expiry ON balda_mfa_ceremonies(expires_at);
ALTER TABLE balda_backoffice_sessions ADD COLUMN webauthn_verified_at TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_backoffice_sessions ADD COLUMN mfa_factor_id TEXT NOT NULL DEFAULT '';
-- +goose Down
-- Forward-only: preserve factor authority. Older binaries must not serve enrolled accounts.
