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
applies embedded forward schema migrations. `balda validate` is different: it
checks configuration and graph construction without opening, creating, or
migrating the database. `preflight` and `doctor` can still open and mutate state;
they are not read-only checks. Connection or migration failure prevents
startup. No fallback to SQLite, hot switching, or schema down command exists.

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

Changing `type` does **not** copy data between engines. An empty PostgreSQL
database starts empty: existing users do not appear there automatically.
Preserve the SQLite source and arrange an explicit, verified transfer before
switching an existing installation. This change provides backend selection,
not cross-engine migration tooling, dual writes or failover.

## Backend integration checks

See [Contributing](../../CONTRIBUTING.md) for separate `integration,sqlite` and
`integration,postgres` runs. PostgreSQL tests require an explicitly configured
disposable database and fail rather than skip when it is absent.
