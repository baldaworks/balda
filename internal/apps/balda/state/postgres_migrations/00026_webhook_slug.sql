-- +goose Up
DROP INDEX idx_balda_webhook_routes_active_path;
ALTER TABLE balda_webhook_routes DROP COLUMN path;

-- +goose Down
-- Route paths were intentionally discarded. Restore a database backup to downgrade.
SELECT * FROM balda_webhook_slug_downgrade_requires_database_backup;
