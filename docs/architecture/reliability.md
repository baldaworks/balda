# Reliability

Owner: Balda maintainers  
Status: active

## Invariants

- Delivery is at-least-once at transport level and idempotent at side-effect level.
- Retry policy and max deliver behavior are explicit and observable.
- DLQ entries include enough context for diagnosis and replay planning.
- User-visible delivery paths remain transport-durable; provider-side dedupe/outbox policy depends on the ingress/runtime path.
- Concrete channel adapters classify provider failures as retryable, permanent, or ambiguous at the transport-neutral `deliverycmd` boundary. Transport timeouts, connection resets, server errors (>= 500), and empty provider responses classify as ambiguous (`deliverycmd.ErrorKindAmbiguous`) because provider acceptance cannot be proven. Unknown legacy errors retain the existing bounded external-delivery retry behavior.
- For durable outbox-backed deliveries, an ambiguous provider outcome retains the `sending` status in `execution_delivery_outbox` rather than transitioning to `failed`. Automatic resend across restarts is disabled to prevent duplicate side effects; subsequent attempts observe the `sending` status and fail closed with a transient error without calling the provider.
- If durable delivery is requested but the outbox store is unavailable, the delivery fails closed before contacting the provider.
- Provider runtime log adapters redact credentials before forwarding generated request errors to application logging. Operational diagnostics and DLQ entries record structural error classes, text lengths, and envelope identities, never raw request secrets or provider credentials.
- Ambiguous delivery outcomes for interactive questions do not mark the question failed, ensuring unconfirmed presentations are not prematurely aborted.
- Delivery adapters may retry locally only when the provider explicitly rejected presentation semantics before accepting the side effect, such as a Telegram entity-parse error. Transport timeouts must not trigger a second immediate send through a different presentation path.
- Retried interactive-question delivery rechecks durable question state before every provider side effect. A question that is already answered, timed out, or failed is never presented again by a late command retry.
- Job state transitions atomically enqueue semantic events in `execution_job_event_outbox`; publication is at-least-once with stable envelope IDs and background retry.
- Bypassed/non-outbox conversational paths rely on transport durability without outbox reservations; duplicate suppression is not guaranteed for those paths on lost provider responses.
- Operational recovery for retained `sending` deliveries: operators inspect chat/channel history and local outbox timestamps/keys. Balda does not perform automated provider receipt reconciliation or provide an ad-hoc recovery CLI; deliberate manual resubmission carries an explicit risk of message duplication.

## Related tests

- `internal/apps/balda/execution/host_test.go`
- `internal/apps/balda/actors/delivery_actor_test.go`
- `internal/apps/balda/deliveryworkflow/service_test.go`
- `internal/apps/balda/channel/telegram/messenger_test.go`
- `internal/apps/balda/eventbus/nats/connection_test.go`
- `internal/apps/balda/handlers/command_test.go`
- `internal/apps/balda/jobs/service_test.go`
- `internal/apps/balda/state/sqlite_jobs_test.go`

## Related packages

- `internal/apps/balda/execution`
- `internal/apps/balda/jobs`
- `internal/apps/balda/actors`
- `internal/apps/balda/deliverycmd`
- `internal/apps/balda/deliveryworkflow`
- `internal/apps/balda/channel/telegram`
- `internal/apps/balda/eventbus/nats`
- `internal/apps/balda/handlers`
- `internal/apps/balda/state`

## Update triggers

- Error taxonomy or retry classification changes.
- Outbox/dedupe storage changes.
- DLQ schema or inspection command changes.
