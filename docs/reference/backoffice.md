# Backoffice

Backoffice is a separate stateless browser application built from
`cmd/backoffice`. Balda Core owns all durable state, migrations, authorization,
audit policy, and product operations. The two processes communicate only over
the private Management API using the generated OpenAPI client, over loopback TCP
or a Unix socket.

The browser never receives the operator token. It stores opaque WebAuth access,
refresh, and CSRF cookies issued by Core. AI Session identifiers belong only to
assistant conversations and are never accepted as browser credentials.

## Build and configuration

Build Core and Backoffice from the same revision:

```bash
go build -trimpath -o ./bin/balda ./cmd/balda
go build -trimpath -o ./bin/balda-backoffice ./cmd/backoffice
```

Configure a loopback Management API and a shared operator token:

```yaml
balda:
  http:
    listen_addr: 127.0.0.1:8094
    base_url: https://balda.example.com
    base_path: /balda
  management:
    enabled: true
    listen_addr: 127.0.0.1:8096
    operator_token: "${BALDA_MANAGEMENT_OPERATOR_TOKEN}"
  backoffice:
    listen_addr: 127.0.0.1:8095
    access_token_ttl: 15m
    refresh_token_ttl: 720h
    ceremony_ttl: 5m
```

`balda.http.listen_addr` belongs to Core webhook and gateway ingress.
`balda.backoffice.listen_addr` belongs only to the standalone browser process.
`balda.http.base_url` and `base_path` define canonical public URLs for both.
With the example above, proxy `/balda/backoffice/` to `127.0.0.1:8095` and
Core's `/balda/webhooks/` and `/balda/gateway/` paths to `127.0.0.1:8094`.

For TCP, the Management listener must use an explicit loopback IP. The token can come
from config, profile, `${VAR}` expansion, or
`BALDA_MANAGEMENT_OPERATOR_TOKEN`; normal mapstructure precedence applies.

For separate containers on the same host, set
`balda.management.socket_path: /run/balda-management/core.sock` in both processes.
This canonical absolute path selects Unix transport instead of `listen_addr`;
Core does not also bind TCP, and Backoffice never falls back to TCP or an
environment proxy. The operator token and all WebAuth/CSRF checks still apply.

Core creates a missing immediate parent directory with mode `0700`; an existing
parent must already be a real directory owned by its user with that mode.
Core creates the socket with mode `0600`. Shared-volume deployments therefore
run both processes under the same UID and mount only the socket directory into
Backoffice. That mount may be read-only: it prevents filesystem changes, not
authenticated API mutations. Do not share Core's database or workspace.

The listener retains a private `.lock` file next to the socket to serialize
ownership across processes. It refuses active or foreign endpoints and only
removes an owned stale socket after a refused connection. Graceful shutdown
cleans its own endpoint without deleting a replacement; the lock file remains
for subsequent starts. Unix listener support requires a Unix host; loopback TCP
remains available on other platforms.

## Deployment and cutover

Deploy a compatible pair in this order:

1. Run Core `validate` and `preflight` with Management enabled.
2. Start the new Core and wait for normal startup completion.
3. Run `balda-backoffice validate`; this performs no network I/O.
4. Run `balda-backoffice preflight`; it verifies Core reachability, operator
   authentication, contract ID, and API major before any browser listener binds.
5. Start `balda-backoffice` and switch the reverse-proxy browser route.
6. Verify login and one guarded read and mutation.

```bash
./bin/balda --config-dir /config validate
./bin/balda --config-dir /config preflight
./bin/balda start --config-dir /config

./bin/balda-backoffice --config-dir /config validate
./bin/balda-backoffice --config-dir /config preflight
./bin/balda-backoffice --config-dir /config start
```

There is no embedded `/backoffice` fallback in Core. A failed compatibility or
operator check prevents the standalone process from binding its listener.

## Rollback

Rollback is binary/configuration-only:

1. Remove browser traffic from the failing Backoffice process and stop it.
2. Restore the previous compatible `balda` and `balda-backoffice` binaries and
   their matching configuration.
3. Start Core, run Backoffice preflight, start Backoffice, then restore browser
   traffic.

Do not restore or rewrite the database for this cutover. The extraction adds no
schema, dual-write, or state conversion. A database restore is appropriate only
for an unrelated data-recovery event.

## Security and state boundaries

- The operator token authenticates the Backoffice application, not a person.
- Core resolves the current WebAuth principal and permissions on every guarded
  request. Reads require operator + access; mutations also require CSRF.
- Refresh uses operator + refresh + CSRF and is never automatically retried.
- Login throttling is owned by the public Backoffice edge.
- Secrets are write-only; responses and logs use bounded safe errors.
- Backoffice has no database driver, migration, Core state import, AI runtime,
  or feature implementation dependency. The architecture test enforces this.
- WebAuth Session lifecycle is independent from AI Session lifecycle.

Core exposes explicit endpoints for users/access, audit, bindings, Providers,
MCP, schedules, webhooks, aliases, hooks, plugins, skills, system views, fact
memory, and AI Session archives. Backoffice uses only the generated client; it
does not implement generic CRUD or product policy.

## Administrator maintenance

Offline user maintenance remains in `cmd/balda` because Core owns the database:

```bash
balda backoffice bootstrap-admin
balda backoffice reset-password
balda backoffice recover-2fa --username superuser --confirm
```

These commands open Core state directly and do not start an HTTP server. The
standalone Backoffice process never opens the database.

## Local QA

The explicit preview command serves the real standalone browser edge with a
synthetic in-memory service on loopback only:

```bash
balda backoffice qa serve --listen 127.0.0.1:8096
./scripts/qa/run-backoffice-e2e.sh --no-system-install
```

See [Backoffice UI review](backoffice-ui-review.md) for the smoke-test contract.
