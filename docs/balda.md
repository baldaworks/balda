# Balda (V1)

Balda is a self-hosted autonomous engineering worker in team chat.

`balda start` runs the channel-aware background service that binds team chat
sessions to autonomous Balda worker sessions.

Architecture contracts are maintained in:

- `docs/architecture/index.md`

## Summary

- Runtime stack: one or more channel runtimes (Telegram, Zulip, Slack) plus the configured Balda provider runtime.
- Supported channels: Telegram (polling or webhook), Zulip (outgoing webhook), and Slack Agent DMs/channel threads (signed HTTP Events API).
- Main agent: Balda app key `balda.provider` (profile overrides via `profiles.<profile>.balda.provider`).
- Subagents: one session per channel topic/thread with dedicated git worktree.
- Balda startup prompt includes workspace settings for each session; in git workspace mode it also includes session/base/current-branch context and workspace MCP guidance.
- Output streaming:
  - Progress updates: non-terminal provider progress emits channel progress. Telegram maps this to throttled typing indicators for all chats, plus DM-only thinking placeholders.
  - Final assistant response uses `balda.telegram.formatting_mode` (`rich_markdown|rich_html|none`; default `rich_markdown`; `none` is literal plain text).
- Auth model: one-time owner authorization with startup-generated token.

## Technical reference

The reference is organized by topic so that each page has a stable scope and
can be read, linked, and indexed independently:

- [User onboarding](reference/onboarding.md) — startup, authorization, and the
  first operator checks, including `task runtime-state`.
- [Internal packages and startup architecture](reference/internals.md) — owned
  packages, dependency boundaries, architecture layers, and startup order.
- [Configuration](reference/configuration.md) — the complete runtime, provider,
  channel, workspace, scheduler, webhook, and MCP configuration contract.
- [Durable session memory](reference/session-memory.md) — lifecycle, storage,
  recall, compaction, observability, and the operator verification runbook.
- [Session and messaging behavior](reference/messaging.md) — session keys,
  message flow, and channel-specific behavior.
- [Topic sessions](reference/topic-sessions.md) — topic lifecycle, locators,
  restore behavior, and workspace isolation.
- [Job, scheduler, and webhook runtime](reference/job-runtime.md) — durable job
  execution, scheduled delivery, and inbound webhook behavior.
- [Command runtime internals](reference/command-runtime.md) — command delivery,
  retries, projections, queues, replay boundaries, and runtime inspection.
- [Operations and verification](reference/operations.md) — troubleshooting,
  workspace MCP verification, and acceptance checks.

Focused guides remain separate from the technical reference, including
[Telegram message formatting](telegram-formatting.md),
[Slack Agent setup](slack.md), and the [command reference](commands.md).
