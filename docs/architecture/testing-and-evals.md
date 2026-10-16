# Testing and Evals

Owner: Balda maintainers  
Status: active

## Invariants

- Architecture contracts are enforced by `go-arch-lint`.
- Built-in runtime integration coverage remains first-class.
- Reliability scenarios cover redelivery, retry, and terminal failure flows.
- Projection and runtime behavior are testable without external infra.

## Related tests

- `internal/apps/balda/eventbus/nats/connection_test.go`
- `internal/apps/balda/execution/host_test.go`
- `internal/apps/balda/application_lifecycle_test.go`
- `internal/apps/balda/actors/turn_dispatcher_test.go`
- `internal/apps/balda/jobs/service_test.go`
- `internal/apps/balda/channel/webhook/receiver_test.go`
- `internal/apps/balda/handlersfx/telegram_command_test.go`

## Related packages

- `internal/apps/balda/eventbus/nats`
- `internal/apps/balda/execution`
- `internal/apps/balda/jobs`
- `internal/apps/balda/channel/webhook`
- `internal/apps/balda/handlersfx`
- `internal/apps/balda`

## Update triggers

- New ingress channels.
- New actor types or lifecycle stages.
- Changes to command/event subjects, settlement, or projection rules.

## Chat session archive evidence

[The Backoffice QA runbook](../reference/backoffice-ui-review.md#chat-session-archive-layout-gate)
defines the ordered synthetic-layout then authenticated-application browser
gates. `TestChatArchiveSharedProviderLifecycleAndTurns` joins the real selected
SQLite provider, ADK session storage, deterministic provider, session manager,
original-input admission and committed-final execution/replay. It covers durable
reopen, memory boot replacement, reset/close/fallback identity and late committed
replay without another model invocation. Tagged shared backend contracts run
against both SQLite and disposable PostgreSQL for storage/authority behavior.
The SQL-only archive reader has no provider or session accessor; production
ports remain enforced by `.go-arch-lint.yml`. All durable requirement/evidence
mapping and independent acceptance live in Beads Story `balda-x22c`.
