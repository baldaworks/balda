# relay

[![test](https://github.com/normahq/relay/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/normahq/relay/actions/workflows/test.yml)
[![lint](https://github.com/normahq/relay/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/normahq/relay/actions/workflows/lint.yml)

Relay is a Telegram-first control plane for long-running Norma agent sessions.
It gives you one authenticated owner chat, a direct-message owner session, and
topic-scoped sessions started with `/topic <name>`.

## Quickstart

Before you start, have:

- a Telegram bot token from BotFather
- for host installs, at least one supported provider CLI available: `codex`,
  `opencode`, `copilot`, `gemini`, or `claude`
- Node.js/npm, unless you use the Docker Compose flow below

Install Relay:

```bash
npm install -g -y @normahq/relay
```

Initialize Relay in your project:

```bash
relay init
```

`relay init` detects available provider CLIs, validates your Telegram bot token,
creates `.config/relay/config.yaml`, creates `.config/relay/relay.db`, and
prints the next commands. By default, the Telegram token is stored in `.env`.

Start Relay:

```bash
relay start
```

Authenticate in Telegram using the auth URL printed by `relay init` or
`relay start`. You can also send the printed command directly to your bot:

```text
/start owner=<owner_token>
```

After owner auth, send a normal direct message to use the owner session, or
create a named topic session:

```text
/topic <name>
```

## Docker Compose

Relay ships a root [`Dockerfile`](Dockerfile) and [`compose.yaml`](compose.yaml)
for local Docker Compose runtime. The service builds a local image, runs
`relay`, and bind-mounts the current directory as `/workspace`.

```bash
docker compose build relay
docker compose run --rm relay init
docker compose up -d relay
```

The `.:/workspace` mount is intentional. Relay uses the host checkout, `.git`,
`.env`, `.config/relay/config.yaml`, and `.config/relay/relay.db` instead of
baking local state into the image.

Provider credentials are not baked into the image. Authenticate with provider
environment variables or provider login commands run through Compose;
`relay-home` persists provider CLI home config across container recreates.

Polling mode is the default and does not require publishing a port. Webhook
setup and image details are documented in [`docs/relay.md`](docs/relay.md).

## Bot Commands

- `/topic <name>`: owner/collaborator direct-message command that creates a named topic session.
- `/reset`: owner/collaborator command that clears conversation history for the current session.
- `/close`: owner/collaborator direct-message command that resets history, then closes the current topic or restarts the owner session on the next message.
- `/cancel`: owner/collaborator command that cancels in-flight work and drops queued turns for the current session.
- `/memory`: owner/collaborator direct-message command that prints current `${relay.state_dir}/MEMORY.md` contents.
- `/start owner=<owner_token>`: owner authentication/bootstrap in direct messages.
- `/start invite=<invite_token>`: collaborator onboarding in direct messages.
- `/user add|list|remove`: owner-only collaborator management.

## Configuration

Relay loads `.config/relay/config.yaml` and then applies `RELAY_*` environment
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
    persistence: "memory"
  memory:
    enabled: true
    soul_enabled: true
  workspace:
    mode: "auto"
    base_branch: ""
  mcp_servers: []
  global_instruction: ""
```

Common settings:

- `relay.provider`: provider ID selected during `relay init`.
- `relay.telegram.token`: Telegram bot token, usually supplied by `.env` as `RELAY_TELEGRAM_TOKEN`.
- `relay.sessions.persistence`: `memory` by default; set to `sqlite` to keep ADK conversation history across restarts until `/reset` or explicit `/close`.
- `relay.memory.enabled`: enables `${relay.state_dir}/MEMORY.md`, `/memory`, and `relay.memory.*` MCP tools.
- `relay.memory.soul_enabled`: reads optional `${relay.state_dir}/SOUL.md` at session start/restore.
- `relay.workspace.mode`: `auto` by default; uses Git worktrees when Relay runs in a Git repository.
- `relay.mcp_servers`: extra MCP server IDs added to every Relay-started session.

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

See [`docs/relay.md`](docs/relay.md) for the full config, MCP, Docker, session,
and workspace reference.

## Troubleshooting

- `telegram token is required`: run `relay init`, set `RELAY_TELEGRAM_TOKEN` in `.env`, or set `relay.telegram.token` in config.
- `no supported agent CLI detected`: install or expose one of `codex`, `opencode`, `copilot`, `gemini`, or `claude`.
- `relay.provider is required`: rerun `relay init` or set `relay.provider` to a configured provider ID.
- Session history should survive restarts: set `relay.sessions.persistence=sqlite` or `RELAY_SESSIONS_PERSISTENCE=sqlite`.
- Memory facts are not visible in an active session: memory is snapshotted when a session starts or restores; use `/reset` or `/close` to recreate the provider session.
- Workspace import/export issues: check `relay.workspace.mode`, `relay.workspace.base_branch`, and that Relay is running in the expected Git checkout.
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
