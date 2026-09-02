# Slack Agent Integration

Balda integrates with Slack's `agent_view` over the signed HTTP Events API.
Balda serves plain HTTP; terminate public HTTPS in a reverse proxy, ingress, or
tunnel and forward the request without changing its body.

## Slack App Setup

1. Create a Slack app and configure it as an agent.
2. Install it to the workspace with `chat:write`, `im:history`,
   `app_mentions:read`, and `channels:history` bot scopes. Add
   `groups:history` only when Balda must load context from private-channel
   threads. The bot must be a member of each public or private channel whose
   thread context it reads.
   Slack adds the agent-specific `assistant:write` scope when the app is declared
   as an agent.
3. Enable Event Subscriptions and subscribe to these bot events:
   - `message.im`
   - `app_mention`
   - `agent_session_stopped`
4. Set the Events Request URL to a public HTTPS URL that forwards to
   `balda.slack.agent.events_path`.

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
BALDA_SLACK_AGENT_ENABLE_STREAMING=false
```

Equivalent YAML:

```yaml
balda:
  slack:
    bot_token: "xoxb-..."
    signing_secret: "..."
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

Event subscriptions and history scopes solve different problems:

- `app_mention` and `message.im` deliver explicitly addressed input;
- `channels:history` and optional `groups:history` allow an on-demand
  `conversations.replies` read after a thread mention;
- history scopes do not cause ordinary channel messages to be delivered as
  events or interpreted as turns.

## Messaging Behavior

- Balda accepts human `message.im` events and explicit `app_mention` events in
  public and private channels. Bot-originated and subtype events are ignored,
  preventing response loops.
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
- A retryable history failure delays the turn and lets Slack retry the signed
  event. If history is permanently inaccessible because of scope, membership,
  or channel access, Balda still accepts the mention with an explicit
  context-unavailable marker instead of inventing the missing discussion.
- Slack workspace membership is the Slackagent access boundary: any workspace
  user who can address the installed app may collaborate with it. Slackagent
  does not apply Balda's owner/collaborator bootstrap gate.
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
- Replies or session states fail: inspect Slack API error codes and confirm the
  app has the required bot scopes.
- Thread context is unavailable: confirm `channels:history` for public channels
  or `groups:history` plus app membership for private channels. These scopes do
  not require `message.channels` or `message.groups` event subscriptions.
