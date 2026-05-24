-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_swarm_messages_dedupe;

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '' AND status != 'shadow';

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_shadow_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '' AND status = 'shadow';

INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES(14, datetime('now'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM schema_migrations WHERE version = 14;

DROP INDEX IF EXISTS idx_swarm_messages_shadow_dedupe;
DROP INDEX IF EXISTS idx_swarm_messages_dedupe;

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '';
-- +goose StatementEnd
