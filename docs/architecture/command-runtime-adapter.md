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

## Command routing scope

All supported chat commands (`locator`, `reset`, `help`, `usage`, `auto`, `cancel`,
`goalkeeper`, `topic`, `close`, `start`, `user`, `plugin`) route via the
transport-neutral `CommandActor`. Handlers are organized in scoped families under
`internal/apps/balda/actors/command/<family>` and registered through `commandfx`.
Concrete transports retain only parsing and whitelist enforcement before publishing
`commandcmd.Request` envelopes.

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
