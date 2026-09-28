# Mattermost Integration

Balda integrates with Mattermost through the v4 websocket and REST APIs using a
bot account and a personal access token. Unlike the Zulip and Slack transports,
Mattermost does not expose a per-bot outgoing webhook that preserves threads and
post mutations, so the websocket API is the ingress: the bot subscribes to the
event stream and receives every post it is entitled to see.

The transport requires no public URL and no reverse proxy — the bot dials the
Mattermost server outbound, which makes it usable from a private network.

## Mattermost Setup

1. Sign in to Mattermost as a system administrator.
2. Enable bot account creation once, from the server host:

   ```bash
   docker exec mattermost mmctl --local config set ServiceSettings.EnableBotAccountCreation true
   ```

   Setting this through the REST configuration API returns `200 OK` without
   applying the change, because the environment overrides the stored config.
   Use `mmctl --local` on the server host.

3. Create the bot account and mint a personal access token:

   ```bash
   docker exec mattermost mmctl --local bot create balda balda "Balda worker"
   docker exec mattermost mmctl --local bot list
   ```

   Issue the token through the REST API — `POST /api/v4/users/{bot_user_id}/tokens`.
   The token value is returned **exactly once**; a later `GET` on the same
   resource lists token metadata without the secret. Store it immediately.

4. Add the bot to every team and channel it must serve. A bot that is not a
   member of a channel cannot post to it and receives `api.context.permissions.app_error`:

   ```bash
   docker exec mattermost mmctl --local team users add <team> <bot_user_id>
   docker exec mattermost mmctl --local channel users add <team>:<channel> <bot_user_id>
   ```

5. Record the bot `user_id`. It is required so Balda can ignore its own posts
   and avoid answering itself.

## Balda Configuration

Environment:

```env
BALDA_MATTERMOST_ENABLED=true
BALDA_MATTERMOST_SERVER_URL=http://mattermost:8065
BALDA_MATTERMOST_TOKEN=***
BALDA_MATTERMOST_BOT_USER_ID=***
BALDA_MATTERMOST_BOT_USERNAME=balda
BALDA_MATTERMOST_COMMANDS_ENABLED=true
BALDA_MATTERMOST_COMMANDS_LISTEN_ADDR=:8093
BALDA_MATTERMOST_COMMANDS_PATH=/mattermost/commands
BALDA_MATTERMOST_COMMANDS_TOKEN=***
```

Equivalent YAML:

```yaml
balda:
  mattermost:
    enabled: true
    server_url: "http://mattermost:8065"
    token: "${BALDA_MATTERMOST_TOKEN}"
    bot_user_id: "${BALDA_MATTERMOST_BOT_USER_ID}"
    bot_username: "balda"
    commands_enabled: true
    commands_listen_addr: ":8093"
    commands_path: "/mattermost/commands"
    commands_token: "${BALDA_MATTERMOST_COMMANDS_TOKEN}"
```

| Key | Required | Purpose |
| --- | --- | --- |
| `enabled` | no (default `false`) | Enables the Mattermost transport. Enabling it satisfies the "at least one channel" startup requirement on its own. |
| `server_url` | yes when enabled | Absolute `http://` or `https://` base URL of the Mattermost server. When Balda runs in the same Docker network as Mattermost, use the service name (`http://mattermost:8065`) rather than the public hostname. |
| `token` | yes when enabled | Bot account personal access token. |
| `bot_user_id` | yes when enabled | Bot account user id. The ingress refuses to start without it, because it cannot otherwise distinguish its own posts from a user's. |
| `bot_username` | no | Bot username used to detect `@mention` activation in channels. Mention handling is disabled when it is empty. |
| `commands_enabled` | no (default `false`) | Enables the HTTP slash-command receiver. Mattermost delivers slash commands as HTTP requests, never as posts, so this must be on for any slash command to work. It requires `enabled: true`, because command authorization, channel resolution, and result delivery use the Mattermost transport. |
| `commands_listen_addr` | no (default `:8093`) | Local address the slash-command receiver listens on. |
| `commands_path` | no (default `/mattermost/commands`) | Local HTTP path Mattermost posts slash commands to. It must start with `/`. Point each Mattermost slash command's Request URL at this path. |
| `commands_token` | yes when `commands_enabled` | Slash-command token Mattermost generates for the integration. Mattermost sends it in the request body and Balda compares it exactly; the receiver refuses to start without it and rejects every request when the token does not match. |

Environment variables are applied only to keys that already exist in the loaded
YAML, so keep the `balda.mattermost` block present in your config file even when
you configure it exclusively through the environment.

`server_url` must be reachable from the Balda process and must speak the v4 REST
API. Pointing it at an external hostname routes traffic through whatever
terminates TLS for Mattermost, which is an unnecessary dependency when both
services share a Docker network.

## Conversation Behavior

Balda maps each Mattermost conversation to a session:

- **Direct message** — the direct channel between the bot and one user. Every
  message activates Balda; no mention is needed.
- **Channel** — a public or private team channel. Balda responds only when
  mentioned, for example `@balda summarize the failing tests`. Posts without the
  mention are ignored.
- **Channel thread** — a reply chain inside a channel. A thread is its own
  session: every reply in a thread accumulates in one Balda session, and the
  thread is kept separate from the channel-wide session.

Group message channels are treated as direct conversations.

### Post mutations are ignored on purpose

Mattermost emits `post_edited` and `post_deleted` events. Balda ignores both:
it acts on the instruction it received and does not retroactively rewrite its
answer. Editing a message after sending it does not re-run the turn.

### Progress and typing

Mattermost bot accounts have no typing API, so Balda does not send typing
indicators. Progress appears as plan updates. This differs from Telegram, which
maps progress onto typing indicators.

## Slash command setup

Mattermost executes a slash command **without posting a message**, so the
websocket event stream never carries one. Slash commands reach Balda only over
HTTP, which needs a Request URL per command:

1. Enable the receiver (`commands_enabled: true`, a `commands_token`, and a
   `commands_listen_addr` reachable from the Mattermost server).
2. In Mattermost, open **Integrations > Slash Commands > Add Slash Command**.
3. Set the **Request URL** to `http(s)://<balda-host>:8093/mattermost/commands`,
   method `POST`.
4. Copy the **token** Mattermost shows and set it as `commands_token`.
5. Register one root command (`/balda`) and select the action with its first
   word, for example `/balda locator`. This keeps the receiver bound to the
   single integration token configured as `commands_token`.

A slash command is answered immediately and its result is delivered as a post.
Mattermost shows the author a timeout when a command does not respond within a
few seconds, so Balda never blocks the HTTP response on the command itself.

## Activation flow

1. The owner authorizes the bot once with `/start owner=<token>` **in a direct
   message**. `/start` is the only command reachable before authorization;
   every other command and every conversational turn is rejected until the
   owner is bound. This requires the slash-command receiver described above.
2. In a channel, address the bot explicitly with an `@mention`.
3. Before exposing a channel to a wide audience, confirm the bot is a member of
   it and that the owner binding is present — a sender who is neither the owner
   nor a verified collaborator receives an access-denied reply rather than an
   answer.

## Troubleshooting

- **The bot never replies, and logs show the websocket connected.** Check for
  `api.web_socket_router.bad_seq.app_error` responses. Mattermost requires a
  strictly increasing `seq` on every client frame; a frame without it makes the
  connection stop receiving events *without closing the socket*. Balda sends
  every client frame through a single `seq`-assigning path, so a recurrence
  means a new code path bypasses it.
- **Every message settles as `unauthorized`.** The sender is not bound as owner
  or collaborator. Verify the binding by comparing the acting Mattermost
  `user_id` against the stored principal; the principal is stored **without** a
  transport prefix, so a value like `mattermost:<id>` in the binding column
  never matches.
- **`x509: certificate signed by unknown authority` on the provider call (not
  the Mattermost call).** This is unrelated to Mattermost: a `scratch`-based
  image with no CA bundle cannot verify any outbound TLS certificate. Provide
  `/etc/ssl/certs/ca-certificates.crt` in the image.
- **A bot cannot post to a channel.** The bot is not a member of the team or
  channel, or the channel is private and the bot was never added.
- **A slash command does nothing at all — no reply, no error.** Slash commands
  do not travel over the websocket, so this is expected when the HTTP receiver
  is disabled or unused. Check `commands_enabled`, that Mattermost has a slash
  command whose Request URL points at `commands_path`, and that the address is
  reachable from the Mattermost server.
- **A slash command returns 401.** The request token does not match
  `commands_token`. Mattermost shows a fresh token when you open the integration;
  re-copy it rather than reusing an old value.

## Related

- [Configuration reference](reference/configuration.md) — full `balda.mattermost.*` key list
- [Command reference](commands.md) — per-transport command availability
- [Slack Agent setup](slack.md) — the signed-HTTP transport for comparison
- [Zulip webhook setup](zulip-webhook.md) — the outgoing-webhook transport for comparison
