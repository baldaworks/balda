---
title: Balda state database
description: Select, back up, and restore the database shared by Balda and Backoffice.
category: Storage
audience: operator
status: stable
outline: deep
---

# Balda state database

`balda.database` selects the database for the existing Balda state provider.
SQLite is the default; PostgreSQL is optional. This is not a separate service
or a Backoffice database. Start either configuration with `balda start`.

## SQLite (default)

```yaml
balda:
  state_dir: .config/balda
  database:
    type: sqlite
    sqlite:
      path: '{{.StateDir}}/state.db'
```

Omitting the entire `database` section opens the same existing `state.db`.
Existing owner and collaborator identities, sessions, jobs, plugins, KV and
polling offsets remain in that file. Historical SQLite migrations are retained
and applied forward in place; initialization does not replace the database.

`state_dir` is resolved relative to `balda.working_dir` (process CWD by default).
The SQLite path is a strict, one-pass Go text template. Only `.StateDir`, already
absolute, is available: no functions, environment lookup, branches, or other
fields. Unknown fields, malformed templates and empty paths fail startup.
Rendered relative paths are anchored to the working directory and cleaned.
For example, `{{.StateDir}}/databases/runtime.db` selects a custom file.

SQLite is intended for one Balda process on local persistent storage. The bot
and embedded Backoffice share its provider. Do not run multiple active Balda
processes, share the file across hosts, or place it on a network filesystem.
It uses foreign keys, WAL where available, an immediate transaction lock for
writes, a bounded busy timeout, and one pooled connection.

Native absolute paths are supported on Linux, macOS and Windows, including
filenames containing spaces, `#` or `%`. Balda escapes the path as a local SQLite
file URI; do not pre-encode it in configuration.

## PostgreSQL

Provision a dedicated database and login externally, then configure:

```yaml
balda:
  database:
    type: postgres
    postgres:
      host: db.example.internal
      port: 5432
      name: balda
      user: balda
      password: '' # Supply BALDA_DATABASE_POSTGRES_PASSWORD securely.
      sslmode: verify-full
```

The selected login must connect and create/alter tables, indexes and sequences
in its schema. Balda embeds its migrations and applies them before readiness.
It does not create or manage a PostgreSQL server or database. PostgreSQL 17 is
the integration-tested server major version. TLS trust must be configured for
`verify-full`; `disable` is suitable only for an explicitly trusted local setup.

The embedded PostgreSQL defaults are localhost, port 5432, database/user `balda`,
empty password, and `sslmode: disable`. Only the selected backend is validated.
All fields support standard environment overrides, for example
`BALDA_DATABASE_TYPE=postgres` and `BALDA_DATABASE_POSTGRES_HOST=db.example.internal`.
Passwords and generated connection strings are not logged. Driver errors expose
operation context and SQLSTATE rather than server details or credentials.

The pool is bounded to eight connections. Migrations use an advisory lock;
read/modify/write transactions use a transaction-scoped advisory lock to retain
the provider's serialized semantics across connections. PostgreSQL support does
not imply that multiple active Balda runtimes can safely share transports or
other local resources. `state_dir` is still needed for NATS and local artifacts.

## Commands and session persistence

`start`, `init`, `preflight`, `doctor`, Backoffice maintenance, and plugin
commands use the same selected backend whenever they access state. Opening it
applies embedded forward Goose schema and data migrations. `balda validate` is
different: it checks configuration and graph construction without opening, creating, or
migrating the database. `preflight` and `doctor` can still open and mutate state;
they are not read-only checks. Connection or migration failure prevents
startup. No fallback to SQLite, hot switching, or schema down command exists.

User conversion is part of those migrations on both backends: owner/collaborator
records become canonical users and bindings in the same transaction as the
Goose version marker. A fresh database needs no conversion; a completed
conversion preserves existing credentials and browser sessions. Invalid or
conflicting source data aborts the upgrade without a partial conversion.
A following SQL migration sets a temporary `superusersuperuser` password for the
primary administrator if its browser credential is disabled. First login requires
replacement; existing usable passwords are preserved. Other converted users retain
disabled browser credentials. See [Backoffice](backoffice.md) for CLI password reset.

`balda init` retains its refusal to overwrite an existing config. On a fresh
installation it honors effective database environment settings; keep those
settings in the deployment environment or configure the generated YAML before
subsequent commands.

`balda.sessions.persistence: sqlite` remains the existing name for durable
conversation persistence and uses the selected database, including PostgreSQL.
`memory` keeps conversation history process-local; other durable Balda state
still uses the selected database.

## Backup, restore and changing engines

Back up before an upgrade or configuration change. For SQLite, stop Balda and
use a consistent SQLite backup (or copy the stopped database and
any remaining WAL/SHM files together). Do not copy only a live WAL-mode main
file. For PostgreSQL, use your normal consistent `pg_dump`/restore or managed
backup procedure, including schema, data, sequences and Goose version history.
Restore into a stopped deployment, run `balda validate`, then start Balda and
verify Backoffice health and bot ingress. `balda start` applies schema
migrations and checks canonical users before listeners become ready. Schema
recovery is forward migration or backup restore, not automatic migration
downgrade.

Back up the deployment credential key securely alongside the database if
protected MCP values, worker grants, or managed provider values are stored.
Restore the matching
`balda.mcp_management.credential_key` (or
`BALDA_MCP_MANAGEMENT_CREDENTIAL_KEY`) before starting the restored deployment.
The database alone cannot decrypt that data. Retained MCP and provider
revisions need the same key even after a definition is edited or deleted. See
[MCP credential configuration](configuration.md#protected-values-and-worker-grants).

SQLite migration 67 and PostgreSQL migration 32 add managed provider records,
immutable revisions, one global provider state version, and the inline session
provider binding. They are additive and forward-only. Before rolling back to a
binary without managed providers, configure the complete selected root graph
in YAML and reset affected sessions; older binaries ignore the provider tables
but cannot reconstruct managed bindings. Schema downgrade is unsupported.

Changing `type` does **not** copy data between engines. An empty PostgreSQL
database starts empty: existing users do not appear there automatically.
Preserve the SQLite source and arrange an explicit, verified transfer before
switching an existing installation. This change provides backend selection,
not cross-engine migration tooling, dual writes or failover.

## Backend integration checks

See [Contributing](../../CONTRIBUTING.md) for separate `integration,sqlite` and
`integration,postgres` runs. PostgreSQL tests require an explicitly configured
disposable database and fail rather than skip when it is absent.

## Optional WebAuthn state and upgrades

Embedded Goose migrations SQLite 46 and PostgreSQL 11 add per-user MFA profiles,
public authenticator credentials and expiring one-use ceremonies, and extend
browser families with factor identity and verification time. They run through
the existing `state.Open` upgrade path; no separate user or MFA migration command
is needed. Existing users default to 2FA off. Password hashes, bot bindings and
valid pre-upgrade browser families are preserved. Private authenticator keys
never enter the database; pending ceremony/browser/CSRF selectors are digested.

Enable, replacement, disable and confirmed recovery advance user/credential/MFA
authority, revoke old families and append audit in one transaction. Counter
verification and family issuance are transactional as well. An audit or
SQL failure rolls back the corresponding security mutation.

These migrations are forward-only. Back up the database before upgrading.
Do not run an older binary against enrolled MFA state: it would not enforce the
new factor policy. A rollback needs a compatible binary or a validated backup
and explicit recovery plan; schema downgrade is unsupported. See
[Backoffice recovery](backoffice.md#optional-administrator-passkey-2fa).

## Durable chat archive and upgrades

The selected provider stores conversational archive lifetimes, original human
inputs and committed final replies in the same database as other Balda state.
SQLite migrations 65–66 and PostgreSQL migrations 30–31 add this storage and
final-replay claims. They are additive, forward-only Goose migrations applied
before ingress. Archive rows have no foreign-key cascade from live users,
bindings or ADK history. Reset, close, deleting live runtime history and database
reopen therefore preserve the archive, including with session persistence set
to `memory`. A new memory-process boot ends the earlier process-local lifetime;
exact durable restore continues its existing identity.

Capture starts at upgrade. Existing saved sessions are adopted lazily with
**Partial coverage**; their earlier messages are not reconstructed. Retention
is indefinite, with no automatic purge, export or deletion UI. Account for chat
text and sensitive historical metadata in disk capacity, access control and
backup retention. Attachments retain safe filename/kind labels only; file bytes,
private URLs and paths are excluded. Ordinary database backups above include
all archive rows and lifecycle journals; there is no separate transcript file.

An input write failure prevents turn publication. Failed final writes delay or
stop final publication with a bounded archival failure and never require a new
model invocation merely to rebuild history. A candidate held only in memory
before its first successful durable commit can be lost on process failure.
After successful commit, immutable final replay survives later bookkeeping or
publication failure. Reading or repairing archival state never sends a reply.
See [messaging](messaging.md#durable-final-reply-selection) for the boundary.

Pending or uncertain lifecycle records need investigation of the recorded
transition and the matching runtime epoch; they are not proof of an active
runtime. Normal preparation reconciles a proven ready epoch without replaying
completed hooks. Do not delete journals/archives or rerun an agent as an archive
repair procedure. Reopen after transient storage recovery; unresolved or
contradictory readiness fails explicitly. Operational logs identify operation
and bounded result, never transcript content. Rollback uses a compatible binary
or a validated pre-upgrade backup restored while stopped. Schema downgrade is
unsupported; running an older binary loses subsequent capture. Verify health,
normal ingress and archive access after the restore.
