-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_swarm_messages_dedupe;

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '' AND status != 'shadow';

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_shadow_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '' AND status = 'shadow';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_swarm_messages_shadow_dedupe;
DROP INDEX IF EXISTS idx_swarm_messages_dedupe;

CREATE UNIQUE INDEX IF NOT EXISTS idx_swarm_messages_dedupe
ON swarm_messages(mailbox, dedupe_key)
WHERE dedupe_key IS NOT NULL AND dedupe_key != '';
-- +goose StatementEnd
