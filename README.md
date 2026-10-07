# balda

[![test](https://github.com/baldaworks/balda/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/baldaworks/balda/actions/workflows/test.yml)
[![lint](https://github.com/baldaworks/balda/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/baldaworks/balda/actions/workflows/lint.yml)

## Self-hosted engineering agent for team chat

Balda is a self-hosted engineering agent that lives in your team chat and works
inside your project.

You can give it a task in chat, open a focused topic for a piece of work, run a
longer goal loop, or wire external events into the same workflow. Balda keeps
context, uses your configured tools, and returns something reviewable: a
summary, changed files, validation output, a commit, or a concrete next step.

## What Balda is good for

- chat-native engineering help in Telegram, Zulip, Slack Agent, or Mattermost DMs/channel threads
- focused task threads instead of one shared bot conversation
- long-running goal execution with progress updates and final results
- `wedge` style operation: put the agent in the middle of your team workflow so
  chat, tools, schedules, and external events all feed the same execution path
- self-hosted deployment close to your repo, config, and credentials

## Quickstart

You need:

- one chat surface: Telegram, Zulip, Slack, or Mattermost
- one supported provider CLI installed on the host or in Docker:
  `codex`, `opencode`, `copilot`, `gemini`, or `claude`
- Node.js/npm, unless you run the Docker Compose path

Install:

```bash
npm install -g -y @baldaworks/balda
```

Initialize in your project:

```bash
balda init
```

`balda init` also creates the Backoffice administrator with username `superuser` and prints its generated
password once. Save it securely, then start:

```bash
balda start
```

The primary administrator's display name is also `superuser`. In Backoffice,
use **Account → Change password** to replace your own password. Administrators
manage each user's chat bindings under **Access**: a user may have Telegram,
Slack Agent (`slackagent`), Zulip, and Mattermost principals where those
integrations are configured. Each channel has its own invitation panel: generate
an invitation for the selected user and send it from their provider account.
The value appears once; Refresh bindings shows status and confirmed identities.
Use explicit Replace invitation or Cancel invitation for a pending value. Removing one binding revokes bot access for that principal without
removing the user's other bindings or browser account.

**Account** shows your active browser sessions with the current one first.
Use **Older active sessions** to reach further sessions, or **End session**
to revoke one; ending the current session requires confirmation. **Access**
lets administrators find users and manage their sessions and bindings. **Audit**
shows recent security events with filters. The **Overview** integration cards
show configuration, not a live health check. See the
[Backoffice reference](docs/reference/backoffice.md) for session and security details.

Administrators can open **MCP** in Backoffice to inspect configured servers and
create, edit, probe, disable or delete managed connections. Configuration entries
are read-only. HTTP/SSE servers offer OAuth on creation and detail, with optional
client settings; enter service account credentials at the external service.
OAuth is managed in Backoffice even for configuration-owned addresses. Protected
values are write-only, and **Available** tools are shown separately from OAuth
status. Native browser/device authorization
uses the current trusted definition, including explicit configured capture;
saved grants can retry tool attachment without repeating OAuth. See [MCP management](docs/reference/backoffice.md#mcp-management)
for provider targeting and value operations.

Administrators can open **Schedules** in Backoffice to inspect configured
recurring jobs and create persistent UI-owned jobs. Configuration entries are
read-only; either source can be run manually, and both have retained run
history. Cron uses UTC. A manual run queues work without moving its next
scheduled time; a disabled job requires confirmation. See
[Schedules management](docs/reference/backoffice.md#schedules-management).

Before storing protected MCP values or worker grants, configure the deployment
[MCP credential key](docs/reference/configuration.md#protected-values-and-worker-grants)
and keep it with your database backup. Existing sessions retain their captured
MCP revision; create or reset a session to activate changed definitions.

`balda init` creates `.config/balda/config.yaml`, initializes
`.config/balda/state.db` by default, detects available provider CLIs, and prints
the next step for your selected chat provider.

SQLite is the default state database. To select PostgreSQL, configure
`balda.database.type: postgres`; launch remains `balda start`.
See [database configuration and operations](docs/reference/database.md).

`balda start` applies embedded schema and data migrations to the selected database,
checks canonical users and administrator bootstrap, then starts the bot,
Backoffice, and enabled integrations in one process. On a fresh database,
`balda init` has already bootstrapped the administrator. For an existing
installation, stop Balda, back up the database, and deploy the new binary.
Goose automatically converts owner/collaborator records when Balda opens the
database, preserving their roles and bot bindings. See the
[Backoffice startup and security contract](docs/reference/backoffice.md).

```bash
# Existing installation with owner/collaborator records, after stopping Balda and backing up:
balda backoffice bootstrap-admin
balda start
```

Conversion does not generate passwords. The converted primary user is named
`superuser`; `bootstrap-admin` generates and prints its first browser password
once. Already-converted users retain their credentials and browser sessions.
If the administrator already has a usable credential, start directly; replacing
that password requires `bootstrap-admin --reset` and revokes its browser session
families. Never pass passwords as command arguments.

Browser refresh tokens default to 30 days (`720h`), renewed after every successful
refresh. Existing explicit `12h` config or environment overrides must be changed
to `720h` to use the monthly window. Expired sessions require sign-in. See the
[browser session contract](docs/reference/backoffice.md#browser-sessions-and-refresh-rotation).

Administrator passkey 2FA is optional and off by default. Enable it in Account
using the current password and a verified passkey at an HTTPS origin or localhost.
Enrolled accounts require their passkey when signing in with a password;
session refresh and ordinary Backoffice actions do not prompt for it again.
Password resets preserve it. If a key is lost, an authorized host administrator
can run `balda backoffice recover-2fa --username superuser --confirm`, which
revokes browser sessions while retaining the password and bot bindings. See the
[2FA and recovery contract](docs/reference/backoffice.md#optional-administrator-passkey-2fa).

## First run

For Telegram, sign in to Backoffice, select the existing user in Access and use
its Telegram invitation link, or send the generated payload:

```text
/start bind_<opaque_token>
```

The exact payload also works as a direct message. It connects the verified sender
to that selected user, preserving their role. Invitations expire after 24 hours
and can be used once; refresh the user's bindings in Backoffice to confirm.

The existing owner bootstrap command printed by `balda init` remains supported:

```text
/start owner=<owner_token>
```

After connecting the account, send a normal direct message to the bot, or open an isolated topic:

```text
/topic release
```

From there you can:

- ask for ordinary help in chat
- start a goal loop with `/goalkeeper <objective>`
- run a session skill with `/skill <skill> [prompt...]` or
  `/skill <plugin>:<skill> [prompt...]`
- stop the current turn with `/cancel`
- reset the current session with `/reset`

## Main workflows

### 1. Ordinary chat work

Send a message in the session where you want work to happen. Balda keeps that
conversation as the execution context.

### 2. Focused topic work

Use `/topic <name>` to create a separate session for a task, incident, release,
or stream of work.

### 3. Goal-driven execution

Use `/goalkeeper <objective>` when you want Balda to keep working until there is a
result to review.

Balda will:

- work in repeated passes
- post progress updates
- ask follow-up questions when critical input is missing
- return a terminal result with outcome details

See [docs/goal-workflow.md](docs/goal-workflow.md) for the detailed goal
contract.

### 4. Wedge mode

Balda can act as a wedge between team chat and the rest of your engineering
system:

- chat messages start work
- scheduled jobs wake work up
- inbound webhooks turn external events into session work
- the same session can continue through follow-up questions and delayed work

This is useful when you want one operational path for human requests,
automation, and agent execution instead of separate bots and scripts.

## Supported chat providers

- Telegram
- Zulip
- Slack Agent DMs and mentioned channel threads
- Mattermost DMs and mentioned channel threads

Balda maps each conversation scope to its own session:

- Telegram direct chat or personal/group topic
- Zulip stream + topic
- Slack Agent DM or mentioned channel thread
- Mattermost DM, channel, or channel thread

In Slack channels, every turn requires an explicit `@Balda` mention. A mention
inside an existing thread can use its preceding accessible discussion and
persisted files as bounded context; ordinary channel messages never activate
Balda. With the optional `files:write` scope, generated photos and documents
are delivered back into the same root thread from bounded local files.

## Docker Compose

Balda ships a root [Dockerfile](Dockerfile) and [compose.yaml](compose.yaml)
for local Docker Compose deployment.

The current directory is mounted as `/workspace`, so Balda sees your checkout,
config, git metadata, and local state.

```bash
docker compose build balda
docker compose run --rm balda init
docker compose up -d balda
```

Polling mode is the default, so Telegram does not require publishing a port.
Webhook deployment details live in the
[configuration reference](docs/reference/configuration.md).

## Published container image

Balda publishes a release image at `ghcr.io/baldaworks/balda:latest`.

That image contains the `balda` binary only. It does not bundle provider CLIs.
Use it as a source stage in your own image and add the provider runtime you
want.

Example with Codex:

```dockerfile
FROM node:24-bookworm-slim AS cli-builder
RUN npm install -g @openai/codex

FROM ghcr.io/baldaworks/balda:latest AS balda

FROM node:24-bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      git \
      openssh-client \
      ripgrep \
 && rm -rf /var/lib/apt/lists/*

COPY --from=cli-builder /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=cli-builder /usr/local/bin/codex /usr/local/bin/codex
COPY --from=balda /usr/local/bin/balda /usr/local/bin/balda

WORKDIR /workspace
ENTRYPOINT ["balda"]
```

## Core commands

Balda provides onboarding, session control, GoalKeeper, locator, usage, skill,
user, and plugin commands. Telegram and Zulip use `/skill`, `/locator`, and
`/reset`; Slack exposes the conversation-scoped forms `/balda skill`,
`/balda locator`, and `/balda reset`. For Backoffice account binding, Zulip accepts `/start bind_<token>` or the exact
`bind_<token>` DM generated for its configured bot. In Slack, send the
generated `bind_<token>` in the configured bot’s DM or use `/balda start bind_<token>`.
Mattermost accepts the exact DM payload (including `/msg @<bot_username> bind_<token>`);
`/balda start bind_<token>` is available when its slash receiver is enabled.

Slack formats the response for scanning and copying:

````text
📍 *Balda Locator* • *Transport:* `slackagent` • *Locator:* `slackagent:c:T0BFTRBFA94:C0BU4LKUB6W`

*Scheduler / webhook configuration*
```
target: locator
key: slackagent:c:T0BFTRBFA94:C0BU4LKUB6W
```
````

See the [complete command reference](docs/commands.md) for syntax, transport
availability, access, valid contexts, effects, errors, and examples.

Owner plugin lifecycle commands include `install`, explicit origin adoption for
migrated installs, `upgrade`, `enable`, `disable`, `rollback`, `remove`,
explicit `purge`, and bounded `status`.
Every chat command is durably published and executed by `CommandActor`;
plugin-contributed commands are declarative, revision-pinned normal turns and
never native plugin handlers.
`/skill <skill> [prompt...]` selects one uniquely named skill from the current
session snapshot. Use `/skill <plugin>:<skill> [prompt...]` to select an exact
plugin source. The optional trailing prompt becomes the turn text; the skill
body stays lazy until the turn executes. Balda discovers standalone skills from
the runtime account's `$HOME/.agents/skills`, `$CODEX_HOME/skills` (defaulting
to `$HOME/.codex/skills`), `<state_dir>/skills`, and the current workspace's
`.agents/skills`; enabled plugins contribute their own skills. Run `/reset` to
adopt skills installed or changed after the session was created.
See [Plugins and session capabilities](docs/reference/plugins.md) for the
`dev.baldaworks.balda` extension schema and the session-bound command, skill,
and MCP lifecycle.

## Configuration

Balda loads `.config/balda/config.yaml` and then applies `BALDA_*` environment
overrides. If a local `.env` exists, Balda loads it before resolving config.

Minimal shape:

```yaml
runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp: {}
  mcp_servers: {}

balda:
  provider: codex
  telegram:
    token: ""
```

Common settings:

- `balda.provider` — which configured provider runtime to use
- `balda.telegram.token` — Telegram bot token

Explicit `model` and `reasoning_effort` values on an ACP provider are reapplied
to restored sessions after restart. Persisted values for settings omitted from
the provider configuration remain unchanged, so changing an explicit setting
does not require `/reset`. Custom ACP servers can set `model_config_id` and
`reasoning_effort_config_id` when their advertised option IDs differ.
- `balda.telegram.formatting_mode` — Telegram output mode: `rich_markdown`
  (default), `rich_html`, or `none` for literal plain text
- `balda.zulip.*` — Zulip outgoing webhook bot credentials and receiver config
- `balda.mattermost.*` — Mattermost bot credentials (`enabled`, `server_url`,
  `token`, `bot_user_id`, `bot_username`) for websocket event ingress
- `balda.slack.*` — Slack Agent credentials plus `agent.*` HTTP/streaming config
- `balda.webhooks.*` — optional inbound webhook routes
- `balda.scheduler.jobs` — recurring scheduled jobs owned by host configuration; administrators can also create persistent schedules in Backoffice. See [Schedules management](docs/reference/backoffice.md#schedules-management).
- `balda.workspace.*` — workspace/worktree behavior for goal execution
- `balda.permissions.mode` — agent permission policy: `allow_all`, `ask`, or `deny_all`
- `balda.permissions.timeout` — maximum wait for an interactive permission decision (default `2m`)
- `balda.memory.enabled` — enable the global explicit-fact memory store and its
  bundled MCP tools (default `true`)
- `balda.mcp_servers` — MCP servers injected into Balda-started sessions

### Explicit fact memory

The bundled `balda.memory.remember` tool stores explicit facts globally for the
Balda instance. Each update records a latest-memory timestamp. On a subsequent
session turn, Balda compares that timestamp with the turn's memory cursor. When
they differ, Balda adds the complete current fact-memory snapshot to that same
provider user prompt in a delimited application-memory block. Unchanged, empty,
or disabled memory adds nothing to the prompt. Global fact memory is expected to
remain small; it is separate from optional durable session memory.

### Knowl sidecar

Run [Knowl](https://github.com/baldaworks/knowl) separately, then register its
MCP endpoint through Balda's existing generic configuration:

```yaml
runtime:
  mcp_servers:
    knowl:
      type: http
      url: http://127.0.0.1:8080/mcp

balda:
  mcp_servers:
    - knowl
```

This exposes `knowl_retrieve`, `knowl_ingest`, and `knowl_operation` to
Balda-started sessions. Balda does not start Knowl, initialize its workspace,
own its provider/storage configuration, or automatically ingest conversation
turns.

`allow_all` preserves historical behavior and should be used only where every
agent tool call is trusted. Production chat deployments should normally set
`BALDA_PERMISSIONS_MODE=ask`; unsupported channels, missing requester context,
cancellation, and timeout fail closed.

Set `BALDA_TELEGRAM_FORMATTING_MODE` to override the Telegram mode. Existing
`markdownv2` configurations must move to `rich_markdown` (or `none`), and
existing `html` configurations must move to `rich_html` (or `none`). Balda does
not accept compatibility aliases: an unsupported value fails startup before
ingress begins accepting messages. See the
[Telegram formatting guide](docs/telegram-formatting.md) for rollout and
fallback details.

For complete configuration, examples, and provider-specific details, see the
[configuration reference](docs/reference/configuration.md).

## Troubleshooting

- `telegram token is required` — run `balda init` or set
  `BALDA_TELEGRAM_TOKEN`
- `no supported agent CLI detected` — install or expose one of `codex`,
  `opencode`, `copilot`, `gemini`, or `claude`
- `balda.provider is required` — rerun `balda init` or set a configured
  provider id manually
- webhook or Slack/Zulip startup issues — verify the matching `balda.*`
  integration settings in config
- workspace import/export issues — check `balda.workspace.mode`,
  `balda.workspace.base_branch`, and the git checkout Balda is running in

## Docs

- Technical reference: [docs/balda.md](docs/balda.md)
- Onboarding: [docs/reference/onboarding.md](docs/reference/onboarding.md)
- Configuration: [docs/reference/configuration.md](docs/reference/configuration.md)
- Topic sessions: [docs/reference/topic-sessions.md](docs/reference/topic-sessions.md)
- Session memory: [docs/reference/session-memory.md](docs/reference/session-memory.md)
- Operations: [docs/reference/operations.md](docs/reference/operations.md)
- Goal workflow: [docs/goal-workflow.md](docs/goal-workflow.md)
- Architecture map: [docs/architecture/index.md](docs/architecture/index.md)
- Telegram formatting: [docs/telegram-formatting.md](docs/telegram-formatting.md)
- Zulip webhook setup: [docs/zulip-webhook.md](docs/zulip-webhook.md)
- Mattermost setup: [docs/mattermost.md](docs/mattermost.md)
- Slack setup: [docs/slack.md](docs/slack.md)
- Contributing: [CONTRIBUTING.md](CONTRIBUTING.md)

## Release

- GitHub Releases: <https://github.com/baldaworks/balda/releases>
- npm package: <https://www.npmjs.com/package/@baldaworks/balda>

npm releases are published from Git tags through the Omnidist workflow. The
workflow uses npm trusted publishing with GitHub Actions OIDC; it does not use a
long-lived npm publish token. Each `@baldaworks/balda*` package trusts the
`baldaworks/balda` repository and the `omnidist-release.yml` workflow.
