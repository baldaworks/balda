# Session and messaging behavior

## Session model

Session key:

- Owner session: owner DM `(chat_id, topic_id=0)`
- Regular session: any other channel address `(chat_id, topic_id)`, including public `topic_id=0`
- Canonical Balda session IDs are channel-scoped. Telegram uses `tg-<chat_id>-<topic_id>`; Slack uses stable hashed IDs derived from DM or thread locators.
- The owner session is bootstrapped for the bound owner DM chat (`topic_id=0`) during activation/startup when an owner is already registered.

Balda always persists session metadata in `state.db` for lazy restore.
By default, Balda also persists session history and state in `state.db`. Exact
restore reuses them after restart. Explicit `/reset` or `/close`, and successful
automatic fresh-session fallback after exact restore fails, clear prior ADK
state and history. Set `balda.sessions.persistence=memory` to keep
conversation/runtime state process-local while retaining Balda session metadata
for lazy restore.

## Message flow

Ordinary conversational chat transports follow the same runtime model.

1. User sends a chat message through Telegram, Slack, or Zulip.
2. The ingress handler normalizes provider data and resolves the Balda session locator for that chat/thread/topic.
3. The common conversational service prepares the session through a shared
   transport-neutral adapter. The session manager returns an active session,
   restores persisted metadata/history, or creates only when metadata is absent.
   Owner and collaborator DMs use this same path. Temporary preparation errors
   retain retry settlement rather than dropping the inbound message.
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

## Original conversational input snapshots

Chat transports preserve current human input separately from the model prompt.
The snapshot retains original text, stable inbound identity, the actual known
speaker, canonical public location, and independent audience and subdivision
metadata. Unknown names/classifications stay unknown. Telegram captures before
attachment persistence and quote/forward enrichment; Slack captures before
current-file ingestion and historical thread hydration; Zulip uses raw webhook
message data before mention cleanup; Mattermost preserves the original post and
its D/G channel distinction. A personal conversation can also have a topic or
thread. Group input keeps the individual sender rather than the runtime owner.

Attachment snapshots contain filename/kind labels only. Local paths, private
URLs, file bytes and hydrated historical attachments remain outside them.
Known channel titles and message-sender names are copied from trusted inbound
metadata, with no extra provider lookup. Subsequent model prompt enrichment
uses its existing text and attachment fields and does not modify the original
snapshot. Recording, lifecycle attribution and replay are owned by the archive
application boundary rather than these concrete transport adapters.

## Durable conversational input admission

After access checks, successful session preparation, and explicit skill
validation, intake commits the original human snapshot to the shared SQL archive
before publishing the session turn. Archive storage failure prevents publication
and retains retry settlement. A later broker failure, cancellation, dropped
queue entry, or turn-preparation failure does not erase this admitted input;
an archived bubble does not imply successful execution or delivery.

Provider input identities deduplicate across runtime lifetimes. A repeated
observation retains its first timestamp, immutable content and archive UUID;
contradictory content fails rather than overwriting history. A retry observed
after reset cannot dispatch into the replacement lifetime. Durable turns and
each steering constituent carry the frozen epoch; queued execution checks the
runtime epoch before invoking the provider, and steering batches cannot merge
inputs from different lifetimes.

Typed `/skill` commands retain the literal request before selector rewriting.
Conversational free-text question answers commit after responder/answer
validation and before durable settlement, using the question and provider
message identity. Questions freeze their originating lifetime when created;
resumed session work retains that binding. Button choices do not fabricate user
text, and GoalKeeper/private continuations and authentication/control commands
remain outside conversational capture. Input snapshots fill only previously
unknown location fields; later renames cannot rewrite historical labels or
per-message speakers.
