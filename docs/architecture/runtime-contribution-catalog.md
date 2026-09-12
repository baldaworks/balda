# Runtime contribution catalog

Owner: Balda maintainers
Status: draft

## Context

Balda receives runtime capabilities from several sources:

- built-in product commands;
- standalone skills installed for a user or workspace;
- MCP servers declared in host configuration;
- installed Agent Plugins packages containing skills and `mcp.json`;
- Balda-specific command declarations inside Agent Plugins extensions.

These sources currently use unrelated lifecycle paths. `commandfx` assembles an
immutable command router, `agentplugin` scans installed plugin skills once at
startup, the agent builder copies complete `SKILL.md` bodies into the system
instruction, and configured MCP servers use their own startup wiring. Plugin
`mcp.json` files are not loaded.

A plugin-only catalog would not solve the whole problem. Built-in commands,
standalone skills, and configured MCP servers participate in the same name,
prompt-budget, lifecycle, and runtime constraints even though they do not
belong to a plugin.

[Agent Plugins 1.0](https://agent-plugins.org/specification) defines portable
skills and MCP servers. Commands are intentionally outside its portable core.
The specification permits client-specific data under reverse-domain keys in
`plugin.json.extensions`, so Balda can define commands without changing the
portable package format.

Balda also has a durable independent command boundary. Transport ingress
parses, authenticates, and publishes a neutral command; `CommandActor` owns
command execution and policy. Runtime contributions must preserve that
boundary and the existing at-least-once delivery guarantees.

## Decision

Balda will compile enabled application sources into one immutable application
snapshot and scoped sources into immutable overlays. Their deterministic merge
produces the effective runtime contribution snapshot. Plugins are one source of
typed contributions rather than the owner of the global catalog.

```text
built-in commands -----+
standalone skills -----+
configured MCP --------+--> contribution compiler
installed plugins -----+            |
                                    v
                     RuntimeContributionSnapshot
                         |         |         |
                         v         v         v
                    CommandActor  agent   MCP runtime
```

The catalog unifies discovery, identity, validation, activation, collision
handling, revision pinning, and change notification. It is not a generic
execution engine. Commands, skills, and MCP servers retain their distinct
execution and failure semantics.

### Contribution sources

Every contribution has a typed source:

- `builtin`: compiled Balda behavior;
- `user-skill`: a host-configured user skill root;
- `workspace-skill`: a skill root belonging to the active workspace;
- `configured-mcp`: an MCP server from trusted host configuration;
- `plugin`: an installed Agent Plugins package revision.

Source discovery produces descriptors and diagnostics. It does not construct
command handlers, inject skill bodies, or start MCP clients.

Plugins, built-ins, configured MCP servers, and host user skills are
application-scoped. Workspace skills are scoped to the workspace bound to a
session. Balda therefore maintains an application snapshot plus immutable
workspace overlays. It derives an effective snapshot for an execution scope:

```text
application snapshot + workspace overlay -> effective snapshot
```

The effective snapshot has its own content-derived ID. One workspace can never
change the commands, skills, or tools visible to a session bound to another
workspace. Sources with future principal-specific credentials or policy must
use another explicit overlay rather than adding user checks inside the global
catalog.

Conceptually:

```go
type SourceID struct {
    Kind SourceKind
    Name string
}

type ContributionID struct {
    Source SourceID
    Kind   ContributionKind
    Name   string
}
```

Core code carries these fields as structured values. Forms such as
`<plugin>/<skill>` are presentation syntax and must not become delimiter-based
storage contracts.

Every mutable source also has a content-derived `RevisionID`. Plugin revisions
cover the validated package tree, standalone-skill revisions cover their
validated skill roots, and configured-MCP revisions cover normalized non-secret
configuration. Built-in revisions come from the Balda build contract version.

### Plugin identity, origin, and revision

Plugin identity has separate logical, provenance, and content dimensions:

- `PluginID` is the normalized `plugin.json.name`;
- `Origin` records marketplace identity, source, and package path;
- `RevisionID` is a SHA-256 digest produced by a versioned deterministic hash
  of the validated package tree;
- manifest `version` is author-provided display metadata and is not identity.

Balda installs at most one plugin with a given `PluginID`. Multiple
marketplaces may publish that name, but installation requires an unambiguous
selector such as `name@marketplace`. The first installation persists its
origin. Upgrade uses that origin; changing origin requires an explicit owner
action. A manifest name change is removal of one plugin and installation of
another.

A package revision is immutable after validation. The active install record
points to one revision and keeps origin, manifest version, enabled state, and
capability summary. Package data is stored separately from writable
`${PLUGIN_DATA}` state.

### Snapshot identity

Every application and effective snapshot has a content-derived identity that
survives process restart. A local sequence number may be used only for
efficient in-process notifications.

```go
type Snapshot struct {
    ID          SnapshotID
    Sequence    uint64
    Scope       SnapshotScope
    Parents     []SnapshotID
    Sources     map[SourceID]SourceDescriptor
    Commands    map[ContributionID]CommandDescriptor
    Skills      map[ContributionID]SkillMetadata
    MCPServers  map[ContributionID]MCPServerDescriptor
    Diagnostics []Diagnostic
}
```

`SnapshotID` is derived from the sorted source and revision identities, scope,
parent snapshot IDs, and the version of the compilation rules. `Sequence` is
never persisted in actor or turn payloads.

Snapshots contain descriptors, resource references, and diagnostics. They do
not contain live handlers, MCP clients, complete skill bodies, credentials, or
absolute filesystem paths in durable forms.

### Installed, enabled, and healthy are distinct

Plugin lifecycle uses three separate states:

```text
installed -> enabled -> observed health
```

- installed means a validated immutable revision and install record exist;
- enabled means its contributions are eligible for the application snapshot;
- health describes process-local MCP and projection readiness.

An explicit owner install validates, enables, and activates the first revision
in one operation. Upgrade validates and activates a new revision while
preserving the enabled state. Installation with disabled state is available as
an explicit option. A package change discovered outside a managed operation is
reported as drift and is not activated automatically. This prevents a file or
marketplace refresh from silently starting a new executable.

Disable removes a plugin's contributions from the next snapshot without
deleting its package or `${PLUGIN_DATA}`. Remove disables first, retires the
revision, and handles package data according to the explicit remove or purge
operation.

Health is observed state and does not change snapshot identity. A temporary
MCP connection failure cannot make commands or skills appear and disappear
from the desired catalog.

### Plugin command extension

Balda-specific plugin commands are declared under the stable, opaque
`dev.baldaworks.balda` extension namespace. It corresponds to the
`baldaworks.dev` organizational domain, but loading never requires DNS or HTTP
access.

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
          "skill": "deploy"
        }
      ]
    }
  }
}
```

`schema_version` is required and versions the Balda extension independently of
the portable Agent Plugins schema. Balda validates supported versions with a
bundled schema and never fetches a schema at runtime. An unsupported extension
version disables only Balda-specific contributions.

The initial command declaration contains only `name`, `description`, and a
plugin-local `skill` reference. Access, context, approval, retry, and delivery
policy remain host-owned. Plugin data cannot grant permissions. Plugin
commands are available only to authenticated Balda users.

Direct command-to-MCP-tool execution is outside this contract. It requires a
separate typed argument, authorization, approval, idempotency, and result
contract. A skill-backed command already composes command, instruction, and
tool behavior through existing runtime boundaries.

### Durable command routing

`CommandActor` remains the only product command executor. Built-in handlers
continue to be assembled by `commandfx`; plugin command descriptors are
adapted through one generic plugin-command handler.

Transport parsing normalizes provider syntax to a canonical command name.
Before durable publication, command ingress obtains the effective catalog
snapshot ID for the command's session and stores it with that name. Neither
layer resolves the command to a handler or plugin contribution:

```go
type RoutedCommandName struct {
    Name     string
    Snapshot SnapshotID
}
```

`CommandActor` loads the retained snapshot, resolves the exact canonical name,
and selects either a built-in handler or a plugin command descriptor. That
descriptor contains the plugin and revision identity. A retry uses the same
snapshot and cannot rebind an old name to another plugin or revision. If the
snapshot or revision is no longer retained, the actor returns a stable
unavailable-revision result.

Built-in names are reserved. Duplicate or transport-incompatible plugin
aliases are omitted from that transport's advertisement projection and
reported as diagnostics. Canonical contribution identity remains valid even
when a provider cannot expose a direct alias.

The plugin-command handler publishes a normal conversational turn with an
explicit skill reference and the command arguments as user input:

```go
type SkillRef struct {
    Source   SourceID
    Revision RevisionID
    Name     string
}
```

The child turn derives correlation, causation, and deduplication identity from
the command envelope. Re-delivery of one command cannot create two turns.

```text
provider command
  -> transport parse/auth and canonical name normalization
  -> durable command payload
  -> CommandActor
  -> plugin command adapter
  -> deduplicated normal turn with SkillRef
  -> lazy SKILL.md read
  -> model may use healthy MCP tools from the pinned revision
```

### Management commands are separate

`/plugin install`, `upgrade`, `enable`, `disable`, `remove`, and `status` are
built-in owner management commands. They are not plugin-contributed commands
and do not share the plugin-command adapter.

The management handler depends on a small local management port and neutral
DTOs. It must not expose `pluginapp` service types through the actor boundary.
`pluginapp` owns managed package state and implements the port through
composition-root wiring.

### Skill discovery and selection

All skill sources project the same bounded metadata contract:

- stable contribution and source IDs;
- skill name and description from valid Agent Skills frontmatter;
- immutable revision identity;
- availability diagnostics when useful to the model or operator.

The catalog does not include complete `SKILL.md` bodies. A skill reader loads
the selected body and supporting resources only when the model or a command
selects that exact skill reference. Loaded instructions enter the current turn
as user-level context and cannot alter host permissions or system policy.

Selection has an explicit protocol:

- the turn assembler recognizes an explicit qualified `$skill` reference and
  loads it before provider execution;
- a plugin command supplies its revision-pinned `SkillRef` directly to the turn
  assembler;
- for implicit selection, the metadata prompt tells the model to call a
  read-only skill-loader operation with the qualified contribution ID;
- provider adapters may implement that operation through a native skill
  mechanism or a bundled MCP adapter over the same skill-reader port.

The loader obtains snapshot and execution scope from trusted turn context, not
from model arguments. It returns the main `SKILL.md` body and allows bounded,
contained reads of referenced skill resources. The model never receives or
submits an absolute host path. This read operation is a catalog projection, not
a plugin lifecycle or execution control plane.

Explicit qualified references resolve exactly. An unqualified skill name is
accepted only when it is unique among enabled sources; ambiguity produces a
clear error and requires a qualified reference. Automatic selection receives
qualified metadata for all eligible skills and may choose based on description.
There is no hidden name-shadowing precedence between workspace, user, plugin,
and built-in sources.

Metadata and loaded content have separate host-defined budgets. Discovery is
limited to the source's defined immediate skill directories. Resolved files,
symlinks, and supporting resources must remain inside their source root. Size,
file-count, and total prompt budgets are enforced before content reaches the
model.

Each turn pins its skill revision. A refresh affects new turns and cannot
change instructions midway through an active or retried turn.

Workspace overlays may refresh from committed or working-tree skill changes
between turns because they contain instructional resources, not plugin MCP
process declarations. This does not weaken the rule that unmanaged changes to
an installed plugin revision are never activated automatically.

Metadata budgeting is deterministic. The effective catalog sorts by stable
source and contribution identity, applies a host-defined total budget, and
reports omitted metadata as a bounded diagnostic. It never partially emits one
skill descriptor.

### MCP desired and observed state

Configured and plugin MCP servers project into the same typed namespace. Server
and tool identities include their `SourceID`; plugin servers additionally pin
the plugin revision.

Plugin-root `mcp.json` is validated according to its declared Agent Plugins
version. Balda resolves `${PLUGIN_ROOT}` and `${PLUGIN_DATA}`, enforces
filesystem containment, treats `command` as one executable token rather than a
shell string, and enables only supported transports.

Snapshot publication describes desired MCP state. The MCP runtime reconciler
owns process start, connection, handshake, tool discovery, shutdown, and
observed health:

```text
disabled | starting | ready | degraded | failed | stopping
```

Connection or authentication failure updates health and diagnostics but does
not invalidate unrelated contributions. A turn sees only tools ready for its
pinned source revision. MCP timeouts, process limits, environment filtering,
logging redaction, and shutdown deadlines are host policy.

MCP runtime instances are keyed by source and revision. During upgrade, an old
instance remains available to turns pinned to the old revision while the new
instance starts. If two revisions cannot run concurrently, activation requires
the old revision to drain; Balda must not silently route an old turn to a new
server. HTTP endpoints may share a remote origin, but their local identities
remain revision-specific.

Provider runtimes differ in whether they support dynamic MCP registration. The
projection port must report one of these outcomes:

- applied to the active runtime;
- available only to newly created runtimes;
- requires a managed runtime rebuild;
- unsupported.

Balda must not claim a plugin is ready until the provider-specific outcome and
MCP health are known. Runtime rebuilds follow the existing session and startup
lifecycle; a contribution refresh cannot restart the process implicitly.

### Configuration, credentials, and plugin data

The package manifest declares portable components and Balda command metadata.
It does not store host credentials or grant runtime policy.

The active install record stores origin, revision, enablement, and capability
summary. Host configuration owns resource limits, permitted transports,
credential bindings, environment allowlists, and provider-specific projection
policy. Secret values are resolved only at the MCP launch or connection
boundary and never enter the snapshot, prompt, diagnostics, or actor payload.

`${PLUGIN_DATA}` resolves to stable writable state scoped by `PluginID`, not by
revision. Upgrade retains it. Disable leaves it untouched. Remove and purge
have distinct data-retention behavior.

Plugin MCP servers are application-scoped unless a future server contract
explicitly requires per-principal isolation. Per-user credentials must not be
placed in a shared process environment.

### Trust and supply chain

A revision digest proves which bytes Balda validated and activated; it does not
authenticate the publisher. Adding a marketplace or installing directly from
a source is the owner's trust decision. Managed Git installs record the source
and resolved commit used to produce the revision.

Marketplace refresh only updates discovery metadata. It cannot change an
active revision. Install and upgrade return and audit the source, old and new
revisions, and a bounded capability diff. Any future unattended update mode
requires a separate policy decision.

Plugin stdio MCP servers execute native processes with the Balda service
account. Validation enforces containment and launch shape but is not a sandbox.
Host policy controls allowed transports, executables, environment keys,
network access where enforceable, process count, memory, and time limits.

### Compilation, activation, and rollback

Managed install and upgrade use this sequence:

1. fetch or copy into a staging directory;
2. enforce package size, file-count, path, symlink, and schema limits;
3. validate portable components and implemented Balda extensions;
4. calculate and persist the immutable revision and capability summary without
   changing the active install record;
5. compare origin and capability changes with the authorized operation;
6. compile and validate a complete candidate runtime snapshot using the
   proposed install record;
7. persist a recoverable activation intent;
8. switch the active install record and in-process snapshot under one catalog
   write lock;
9. mark the activation intent complete;
10. notify command, skill, MCP, and transport projections;
11. record projection health separately.

If validation or snapshot compilation fails, the active revision and snapshot
remain unchanged. The activation intent makes a crash between durable record
update and in-process publication recoverable: startup always recompiles from
the durable active records before opening dependent ingress. If a process-local
projection fails, desired state remains visible with failed health and can be
retried or rolled back explicitly.

Old immutable revisions remain readable while durable commands or turns refer
to them. The first implementation performs no automatic revision garbage
collection. An explicit purge requires a drain and reference preflight; it
fails closed when safety cannot be established. If a retained revision is
unavailable, execution returns a stable unavailable-revision outcome rather
than binding to newer content.

At startup, Balda reconstructs the snapshot from active install records,
standalone roots, built-ins, and host MCP configuration before accepting
ingress that depends on the catalog.

### Validation and failure isolation

Validation follows Agent Plugins component isolation. A fatal `plugin.json`
error rejects the plugin revision. An invalid skill is omitted without
disabling valid MCP servers. An invalid `mcp.json` disables MCP for that plugin
without disabling valid skills. An invalid Balda extension disables its Balda
commands without changing portable component validity.

Source-local errors become diagnostics and do not invalidate unrelated
sources. The candidate snapshot is rejected only when its own global invariants
cannot be represented deterministically, such as duplicate source identity or
an invalid built-in registration.

### Wire compatibility

Snapshot-pinned command names and revision-pinned skill references require new
versioned durable payload schemas. Plugin commands are never encoded in the
legacy unpinned command payload. Built-in command compatibility may be read
during a bounded migration window, but every newly published payload uses the
new schema.

Deployment follows the existing mixed-version rule: old ingress is stopped and
pending incompatible commands are drained before consumers that require the
new routing contract start. Schema changes update the canonical contract
specification and generated types in the same implementation change.

### Observability and operator surface

Plugin status exposes bounded, non-secret data:

- logical ID, manifest version, origin, and revision;
- installed and enabled state;
- validation diagnostics;
- contributed command aliases and advertisement state per transport;
- skill metadata and ambiguity diagnostics;
- desired MCP servers and observed health;
- whether a provider runtime rebuild is required.

A runtime-catalog inspection surface additionally reports application and
workspace snapshot IDs, enabled non-plugin sources, metadata-budget omissions,
and projection lag. It remains an operator surface rather than a model tool.

Install, source change, activation, disable, rollback, and removal emit audit
events. Metrics cover compile failures, snapshot activation, MCP start and
handshake failures, projection lag, and retained revisions. Logs never include
skill bodies, MCP headers, environment values, credentials, or raw tool data.

### Ownership boundaries

- the runtime contribution catalog owns source descriptors, snapshot
  compilation, scope overlays, collision policy, immutable publication, and
  diagnostics;
- `pluginapp` owns marketplace access, staged package installation, active
  install records, origins, revisions, enablement, and rollback;
- `actors/command` owns command validation and behavior;
- `commandcmd` owns neutral durable command names, snapshot references,
  payloads, and transport advertisement contracts;
- `commandfx` ingress pins the effective catalog snapshot through a small local
  port before durable publication;
- the agent layer owns skill selection and turn-context assembly through local
  snapshot and reader ports;
- the MCP runtime owns server processes, connections, tools, and health through
  a local projection port;
- composition packages provide adapters between these local ports;
- `channel/*` packages retain provider parsing, syntax, advertisement, and
  delivery behavior.

No command actor, agent builder, MCP consumer, or transport adapter imports
`pluginapp` for convenience. Shared IDs and descriptors belong to a dedicated
transport-neutral contract package. Process-local paths and clients stay behind
ports in their owning consumers.

## Alternatives considered

### Make the plugin service own the global snapshot

This omits built-in commands, standalone skills, and configured MCP servers.
The plugin service contributes one source to a host-wide catalog instead.

### Inject all skill bodies into the system instruction

This consumes context for unused skills and grants third-party text global
placement. Metadata discovery plus turn-scoped loading replaces it.

### Make a skills-only MCP API the catalog owner

This creates another control plane without solving command routing, activation,
or MCP lifecycle. Skills remain resources owned by the shared catalog. A
read-only MCP adapter may expose the catalog's skill-reader port when a provider
has no native skill-loading mechanism.

### Use an opaque random installation ID

A random ID complicates references without solving provenance. Balda uses the
manifest name as logical identity, persists origin separately, and uses a
content digest for immutable revision identity.

### Use manifest version as revision identity

Versions may be empty, reused, or incorrect. A content digest provides stable
retry and rollback semantics.

### Resolve commands against the latest snapshot during every retry

This can bind an old durable command to a different plugin after refresh.
Ingress publishes the canonical name with a retained snapshot ID instead, and
`CommandActor` remains the owner of exact-name resolution.

### Let plugins register native Go handlers

Dynamic host code would bypass portability and complicate trust, upgrades, and
isolation. Plugin commands remain declarative and skill-backed.

### Define one generic `Capability.Execute` interface

Commands, skills, and MCP servers have different semantics. Balda unifies their
catalog lifecycle while retaining typed execution paths.

### Activate filesystem changes automatically

This could start changed executables without an owner operation. Unmanaged
changes produce drift until explicitly activated.

## Consequences

### Positive

- Every runtime contribution participates in one identity, collision, and
  lifecycle model.
- Plugins can provide coherent command, instruction, and tool workflows.
- `CommandActor` remains the authoritative durable command boundary.
- Unused skills no longer consume the system prompt.
- Origin locks and content revisions make upgrades, retries, and rollback
  deterministic.
- Workspace overlays prevent capability leakage between sessions.
- Desired state remains stable while MCP health changes independently.
- Portable Agent Plugins components remain portable; commands use an explicit
  Balda client extension.

### Negative

- Durable command and turn schemas need snapshot and revision references.
- Immutable revisions and safe garbage collection require additional state.
- Safe upgrades may temporarily run two MCP revisions or wait for a drain.
- Command advertisements and skill metadata become changing projections.
- MCP runtime needs reconciliation, health, and provider capability reporting.
- Install, enable, remove, and purge become distinct operations.
- Provider command syntax still requires transport-specific alias policy.

## Migration

The feature is one runtime contract even when delivered through internal
steps:

1. add source, contribution, plugin origin, revision, snapshot, and diagnostic
   contracts;
2. persist active plugin install records and immutable revisions alongside the
   current package directories;
3. compile built-ins, standalone skills, configured MCP, and plugin packages
   into application snapshots and workspace overlays;
4. replace full-body system injection with bounded skill metadata and a
   revision-aware reader;
5. add snapshot-pinned command names, plugin command adaptation, and dynamic
   advertisement projections;
6. add plugin MCP reconciliation and provider capability reporting;
7. connect managed install, upgrade, enable, disable, rollback, remove, and
   purge operations to atomic snapshot activation;
8. migrate existing installed packages to origin-unknown active records and
   require explicit origin selection before their first managed upgrade;
9. remove the startup-only `agentplugin.Catalog` path after every consumer uses
   the shared catalog.

No intermediate release may expose plugin commands whose referenced skill and
MCP projections can resolve from a different revision.

## Non-goals

- standardizing commands for the Agent Plugins ecosystem;
- executing arbitrary Go code from plugins;
- direct command-to-MCP invocation in the first contract;
- automatic plugin dependency resolution;
- storing secrets in portable packages;
- silently activating untrusted package drift or restarting Balda.

## Related decisions

- [Plugin marketplace repo format](plugin-marketplace-format.md)
- [Command runtime adapter](command-runtime-adapter.md)
- [Application sub-zones](application-zones.md)
- [Runtime contract](runtime-contract.md)
