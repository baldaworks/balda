-- +goose Up
-- Preserve immutable catalog JSON and session pins. SQL records only the old
-- producer representation; runtime verifies its historical structural digest.
INSERT INTO balda_app_kv (namespace, key, value_json, updated_at)
SELECT 'balda.app',
    'runtime_catalog_configured_upgrade:' || (server->>'name') || ':' || (server->>'revision'),
    jsonb_build_object('version', 1, 'descriptor', min(server::text)::jsonb)::text,
    to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
FROM balda_app_kv AS snapshot
CROSS JOIN LATERAL jsonb_array_elements(
    CASE WHEN jsonb_typeof(snapshot.value_json::jsonb->'mcp_servers') = 'array'
        THEN snapshot.value_json::jsonb->'mcp_servers' ELSE '[]'::jsonb END
) AS servers(server)
WHERE snapshot.namespace = 'balda.app'
    AND left(snapshot.key, length('runtime_catalog_snapshot:')) = 'runtime_catalog_snapshot:'
    AND jsonb_typeof(server) = 'object'
    AND (SELECT count(*) FROM jsonb_object_keys(CASE WHEN jsonb_typeof(server) = 'object' THEN server ELSE '{}'::jsonb END)) = 4
    AND jsonb_typeof(server->'id') = 'object'
    AND (SELECT count(*) FROM jsonb_object_keys(CASE WHEN jsonb_typeof(server->'id') = 'object' THEN server->'id' ELSE '{}'::jsonb END)) = 3
    AND jsonb_typeof(server->'id'->'source') = 'object'
    AND (SELECT count(*) FROM jsonb_object_keys(CASE WHEN jsonb_typeof(server->'id'->'source') = 'object' THEN server->'id'->'source' ELSE '{}'::jsonb END)) = 2
    AND server->'id'->'source'->>'kind' = 'configured-mcp'
    AND server->'id'->>'kind' = 'mcp-server'
    AND jsonb_typeof(server->'name') = 'string'
    AND length(server->>'name') > 0
    AND jsonb_typeof(server->'id'->'source'->'name') = 'string'
    AND jsonb_typeof(server->'id'->'name') = 'string'
    AND server->'id'->'source'->>'name' = server->>'name'
    AND server->'id'->>'name' = server->>'name'
    AND jsonb_typeof(server->'revision') = 'string'
    AND server->>'revision' ~ '^[0-9a-f]{64}$'
    AND server->>'transport' IN ('stdio', 'streamable-http', 'sse')
    AND NOT server ? 'config_ref'
    AND NOT server ? 'targeting_known'
    AND NOT server ? 'target_provider_ids'
GROUP BY server->>'name', server->>'revision'
HAVING count(DISTINCT server->>'transport') = 1
ON CONFLICT (namespace, key) DO NOTHING;

-- +goose Down
DELETE FROM balda_app_kv
WHERE namespace = 'balda.app'
    AND left(key, length('runtime_catalog_configured_upgrade:')) = 'runtime_catalog_configured_upgrade:';
