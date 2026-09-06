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
| Control and access | operator-driven cancel/clear/wait flows, owner/collaborator/channel auth state | `controlapp`, `auth` | feature actor behavior, transport-specific command handling |
| Command behavior | exact-name product command handlers and neutral command envelopes | `actors/command`, `commandcmd`; wiring in `commandfx` | transport parsing, provider markup, generic session ownership |
| Conversational ingress | provider-neutral authorization/session preconditions, one durable SessionActor publish attempt, accepted/retry/terminal settlement | `ingressapp`; inbound normalization in `handlers`; concrete runtime bindings in `handlersfx` | channel delivery, provider turn execution |
| Interactive questions | session-scoped user questions, pending-question lifecycle, reply settlement, timeout orchestration, actor resume targeting | `questions`, transport-neutral question contracts in `questionfmt` | transport adapters, generic session lifecycle, hidden suspended runtime frames |
| Agent permissions | transport-neutral agent permission policy, interactive permission review, fail-closed settlement | `permissions`, ADK-facing adapter in `agent`, transport-neutral permission contracts in `permissionfmt` | provider protocol types, transport-specific reply parsing, general question lifecycle |
| Delivery presentation | prompt-format routing for model text, structured deterministic rendering for system-authored delivery messages, transport-neutral descriptors/registration surfaces | `deliveryfmt`, `deliveryfx`, service-message contracts in `permissionfmt`, `questionfmt`, `progressfmt` | transport adapters, provider SDK types, product workflow policy |
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
`authcmd` owns the transport-neutral collaborator data contract, ensuring persistence (`state`) and other layers can reference collaborator identity without importing the `auth` service package.

Do not mix feature behavior into this zone. It should express "who may do what"
and "how operator actions settle", not product actor workflows.

### Conversational ingress

This zone owns the provider-neutral acceptance boundary for ordinary chat work:

- authorization through a small consuming-package port;
- session create/restore preconditions through a small consuming-package port;
- exactly one durable SessionActor publish attempt per processing attempt;
- accepted, retry, and terminal settlement classification with stable identity.

Inbound provider parsing and normalization remain concrete in `handlers`.
`handlersfx` supplies composition-root bindings for provider runtime operations;
provider delivery remains in `channel/*`, and model execution remains outside
this zone. A rejected or ignored inbound item terminates before turn creation;
a transient failure before a dispatch receipt retains the same dedupe identity.

### Interactive questions

This zone owns interactive user-input orchestration that is scoped to a Balda
conversation but may be initiated by any product actor in that conversation's
activation chain.

It owns:

- pending question lifecycle;
- durable reply correlation;
- timeout orchestration via scheduled jobs;
- actor resume targeting after answer or timeout.

It does not own:

- transport-specific reply parsing;
- session lifecycle/create/restore semantics;
- provider runtime suspension across time.

Keep this zone small and explicit. If question contracts are shared across
layers, place those contracts in a dedicated contract package rather than in
`questions` itself.

Transport-specific reply parsing, presentation, and correlation remain below
this zone in the owning `channel/*` subtree.

### Agent permissions

This zone owns application policy for sensitive actions requested during an
active agent run:

- static `allow_all` and `deny_all` decisions by option semantics;
- interactive `ask` orchestration over the shared question lifecycle;
- bounded waiting and fail-closed behavior;
- mapping a settled option back to the active ADK-facing permission callback.
- recording a provider-independent semantic outcome so an empty terminal turn
  can distinguish timeout, delivery failure, and user denial.

`permissioncmd` contains the transport- and provider-neutral contracts. The
`agent` package adapts the ADK-facing permission callback into those contracts.
Provider protocol SDK types must not cross that adapter boundary. The
`permissions` package may reuse `questions`, but it does not own generic reply
correlation, durable question state, or concrete channel delivery behavior.
`permissionfmt` owns transport-neutral structured permission contracts and
registration surfaces. Concrete transport rendering belongs to the owning
`channel/*` package. Opaque provider input remains below the ADK-facing adapter
boundary and is never used as user-facing prompt text.

### Delivery presentation

This zone owns presentation routing contracts, not transport implementations.

It owns:

- prompt-format routing for model-authored text;
- structured message descriptors for system-authored service messages;
- transport-neutral registration surfaces for structured renderers and prompt
  formatters.

It does not own:

- provider SDK message shapes;
- transport-local rendering branches;
- transport-local prompt injection text;
- provider correlation rules.

Packages such as `questionfmt`, `permissionfmt`, and `progressfmt` should stay
transport-neutral. If a transport needs custom rendering, that renderer should
live in the owning `channel/*` subtree and register through DI.
`deliveryfmt` owns the transport-neutral prompt format and structured message
formatting registries; in architecture enforcement, it forms an isolated neutral
registry component so that format registration at composition time does not create
upward dependencies on concrete provider renderers.

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
