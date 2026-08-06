# Session-memory acceptance and operations

This matrix records positive evidence for the approved extraction-ready
session-memory design. It is not a plan for a remote service. No test is added
to assert that removed symbols, old table names, or legacy MCP names are absent.

## Ownership and restart invariants

| Contract | Owner | Positive evidence |
| --- | --- | --- |
| Exact locator and personal/group isolation | `sessionmemory`, Balda `locatorref`, `sessionmemoryapp` | Scope validation, broker-bound context tests, and foreign-scope recall rejection |
| Canonical identity, CAS, idempotent outcome, provenance | `sessionmemory` + `sessionmemory/store/badger` | Canonical processor, operation, reader, replay, integrity, and reopen tests |
| Durable ingress before publish | Balda `sessionmemoryapp` + `eventbus/nats` | Ingress outbox FIFO/lease/audit tests and existing worker contract tests |
| Rebuildable lexical candidates | `sessionmemory/index/bleve` | Projection apply, metadata filtering, generation/reopen tests |
| Canonical recall truth | `sessionmemory/app` | Hydration, active/lifecycle, as-of/expiry, sensitivity, evidence, scope, and lag tests |
| User-facing boundary | `sessionmemory/mcp` | Neutral tool registration, bounded inputs/outputs, scope injection, and untrusted-reference tests |
| Authenticated Balda context | `internal/apps/balda/sessionmemorymcp` | Broker binding, header overwrite, and invalid-context tests |
| Global explicit facts | `internal/apps/balda/memory` | Positive `balda.memory.read`/`balda.memory.remember` store and MCP tests |

## Requirement evidence

| Requirement | Evidence location | Gate |
| --- | --- | --- |
| REQ-OWN-001 semantic ownership | `sessionmemory` canonical processor and `sessionmemory/app` turn/boundary services | `go test -race ./sessionmemory/...` |
| REQ-API-002 narrow capabilities | `sessionmemory/app/ports.go`, `runtime.go`, Balda composition | full race test and architecture lint |
| REQ-STO-003 independent adapters | `sessionmemory/store/badger`, `sessionmemory/index/bleve` | adapter race tests and Badger reopen/Bleve rebuild positives |
| REQ-MCP-004 neutral search/trace | `sessionmemory/mcp/mcp.go`, `mcp_test.go` | MCP contract tests |
| REQ-HOST-005 host delivery ownership | Balda `sessionmemorycmd`, `sessionmemoryapp`, `eventbus/nats`, and state ingress outbox | Balda race tests and outbox positives |
| REQ-LEG-006 legacy removal and migration | `state/migrations/00033_drop_legacy_session_memory.sql`, public runtime composition | positive ingress/audit migration tests and architecture lint |
| REQ-DOC-007 package and compatibility docs | this matrix, `README.md`, `docs/balda.md`, `session-memory-native.md` | documentation review |
| REQ-IND-008 portable dependency boundary | `.go-arch-lint.yml`, public package imports | `go tool go-arch-lint check --project-path .` |
| REQ-COMP-009 canonical behavior continuity | core, app, Badger, Bleve, forget, recall, and lifecycle tests | `go test -race ./...` |
| REQ-ENF-010 component enforcement | `.go-arch-lint.yml` session-memory components and vendor policy | architecture lint |
| REQ-QUAL-011 current positive contract only | repository test suite; no blacklist/compatibility tests | all required gates |

## Data and lifecycle invariants

- Badger is the canonical source of truth. Bleve is a disposable lexical
  projection and may be rebuilt without changing logical memory state.
- `balda.session_memory` configuration, canonical/projection paths, NATS
  subjects, and startup ordering remain stable.
- SQLite migrations `00030`–`00032` continue to support ingress outbox and
  audit behavior. Migration `00033` targets only the six unsupported `00029`
  domain tables, drops them child-first, and recreates them empty only in
  `Down`; dropped rows are unrecoverable.
- The migration does not touch `internal/apps/balda/memory`, generic SQLite KV
  state, `memory/global`, `MEMORY.md` import, or ingress/audit tables.
- If canonical Badger state is absent, the enabled runtime opens empty. No
  SQLite-to-Badger importer or migration coordinator runs.
- Forget remains a trusted Go capability. Logical denial precedes projection
  cleanup, and recall rejects denied, stale, foreign, or out-of-scope records.
- The bundled MCP server exposes only `session_memory.search` and
  `session_memory.trace`; it does not expose ingest or forget mutation tools.

## Operational sequence

Startup follows the repository lifecycle: load configuration, start the bundled
MCP lifecycle, start the enabled session-memory runtime, start the Balda
provider, then start channel and other ingress runtimes. Disabled session memory
opens no canonical, projection, stream, or worker resource.

Shutdown stops ingress and durable transport in the existing order, publishes
required session boundaries while transport is live, drains per-scope work,
then closes projection, canonical Badger, and model resources. Projection files
are rebuildable maintenance state and are not logical export/import data.

The operator runbook in [`docs/balda.md`](../balda.md#operator-verification-runbook)
uses metadata-only live checks when credentials and a real deployment are
available. It does not replace the deterministic repository gates and must not
record message bodies, recalled text, credentials, or capability URLs.

## Required checks

Run from the repository root before handoff:

```text
go test -race ./...
go tool golangci-lint run
go tool go-arch-lint check --project-path .
git diff --check
bd lint
```

Focused positive checks for the session-memory increment are:

```text
go test -race ./sessionmemory/...
go test -race ./internal/apps/balda/sessionmemoryapp/...
go test -race ./internal/apps/balda/sessionmemorymcp/...
go test -race ./internal/apps/balda/state -run 'TestSQLiteSessionMemoryIngressOutbox'
go test -race ./internal/apps/balda/memory/...
```

These checks validate current canonical, adapter, ingress, authentication, and
global-fact behavior. They do not test a remote session-memory service because
no such service is part of this story.
