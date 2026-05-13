# relay

[![test](https://github.com/normahq/relay/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/normahq/relay/actions/workflows/test.yml)
[![lint](https://github.com/normahq/relay/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/normahq/relay/actions/workflows/lint.yml)

## One app. No backing services. No webhook required.

Relay turns Telegram into the control plane for ACP agent sessions in your
projects. Run it next to your checkout, connect your Telegram bot, and control
agents from DMs, groups, or topic chats.

Relay does not require Redis, Postgres, object storage, queues, or a public
webhook endpoint. It persists local state in SQLite, uses Telegram polling by
default, and works with the ACP agent command you choose.

Use Codex, OpenCode, Copilot, Gemini, Claude Code, or any command that speaks
ACP. Relay wraps it with durable history, memory, MCP tools, and optional git
workspace isolation.

```bash
npm install -g -y @normahq/relay
relay init
relay start
```

## What You Get

| Feature | What it means |
|---------|---------------|
| No backing services | Relay stores local state in SQLite and does not require Redis, Postgres, queues, or object storage. |
| No webhook required | Polling mode is the default, so local quickstarts do not need a public URL or published port. |
| Any ACP agent | Use built-in providers for `codex`, `opencode`, `copilot`, `gemini`, and `claude`, or wire any ACP-compatible command with `generic_acp`. |
| Telegram control plane | One owner, optional collaborators, direct-message sessions, topic sessions with `/topic <name>`, and public-chat mention/reply routing. |
| Git workspaces | Each session can get its own git worktree, with `relay.workspace.import` and `relay.workspace.export` MCP tools for safe branch flow. |
| Durable sessions | SQLite persistence is on by default, so conversation history survives restarts until `/reset` or explicit `/close`. |
| Memory system | `MEMORY.md` stores facts, `/memory` shows them, and `relay.memory.*` MCP tools let agents remember user-approved facts. |
| MCP support | Add stdio, HTTP, or SSE MCP servers globally, per provider, or for every Relay session. |
| Docker Compose runtime | Run Relay in a container while using the current directory, `.env`, `.git`, and `.config/relay` from the host. |

Relay is useful when you want an agent to live where you already coordinate:
Telegram DMs, group chats, and project topics.

## How It Works

1. Pick an ACP provider.
2. Connect a Telegram bot token.
3. Chat, create topics, and let Relay persist session state, memory, and workspaces.

Relay runs one provider runtime per process and maps Telegram chats/topics to
separate agent sessions. That keeps the bot simple to operate while preserving
session boundaries.

## Quickstart

You need:

- a Telegram bot token from BotFather
- at least one provider CLI for host installs: `codex`, `opencode`, `copilot`,
  `gemini`, or `claude`
- Node.js/npm, unless you use the Docker Compose flow

Install Relay:

```bash
npm install -g -y @normahq/relay
```

Initialize Relay in your project:

```bash
relay init
```

`relay init` detects provider CLIs, validates the Telegram token, writes
`.config/relay/config.yaml`, initializes `.config/relay/relay.db`, and prints
the next commands. By default, the Telegram token is stored in `.env`.

Start Relay:

```bash
relay start
```

Authenticate in Telegram with the printed auth URL, or send the printed command
directly to your bot:

```text
/start owner=<owner_token>
```

After owner auth, send a normal direct message to use the owner session. Create
a named topic session when you want an isolated workspace and conversation:

```text
/topic <name>
```

## Docker Compose

Relay ships a root [`Dockerfile`](Dockerfile) and [`compose.yaml`](compose.yaml)
for local Docker Compose runtime.

This path is designed for real project work. The current directory is mounted as
`/workspace`, so Relay sees your host checkout, `.git`, `.env`,
`.config/relay/config.yaml`, and `.config/relay/relay.db`.

```bash
docker compose build relay
docker compose run --rm relay init
docker compose up -d relay
```

Provider credentials are not baked into the image. Authenticate with provider
environment variables or provider login commands run through Compose.
`relay-home` persists provider CLI home config across container recreates.

Polling mode is the default and does not require publishing a port. Webhook
setup and image details are documented in [`docs/relay.md`](docs/relay.md).

## Configure Any ACP Agent

Relay has built-in provider types for common CLIs and a generic ACP adapter for
anything else that speaks ACP.

```yaml
runtime:
  providers:
    my-agent:
      type: generic_acp
      generic_acp:
        cmd: ["my-acp-agent", "--stdio"]
        model: "my-model"

relay:
  provider: my-agent
```

Built-in provider types:

- `codex_acp`
- `opencode_acp`
- `copilot_acp`
- `gemini_acp`
- `claude_code_acp`
- `generic_acp`
- `pool`

## Bot Commands

- `/topic <name>`: create a named topic session.
- `/reset`: clear conversation history for the current session.
- `/close`: reset history, then close the current topic or restart the owner session on the next message.
- `/cancel`: cancel in-flight work and drop queued turns for the current session.
- `/memory`: print current `${relay.state_dir}/MEMORY.md` contents when memory is enabled.
- `/start owner=<owner_token>`: authenticate the owner in direct messages.
- `/start invite=<invite_token>`: onboard a collaborator in direct messages.
- `/user add|list|remove`: manage collaborators; owner only.

## Configuration

Relay loads `.config/relay/config.yaml`, then applies `RELAY_*` environment
overrides. If `.env` exists in the working directory, Relay loads it before
config resolution.

Minimal shape:

```yaml
runtime:
  providers:
    <provider_id>:
      # generic_acp | gemini_acp | codex_acp | opencode_acp | copilot_acp | claude_code_acp | pool
      type: <provider_type>
  mcp_servers: {}

relay:
  provider: <provider_id>
  telegram:
    token: ""
    formatting_mode: "markdownv2"
    plan_updates: true
    webhook:
      enabled: false
      listen_addr: "0.0.0.0:8080"
      path: "/telegram/webhook"
      url: ""
  logger:
    level: "info"
    pretty: true
  working_dir: ""
  state_dir: ".config/relay"
  sessions:
    persistence: "sqlite"
  memory:
    enabled: true
  workspace:
    mode: "auto"
    base_branch: ""
  mcp_servers: []
  global_instruction: ""
```

Common settings:

- `relay.provider`: provider ID selected during `relay init`.
- `relay.telegram.token`: Telegram bot token, usually supplied by `.env` as `RELAY_TELEGRAM_TOKEN`.
- `relay.sessions.persistence`: `sqlite` by default; keeps ADK conversation history across restarts until `/reset` or explicit `/close`.
- `relay.memory.enabled`: `true` by default; controls `${relay.state_dir}/MEMORY.md`, `/memory`, and `relay.memory.*` MCP tools.
- `${relay.state_dir}/SOUL.md`: optional operator instructions read at session start/restore when the file exists.
- `relay.workspace.mode`: `auto` by default; uses git worktrees when Relay runs in a git repository.
- `relay.mcp_servers`: extra MCP server IDs added to every Relay-started session.

## MCP Servers

MCP servers can be attached to providers or injected into every Relay session.
Relay also includes a built-in `relay` MCP server for memory and workspace
tools.

### MCP Servers Example

```yaml
runtime:
  mcp_servers:
    local-tools:
      type: stdio
      cmd: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
    remote-tools:
      type: http
      url: https://mcp.example.com/mcp

  providers:
    codex:
      type: codex_acp
      mcp_servers:
        - local-tools

relay:
  provider: codex
  mcp_servers:
    - remote-tools
```

Effective MCP IDs are built-in relay + provider mcp_servers + relay.mcp_servers.
Do not define `runtime.mcp_servers.relay`; Relay owns that bundled server.

## Troubleshooting

- `telegram token is required`: run `relay init`, set `RELAY_TELEGRAM_TOKEN` in `.env`, or set `relay.telegram.token` in config.
- `no supported agent CLI detected`: install or expose one of `codex`, `opencode`, `copilot`, `gemini`, or `claude`.
- `relay.provider is required`: rerun `relay init` or set `relay.provider` to a configured provider ID.
- Session history should not survive restarts: set `relay.sessions.persistence=memory` or `RELAY_SESSIONS_PERSISTENCE=memory`.
- Memory facts are not visible in an active session: memory is snapshotted when a session starts or restores; use `/reset` or `/close` to recreate the provider session.
- Workspace import/export issues: check `relay.workspace.mode`, `relay.workspace.base_branch`, and that Relay is running in the expected git checkout.
- Progress updates are too noisy: set `relay.telegram.plan_updates=false`.

## Documentation

- Technical specification: [`docs/relay.md`](docs/relay.md)
- Release notes: [`docs/release-notes.md`](docs/release-notes.md)
- Telegram formatting guide: [`docs/telegram-formatting.md`](docs/telegram-formatting.md)
- Contributing guide: [`CONTRIBUTING.md`](CONTRIBUTING.md)
- Agent workflow/policies: [`AGENTS.md`](AGENTS.md)

## Release

- GitHub Releases: <https://github.com/normahq/relay/releases>
- npm package: <https://www.npmjs.com/package/@normahq/relay>
