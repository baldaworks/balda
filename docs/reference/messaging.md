# Session and messaging behavior

## Session model

Session key:

- Owner session: owner DM `(chat_id, topic_id=0)`
- Regular session: any other channel address `(chat_id, topic_id)`, including public `topic_id=0`
- Canonical Balda session IDs are channel-scoped. Telegram uses `tg-<chat_id>-<topic_id>`; Slack uses stable hashed IDs derived from DM or thread locators.
- The owner session is bootstrapped for the bound owner DM chat (`topic_id=0`) during activation/startup when an owner is already registered.

Balda always persists session metadata in `state.db` for lazy restore.
By default, Balda also persists session history and state in `state.db` until the session is explicitly closed. Set `balda.sessions.persistence=memory` to keep conversation/runtime state process-local while retaining Balda session metadata for lazy restore.

## Message flow

Ordinary conversational chat transports follow the same runtime model.

1. User sends a chat message through Telegram, Slack, or Zulip.
2. The ingress handler normalizes provider data and resolves the Balda session locator for that chat/thread/topic.
3. If the session is missing in memory, Balda attempts lazy restore from persisted metadata.
4. Conversational ingress publishes exactly one durable `balda.v1.cmd.session` envelope directly to the session actor lane for the normalized inbound item.
5. The session actor runs the configured provider runtime for that session and emits separate delivery envelopes for progress/final replies.

Telegram-specific message selection rules:

- In non-DM chats (groups/supergroups/topics), Balda processes a message when it contains a mention entity for `@<bot_username>` or is a reply to this bot's message.
- For processed replies, balda forwards selected Telegram quote text first, then replied message `text`, `caption`, or readable rich-message text/captions as model context, plus the new user message when present.
- In DM chats, Balda processes non-command text messages normally and preserves reply context for reply messages.

## Telegram messaging behavior

Per model turn:

1. Non-terminal session progress always emits a progress delivery. Telegram treats any progress delivery as activity for the same chat/topic and sends typing indicators; only progress deliveries marked visible render as drafts/messages. DM chats may also emit thinking placeholders. When `balda.telegram.plan_updates=true`, work-plan snapshots are visible, replace generic DM thinking placeholders, and are sent as plain-text progress messages in public chats/topics. When the flag is `false`, plan progress stays hidden while typing activity still flows.
2. Final assistant text uses `balda.telegram.formatting_mode`:
   - `rich_markdown`: model writes Markdown/plain text; Balda sends Telegram rich Markdown.
   - `rich_html`: model writes rich-message HTML; Balda sends Telegram rich HTML.
   - `none`: Balda sends literal text without Telegram `parse_mode`.
3. If Telegram explicitly rejects rich formatting, Balda makes at most one parse-mode-free plain send. Ambiguous transport failures, timeouts, authentication errors, rate limits, and provider errors do not trigger presentation fallback.

## Slack Agent messaging behavior

Slackagent keeps ingress, correlation, rendering, and delivery inside the
`channel/slackagent` subtree.

1. Signed human `message.im` and channel `app_mention` events use
   `(team_id, channel, root_ts)` as the stable thread locator; bot and subtype
   events are ignored.
2. Every channel turn requires a fresh `app_mention`, including turns in an
   existing Balda thread. Ordinary `message.channels` and `message.groups`
   events are ignored regardless of persisted session state.
3. A mention in any accessible existing channel thread loads a bounded
   `conversations.replies` snapshot before the mention. Slackagent keeps author
   provenance, marks the snapshot as untrusted background, and dispatches only
   the fully enriched transport-neutral session turn. DM turns do not perform
   this history read.
4. Accepted turns set the Agent Session to `processing` and apply an initial
   title. Completion returns it to `active`, user-intervention waits use
   `suspended`, and session close uses `closed`.
5. Ordinary replies use `chat.postMessage`; when enabled, streaming uses the
   Slack start/append/stop lifecycle in the same thread.
6. `agent_session_stopped` cancels active work for the matching Balda session.

The required conversational bot events are `message.im`, `app_mention`, and
`agent_session_stopped`. `channels:history` enables public-thread context;
`groups:history` is needed only for private-thread context. User-token Slack
search/MCP capabilities do not participate in this ingress path.

See [Slack Agent setup](../slack.md) for scopes, subscriptions, configuration, and
sandbox validation.
