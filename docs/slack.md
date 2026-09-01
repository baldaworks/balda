# Slack Agent Integration

Balda integrates with Slack's `agent_view` over the signed HTTP Events API.
Balda serves plain HTTP; terminate public HTTPS in a reverse proxy, ingress, or
tunnel and forward the request without changing its body.

## Slack App Setup

1. Create a Slack app and configure it as an agent.
2. Install it to the workspace with `chat:write`, `im:history`,
   `app_mentions:read`, `channels:history`, and `groups:history` bot scopes.
   Slack adds the agent-specific `assistant:write` scope when the app is declared
   as an agent.
3. Enable Event Subscriptions and subscribe to these bot events:
   - `message.im`
   - `app_mention`
   - `message.channels`
   - `message.groups`
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
app-level token is required.

## Messaging Behavior

- Balda accepts human `message.im` events and explicit `app_mention` events in
  public and private channels. Bot-originated and subtype events are ignored,
  preventing response loops.
- A top-level DM message starts a Slack thread. Replies carrying `thread_ts`
  restore the same Balda session; different root timestamps remain isolated.
- In channels, an explicit app mention starts a Balda thread. Human
  `message.channels` and `message.groups` replies continue only an existing
  Balda thread; unrelated top-level messages and unrelated threads are ignored.
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
   the same thread, and a follow-up reply continues that thread without another
   mention.
5. Unrelated channel messages and bot/subtype events create no Balda turn or
   reply.
6. The Agent Session title and processing/active states appear correctly.
7. The Stop button cancels active work.
8. Repeat the DM and channel tests with streaming both disabled and enabled.

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
