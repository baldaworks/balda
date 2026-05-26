-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_swarm_tasks_session_status
ON swarm_tasks(session_id, status, updated_at);

CREATE INDEX IF NOT EXISTS idx_swarm_tasks_status_updated
ON swarm_tasks(status, updated_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_swarm_tasks_status_updated;
DROP INDEX IF EXISTS idx_swarm_tasks_session_status;
-- +goose StatementEnd
