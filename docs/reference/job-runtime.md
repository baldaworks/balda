# Job, scheduler, and webhook runtime

## Job runtime semantics (internal)

Assignable job work is persisted in `execution_jobs`. Each job state mutation atomically appends its publication intent to
`execution_job_event_outbox`; the publisher sends it to `BALDA_EVENTS`, and the idempotent projector builds `execution_job_events`. Ingress publishes a
durable command first; job records are created after command delivery.
Ordinary conversational turns from Telegram, Slack, and Zulip do not create
`execution_jobs` rows; they run directly on the session actor path.

- `/goalkeeper` starts goal work for the current session context. Balda restores or creates the
  chat session, allocates separate GoalKeeper worker/validator ADK sessions for the
  job, runs repeated work and validation passes, passes only the latest worker and
  validator results across those role sessions, exports successful work back to the
  base branch when workspace mode is enabled, records `not_exported` when workspace
  mode is disabled, records the job result, and sends progress/final messages.
- Job statuses are `created`, `queued`, `running`, `waiting_for_agent`,
  `waiting_for_user`, `validating`, `completed`, `failed`, `canceled`, and
  `deadlettered`.
- Job events are append-only durable transport events projected into SQLite read
  models. Job state plus outbox enqueue is one SQLite transaction; transient
  event publication is retried after restart. Event projection failure never
  decides command success. Semantic
  event types include `job.created`, `job.assigned`, `job.started`,
  `agent.started`, `agent.progress`, `agent.result`, `job.validating`,
  `job.completed`, `job.failed`, `job.canceled`, `delivery.sent`, and
  `delivery.failed`.
- Runtime deadletters mark the owning job `deadlettered`. Session control
  commands and internal control envelopes publish durable control work.
  `/cancel` stops the current session turn and clears queued turns for that
  session. `/goalkeeper clear` marks active goal jobs `canceled` and stops any
  currently running GoalKeeper job for that session.
- Terminal job delivery stores reviewable outcomes and, when applicable,
  sends concise result, export, work, validation, and actionable next-step
  sections. Artifacts are best-effort
  workspace data from the bound session: changed files, branch, current commit,
  workspace export hint, and validation output.
- Job progress/results and projected event payload summaries redact common
  secret patterns (for example bearer tokens, `token=...`, `password=...`,
  Telegram bot tokens, and PEM private keys) before persistence and delivery.
- Job records and projected job events remain internal runtime/operator data.
  Telegram does not expose direct job inspection or per-job control commands.

## Scheduled job runtime semantics (internal)

Balda includes an internal scheduler backed by `balda_scheduled_jobs` and a
durable run ledger in `balda_schedule_runs`. Recurring definitions have two
owners: host configuration (`balda.scheduler.jobs`) and Backoffice. Each has
an ID, a five-field UTC cron expression, a target/key, content, and optional
report destination. Backoffice creates managed definitions through the
administrator-only [Schedules page](backoffice.md#schedules-management).
Internal one-shot `@once` timers share the job store but are never listed as
recurring schedules.

- Eligibility: only enabled, non-deleted, `status=active` recurring jobs with
  `next_run_at <= now` are selected. A manual run can be requested for either
  source without changing the cron cursor; a disabled job requires explicit
  confirmation.
- Dispatch path: admitted runs retain an exact definition snapshot and resolve
  the envelope target by `target`/`key`, persist its canonical locator
  (`channel_type`, `address_key`, `address_json`, `session_id`), and publish a
  durable job command. Session restore and execution happen after command
  delivery. Pending or retrying runs survive restart.
- Locator target form: `target=locator`, `key=<channel_type>:<address_key>`;
  `/locator` returns the paste-ready value in a transport-formatted structured
  response. See the [locator command contract](../commands.md#locator).
- Alias target form: `target=alias`, `key=owner` (or `owner@<channel_type>`);
  resolves through the transport-neutral destination resolver across active
  channels (Telegram, Slack, Zulip) with deterministic default selection and
  fallback to registered Telegram owner data.
- Delivery: scheduled jobs are fire-and-forget by default. If `envelope.report_to` is set, the session turn delivers progress/final replies to that locator.
- Idempotency key: each due slot uses deterministic `last_dispatch_key = <job_id>@<due_next_run_at_rfc3339nano>`. A manual request has a separate request key; retrying the same request returns the same run.
- Startup reconciliation: configuration updates only config-owned rows. A removed
  config entry is archived for retained history; managed rows survive unchanged.
  A configured ID colliding with a managed or internal ID aborts startup.
- Publication: a due slot is admitted with a version and selection check before
  publication. A publishing lease allows recovery after a crash, using the same
  dispatch key. Successful publication advances the cron cursor only while its
  selected version still matches. An edit, disable, or delete cannot be undone
  by a concurrent dispatch. Manual runs never advance that cursor.
- Success after actor execution: `last_run_at` is updated, `last_error` is cleared, `retry_count` is reset to `0`, and the job remains `active`.
- Pre-publication failure: the run ledger records a safe failure code and increments
  that run's attempt count. Retryable failures wait 1, 2 and 3 seconds before
  further attempts; the next failure is terminal. Only a terminal failed cron
  run updates the still-selected definition's `retry_count` and `last_error`
  and pauses it. Manual-run failures leave recurring definition diagnostics and
  cursor unchanged.
- Execution failure after transport delivery: `last_run_at` and `last_error` are recorded for visibility, but scheduler retry fields and `next_run_at` are not changed. Transport owns command retry, redelivery, and DLQ after publish.
- Run history is newest first and retains both scheduled and manual attempts,
  including failures before publication and terminal execution outcomes.
  Archived schedules retain readable history. Browser views expose bounded
  status/failure labels, never raw provider errors or instruction content in
  inventory.

## Inbound webhook contract (internal)

Balda can optionally expose local webhook routes that map path -> route envelope.

- Endpoint config: `balda.webhooks.enabled`, `listen_addr`, `routes`.
- Security:
  - each route can require shared-header auth (`auth.type=header`, `auth.header`, `auth.value|secret_env`)
  - keep the endpoint private or protected by a trusted gateway even with route auth
- Method: `POST` only.
- Route resolution:
  - request path must match a configured route `path`
  - destination comes from route `envelope.target` + `envelope.key` (default `alias:owner`)
  - authenticated routes may use `envelope.key_from_body` to read a top-level JSON string instead of a fixed `key`; `target=session` resolves that session ID to its persisted locator
  - `envelope.report_to` supports the same `key_from_body` field, so replies return to the source session
  - `envelope.fallback_to` may name a fixed `alias` or `locator` for authenticated, body-sourced session routes; it is used only when the source session is missing or inactive
  - `target=locator` accepts `<channel_type>:<address_key>` in `key`; obtain the
    current value from the [locator command](../commands.md#locator)
  - route `envelope.mode` decides publish target:
    - `job` (default): publish webhook job command; job execution later emits the session command
    - `session`: publish session command directly

For a trusted event source that includes the originating session ID:

```yaml
balda:
  webhooks:
    routes:
      broker_events:
        path: /webhook/broker-events
        prompt_template: '{{ .RawBody }}'
        auth:
          type: header
          header: Authorization
          secret_env: BROKER_WEBHOOK_AUTHORIZATION
        envelope:
          target: session
          key_from_body: chat_id
          mode: job
          ack_on_delivery: true
          report_to:
            target: session
            key_from_body: chat_id
          fallback_to:
            target: alias
            key: owner@mattermost
```

- Prompt generation:
  - request body is treated as opaque raw text unless a route uses `key_from_body`, which requires a JSON object
  - route `prompt_template` is rendered with `RequestID`, `Path`, `Method`, `RawBody`, `Headers`
  - rendered prompt must be non-empty
- Session resolution:
  - ingress resolves route target locator and user id from owner store aliases
  - ingress publishes a durable command after prompt rendering
  - job mode: the job command later emits the session command for execution
  - session mode: ingress command is already a session command
  - the runtime lazily restores the persisted session when inactive in memory and creates the owner session when no persisted session exists
  - webhook acceptance therefore depends on transport publish, not on synchronous session restore
  - uses `deliver=false` by default; route `envelope.report_to` enables progress/final delivery to that destination
- Dedupe:
  - default source is `request_id`
  - `dedupe.source=header` uses `dedupe.header` value when present
  - `dedupe.source=body_sha256` uses body hash
- Response model (JSON):
  - accepted: `202` with `{status:"accepted", accepted:true, request_id, message_id, duplicate?}`
  - with `ack_on_delivery=true`, `202` means queued or delivery pending; repeat the same idempotent POST until `200` with `status:"delivered"`, `job_id`, and `provider_message_id`
  - `200` is returned only after the final reply's durable outbox entry records a provider message ID
  - route not found: `404` + `error.code="route_not_found"` + message `could not accept request`
  - invalid method: `405` + `error.code="invalid_method"` + message `could not accept request`
  - auth reject: `401` + `error.code="unauthorized"` + message `could not accept request`
  - invalid body/template render: `400` + `error.code="invalid_payload"` + message `could not accept request`
  - unresolved/restore-failed session: `404` + `error.code="session_not_found"` + message `could not accept request`
  - queue pressure: `429` + `error.code="queue_full"` + message `temporarily busy`
  - transport publish/internal failures: `503` + `error.code="dispatch_failed"` + message `temporarily busy`
- Observability:
  - logs keep request routing and transport metadata internal; public responses stay limited to request id, message id, status, acceptance, and stable error code/message values
  - internal outcome counters track accepted, invalid, not-found, queue-full, and dispatch-failure events
