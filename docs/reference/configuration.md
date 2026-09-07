# Configuration reference

## Configuration

Balda config is loaded from one selected file (priority order):

1. Embedded defaults (`cmd/balda/balda.yaml`)
2. Runtime config in `.config/balda/config.yaml`
3. Profile app overrides in the same file (`profiles.<name>.balda.*`)
4. Environment variables (`BALDA_*`) via Viper env mapping

Balda also auto-loads a `.env` file at startup (via `godotenv`) from the Balda process working directory only. Values loaded from `.env` are treated as environment variables, so `BALDA_*` entries override file config the same way as exported shell variables.
The selected config file is env-expanded before YAML parsing, so both `$VAR` and `${VAR}` placeholders work anywhere in that file. For `runtime.mcp_servers.<id>` entries with `type: stdio`, the launched MCP process inherits Balda's full process environment by default, and `env` overrides individual variables.

Example `.env`:

```dotenv
BALDA_TELEGRAM_TOKEN=123456:ABCDEF
BALDA_TELEGRAM_FORMATTING_MODE=rich_markdown
BALDA_TELEGRAM_WEBHOOK_ENABLED=true
BALDA_TELEGRAM_WEBHOOK_URL=https://example.com/telegram/webhook
```

Slack Agent deployments use plain HTTP inside Balda. Public HTTPS Request URLs,
certificates, reverse proxies, ingress, and tunnels are deployment
infrastructure outside Balda. Forward the signed Slack Events API traffic to
`balda.slack.agent.events_path` and `/balda` slash-command traffic to
`balda.slack.commands_path` without changing the raw request body.

Config shape:

```yaml
runtime:
  providers:
    <provider_id>:
      type: <provider_type>
  mcp_servers: {}
balda:
  provider: <provider_id>
  session_memory:
    enabled: true
    provider: ""  # optional extraction provider; empty falls back to balda.provider
  telegram:
    token: ""
    formatting_mode: "rich_markdown"
profiles:
  <profile>:
    balda:
      provider: <provider_id>
```

### Docker Compose Runtime

Balda ships a maintained root `Dockerfile` and `compose.yaml` for local Docker
Compose runtime. This path is the local workspace-oriented runtime: Compose
builds the image from the root Dockerfile and mounts the current project
directory as the runtime workspace.

The `Dockerfile` uses a Node Bookworm runtime with the common tools Balda needs:

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
      @baldaworks/balda \
      @openai/codex \
      opencode-ai \
      @google/gemini-cli \
      @anthropic-ai/claude-code \
      @github/copilot \
 && npm cache clean --force

RUN command -v balda \
 && command -v codex \
 && command -v opencode \
 && command -v gemini \
 && command -v claude \
 && command -v copilot

USER node

WORKDIR /workspace
ENTRYPOINT ["balda"]
```

The `compose.yaml` uses a current-directory bind mount:

```yaml
services:
  balda:
    build: .
    working_dir: /workspace
    volumes:
      - .:/workspace
      - balda-home:/home/node
    command: start

volumes:
  balda-home:
```

With `.:/workspace`, Balda resolves the default runtime paths inside the mounted
project:

- `.env` is loaded from `/workspace/.env`.
- `.config/balda/config.yaml` remains the selected app config.
- `.config/balda/state.db` persists owner auth, session metadata, task
  read-model state, MCP KV, durable memory, and Telegram polling offsets on the host.
- Existing `.config/balda/MEMORY.md` content is imported into state DB memory once
  when `balda.memory.enabled=true` and KV memory is empty.
- `.git` stays visible to `balda.workspace.mode=auto|on`, so workspace mode sees
  the same repository as host execution.
- `balda-home` persists provider CLI auth/config written under `/home/node`.

Balda auto-loads `/workspace/.env`. `env_file: .env` is optional after the file
exists, but should not be required for the first `docker compose run --rm balda init`.

The container image bundles Balda plus every provider CLI detected by
`balda init`: `codex`, `opencode`, `copilot`, `gemini`, and `claude`. Claude
Code is detected through the real `claude` binary; `claudecode` is not a
supported binary name. Provider credentials are not baked into the image.
Authenticate through provider environment variables or by running provider login
commands through Compose. If you need fully repeatable builds, pin `NODE_IMAGE`
to a digest or concrete supported Bookworm tag, and pin the Dockerfile package
build args to exact npm versions: `BALDA_NPM_PACKAGE`, `CODEX_NPM_PACKAGE`,
`OPENCODE_NPM_PACKAGE`, `GEMINI_NPM_PACKAGE`, `CLAUDE_CODE_NPM_PACKAGE`, and
`COPILOT_NPM_PACKAGE`.

### Published GHCR Image

Balda also publishes an official container image at
`ghcr.io/baldaworks/balda:latest`. Unlike the local Compose Dockerfile, the
published image is built from the tagged source tree with
`Dockerfile.release`, so the Balda binary comes from the release commit rather
than from the npm package.

The published GHCR image is intentionally minimal. It contains only the
`/usr/local/bin/balda` binary and an absolute Balda entrypoint. It does not
bundle provider CLIs such as `codex`, `opencode`, `copilot`, `gemini`, or
`claude`, and it is not the documented all-in-one runtime equivalent of local
Compose.

Treat `ghcr.io/baldaworks/balda:latest` as a source stage for downstream bot
images. Copy `balda` from it, then add exactly the provider CLI runtime you
want in your own final image. A concrete Codex example:

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

In that pattern, your final runtime image owns provider auth, provider
environment variables, any extra system packages, and any persisted home
directory layout required by the selected CLI.

For the bundled local runtime flow, keep using the root `Dockerfile` and
`compose.yaml`. That Compose path still mounts the host checkout into
`/workspace`, keeps `.env`, `.config/balda/config.yaml`,
`.config/balda/state.db`, and `.git` on the host, and persists provider CLI
auth/config in the named `balda-home` volume.

The published image is released from Git tags through `release.yml` and is
currently tagged only as `latest`. OCI labels still record the release tag,
commit SHA, source repository, and build timestamp.

Polling mode is the default and does not require a published port. Webhook mode
requires `balda.telegram.webhook.enabled=true`,
`balda.telegram.webhook.url=https://.../telegram/webhook`, and a published local
listener such as `8080:8080`; TLS and public routing should be handled outside
the Balda process.

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

#### Knowl sidecar

Knowl is integrated as an ordinary external MCP server. Start and configure the
Knowl service separately, including its workspace, storage, provider, and
listener:

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

The server ID and tools retain Knowl naming: `knowl`, `knowl_retrieve`,
`knowl_ingest`, and `knowl_operation`. Balda does not embed or supervise
Knowl, initialize its workspace, or own its provider and persistence. There is
no automatic turn or memory ingestion; content enters Knowl only through an
explicit tool call or another operator-controlled ingest path.

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
      codex_acp:
        reasoning_effort: high
      mcp_servers:
        - python-tools

balda:
  provider: codex
  mcp_servers: []  # extra servers added to all sessions
```

#### Bundled Balda MCP Server

The balda MCP server (`balda`) is automatically included in all sessions. It provides:

- `balda.state` - persistent key-value storage
- `balda.memory.read` - read durable memory from `state.db` when `balda.memory.enabled=true`
- `balda.memory.remember` - append a durable fact to `state.db` memory when `balda.memory.enabled=true`
- `balda.workspace.import` - import workspace from base branch
- `balda.workspace.export` - export workspace to base branch

`balda.memory.remember` is for explicit user requests such as "remember this".
It updates durable memory and its latest-update timestamp immediately. New
sessions start with the complete current memory plus its version and timestamp.
Before each active or restored session turn invokes the provider, Balda compares
the timestamp carried by that turn (falling back to runtime session state) with
one current memory snapshot. A missing or different timestamp refreshes session
state and prepends the complete current global-memory snapshot to that same user
prompt inside an `[application-memory]` block. The block identifies remembered
text as durable context data, not a new user command. An equal timestamp, empty
memory, or disabled memory leaves the user prompt unchanged. The global fact
store is expected to remain small, so changed turns inject the full snapshot,
not only facts written since the previous timestamp.

### Agent permission review

- `balda.permissions.mode`: `allow_all`, `ask`, or `deny_all` (default `allow_all`)
- `balda.permissions.timeout`: maximum time to wait for a reply in `ask` mode
  (default `2m`)

Environment overrides are `BALDA_PERMISSIONS_MODE` and
`BALDA_PERMISSIONS_TIMEOUT`. `allow_all` is retained for backward
compatibility, but it grants every agent request using an allow option and should
only be used for trusted agents. `ask` sends a redacted permission prompt to
the initiating user on Telegram or Slackagent. Replies can use the displayed
number, exact option ID, or option name. Unknown channels, another user's
reply, timeout, cancellation, or missing interaction context never grant the
request.

Example:

```yaml
balda:
  permissions:
    mode: ask
    timeout: 2m
```

### Telegram settings

- `balda.telegram.token`: bot token (required)
  - `balda init` validates token via Telegram API and can store it either in:
    - CWD `.env` as `BALDA_TELEGRAM_TOKEN` (default)
    - balda config file key `balda.telegram.token`
  - when `.env` storage is selected, existing `.env` content is preserved and `BALDA_TELEGRAM_TOKEN` is upserted
- `balda.telegram.formatting_mode`: final assistant response format mode.
  - allowed values: `rich_markdown`, `rich_html`, `none`
  - default: `rich_markdown`
  - `rich_markdown` accepts Markdown/plain text from the model and sends it with Telegram rich messages
  - `rich_html` accepts rich-message HTML from the model and sends it with Telegram rich messages
  - `none` sends literal plain text without Telegram `parse_mode`
  - invalid values fail startup
  - migration is a hard cut: replace an older `markdownv2` value with `rich_markdown` (or `none`) and an older `html` value with `rich_html` (or `none`); no compatibility aliases are accepted
  - before a mixed-version upgrade, stop old ingress and drain pending actor commands so old delivery payloads do not cross the format-contract boundary
  - see [Telegram Message Formatting](../telegram-formatting.md) for supported tags, unsupported tags, and escaping behavior
- `balda.telegram.plan_updates`: control visibility of work-plan progress in Telegram (default: `true`)
  - `true`: DM chats replace generic thinking placeholders with plan snapshots when the provider emits plan updates
  - `true`: public chats/topics send a plain-text message for each distinct plan snapshot
  - `false`: plan progress remains hidden; Balda still emits progress activity, sends typing indicators, and keeps DM thinking drafts instead of plan snapshots
- `balda.telegram.webhook.enabled`: enable local HTTP webhook endpoint (`true` => webhook mode, `false` => polling mode; default: `false`)
- `balda.telegram.webhook.url`: outgoing Telegram webhook URL (required when `balda.telegram.webhook.enabled=true`)
- `balda.telegram.webhook.auth_token`: webhook auth token required when `balda.telegram.webhook.enabled=true`; Telegram sends it as `X-Telegram-Bot-Api-Secret-Token`
- `balda.telegram.webhook.listen_addr`: local webhook listen address (default: `0.0.0.0:8080`)
- `balda.telegram.webhook.path`: local webhook path (default: `/telegram/webhook`)
- Telegram polling holds the persisted update offset until the provider event
  has completed runtime settlement. Accepted and terminal events advance the
  offset; retryable handler failures leave it unchanged so Telegram replays the
  same update ID. Polling retries are bounded and then become terminal to avoid
  a poison-update loop. Webhook delivery keeps its existing request settlement
  and does not use the polling offset gate.
- `balda.zulip.bot_email`: Zulip outgoing webhook bot email (required when `balda.zulip.webhook.enabled=true`; env: `BALDA_ZULIP_BOT_EMAIL`)
- `balda.zulip.api_key`: Zulip bot API key used for REST replies (required when `balda.zulip.webhook.enabled=true`; env: `BALDA_ZULIP_API_KEY`)
- `balda.zulip.server_url`: Zulip server base URL, absolute `http://` or `https://` (required when `balda.zulip.webhook.enabled=true`; env: `BALDA_ZULIP_SERVER_URL`)
- `balda.zulip.webhook_token`: Zulip outgoing webhook token that must match the incoming payload token (required when `balda.zulip.webhook.enabled=true`; env: `BALDA_ZULIP_WEBHOOK_TOKEN`)
- `balda.zulip.webhook.enabled`: enable local Zulip outgoing webhook receiver (`true` => Zulip channel enabled; default: `false`; env: `BALDA_ZULIP_WEBHOOK_ENABLED`)
- `balda.zulip.webhook.listen_addr`: local Zulip webhook listen address (default: `0.0.0.0:8090`; env: `BALDA_ZULIP_WEBHOOK_LISTEN_ADDR`)
- `balda.zulip.webhook.path`: local Zulip webhook path, which must start with `/` (default: `/zulip/webhook`; env: `BALDA_ZULIP_WEBHOOK_PATH`)
- `balda.slack.bot_token`: Bot OAuth Token used for Slack Agent Session and chat methods (required when Slack Agent is enabled; env: `BALDA_SLACK_BOT_TOKEN`)
- `balda.slack.signing_secret`: signing secret used to verify exact Events API and slash-command requests (required when Slack Agent is enabled; env: `BALDA_SLACK_SIGNING_SECRET`)
- `balda.slack.commands_path`: local `/balda` slash-command path, which must start with `/` and differ from the Agent Events path (default: `/slack/commands`; env: `BALDA_SLACK_COMMANDS_PATH`)
- `balda.slack.agent.enabled`: enable Slack Agent HTTP ingress (default: `false`; env: `BALDA_SLACK_AGENT_ENABLED`)
- `balda.slack.agent.listen_addr`: local Agent Events listener (default: `0.0.0.0:8092`; env: `BALDA_SLACK_AGENT_LISTEN_ADDR`)
- `balda.slack.agent.events_path`: local Agent Events path, which must start with `/` (default: `/slack/agent/events`; env: `BALDA_SLACK_AGENT_EVENTS_PATH`)
- `balda.slack.agent.enable_streaming`: deliver responses through Slack streaming methods instead of `chat.postMessage` (default: `false`; env: `BALDA_SLACK_AGENT_ENABLE_STREAMING`)
- `balda.slack.agent.suggested_prompts`: enable Slack Agent suggested prompts (default: `false`; env: `BALDA_SLACK_AGENT_SUGGESTED_PROMPTS`)
- `balda.webhooks.enabled`: enable generic inbound webhook receiver (default: `false`)
- `balda.webhooks.listen_addr`: local inbound webhook listen address (default: `127.0.0.1:8090`)
- `balda.webhooks.routes`: route table keyed by route name
  - required when `balda.webhooks.enabled=true`
  - each route requires:
    - `path`: local inbound webhook path (for example `/webhook/release`)
    - `prompt_template`: Go `text/template` rendered with `RequestID`, `Path`, `Method`, `RawBody`, and `Headers`
  - optional `envelope`:
    - `target` + `key`: destination address (defaults to `alias` + `owner`)
      - `target=locator` consumes a locator ref in the form `<channel_type>:<address_key>`; `/locator` prints the current session value
      - `target=alias` consumes an alias key (defaults to `owner`, with optional channel qualification such as `owner@telegram` or `owner@slackagent`); resolves via the transport-neutral destination resolver, supporting multiple concurrent channels and default selection with fallback to legacy Telegram owner state when no explicit destination records exist
    - `mode`: `task` (default) or `session`
    - `report_to`: optional destination for progress/final replies
  - optional `auth`:
    - `type`: `none` (default) or `header`
    - `header` + `value` (or `secret_env`) for `type=header`
  - optional `dedupe`:
    - `source`: `request_id` (default), `header`, or `body_sha256`
    - `header` required for `source=header`

### Attachment prompt representation

Telegram documents, photos, and voice messages are persisted under
`balda.state_dir` before the provider turn. For every non-empty regular file
with a preserved or detected MIME type, Balda supplies an ADK `FileData` part
with an absolute, escaped `file://` URI and the persisted display name.
Metadata remains an adjacent text part and does not replace a valid file
reference.

The ACP adapter maps every persisted `FileData` to baseline `ResourceLink`,
regardless of the server's optional image/audio capabilities. Native `Image`
and `Audio` blocks are reserved for inline bytes; `ResourceLink` does not
require a separate capability flag. Balda does not inline-read or size-limit
persisted files for this conversion, and it does not create temporary files.
Missing or empty blobs retain the deterministic metadata fallback; unreadable
or non-regular paths return a stable build error.

### Balda settings

- `balda.working_dir`: optional balda working directory (defaults to process CWD)
- `balda.state_dir`: balda state directory for persistent balda SQLite state (`state.db`).
  - Stores owner/app KV, `balda.state` MCP KV, session metadata, job/read-model state, optional session history, and Telegram polling offset.
  - Schema is migration-versioned and auto-applied on startup.
  - Relative paths are resolved from `balda.working_dir`.
  - Default: `.config/balda`
- `balda.sessions.persistence`: `sqlite|memory` (default `sqlite`)
  - `sqlite`: session history and state are persisted in `state.db` and reused after restart until the session is explicitly closed.
  - `memory`: conversation/runtime state is process-local; only Balda metadata is persisted.
- `balda.memory.enabled`: enable internal durable memory (default `true`)
  - when disabled, Balda does not snapshot durable memory or register `balda.memory.*` MCP tools.
- `balda.session_memory`: optional durable conversation-memory integration (default disabled)
  - `enabled`: starts the serialized JetStream consumer and enables the neutral locator-scoped `session_memory.search` and `session_memory.trace` tools.
  - `provider`: optional ID from `runtime.providers` for isolated extraction; empty falls back to `balda.provider` while enabled.
  - `derivation.timeout` / `derivation.max_output_bytes`: bounds the isolated Norma derivation runtime.
  - completed text-only turns and session reset/close/rotation/shutdown boundaries are published to the dedicated `BALDA_SESSION_MEMORY` stream.
  - `stream` / `consumer`, timeout, retry, and retention fields are validated and must not collide with command/event/DLQ names.
  - search is bound to the authenticated current locator; personal and group
    audiences, including their topics/threads, are classified by concrete
    channel codecs, while the exact `<channel_type>:<address_key>` remains the
    isolation key.
  - recalled text is returned as untrusted reference data and is never treated as instructions or commands.
- `balda.goal.max_iterations`: maximum `/goalkeeper` worker-validator loop iterations (default `25`)
  - invalid values are clamped to `25`.
- `runtime.providers.<provider_id>.<acp_type>.model` and `reasoning_effort`: optional explicit ACP session settings.
  - `reasoning_effort` values: `minimal`, `low`, `medium`, `high`, `xhigh`
  - Explicit values replace conflicting persisted values after `session/resume`; omitted values remain session-controlled, so changing provider configuration does not require `/reset`.
  - ACP option IDs default to `model` and `reasoning_effort`. For a custom server, set `model_config_id` or `reasoning_effort_config_id` to the exact advertised ID (for example `thought_level`).
  - Balda passes the selected provider's values through to the isolated session-memory runtime (or chat runtime for `balda.provider`); they are not inherited across providers.
- `balda.nats.embedded`: run Balda-owned NATS inside the process (default `true`)
- `balda.nats.host` / `port`: embedded listener address (default `127.0.0.1:-1`, random local port)
- embedded NATS transport files live under `${balda.state_dir}/nats`
- `balda.nats.max_memory` / `max_store`: embedded runtime resource caps (defaults `256mb` and `2gb`)
- `balda.runtime`: optional advanced runtime tuning for command handling, retries, backpressure, and failure retention. Most installs should leave it at defaults.
- `/goalkeeper` runs repeated work and validation passes in isolated GoalKeeper worker/validator ADK sessions until the goal passes validation or `balda.goal.max_iterations` is reached.
  - with workspace mode enabled, `/goalkeeper` uses a separate goal worktree and exports passing work to `balda.workspace.base_branch`.
  - with workspace mode disabled, `/goalkeeper` works directly in `balda.working_dir` and records `not_exported` on passing runs.
- internal durable memory uses app KV in `${balda.state_dir}/state.db` when `balda.memory.enabled=true`
  - `balda.memory.read` reads memory from MCP.
  - `balda.memory.remember` appends facts from MCP.
  - every write advances the latest-memory timestamp.
  - on the next turn after a write, active or restored sessions inject the
    complete current snapshot into the provider user prompt and advance the
    turn/session timestamp boundary; unchanged timestamps do not inject memory.
  - existing `${balda.state_dir}/MEMORY.md` content is imported once when KV memory is empty.
- owner auth token is generated during `balda init`, persisted in `state.db`, and reused by `balda start`
  - if token is missing in existing state, `balda start` backfills one-time and persists it
  - if no owner is registered yet, `balda start` logs the owner bootstrap command and auth link again to help finish first-time onboarding
  - after the first successful owner auth, normal startup logs go back to bot identity only and no longer expose owner auth tokens or auth links
  - if an owner is already registered, `balda start` fails fast when the owner session cannot be restored or created
- bundled balda MCP listener always binds to local ephemeral address (`127.0.0.1:0`)
  - bundled routes on this listener:
    - `/mcp/balda` for the built-in balda MCP server
- Balda config is edited via the config file itself, not through MCP.
  - balda agents should use the config path shown in the system instruction and edit `.config/balda/config.yaml` directly
- `balda.mcp_servers`: extra MCP server IDs for all balda-started sessions (must reference IDs declared in `runtime.mcp_servers`)
  - effective MCP IDs = bundled defaults + `runtime.providers.<provider_id>.mcp_servers` + `balda.mcp_servers` (deduplicated)
- `balda.global_instruction`: optional balda-wide global instruction applied to all sessions
  - value: global instruction text included in balda prompt for all agents
  - effective balda instruction order: built-in balda instructions + `balda.global_instruction` + `runtime.providers.<provider_id>.system_instructions`
  - `balda init` generates a channel-aware example prompt
- `balda.workspace.mode`: `on|off|auto` (default `auto`)
  - `on`: always use Git worktrees per session; startup fails if `working_dir` is not a Git repository
  - `off`: run agents directly in balda `working_dir` (no `balda.workspace` namespace)
  - `auto`: enable worktrees only when `working_dir` is a Git repo, otherwise fallback to `off`
- `balda.workspace.base_branch`: base branch used for workspace sync/export (for example `main`, `master`, `develop`)
  - `balda init` detects current HEAD branch and writes it when available
  - if empty, balda resolves base branch from current HEAD at startup
  - `balda.workspace.export` requires main repo to be on this branch
- `balda.workspace.sessions_dir`: directory name under `balda.state_dir` used for per-session worktrees
  - defaults to `sessions`
- Balda auto-starts only its built-in `balda` MCP server. Any additional MCP
  servers must be declared explicitly through `runtime.mcp_servers`,
  provider-level `mcp_servers`, or `balda.mcp_servers`.
