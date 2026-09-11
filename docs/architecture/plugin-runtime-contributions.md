# Plugin runtime contributions

Owner: Balda maintainers  
Status: draft

## Context

Balda installs Agent Plugins packages, but the installed components do not yet
share one runtime lifecycle.

- `pluginapp` owns marketplace and installation operations.
- `agentplugin` scans installed packages when the application starts.
- the agent builder copies every discovered `SKILL.md` body into the system
  instruction.
- the independent `CommandActor` resolves an immutable set of Go handlers
  assembled through `commandfx`.
- plugin `mcp.json` files are not projected into the MCP runtime.

This split causes three problems. Installed or removed components are stale
until restart, skills consume context even when they are irrelevant, and a
plugin cannot contribute a coherent command-to-skill-to-tool workflow.

[Agent Plugins 1.0](https://agent-plugins.org/specification) defines portable
skills and MCP servers. Commands are intentionally outside its portable core.
The specification permits client-specific data under reverse-domain keys in
`plugin.json.extensions`, so Balda can define commands without changing the
portable package format.

Balda already has an independent command boundary. Transport ingress parses,
authenticates, and publishes `commandcmd.Payload`; `CommandActor` owns exact-name
routing and command policy. Plugin support must preserve that boundary rather
than introduce another command dispatcher inside the agent or plugin service.

## Decision

Balda will compile every installed plugin package into one immutable,
generation-tagged runtime snapshot. Commands, skills, and MCP servers are typed
contributions projected from that snapshot into their existing execution
subsystems.

The plugin layer owns package discovery, validation, identity, lifecycle, and
snapshot publication. It does not become a generic execution engine.

```text
installed plugin packages
          |
          v
  validate and compile
          |
          v
 PluginSnapshot generation N
    |          |          |
    v          v          v
 commands    skills    MCP servers
    |          |          |
    v          v          v
CommandActor  agent     MCP runtime
```

### Runtime snapshot

A snapshot contains descriptors, resource references, and diagnostics. It does
not contain live handlers, MCP clients, or complete skill bodies.

Conceptually:

```go
type Snapshot struct {
    Generation uint64
    Plugins    map[PluginID]PluginDescriptor
    Commands   map[CommandID]CommandDescriptor
    Skills     map[SkillID]SkillMetadata
    MCPServers map[MCPServerID]MCPServerDescriptor
    Diagnostics []Diagnostic
}
```

Identifiers are stable and namespaced:

- plugin: `<plugin>`
- command: `<plugin>/<command>`
- skill: `<plugin>/<skill>`
- MCP server: `<plugin>/<server>`

Filesystem paths and process-local objects are implementation details behind
reader and runtime ports. They must not be persisted in actor envelopes.

Compilation follows Agent Plugins failure isolation. A fatal `plugin.json`
error rejects that plugin. An invalid skill or MCP component is omitted and
reported while other valid component types remain available. Balda extension
errors disable only the affected Balda contributions. The complete resulting
snapshot is published atomically.

### Skills use progressive disclosure

The skill projection exposes bounded metadata to the agent:

- stable skill ID;
- skill name and description from valid Agent Skills frontmatter;
- plugin identity;
- availability diagnostics when relevant.

`SKILL.md` content is read through a skill resource port only after the model
or a command selects that skill. The selected body is added to the current turn
as user-level instructional context. Skill bodies are never concatenated into
the global system instruction.

Metadata and loaded bodies have separate host-defined budgets. Discovery is
limited to immediate children of `<plugin>/skills/`, and every resolved skill
file and supporting resource must remain within the resolved plugin root.

Each turn pins the snapshot generation it starts with. A later plugin refresh
affects new turns and does not change instructions or tool identities midway
through an active turn.

### Plugin commands remain CommandActor commands

Balda-specific plugin commands are declared under the `works.balda` extension
namespace:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "release-tools",
  "extensions": {
    "works.balda": {
      "commands": [
        {
          "name": "release",
          "description": "Run the release workflow",
          "skill": "deploy"
        }
      ]
    }
  }
}
```

The initial command contribution has only three fields: `name`, `description`,
and a plugin-local `skill` reference. Access, conversation context, approval,
and delivery policy remain host-owned and cannot be weakened by plugin data.
Plugin commands are available only to authenticated Balda users.

The command projection adapts each valid descriptor to the existing command
contract. `CommandActor` remains the only product command executor and keeps
exact-name routing. Its resolver combines:

1. built-in handlers assembled by `commandfx`;
2. the command descriptors from one pinned plugin snapshot.

Built-in command names are reserved and always win. Duplicate or
transport-incompatible plugin aliases are not advertised and produce a
diagnostic; they never shadow another command.

A plugin command handler publishes a normal conversational turn with the
referenced skill selected explicitly and the remaining command arguments as
user input. It does not execute a skill, call an MCP tool, or deliver a response
inside the command router.

```text
provider command
  -> transport parse/auth
  -> commandcmd.Payload
  -> CommandActor
  -> plugin command adapter
  -> normal turn with pinned skill ID and snapshot generation
  -> lazy SKILL.md read
  -> model may use projected MCP tools
```

Direct command-to-MCP-tool execution is not part of the first contract. It
would require a separate typed argument, authorization, approval, retry, and
result-delivery contract. A skill-backed command already composes all three
plugin component types without bypassing agent or actor policy.

### Transport command surfaces are projections

`commandcmd` remains the transport-neutral owner of command envelopes and
advertisement descriptors. The current fixed advertisements become a
projection of built-in commands plus the current plugin snapshot.

Transport adapters continue to own parsing, provider-specific syntax,
whitelists, and advertisement calls. A transport may expose a direct native
alias only when it can represent the neutral descriptor without ambiguity.
Acceptance and advertisement are separate: failure to advertise a valid
plugin command must not corrupt the runtime snapshot.

The exact user-facing alias mapping for each transport is a transport contract,
not part of the Agent Plugins package format. Canonical plugin command IDs
remain stable even when provider aliases differ.

### MCP servers are reconciled from the same snapshot

Balda validates plugin-root `mcp.json` according to its declared Agent Plugins
version and projects valid server descriptors into the existing MCP runtime.
Server names are namespaced by plugin ID before they enter the host registry.

The MCP projection resolves `${PLUGIN_ROOT}` and `${PLUGIN_DATA}`, enforces
filesystem containment, supports only configured transports, and uses the
existing host permission and credential mechanisms. Plugin package data cannot
grant credentials or bypass approval policy.

Snapshot publication describes desired MCP state. A reconciler starts, stops,
or reconnects servers for the new generation. Connection and authentication
failures update runtime health and diagnostics but do not remove valid skills
or commands from the snapshot. A turn receives only tools that are healthy for
its pinned generation.

### Lifecycle and consistency

Installation, upgrade, removal, and external package changes trigger the same
refresh flow:

1. discover all installed package roots;
2. validate portable components and implemented Balda extensions;
3. compile a complete candidate snapshot and diagnostics;
4. reject the candidate if global invariants such as stable-ID uniqueness fail;
5. atomically publish the next generation;
6. notify command, agent, MCP, and transport-advertisement projections;
7. reconcile process-local resources and report their health separately.

Package mutation and snapshot publication must not expose a partially copied
plugin directory. Installation uses staging plus an atomic directory replace;
removal first publishes a generation without the plugin and deletes retired
package data after no active turn references that generation.

A queued command is resolved against the snapshot current when
`CommandActor` begins handling it. If its plugin or command is no longer
available, the actor returns a deterministic unavailable result. Once the
command publishes a turn, the selected skill ID and generation are carried in
the turn contract so retries cannot silently bind to a newer plugin version.

### Ownership boundaries

- the plugin application layer owns install state, validation orchestration,
  immutable snapshots, refresh, and diagnostics;
- a dedicated transport-neutral plugin contract package owns contribution IDs
  and descriptors;
- `actors/command` owns command resolution and behavior;
- `commandcmd` owns neutral command payload and advertisement contracts;
- the agent layer owns skill selection and turn-context assembly through a
  small snapshot/reader port;
- the MCP runtime owns server processes, connections, tool discovery, and
  health through a small snapshot projection port;
- `commandfx` and the application composition root provide adapters and wiring;
- `channel/*` packages remain provider-specific ingress and delivery adapters.

No command actor, agent builder, or transport adapter imports `pluginapp` for
convenience. Consumers depend on local ports implemented by composition-root
adapters.

## Alternatives considered

### Inject all skill bodies into the system instruction

This is the current behavior. It consumes context for unused skills, gives
third-party text system-level placement, and cannot refresh consistently. It is
replaced by metadata discovery plus turn-scoped loading.

### Add a skills-only MCP API

This would create a second control plane that does not solve command routing,
plugin lifecycle, or MCP server activation. Skills are resources projected
from the shared snapshot; MCP may expose them later, but it is not their owner.

### Let plugins register native Go handlers

Dynamic host code would bypass package portability and make trust, upgrades,
and process isolation much harder. The first command contract is declarative
and skill-backed.

### Define one generic `Capability.Execute` interface

Commands, skills, and MCP servers have different execution and failure
semantics. A generic executor would move policy into the plugin layer and blur
existing ownership. Balda unifies lifecycle and identity while preserving
typed projections.

### Refresh each subsystem independently

Independent command, skill, and MCP reloads allow mixed generations. One
snapshot and pinned generations provide a consistent input to every
projection.

## Consequences

### Positive

- Plugins can provide one coherent command, instruction, and tool workflow.
- `CommandActor` remains the authoritative command boundary.
- Unused skills no longer consume the system prompt.
- Install, upgrade, removal, and local edits become visible without restart.
- Namespacing and pinned generations make retries and concurrent refreshes
  deterministic.
- Portable Agent Plugins components remain portable; commands live in an
  explicit Balda client extension.

### Negative

- The command resolver and advertisements must support changing projections.
- Turn contracts need selected-skill and plugin-generation identity.
- MCP lifecycle gains reconciliation and health reporting.
- Retired snapshot resources must remain available while active turns drain.
- Provider command constraints require per-transport alias policy.

## Migration

The change is delivered as one runtime contract even if implemented in several
internal steps:

1. introduce contribution descriptors, diagnostics, snapshot compilation, and
   generation pinning;
2. replace full-body prompt injection with skill metadata and a bounded reader;
3. project Balda command extensions through `CommandActor` and dynamic neutral
   advertisements;
4. project `mcp.json` into the MCP runtime reconciler;
5. connect install, upgrade, removal, and filesystem change events to atomic
   snapshot refresh;
6. remove the startup-only `agentplugin.Catalog` path after all projections use
   the shared snapshot.

No intermediate release may expose plugin commands while their referenced
skill and MCP projections come from a different generation.

## Related decisions

- [Plugin marketplace repo format](plugin-marketplace-format.md)
- [Command runtime adapter](command-runtime-adapter.md)
- [Application sub-zones](application-zones.md)
- [Runtime contract](runtime-contract.md)

