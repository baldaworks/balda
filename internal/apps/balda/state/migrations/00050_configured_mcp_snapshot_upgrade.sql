-- +goose Up
-- Retained catalog IDs are content-derived: never rewrite their JSON or session
-- pins. Mark only the exact pre-targeting configured descriptor representation.
-- Runtime validates these markers against the historical structural digest;
-- SQL has no deployment configuration or historical private binding values.
INSERT OR IGNORE INTO balda_app_kv (namespace, key, value_json, updated_at)
SELECT 'balda.app',
    'runtime_catalog_configured_upgrade:' || json_extract(server.value, '$.name') || ':' || json_extract(server.value, '$.revision'),
    json_object('version', 1, 'descriptor', json(min(server.value))),
    strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
FROM balda_app_kv AS snapshot,
    json_each(CASE WHEN json_valid(snapshot.value_json) THEN snapshot.value_json ELSE '{}' END, '$.mcp_servers') AS server
WHERE snapshot.namespace = 'balda.app'
    AND substr(snapshot.key, 1, length('runtime_catalog_snapshot:')) = 'runtime_catalog_snapshot:'
    AND json_type(snapshot.value_json, '$.mcp_servers') = 'array'
    AND server.type = 'object'
    AND (SELECT count(*) FROM json_each(server.value)) = 4
    AND (SELECT count(*) FROM json_each(server.value, '$.id')) = 3
    AND (SELECT count(*) FROM json_each(server.value, '$.id.source')) = 2
    AND json_extract(server.value, '$.id.source.kind') = 'configured-mcp'
    AND json_extract(server.value, '$.id.kind') = 'mcp-server'
    AND json_type(server.value, '$.name') = 'text'
    AND length(json_extract(server.value, '$.name')) > 0
    AND json_extract(server.value, '$.id.source.name') = json_extract(server.value, '$.name')
    AND json_extract(server.value, '$.id.name') = json_extract(server.value, '$.name')
    AND json_type(server.value, '$.revision') = 'text'
    AND length(json_extract(server.value, '$.revision')) = 64
    AND json_extract(server.value, '$.revision') NOT GLOB '*[^0-9a-f]*'
    AND json_extract(server.value, '$.transport') IN ('stdio', 'streamable-http', 'sse')
    AND json_type(server.value, '$.config_ref') IS NULL
    AND json_type(server.value, '$.targeting_known') IS NULL
    AND json_type(server.value, '$.target_provider_ids') IS NULL
GROUP BY json_extract(server.value, '$.name'), json_extract(server.value, '$.revision')
HAVING count(DISTINCT json_extract(server.value, '$.transport')) = 1;

-- +goose Down
-- Original snapshots and history remain untouched. Rolling back the binary
-- requires restoring the pre-upgrade backup, not discarding session pins.
DELETE FROM balda_app_kv
WHERE namespace = 'balda.app'
    AND substr(key, 1, length('runtime_catalog_configured_upgrade:')) = 'runtime_catalog_configured_upgrade:';
