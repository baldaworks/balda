# Session memory core

`github.com/normahq/balda/sessionmemory` is an extraction-ready Go library for durable, provenance-grounded session memory. It is currently versioned with Balda; it is not yet a separately released module or service.

The library owns two related contract families:

- raw completed turns and lifecycle boundaries (`session-memory/v1`);
- derived atoms, scenarios, profiles, processing, retrieval, and forgetting (`session-memory-derived/v1`).

It depends only on the Go standard library. Consumers provide persistence and model adapters through public ports.

## Isolation model

`Scope` is the only ownership and concurrency boundary. Personal conversations,
personal topics, group chats, and individual group topics must have different
canonical scope keys. Topic/thread identity is orthogonal to the personal/group
audience. The core never inherits or shares memory between scopes, even when
session IDs, source turn IDs, or human participants collide.

Transport-specific locator parsing stays outside this package. A future adapter maps its authenticated locator to:

```go
sessionmemory.Scope{
    Key:  canonicalLocator,
    Kind: sessionmemory.ScopeKindPersonal, // or ScopeKindGroup
}
```

A topic is represented by its exact topic locator in `Key`; it does not inherit the containing group's derived memory.

## Derived model

The engine produces three append-only layers:

1. Atoms classify precise facts, preferences, constraints, decisions, and events from completed turns.
2. Scenarios summarize topic or project context from active same-scope atoms.
3. A profile summarizes long-lived context for one exact locator from active same-scope atoms or scenarios.

Every revision carries immutable raw-source and/or parent-revision provenance. Updates use explicit coexistence, supersession, and invalidation states. The engine, rather than a model adapter, owns scope, stable IDs, timestamps, revision state, operation identity, and provenance validation.

## Ports and consistency

`NewEngine` accepts:

- a concurrency-safe `Store`;
- `AtomExtractor`;
- `ScenarioSynthesizer`;
- `ProfileSynthesizer`;
- optional lower hard bounds in `Config`.

Processing uses stable operation IDs and exact-scope optimistic concurrency. Replay first asks the Store for a durable outcome, so an already committed stage does not call a model again. A same-scope CAS conflict is returned to the caller; the engine does not rerun potentially nondeterministic model output implicitly. Scenario and profile boundary stages are independently durable, so replay resumes after a partial boundary failure.

A Store must atomically enforce idempotency, CAS, revision transitions, and reverse provenance. The library deliberately ships no production database, filesystem, vector index, or network backend.

## Retrieval and trust

`Engine.Search` and `Engine.Trace` are bounded, explicit, on-demand reads. Search validates exact scope, active state, filters, deterministic result shape, and response bounds. Trace additionally requires a closed, acyclic provenance graph with no forgotten source content. Results carry `ReferenceTrustUntrusted`; they are reference data, not executable instructions. The package has no automatic prompt-injection API.

## Forgetting

`Engine.ForgetSource` calculates the complete reverse-provenance closure, including superseded history. In one atomic Store operation it requires the raw turn text to become an identity-only tombstone and every active or superseded dependent revision to become invalidated. `Engine.ForgetScope` applies the same rule to all readable content in one exact locator.

Forgetting is model-independent: it does not regenerate summaries. It never reads or mutates Balda's separate global explicit fact memory.

## Testing a Store adapter

The `sessionmemory/sessionmemorytest` package includes a deterministic in-memory Store, scripted typed models, and a reusable conformance runner:

```go
func TestMyStore(t *testing.T) {
    sessionmemorytest.RunStoreContract(t, func() sessionmemory.Store {
        return newMyStoreForTest(t)
    })
}
```

The suite exercises end-to-end derivation, replay, CAS atomicity, revision history, reverse provenance, retrieval, forgetting, locator collisions, malformed input, and concurrent same-scope operations under the race detector.

## Explicit non-goals

This package does not provide or change:

- Balda runtime, HTTP, MCP, JetStream, command, or configuration wiring;
- a production Store, model client, embedding provider, daemon, or separate service;
- automatic recall or prompt injection;
- cross-locator sharing or promotion;
- global explicit fact memory;
- LLM Wiki, documents, CodeGraph, skills, or an asset control plane.
