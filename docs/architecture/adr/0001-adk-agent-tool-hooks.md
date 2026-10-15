# ADR 0001: Provider-neutral ADK agent tool hooks

- Status: Proposed
- Date: 2026-10-15
- Owners: Balda maintainers
- Decision scope: agent runner construction, managed hooks, Agent Plugins contributions
- Supersedes: none

This ADR is a proposal. It is not an active architecture contract and does not
authorize implementation. Move it to `Accepted` only after the maintainers
approve the decision and resolve the open questions below.

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

## Proposed decision

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
    cfg.PluginConfig.Plugins = append(
        slices.Clone(cfg.PluginConfig.Plugins),
        hostPlugins...,
    )
    return runner.New(cfg)
}
```

The factory preserves caller plugins and `PluginConfig.CloseTimeout` and does
not mutate caller-owned slices. A fresh host plugin set per runner avoids
sharing plugin lifecycle state unless the ADK contract is explicitly verified
to permit sharing.

The following Balda-owned user-facing construction sites use it:

- the main chat/session runner in `agent.Builder`;
- child runners created by `agent.RuntimeManager`;
- GoalKeeper worker and validator runners;
- future user-facing runners around an `agent.Agent`.

Dedicated internal runtimes, including session-memory derivation, remain on a
plain runner unless explicitly opted in. Tests may construct a plain runner
when the plugin boundary is irrelevant.

No change is proposed to `agentfactory.Factory`, its `Build` result, or the
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

The portable package format remains Agent Plugins 1.0. A proposed Balda
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
contribution. Version 1 remains valid and unchanged. The proposed canonical
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

## Proposed ownership

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

Rejected from this proposal. The current factory returns an agent and does not
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

## Risks and open questions

The following items must be resolved before this ADR can be accepted:

1. Verify the lifetime and close behavior of `plugin.Plugin` in the pinned ADK
   release and decide whether every runner requires a fresh plugin instance.
2. Verify callback propagation to agent-tool sub-runners. Google ADK tracks a
   related issue in
   [google/adk-go#669](https://github.com/google/adk-go/issues/669).
3. Confirm which callback context fields provide stable invocation and tool-call
   identifiers for deduplication and audit.
4. Define the exact extension v2 JSON Schema bounds and compatibility tests.
5. Define durable package-hook activation storage, upgrade behavior, and
   transaction boundaries with package removal.
6. Decide whether lexical identity ordering is sufficient or a later schema
   needs explicit priority.
7. Confirm how `additional_context` and post-tool denial map into ADK result
   values without weakening the current untrusted-context boundary.
8. Finalize the consumer-owned native adapter contract, including leaf-config
   handoff after provider-tree traversal, ambiguous matches, and release
   ownership.

## Validation required for acceptance

- `agentfactory.Factory` still returns only `agent.Agent`.
- `RunnerFactory` preserves caller configuration, creates host plugins with the
  agreed lifetime, and does not mutate caller-owned slices.
- Main, child, and GoalKeeper runners receive the plugin; dedicated internal
  runners do not unless explicitly opted in.
- An ADK-managed function tool can be allowed, denied before execution, have its
  arguments rewritten in place, and have its result replaced.
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

## Rollout after acceptance

1. Add `agent.RunnerFactory` and the Balda ADK plugin constructor.
2. Move all Balda-owned user-facing runner construction through the factory.
3. Add callback translation and ADK integration tests.
4. Extract provider recognition, launch mutation, proxy commands, binding-kind
   dispatch, and wire codecs from `hookprovider` into registered native adapters.
   Keep `hookprovider` as the neutral binding and normalized hook dispatch
   service used by those adapters.
5. Add source-aware deduplication if validation demonstrates overlapping events.
6. Add extension schema v2 and immutable catalog hook descriptors.
7. Add durable exact-revision activation and Backoffice review.
8. Enable package hook execution only after audit, rollback, and kill-switch
   validation passes.

## References

- [Google ADK runner package](https://pkg.go.dev/google.golang.org/adk/v2/runner)
- [Google ADK plugin package](https://pkg.go.dev/google.golang.org/adk/v2/plugin)
- [Google ADK callbacks](https://google.github.io/adk-docs/callbacks/)
- [Agent Plugins specification](https://agent-plugins.org/specification)
