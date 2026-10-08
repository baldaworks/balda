-- +goose Up
ALTER TABLE execution_jobs ADD COLUMN webhook_turn_claimed_at TEXT NOT NULL DEFAULT '';
-- Existing active webhook runs may already have invoked the provider. Their
-- pre-upgrade publication state cannot prove otherwise, so fail closed.
UPDATE execution_jobs SET webhook_turn_claimed_at = updated_at
WHERE private_run_kind = 'webhook' AND status NOT IN ('completed', 'failed', 'canceled', 'deadlettered');

-- +goose Down
ALTER TABLE execution_jobs DROP COLUMN webhook_turn_claimed_at;
