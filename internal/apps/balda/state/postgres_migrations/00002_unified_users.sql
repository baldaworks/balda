-- +goose Up
CREATE TABLE balda_users (
    user_id TEXT COLLATE "C" PRIMARY KEY,
    display_name TEXT COLLATE "C" NOT NULL CHECK (display_name <> ''),
    username TEXT COLLATE "C" NOT NULL CHECK (username <> ''),
    normalized_username TEXT COLLATE "C" NOT NULL UNIQUE
        CHECK (normalized_username <> '' AND normalized_username = lower(trim(normalized_username))),
    status TEXT COLLATE "C" NOT NULL CHECK (status IN ('active', 'disabled')),
    role TEXT COLLATE "C" NOT NULL CHECK (role IN ('administrator', 'operator')),
    password_hash TEXT COLLATE "C" NOT NULL DEFAULT '',
    credential_state TEXT COLLATE "C" NOT NULL CHECK (credential_state IN ('temporary', 'active', 'disabled')),
    must_change BIGINT NOT NULL CHECK (must_change IN (0, 1)),
    is_primary BIGINT NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    version BIGINT NOT NULL CHECK (version > 0),
    created_at TEXT COLLATE "C" NOT NULL CHECK (created_at <> ''),
    updated_at TEXT COLLATE "C" NOT NULL CHECK (updated_at <> ''),
    CHECK (
        (credential_state = 'temporary' AND must_change = 1 AND password_hash <> '') OR
        (credential_state = 'active' AND must_change = 0 AND password_hash <> '') OR
        (credential_state = 'disabled' AND must_change = 0)
    ),
    CHECK (is_primary = 0 OR role = 'administrator')
);

CREATE UNIQUE INDEX idx_balda_users_primary
    ON balda_users(is_primary)
    WHERE is_primary = 1;
CREATE INDEX idx_balda_users_status_role
    ON balda_users(status, role, user_id);

CREATE TABLE balda_user_bindings (
    binding_id TEXT COLLATE "C" PRIMARY KEY,
    user_id TEXT COLLATE "C" NOT NULL UNIQUE,
    channel_type TEXT COLLATE "C" NOT NULL
        CHECK (channel_type <> '' AND channel_type = lower(trim(channel_type))),
    principal TEXT COLLATE "C" NOT NULL CHECK (principal <> '' AND principal = trim(principal)),
    display_name TEXT COLLATE "C" NOT NULL DEFAULT '',
    provenance TEXT COLLATE "C" NOT NULL DEFAULT '',
    created_at TEXT COLLATE "C" NOT NULL CHECK (created_at <> ''),
    updated_at TEXT COLLATE "C" NOT NULL CHECK (updated_at <> ''),
    UNIQUE (channel_type, principal),
    FOREIGN KEY (user_id) REFERENCES balda_users(user_id) ON DELETE CASCADE
);

CREATE TABLE balda_user_binding_claims (
    claim_id TEXT COLLATE "C" PRIMARY KEY,
    user_id TEXT COLLATE "C" NOT NULL,
    channel_type TEXT COLLATE "C" NOT NULL CHECK (channel_type <> ''),
    expires_at TEXT COLLATE "C" NOT NULL CHECK (expires_at <> ''),
    consumed_at TEXT COLLATE "C" NOT NULL DEFAULT '',
    created_at TEXT COLLATE "C" NOT NULL CHECK (created_at <> ''),
    CHECK (consumed_at = '' OR consumed_at >= created_at),
    FOREIGN KEY (user_id) REFERENCES balda_users(user_id) ON DELETE CASCADE
);

CREATE INDEX idx_balda_user_binding_claims_expiry
    ON balda_user_binding_claims(expires_at, consumed_at);

CREATE TABLE balda_backoffice_sessions (
    session_id TEXT COLLATE "C" PRIMARY KEY,
    user_id TEXT COLLATE "C" NOT NULL,
    assurance TEXT COLLATE "C" NOT NULL CHECK (assurance IN ('restricted', 'normal')),
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    access_selector TEXT COLLATE "C" NOT NULL UNIQUE CHECK (access_selector <> ''),
    access_verifier_digest BYTEA NOT NULL CHECK (octet_length(access_verifier_digest) > 0),
    csrf_verifier_digest BYTEA NOT NULL CHECK (octet_length(csrf_verifier_digest) > 0),
    created_at TEXT COLLATE "C" NOT NULL CHECK (created_at <> ''),
    last_seen_at TEXT COLLATE "C" NOT NULL CHECK (last_seen_at <> ''),
    access_expires_at TEXT COLLATE "C" NOT NULL CHECK (access_expires_at <> ''),
    refresh_expires_at TEXT COLLATE "C" NOT NULL CHECK (refresh_expires_at <> ''),
    revoked_at TEXT COLLATE "C" NOT NULL DEFAULT '',
    revocation_reason TEXT COLLATE "C" NOT NULL DEFAULT '',
    version BIGINT NOT NULL CHECK (version > 0),
    CHECK (created_at <= last_seen_at),
    CHECK (access_expires_at < refresh_expires_at),
    CHECK (created_at < refresh_expires_at),
    CHECK ((revoked_at = '' AND revocation_reason = '') OR revoked_at <> ''),
    FOREIGN KEY (user_id) REFERENCES balda_users(user_id) ON DELETE CASCADE
);

CREATE INDEX idx_balda_backoffice_sessions_user
    ON balda_backoffice_sessions(user_id, revoked_at, refresh_expires_at);

-- +goose StatementBegin
CREATE FUNCTION balda_reject_session_refresh_expiry_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.refresh_expires_at <> OLD.refresh_expires_at THEN
        RAISE EXCEPTION 'session refresh expiry is immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_balda_backoffice_sessions_fixed_refresh_expiry
BEFORE UPDATE OF refresh_expires_at ON balda_backoffice_sessions
FOR EACH ROW EXECUTE FUNCTION balda_reject_session_refresh_expiry_change();

CREATE TABLE balda_backoffice_refresh_tokens (
    session_id TEXT COLLATE "C" NOT NULL,
    selector TEXT COLLATE "C" NOT NULL UNIQUE CHECK (selector <> ''),
    verifier_digest BYTEA NOT NULL CHECK (octet_length(verifier_digest) > 0),
    generation BIGINT NOT NULL CHECK (generation > 0),
    state TEXT COLLATE "C" NOT NULL CHECK (state IN ('active', 'used', 'revoked')),
    issued_at TEXT COLLATE "C" NOT NULL CHECK (issued_at <> ''),
    used_at TEXT COLLATE "C" NOT NULL DEFAULT '',
    expires_at TEXT COLLATE "C" NOT NULL CHECK (expires_at <> ''),
    PRIMARY KEY (session_id, generation),
    CHECK (
        (state = 'active' AND used_at = '') OR
        (state = 'used' AND used_at <> '') OR
        state = 'revoked'
    ),
    CHECK (issued_at < expires_at),
    CHECK (used_at = '' OR (used_at >= issued_at AND used_at <= expires_at)),
    FOREIGN KEY (session_id) REFERENCES balda_backoffice_sessions(session_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX idx_balda_backoffice_refresh_active
    ON balda_backoffice_refresh_tokens(session_id)
    WHERE state = 'active';
CREATE INDEX idx_balda_backoffice_refresh_expiry
    ON balda_backoffice_refresh_tokens(expires_at, session_id);

-- +goose StatementBegin
CREATE FUNCTION balda_validate_refresh_token_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    family_expiry TEXT;
BEGIN
    IF TG_OP = 'INSERT' THEN
		IF NEW.state <> 'active' THEN
			RAISE EXCEPTION 'refresh token must start active';
		END IF;
        SELECT refresh_expires_at INTO family_expiry
        FROM balda_backoffice_sessions
        WHERE session_id = NEW.session_id;
        IF NEW.expires_at <> family_expiry THEN
            RAISE EXCEPTION 'refresh token must inherit session expiry';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.session_id <> OLD.session_id OR
       NEW.selector <> OLD.selector OR
       NEW.verifier_digest <> OLD.verifier_digest OR
       NEW.generation <> OLD.generation OR
       NEW.issued_at <> OLD.issued_at OR
       NEW.expires_at <> OLD.expires_at THEN
        RAISE EXCEPTION 'refresh token identity and expiry are immutable';
    END IF;
    IF NOT (
        NEW.state = OLD.state OR
        (OLD.state = 'active' AND NEW.state IN ('used', 'revoked')) OR
        (OLD.state = 'used' AND NEW.state = 'revoked')
    ) THEN
        RAISE EXCEPTION 'invalid refresh token state transition';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER trg_balda_backoffice_refresh_mutation
BEFORE INSERT OR UPDATE ON balda_backoffice_refresh_tokens
FOR EACH ROW EXECUTE FUNCTION balda_validate_refresh_token_mutation();

CREATE TABLE balda_security_audit_events (
    event_id TEXT COLLATE "C" PRIMARY KEY,
    actor_user_id TEXT COLLATE "C" NOT NULL DEFAULT '',
    actor_session_id TEXT COLLATE "C" NOT NULL DEFAULT '',
    action TEXT COLLATE "C" NOT NULL CHECK (action <> ''),
    target_type TEXT COLLATE "C" NOT NULL CHECK (target_type <> ''),
    target_id TEXT COLLATE "C" NOT NULL CHECK (target_id <> ''),
    outcome TEXT COLLATE "C" NOT NULL CHECK (outcome IN ('succeeded', 'denied')),
    reason TEXT COLLATE "C" NOT NULL DEFAULT '',
    metadata_json TEXT COLLATE "C" NOT NULL DEFAULT '{}',
    request_id TEXT COLLATE "C" NOT NULL DEFAULT '',
    source TEXT COLLATE "C" NOT NULL CHECK (source <> ''),
    correlation_id TEXT COLLATE "C" NOT NULL DEFAULT '',
    occurred_at TEXT COLLATE "C" NOT NULL CHECK (occurred_at <> '')
);

CREATE INDEX idx_balda_security_audit_occurred
    ON balda_security_audit_events(occurred_at, event_id);
CREATE INDEX idx_balda_security_audit_target
    ON balda_security_audit_events(target_type, target_id, occurred_at);

CREATE TABLE balda_user_migrations (
    migration_id TEXT COLLATE "C" PRIMARY KEY,
    source_fingerprint TEXT COLLATE "C" NOT NULL UNIQUE CHECK (source_fingerprint <> ''),
    source_counts_json TEXT COLLATE "C" NOT NULL DEFAULT '{}',
    generated_user_count BIGINT NOT NULL CHECK (generated_user_count >= 0),
    generated_binding_count BIGINT NOT NULL CHECK (generated_binding_count >= 0),
    primary_user_id TEXT COLLATE "C" NOT NULL,
    completed_at TEXT COLLATE "C" NOT NULL CHECK (completed_at <> ''),
    FOREIGN KEY (primary_user_id) REFERENCES balda_users(user_id) ON DELETE RESTRICT
);

-- +goose Down
DROP TABLE IF EXISTS balda_user_migrations;
DROP TABLE IF EXISTS balda_security_audit_events;
DROP TABLE IF EXISTS balda_backoffice_refresh_tokens;
DROP FUNCTION IF EXISTS balda_validate_refresh_token_mutation();
DROP TABLE IF EXISTS balda_backoffice_sessions;
DROP FUNCTION IF EXISTS balda_reject_session_refresh_expiry_change();
DROP TABLE IF EXISTS balda_user_binding_claims;
DROP TABLE IF EXISTS balda_user_bindings;
DROP TABLE IF EXISTS balda_users;
