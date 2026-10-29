-- +goose Up
ALTER TABLE balda_session_metadata ADD COLUMN requested_model TEXT NULL
 CHECK(requested_model IS NULL OR (octet_length(requested_model) BETWEEN 1 AND 512 AND btrim(requested_model) = requested_model));
ALTER TABLE balda_runtime_operations ADD COLUMN target_requested_model TEXT NULL
 CHECK(target_requested_model IS NULL OR (octet_length(target_requested_model) BETWEEN 1 AND 512 AND btrim(target_requested_model) = target_requested_model));

-- Existing effective_model and target_model bytes are compatibility evidence,
-- never an implicit requested preference. Deliberately perform no backfill.

-- +goose Down
-- Preference-aware transition evidence is forward-only. Restore a validated
-- backup to downgrade after lifetime_preference has been written.
SELECT * FROM balda_requested_model_downgrade_requires_database_backup;
