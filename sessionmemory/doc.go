// Package sessionmemory defines portable raw and derived contracts for durable,
// locator-scoped session memory.
//
// Scope is the ownership boundary. A personal conversation, each personal
// topic, a group chat, and each group topic use distinct canonical Scope
// values. Topic/thread identity is orthogonal to the personal/group audience;
// the package performs no inheritance, promotion, persona merge, or cross-scope
// lookup. Transport adapters are responsible for producing the canonical
// locator key; the core remains transport-neutral.
//
// The derived pipeline has three layers:
//
//   - Atom records a fact, preference, constraint, decision, or event from one
//     completed turn.
//   - Scenario summarizes topic or project context from active atoms.
//   - Profile summarizes long-lived context for one exact locator only.
//
// Every derived revision has immutable Provenance. Change is append-only:
// revisions may coexist, supersede prior revisions, or become invalidated, but
// prior evidence is never silently overwritten. Engine uses stable operation
// identities and exact-scope optimistic concurrency. It checks durable outcomes
// before model calls and does not implicitly retry a model after a CAS conflict.
//
// Store owns atomic persistence, idempotency, reverse-provenance indexes,
// bounded search, trace, and forgetting. Model behavior is supplied through the
// typed AtomExtractor, ScenarioSynthesizer, and ProfileSynthesizer ports. Engine
// validates model and Store output as hostile input and owns stable identities,
// scope, timestamps, lifecycle state, and provenance grounding.
//
// Search and Trace are explicit, on-demand operations. Returned content is
// marked ReferenceTrustUntrusted and is never injected into a prompt by this
// package. ForgetSource replaces raw content with an identity-only tombstone and
// atomically invalidates every transitive dependent revision. ForgetScope does
// the same for one exact locator. Forgetting performs no model synthesis.
//
// This package intentionally contains no production Store, model client,
// embedding or ranking implementation, service, HTTP or MCP surface, JetStream
// wiring, automatic prompt injection, Wiki, document store, or integration with
// Balda's separate global explicit fact memory. The core imports only the Go
// standard library. Package sessionmemorytest provides a deterministic test
// Store, scripted models, and a reusable Store conformance suite for adapters.
package sessionmemory
