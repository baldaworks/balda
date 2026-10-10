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
BALDA_HTTP_BASE_URL=https://example.com
BALDA_HTTP_BASE_PATH=/balda
```

Balda binds one local HTTP listener for Backoffice, generic webhooks, and
enabled HTTP transport callbacks. Public HTTPS Request URLs, certificates,
reverse proxies, ingress, and tunnels are deployment infrastructure outside
Balda. Forward requests to the shared listener with the original path and raw
request body. The handlers keep their existing browser, webhook-secret, and
transport-signature checks.

Config shape:

```yaml
runtime:
  providers:
    <provider_id>:
      type: <provider_type>
  mcp_servers: {}
balda:
  provider: <provider_id>
  http:
    listen_addr: "127.0.0.1:8095"
    base_url: "http://127.0.0.1:8095"
    base_path: ""
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

### Shared HTTP listener and public URLs

`balda.http.listen_addr` is the local `host:port` bind for all HTTP-facing
areas. It defaults to `127.0.0.1:8095`; a port collision aborts startup even
when no webhook route is enabled. `balda.http.base_url` is the public origin
used to display and register callbacks. It must contain only `http://` or
`https://` plus the authority, without credentials, path, trailing slash,
query, or fragment. A non-loopback bind requires an HTTPS origin.
`balda.http.base_path` is an optional canonical root-relative deployment prefix:
empty or an absolute path such as `/balda`, without a trailing slash. Invalid
values fail configuration validation before the listener binds. Environment
overrides are `BALDA_HTTP_LISTEN_ADDR`, `BALDA_HTTP_BASE_URL`, and
`BALDA_HTTP_BASE_PATH`.

The three sibling areas are `<base_path>/backoffice/` for the browser,
`<base_path>/webhooks/<name>` for every config and managed generic webhook, and
`<base_path>/gateway/<transport>/...` for chat transport callbacks. The
Backoffice Webhooks management page is under `/backoffice/webhooks`; it is
separate from the inbound `/webhooks` area. For the agreed **illustrative** asus
values:

```yaml
balda:
  http:
    listen_addr: "127.0.0.1:8095"
    base_url: https://lab.metalagman.dev
    base_path: /balda
```

| Area | Public URL |
| --- | --- |
| Backoffice | `https://lab.metalagman.dev/balda/backoffice/` |
| Webhook management | `https://lab.metalagman.dev/balda/backoffice/webhooks` |
| Config or managed webhook `orders` | `https://lab.metalagman.dev/balda/webhooks/orders` |
| Slack Events | `https://lab.metalagman.dev/balda/gateway/slack/events` |
| Slack Commands | `https://lab.metalagman.dev/balda/gateway/slack/commands` |

The local `listen_addr` does not appear in those public URLs. With an empty
`base_path`, the same area paths start at `/backoffice`, `/webhooks`, and
`/gateway`. If a shared field is omitted, it falls back to the corresponding
`balda.backoffice.listen_addr`, `public_url`, or `base_path` value, then the
default. Set `balda.http.*` explicitly in new configurations. Old per-area
listen-address fields are ignored for binding once the shared listener is in
use; the old Backoffice address fields do not create a separate browser mount.
Every generic webhook derives its callback path from the current shared prefix
and immutable route name; changing `base_path` moves all route addresses after
restart without rewriting route rows. For generic webhook display, only an
explicit `balda.http.base_url` supplies the public origin. If it is omitted,
Backoffice shows the canonical root-relative callback path and an explanation,
even when browser authentication uses a fallback `balda.backoffice.public_url`.
Chat transport callbacks use only their canonical `/gateway` paths.

The live asus ingress, deployment, and Telegram/Slack/external callback
registrations are unchanged by this Story. The table is an application URL
example, not a claim that those addresses are already publicly reachable;
external exposure needs a separate ingress and callback-registration rollout.
See [Backoffice startup and browser security](backoffice.md#startup-and-configuration).

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

### Skill discovery roots

Skill roots are conventions rather than Balda configuration keys. Balda
compiles direct-child skills from the runtime account's `$HOME/.agents/skills`,
from `$CODEX_HOME/skills` (defaulting to `$HOME/.codex/skills`), and from
`<state_dir>/skills` into the application catalog. It separately captures
`<workspace>/.agents/skills` for each unpinned session runtime. In the default
Compose setup, `$HOME` is `/home/node`, `<state_dir>` is
`/workspace/.config/balda`, and `<workspace>` is `/workspace` unless workspace
mode selects an isolated session worktree.

Missing roots are empty. Existing roots are subject to the catalog's bounded
filesystem traversal and validation. The global root does not replace the
state-directory root, and neither overrides workspace or enabled-plugin skills;
duplicate unqualified names remain ambiguous. Restart Balda to compile changes
to application roots, then use `/reset` in an existing conversation to adopt
the current catalog snapshot.

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

Polling mode is the default. The bundled Compose file publishes no HTTP port.
With `balda.telegram.webhook.enabled=true`, an omitted
`balda.telegram.webhook.url` is composed from `balda.http.base_url` and
`balda.http.base_path` at `<base_path>/gateway/telegram/webhook`. Telegram
callbacks enter the shared `balda.http.listen_addr` listener; the old Telegram
bind setting is ignored. The shared bind defaults to loopback
`127.0.0.1:8095`, which is local to the container in Compose. Any container
port exposure, public routing, and TLS termination belong to the deployment,
outside this Story's unchanged ingress and Compose configuration.

### MCP Server Configuration

MCP servers are configured in `runtime.mcp_servers` and referenced by providers via `runtime.providers.<id>.mcp_servers`.

Backoffice also manages durable MCP definitions. Configuration-owned entries
remain read-only there; worker authorization explicitly captures their current
configuration. Provider targets apply when a session is created or reset.
When a provider pool is selected, its MCP selection also applies to that pool's
members for that runtime. It does not change the shared member configuration or
the selection for standalone members or other pools.
Existing and restored sessions keep their captured revision and authorization
binding. Changing a file or saving a managed definition does not update those
sessions. See [MCP management](backoffice.md#mcp-management) for authorization,
readiness and retry operations.

#### Protected values and worker grants

HTTP/SSE OAuth settings and authorization are managed in Backoffice, including
for file-owned server definitions; no OAuth settings need to be added to YAML.
The operator signs in at the external service. Stdio keeps its ordinary command,
arguments and environment configuration.

Set `balda.mcp_management.credential_key`, or its environment override
`BALDA_MCP_MANAGEMENT_CREDENTIAL_KEY`, to a standard base64-encoded 32-byte
deployment key before storing protected values or worker OAuth grants. Supply
it through your deployment's secret mechanism. An empty key supports
installations without encrypted MCP data; protected writes require a key.
Startup validates every retained encrypted revision and worker grant, including
historical session bindings. A missing or different key prevents startup when
that data cannot be decrypted.

Keep the same key across restarts and back it up securely with the database.
Replacing it is not a key rotation operation: no automatic re-encryption or
lost-key recovery is provided. Run one active Balda grant writer. Pending
authorization attempts are process-local and must be restarted after a host
restart; saved encrypted grants persist. Register the browser callback as
`<balda.http.base_url><balda.http.base_path>/backoffice/mcp/oauth/callback`, and use a supported pre-registered
client for device authorization. Browser authorization requires the issuer to
advertise S256 PKCE and `authorization_response_iss_parameter_supported`, with a
callback `iss` value matching the trusted issuer. Use browser authorization after
an unsupported device flow only when the service supports browser authorization.

HTTP/SSE connections with configured headers or worker OAuth use Balda's private
credential bridge for discovery and execution. External ACP clients receive
only the local bridge endpoint and its capability, rather than upstream headers
or OAuth tokens. Headerless connections without OAuth and stdio connections use
their direct transports. Saving authorization and making tools ready are
separate outcomes; retry a saved grant's tool attachment from Backoffice when
publication or discovery failed.

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
- `balda.telegram.webhook.url`: Telegram registration URL; when omitted in webhook mode, Balda composes `<balda.http.base_url><balda.http.base_path>/gateway/telegram/webhook`. An explicit URL must use that canonical path.
- `balda.telegram.webhook.auth_token`: webhook auth token required when `balda.telegram.webhook.enabled=true`; Telegram sends it as `X-Telegram-Bot-Api-Secret-Token`
- `balda.telegram.webhook.listen_addr`: legacy bind setting; ignored in favor of `balda.http.listen_addr`
- `balda.telegram.webhook.path`: obsolete path setting; the shared listener uses `<base_path>/gateway/telegram/webhook`
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
- `balda.zulip.webhook.listen_addr`: legacy bind setting; ignored in favor of `balda.http.listen_addr` (env: `BALDA_ZULIP_WEBHOOK_LISTEN_ADDR`)
- `balda.zulip.webhook.path`: obsolete path setting; the shared listener uses `<base_path>/gateway/zulip/webhook` (env: `BALDA_ZULIP_WEBHOOK_PATH`)
- `balda.mattermost.enabled`: enable the Mattermost bot-account websocket transport (`true` => Mattermost channel enabled; default: `false`; env: `BALDA_MATTERMOST_ENABLED`)
- `balda.mattermost.server_url`: Mattermost server base URL, absolute `http://` or `https://` (required when the Mattermost transport is enabled; env: `BALDA_MATTERMOST_SERVER_URL`)
- `balda.mattermost.token`: Mattermost bot account personal access token (required when the Mattermost transport is enabled; env: `BALDA_MATTERMOST_TOKEN`)
- `balda.mattermost.bot_user_id`: Mattermost bot account user id, used to ignore the bot's own posts (required when the Mattermost transport is enabled; env: `BALDA_MATTERMOST_BOT_USER_ID`)
- `balda.mattermost.bot_username`: Mattermost bot account username, used to detect `@mention` activation in public and private channels (required when the Mattermost transport is enabled; env: `BALDA_MATTERMOST_BOT_USERNAME`)
- `balda.mattermost.commands_enabled`: enable the Mattermost HTTP slash-command receiver (requires `balda.mattermost.enabled=true`; default: `false`; env: `BALDA_MATTERMOST_COMMANDS_ENABLED`)
- `balda.mattermost.commands_listen_addr`: legacy bind setting; ignored in favor of `balda.http.listen_addr` (env: `BALDA_MATTERMOST_COMMANDS_LISTEN_ADDR`)
- `balda.mattermost.commands_path`: obsolete path setting; the shared listener uses `<base_path>/gateway/mattermost/commands` (env: `BALDA_MATTERMOST_COMMANDS_PATH`)
- `balda.mattermost.commands_token`: Mattermost root slash-command integration token (required when `commands_enabled=true`; env: `BALDA_MATTERMOST_COMMANDS_TOKEN`)
- `balda.slack.bot_token`: Bot OAuth Token used for Slack Agent Session and chat methods (required when Slack Agent is enabled; env: `BALDA_SLACK_BOT_TOKEN`)
- `balda.slack.signing_secret`: signing secret used to verify exact Events API and slash-command requests (required when Slack Agent is enabled; env: `BALDA_SLACK_SIGNING_SECRET`)
- `balda.slack.commands_path`: obsolete path setting; the shared listener uses `<base_path>/gateway/slack/commands` (env: `BALDA_SLACK_COMMANDS_PATH`)
- `balda.slack.agent.enabled`: enable Slack Agent HTTP ingress (default: `false`; env: `BALDA_SLACK_AGENT_ENABLED`)
- `balda.slack.agent.listen_addr`: legacy bind setting; ignored in favor of `balda.http.listen_addr` (env: `BALDA_SLACK_AGENT_LISTEN_ADDR`)
- `balda.slack.agent.events_path`: obsolete path setting; the shared listener uses `<base_path>/gateway/slack/events` (env: `BALDA_SLACK_AGENT_EVENTS_PATH`)
- `balda.slack.agent.enable_streaming`: deliver responses through Slack streaming methods instead of `chat.postMessage` (default: `false`; env: `BALDA_SLACK_AGENT_ENABLE_STREAMING`)
- `balda.slack.agent.suggested_prompts`: enable Slack Agent suggested prompts (default: `false`; env: `BALDA_SLACK_AGENT_SUGGESTED_PROMPTS`)
- `balda.features.attachments.max_files_per_message`: maximum files accepted in one inbound attachment set (default: `10`; env: `BALDA_FEATURES_ATTACHMENTS_MAX_FILES_PER_MESSAGE`); a Slack thread turn shares this count between current-message and historical files
- `balda.features.attachments.max_file_bytes`: maximum bytes accepted for one inbound file or one outbound Slack local-file delivery (default: `26214400`, 25 MiB; env: `BALDA_FEATURES_ATTACHMENTS_MAX_FILE_BYTES`)
- `balda.features.attachments.max_total_bytes`: maximum bytes accepted across one inbound message (default: `52428800`, 50 MiB; env: `BALDA_FEATURES_ATTACHMENTS_MAX_TOTAL_BYTES`)
- `balda.features.attachments.store.engine`: inbound attachment persistence engine (`local` or `off`; default: `local`; env: `BALDA_FEATURES_ATTACHMENTS_STORE_ENGINE`)
- `balda.webhooks.enabled`: enable config-owned inbound webhook routes (default: `false`); Backoffice-managed routes have separate enabled state
- `balda.webhooks.listen_addr`: legacy bind setting; ignored in favor of `balda.http.listen_addr`. The shared listener binds on every `balda start`, even when no route is active. Keep it on a private interface or behind a trusted gateway.
- `balda.webhooks.routes`: config-owned route table keyed by immutable route name; entries are read-only in Backoffice and require restart to change
  - names use `^[a-z0-9][a-z0-9_-]{0,63}$`: 1–64 lowercase letters, digits, underscores or hyphens, beginning with a lowercase letter or digit
  - every route receives `<balda.http.base_path>/webhooks/<name>`; with an empty prefix this is `/webhooks/<name>`
  - a declared `path`, including an empty value or a disabled route, fails startup with `balda.webhooks.routes.<name>.path`; remove that field and update the sender URL
  - each route requires:
    - `prompt_template`: Go `text/template` rendered with `RequestID`, `Path`, `Method`, `RawBody`, and `Headers`
  - optional `envelope.report_to`: final-report destination with `target` and `key`
    - `target=locator`: public `<channel_type>:<address_key>` ref; `/locator` prints one
    - `target=managed_alias`: Backoffice-managed name such as `main_chat`
    - `target=alias`: existing role selector such as `owner@telegram`
    - omitted: retain final output in the job without external delivery
    - the current destination is selected when a new request is accepted and stays fixed for deduplication and retries; an unavailable name rejects a new request
  - optional `envelope.ack_on_delivery`: return `200` only after the final report is posted; requires `report_to` (default: `false`)
  - every accepted request executes in a new private session, independent of `report_to`; only final output is sent externally
  - older route keys `target`, `key`, `mode`, `key_from_body`, and `fallback_to` are incompatible and fail startup. Replace an older session-target route with an optional `report_to` and remove those fields; the webhook no longer continues that session.
  - optional `auth`:
    - `type`: `none` (default) or `header`
    - `header` + `value` (or `secret_env`) for `type=header`
  - optional `dedupe`:
    - `source`: `request_id` (default), `header`, or `body_sha256`
    - `header` required for `source=header`

Administrators can create persistent webhook routes at the
[Backoffice Webhooks page](backoffice.md#webhooks-management). These routes
are stored in the selected state database, accept new requests immediately
when enabled, and survive restart independently of `balda.webhooks.enabled`.
Their names cannot collide with config or archived route names. Each managed
route requires the generated
`X-Balda-Webhook-Secret` header. Copy its secret at creation or explicit
rotation because only a verifier is stored and later GETs cannot reveal it.
Its optional **Report to** field accepts a public locator or managed alias
without a destination-type selector. Config routes continue to support
`target=alias` role selectors and their configured authentication. Disabling
or deleting a route prevents new external admissions while retained request
history remains readable. An unresolved alias prevents that particular
request from being admitted; the route definition remains available.

The SQLite and PostgreSQL upgrade uses embedded SQL migrations to remove the
route path column and its active-path index. It preserves route names, source,
state, versions, prompt/report settings, managed secret verifiers and request
input/output history. Existing routes immediately use their canonical URLs;
old custom URLs no longer accept requests. Back up the database before upgrading,
update external senders, and restore that backup with an older binary if a
downgrade is needed; removed custom paths cannot be reconstructed.

- `balda.scheduler.jobs`: config-owned recurring schedules, each with an `id`, five-field UTC `cron`, and `envelope.content`
  - optional `envelope.report_to` uses `target: locator` or `target: managed_alias` with `key`; omit it to retain output only in run history
  - a managed alias may be referenced before its mapping exists. Each cron or manual run selects the current concrete locator at admission. A missing alias fails only that run; the next cron slot remains eligible.
  - an existing `envelope.target: locator` plus `envelope.key` remains supported as a literal report destination. Do not combine it with `report_to`.

```yaml
balda:
  scheduler:
    jobs:
      - id: daily_summary
        cron: "0 9 * * *"
        envelope:
          content: Summarize yesterday's work
          report_to:
            target: managed_alias
            key: main_chat
  webhooks:
    enabled: true
    routes:
      release:
        prompt_template: '{{ .RawBody }}'
        envelope:
          report_to:
            target: managed_alias
            key: main_chat
          ack_on_delivery: true
```

### Attachment storage and prompt representation

Telegram media, files on the triggering Slack Agent event, and eligible files
from preceding Slack thread messages are persisted under
`${balda.state_dir}/attachments` before the provider turn. The `local`
engine streams to a temporary file and publishes a content-addressed blob only
after the configured byte checks pass. The `off` engine disables persistence;
a Slack event containing current files is then rejected as one terminal turn
while text-only Slack behavior remains available. A text mention with only
historical files still proceeds with bounded unavailable markers and no
historical provider attachments. Telegram retains its transport behavior when
persistence is unavailable.

The attachment limits must be positive, and `max_total_bytes` must be at least
`max_file_bytes`. Slack enforces declared and streamed sizes and rejects the
whole current-message media turn when any file or aggregate limit is exceeded.
For a Slack thread mention, current files consume the configured count and
actual-byte budget first. Historical candidates are deduplicated by Slack file
ID, considered newest-first within the remaining capacity, and supplied after
current files in chronological message/file order. Historical over-budget,
unsupported, storage-disabled, and permanently inaccessible files become
bounded untrusted-context markers instead of rejecting the current mention.
Temporary Slack, network, or storage failures retry the whole triggering event
before durable publication.

Current and historical Slack media require the bot `files:read` scope plus
access to the conversation. Slack Connect `check_file_info` placeholders are
resolved with `files.info`; private URLs and authorization material are never
written to context or logs.

Outbound Slack photos and documents require the independent bot `files:write`
scope. Balda accepts one non-empty, non-symlink regular local file and applies
the same `max_file_bytes` limit; it adds no outbound-specific setting. File IDs,
URLs, directories, missing paths, and changed files are rejected before Slack
completion. Balda obtains an external upload ticket, streams the exact bytes,
and completes the file into the locator conversation and root thread with its
filename, MIME type, and caption. The resulting Slack file ID is stored as the
provider message ID.

Definitive pre-completion failures can use the existing retry path. An unknown
completion outcome remains durable state `sending` and is not dispatched again
automatically after restart, preventing duplicate files. Missing `files:write`
affects only outbound media; text delivery and `files:read` ingestion remain
available. Logs and returned errors omit local paths, upload URLs, credentials,
captions, response bodies, and file content. Disabling the inbound attachment
store does not create a remote-source fallback for outbound delivery.

For every non-empty regular file with a preserved or detected MIME type, Balda
supplies an ADK `FileData` part with an absolute, escaped `file://` URI and the
persisted display name.
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
- `balda.state_dir`: local state directory; supplies the default SQLite database location and other local runtime paths.
  - Relative paths are resolved from `balda.working_dir`.
  - Default: `.config/balda`
- `balda.database.type`: `sqlite|postgres` (default `sqlite`).
  - Stores owner/app KV, `balda.state` MCP KV, session metadata, job/read-model state, optional session history, and Telegram polling offset.
  - Schema is migration-versioned and auto-applied on startup.
- `balda.database.sqlite.path`: strict one-pass template, default `{{.StateDir}}/state.db`.
- `balda.database.postgres`: structured `host`, `port`, `name`, `user`, `password`, and `sslmode` settings.
  See [State database](database.md) for copyable examples, template rules and operations.
- `balda.sessions.persistence`: `sqlite|memory` (default `sqlite`)
  - `sqlite`: durable session history in the selected database (SQLite or PostgreSQL), reused after restart until explicitly closed.
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
- internal durable memory uses app KV in the selected database when `balda.memory.enabled=true`
  - `balda.memory.read` reads memory from MCP.
  - `balda.memory.remember` appends facts from MCP.
  - every write advances the latest-memory timestamp.
  - on the next turn after a write, active or restored sessions inject the
    complete current snapshot into the provider user prompt and advance the
    turn/session timestamp boundary; unchanged timestamps do not inject memory.
  - existing `${balda.state_dir}/MEMORY.md` content is imported once when KV memory is empty.
- owner auth token is generated during `balda init`, persisted in the selected database, and reused by `balda start`
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
  - configured-source selection = bundled defaults + `runtime.providers.<provider_id>.mcp_servers` + `balda.mcp_servers` (deduplicated).
  - Provider-open Goose SQL migrations preserve configured MCP pins from snapshots
    written before targeting metadata. Only explicitly marked historical descriptors
    can restore with a matching source/transport/command/arguments/directory/URL and
    environment/header key set. Their original digest omitted binding values, so
    these pins still use host-file values and configured provider defaults. New pins
    bind full values and captured targeting exactly. Migration leaves snapshot IDs,
    sessions and history unchanged; reset remains an explicit user action.
  - Effective selection also includes enabled, available Backoffice-managed definitions matching the requested provider's targets and applicable enabled plugin contributions.
  - Provider and `balda.mcp_servers` lists reference configured IDs; managed definitions use their Backoffice provider targets and do not require a duplicate YAML declaration or ID-list entry.
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
- Balda includes its built-in `balda` MCP server by default. Any additional MCP
  servers must be declared in `runtime.mcp_servers` and selected through provider
  or `balda.mcp_servers` lists, managed through Backoffice with provider targets,
  or supplied by an applicable enabled plugin. A selected current or retained
  revision that is unavailable fails closed; saving a definition or worker grant
  does not make it **Ready**.
