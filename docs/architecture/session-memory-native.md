# Native session memory

Balda's session memory is an optional in-process capability. It is separate
from the global fact KV (`balda.memory.*`) and partitioned by the exact public
locator `<channel_type>:<address_key>`.

This increment establishes extraction-ready packages inside the current Go
module. It does not ship a standalone service, remote API, vector index,
encryption layer, or MCP ingest/forget endpoint.

## Ownership

- `sessionmemory` is the storage-neutral semantic core. It owns scope,
  canonical records, evidence, temporal validation, reconciliation,
  idempotency, provenance, bounded retrieval contracts, and forget semantics.
  It imports no Balda, Badger, Bleve, MCP, transport, or Fx package.
- `sessionmemory/app` is the portable application layer. `Runtime` composes
  narrow turn, boundary, recall, trace, forget, projection, model, and lifecycle
  capabilities. `Deriver` validates structured model output; it does not choose
  scope or assign durable identity.
- `sessionmemory/store/badger` owns the canonical Badger adapter and maintenance
  lifecycle. Badger is authoritative and lives at the state-owned
  `${balda.state_dir}/session-memory/badger` path.
- `sessionmemory/index/bleve` owns the disposable Bleve lexical projection.
  Projection generations are rebuildable and never replace canonical truth.
- `sessionmemory/mcp` owns the neutral `session_memory.search` and
  `session_memory.trace` adapter. It receives an exact authenticated scope
  through a resolver and never parses transport locators.
- `internal/apps/balda/sessionmemoryapp` owns Balda capture, redaction, locator
  classification, ingress outbox, worker lanes, Norma invocation, and host
  lifecycle wiring.
- `internal/apps/balda/sessionmemorymcp` owns the Balda broker/context bridge;
  `internal/apps/balda/sessionmemorycmd` owns export envelopes; and
  `internal/apps/balda/eventbus/nats` owns JetStream delivery, acknowledgement,
  retry, and DLQ policy.
- `internal/apps/balda/memory` remains the separate global explicit-fact
  subsystem. Its `balda.memory.read`, `balda.memory.remember`, `MEMORY.md`
  import, `memory/global`, and generic SQLite KV state are not session memory.

Balda composition depends on the public packages. The public core and
application packages do not depend on Balda policy or concrete transports.

## Data flow

```text
eligible completed turn or session boundary
  -> Balda redaction and exact locator classification
  -> SQLite ingress outbox
  -> JetStream publish and PubAck
  -> bounded per-scope worker
  -> sessionmemory/app typed ingest
  -> sessionmemory semantic extraction/reconciliation
  -> canonical Badger commit
  -> Bleve projection sync (rebuildable side effect)

canonical active revisions
  -> Bleve lexical candidates
  -> app RecallService canonical hydration and fail-closed validation
  -> sessionmemory/mcp bounded untrusted reference
```

The host owns delivery durability and provider credentials. The memory layer
owns semantic idempotency and exact-scope state. A projection failure cannot
make non-canonical data authoritative; recall can use a bounded canonical tail
when a projection is unavailable or empty.

## Scope, temporal state, and forgetting

`Scope` is the ownership and concurrency boundary. Personal roots, personal
topics, group roots, and group topics are distinct partitions. There is no
participant, channel, session, or topic inheritance, and unsupported or
ambiguous locator classification fails closed.

Completed turns produce grounded atom/state/event revisions. Boundaries can
produce scenario and profile summaries from active same-scope records. Revisions
retain source or parent provenance and temporal metadata; supersession and
invalidation are append-only lifecycle transitions. Model output cannot assign
scope, IDs, timestamps, lifecycle state, or evidence outside the supplied
typed ports.

`ForgetSource` and `ForgetScope` are trusted Go capabilities. They first commit
logical denial and identity-only tombstones in canonical state, invalidate the
dependent revision closure, and then scrub projection candidates. A scrub or
projection retry cannot make denied content readable. Forget is not implicit in
session reset/close and is not exposed as a mutating MCP tool.

## MCP trust boundary

The public MCP adapter registers only `session_memory.search` and
`session_memory.trace`. Locator and session identity are absent from tool
arguments; Balda's authenticated broker supplies the exact scope server-side.
Search and trace outputs are bounded and marked `untrusted_reference`. The
adapter never executes recalled text or turns it into a prompt, system
instruction, transport request, or command. Provider/storage errors are
returned as stable safe tool errors.

## Storage migration and compatibility

Session-memory backend paths are grouped below the configured state directory:

```text
${balda.state_dir}/session-memory/
├── badger/       # canonical state
└── bleve/        # rebuildable projection
```

At startup, old direct-child directories
`${balda.state_dir}/session-memory.badger` and
`${balda.state_dir}/session-memory-bleve` are relocated into this subtree
before either backend opens. The relocation is local and idempotent; existing
canonical records are preserved. If an old and grouped directory coexist, the
runtime fails closed instead of selecting one or starting an empty store. If
neither canonical path exists, enabled session memory starts empty; no SQLite
domain import runs. Goose migrations `00030` through `00032` continue to own
the SQLite ingress outbox and audit records. New migration `00033` drops only
the six unsupported domain tables created by `00029`, in child-first order.
Its `Down` section recreates an empty schema for binary rollback and explicitly
cannot recover dropped rows.

The deleted `sessionmemory` Engine/Store runtime, `Config`, `NativeProvider`,
legacy importer, migration coordinator, Balda SQLite domain adapter, and old
Balda MCP names have no compatibility aliases. The global fact-memory path is
not touched by this migration.

## Lifecycle and verification

Startup remains ordered: configuration loads first; the bundled MCP lifecycle
starts; the enabled session-memory runtime opens canonical Badger and the
rebuildable projection; the configured Balda provider starts; then channel and
other runtime ingress starts. Shutdown reverses this order, draining ingress
and per-scope workers before closing projection, canonical storage, and model
resources. Disabled session memory creates no stream, worker, or projection
resource.

The [acceptance matrix](session-memory-v2-acceptance.md) maps each current
contract to positive tests and quality gates. The [operator runbook](../balda.md#operator-verification-runbook)
contains metadata-only rollout and recovery checks; it is not a substitute for
the repository gates.
