# Plugins and session capabilities

This page is the canonical reference for Balda's Agent Plugins package support,
Balda-specific command extension, and session capability lifetime. Marketplace
repository layout is documented separately in the
[marketplace format ADR](../architecture/plugin-marketplace-format.md).

## Backoffice package management

Administrators manage the global package catalog through **Operations →
Plugins**, with **Installed** and **Marketplaces** tabs. Add a named local or
Git marketplace, explicitly **Refresh marketplace**, then review the qualified
package and exact revision before **Install plugin**. A page read does not fetch
a source. Failed refresh keeps the last successful discovery visible as stale
reference and disables candidate review until a successful refresh.

Installed and discovered package names carry a **Balda extension** marker when
the displayed revision's `plugin.json` declares
`extensions["dev.baldaworks.balda"]`. It reports declaration even if the
extension is invalid or supplies no usable commands. Enabled state, extension
diagnostics, and runtime health remain separate; stale discovery shows the
last successful candidate's declaration until another successful refresh.

The configured marketplace name is the selection identity; it can differ from
the index's manifest name. Upgrade resolves the proven installation origin and
cannot switch to another publisher merely because it offers the same package
name. Ref and newline-separated Sparse paths apply only to Git sources. Source
is never refilled, and stored browser projections redact credentials and local
paths. See [Backoffice Plugins management](backoffice.md#plugins-management)
for exact fields and outcomes.

Enable/disable and rollback affect new catalog selections. Rollback chooses an
exact retained package revision and preserves Enabled. Remove deactivates the
installation without purging plugin data or revision archives; removing its
marketplace definition also preserves those resources. Disabled packages can
still be inspected. Runtime health, selected/current revision and Enabled are
separate states.

Installation stages immutable bytes and prepares the complete catalog before
guarded durable acceptance. SQL acceptance records state and audit together;
filesystem staging and catalog publication are separate steps. An accepted
intent whose publication/completion fails remains **Accepted; recovery pending**
until ordinary recovery reconciles it. Reopen the detail before retrying a
conflict or pending operation. One process must own the state directory; its
catalog lock does not provide coordination across independently running Balda
processes.

Management does not reset existing sessions or change their pins. New sessions
select the current catalog; a healthy active conversation explicitly adopts
current capabilities with `/reset`. A failed restore can also replace the saved
ADK session with current capabilities as described below.

MCP availability does not filter catalog selection: an exact restore attempt
keeps every selected plugin server at its captured revision. HTTP/SSE attachment
is invocation-owned, with no startup or idle remote discovery. A failed restore
uses the fresh-session fallback described below.
Balda-owned plugin stdio discovery starts asynchronously under bounded
supervision; provider execution retains its separate child ownership. See
[MCP deadlines](configuration.md#mcp-availability-and-deadlines) for limits and recovery scope.

## Package boundaries

An installed package uses the Agent Plugins 1.0 `plugin.json` as its portable
manifest. Portable `skills/` and `mcp.json` components keep their Agent Plugins
meaning. Balda chat commands are not portable Agent Plugins commands: they are
client-specific data under the stable Balda client namespace
`extensions["dev.baldaworks.balda"]`.

The namespace and schema identity are deliberately different:

- client extension key: `dev.baldaworks.balda`;
- schema title: `balda-extension`;
- schema `$id`:
  `https://baldaworks.dev/schemas/balda-extension/1.0.0/schema.json`.

Balda embeds that Draft 2020-12 schema and validates the extension offline. It
does not resolve the schema `$id` over HTTP.

## Balda extension version 1

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "release-tools",
  "extensions": {
    "dev.baldaworks.balda": {
      "schema_version": 1,
      "commands": [
        {
          "name": "release",
          "description": "Run the release workflow",
          "instruction": "Inspect the release configuration and execute the release workflow."
        }
      ]
    }
  }
}
```

Both the extension object and every command object are closed. Version 1
requires `schema_version` and `commands`; every command requires exactly
`name`, `description`, and `instruction`. Names are normalized lowercase command
tokens. Arrays and strings are bounded by the embedded schema, and duplicate
normalized names invalidate the extension.

A command owns its inline instruction. It does not reference a skill, command
file, agent, model, permission set, or MCP server. Invocation arguments and the
instruction are formatted as user-level input and enter the ordinary durable
session-turn path. They cannot replace Balda's system policy or change access,
approval, retry, workspace, delivery, or runtime configuration.

## Standalone skill roots

Balda captures standalone skills from direct child directories under three
application roots and one session workspace root:

- `$HOME/.agents/skills` for the operating-system account running Balda;
- `$CODEX_HOME/skills`, or `$HOME/.codex/skills` when `CODEX_HOME` is unset,
  for Codex global skills;
- `<state_dir>/skills` for Balda-managed application skills;
- `<workspace>/.agents/skills` for the workspace bound to the session.

The application roots are captured when the application catalog is compiled.
The workspace root is captured when an unpinned session runtime selects its
effective snapshot. A missing root is empty. All roots use the same bounded
loader, validation, revision archive, and lazy reader. Enabled plugins remain a
separate source. Balda does not assign precedence between sources, so duplicate
unqualified names are ambiguous.

## Independently managed skills

**Operations → Skills** provides reviewed Git/local installation and explicit
update, enable/disable and remove for standalone skills. Plugin skills remain
package-owned and link to Plugins; existing global roots remain read-only with
explicit refresh. Complete resources live in the existing immutable archive,
using its direct-child skill layout, while private origin/selection metadata lives
in the shared state database. See [Skills management](backoffice.md#skills-management)
for source selection, recovery and operator flow. No management action resets
or repins an existing session.

## One capability snapshot per session

Balda compiles commands, skill metadata, and MCP server descriptors into an
immutable effective catalog snapshot. Creating a session selects one non-empty
snapshot ID and persists it with the session before the session is exposed.

That snapshot ID fixes all three capability classes for the session lifetime:

- command ingress and `CommandActor` resolve command descriptors from the exact
  retained snapshot;
- provider-visible skill metadata and explicit `$skill:` resolution use that
  snapshot, while selected skill bodies and resources are loaded lazily from
  its exact archived source revision;
- the MCP server list is derived from the same snapshot and supplied when the
  ADK session runtime is created or opened. Its acquisitions live until that
  Balda session runtime closes.

The built-in `/skill <skill> [prompt...]` command explicitly selects a skill
from this snapshot; Slack spells it `/balda skill <skill> [prompt...]`. An
unqualified name must be unique across the snapshot. The
`/skill <plugin>:<skill> [prompt...]` form selects that exact plugin source and
does not fall back to another contribution. The optional trailing prompt is the
ordinary user text for the turn and may be empty. Resolution pins the revision
without reading its body; the body and resources are loaded lazily when the
turn executes. The existing `$skill:` selection mechanism remains available.

Selected skill content is user-level context. It cannot replace Balda's system
policy or weaken permissions and approval controls. A healthy active session
keeps its captured catalog; `/reset` is the explicit path for that session to
adopt a newly installed or changed skill. If an exact persisted snapshot cannot
restore, automatic fresh-session fallback may instead adopt current eligible
capabilities.

MCP is not supplied again with each prompt. A command does not receive an MCP
list and cannot add tools; it merely executes inside the already-created
ADK session runtime, where the model may use the tools already bound there.
Every command, ordinary, and selected-skill turn uses the existing
`TopicSession` runner. No turn constructs or swaps a session runtime.

Catalog activation affects newly created sessions only. Restoring a session
first uses its exact persisted snapshot. If that attempt fails while the caller
and service remain active, Balda replaces the saved ADK session at the same
locator with a fresh one using the current effective snapshot. The replacement
keeps the stable ADK session ID, clears prior ADK state and history, and persists
the new snapshot before exposure; fallback failure remains an error. A legacy
session without a pin adopts the current effective snapshot once and persists
it. `/reset` remains the explicit user action that closes the old runtime and
creates a fresh unpinned runtime with current commands, skills, and MCP servers.

Immutable command instructions are retained in snapshot descriptors. Archived
skill bytes remain readable by exact revision. MCP processes are the only
capability resources with a live acquisition that must be released.

## Session instruction contributions

Balda assembles one provider-neutral session instruction before constructing a
provider runtime. Trusted host extensions may contribute bounded, named
sections through the `agent.SessionInstructionContributor` port. Contributions
receive the immutable session snapshot identity and safe session context, are
ordered by stable contributor ID, and fail session construction on invalid
IDs, duplicates, errors, or size overflow. Each contributor is limited to 256
KiB. The combined extension content defaults to 1 MiB and can be configured
with `balda.session_instructions.max_total_bytes`; zero selects the default.
These limits apply to extension content rather than Balda's base instruction.

This is a host extension boundary, not a portable plugin-data surface. A
contributor cannot replace the built-in Balda instruction, select a different
snapshot, or write provider-specific session metadata. The provider adapter
remains responsible for mapping the assembled instruction to its native
instruction mechanism. ACP providers without such a mechanism receive the
adapter's documented first-prompt fallback.

## Transport behavior

Plugin command names are projected into each compatible transport's local
command registry. Transport parsing and authentication remain provider-specific,
but accepted invocations publish the same transport-neutral command payload.
`CommandActor` is the sole command executor and the generic plugin adapter
publishes one deduplicated normal session turn.

Built-in names remain reserved, duplicate plugin aliases are omitted, and each
transport still applies its own command-token syntax. See
[Command architecture and runtime internals](command-runtime.md) for the
transport matrix, durable routing, retries, and settlement behavior.

## Relation to other hosts

Codex, OpenCode, and other hosts may define their own native plugin or command
configuration. Those formats do not define Balda's extension. A portable
package can carry host-specific values under separate client namespaces;
Balda reads only `dev.baldaworks.balda` and validates it with `balda-extension`.
