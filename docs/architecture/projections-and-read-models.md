# Projections and Read Models

Owner: Balda maintainers  
Status: active

## Invariants

- SQLite job history is a read model projected from durable events.
- Every JobService state transition atomically writes its publication intent to `execution_job_event_outbox`; transient event-stream failure cannot lose the semantic event.
- The outbox publisher retries pending events, while `execution_job_events` remains an idempotent projection of `BALDA_EVENTS`.
- Projection failures do not stop command execution.
- Projection handlers are idempotent.
- Internal task/read-model views read product state + projections; they are not chat commands.

## Related tests

- `internal/apps/balda/jobs/service_test.go`
- `internal/apps/balda/handlers/command_test.go`
- `internal/apps/balda/memory/store_test.go`

## Related packages

- `internal/apps/balda/jobs`
- `internal/apps/balda/state`
- `internal/apps/balda/handlers`

## Update triggers

- Event schema/version changes.
- Read-model schema changes.
- New internal projection consumers or operator inspection tooling.
