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
an ID, a five-field UTC cron expression, content, and an optional report
destination: a public locator or a Backoffice-managed alias.
Backoffice creates managed definitions through the
administrator-only [Schedules page](backoffice.md#schedules-management).
Internal one-shot `@once` timers share the job store but are never listed as
recurring schedules.

- Eligibility: only enabled, non-deleted, `status=active` recurring jobs with
  `next_run_at <= now` are selected. A manual run can be requested for either
  source without changing the cron cursor; a disabled job requires explicit
  confirmation.
- Dispatch path: admitted runs retain an exact definition snapshot and publish
  a durable job command. Execution starts in a new private session derived from
  the run's execution job identity, independent of any recipient chat. Pending
  or retrying runs survive restart.
- Optional report form: omit `envelope.report_to` for no external report, or
  provide `target=locator` with `key=<channel_type>:<address_key>` or
  `target=managed_alias` with a managed name. An existing literal
  `envelope.target=locator` plus `envelope.key` remains accepted. `/locator`
  returns a paste-ready public ref. A locator's external address need not exist;
  an alias may be referenced before its mapping exists.
- Admission selection: cron and manual runs resolve the report reference before
  publication and durably store the concrete selected locator with the run.
  Retargeting or deleting the alias affects future runs only. A missing alias
  creates a failed run without command publication. A failed cron slot advances
  its cursor while the definition stays active; a manual failure changes no
  cron cursor. Resolver storage errors use the bounded retry policy.
- Output and delivery: the execution job stores the provider output, and the
  run snapshot stores its input. If a destination is selected, Balda offers that
  output (or a bounded failure message) through the durable delivery outbox.
  Progress and interactive permission questions never go to the report locator;
  permission requests that require a live conversation fail closed.
  No destination means no external delivery. The private runtime session, its events,
  and its ephemeral workspace branch are deleted after execution and, when
  applicable, final delivery settles, even if workspace mode changed during a
  restart. A canceled run with no queued report
  closes without waiting for a delivery that will never be created.
  An ambiguous external send stays pending and is not automatically retried.
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
  Archived schedules retain readable history. Browser inventory exposes bounded
  status/failure labels without instruction content. Guarded run detail exposes
  the selected concrete locator, frozen input and durable output, regardless of
  report delivery.

## Inbound webhook contract (internal)

Balda can optionally expose local webhook routes that map path -> route envelope.

- Endpoint config: `balda.webhooks.enabled`, `listen_addr`, `routes`.
- Security:
  - each route can require shared-header auth (`auth.type=header`, `auth.header`, `auth.value|secret_env`)
  - keep the endpoint private or protected by a trusted gateway even with route auth
- Method: `POST` only.
- Route resolution:
  - request path must match a configured route `path`
  - optional `envelope.report_to` accepts `target=locator`, `managed_alias`, or
    the existing role `alias`, with a `key`; `/locator` prints a public locator
  - no report destination retains the final job output without external delivery
  - a new request resolves `report_to` when admitted and stores the concrete
    locator in its durable admission row. Duplicates, retries and restarted
    publication use that selection even after an alias changes or is deleted.
    A missing destination rejects new admission without publishing work.
  - every accepted request publishes a JobActor command for a new private
    `wh-` session; it never restores the recipient's conversation session

For an authenticated event source that reports to `main_chat`:

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
          ack_on_delivery: true
          report_to:
            target: managed_alias
            key: main_chat
```

- Prompt generation:
  - request body is treated as opaque raw text
  - route `prompt_template` is rendered with `RequestID`, `Path`, `Method`, `RawBody`, `Headers`
  - rendered prompt must be non-empty
- Private execution and delivery:
  - after admission, ingress publishes one durable JobActor command; JobActor
    republishes the stable SessionActor turn if interrupted after job creation
  - immediately before provider invocation, the private turn atomically claims
    its webhook job in durable state. A replay before this claim may start the
    turn; a replay after it cannot invoke the provider again, even beyond the
    broker's duplicate window. If the process stops after the claim but before
    recording output, Balda reports an indeterminate execution failure rather
    than risking a duplicate provider or tool side effect
  - during upgrade, active webhook jobs created before this claim existed are
    treated as indeterminate on replay because prior provider execution cannot
    be proven absent
  - the provider executes in a transient private session independent of the
    selected report locator; progress, session memory, automatic turns and
    interactive questions do not enter this path
  - job input and final output remain in durable job state. Only the final plain
    text output or bounded terminal failure is sent through DeliveryActor and
    the outbox when `report_to` is configured
  - after terminal delivery settles, cleanup removes the private session,
    runtime events and ephemeral workspace state; interrupted cleanup retries
    after restart without resending the report
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
  - unavailable report destination: `404` + `error.code="destination_not_found"` + message `could not accept request`
  - queue pressure: `429` + `error.code="queue_full"` + message `temporarily busy`
  - transport publish/internal failures: `503` + `error.code="dispatch_failed"` + message `temporarily busy`
- Observability:
  - logs keep request routing and transport metadata internal; public responses stay limited to request id, message id, status, acceptance, and stable error code/message values
  - internal outcome counters track accepted, invalid, not-found, queue-full, and dispatch-failure events

Older webhook route fields that selected an execution target (`target`, `key`,
`mode`, `key_from_body`, `fallback_to`) are rejected at startup. Replace them
with optional `envelope.report_to`; the route now executes a new private job
instead of continuing an existing session.
