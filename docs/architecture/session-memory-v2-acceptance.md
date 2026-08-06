# Session-memory v2 acceptance and operations

This matrix records evidence for the approved v2 design. It is a verification
map, not a claim that environment-dependent latency targets have already been
measured. The current scope covers logical fail-closed forgetting, retention,
projection removal, canonical Badger state, Bleve retrieval, and optional local
Vecgo retrieval. Removed key-management and encryption surfaces are not part
of this matrix.

## Ownership and restart invariants

| Contract | Owner | Positive evidence |
| --- | --- | --- |
| Exact locator and personal/group isolation | `sessionmemory`, `locatorref`, `sessionmemoryapp` | Scope validation, broker-bound MCP tests, foreign-scope recall rejection |
| Canonical identity, CAS, idempotent outcome, provenance | `state` Badger adapter | Canonical mutation, replay, integrity, provenance, and reopen tests |
| Durable ingress before publish | `sessionmemoryapp` ingress outbox + NATS adapter | Ingress outbox restart and PubAck integration tests |
| Mandatory lexical candidates | `state.BleveRecallProjection` | Russian/English analyzer, metadata filter, generation switch, reopen tests |
| Optional semantic candidates | `state.VecgoRecallProjection` | Disabled default, model/dimension pinning, dirty/commit/watermark/activation, reopen tests |
| Canonical recall truth | `sessionmemoryapp.RecallService` | Hydration, active/lifecycle, as-of/expiry, sensitivity, evidence, scope, and lag checks |
| User-facing boundary | `sessionmemorymcp` | Additive filters, compact evidence/explain, separate Trace, untrusted reference marker |

## Requirement evidence

| Requirement family | Evidence location | Gate |
| --- | --- | --- |
| State/event identity and temporal validity | `sessionmemory/v2.go`, reconciler and canonical tests | `go test -race ./sessionmemory/...` |
| Evidence and failed-turn capture | `sessionmemory/derived_validation.go`, capture and worker tests | focused session-memory race suite |
| Incremental canonical mutation and migration | `internal/apps/balda/state/badger_session_memory*.go` | Badger integrity, migration, replay, and reopen tests |
| Ordered processing and ingress durability | `sessionmemoryapp`, NATS adapter, runtime integration tests | `go test -race ./internal/apps/balda/...` |
| Bounded retrieval and structured filters | `sessionmemory/recall.go`, `sessionmemoryapp/recall.go`, Bleve/Vecgo adapters, MCP tests | recall race suite and full test gate |
| Rebuildable views and projection checkpoints | `sessionmemory/projection*.go`, Badger checkpoint tests | projection coordinator and checkpoint tests |
| Logical forget/retention and stale-hit fail-closed behavior | canonical logical/forget tests and RecallService tests | positive active/forgotten/sensitivity/scope gates |
| Untrusted recall and global-fact separation | MCP contract and global memory tests | MCP schema and architecture gates |

## Operational sequence

Startup follows the repository lifecycle: load configuration, start bundled
MCP lifecycle, open the single canonical Badger owner and rebuildable
projection handles, then start the Balda provider and channel runtime. Disabled
session memory opens no stream, worker, or projection files.

Shutdown stops ingress, drains per-scope work, commits pending projection
batches, records watermarks only after commit, closes projections, then closes
the canonical owner. A dirty generation is never activated after reopen; it is
discarded or rebuilt from canonical changes.

For rollback, disable the optional retrieval adapter or the whole session-memory
feature and restart. Canonical records and the global fact KV are not deleted.
Projection files are rebuildable maintenance state and are not part of logical
export/import.

## Required checks

Run from the repository root before handoff:

```text
go test -race ./...
go tool golangci-lint run
go tool go-arch-lint check --project-path .
git diff --check
```

Focused positive checks used for the current increment:

```text
go test -race -count=1 ./sessionmemory ./internal/apps/balda/sessionmemoryapp ./internal/apps/balda/sessionmemorymcp ./internal/apps/balda/state
go test ./...
```

The 2026-08-06 local benchmark shape recorded 100,000 one-scope commits at
approximately 2.515 ms/commit. The 50,000-mutation/128-scope run recorded
apply p95 759.7 ms and p99 879.7 ms, above the design targets of 25 ms and
100 ms. This is an explicit acceptance miss, not a pass; tuning and requirement
review are tracked in Beads issue `balda-tmb0`. Projection lag and restore-drill
targets still require a dedicated reference environment and remain unmeasured.
