-- +goose Up
ALTER TABLE balda_runtime_operations
    ADD COLUMN target_configuration_summary_json TEXT NOT NULL DEFAULT ''
    CHECK (octet_length(target_configuration_summary_json) <= 16384);

-- +goose Down
SELECT * FROM balda_configuration_summary_downgrade_requires_database_backup;
