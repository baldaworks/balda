-- +goose Up
ALTER TABLE balda_security_audit_events
    RENAME COLUMN actor_session_id TO actor_web_auth_session_id;

-- +goose Down
ALTER TABLE balda_security_audit_events
    RENAME COLUMN actor_web_auth_session_id TO actor_session_id;
