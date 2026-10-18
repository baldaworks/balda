# ADR 0001: Provider-neutral ADK agent tool hooks

- Status: Accepted
- Date: 2026-10-15
- Owners: Balda maintainers
- Decision scope: agent runner construction, managed hooks, Agent Plugins contributions
- Supersedes: none
- Approval: Prism Story `balda-qubf`, Human gate approved 2026-10-15

This ADR records the architecture approved through the Human gate of Prism
Story `balda-qubf`. Implementation remains governed by the Story tasks and
their independent review and verification gates.

## Context

Balda has managed lifecycle and native tool hooks. The current native tool path
wraps selected ACP provider processes so `PreToolUse` can synchronously allow,
deny, or rewrite a call before the external process executes it.

Balda also uses the stock `google.golang.org/adk/v2/runner`. Google ADK runner
plugins expose `BeforeToolCallback`, `AfterToolCallback`, and
`OnToolErrorCallback`, providing a provider-neutral interception point for tools
executed through the ADK tool path.

The current construction boundaries are:

```text
agentfactory.Factory.Build()
        |
        v
    agent.Agent
        |
        v
Balda creates runner.Runner
```

`agentfactory.Factory` returns only `agent.Agent`. It has no tool-hook metadata
or capability contract. Provider-native launch preparation is a separate path
owned by `catalogapp.ProviderFactory` and registered native adapters.

Agent Plugins 1.0 packages can already contribute Balda commands through
`extensions["dev.baldaworks.balda"]`. The current Balda extension schema is
closed and commands-only.

## Decision drivers

- Keep `agentfactory.Factory` and `agent.Agent` free of Balda-specific hook
  metadata.
- Use the stock ADK plugin API instead of a runner fork.
- Apply one hook protocol and execution policy to ADK and native events.
- Preserve synchronous native interception for tools executed inside ACP
  processes.
- Ensure every Balda-owned user-facing runner receives the same host plugins.
- Prevent plugin installation or upgrade from silently authorizing executable
  code.
- Keep the existing ownership boundaries for catalog, plugin lifecycle, state,
  hooks, and Backoffice.

## Decision

### 1. Centralize Balda-owned runner construction

Introduce `agent.RunnerFactory`, a small constructor around the stock ADK
runner. It adds host plugins to `runner.PluginConfig` and calls `runner.New`.

Conceptually:

```go
type RunnerFactory struct {
    newPlugins func() ([]*plugin.Plugin, error)
}

func (f *RunnerFactory) New(cfg runner.Config) (*runner.Runner, error) {
    hostPlugins, err := f.newPlugins()
    if err != nil {
        return nil, err
    }
    plugins := slices.Clone(hostPlugins)
    cfg.PluginConfig.Plugins = append(plugins, cfg.PluginConfig.Plugins...)
    return runner.New(cfg)
}
```

The factory preserves caller plugins and `PluginConfig.CloseTimeout` and does
not mutate caller-owned slices. A fresh host plugin set per runner avoids
sharing plugin lifecycle state unless the ADK contract is explicitly verified
to permit sharing. Balda host plugins precede caller plugins because pinned ADK
stops a callback chain at the first non-nil result. Balda therefore evaluates
model/provider arguments before trusted in-process extension callbacks and a
deny or failure stops the chain. Allow, rewrite, and unchanged paths return nil
so trusted caller or agent callbacks continue and may deliberately transform
the accepted arguments afterward. Stock ADK has no second host callback after
those callbacks, so Balda does not claim to inspect the final effective
arguments in that configuration.

The following Balda-owned user-facing construction sites use it:

- the main chat/session runner in `agent.Builder`;
- child runners created by `agent.RuntimeManager`;
- GoalKeeper worker and validator runners;
- future user-facing runners around an `agent.Agent`.

Dedicated internal runtimes, including session-memory derivation, remain on a
plain runner unless explicitly opted in. Tests may construct a plain runner
when the plugin boundary is irrelevant.

No change is made to `agentfactory.Factory`, its `Build` result, or the
`agent.Agent` interface.

### 2. Add one Balda ADK runner plugin

Each user-facing runner receives a host-owned plugin that translates ADK tool
callbacks into the existing `hookcmd.Event` protocol and invokes
`hooks.Runner`.

Supplying the plugin is valid for any `agent.Agent`. Callback delivery depends
on that agent using the ADK tool execution path. A custom agent that implements
`Run` without ADK-managed tools simply emits no ADK tool callbacks. The agent
factory does not need to predict or describe this behavior.

The plugin is present even when no hook is enabled, allowing managed ADK hook
changes to affect the next tool event without rebuilding the runner.

For `BeforeToolCallback`:

- `allow` returns `(nil, nil)`;
- `updated_input` replaces the callback `args` map in place and returns
  `(nil, nil)`;
- `deny` returns a bounded structured result, causing ADK to skip the tool;
- timeout, malformed output, or executable failure fails closed.

The callback evaluates the arguments supplied by the model/provider. Later
in-process caller and agent callbacks are trusted extension code. They observe
the accepted or rewritten map and may intentionally modify it before tool
execution; this boundary does not treat those callbacks as untrusted input or
claim that Balda revalidates their final transformation.

For `AfterToolCallback`, the hook may retain or replace the tool result. A
post-execution decision cannot undo a completed side effect and is never
reported as prevention. `OnToolErrorCallback` may normalize an error that
remains after the post-tool chain.

### 3. Retain native ACP interception as a separate fallback

Tools executed inside external ACP processes remain outside the ADK tool path:

```text
catalogapp.ProviderFactory (provider-tree traversal)
        |
        v
consumer-owned NativeHookPreparer port
        |
        v
hooknative.Registry (adapter iteration only)
        |
        v
provider-owned NativeAdapter (one leaf config)
        |
        v
hookprovider.Service.Bind()
        |
        v
hooks.Runner
```

`hookprovider` is provider-neutral. It owns authenticated session bindings over
normalized `hookcmd.Event`/`hookcmd.Result` transport, execution through
`hooks.Runner`, bounds, opaque binding tokens, and binding cleanup. It does not
import ACP provider IDs or families, recognize provider types, walk provider
pools, rewrite provider launch commands, select proxy commands, dispatch on a
binding kind, or encode and decode provider-specific wire formats.

Concrete native adapters own their protocol and launch integration. Each
adapter decides whether it supports one leaf provider configuration and, when
selected, obtains a neutral binding from `hookprovider.Service` and injects that
binding into its provider-specific launch configuration. The adapter also owns
its proxy command, wire codec, normalization into `hookcmd.Event`, conversion
from `hookcmd.Result`, and any provider-specific binding metadata. Examples may
support Codex, Claude, or OpenCode, but those names occur only in their adapter
packages and composition registration.

The registry accepts adapters through a neutral interface. Conceptually:

```go
type NativeAdapter interface {
    Supports(agentconfig.Config) bool
    Prepare(context.Context, LaunchRequest, BindingFactory) (PreparedLaunch, error)
}
```

`catalogapp.ProviderFactory`, as the consuming launch layer, owns the port and
provider-tree traversal. It passes each leaf configuration to the registry. The
registry only iterates registered adapters; it contains no provider type or
family switch. A concrete adapter determines support and may mutate only the
selected leaf launch configuration.

Registration order must not silently choose between multiple matches: zero
matches is unsupported when native interception is required, and multiple
matches is a configuration error. Provider ID remains opaque event data and an
optional hook filter; it is never an adapter dispatch key. The provider map and
root ID never enter `hookprovider`. Adding a provider requires registering
another adapter, not changing the hook execution service.

Both paths call the same `hooks.Runner`. Events identify their source as `adk`
or `native`. Normally only one path observes a tool because execution ownership
differs. If an integration exposes the same call on both paths, bounded
per-turn deduplication uses session, invocation, tool-call identifier, and event.
A missing tool-call identifier must not collapse unrelated calls.

Existing native sessions may require `/reset` when started without the proxy.
The always-present ADK runner plugin does not require reset for hook definition
changes.

### 4. Add hook contributions to the Balda Agent Plugins extension

The portable package format remains Agent Plugins 1.0. Balda
extension schema version 2 adds `hooks` while retaining `commands`:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "policy-pack",
  "extensions": {
    "dev.baldaworks.balda": {
      "schema_version": 2,
      "hooks": [
        {
          "name": "reject-dangerous-shell",
          "description": "Reject destructive shell invocations",
          "event": "PreToolUse",
          "executable": "hooks/reject-dangerous-shell",
          "args": [],
          "matcher": "^(exec_command|Bash|bash)$",
          "timeout_seconds": 30
        }
      ]
    }
  }
}
```

Version 2 accepts `commands`, `hooks`, or both and requires at least one
contribution. Version 1 remains valid and unchanged. The canonical
schema ID is
`https://baldaworks.dev/schemas/balda-extension/2.0.0/schema.json`; Balda embeds
and validates it offline.

Hook executables are contained relative paths in an immutable package revision.
Validation rejects absolute paths, traversal, symlink escape, missing files,
unknown fields, duplicate names, and invalid events. Execution does not use
implicit shell interpolation. Existing input, output, timeout, process-tree,
and context bounds continue to apply.

### 5. Separate package discovery from execution authority

`runtimecatalogcmd` gains an immutable hook contribution descriptor. Catalog
compilation discovers and validates declarations but never executes them.

Installation, enablement, or upgrade of an Agent Plugins package does not
activate executable hooks. An administrator separately approves one exact
plugin ID, package revision, and hook name in Backoffice.

An activation may enable or disable the hook and narrow its provider scope,
matcher, or timeout. It cannot replace the executable, arguments, event, or
revision, or widen manifest constraints.

Changed hooks in a new revision require new approval. Disable is an immediate
kill switch. Package removal deactivates associated hooks atomically while
retaining revision bytes required for audit and in-flight cleanup.

Managed Backoffice hooks and approved package hooks form one ordered execution
view. Their stable identities are:

```text
managed/<hook-name>
plugin/<plugin-id>/<hook-name>
```

The initial order is lexical by effective identity within an event.

## Ownership

- `agentfactory.Factory` creates and returns `agent.Agent`; unchanged.
- `agent.RunnerFactory` creates stock ADK runners with host plugins.
- `agent.Builder`, `agent.RuntimeManager`, and GoalKeeper use the runner factory
  for user-facing runners.
- `hookcmd` owns provider-neutral hook events and results.
- `hooks` owns the combined managed/package execution view, ordering, bounds,
  and failure policy.
- `hookprovider` owns provider-neutral native bindings, normalized hook
  dispatch, bounds, opaque tokens, and cleanup. It has no provider config,
  proxy codec, ACP ID, or family knowledge.
- `hooknative` owns adapter registration and selection; concrete adapter
  packages own provider protocol detection, launch mutation, proxy commands,
  wire codecs, and normalization.
- `catalogapp.ProviderFactory` owns provider-tree traversal and supplies leaf
  configurations through its consumer-owned native preparation port.
- `runtimecatalog` owns extension discovery and immutable hook descriptors; it
  never executes hooks.
- `pluginapp` owns package review, retained revisions, and activation candidates.
- `state` owns guarded activation persistence and audit records.
- Backoffice uses application ports and does not import catalog or provider
  implementations.

## Consequences

### Positive

- The agent factory remains simple and compatible with Norma.
- ADK-managed tools receive hooks independently of provider identity.
- Existing native interception remains available for external ACP tools.
- One runner-construction boundary reduces missed plugin registration.
- Package hook code cannot run solely because a package was installed or
  upgraded.

### Negative

- Every Balda-owned user-facing runner must migrate to `RunnerFactory`.
- Two interception implementations remain: ADK callbacks and native proxies.
- Native interception gains a registry plus provider-owned adapter packages.
- Native sessions can retain a reset boundary when proxy instrumentation changes.
- Package hooks add durable activation, review, and revision-retention state.
- Third-party agents that create private runners outside Balda's constructor do
  not automatically inherit the plugin.

## Alternatives considered

### Add capabilities to `agentfactory.Factory` or `agent.Agent`

Rejected. The current factory returns an agent and does not
own runner behavior. Adding Balda tool-hook metadata would widen a stable generic
boundary without being necessary for plugin attachment.

### Keep tool hooks provider-native only

Rejected. It duplicates adapters and excludes tools already managed by ADK,
which exposes the required synchronous callback points.

### Infer support from provider names in generic hook code

Rejected. Provider identity does not define where a tool executes. Native type
checks belong only to the native adapter.

### Observe `session.Event` function calls instead of intercepting

Rejected for enforcement. Notifications do not guarantee that execution waits
for Balda, so they cannot safely implement deny or argument rewriting.

### Fork the ADK runner

Rejected. Stock `runner.PluginConfig` supplies the required extension point and
a fork would add avoidable lifecycle and upgrade cost.

### Automatically activate hooks contributed by an installed package

Rejected. Package hooks are executable code running with Balda's operating-system
authority and require explicit approval of an exact revision.

## Resolved design questions

The Human-approved Story design resolves the former acceptance questions as
follows:

1. **Plugin lifetime.** `agent.RunnerFactory` creates a fresh, stateless host
   plugin for each runner. The pinned ADK runner has no public close method, so
   mandatory cleanup cannot rely on `plugin.Plugin.CloseFunc`; invocation and
   session cleanup belong to explicit callbacks and the owning runtime. Owner:
   `agent` and `hookadk`. Consequence: plugin instances are not shared, and
   lifecycle tests must pin this behavior.
2. **Nested runners.** Balda-owned main, child, GoalKeeper worker, and GoalKeeper
   validator runners use `RunnerFactory`. Dedicated internal runners opt in
   explicitly. Private runners created inside ADK `agenttool` do not inherit
   host plugins; the outer agent-tool invocation remains visible, while inner
   calls are outside this decision. Owner: each runner construction site.
   Consequence: privately constructed third-party runners are not claimed as
   instrumented.
3. **Callback identity and deduplication.** ADK events use `SessionID`,
   `InvocationID`, and `FunctionCallID`. Eligible tool events deduplicate on
   `(session, invocation, tool-call, event)` in an in-memory cache owned by
   `hooks.Runner`; source is excluded. The cache is bounded to 256 entries per
   invocation and 4096 globally with a ten-minute safety TTL.
   `AfterRunCallback` releases an invocation and native binding release clears
   its session. Missing identity bypasses deduplication. Owner: `hookcmd`,
   `hooks`, and `hookadk`. Consequence: no cross-restart guarantee is provided
   for synchronous in-flight calls.
4. **Extension v2 validation.** Agent Plugins 1.0 remains the outer format. A
   separately embedded closed v2 schema accepts commands, hooks, or both and
   requires a non-empty contribution set. It retains v1 unchanged and rejects
   unsafe paths, symlinks, missing or non-executable files, duplicate names,
   unknown fields, invalid events or regular expressions, and out-of-range
   bounds. Owner: `runtimecatalog`. Consequence: exact numeric limits and the
   fixture corpus are explicitly deferred to delivery task `balda-qubf.9` as
   implementation constants covered by compatibility tests; they cannot widen
   the containment or direct-execution contracts accepted here.
5. **Activation persistence and package transactions.** Approvals are keyed by
   plugin ID, immutable revision, and hook name, fenced by a descriptor digest.
   Eligibility is read by joining the activation with the enabled installation
   and its active exact revision. Existing atomic plugin commits therefore make
   disable, removal, upgrade, and rollback visible to the next snapshot without
   copying authority. Revision purge is forbidden while approvals or in-flight
   leases reference it. Owner: `pluginapp` and `state`. Consequence: migrations
   must refuse destructive downgrade while v2 descriptors or activations exist.
6. **Ordering.** Lexical order by full effective identity is the accepted
   initial contract. Owner: `hooks`. Consequence: explicit priority requires a
   future ADR and schema revision rather than an implicit ordering change.
7. **Post-tool results and context.** `hookcmd.Result` gains a bounded JSON
   replacement output for `PostToolUse`. ADK replacements must decode to an
   object. `additional_context` may appear only as named untrusted result data;
   it never enters system or developer instructions or trusted session state.
   Post-tool outcomes are recorded as observed or replaced and never as
   prevention. Owner: `hookcmd` and `hookadk`. Consequence: a native adapter
   that cannot apply an actionable result must reject that activation or result
   explicitly rather than ignore it.
8. **Native adapter boundary.** `catalogapp.ProviderFactory` owns provider-tree
   traversal and passes leaf configurations through its consumer-owned port.
   `hooknative.Registry` only iterates adapters and requires exactly one match
   for a native-intercepted leaf. Concrete adapters own detection, launch
   mutation, proxy commands, wire codecs, normalization, and partial-failure
   release. `hookprovider.Service.Bind` exposes only a neutral endpoint and
   opaque token. Owner: `catalogapp`, `hooknative`, provider adapters, and
   `hookprovider` respectively. Consequence: provider ID may remain opaque event
   data but cannot select an adapter, command, or codec.

Implementation still must verify migration numbering, audit-retention wiring,
and final Backoffice route and template touch points before editing those areas.
These checks belong to the owning delivery tasks and do not reopen this
architecture decision.

## Validation contract

- `agentfactory.Factory` still returns only `agent.Agent`.
- `RunnerFactory` preserves caller configuration, prepends fresh host plugins
  with the agreed lifetime, and does not mutate caller-owned slices. Nil host
  callback results continue through caller plugins.
- Main, child, and GoalKeeper runners receive the plugin; dedicated internal
  runners do not unless explicitly opted in.
- An ADK-managed function tool can be allowed, denied before execution, have its
  model/provider arguments rewritten in place, and have its result replaced.
  Trusted downstream callbacks observe accepted/rewritten arguments and may
  transform them further; Balda does not validate the final effective arguments
  after those callbacks.
- A custom agent without ADK-managed tools runs normally and produces no false
  tool-hook events.
- Concrete native adapter integration tests retain the existing Claude, Codex,
  and OpenCode behavior while proving that `hookprovider` contains no provider
  type constants, family switches, provider configuration imports, tree
  traversal, launch mutation, or provider wire codecs. Passing
  `hookcmd.Event.Provider` through as opaque event data remains valid.
- An architecture test prevents `hookprovider` from importing provider config or
  concrete native adapter packages; zero and multiple adapter matches fail with
  explicit errors in the native launch layer.
- A call visible through both integrations invokes each hook once.
- Extension v1 remains compatible; v2 validates commands-only, hooks-only, and
  combined packages and rejects unsafe paths and malformed declarations.
- Install and upgrade do not execute hooks. Exact-revision activation, disable,
  removal, reapproval, and rollback behave atomically.
- Timeout, malformed JSON, executable failure, oversized output, and process-tree
  cancellation produce bounded audit evidence and the documented failure mode.

## Implementation rollout

1. Add `agent.RunnerFactory` and the Balda ADK plugin constructor.
2. Move all Balda-owned user-facing runner construction through the factory.
3. Add callback translation and ADK integration tests.
4. Extract provider recognition, launch mutation, proxy commands, binding-kind
   dispatch, and wire codecs from `hookprovider` into registered native adapters.
   Keep `hookprovider` as the neutral binding and normalized hook dispatch
   service used by those adapters.
5. Add bounded cross-source deduplication keyed by session, invocation,
   tool-call, and event. Exclude source so ADK and native observations coalesce;
   bypass deduplication when identity is incomplete. Enforce the accepted limits
   of 256 entries per invocation and 4096 globally with a ten-minute safety TTL,
   and release entries through ADK invocation and native session cleanup.
6. Add extension schema v2 and immutable catalog hook descriptors.
7. Add durable exact-revision activation and Backoffice review.
8. Enable package hook execution only after audit, rollback, and kill-switch
   validation passes.

## Delivery completion

Prism Story `balda-qubf` remains open after implementation verification and
after its implementation pull request is opened. Verify records the canonical
pull request and exact reviewed head, then keeps the Story in
`phase:story:verify` with delivery state `awaiting_merge`. A changed head
requires review and verification again. Only confirmed merge of that verified
pull request into `main`, with its reported merge commit reachable from
`origin/main`, permits the lifecycle orchestrator to close the Story.

## References

- [Google ADK runner package](https://pkg.go.dev/google.golang.org/adk/v2/runner)
- [Google ADK plugin package](https://pkg.go.dev/google.golang.org/adk/v2/plugin)
- [Google ADK callbacks](https://google.github.io/adk-docs/callbacks/)
- [Agent Plugins specification](https://agent-plugins.org/specification)
