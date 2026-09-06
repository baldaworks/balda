# Runtime Contract

Owner: Balda maintainers  
Status: active

## Invariants

- Startup order stays strict: config -> bundled MCP -> provider runtime -> session/mailbox and durable actor infrastructure -> scheduler/webhook/Zulip/Telegram/Slackagent ingress.
- Shutdown follows the exact reverse lifecycle order.
- The durable command runtime must be available before ingress accepts work.
- No runtime path executes user work without durable actor dispatch acceptance.
- The session actor is the only code path that can enqueue `TurnDispatcher` work; `sessionturn` is the only queued-turn restoration use case.
- `TurnDispatcher` is the authoritative per-session mailbox. Canceling a session resolves every dropped completion (including constituent steering waiters) with `context.Canceled` and cancels the active turn.
- Coalesced steering messages retain individual constituent completion channels; constituent envelopes remain unacknowledged to durable transport until batch execution finishes, fails, or cancels.
- Pre-execution process restart safely replays unacknowledged envelopes. Pending and running batches deduplicate constituents by stable identity (`DedupeKey` / message ID), attaching duplicate arrivals as waiters without duplicate text fragments. Transport delivery interruptions remain retryable rather than falsely settled.
- SQLite does not own command selection, claim, retry, or wakeup semantics.
- Runtime boundaries are strict and explicit: ingress publishes through actorlayer transport dispatcher contracts, actor execution and delivery settlement flow through `github.com/baldaworks/go-actorlayer`, and concrete transport policy stays in Balda's NATS adapter.
- Balda owns queue, retry exhaustion, dead-letter side effects, projection writes, and command visibility telemetry.
- Runtime diagnostics expose only structural settlement metadata. Retry/dead-letter reasons are bounded outcome and error-class codes; command DLQ records retain identity, routing, source metadata, payload size, and SHA-256, but never the original payload, provider error body, header values, credentials, or capability URLs.
- Balda keeps that ownership inside explicit app layers: `actorcmd` owns the canonical wire taxonomy; `execution` owns runtime policy and re-exports that taxonomy as the runtime-facing compatibility facade; `jobs` owns durable job state, event outbox, and projections; `actors` owns product behavior; `sessionturn` owns queued-turn restoration; `internalmcp` owns bundled MCP lifecycle; `handlers` owns ingress normalization and publication; and `handlersfx` binds ingress-owned ports to concrete provider runtimes.
- `agent` owns provider-backed runtime construction, root runtime prompt/session-state bootstrap, isolated goal runtime preparation, runtime-adjacent workspace support, and adaptation of ADK-facing permission callbacks into provider-neutral Balda contracts. It does not own session lifecycle semantics, queued-turn orchestration, or permission policy.
- `permissions` owns provider-neutral agent permission policy and interactive review orchestration; provider protocol types stay below the `agent` adapter boundary.
- `sessionturnapp` records failed ADK tool responses with redacted error metadata and never logs raw tool arguments or complete tool responses.
- `github.com/baldaworks/go-actorlayer` owns generic envelopes, retry/error helpers, runtime primitives, and transport-facing contracts, but does not make Balda-specific product policy decisions.
- Delivery boundaries are explicit: `deliverycmd` owns transport-neutral delivery contracts, `deliveryfmt` owns the immutable format registry and transport-capability routing, `deliveryfx` supplies process-local formatter registrations, `locatorref` owns public locator parsing/formatting, and `channel/*` owns concrete provider delivery and transport-local presentation behavior behind DI boundaries.
- Channel transport boundaries are explicit: `internal/apps/balda/channel/*` packages (`telegram`, `zulip`, `slackagent`) own provider-specific transport carriers and presentation, exporting narrow ports (`InboundHandler`, `InboundProcessor`). Composition and DI wiring are isolated in composition roots (`telegramfx`, `zulipfx`, `slackagentfx`, `handlersfx`) which bind ingress, session, and application services without leaking application domain policy into transport packages.
- Presentation routing is explicit: model-authored text stays on the prompt-formatting path, while system-authored service messages use typed structured message contracts with deterministic per-transport renderers. Where those structured messages are durable/runtime-facing contracts, their schema identity belongs in AsyncAPI rather than in channel formatter registrations.
- Slackagent behavior lives in a dedicated `slackagent` path with its own ingress and response contracts. `internal/apps/balda/channel/slackagent` owns the channel boundary, and `internal/apps/balda/channel/slackagent/slackagentfx` is the composition/DI boundary exported to the rest of the app. See [Slackagent mode](slack-agent-mode.md).
- Session boundaries are explicit: `session` owns create/restore/reset/lifecycle semantics and may consume shared delivery contracts, but it must not become the home of transport delivery contract types.
- Adapter boundaries are explicit: transport/use-case integrations should prefer package-local ports with composition-root adapters instead of reaching directly into concrete runtime or transport implementations.
- Ingress construction is fail-fast: formatting/registry validation and all downstream runtime dependencies must resolve before any ingress lifecycle stage can accept work.
- Telegram polling settlement is explicit: the provider-owned offset boundary advances only after accepted or terminal event processing; retryable handler outcomes preserve the previous offset for stable-ID replay, while webhook settlement remains request-local.

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
  - Telegram, Slackagent, Zulip, webhook, and scheduler ingress in `internal/apps/balda/handlers`; ingress publishes commands and does not register product actors. Concrete provider bindings live in `internal/apps/balda/handlersfx`.
  - Concrete transport adapter semantics: command stream, ack/nak/term behavior, heartbeats, in-progress redelivery, exposed upward only as actorlayer source/delivery and small Balda-facing dispatch/event interfaces.
  - Retry strategy and classification, dead-letter promotion logic, and DLQ reporting.
  - Job state in `execution_jobs`, transactional event publication intent in `execution_job_event_outbox`, and idempotent history projections in `execution_job_events`.
  - Internal command visibility backed by logs and tooling.
  - Mapping between policy metadata (`chat_id`, `topic_id`, `goal_id`, `attempt`) and actor-level envelopes.
  - The single app-scoped provider runtime selected by `balda.provider`.

- Internal Balda runtime decomposition:
  - `host.go`: host loop and dispatch-runtime wiring.
  - `lane_policy.go`: Balda actor addressing and lane-key policy.
  - `heartbeat.go`: Balda heartbeat cadence and in-progress visibility publication.
  - `deadletter.go`: Balda retry-exhaustion and job dead-letter side effects.
  - `delivery_wrapper.go`: Balda delivery wrapping and envelope-context attachment.

- Boundary obligations:
  - Actor definitions and actor state must not select or branch on provider IDs.
  - Provider-specific types stay outside actorlayer-facing contracts.
  - Transport settlement is hidden behind actorlayer delivery methods and exposes the same lifecycle outcomes regardless of command kind.
  - Shared transport-neutral types must live in dedicated contract packages, not inside concrete adapter packages.
  - Concrete transport adapters must not import application lifecycle/use-case packages just to reuse locator/profile/progress types.
  - Transport-specific rendering, prompt injection, and provider correlation rules must stay inside the owning `channel/*` subtree rather than in shared application presentation packages.
  - Outer packages must not import `channel` implementation subpackages directly; composition should flow through root channel contracts and explicit DI modules such as `slackagentfx`.
  - Public locator parsing/formatting must not require importing concrete transport adapter packages.
  - Bundled session MCP tools reconstruct canonical provider address JSON from `channel_type` and `address_key`; callers do not need to supply compatibility `address_json`.

## Related tests

- `internal/apps/balda/execution/config_test.go`
- `internal/apps/balda/eventbus/config_test.go`
- `internal/apps/balda/application_lifecycle_test.go`
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
