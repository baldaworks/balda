-- +goose Up
ALTER TABLE balda_backoffice_sessions ADD COLUMN device_label TEXT;
ALTER TABLE balda_backoffice_sessions ADD COLUMN connection_peer TEXT;

-- +goose Down
ALTER TABLE balda_backoffice_sessions DROP COLUMN connection_peer;
ALTER TABLE balda_backoffice_sessions DROP COLUMN device_label;
