# relay

[![test](https://github.com/normahq/relay/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/normahq/relay/actions/workflows/test.yml)
[![lint](https://github.com/normahq/relay/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/normahq/relay/actions/workflows/lint.yml)

Relay is a Telegram-first control plane for long-running Norma agent sessions.
It gives you one authenticated owner channel, an owner session in direct chat, and topic-scoped sessions started with `/topic <name>`.

## Install

```bash
npm install -g -y @normahq/relay
```

## Quickstart

1. Initialize relay in your project:

```bash
relay init
```

`relay init` will:
- require and validate your Telegram bot token
- let you store the token in `.env` (default) or `config.yaml`
- create `.config/relay/config.yaml`
- create `.config/relay/relay.db`
- generate an owner token and print auth/start commands

2. Start relay:

```bash
relay start
```

3. Authenticate in Telegram:
- open the auth URL printed by `relay init` or, while no owner is registered yet, by `relay start`, or
- send `/start owner=<owner_token>` in a direct message to your bot

4. Start a topic session:

```text
/topic <name>
```

## Run with Docker Compose

Relay does not currently ship an official Docker image, `Dockerfile`, or Compose
file. If you want to run it under Docker Compose, keep the container image as a
local deployment wrapper and mount the current project directory as the runtime
workspace.

Example `Dockerfile`:

```dockerfile
FROM node:lts-bookworm-slim

RUN npm install -g @normahq/relay

# Install the provider CLI used by relay.provider here, for example Codex,
# Gemini, Claude Code, opencode, or another ACP-compatible command.

WORKDIR /workspace
ENTRYPOINT ["relay"]
```

Example `compose.yaml`:

```yaml
services:
  relay:
    build: .
    working_dir: /workspace
    volumes:
      - .:/workspace
    command: start
```

Run setup and start Relay from the repository root:

```bash
docker compose run --rm relay init
docker compose up -d relay
```

The `.:/workspace` bind mount is intentional. It lets Relay use the same
project checkout, `.git`, `.env`, `.config/relay/config.yaml`, and
`.config/relay/relay.db` as a host install. Do not bake `.env` or
`.config/relay/relay.db` into the image.

Relay auto-loads `/workspace/.env`; adding `env_file: .env` is optional after
the file exists.

The default Telegram mode is polling and does not need a published port. For
webhook mode, configure `relay.telegram.webhook.*` or the matching `RELAY_*`
environment variables, then publish the webhook listener:

```yaml
services:
  relay:
    # same settings as above
    ports:
      - "8080:8080"
```

## Bot Commands

- `/start owner=<owner_token>`: direct-message owner auth/bootstrap.
- `/start invite=<invite_token>`: direct-message collaborator onboarding via invite link.
- `/topic <name>`: owner/collaborator, direct message only; starts a topic session with a required free-form name.
- `/close`: owner/collaborator, direct message only; closes current topic session or stops the owner session.
- `/cancel`: owner/collaborator; cancels in-flight turn and drops queued turns for current session.
- `/user add`: owner only; generates collaborator invite link.
- `/user list`: owner only; lists collaborators and active invites.
- `/user remove <user_id>`: owner only; removes a collaborator.

## Configuration

Relay loads `.config/relay/config.yaml` and then applies `RELAY_*` environment overrides.
If present, `.env` from the current working directory is auto-loaded before config resolution.

Config resolution order:

1. bundled defaults
2. `.config/relay/config.yaml`
3. selected profile overrides from `profiles.<name>`
4. `RELAY_*` environment variables

The generated config uses this shape:

```yaml
runtime:
  providers:
    <provider_id>:
      # Required. Supported values:
      # generic_acp | gemini_acp | codex_acp | opencode_acp | copilot_acp | claude_code_acp | pool
      type: codex_acp

      # Optional MCP server IDs from runtime.mcp_servers for this provider.
      mcp_servers: []

      # Optional provider instruction appended after relay.global_instruction.
      system_instructions: ""

      # Type-specific block. Use the block matching `type`.
      codex_acp:
        # Optional model/mode/command settings for ACP agent types.
        model: gpt-5.3-codex
        mode: ""
        cmd: []
        extra_args: []

      # Other ACP type-specific blocks have the same fields:
      # generic_acp: {}
      # gemini_acp: {}
      # opencode_acp: {}
      # copilot_acp: {}
      # claude_code_acp: {}

      # Pool providers use this instead of an ACP block:
      # pool:
      #   members: [codex, opencode]

  mcp_servers:
    <server_id>:
      # Required. Supported values: stdio | http | sse
      type: stdio

      # stdio transport:
      cmd: []
      args: []
      env: {}
      working_dir: ""

      # http/sse transport:
      url: ""
      headers: {}

relay:
  # Provider ID from runtime.providers used for owner and topic sessions.
  provider: ""

  telegram:
    # Required unless RELAY_TELEGRAM_TOKEN is set.
    token: ""

    # Final assistant response mode: markdownv2 | html | none.
    formatting_mode: "markdownv2" # markdownv2 | html | none

    # Surface ACP plan updates in Telegram progress.
    plan_updates: true

    webhook:
      enabled: false
      url: ""
      auth_token: ""
      listen_addr: "0.0.0.0:8080"
      path: "/telegram/webhook"

  logger:
    level: "info"
    pretty: true

  # Defaults to the relay process working directory when empty.
  working_dir: ""

  # Relative paths are resolved from relay.working_dir.
  state_dir: ".config/relay"

  workspace:
    mode: "auto" # auto | on | off
    base_branch: ""

  # Extra runtime.mcp_servers IDs injected into every relay-started session.
  # The built-in `relay` MCP server is reserved and added automatically.
  mcp_servers: []

  # Optional relay-wide instruction applied to all sessions.
  global_instruction: ""

profiles:
  <profile_name>:
    relay:
      provider: <provider_id>
      mcp_servers: []
      global_instruction: ""
```

### MCP Servers Example

Define reusable MCP servers in `runtime.mcp_servers`, then attach them either to one provider with `runtime.providers.<provider_id>.mcp_servers` or to every relay session with `relay.mcp_servers`.

The selected config file is env-expanded before YAML parsing, so both `$VAR` and `${VAR}` work anywhere in the file. For `stdio` MCP servers, the child process inherits Relay's full process environment by default, and `runtime.mcp_servers.<id>.env` overrides individual variables.

```yaml
runtime:
  mcp_servers:
    filesystem:
      type: stdio
      cmd: ["npx"]
      args:
        - "-y"
        - "@modelcontextprotocol/server-filesystem"
        - "/home/me/project"

    github:
      type: stdio
      cmd: ["npx"]
      args:
        - "-y"
        - "@modelcontextprotocol/server-github"
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: "${GITHUB_PERSONAL_ACCESS_TOKEN}"

    remote-tools:
      type: http
      url: "https://mcp.example.com/mcp"
      headers:
        Authorization: "Bearer ${REMOTE_MCP_TOKEN}"

  providers:
    codex:
      type: codex_acp
      mcp_servers:
        - filesystem
      codex_acp:
        model: gpt-5.3-codex

relay:
  provider: codex
  mcp_servers:
    - github
    - remote-tools
```

Effective MCP IDs for a session are:

```text
built-in relay + provider mcp_servers + relay.mcp_servers
```

The built-in `relay` MCP server is always reserved for Relay’s own tools. Do not define `runtime.mcp_servers.relay`; Relay starts that server internally.

## Troubleshooting

- `telegram token is required`:
  - set `RELAY_TELEGRAM_TOKEN` in `.env` (default init flow), or
  - set `relay.telegram.token` in `.config/relay/config.yaml`
- `relay.provider is required`:
  - run `relay init` and choose a provider, or
  - set `relay.provider` to a value from `runtime.providers`
- `relay.provider is not configured` on `/topic <name>`:
  - set `relay.provider` to a value from `runtime.providers`
- workspace import/export issues:
  - verify `relay.workspace.mode` and `relay.workspace.base_branch`
- plan updates are too noisy:
  - set `relay.telegram.plan_updates` to `false` to keep only typing / `Thinking...` progress
  - when `mode: on`, run relay in a Git repository

## Documentation

- Technical specification: [`docs/relay.md`](docs/relay.md)
- Release notes: [`docs/release-notes.md`](docs/release-notes.md)
- Telegram formatting guide: [`docs/telegram-formatting.md`](docs/telegram-formatting.md)
- Contributing guide: [`CONTRIBUTING.md`](CONTRIBUTING.md)
- Agent workflow/policies: [`AGENTS.md`](AGENTS.md)

## Release

- GitHub Releases: <https://github.com/normahq/relay/releases>
- npm package: <https://www.npmjs.com/package/@normahq/relay>
