# Background Monitors

Owner: Balda maintainers  
Status: proposed

## Problem

Balda does not currently have a durable background execution model for operator requests such as "watch GitHub until checks finish and report back".

Trying to keep an interactive agent turn alive in the background would mix session execution, transport delivery, retry policy, and long-lived monitoring state into one path. That would make restarts, retries, cancellation, and operator observability hard to reason about.

## Decision

Balda will treat background monitoring as a separate durable actor/job capability, not as a long-running conversational turn.

The chat/session layer may start, inspect, and cancel monitors, but the monitor itself runs as its own durable actor-driven workflow.

Background updates are delivered as separate delivery envelopes through actorlayer. They are not continuations of the original LLM turn.

## Model

### Execution split

Keep three concerns separate:

1. Interactive session turns  
   Short-lived conversational work for immediate user interaction.
2. Background monitor jobs  
   Durable polling or waiting work with its own lifecycle.
3. Delivery subscriptions  
   Where updates are reported, with transport-specific formatting and idempotency.

### Target flow

For an operator request such as "watch this commit until all checks are green":

`chat ingress -> session actor -> monitor start command -> monitor actor -> delivery actor(s)`

The session actor acknowledges setup immediately. The monitor actor owns polling, state, retry, completion, and cancellation.

## Required properties

- Monitor execution survives process restarts.
- Monitor state is explicit and inspectable.
- Retry and polling policy are owned by the monitor runtime, not by chat transport code.
- Intermediate and final updates are separate deliveries.
- Delivery transport concerns stay outside monitor business logic.
- One actor may emit deliveries to one or many downstream actors during monitor execution.

## Command and event shape

Initial direction:

- `balda.v1.cmd.monitor.start`
- `balda.v1.cmd.monitor.cancel`
- `balda.v1.monitor.started`
- `balda.v1.monitor.progressed`
- `balda.v1.monitor.completed`
- `balda.v1.monitor.failed`
- `balda.v1.monitor.cancelled`

A monitor start command should carry:

- monitor kind;
- target spec;
- polling interval or schedule policy;
- stop condition;
- notification policy;
- report destination.

## Layering constraints

- `actorlayer` remains the transport and execution boundary.
- Chat ingress must not implement long-lived polling loops.
- Transport handlers must not own monitor lifecycle.
- SQLite/read models may store monitor state if chosen, but not as a generic transport queue.
- Delivery actors keep provider-specific idempotency and settlement policy.

## Consequences

- Balda gets true background work without pretending that the interactive agent is still running between messages.
- Operator-facing updates become explicit, observable, and restart-safe.
- Chat transports stay simple: they start monitors and receive monitor updates.
- The same pattern can later support GitHub checks, deployment watches, queue drains, health probes, and similar long-running operator requests.

## Non-goals for now

- No implementation in this ADR.
- No commitment yet to exact persistence schema.
- No commitment yet to whether monitors reuse existing job projections or get a dedicated projection model.

## Open questions

- Should monitors be modeled as a dedicated actor family or as a typed job on top of existing job orchestration?
- What operator-facing commands should expose list/status/cancel?
- Which monitor types are worth supporting first beyond GitHub checks?
- Should monitor updates edit prior transport messages or always emit new ones?

## Update triggers

- Introduction of any background watch or wait capability.
- Changes to actor/job layering for durable orchestration.
- Changes to delivery semantics for intermediate monitor updates.
