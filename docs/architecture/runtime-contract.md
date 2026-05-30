# Runtime Contract

Owner: Balda maintainers  
Status: active

## Invariants

- Startup order stays strict: config -> bundled MCP lifecycle -> balda provider -> channel runtime.
- JetStream must be available before ingress accepts work.
- No runtime path executes user work without a JetStream command publish ack.
- SessionActor is the only actor that can enqueue TurnDispatcher work.
- SQLite does not own command selection, claim, retry, or wakeup semantics.
- Runtime boundaries are strict and explicit: actor execution flows through Norma actorlayer, while command transport policy stays in Balda.
- Balda owns queue, retry exhaustion, dead-letter side effects, projection writes, and command visibility telemetry.
- Norma actorlayer is a typed engine only: it can receive commands/deliveries and emit events, but does not make Balda-specific product policy decisions.

## Boundary contract

- Norma actorlayer core:
  - Typed command envelopes and actor keys.
  - Per-key deterministic lanes.
  - Delivery lifecycle hooks (accept/running/in_progress/acked/retry/deadletter/noop).
  - Actor dispatch and state transition primitives.
  - No Balda provider selection, queue runtime, Telegram, MCP, or task projection policy.

- Balda integration layer (policy owner):
  - Command transport semantics: JetStream command stream, ack/nak/term behavior, heartbeats, in-progress redelivery.
  - Retry strategy and classification, dead-letter promotion logic, and DLQ reporting.
  - Task/projector side effects in SQLite (`swarm_tasks`, `swarm_task_events`, command/task status state).
  - Command visibility and operator-facing status surfaces (`/queue status`, `/dlq`, `/projection status`).
  - Mapping between policy metadata (`chat_id`, `topic_id`, `goal_id`, `attempt`) and actor-level envelopes.
  - The single app-scoped ADK provider runtime selected by `balda.provider`.

- Boundary obligations:
  - Actor definitions and actor state must not select or branch on provider IDs.
  - Provider-specific types stay outside actorlayer-facing contracts.
  - JetStream settlement exposes the same lifecycle outcomes to actorlayer regardless of command kind.

## Related tests

- `internal/apps/balda/swarm/config_test.go`
- `internal/apps/balda/eventbus/config_test.go`
- `internal/apps/balda/architecture_contract_test.go`

## Related packages

- `internal/apps/balda/app.go`
- `internal/apps/balda/handlers`
- `internal/apps/balda/swarm`
- `internal/apps/balda/eventbus`

## Update triggers

- Runtime startup wiring changes.
- Any command execution path change.
- New config keys that affect transport or execution mode.
