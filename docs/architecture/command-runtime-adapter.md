# Command Runtime Adapter

Owner: Balda maintainers  
Status: active

## Invariants

- Command delivery uses one durable command transport.
- Event projection and lifecycle history use dedicated durable event streams.
- Terminal command failures are retained for inspection and replay decisions.
- Command and event processing use explicit settlement.
- Command subjects stay under `balda.v1.cmd.*`; events under `balda.v1.evt.*`.
- Subject/header/namespace definitions live canonically in `internal/apps/balda/actorcmd`; `execution` re-exports them as the runtime-facing compatibility facade while consuming them for host policy.
- Product/runtime packages consume actorlayer `Source`/`Delivery` and
  actorlayer transport dispatcher abstractions, not transport APIs directly.
- `balda.v1.cmd.command` targets the independent CommandActor. Its payload is
  `commandcmd.Payload`, its address key is the canonical session ID, and its
  router selects an immutable exact-name handler table.
- Concrete transports own command parsing and explicit whitelists. Ingress
  resolves access and publishes. Actor handlers own behavior. `commandfx`
  contains only registration and port wiring.

## Current migration scope

`locator` and `reset` use CommandActor in Telegram, Zulip, and Slack. The Slack
surface is `/balda locator|reset`. Remaining commands keep their existing
execution path until migrated by separate stories; this document does not
claim that migration is complete.

## Related tests

- `internal/apps/balda/eventbus/nats/connection_test.go`
- `internal/apps/balda/execution/host_test.go`
- `internal/apps/balda/execution/config_test.go`
- `internal/apps/balda/handlers/inbound_webhook_test.go`

## Related packages

- `internal/apps/balda/eventbus/nats`
- `internal/apps/balda/execution`
- `internal/apps/balda/handlers`
- `internal/apps/balda/commandcmd`
- `internal/apps/balda/actors/command`
- `internal/apps/balda/commandfx`

## Update triggers

- Transport config changes.
- Subject taxonomy or envelope/header changes.
- Publish/consume settlement behavior changes.
