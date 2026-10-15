# Provider-neutral ADK agent tool hooks

Owner: Balda maintainers  
Status: proposed

## Context

Balda builds an `agent.Agent` through Norma's `agentfactory.Factory` and then
creates the stock `google.golang.org/adk/v2/runner` in the Balda agent layer.
The factory boundary returns only an agent. It does not describe tool-hook
support and must not acquire Balda-specific capability metadata.

Google ADK exposes runner plugins with `BeforeToolCallback`,
`AfterToolCallback`, and `OnToolErrorCallback`. These callbacks are the generic
hook integration point for agents whose tools use the ADK tool path. Tools
executed inside an external ACP process continue to use the provider-native
proxy introduced with native tool hooks.

This document also defines executable hook contributions in an
[Agent Plugins 1.0](https://agent-plugins.org/specification) package. "Agent
Plugins package" below means a package described by `plugin.json`; "ADK runner
plugin" means the Go callback object attached through `runner.PluginConfig`.

## Goals

- Attach one Balda ADK runner plugin to every user-facing runner without
  changing `agentfactory.Factory` or `agent.Agent`.
- Centralize user-facing runner construction so main, child, and GoalKeeper
  runtimes receive the same plugin configuration.
- Preserve native interception for tools executed inside external ACP
  providers.
- Allow Agent Plugins 1.0 packages to contribute hooks through the Balda client
  extension without executing newly installed code automatically.
- Keep discovery, activation, execution, persistence, and presentation in their
  existing architecture boundaries.

## Non-goals

- Forking or replacing the Google ADK runner.
- Adding a capability API to the agent factory or agent interface.
- Making a custom `agent.Agent` use ADK tool callbacks when its implementation
  does not participate in the ADK tool path.
- Replacing the provider-native proxy for external ACP tool execution.
- Providing an operating-system sandbox for hook executables.

## Runner ownership

Balda adds `agent.RunnerFactory`, a small host-owned constructor around the
stock ADK runner:

```go
type RunnerFactory struct {
    plugins []*plugin.Plugin
}

func (f *RunnerFactory) New(cfg runner.Config) (*runner.Runner, error) {
    cfg.PluginConfig.Plugins = append(
        cfg.PluginConfig.Plugins,
        f.plugins...,
    )
    return runner.New(cfg)
}
```

The exact implementation must copy plugin slices before appending so caller
storage is not mutated. It preserves caller-supplied plugins and
`PluginConfig.CloseTimeout`.

The existing flow becomes:

```text
agentfactory.Factory.Build()
        |
        v
    agent.Agent
        |
        v
agent.RunnerFactory.New()
        |
        v
stock ADK runner with Balda plugin
```

`agent.Builder` continues to own the main runtime construction. It receives the
runner factory and passes the `agent.Agent`, application name, and session
service into it. The agent factory contract remains unchanged.

All user-facing runner creation must go through the same constructor:

- the main chat/session runner in `agent.Builder`;
- child runners created by `agent.RuntimeManager`;
- GoalKeeper worker and validator runners;
- future user-facing runners around an `agent.Agent`.

Dedicated internal runtimes, including session-memory derivation, use a plain
runner unless they explicitly opt in. Tests may construct a plain runner when
the plugin boundary is irrelevant.

The plugin is installed even when no hook is active. Managed hook changes can
therefore apply to the next event without rebuilding an ADK runner.

## ADK plugin behavior

The host-owned Balda plugin translates ADK callbacks into the existing
`hookcmd.Event` protocol and invokes `hooks.Runner`.

Supplying the plugin works for any `agent.Agent`; callback delivery still
depends on the agent using the ADK tool execution path. A custom agent that
implements `Run` without ADK-managed tools emits no ADK tool callbacks. This is
normal agent behavior and does not require factory metadata or a separate
capability contract.

### Before a tool

The adapter copies callback arguments into a `PreToolUse` event and runs the
ordered hook chain.

- `allow`: return `(nil, nil)` and let ADK execute the tool.
- `updated_input`: validate it as a JSON object, replace the contents of the
  callback's `args` map in place, then return `(nil, nil)`. Returning a non-nil
  result would cause ADK to skip the tool.
- `deny`: return a bounded structured result explaining the policy decision.
  ADK skips the actual tool.
- timeout, malformed output, or executable failure: fail closed and return the
  same bounded denial form.

The adapter does not return raw executable errors to the agent loop. Detailed
diagnostics and stderr remain in the hook audit record.

### After a tool

The adapter creates a `PostToolUse` event from the effective arguments and tool
result. A hook may keep the result or replace it with bounded policy feedback.
A post-hook decision cannot undo a completed side effect and must not be
reported as prevention.

`AfterToolCallback` receives results and errors. `OnToolErrorCallback` may
normalize an error that remains after the post-tool chain.

Additional context is encoded in the replacement result or retained as turn
evidence according to the callback contract. Raw executable output never
bypasses the existing untrusted-context boundary.

## Native ACP fallback

Some providers execute tools inside their external ACP process, outside the ADK
tool path. Their existing composition remains separate:

```text
catalogapp.ProviderFactory
        |
        v
ProviderLaunchPreparer
        |
        v
hookprovider.Service.Prepare()
        |
        v
provider-native proxy
```

`hookprovider` privately owns provider type recognition, command wrapping,
session bindings, process cleanup, and validation of native interception. This
provider-specific knowledge does not enter `agentfactory`, `agent.Agent`, the
ADK plugin, or the generic hook protocol.

An active tool hook may cause native `Prepare` to reject construction when a
selected ACP provider cannot be intercepted. That check uses the provider
configuration already owned by `catalogapp.ProviderFactory` and
`hookprovider.Service`; it does not require metadata on the returned agent.

ADK and native paths use the same `hooks.Runner` and command protocol. Each
event records its source as `adk` or `native`. Normally only one path sees a
given tool because execution ownership is different. A bounded per-turn
deduplication key using session, invocation, tool-call identifier, and event
guards against double execution when an integration exposes the same call on
both paths. Missing tool-call identifiers must not cause unrelated calls to be
collapsed.

Existing native sessions may require `/reset` when they were started without
the proxy. ADK hooks do not require reset because the dormant runner plugin is
installed when the runner is created.

## Agent Plugins 1.0 extension

The portable package manifest remains Agent Plugins 1.0. Hook declarations use
version 2 of the Balda client extension because version 1 is a closed,
commands-only schema. Its canonical `$id` is
`https://baldaworks.dev/schemas/balda-extension/2.0.0/schema.json`; Balda embeds
and validates it offline. Version 1 remains valid and unchanged.

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
contribution. The schema remains closed. Hook names are unique within a package;
events initially accept `PreToolUse` and `PostToolUse`.

`executable` is a relative path contained in the immutable package revision.
Validation rejects absolute paths, traversal, symlink escape, missing files, and
paths outside the materialized revision. Balda executes the resolved file
directly with declared arguments and does not use shell interpolation. Scripts
may use a shebang. Existing bounds for arguments, input, output, stderr, process
termination, and duration continue to apply. Manifests must not contain
credentials.

The extension declaration is immutable package metadata. Installation only
makes a hook available for review; it does not authorize execution.

## Catalog and activation lifecycle

`runtimecatalogcmd` gains `ContributionKindHook`, an immutable `HookDescriptor`,
and `Snapshot.Hooks`. The descriptor contains the package identity and revision,
effective name, event, resolved executable reference, arguments, matcher,
timeout, and description. Catalog compilation validates and reports diagnostics;
it never executes the hook.

Activation is separate durable state. An administrator activates one exact
plugin ID, package revision, and hook name in Backoffice. The activation may:

- enable or disable the hook;
- narrow its provider scope;
- narrow its matcher;
- lower its timeout.

It cannot replace the executable, arguments, event, or package revision, or
widen manifest constraints.

Install and upgrade review shows the executable path, arguments, event, matcher,
and timeout. Installing, enabling, or upgrading a package never auto-activates
new or changed executable hooks. An approved hook stays pinned to its exact
retained revision until an administrator approves its replacement.

Disabling an activation is an immediate live kill switch. Removing a package
deactivates its hook activations atomically with the durable package transition;
retained bytes remain available for audit and in-flight cleanup. Re-enabling a
package does not silently restore executable hook activations.

Managed hooks created directly in Backoffice keep their existing storage and
lifecycle. The execution layer projects both sources into one ordered runtime
view.

## Ordering and identity

Managed database hooks and active package hooks form one deterministic chain.
The effective identity is stable and unique:

```text
managed/<hook-name>
plugin/<plugin-id>/<hook-name>
```

The initial ordering is lexical by effective identity within an event. Package
discovery order, installation time, and map iteration do not affect execution.

## Ownership

- `agentfactory.Factory` creates and returns `agent.Agent`; its contract does not
  change.
- `agent.RunnerFactory` merges host plugins into `runner.PluginConfig` and
  creates stock ADK runners.
- `agent.Builder`, `agent.RuntimeManager`, and GoalKeeper use the runner factory
  for user-facing runners.
- `hookcmd` owns provider-neutral events and results.
- `hooks` owns the combined managed/package runtime view, ordering, execution
  bounds, and failure policy.
- `runtimecatalog` owns extension v2 discovery, immutable hook descriptors,
  revision pinning, and diagnostics. It does not execute hooks.
- `pluginapp` owns install/upgrade review, activation candidates, retained
  revisions, and package lifecycle transitions.
- `state` owns guarded activation persistence and audit records.
- `hookprovider` owns provider-native synchronous adapters only.
- Backoffice presents and mutates hooks through application ports and does not
  import catalog or provider implementations.

## Nested runners

Balda passes `RunnerFactory` to every runner construction point it owns. A
third-party agent may create an internal runner without using the host factory;
that runner does not automatically inherit Balda plugins. Balda must not claim
coverage for such internally created execution. Where Balda owns the child
construction, it must use the shared runner factory.

Google ADK currently tracks related plugin propagation behavior for agent-tool
sub-runners in
[google/adk-go#669](https://github.com/google/adk-go/issues/669). The integration
test suite should pin the observed behavior of the repository's ADK version.

## Failure and security model

Executable hooks run with the Balda process's operating-system authority. The
explicit administrator activation and exact revision pin are security
boundaries. Package trust verification, retained revision integrity, path
containment, output bounds, timeouts, process-tree termination, and audit remain
mandatory.

Pre-execution paths fail closed. Post-execution hook failure replaces or
annotates the result according to existing policy but never claims that the
completed side effect was prevented.

## Migration

1. Add `agent.RunnerFactory` and the host-owned ADK plugin constructor.
2. Route the main runtime, child runtimes, and GoalKeeper worker/validator
   runners through it. Keep dedicated internal runners explicitly unhooked.
3. Implement ADK callback translation on top of the existing `hooks.Runner` and
   verify in-place argument rewriting against the repository-pinned ADK version.
4. Keep `catalogapp.ProviderFactory` and `hookprovider.Service.Prepare` as the
   separate native ACP fallback. Remove provider restrictions only from generic
   ADK hook code; native validation remains private to `hookprovider`.
5. Add source-aware deduplication for calls visible through both integrations.
6. Add Balda extension schema v2, catalog hook descriptors, durable activation,
   and Backoffice review.
7. Enable package-hook execution after audit, rollback, and kill-switch tests
   pass. Existing managed hooks require no data migration.

## Verification matrix

- `agentfactory.Factory` still returns only `agent.Agent` and has no new hook or
  capability contract.
- `RunnerFactory` preserves caller plugins and close timeout, adds the Balda
  plugin once, and does not mutate caller-owned slices.
- Main, child, and GoalKeeper runners receive the plugin; dedicated internal
  runners do not unless explicitly requested.
- An ADK-managed function tool allows execution, denies before execution,
  rewrites arguments in place, and replaces a result.
- A custom agent without ADK-managed tools runs normally and produces no false
  tool-hook events.
- Existing Claude, Codex, and OpenCode native proxy tests continue unchanged in
  ownership and behavior.
- A tool visible through both integrations invokes each hook once.
- Agent Plugins extension v1 remains compatible. Extension v2 accepts
  commands-only, hooks-only, and combined packages and rejects duplicate names,
  unknown fields, escaping paths, and invalid events.
- Install does not execute or activate a hook. Activation pins an exact revision;
  upgrade requires review; disable and removal stop new invocation immediately;
  rollback restores only an explicitly approved revision.
- Timeout, malformed JSON, executable failure, oversized output, and process-tree
  cancellation produce bounded audit evidence and the documented failure mode.

## Invariants

- Balda uses the stock Google ADK runner and plugin API.
- `agentfactory.Factory` and `agent.Agent` remain free of Balda hook metadata.
- All Balda-owned user-facing runners use the shared `agent.RunnerFactory`.
- Provider-specific interception stays inside `hookprovider` and provider launch
  composition.
- Agent Plugins 1.0 remains the package format; Balda extension schema v2 adds
  hook contributions without changing the portable manifest contract.
- Installing or upgrading a package never authorizes newly supplied executable
  code to run.
- Exactly one integration path owns each authoritative hook invocation.

## References

- [Google ADK runner package](https://pkg.go.dev/google.golang.org/adk/v2/runner)
- [Google ADK plugin package](https://pkg.go.dev/google.golang.org/adk/v2/plugin)
- [Google ADK callbacks](https://google.github.io/adk-docs/callbacks/)
- [Agent Plugins specification](https://agent-plugins.org/specification)
