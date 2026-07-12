# Balda application sub-zones

This document defines the semantic sub-zones inside Balda's `application`
layer. The goal is to keep `application` from becoming a generic holding area
 for everything that is not clearly ingress, domain, or infrastructure.

## Why this exists

Balda's top-level architecture lint groups several packages under one
`application` layer. That is useful for dependency control, but it is too broad
to explain ownership on its own.

Inside that layer, Balda currently has a small number of stable semantic zones.
New code should attach to one of these zones deliberately instead of landing in
`application` "because it fits there somewhere."

## Zones

| Zone | Owns | Primary packages | Must not absorb |
| --- | --- | --- | --- |
| Session runtime | runtime session lifecycle, restore/ensure/get session semantics, workspace/session binding, provider-backed runtime ownership for Balda sessions and goal runs | `session`, `sessionapp`, session-facing runtime support in `agent` | queued turn execution policy, durable job orchestration, feature actor behavior |
| Turn execution | queued turn restoration, provider turn execution, turn progress/final reply orchestration | `sessionturn`, `sessionturnapp` | general session lifecycle, generic job persistence |
| Job lifecycle | durable job records, job events, delivery persistence, projections, scheduled durable work execution | `jobs`, `jobexec`, `scheduledjobs` | transport adapters, ingress parsing, conversational session ownership |
| Control and access | operator-driven cancel/clear/restart/wait flows, owner/collaborator/channel auth state | `controlapp`, `auth` | feature actor behavior, transport-specific command handling |
| Application support | small app-facing ports and support helpers used by the zones above | `appports`, `envelopetarget`, `memory` | broad workflow orchestration, feature-specific business logic |

## Zone details

### Session runtime

This zone owns the meaning of a live Balda session:

- locating a session;
- restoring a persisted session;
- ensuring a session exists when work starts;
- binding sessions to workspace/runtime dependencies.

Keep "what is a session?" here. Do not move queued turn execution policy or
durable job result handling into this zone.

`agent` belongs here as runtime support, not as a generic feature package. It
owns provider-backed runtime construction and runtime-adjacent workspace support
used by Balda sessions and isolated goal runs. It does not own session
lifecycle semantics itself; `session` remains that owner.

### Turn execution

This zone owns the orchestration for one queued session turn:

- restore-or-ensure session access before execution;
- provider run execution;
- turn progress emission;
- final reply delivery decisions for the turn path.

This zone may depend on session/runtime services and delivery contracts, but it
must not become a second session lifecycle owner.

`sessionturnapp` is expected to stay focused on turn execution. If it grows
substantially beyond that scope, split by responsibility inside the package
before introducing new top-level packages.

### Job lifecycle

This zone owns durable work state:

- durable job records;
- job event append/projection behavior;
- durable delivery reservation state;
- scheduled job reconciliation and due-job execution.

This is already close to a small subsystem. Keep the ownership narrow:

- `jobs` owns durable state and projection-oriented services;
- `jobexec` owns job-specific execution use-cases over that durable state;
- `scheduledjobs` owns startup-managed recurring and one-shot scheduled work.

Do not make `jobs` the default dependency for unrelated application logic just
because it already has a convenient store or service.

### Control and access

This zone owns operator-facing control semantics and access state:

- session/job cancellation;
- goal clearing;
- wait scheduling;
- owner/collaborator/channel authorization state.

`controlapp` owns control flows.
`auth` owns access-control state and access-control support services.

Do not mix feature behavior into this zone. It should express "who may do what"
and "how operator actions settle", not product actor workflows.

### Application support

These packages exist to support the zones above without becoming workflow
owners:

- `appports`: narrow application-facing contracts shared across layers;
- `envelopetarget`: configured target resolution helpers;
- `memory`: durable Balda memory support used by application/runtime flows.

Support packages should stay small and specific. If a support package starts
collecting orchestration logic, it probably belongs in a real application zone
instead.

## Current hotspots

The current architecture does not need another large package split, but these
packages deserve monitoring:

- `jobs`: risk of turning into a god-package for every durable concern.
- `sessionturnapp`: risk of turning into a generic turn orchestration blob.

When either grows, prefer clarifying ownership inside the zone first. Only
introduce a new top-level package if a stable semantic boundary emerges.

## Placement rules

When adding new application code, ask:

1. Which zone owns this policy?
2. Is it about a session, a turn, a durable job, a control action, or support?
3. Would this package still make sense if transport/provider details changed?
4. Am I adding workflow ownership to a support package by accident?

If the answer is unclear, update this document before adding a new application
package.
