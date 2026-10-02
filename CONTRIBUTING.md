# Contributing

Thanks for contributing to `balda`.

## Development Setup

1. Use Go `1.26.6` (see `go.mod`).
2. Clone the repository and fetch dependencies:

```bash
go mod tidy
```

3. Run balda locally when needed:

```bash
go run ./cmd/balda --help
```

## Required Quality Gates

Run before opening or updating a PR:

```bash
go test -race ./...
go tool golangci-lint run
go tool go-arch-lint check --project-path .
```

## Database Integration Checks

State database integration suites are explicitly tagged and run separately:

```bash
go test -race -tags=integration,sqlite ./internal/apps/balda/state
BALDA_TEST_POSTGRES_DSN='postgres://postgres:test-only@localhost:5432/postgres?sslmode=disable' \
  go test -race -tags=integration,postgres ./internal/apps/balda/state
```

Use a disposable PostgreSQL database with permission to create/drop test schemas.
Each test gets an isolated schema and cleans it up. An explicit PostgreSQL run
fails if the DSN is missing. Both backends have separate required CI jobs;
configuration and dispatch unit tests remain untagged.

## Database Migrations

Create sequential SQL migration files with the project-pinned Goose CLI:

```bash
go tool goose -s -dir internal/apps/balda/state/migrations create <name> sql
go tool goose -s -dir internal/apps/balda/state/postgres_migrations create <name> sql
```

Use the SQLite or PostgreSQL command for the backend being changed. Fill in
the generated `Up` section. Choose a version above both SQL files and registered
Go migrations for that backend; the CLI's sequential allocator sees only files
in the selected directory. SQLite Go registrations are in
`state/00020_runtime_session_tables.go`; PostgreSQL Go registrations are local
to the provider in `state/postgres_migrations.go`. Keep PostgreSQL's global
registry disabled so SQLite callbacks cannot enter its history.

Use a transactional Go migration for a data upgrade that needs Go normalization.
Read and write through the callback's `*sql.Tx`, including data-conversion
markers; the Goose version marker commits in the same transaction. Schema and
data conversion belong to `internal/apps/balda/state`. Credential bootstrap
belongs to Backoffice and runs separately from database upgrades.

Validate both embedded SQL migration directories:

```bash
go tool goose -dir internal/apps/balda/state/migrations validate
go tool goose -dir internal/apps/balda/state/postgres_migrations validate
```

Balda applies migrations through the embedded Goose providers when it opens
the selected database. Run the tagged integration suite against a disposable
database before shipping a migration.

## Code Standards

- Follow idiomatic Go and Google Go best practices.
- Prefer project-local tooling via `go tool ...` when available.
- Use Conventional Commits for commit messages.
- Sync shared branches with merge (`git pull --no-rebase`), not rebase.
- Keep Balda actor execution on generic `github.com/baldaworks/go-actorlayer`. Do not add extra adapter or runtime-provider selector packages around it.
- `balda.provider` is the single app-scoped provider runtime for Balda sessions; actor code must not choose providers.

## Logging Policy

- Allowed: `github.com/rs/zerolog`, `log/slog`.
- Disallowed: `logrus`, `zap`, direct standard `log` usage.
- Initialize logging through `internal/logging.Init()`.
- Prefer structured fields over formatted strings.

## Documentation Policy

- Keep `README.md` focused on installation and usage.
- Keep `docs/balda.md` as the technical-reference entry point and put detailed
  contracts in the relevant page under `docs/reference/`.
- Keep `AGENTS.md` focused on agent workflow guardrails.
- If bot commands or config contracts change, update `README.md` and the
  relevant technical-reference page.
