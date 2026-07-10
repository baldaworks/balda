# Runtime Contract

Owner: Balda maintainers  
Status: active

## Invariants

- Startup order stays strict: config -> bundled MCP -> provider runtime -> session/mailbox and durable actor infrastructure -> scheduler/webhook/Slack/Zulip/Telegram ingress.
- Shutdown follows the exact reverse lifecycle order.
- The durable command runtime must be available before ingress accepts work.
- No runtime path executes user work without durable actor dispatch acceptance.
- The session actor is the only code path that can enqueue `TurnDispatcher` work; `sessionturn` is the only queued-turn restoration use case.
- `TurnDispatcher` is the authoritative per-session mailbox. Canceling a session resolves every dropped completion with `context.Canceled` and cancels the active turn.
- SQLite does not own command selection, claim, retry, or wakeup semantics.
- Runtime boundaries are strict and explicit: ingress publishes through actorlayer transport dispatcher contracts, actor execution and delivery settlement flow through Balda's local actorlayer contracts, and concrete transport policy stays in Balda's NATS adapter.
- Balda owns queue, retry exhaustion, dead-letter side effects, projection writes, and command visibility telemetry.
- Balda keeps that ownership inside explicit app layers: `actorcmd` owns wire taxonomy; `execution` owns runtime policy; `jobs` owns durable job state, event outbox, and projections; `actors` owns product behavior; `sessionturn` owns queued-turn restoration; `internalmcp` owns bundled MCP lifecycle; and `handlers` owns ingress plus the provider-turn executor adapter.
- The local `pkg/actorlayer` owns generic envelopes, retry/error helpers, runtime primitives, and transport-facing contracts, but does not make Balda-specific product policy decisions.

## Boundary contract

- Local actorlayer core:
  - Typed command envelopes and actor keys.
  - Per-key deterministic lanes.
  - Delivery lifecycle hooks (accept/running/in_progress/acked/retry/deadletter/noop).
  - Actor dispatch and state transition primitives, including the dispatch runtime that owns address resolution and lane execution.
  - Transport-facing interfaces for dispatch, event publication/consumption, and draining.
  - No Balda provider selection, queue runtime, Telegram, MCP, or job projection policy.

- Balda integration layer (policy owner):
  - Product actor implementations in `internal/apps/balda/actors` and wire contracts in leaf package `internal/apps/balda/actorcmd`.
  - Telegram, Slack, Zulip, webhook, and scheduler ingress in `internal/apps/balda/handlers`; ingress publishes commands and does not register product actors.
  - Concrete transport adapter semantics: command stream, ack/nak/term behavior, heartbeats, in-progress redelivery, exposed upward only as actorlayer source/delivery and small Balda-facing dispatch/event interfaces.
  - Retry strategy and classification, dead-letter promotion logic, and DLQ reporting.
  - Job state in `execution_jobs`, transactional event publication intent in `execution_job_event_outbox`, and idempotent history projections in `execution_job_events`.
  - Internal command visibility backed by logs and tooling.
  - Mapping between policy metadata (`chat_id`, `topic_id`, `goal_id`, `attempt`) and actor-level envelopes.
  - The single app-scoped provider runtime selected by `balda.provider`.

- Internal Balda runtime decomposition:
  - `runtime.go`: host loop and dispatch-runtime wiring.
  - `runtime_lane_policy.go`: Balda actor addressing and lane-key policy.
  - `runtime_heartbeat.go`: Balda heartbeat cadence and in-progress visibility publication.
  - `runtime_deadletter.go`: Balda retry-exhaustion and job dead-letter side effects.
  - `runtime_delivery.go`: Balda delivery wrapping and envelope-context attachment.

- Boundary obligations:
  - Actor definitions and actor state must not select or branch on provider IDs.
  - Provider-specific types stay outside actorlayer-facing contracts.
  - Transport settlement is hidden behind actorlayer delivery methods and exposes the same lifecycle outcomes regardless of command kind.

## Related tests

- `internal/apps/balda/execution/config_test.go`
- `internal/apps/balda/eventbus/config_test.go`
- `internal/apps/balda/application_lifecycle_test.go`
- `internal/apps/balda/architecture_dependencies_test.go`
- `internal/apps/balda/actors/turn_dispatcher_test.go`
- `internal/apps/balda/jobs/service_test.go`

## Related packages

- `internal/apps/balda`
- `internal/apps/balda/actors`
- `internal/apps/balda/actorcmd`
- `internal/apps/balda/handlers`
- `internal/apps/balda/sessionturn`
- `internal/apps/balda/internalmcp`
- `internal/apps/balda/execution`
- `internal/apps/balda/jobs`
- `internal/apps/balda/eventbus`

## Update triggers

- Runtime startup wiring changes.
- Any command execution path change.
- New config keys that affect transport or execution mode.
