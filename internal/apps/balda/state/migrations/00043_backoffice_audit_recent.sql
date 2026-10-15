-- +goose Up
CREATE INDEX idx_balda_security_audit_recent
    ON balda_security_audit_events (
        (substr(occurred_at,1,19) || '.' || substr((CASE WHEN substr(occurred_at,20,1)='.' THEN substr(occurred_at,21,instr(occurred_at,'Z')-21) ELSE '' END) || '000000000',1,9)) DESC,
        event_id DESC
    );

-- +goose Down
DROP INDEX idx_balda_security_audit_recent;
