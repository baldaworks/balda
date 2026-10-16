# Topic sessions

## Overview

Balda chat selects one app-scoped provider configuration per process
(`balda.provider`) and creates an isolated ADK session runtime for each Balda
topic session. Enabled session memory may use a separate provider from
`runtime.providers` for extraction; that provider never changes chat or
GoalKeeper sessions.

- The provider is initialized before message handling.
- The owner main-DM session (`topic_id=0` in the owner DM) is bootstrapped for the owner chat during activation.
- On restart, that owner main-DM session follows the same restore path as regular sessions: Balda first opens the saved ADK session with its exact runtime snapshot. If that restore fails while startup is still active, Balda attempts a fresh ADK session at the same locator with current capabilities.
- Other direct-message main-chat sessions and public/topic sessions are restored or created lazily on demand. They use the same configured provider but not a shared mutable runtime.

## Manual session control

The [command reference](../commands.md) is the sole complete catalog for command
syntax, transport availability, access, context, side effects, non-effects,
errors, and examples.

At runtime, session-control commands operate on the locator attached to inbound
transport data. Reset replaces the runtime session at that locator;
cancel affects its conversational turn queue; GoalKeeper has separate durable
state; close applies the session boundary and may close a provider topic. MCP
tools such as `balda.memory.*` are internal capabilities rather than chat
commands.

## Locator response delivery

The public locator comes from `commandcmd.Payload.Locator`, represented by the
transport-neutral `deliverycmd.Locator` contract and formatted canonically by
`locatorref`. `actorlayer.ActorAddress` identifies an internal envelope sender
or recipient; it is not a public transport destination and is never printed by
the locator command.

A successful locator command resolves the typed
`balda.locator.response.v1` message through the shared structured registry.
Registered transports select these delivery formats:

| Transport | Required registrar | Delivery format |
|---|---|---|
| `slackagent` | `slackagentfx.NewLocatorStructuredRegistrar` | `mrkdwn` |
| `telegram` | `telegramfx.NewLocatorStructuredRegistrar` | `rich_markdown` |
| `zulip` | `zulipfx.NewLocatorStructuredRegistrar` | `markdown` |

The result is sent as `deliverycmd.ModeMarkdown` with bypass settlement and the
renderer-selected explicit format. It does not enter agent-reply streaming or
start/restore a turn. Slack slash-command payloads do not contain a thread
timestamp, so `/balda locator` intentionally returns a conversation-level
locator.

Rendering fails closed. A missing registry, missing transport registration,
invalid canonical locator, empty renderer result, or renderer error produces no
partial or plain fallback locator. Check startup errors for structured registrar
failures. For a request-time failure, inspect the handler error or the Zulip log
message `failed to render locator response`, then verify that the transport Fx
module contributes its locator registrar to
`balda_delivery_structured_registrar`. Also verify that the renderer returns the
format listed above. The complete implementation flow is documented in
[Presentation routing](../architecture/presentation-routing.md#worked-flow-locator-response);
package ownership is documented in the
[transport presentation boundary](../architecture/transport-presentation-boundary.md).

## Session restore/create behavior

- Balda restores persisted session metadata on first message after restart.
- Ordinary channel handlers consume the common conversational preparation path;
  they do not select create or restore. `EnsureSession` owns that choice in the
  session manager, while `RestoreSession` remains restore-only and
  `CreateSession` remains explicit fresh creation for lifecycle commands.
- Preparation uses the saved canonical identity even if an inbound session-ID
  format differs. Concurrent preparation of that identity shares one operation
  lock through runtime initialization, metadata persistence and publication.
- When `balda.sessions.persistence=sqlite`, exact restore reuses the stable session ID and prior session history/state.
- Session metadata includes one non-empty runtime catalog snapshot ID. Restore
  first uses that exact pin. If binding the snapshot or opening the saved ADK
  session fails, Balda attempts a fresh session at the same validated locator
  with the current eligible capabilities. Caller cancellation and shutdown stop
  this fallback.
- Fresh fallback keeps the stable ADK session ID, applies the session rotation
  boundary, and replaces the saved ADK session with Delete/Create. This clears
  its prior ADK state and history. After creation succeeds, Balda persists the
  current snapshot; an ordinary restart then opens that snapshot exactly.
- If binding, deletion, creation, or persistence for the fresh fallback fails,
  restore remains an error and Balda does not expose a partially restored
  session.
- A legacy record without a pin selects and persists current capabilities once.
- Provider construction receives skill metadata and the MCP list from the same
  pinned snapshot. MCP is supplied when the ADK session runtime is created or
  restored, not with each prompt. Commands and explicit skills also resolve
  from that pin.
- Every turn reuses the session's existing runner. `/reset` remains a separate
  explicit action: it closes the runner and creates a new runtime from the
  current catalog snapshot.
- Persisted session label is reused as-is for restore; if missing, balda falls back to label `auto`.
- In workspace mode, restore first tries to sync the session branch with the configured base branch.
- If that sync conflicts, balda recreates a clean worktree on the persisted session branch, restores the session anyway, and sends a short warning that the workspace was reset to the saved session-branch state and Balda can retry the sync later.
- If no persisted session metadata exists, balda creates a new regular session using label `auto`.
- Public-channel welcome banners always display `Name: balda` to keep app identity stable, even when the internal persisted session label differs.
- Welcome messages use the current transport formatting route:
  - Example:
    🚀 **Session Started** • **Name:** `balda` • **ID:** `tg-1-0` • **Model:** `opencode/big-pickle` • **Reasoning:** `none` • **Type:** `opencode_acp` • **MCP:** `balda`

## Workspace allocation contract

- Workspace allocation is session-scoped:
  - session branch name: `norma/balda/<session_id>`
  - workspace dir root: `${balda.state_dir}/<balda.workspace.sessions_dir>/<session_id>` (defaults to `sessions`)
- Allocation lifecycle:
  - on session create/restore in workspace mode, Balda ensures a dedicated worktree at that path
  - if the workspace path already exists with a different branch binding, Balda rejects it as a workspace collision
  - on normal close/stop flows, Balda removes the worktree mount via `CleanupWorkspace`
- Restore and sync behavior:
  - Balda first tries to import/rebase the session branch onto `balda.workspace.base_branch`
  - on conflict, Balda remounts a clean worktree on the same session branch and marks sync skipped
  - the agent/runtime can retry sync later with `balda.workspace.import`; the chat-facing warning does not expose MCP tool names directly
- Source of truth:
  - persisted metadata (`workspace_dir`, `branch_name`) is stored in `state.db` session records
  - job and goal work resolve workspace metadata from session info when commands are dispatched and handled

## Chat archive lifetime activation

Conversational preparation reserves a separate archive UUID before creating a
runtime. The runtime's initial state includes that epoch; it does not replace
the canonical Balda session ID, ADK routing ID, or public locator. After start
hooks succeed, Balda persists activation readiness and the exact binding in
runtime state, then atomically activates the archive and saves its live metadata
binding. A runtime is exposed to intake only after this commit succeeds.
Private schedule and webhook execution sessions do not enter this archive.

Exact restoration continues the saved archive identity. Pre-upgrade sessions
without an archive identity are adopted lazily during ordinary preparation,
with partial coverage beginning at adoption; reading an archive never creates
or restores a runtime. Existing messages are not fabricated from old history.

An interrupted activation can finish only when its runtime epoch, operation
readiness marker and frozen binding agree. Recovery opens the pinned runtime
and commits the same reserved archive, without repeating completed start hooks.
Missing or contradictory readiness remains a preparation error with retained
pending/uncertain evidence. Failed creation or hooks do not expose a successful
active archive. A database failure before reservation prevents runtime creation;
a failure after successful readiness retains the runtime for a later lazy retry.
