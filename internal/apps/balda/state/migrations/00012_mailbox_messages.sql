-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS balda_mailbox_messages (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id TEXT NOT NULL UNIQUE,
    mailbox_id TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_key TEXT NOT NULL,
    subject TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    status TEXT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    available_at TEXT NOT NULL,
    claimed_at TEXT NOT NULL DEFAULT '',
    completed_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_balda_mailbox_messages_idempotency
    ON balda_mailbox_messages(mailbox_id, idempotency_key)
    WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS idx_balda_mailbox_messages_pending
    ON balda_mailbox_messages(mailbox_id, status, available_at, sequence);

CREATE INDEX IF NOT EXISTS idx_balda_mailbox_messages_status
    ON balda_mailbox_messages(status, updated_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_balda_mailbox_messages_status;
DROP INDEX IF EXISTS idx_balda_mailbox_messages_pending;
DROP INDEX IF EXISTS idx_balda_mailbox_messages_idempotency;
DROP TABLE IF EXISTS balda_mailbox_messages;
-- +goose StatementEnd
