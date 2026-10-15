-- +goose Up
-- Rebuild the table because SQLite cannot drop the inline user_id uniqueness.
CREATE TABLE balda_user_bindings_new (
    binding_id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    channel_type TEXT NOT NULL
        CHECK (channel_type <> '' AND channel_type = lower(trim(channel_type))),
    principal TEXT NOT NULL CHECK (principal <> '' AND principal = trim(principal)),
    display_name TEXT NOT NULL DEFAULT '',
    provenance TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL CHECK (created_at <> ''),
    updated_at TEXT NOT NULL CHECK (updated_at <> ''),
    UNIQUE (channel_type, principal),
    FOREIGN KEY (user_id) REFERENCES balda_users(user_id) ON DELETE CASCADE
);

INSERT INTO balda_user_bindings_new
    (binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at)
SELECT binding_id, user_id, channel_type, principal, display_name, provenance, created_at, updated_at
FROM balda_user_bindings;

DROP TABLE balda_user_bindings;
ALTER TABLE balda_user_bindings_new RENAME TO balda_user_bindings;
CREATE INDEX idx_balda_user_bindings_user ON balda_user_bindings(user_id, channel_type, principal);

-- +goose Down
-- A single-binding schema cannot represent users with multiple bindings.
-- Restore a pre-migration database backup when rolling back the binary.
