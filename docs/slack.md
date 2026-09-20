# Slack Agent Integration

Balda integrates with Slack's `agent_view` over the signed HTTP Events API.
Balda serves plain HTTP; terminate public HTTPS in a reverse proxy, ingress, or
tunnel and forward the request without changing its body.

## Slack App Setup

1. Create a Slack app and configure it as an agent.
2. Install it to the workspace with `chat:write`, `im:history`,
   `app_mentions:read`, `channels:history`, and `files:read` bot scopes. Add
   `groups:history` only when Balda must load context from private-channel
   threads. The bot must be a member of each public or private channel whose
   thread context it reads. `files:read` is required for files attached to the
   message that starts a turn and for files loaded from preceding thread
   messages.
   Slack adds the agent-specific `assistant:write` scope when the app is declared
   as an agent.
3. Enable Event Subscriptions and subscribe to these bot events:
   - `message.im`
   - `app_mention`
   - `agent_session_stopped`
4. Set the Events Request URL to a public HTTPS URL that forwards to
   `balda.slack.agent.events_path`.
5. Create a `/balda` slash command. Set its Request URL to a public HTTPS URL
   that forwards to `balda.slack.commands_path`.

Slack validates the Request URL with a signed `url_verification` request. The
forwarding layer must preserve the exact raw body and the
`X-Slack-Request-Timestamp` and `X-Slack-Signature` headers.

## Balda Configuration

Environment:

```env
BALDA_SLACK_AGENT_ENABLED=true
BALDA_SLACK_BOT_TOKEN=xoxb-...
BALDA_SLACK_SIGNING_SECRET=...
BALDA_SLACK_AGENT_LISTEN_ADDR=0.0.0.0:8092
BALDA_SLACK_AGENT_EVENTS_PATH=/slack/agent/events
BALDA_SLACK_COMMANDS_PATH=/slack/commands
BALDA_SLACK_AGENT_ENABLE_STREAMING=false
```

Equivalent YAML:

```yaml
balda:
  features:
    attachments:
      max_files_per_message: 10
      max_file_bytes: 26214400
      max_total_bytes: 52428800
      store:
        engine: local
  slack:
    bot_token: "xoxb-..."
    signing_secret: "..."
    commands_path: "/slack/commands"
    agent:
      enabled: true
      listen_addr: "0.0.0.0:8092"
      events_path: "/slack/agent/events"
      enable_streaming: false
      suggested_prompts: false
```

The HTTP Events API requires the Bot OAuth Token and Signing Secret. No Slack
app-level token is required. User-token search, Canvas, file-search, and Slack
MCP scopes are not used by conversational ingress.

The attachment limit environment overrides are:

```env
BALDA_FEATURES_ATTACHMENTS_MAX_FILES_PER_MESSAGE=10
BALDA_FEATURES_ATTACHMENTS_MAX_FILE_BYTES=26214400
BALDA_FEATURES_ATTACHMENTS_MAX_TOTAL_BYTES=52428800
BALDA_FEATURES_ATTACHMENTS_STORE_ENGINE=local
```

The defaults are 10 files per message, 25 MiB per file, and 50 MiB across the
message. Values must be positive, and the total limit must not be smaller than
the per-file limit. The default `local` store writes content-addressed blobs
under `${balda.state_dir}/attachments`. Setting
`balda.features.attachments.store.engine` to `off` keeps text-only Slack turns
available but rejects a turn that contains files.

Event subscriptions and history scopes solve different problems:

- `app_mention` and `message.im` deliver explicitly addressed input;
- `channels:history` and optional `groups:history` allow an on-demand
  `conversations.replies` read after a thread mention;
- history scopes do not cause ordinary channel messages to be delivered as
  events or interpreted as turns.

## Messaging Behavior

- Balda accepts human `message.im` events and explicit `app_mention` events in
  public and private channels. A DM may be text-only, mixed text and files, or
  an attachment-only `file_share`. A channel request may carry files but still
  requires an explicit app mention. Ordinary channel file shares,
  bot-originated events, hidden messages, edits, deletions, and unrelated
  subtypes are ignored, preventing response loops.
- Files on the triggering event are downloaded with the bot credential,
  persisted before the turn is published, and delivered to the provider in
  Slack payload order. Images are classified from their `image/*` MIME type;
  other files are documents. If any file fails validation, download, or
  persistence, Balda publishes no partial turn.
- Slack Connect `check_file_info` placeholders are resolved with `files.info`.
  A file that is inaccessible to the installed app is rejected without
  inventing content. Rate limits, Slack 5xx responses, network failures, and
  temporary storage failures ask Slack to retry; access, policy, unsafe URL,
  disabled-store, and size failures are acknowledged as terminal. Diagnostics
  contain safe reason codes and file IDs, never bot tokens or private URLs.
- A top-level DM message starts a Slack thread. Replies carrying `thread_ts`
  restore the same Balda session; different root timestamps remain isolated.
- In channels, every new Balda turn requires an explicit `@Balda` mention.
  This includes replies in Balda-created threads and threads created by humans
  or other agents. Ordinary channel messages never activate work, even when a
  Balda session already exists for that thread.
- For a mention inside an existing channel thread, Balda calls
  `conversations.replies` and includes the accessible discussion before the
  mention as bounded, author-attributed, untrusted background. The mention is
  kept separately as the current request; messages posted at or after its
  timestamp are excluded. Truncated context is marked explicitly.
- Files and images on those preceding messages, including file-only messages,
  use the same authenticated download, local persistence, and limits as files
  on the triggering mention. Files on the triggering mention consume the
  count and actual-byte budget first. Balda then considers deduplicated
  historical files newest-first within the remaining budget, while presenting
  retained historical attachments in chronological message and Slack file
  order after all current attachments.
- Historical file metadata remains inside the untrusted context block. A
  bounded `history_attachment_NNN` reference uses `supplied`, `duplicate`,
  `over_budget`, `unavailable`, or `unsupported` to explain each retained
  occurrence without exposing a private Slack URL, token, file body, or local
  path. A permanently inaccessible individual historical file becomes a safe
  marker and does not reject the current mention. A temporary Slack, network,
  or storage failure delays the whole turn so Slack can retry it without a
  partial durable publication.
- With attachment storage set to `off`, a triggering event that contains files
  remains terminal. A text mention with historical files still proceeds: the
  files are marked unavailable and the accessible text context is retained.
- A retryable history failure delays the turn and lets Slack retry the signed
  event. If history is permanently inaccessible because of scope, membership,
  or channel access, Balda still accepts the mention with an explicit
  context-unavailable marker instead of inventing the missing discussion.
- Slack workspace membership is the Slackagent access boundary: any workspace
  user who can address the installed app may collaborate with it. Slackagent
  does not apply Balda's owner/collaborator bootstrap gate.
- `/balda locator` runs the shared Balda `locator` command and posts its result
  through the normal Slack delivery path. It returns the conversation-level
  reference `slackagent:c:<team_id>:<conversation_id>` because Slack slash
  payloads do not identify a thread. The command does not start an agent turn.
- Slack Agent Session status is `processing` while a turn runs, `active` after
  completion or cancellation, `suspended` while Balda waits for user input, and
  `closed` when the Balda session closes.
- Balda derives the initial Slack Agent Session title from the first prompt.
- With streaming disabled, Balda replies through `chat.postMessage`. With
  streaming enabled, it uses `chat.startStream`, `chat.appendStream`, and
  `chat.stopStream` in the same thread.
- Slack's Stop button sends `agent_session_stopped`; Balda cancels the matching
  active turn and drops queued turns under the normal session cancellation
  rules.

## Sandbox Validation

Before production rollout, verify in a Slack developer workspace:

1. Slack accepts the Events Request URL challenge.
2. A request with an invalid signature is rejected and creates no Balda turn.
3. A human DM receives one response in the same Slack thread.
4. An app mention in both a public and private channel receives one response in
   the same thread. A follow-up creates another turn only when it mentions
   Balda again.
5. A mention inside a thread created by a human or another agent includes the
   preceding discussion, but not the triggering mention or later messages, as
   background context.
6. Unmentioned channel messages and bot/subtype events create no Balda turn or
   reply, including inside an existing Balda session.
7. The Agent Session title and processing/active states appear correctly.
8. The Stop button cancels active work.
9. Repeat the DM and channel tests with streaming both disabled and enabled.
10. Send an attachment-only image and a mixed text-plus-document request in a
    DM. Confirm each produces one response and the provider receives the
    persisted files in their original order.
11. Send a file with an explicit app mention in a public and, when configured,
    private channel. Confirm it produces one turn; confirm the same file share
    without a mention produces none.
12. Test one standard workspace file and an accessible Slack Connect file on
    the triggering event. For an inaccessible Slack Connect placeholder,
    confirm Balda creates no turn and logs only a safe terminal reason without
    a private URL or token.
13. Verify a request exceeding each configured count, per-file, and aggregate
    limit creates no turn. Temporarily set the attachment store to `off` and
    confirm file input is rejected while a text-only DM still succeeds.
14. In an existing channel thread, post an earlier document, image, and
    repeated reference to the same file, then explicitly mention Balda. Confirm
    the provider receives each accessible file once, current-request files are
    first, selected historical files are chronological, and the prompt contains
    matching bounded markers. Repeat with history exceeding the remaining
    budget and with an inaccessible Slack Connect file; confirm the mention
    still produces one turn with `over_budget` or `unavailable` markers. With
    storage `off`, confirm a text mention still succeeds with unavailable
    historical markers. Finally, repeat with a text-only thread and confirm its
    existing bounded context behavior is unchanged.
15. `/balda locator` posts a conversation locator, and `/balda locator extra`
    posts usage containing `/balda locator`; neither request starts a turn.

Do not record tokens or signing secrets in logs, screenshots, or committed test
artifacts.

## Troubleshooting

- Receiver does not start: enable `balda.slack.agent.enabled` and provide the
  bot token and signing secret.
- URL verification or signatures fail: verify the signing secret and confirm
  the proxy preserves the raw body and Slack signature headers.
- Events do not arrive: confirm the app is configured as an agent, installed to
  the workspace, subscribed to the required events, and using HTTP Events API
  rather than Socket Mode.
- Slash commands fail: confirm `/balda` uses the public URL that forwards to
  `balda.slack.commands_path`, and that this path differs from the Agent Events
  path.
- Replies or session states fail: inspect Slack API error codes and confirm the
  app has the required bot scopes.
- Current-message files fail: confirm `files:read`, conversation membership,
  attachment storage, and the configured count/byte limits. Missing access or
  a disabled store is terminal; temporary Slack, network, or storage failures
  are retried by Slack.
- Thread context is unavailable: confirm `channels:history` for public channels
  or `groups:history` plus app membership for private channels. These scopes do
  not require `message.channels` or `message.groups` event subscriptions. For
  historical files, also confirm `files:read`, attachment storage, and the
  shared count/byte limits. A permanent failure for one historical file is
  reported by a bounded marker; temporary access or storage failures retry the
  entire triggering event.

Balda does not yet upload generated files back to Slack. Outbound file delivery
and `files:write` are not part of this inbound and historical-context support.
