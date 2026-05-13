# Norma Relay (V1)

`relay start` is a channel-aware background ACP service that currently binds Telegram chats/topics to ADK agents created by Norma's agent factory.

## Summary

- Runtime stack: `tgbotkit/runtime` + Google ADK runners.
- Telegram is the first supported relay channel; future channels should be added as top-level config siblings such as `relay.whatsapp`.
- Main agent: relay app key `relay.provider` (profile overrides via `profiles.<profile>.relay.provider`).
- Subagents: one session per Telegram topic (`message_thread_id`) with dedicated git worktree.
- Relay startup prompt includes workspace settings for each session; in git workspace mode it also includes session/base/current-branch context and workspace MCP guidance.
- Output streaming:
  - Progress updates: non-terminal ADK events emit channel progress. Telegram maps this to throttled Bot API `sendChatAction` with `typing` for all chats, plus throttled DM-only `sendMessageDraft` thinking placeholders.
  - Final assistant response: Telegram Bot API `sendMessage` with `relay.telegram.formatting_mode` (`markdownv2|html|none`; default `markdownv2`).
- Auth model: one-time owner authorization with startup-generated token.

## User Onboarding Reference

The primary onboarding path runs Relay as a single app with no required backing
services. Relay persists local state in SQLite and uses Telegram polling by
default, so first-time setup does not require Redis, Postgres, object storage,
queues, a public URL, or a webhook endpoint.

npm remains the shortest install path:

```bash
npm install -g -y @normahq/relay
relay init
relay start
```

`relay init` requires a Telegram bot token, detects supported provider CLIs
(`codex`, `opencode`, `copilot`, `gemini`, `claude`), writes
`.config/relay/config.yaml`, initializes `.config/relay/relay.db`, and prints
both an owner auth command and Telegram auth URL. The default token storage is
CWD `.env` as `RELAY_TELEGRAM_TOKEN`.

Owner onboarding is completed in a direct message with the bot by opening the
printed auth URL or sending:

```text
/start owner=<owner_token>
```

After owner auth, users can send normal direct messages to the owner session or
create a named topic session:

```text
/topic <name>
```

The supported Docker Compose onboarding path uses the shipped root
`Dockerfile` and `compose.yaml`:

```bash
docker compose build relay
docker compose run --rm relay init
docker compose up -d relay
```

The Compose service bind-mounts the current directory as `/workspace`, so the
container uses the same `.env`, `.config/relay/config.yaml`,
`.config/relay/relay.db`, and `.git` as host execution.

## Package Dependencies

```mermaid
flowchart TB
    relay_root["github.com/normahq/relay/internal/apps/relay"]
    agent["github.com/normahq/relay/internal/apps/relay/agent"]
    auth["github.com/normahq/relay/internal/apps/relay/auth"]
    telegram["github.com/normahq/relay/internal/apps/relay/channel/telegram"]
    handlers["github.com/normahq/relay/internal/apps/relay/handlers"]
    messenger["github.com/normahq/relay/internal/apps/relay/messenger"]
    middleware["github.com/normahq/relay/internal/apps/relay/middleware"]
    session["github.com/normahq/relay/internal/apps/relay/session"]
    state["github.com/normahq/relay/internal/apps/relay/state"]
    tgbotkit["github.com/normahq/relay/internal/apps/relay/tgbotkit"]
    welcome["github.com/normahq/relay/internal/apps/relay/welcome"]

    relay_root --> agent
    relay_root --> auth
    relay_root --> handlers
    relay_root --> state
    relay_root --> tgbotkit

    telegram --> messenger
    telegram --> session

    handlers --> auth
    handlers --> telegram
    handlers --> messenger
    handlers --> session
    handlers --> welcome

    middleware --> auth

    session --> agent
    session --> state
```

### Dependency Summary

| Package | Import Path | Description | Depends On |
|---------|-------------|-------------|------------|
| `relay` | `internal/apps/relay` | Root application module | agent, auth, handlers, state, tgbotkit |
| `agent` | `internal/apps/relay/agent` | Agent builder & workspace manager | `internal/git`, `pkg/runtime/*` |
| `auth` | `internal/apps/relay/auth` | Owner authentication store | state (interface) |
| `channel/telegram` | `internal/apps/relay/channel/telegram` | Telegram message adapter | messenger, session |
| `handlers` | `internal/apps/relay/handlers` | Telegram command handlers | auth, channel/telegram, messenger, session, welcome |
| `messenger` | `internal/apps/relay/messenger` | Telegram message sending | `tgbotkit/client` |
| `middleware` | `internal/apps/relay/middleware` | Auth middleware | auth |
| `session` | `internal/apps/relay/session` | Session management | agent, state |
| `state` | `internal/apps/relay/state` | SQLite state persistence | `modernc.org/sqlite`, `updatepoller` |
| `tgbotkit` | `internal/apps/relay/tgbotkit` | Telegram bot runtime | `tgbotkit/*` |
| `welcome` | `internal/apps/relay/welcome` | Welcome message builder | (standalone) |

## Startup Order (Required)

Relay startup order is strict:

1. Load Norma + relay config.
2. Start internal MCP lifecycle manager.
3. Start relay provider via `agentfactory.Factory`.
4. Start Telegram runtime receiver.

Internal MCP v1 scope is config + lifecycle plumbing; server implementations can be added incrementally.

## Configuration

Relay config is loaded from one selected file (priority order):

1. Embedded defaults (`cmd/relay/relay.yaml`)
2. Runtime config in `.config/relay/config.yaml`
3. Profile app overrides in the same file (`profiles.<name>.relay.*`)
4. Environment variables (`RELAY_*`) via Viper env mapping

Relay also auto-loads a `.env` file at startup (via `godotenv`) from the relay process working directory only. Values loaded from `.env` are treated as environment variables, so `RELAY_*` entries override file config the same way as exported shell variables.
The selected config file is env-expanded before YAML parsing, so both `$VAR` and `${VAR}` placeholders work anywhere in that file. For `runtime.mcp_servers.<id>` entries with `type: stdio`, the launched MCP process inherits Relay's full process environment by default, and `env` overrides individual variables.

Example `.env`:

```dotenv
RELAY_TELEGRAM_TOKEN=123456:ABCDEF
RELAY_TELEGRAM_FORMATTING_MODE=markdownv2
RELAY_TELEGRAM_WEBHOOK_ENABLED=true
RELAY_TELEGRAM_WEBHOOK_URL=https://example.com/telegram/webhook
```

Config shape:

```yaml
runtime:
  providers:
    <provider_id>:
      type: <provider_type>
  mcp_servers: {}
relay:
  provider: <provider_id>
  telegram:
    token: ""
    formatting_mode: "markdownv2"
profiles:
  <profile>:
    relay:
      provider: <provider_id>
```

### Docker Compose Runtime

Relay ships a maintained root `Dockerfile` and `compose.yaml` for local Docker
Compose runtime. This image is a local runtime convenience, not the canonical
OSS release artifact. The Compose service builds a local image and mounts the
current project directory as the runtime workspace.

The `Dockerfile` uses a Node Bookworm runtime with the common tools Relay needs:

```dockerfile
ARG NODE_IMAGE=node:24-bookworm
FROM ${NODE_IMAGE}

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      openssh-client \
      ripgrep \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g \
      @normahq/relay \
      @openai/codex \
      opencode-ai \
      @google/gemini-cli \
      @anthropic-ai/claude-code \
      @github/copilot \
 && npm cache clean --force

RUN command -v relay \
 && command -v codex \
 && command -v opencode \
 && command -v gemini \
 && command -v claude \
 && command -v copilot

USER node

WORKDIR /workspace
ENTRYPOINT ["relay"]
```

The `compose.yaml` uses a current-directory bind mount:

```yaml
services:
  relay:
    build: .
    working_dir: /workspace
    volumes:
      - .:/workspace
      - relay-home:/home/node
    command: start

volumes:
  relay-home:
```

With `.:/workspace`, Relay resolves the default runtime paths inside the mounted
project:

- `.env` is loaded from `/workspace/.env`.
- `.config/relay/config.yaml` remains the selected app config.
- `.config/relay/relay.db` persists owner auth, session metadata, MCP KV, and
  Telegram polling offsets on the host.
- `.config/relay/MEMORY.md` and optional `.config/relay/SOUL.md` stay on the
  host. `MEMORY.md` is used when `relay.memory.enabled=true`; `SOUL.md` is
  always read when present.
- `.git` stays visible to `relay.workspace.mode=auto|on`, so workspace mode sees
  the same repository as host execution.
- `relay-home` persists provider CLI auth/config written under `/home/node`.

Relay auto-loads `/workspace/.env`. `env_file: .env` is optional after the file
exists, but should not be required for the first `docker compose run --rm relay init`.

The container image bundles Relay plus every provider CLI detected by
`relay init`: `codex`, `opencode`, `copilot`, `gemini`, and `claude`. Claude
Code is detected through the real `claude` binary; `claudecode` is not a
supported binary name. Provider credentials are not baked into the image.
Authenticate through provider environment variables or by running provider login
commands through Compose. If you need fully repeatable builds, pin `NODE_IMAGE`
to a digest or concrete supported Bookworm tag, and pin the Dockerfile package
build args to exact npm versions: `RELAY_NPM_PACKAGE`, `CODEX_NPM_PACKAGE`,
`OPENCODE_NPM_PACKAGE`, `GEMINI_NPM_PACKAGE`, `CLAUDE_CODE_NPM_PACKAGE`, and
`COPILOT_NPM_PACKAGE`.

Polling mode is the default and does not require a published port. Webhook mode
requires `relay.telegram.webhook.enabled=true`,
`relay.telegram.webhook.url=https://.../telegram/webhook`, and a published local
listener such as `8080:8080`; TLS and public routing should be handled outside
the Relay process.

### MCP Server Configuration

MCP servers are configured in `runtime.mcp_servers` and referenced by providers via `runtime.providers.<id>.mcp_servers`.

#### Transport Types

| Type | Description |
|------|-------------|
| `stdio` | Process-based stdio communication (recommended for local tools) |
| `http` | HTTP transport with SSE streaming |
| `sse` | Server-Sent Events transport |

#### Stdio MCP Server Example

```yaml
runtime:
  mcp_servers:
    # Local Python tool server
    python-tools:
      type: stdio
      cmd: ["uv", "run", "mcp", "run", "path/to/server.py"]
      env:
        API_KEY: "${PYTHON_TOOLS_API_KEY}"
      working_dir: /path/to/project

    # Node.js based MCP server
    node-tools:
      type: stdio
      cmd: ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
      env:
        DEBUG: "true"
```

#### HTTP MCP Server Example

```yaml
runtime:
  mcp_servers:
    remote-mcp:
      type: http
      url: https://mcp.example.com/mcp
      headers:
        Authorization: "Bearer ${MCP_TOKEN}"
```

#### Using MCP Servers in Providers

```yaml
runtime:
  mcp_servers:
    python-tools:
      type: stdio
      cmd: ["uv", "run", "mcp", "run", "server.py"]

  providers:
    codex:
      type: codex_acp
      mcp_servers:
        - python-tools

relay:
  provider: codex
  mcp_servers: []  # extra servers added to all sessions
```

#### Bundled Relay MCP Server

The relay MCP server (`relay`) is automatically included in all sessions. It provides:

- `relay.state` - persistent key-value storage
- `relay.memory.read` - read `${relay.state_dir}/MEMORY.md` when `relay.memory.enabled=true`
- `relay.memory.remember` - append a durable fact to `${relay.state_dir}/MEMORY.md` when `relay.memory.enabled=true`
- `relay.workspace.import` - import workspace from base branch
- `relay.workspace.export` - export workspace to base branch

`relay.memory.remember` is for explicit user requests such as "remember this".
It updates the file immediately, but running agent sessions keep their existing
session-start snapshot. New or restored sessions read the latest file.

### Telegram settings

- `relay.telegram.token`: bot token (required)
  - `relay init` validates token via Telegram API and can store it either in:
    - CWD `.env` as `RELAY_TELEGRAM_TOKEN` (default)
    - relay config file key `relay.telegram.token`
  - when `.env` storage is selected, existing `.env` content is preserved and `RELAY_TELEGRAM_TOKEN` is upserted
- `relay.telegram.formatting_mode`: final assistant response format mode.
  - allowed values: `markdownv2`, `html`, `none`
  - default: `markdownv2`
  - `markdownv2` accepts normal Markdown/plain text from the model and converts it to Telegram MarkdownV2
  - `html` expects Telegram HTML syntax from the model; Relay escapes unsafe raw text while preserving supported Telegram HTML tags
  - `none` omits Telegram `parse_mode` and sends raw text
  - invalid values fail startup
  - see [Telegram Message Formatting](telegram-formatting.md) for supported tags, unsupported tags, and escaping behavior
- `relay.telegram.plan_updates`: surface ACP plan snapshots in relay progress (default: `true`)
  - `true`: DM chats replace generic thinking drafts with plan snapshots when the provider emits plan updates
  - `true`: public chats/topics send a plain-text message for each distinct plan snapshot
  - `false`: relay keeps legacy progress behavior (`typing` plus DM `Thinking...` drafts)
- `relay.telegram.webhook.enabled`: enable local HTTP webhook endpoint (`true` => webhook mode, `false` => polling mode; default: `false`)
- `relay.telegram.webhook.url`: outgoing Telegram webhook URL (required when `relay.telegram.webhook.enabled=true`)
- `relay.telegram.webhook.auth_token`: optional webhook auth token
- `relay.telegram.webhook.listen_addr`: local webhook listen address (default: `0.0.0.0:8080`)
- `relay.telegram.webhook.path`: local webhook path (default: `/telegram/webhook`)

### Relay settings

- `relay.working_dir`: optional relay working directory (defaults to process CWD)
- `relay.state_dir`: relay state directory for persistent relay SQLite state (`relay.db`).
  - Stores owner/app KV, `relay.state` MCP KV, session metadata, optional ADK session history, and Telegram polling offset.
  - Schema is migration-versioned and auto-applied on startup.
  - Relative paths are resolved from `relay.working_dir`.
  - Default: `.config/relay`
- `relay.sessions.persistence`: `sqlite|memory` (default `sqlite`)
  - `sqlite`: ADK session events and state are persisted in `relay.db` and reused after restart until `/reset` or explicit `/close`.
  - `memory`: ADK conversation/runtime state is process-local; only Relay metadata is persisted.
- `relay.memory.enabled`: enable internal durable memory (default `true`)
  - when disabled, Relay does not snapshot `MEMORY.md`, register `relay.memory.*` MCP tools, or expose `/memory` contents.
- internal durable memory uses `${relay.state_dir}/MEMORY.md` when `relay.memory.enabled=true`
  - `/memory` reads the current file in owner/collaborator direct messages.
  - `relay.memory.read` reads the file from MCP.
  - `relay.memory.remember` appends facts to the file from MCP.
  - memory is snapshotted into ADK session state when a session starts or restores; active sessions are not refreshed after writes.
- optional session-start operator instructions use `${relay.state_dir}/SOUL.md`
  - Relay reads the file on session start/restore when it exists; this is independent from `relay.memory.enabled`.
  - Relay does not expose MCP mutation for `SOUL.md`; edit the file directly.
- owner auth token is generated during `relay init`, persisted in `relay.db`, and reused by `relay start`
  - if token is missing in existing state, `relay start` backfills one-time and persists it
  - if no owner is registered yet, `relay start` logs the owner bootstrap command and deeplink again to help finish first-time onboarding
  - after the first successful owner auth, normal startup logs go back to bot identity only and no longer expose owner auth tokens or auth URLs
  - if an owner is already registered, `relay start` fails fast when the owner session cannot be restored or created
- bundled relay MCP listener always binds to local ephemeral address (`127.0.0.1:0`)
  - bundled routes on this listener:
    - `/mcp` and `/mcp/relay` for the built-in relay MCP server
- Relay config is edited via the config file itself, not through MCP.
  - relay agents should use the config path shown in the system instruction and edit `.config/relay/config.yaml` directly
- `relay.mcp_servers`: extra MCP server IDs for all relay-started sessions (must reference IDs declared in `runtime.mcp_servers`)
  - effective MCP IDs = bundled defaults + `runtime.providers.<provider_id>.mcp_servers` + `relay.mcp_servers` (deduplicated)
- `relay.global_instruction`: optional relay-wide global instruction applied to all sessions
  - value: global instruction text included in relay prompt for all agents
  - effective relay instruction order: built-in relay instructions + `relay.global_instruction` + `runtime.providers.<provider_id>.system_instructions`
  - `relay init` generates a channel-aware example prompt
- `relay.workspace.mode`: `on|off|auto` (default `auto`)
  - `on`: always use Git worktrees per session; startup fails if `working_dir` is not a Git repository
  - `off`: run agents directly in relay `working_dir` (no `relay.workspace` namespace)
  - `auto`: enable worktrees only when `working_dir` is a Git repo, otherwise fallback to `off`
- `relay.workspace.base_branch`: base branch used for workspace sync/export (for example `main`, `master`, `develop`)
  - `relay init` detects current HEAD branch and writes it when available
  - if empty, relay resolves base branch from current HEAD at startup
  - `relay.workspace.export` requires main repo to be on this branch
- Relay is Beads-independent by default and does not auto-start bundled `norma.tasks` MCP.

## Session Model

Session key:

- Owner session: owner DM `(chat_id, topic_id=0)`
- Regular session: any other channel address `(chat_id, topic_id)`, including public `topic_id=0`
- Canonical relay session IDs are channel-scoped. Telegram uses `tg-<chat_id>-<topic_id>`.
- The owner session is bootstrapped for the bound owner DM chat (`topic_id=0`) during activation/startup when an owner is already registered.

Relay always persists session metadata in `relay.db` for lazy restore.
By default, Relay also persists ADK session events and state in `relay.db` until `/reset` or explicit `/close`. Set `relay.sessions.persistence=memory` to keep ADK conversation/runtime state process-local while retaining Relay session metadata for lazy restore.

## Message Flow

1. User sends Telegram message.
   - In non-DM chats (groups/supergroups/topics), relay processes a message when it contains a mention entity for `@<bot_username>` or is a reply to this bot's message.
   - For mention-triggered messages that are replies, relay forwards replied message `text` (fallback `caption`) as model context.
   - In DM chats, relay processes non-command text messages normally.
2. Relay resolves session by `(chat_id, topic_id)`.
3. If the session is missing in memory, relay attempts lazy restore from persisted metadata.
4. Relay calls ADK runner for that session.
5. Relay streams non-terminal ADK event progress to Telegram via chat actions (and DM thinking draft updates).

## Telegram Messaging Behavior

Per model turn:

1. Non-terminal ADK events send throttled `sendChatAction` with `typing` for the same chat/topic; DM chats also emit throttled plain `sendMessageDraft` thinking placeholders using a stable `draft_id`.
   When `relay.telegram.plan_updates=true`, ACP plan snapshots replace generic DM thinking drafts and are sent as plain-text progress messages in public chats/topics.
2. Final assistant text is sent with `sendMessage` using `relay.telegram.formatting_mode`:
   - `markdownv2`: model writes Markdown/plain text; Relay converts it to Telegram MarkdownV2 and sends with `parse_mode=MarkdownV2`.
   - `html`: model writes Telegram HTML; Relay escapes unsafe raw text, preserves supported Telegram HTML tags, and sends with `parse_mode=HTML`.
   - `none`: Relay sends text without `parse_mode`.
3. If send fails at transport level, or Telegram returns parse/escaping API errors (for example `can't parse entities`), relay retries once without `parse_mode`.

## Topic Sessions

Relay runs with a single provider per process (`relay.provider`).

- The provider is initialized before message handling.
- The owner session (`topic_id=0` in the owner DM) is bootstrapped for the owner chat during activation.
- On restart, the owner session follows the same restore path as regular sessions: restore persisted metadata first, then fall back to fresh create only when no persisted record exists.
- Every regular channel address maps to its own ADK session, including public main-chat `topic_id=0`, but all sessions in that relay instance use the same provider runtime.

### Manual session control

- `/topic <name>` (DM only, owner/collaborator): creates a new Telegram topic and a topic-bound session.
  - `<name>` is required.
  - `<name>` is a session label, not a provider selector.
- `/close` (DM only, owner/collaborator): resets current session history, then in the owner DM `topic_id=0` stops the owner session; in topic contexts, closes that topic.
- `/reset` (owner/collaborator): cancels queued work and clears the current session's persisted ADK conversation history without deleting Relay metadata or the workspace branch.
- `/cancel` (owner/collaborator): cancels active turn and drops queued turns for current session.
- `/memory` (DM only, owner/collaborator): prints current `${relay.state_dir}/MEMORY.md` contents when `relay.memory.enabled=true`; otherwise reports that memory is disabled.

### Session restore/create behavior

- Relay restores persisted session metadata on first message after restart.
- When `relay.sessions.persistence=sqlite`, restore reuses the stable ADK session ID and prior ADK event/state history.
- Persisted session label is reused as-is for restore; if missing, relay falls back to label `auto`.
- In workspace mode, restore first tries to sync the session branch with the configured base branch.
- If that sync conflicts, relay recreates a clean worktree on the persisted session branch, restores the session anyway, and sends a short warning that `relay.workspace.import` can retry the sync later.
- If no persisted session metadata exists, relay creates a new regular session using label `auto`.
- Public-channel welcome banners always display `Name: relay` to keep app identity stable, even when the internal persisted session label differs.
- Welcome message uses a user-friendly MarkdownV2 format:
  - Example:
    🚀 **Session Started** • **Name:** `relay` • **ID:** `tg-1-0` • **Model:** `opencode/big-pickle` • **Type:** `opencode_acp` • **MCP:** `relay, workspace`

## Workspace MCP Usage

- `relay.workspace.import`
  - rebases the session workspace onto the configured base branch
  - works for active or persisted sessions as long as workspace metadata exists in `relay.db`
  - is the explicit retry path when restart restore skipped base sync because of a conflict
- `relay.workspace.export`
  - squash-merges the session workspace branch into the configured base branch with the provided Conventional Commit message
  - also works for persisted sessions before lazy restore

## Acceptance/Verification Scenarios

1. Startup order enforces internal MCP -> relay provider -> bot runtime.
2. Polling mode starts by default when `relay.telegram.webhook.enabled=false`.
3. Webhook mode (`relay.telegram.webhook.enabled=true`) fails fast without `relay.telegram.webhook.url`.
4. `/start owner=<token>` registers owner once; `/start invite=<token>` onboards collaborators; non-owner traffic is otherwise rejected.
5. `/topic <name>` creates topic + relay session and persists session metadata.
6. `/topic` without name returns usage error.
7. Restart clears active process sessions, but topic sessions are lazy-restored from persisted metadata.
8. Polling mode resumes from persisted Telegram offset in relay state DB.
9. Non-terminal ADK event progress sends throttled `typing` chat actions in DM and public chats; throttled `sendMessageDraft` thinking placeholders are DM-only.
10. Final assistant response is sent with `sendMessage` using configured `relay.telegram.formatting_mode` with fallback retry without `parse_mode` on transport or parse/escaping API errors.
11. `/close` in a topic resets history and closes that topic; `/close` in the owner DM main chat resets only the owner session.
12. With `relay.sessions.persistence=sqlite`, restart restores ADK conversation history and `/reset` or explicit `/close` clears it for the current session.
