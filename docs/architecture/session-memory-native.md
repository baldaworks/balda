# Native session memory

Balda's session memory is a native application capability. It remains separate
from the global fact KV (`balda.memory.*`) and is isolated by the exact public
locator `<channel_type>:<address_key>`.

## Ownership

- `sessionmemory` is the storage-neutral portable core: scopes, canonical v2
  records, evidence, temporal validation, forgetting state, bounded recall
  contracts, projection watermarks, and local ports. It imports no Badger,
  Bleve, MCP, transport, or Fx package.
- `internal/apps/balda/state` owns durable infrastructure. Badger is the
  canonical source of truth; Bleve is the mandatory rebuildable lexical
  projection. Numeric backend identifiers never cross the adapter boundary.
- `internal/apps/balda/sessionmemoryapp` owns capture, ingress-outbox/worker
  lifecycle, canonical hydration, fail-closed recall policy, and the isolated
  structured deriver. It composes ports; it does not own backend records.
- `internal/apps/balda/sessionmemorymcp` owns the exact-scope search/trace MCP
  contract. Scope comes from an authenticated broker capability; tool filters
  cannot select a different locator.
- `internal/apps/balda/sessionmemorymcp.ContextBroker` binds one opaque MCP
  capability to one locator/session and injects trusted headers server-side.

## Data flow

```text
completed turn/boundary
  -> producer ingress outbox
  -> JetStream publish/PubAck
  -> bounded per-scope worker
  -> canonical Badger mutation
  -> projection change record/watermark

canonical active revisions
  -> Bleve lexical generation (mandatory)
  -> RecallService lexical ranking
  -> canonical hydration and fail-closed validation
  -> bounded untrusted MCP reference
```

Canonical Badger records and operation outcomes are authoritative. Bleve
generations are disposable materialized views: a generation is dirty before
writes, committed explicitly, and advertised only after its watermark is
durable. A missing or dirty generation falls back to a bounded canonical tail;
it never causes a full-scope read.

## Isolation and logical forgetting

The exact canonical locator is the partition. Personal/group audience and
root/topic/thread dimensions are independent, so each distinct locator is a
separate scope. Unknown or ambiguous channel classification fails closed.

`ForgetSource` and `ForgetScope` are application-level native operations. They
immediately deny recall, preserve identity-only tombstones, invalidate the
dependent revision closure, remove matching projection candidates, and leave
unrelated scopes and global fact KV unchanged. Canonical hydration rejects
stale, foreign, inactive, expired, or disallowed candidates even when a
projection is behind. These operations are not implicit reset/close behavior
and are not model-invocable destructive MCP tools.

## Verification and operations

The authoritative [operator verification runbook](../balda.md#operator-verification-runbook)
owns rollout, bounded backlog observation, reset/restart recall, foreign-scope
isolation, recovery, rollback, and safe evidence requirements. Operator
evidence is metadata-only; transport bodies, recalled text, and broker
capability URLs do not belong in logs or verification records.

The acceptance evidence matrix is in
[session-memory-v2-acceptance.md](session-memory-v2-acceptance.md). Repository
proofs include:

- `internal/apps/balda/session_memory_runtime_integration_test.go` for ingress,
  PubAck, restart, and canonical reopen behavior;
- `internal/apps/balda/session_memory_recall_integration_test.go` for
  authenticated MCP recall, exact-locator isolation, and provenance;
- `internal/apps/balda/state/badger_session_memory_test.go` for canonical
  mutation, checkpoints, migration, backup/integrity, and reopen behavior;
- `internal/apps/balda/state/bleve_session_memory_test.go` for disposable
  projection generations and recovery markers.

Core behavior remains owned and tested by `sessionmemory`; worker, recall, and
MCP adapter behavior remains owned by `sessionmemoryapp` and
`sessionmemorymcp`. The runbook composes those proofs with live transport
verification; it does not move operational policy into those packages.
