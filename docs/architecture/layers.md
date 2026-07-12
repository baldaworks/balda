# Balda architecture layers

This document defines layer ownership for the Balda application. The goal is to make code placement and dependency choices explicit, so package boundaries stay stable as features evolve.

## Principles

- Every package has a clear owner responsibility.
- Runtime policy and product behavior are different concerns.
- Ingress publishes work; it does not own product execution.
- Feature actors own feature behavior.
- Provider-specific delivery code stays behind delivery/channel boundaries.
- New packages should be introduced only when they represent a stable semantic boundary, not just to shrink file size.

## Layers

| Layer | Owns | Must not own |
| --- | --- | --- |
| `github.com/baldaworks/go-actorlayer` | generic actor runtime primitives, envelopes, transport-facing contracts, retry/error helpers | Balda product policy |
| `internal/apps/balda/execution` | Balda runtime policy, host lifecycle, lane policy, dead-letter behavior, runtime wiring | feature semantics, ingress behavior |
| `internal/apps/balda/handlers` | ingress parsing, auth/session checks, publishing work into actor/runtime system | feature execution logic, provider settlement policy |
| `internal/apps/balda/actors` | product actor behavior and feature-owned orchestration | transport parsing, generic runtime policy |
| `internal/apps/balda/actors/goalkeeper` | goal feature actor behavior, goal run lifecycle, goal progress/outcome assembly | generic runtime policy, provider-specific delivery logic |
| `internal/apps/balda/jobs` | durable job state, events, projection-oriented application services | ingress behavior, transport execution |
| `internal/apps/balda/channel/*` | provider-specific delivery adapters and delivery semantics | product workflow, session use-case policy |
| `internal/apps/balda/state` | storage models and persistence implementation | feature orchestration, delivery policy |

## Dependency rules

- `handlers` may depend on contracts and application services, but should not depend on actor implementation details unless they are publishing actor-owned commands.
- `execution` may wire product behavior, but should not become the owner of feature contracts.
- `actors` may use jobs/session/application services, but should not absorb provider adapter details.
- `channel/*` packages should not know product workflow steps.
- `state` should remain a persistence boundary, not a workflow boundary.

## Application sub-zones

The top-level `application` layer is intentionally broad for dependency linting,
but Balda treats it as several semantic zones rather than one generic bucket.

- Session runtime: `session`, `sessionapp`, session-facing runtime support in `agent`.
- Turn execution: `sessionturn`, `sessionturnapp`.
- Job lifecycle: `jobs`, `jobexec`, `scheduledjobs`.
- Control and access: `controlapp`, `auth`.
- Application support: `appports`, `envelopetarget`, `memory`.

See [Application sub-zones](application-zones.md) for the authoritative zone
map and placement rules.

## Feature package slicing

Inside a feature-owned package, prefer internal file slices before introducing new packages. Split by responsibility:

- coordinator / entrypoint
- lifecycle helpers
- workflow/event loop
- side effects
- result assembly
- wire helpers

Promote a slice to a child package only when it has a stable owner and reusable boundary.

## Review questions

When reviewing a change, ask:

1. Who owns this responsibility?
2. Why does it live in this package instead of an adjacent layer?
3. Which dependencies are required here, and which would be a leak?
4. Is this a real boundary, or just a large file being split?
