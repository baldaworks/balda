-- +goose Up
-- +goose StatementBegin
ALTER TABLE balda_session_metadata ADD COLUMN channel_type TEXT NOT NULL DEFAULT 'telegram';
ALTER TABLE balda_session_metadata ADD COLUMN address_key TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_session_metadata ADD COLUMN address_json TEXT NOT NULL DEFAULT '{}';

UPDATE balda_session_metadata
SET
    address_key = CAST(chat_id AS TEXT) || ':' || CAST(topic_id AS TEXT),
    address_json = json_object('chat_id', chat_id, 'topic_id', topic_id)
WHERE channel_type = 'telegram'
  AND (address_key = '' OR address_json = '{}');

UPDATE balda_session_metadata
SET session_id = 'tg-' || CAST(chat_id AS TEXT) || '-' || CAST(topic_id AS TEXT)
WHERE session_id LIKE 'balda-%';

UPDATE balda_session_metadata
SET branch_name = 'norma/balda/' || session_id
WHERE branch_name LIKE 'norma/balda/balda-%';

CREATE UNIQUE INDEX IF NOT EXISTS idx_balda_session_metadata_channel_address
    ON balda_session_metadata(channel_type, address_key);

INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES(2, datetime('now'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM schema_migrations WHERE version = 2;
DROP INDEX IF EXISTS idx_balda_session_metadata_channel_address;
-- +goose StatementEnd
