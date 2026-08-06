# Session memory

`github.com/normahq/balda/sessionmemory` is the extraction-ready, storage-neutral
core for durable, provenance-grounded session memory. It is versioned with
Balda in this repository; this story does not create a nested module, a remote
service, or a network control API.

The core owns the semantic contract after a host supplies a validated turn or
boundary:

- candidate extraction and evidence grounding;
- temporal interpretation and state/event reconciliation;
- exact-scope identity, idempotent operation outcomes, and provenance;
- bounded recall and trace validation; and
- source/scope forgetting with fail-closed logical denial.

The core imports only the Go standard library. Transport parsing, authentication,
delivery, provider credentials, and persistence paths stay in host adapters.

## Package ownership

| Package | Responsibility |
| --- | --- |
| `sessionmemory` | Portable records, validation, canonical processors, recall/trace/forget contracts, and semantic policy. |
| `sessionmemory/app` | Portable `Runtime`, `Deriver`, turn/boundary orchestration, projection coordination, recall, trace, and Go-only forget capabilities. |
| `sessionmemory/store/badger` | Canonical Badger implementation and maintenance lifecycle. Badger is authoritative. |
| `sessionmemory/index/bleve` | Rebuildable Bleve lexical projection. It never becomes the source of truth. |
| `sessionmemory/mcp` | Neutral `session_memory.search` and `session_memory.trace` adapter with injected exact-scope resolution. |
| `sessionmemory/sessionmemorytest` | Positive canonical test support for adapter consumers; it is not the removed legacy Store conformance suite. |

Balda-specific adapters remain under `internal/apps/balda`:

- `sessionmemorycmd` defines the `turn.v1` and `boundary.v1` export envelopes;
- `sessionmemoryapp` owns redaction, locator classification, SQLite ingress
  outbox, worker lanes, Norma wiring, and Balda lifecycle integration;
- `sessionmemorymcp` owns the authenticated broker/context bridge; and
- `eventbus/nats` owns JetStream publication, acknowledgement, retry, and DLQ
  behavior.

These host packages depend on the public packages. The public packages do not
import Balda application, transport, Fx, NATS, or channel packages.

## Isolation and temporal model

`Scope` is the ownership and concurrency boundary. A personal conversation, a
personal topic, a group conversation, and a group topic have different exact
scope keys. Topic/thread identity is orthogonal to audience; there is no
inheritance, promotion, participant merge, or cross-scope fallback.

The host maps an authenticated transport locator to a canonical scope:

```go
sessionmemory.Scope{
    Key:  canonicalLocator,
    Kind: sessionmemory.ScopeKindPersonal, // or ScopeKindGroup
}
```

The semantic pipeline derives atoms from completed turns, then can synthesize
scenario and profile records at a boundary. Revisions are append-only and carry
immutable source or parent provenance. Scope, stable IDs, timestamps, lifecycle
state, temporal validity, and provenance checks are owned by the application,
not delegated to a model response.

## Application capabilities

Hosts compose `sessionmemory/app.Runtime` from narrow capabilities:

- `IngestTurn` accepts a validated terminal turn and commits canonical state;
- `ApplyBoundary` runs boundary synthesis through the same semantic layer;
- `Search` performs bounded exact-scope recall, hydrating projection candidates
  against canonical state;
- `Trace` validates a bounded provenance closure; and
- `ForgetSource` / `ForgetScope` are explicit Go capabilities and are not MCP
  mutation tools.

`Runtime.Start` and `Runtime.Close` provide ordered lifecycle handling for
projection, model, and host-supplied resources. A missing capability returns a
stable disabled error rather than widening another capability's authority.

## Canonical and projection storage

`sessionmemory/store/badger` is the authoritative canonical store. It preserves
the existing `balda.session_memory` path and can be opened directly by another
in-process host. If the path has no canonical records, the runtime starts with
empty session memory. `sessionmemory/index/bleve` is a disposable lexical
projection that can be rebuilt from canonical records; recall never trusts
projection text without canonical hydration and exact-scope validation.

The Balda host retains SQLite only for the durable ingress outbox and audit
records introduced after the old domain schema. Migration `00033` drops the six
unsupported legacy SQLite session-memory domain tables in foreign-key-safe
order. Its `Down` section recreates empty tables for binary rollback, but the
dropped rows are not recoverable. The global fact-memory subsystem is outside
this migration and remains in `internal/apps/balda/memory` and `memory/global`.

## Neutral MCP surface

`sessionmemory/mcp` registers only:

- `session_memory.search`; and
- `session_memory.trace`.

The caller cannot provide a locator in tool arguments. A host-injected
authenticated resolver supplies the exact current scope. Results are bounded
and marked `untrusted_reference`; recalled text is data, never an instruction,
prompt, or command. Forget remains a trusted in-process Go operation and no
MCP ingest or forget endpoint is shipped.

## Non-goals

This extraction increment does not add a standalone service, remote API, vector
search/Vecgo, encryption, or a second global-memory implementation. It does not
move or rename `balda.memory.read`, `balda.memory.remember`, `MEMORY.md` import,
`memory/global`, or generic SQLite KV state. The removed Engine/Store runtime,
NativeProvider, importer, migration coordinator, and SQLite domain adapter have
no compatibility aliases.
