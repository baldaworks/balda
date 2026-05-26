-- +goose Up
-- +goose StatementBegin
INSERT OR IGNORE INTO balda_app_kv (namespace, key, value_json, expires_at, updated_at)
SELECT
	CASE namespace
		WHEN 'relay.app' THEN 'balda.app'
		WHEN 'relay.session_mcp' THEN 'balda.session_mcp'
		ELSE namespace
	END AS namespace,
	key,
	value_json,
	expires_at,
	updated_at
FROM balda_app_kv
WHERE namespace IN ('relay.app', 'relay.session_mcp');

DELETE FROM balda_app_kv
WHERE namespace IN ('relay.app', 'relay.session_mcp');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

INSERT OR IGNORE INTO balda_app_kv (namespace, key, value_json, expires_at, updated_at)
SELECT
	CASE namespace
		WHEN 'balda.app' THEN 'relay.app'
		WHEN 'balda.session_mcp' THEN 'relay.session_mcp'
		ELSE namespace
	END AS namespace,
	key,
	value_json,
	expires_at,
	updated_at
FROM balda_app_kv
WHERE namespace IN ('balda.app', 'balda.session_mcp');

DELETE FROM balda_app_kv
WHERE namespace IN ('balda.app', 'balda.session_mcp');
-- +goose StatementEnd
