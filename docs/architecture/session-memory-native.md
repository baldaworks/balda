# Native session memory

Balda's session memory is a native application capability. It is deliberately
separate from the global fact KV (`balda.memory.*`).

## Ownership

- `sessionmemory` is the stdlib-only portable core: exact scopes, typed atoms,
  scenarios, profiles, provenance, search, trace, forgetting, validation, and
  Store/model ports.
- `internal/apps/balda/state` owns the SQLite Store and migration. It stores
  sources, derived revisions, provenance, operation outcomes, and forget
  tombstones. It does not store a handoff outbox or lease.
- `internal/apps/balda/sessionmemoryapp` owns JetStream capture/worker wiring,
  the native Provider, and the dedicated Norma structured deriver.
- `internal/apps/balda/sessionmemorymcp` owns the exact-scope search/trace MCP
  contract. Scope comes from an authenticated per-session broker capability;
  tool arguments cannot select a locator.
- `internal/apps/balda/sessionmemorymcp.ContextBroker` binds one opaque MCP URL
  capability to one locator/session and injects trusted headers server-side.

## Data flow

```text
completed turn/boundary
  -> JetStream PubAck
  -> serialized worker
  -> native Engine
  -> SQLite Store transaction

Engine model ports
  -> isolated Norma runtime
  -> bounded typed candidates
  -> Engine validation
  -> SQLite revision/provenance commit

authenticated session runtime
  -> broker capability
  -> exact scope resolver
  -> Engine search/trace
  -> bounded untrusted reference data
```

JetStream is the only handoff durability boundary. SQLite memory tables are
authoritative data and semantic idempotency records, not an outbox. A PubAck
is required before a completed turn is considered handed off; a crash before
PubAck may lose that export by design.

## Isolation and forgetting

The full canonical locator `<channel_type>:<address_key>` is the partition.
Personal/group audience and root/topic/thread dimensions are independent, so
personal topics are not folded into a personal root and group topics are not
folded into a group root. Unknown or ambiguous channel classification fails
closed.

`ForgetSource` and `ForgetScope` are application-level native operations. They
atomically replace raw content with identity-only tombstones, invalidate the
complete dependent revision closure, and preserve unrelated scopes and global
fact KV. They are not implicit reset/close behavior and are not model-invocable
destructive MCP tools.
