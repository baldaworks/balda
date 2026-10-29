-- +goose Up
ALTER TABLE balda_session_metadata ADD COLUMN requested_model TEXT NULL
 CHECK(requested_model IS NULL OR (length(CAST(requested_model AS BLOB)) BETWEEN 1 AND 512 AND trim(requested_model) = requested_model));
ALTER TABLE balda_runtime_operations ADD COLUMN target_requested_model TEXT NULL
 CHECK(target_requested_model IS NULL OR (length(CAST(target_requested_model AS BLOB)) BETWEEN 1 AND 512 AND trim(target_requested_model) = target_requested_model));

-- Existing effective_model and target_model bytes are compatibility evidence,
-- never an implicit requested preference. Deliberately perform no backfill.

-- +goose Down
-- Preference-aware transition evidence is forward-only. Restore a validated
-- backup to downgrade after lifetime_preference has been written.
SELECT * FROM balda_requested_model_downgrade_requires_database_backup;
