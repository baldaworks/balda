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

1. Startup order enforces internal MCP -> Balda provider -> bot runtime.
2. Polling mode starts by default when `balda.telegram.webhook.enabled=false`.
3. Webhook mode (`balda.telegram.webhook.enabled=true`) fails fast without `balda.telegram.webhook.url` or `balda.telegram.webhook.auth_token`.
4. `/start owner=<token>` registers owner once; `/start invite=<token>` onboards collaborators; `/start <balda_token>` binds a generated channel token to the existing owner; users who are neither owner nor collaborator are otherwise rejected.
5. `/topic <name>` creates topic + Balda session and persists session metadata.
6. `/topic` without name returns usage error.
7. Restart clears active process sessions; persisted non-owner sessions are lazy-restored from metadata when addressed again, while the owner main-DM session is bootstrapped during startup.
8. Polling mode resumes from persisted Telegram offset in balda state DB.
   Retryable polling intake failures leave that offset unchanged and replay the
   same provider update after restart or the next poll cycle; accepted and
   terminal outcomes advance it.
9. Non-terminal session progress always emits progress activity; Telegram sends typing indicators in DM and public chats for that activity, while only visible progress renders as drafts/messages. Thinking placeholders remain DM-only.
10. Final assistant response uses configured `balda.telegram.formatting_mode`; `none` is literal plain text, while rich modes make at most one parse-mode-free plain send only after an explicit Telegram formatting rejection.
11. `/reset` cancels current session work, clears history, and immediately starts a fresh runtime session in any supported chat/thread context without closing the underlying chat/topic.
12. `/locator` returns the exact current session locator in
    `<channel_type>:<address_key>` form through the registered structured
    renderer and explicit transport format, with no fallback on render failure.
13. `/close` in a topic resets history and closes that topic; `/close` in a DM main chat resets that chat's current main session.
14. With `balda.sessions.persistence=sqlite`, process restart restores conversation history and explicit `/reset` or `/close` clears it for the current session.
15. `balda eval-fixtures` validates deterministic scenario fixtures in `testdata/scenarios` and checks golden event manifests; use `--scenario` and `--actual-events` for event-type comparison.
