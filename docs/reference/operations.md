---
title: Balda operations
description: Verify, troubleshoot, update, and recover a Balda deployment.
category: Operations
audience: operator
status: stable
outline: deep
---

# Operations and verification

## Troubleshooting

- Startup fails while initializing built-in runtime streams: keep the default `balda.nats` settings unless you have a specific local runtime need, ensure `${balda.state_dir}/nats` is writable, and verify local disk limits.
- Startup fails while initializing built-in runtime consumers: verify `balda.execution.commands.consumer` uniqueness and avoid concurrent writers against the same embedded NATS store dir.
- Rising command backlog or redelivery counts in transport metadata usually means retrying or deadlettering work; inspect lifecycle events, DLQ stream contents, and logs before increasing `max_ack_pending` or `fetch_batch`.
- Webhook ingress returns `503 dispatch_failed`: confirm transport startup succeeded and command publish acknowledgements are being returned.

## Workspace MCP usage

- `balda.workspace.import`
  - rebases the session workspace onto the configured base branch
  - works for active or persisted sessions as long as workspace metadata exists in `state.db`
  - is the explicit retry path when restart restore skipped base sync because of a conflict
- `balda.workspace.export`
  - squash-merges the session workspace branch into the configured base branch with the provided Conventional Commit message
  - also works for persisted sessions before lazy restore
- Cleanup/export contract is explicit:
  - `/close`, session stop, and process shutdown clean up the mounted worktree path when workspace mode is enabled
  - cleanup does **not** auto-export branch changes into the base branch
  - exporting branch changes is an explicit operator action via `balda.workspace.export`

## Acceptance and verification scenarios

1. Startup order brings up internal MCP, durable runtime, Backoffice, and
   enabled transports without resolving the selected agent provider. The first
   provider-backed operation builds it lazily.
2. Polling mode starts by default when `balda.telegram.webhook.enabled=false`.
3. Webhook mode (`balda.telegram.webhook.enabled=true`) fails fast without `balda.telegram.webhook.url` or `balda.telegram.webhook.auth_token`.
4. `/start owner=<token>` registers owner once; `/start invite=<token>` onboards collaborators; `/start <balda_token>` binds a generated channel token to the existing owner; users who are neither owner nor collaborator are otherwise rejected.
5. `/topic <name>` creates topic + Balda session and persists session metadata.
6. `/topic` without name returns usage error.
7. Restart clears active process sessions; every persisted session, including
   the owner's main DM, is restored lazily when addressed again. Startup
   readiness delivery creates no conversational session.
8. Polling mode resumes from persisted Telegram offset in balda state DB.
   Retryable polling intake failures leave that offset unchanged and replay the
   same provider update after restart or the next poll cycle; accepted and
   terminal outcomes advance it.
9. Non-terminal session progress always emits progress activity; Telegram sends typing indicators in DM and public chats for that activity, while only visible progress renders as drafts/messages. Thinking placeholders remain DM-only.
10. Final assistant response uses configured `balda.telegram.formatting_mode`; `none` is literal plain text, while rich modes make at most one parse-mode-free plain send only after an explicit Telegram formatting rejection.
11. `/reset` and its `/new` alias cancel current session work, clear history, and immediately start a fresh runtime session in any supported chat/thread context without closing the underlying chat/topic.
12. `/locator` returns the exact current session locator in
    `<channel_type>:<address_key>` form through the registered structured
    renderer and explicit transport format, with no fallback on render failure.
13. `/close` in a topic resets history and closes that topic; `/close` in a DM main chat resets that chat's current main session.
14. With `balda.sessions.persistence=sqlite`, an exact process-restart restore
    preserves conversation history. Explicit `/reset` or `/close` clears it;
    failed exact restore also clears prior ADK history when automatic
    fresh-session fallback succeeds.
15. `balda eval-fixtures` validates deterministic scenario fixtures in `testdata/scenarios` and checks golden event manifests; use `--scenario` and `--actual-events` for event-type comparison.

## Command admission recovery and NATS storage budgets

For the shared Mattermost chat handler, commandfx ingress and Telegram question
continuations (text replies and inline choices), a successful
receipt means the local state database owns the queued
command. NATS failure after that point is deferred publication, not a failed
user request. Do not ask the user to resubmit an admitted command; inspect the
`command-outbox/pending/` records and publication logs. Recovery republishes the
original envelope, with its original identity. This is an at-least-once handoff;
execution consumers must retain their existing command-identity deduplication.

Question settlement saves `answer_continuation_pending` and
`answer_controls_pending` with the answer in the same SQL mutation. The
`jobs/questionoutbox` worker runs at startup and periodically, admitting the
saved continuation without another inbound update. Its question-based envelope
ID and deduplication key stay stable across retries and restarts. After a local
receipt, it records admission separately and queues control cleanup through the
same durable outbox. A failed cleanup cannot resubmit the admitted continuation.
Late delivery binding reopens cleanup only; an old delivery ref cannot complete
cleanup for a newly bound message. Invalid answers are logged and retained while
the bounded recovery cursor continues with other IDs.

Telegram inline choices use the same local admission boundary before acknowledging
the choice as selected. The pinned Telegram webhook acknowledges HTTP intake
before the handler finishes; answer recovery therefore does not depend on
Telegram redelivery, callback buttons or a repeat text message. HTTP 200 by
itself still means transport intake, not SQL settlement. Once the answer is
saved, its SQL handoff survives failure and restart. A repeated selection or
original text message remains safe, and cannot replace the saved answer. NATS
publication happens in the background after admission.

Take a consistent state backup before applying SQLite migration 63 / PostgreSQL
migration 28. Existing answered rows are left settled without automatic replay,
because their historic publication is unknown. Recovery applies to answers
settled after this upgrade. Downgrade uses the matching database backup.

Pending commands expire after at most 24 hours. Expired payloads are removed;
terminal receipts/tombstones are collected after seven days. Recovery logs an
expired unpublished command; local admission is not a promise of execution or
a terminal task result. Deduplication of the same inbound identity is guaranteed
only within the documented retention window. Back up AppKV with the state DB.

Existing installations keep `balda.nats.bounded_streams: false`. To opt in:

1. Stop admission, take a consistent backup of state and NATS storage, and inspect
   each stream's retained bytes through the configured monitoring/operator tools.
2. Select `max_store` so all current streams fit their proposed budgets. If
   history must be reduced, archive it through a separate explicit operator
   action first. Enabling this setting never silently archives or deletes it.
3. Set `balda.nats.bounded_streams: true`, restart, and verify command admission,
   event retention and pending recovery. A cap below retained data rejects startup
   before that stream is reduced.
4. If activation is rejected, set `bounded_streams: false` and restart with the
   prior resource configuration. Rejection preserves existing history; it does
   not require deleting streams or reinitializing application state.

A pre-admission transport/DB failure may still produce a neutral Mattermost
receipt-status message in the original thread. Private diagnostics stay in logs.
A persisted command with an unavailable NATS publisher follows the accepted path
and does not produce that failure message.
