# Provider-neutral ADK agent tool hooks

Owner: Balda maintainers  
Status: proposed

## Context

Balda already has managed lifecycle and tool hooks, and provider-native adapters
can synchronously intercept tools executed by selected ACP providers. That path
cannot be the generic contract: provider identity does not say who executes a
tool, and an observed tool-call event does not prove that Balda can stop it.

Balda uses the stock `google.golang.org/adk/v2/runner`. Google ADK exposes a
runner-level `plugin.Plugin` with `BeforeToolCallback`, `AfterToolCallback`, and
`OnToolErrorCallback`. These callbacks provide a provider-neutral enforcement
point whenever ADK owns tool execution. Balda does not need a runner fork.

This document also defines tool-hook contributions in an
[Agent Plugins 1.0](https://agent-plugins.org/specification) package. "Agent
Plugins package" below means a package described by `plugin.json`; "ADK runner
plugin" means the Go callback object attached through `runner.PluginConfig`.

## Goals

- Attach one Balda ADK runner plugin to every user-facing runner built around an
  `agent.Agent`.
- Select enforcement from runtime capabilities rather than provider names.
- Preserve provider-native interception for tools executed outside ADK.
- Allow Agent Plugins 1.0 packages to contribute hooks through the Balda client
  extension without executing newly installed code automatically.
- Keep hook discovery, activation, execution, persistence, and presentation in
  their existing architecture boundaries.
- State accurately which paths can deny a tool or rewrite its arguments before
  side effects occur.

## Non-goals

- Forking or replacing the Google ADK runner.
- Claiming that every implementation of `agent.Agent` exposes its internal tool
  execution to runner callbacks.
- Treating an observed `session.Event` as a synchronous authorization request.
- Providing an operating-system sandbox for hook executables.
- Making provider identifiers part of the generic hook capability contract.

## Decision

`agent.Builder` constructs the stock ADK runner with a host-owned plugin:

```go
runner.New(runner.Config{
    AppName:        appName,
    Agent:          ag,
    SessionService: sessionSvc,
    PluginConfig: runner.PluginConfig{
        Plugins: []*plugin.Plugin{baldaToolHooks},
    },
})
```

The Balda plugin translates ADK callbacks into the existing `hookcmd.Event`
contract and invokes `hooks.Runner`. It is installed even when no hook is
active, so enabling or disabling ADK-managed hooks takes effect without a
session reset.

This attachment makes the callbacks available to any `agent.Agent`. Strong
tool-hook guarantees still require the agent's tool execution to pass through
the ADK plugin manager. An arbitrary agent can implement `Run` without using
ADK-managed tools, so runtime construction must declare the actual execution
capability separately.

Dedicated internal runners, such as maintenance or memory workers, opt in
explicitly. They are not made hookable merely because they use ADK.

## Capability contract

Runtime construction resolves one authoritative path for each tool execution:

| Mode | Execution owner | Pre-execution deny | Input rewrite | Post-result handling | Observation |
| --- | --- | --- | --- | --- | --- |
| `adk_managed` | Google ADK tool runtime | Yes | Yes | Yes | Yes |
| `native_managed` | External provider/ACP process with synchronous adapter | If declared | If declared | If declared | Yes |
| `observed_only` | External or opaque runtime | No | No | No | Yes |
| `unavailable` | No usable tool signal | No | No | No | No |

The neutral contract belongs in `hookcmd` and carries the mode plus explicit
capability flags. The mode describes ownership; flags allow a native adapter to
declare only the operations it actually supports. Validation asks whether a
requested behavior is supported by the resolved capability. It never checks a
Claude, Codex, OpenCode, or other provider allowlist.

The runtime binds capabilities when the provider session is created and records
them with session diagnostics. Provider name remains useful for hook scoping and
audit, but it does not grant enforcement capability.

For `observed_only`, Balda may emit audit records and bounded context for a later
turn. It does not run the actionable `PreToolUse` or `PostToolUse` chain, because
silently ignoring a deny, rewrite, or replacement would weaken the hook
contract. A future observation event needs its own explicit schema and semantics.

## ADK callback semantics

### Before a tool

The adapter copies the callback arguments into a `PreToolUse` event and runs the
ordered hook chain.

- `allow`: return `(nil, nil)` and let ADK execute the tool.
- `updated_input`: validate it as a JSON object, replace the contents of the
  callback's `args` map in place, then return `(nil, nil)`. This follows the ADK
  callback contract; returning a non-nil result would skip the tool.
- `deny`: return a bounded structured tool result that explains the policy
  decision. ADK skips the tool and exposes the result to the model.
- timeout, malformed output, or executable failure: fail closed and return the
  same bounded denial form.

The adapter never passes an untrusted hook error directly into the agent loop.
Detailed diagnostics and stderr stay in the audit record.

### After a tool

The adapter creates a `PostToolUse` event from the effective arguments and tool
result. A hook may keep the result or replace it with bounded policy feedback.
A post-hook decision cannot undo an external side effect and must not be
reported as prevention. `AfterToolCallback` receives both results and errors;
`OnToolErrorCallback` may normalize an error that remains after that chain.

Additional context is encoded in the replacement tool result or retained as
turn evidence, according to the callback result type. Raw hook output is never
inserted into a prompt without the existing untrusted-context boundary.

### Ordering and identity

Managed database hooks and active package hooks form one deterministic chain.
The effective identity is stable and unique:

```text
managed/<hook-name>
plugin/<plugin-id>/<hook-name>
```

The initial ordering is lexical by effective identity within an event. A future
priority field requires a schema revision; package discovery order, install
time, and map iteration never affect execution.

Only one mode owns authoritative invocation for a tool call. When ADK owns the
tool, native adapters do not run the same hooks. When a native adapter owns it,
ADK/session events are observation only. As a safety net, Balda deduplicates on
session, invocation, tool-call identifier, and hook event for the active turn.
If a stable tool-call identifier is unavailable, path ownership remains the
primary guard; a guessed identifier must not suppress unrelated calls.

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

Version 2 accepts `commands`, `hooks`, or both, and requires at least one
contribution. The schema remains closed. Hook names are unique within a package;
events initially accept `PreToolUse` and `PostToolUse`.

`executable` is a relative path contained in the immutable package revision.
Validation rejects absolute paths, traversal, symlink escape, missing files, and
paths outside the materialized revision. Balda executes the resolved file
directly with its declared arguments and does not use shell interpolation.
Scripts may use a shebang. Arguments, input, output, stderr, and duration retain
the existing hook bounds. Manifests must not contain credentials.

The extension declaration is immutable package metadata. Installation only
makes a hook available for review; it does not enable execution.

## Catalog and activation lifecycle

`runtimecatalogcmd` gains `ContributionKindHook`, an immutable `HookDescriptor`,
and `Snapshot.Hooks`. The descriptor contains the package identity and revision,
effective name, event, resolved executable reference, declared arguments,
matcher, timeout, and description. Catalog compilation validates and reports
diagnostics; it never runs the executable.

Activation is separate durable state. An administrator activates one exact
plugin ID, package revision, and hook name in Backoffice. The activation may:

- enable or disable the hook;
- narrow its provider deployment scope;
- narrow its matcher;
- lower its timeout.

It cannot replace the executable, arguments, event, package revision, or widen
manifest constraints. Broader changes require a new signed or reviewed package
revision.

Install and upgrade screens show the executable path, arguments, event, matcher,
timeout, and requested capability before activation. Installing, enabling, or
upgrading a package never auto-activates new or changed executable hooks. An
approved hook stays pinned to its exact retained revision until an administrator
approves the replacement. Retained revision bytes support running sessions,
audit, rollback, and inspection.

Disabling an activation is an immediate live kill switch. Removing a package
deactivates its hook activations atomically with the durable package transition;
retained bytes remain available for audit and in-flight cleanup. Re-enabling a
package does not silently restore executable hook activations.

Managed hooks created directly in Backoffice continue to use their existing
storage and lifecycle. The execution layer projects both sources into the same
ordered runtime view.

## Ownership

- `hookcmd` owns provider-neutral events, results, capability values, and
  validation primitives.
- `hooks` owns composition of managed and package activations, deterministic
  ordering, execution bounds, and failure policy.
- `runtimecatalog` owns extension v2 discovery, immutable hook descriptors,
  revision pinning, and diagnostics. It does not execute hooks.
- `pluginapp` owns install/upgrade review, activation candidates, retained
  revisions, and package lifecycle transitions.
- `state` owns guarded activation persistence and audit records.
- `agent` creates the ADK runner plugin and attaches it to stock runners.
- `hookprovider` owns only provider-native synchronous adapters and their
  capability declarations.
- `sessionturnapp` projects observation-only events and telemetry; it does not
  claim pre-execution enforcement.
- Backoffice presents and mutates hooks through application ports and does not
  import catalog or provider implementations.

## Nested agents and sub-runners

The runner plugin applies to the runner that receives `PluginConfig`. Balda must
pass the same plugin configuration to every sub-runner it constructs. Google ADK
currently has an open limitation where runners created internally by
`agenttool` may not inherit parent plugins. Until upstream propagation is
verified, those nested executions must declare `observed_only` or
`unavailable`; Balda must not advertise synchronous enforcement for them. Track
the upstream behavior in [google/adk-go#669](https://github.com/google/adk-go/issues/669).

## Failure and security model

Executable hooks run with the Balda process's operating-system authority. The
explicit administrator activation and exact revision pin are therefore security
boundaries, not convenience settings. Package trust verification, retained
revision integrity, path containment, output bounds, timeouts, process-tree
termination, and audit remain mandatory.

Pre-execution paths fail closed. Observation-only collection failures fail open
for the already external execution and emit diagnostics. Post-execution hook
failure replaces or annotates the result according to configured policy but
never claims that the completed side effect was prevented.

Capability loss after session creation is surfaced as a runtime diagnostic. A
hook requiring stronger enforcement is not silently downgraded to observation.

## Migration

1. Add Balda extension schema v2, catalog hook descriptors, activation records,
   and Backoffice review without executing package hooks.
2. Add the host-owned ADK runner plugin to all user-facing runners and verify
   in-place argument rewriting against the repository-pinned ADK version.
3. Resolve and persist tool execution capabilities at runtime construction.
   Route `adk_managed` through callbacks, `native_managed` through the existing
   adapter, and external event streams through observation only.
4. Replace provider allowlist validation with capability validation. Keep the
   current native adapters as implementations of `native_managed`, not as the
   definition of which providers support hooks.
5. Enable package-hook activation after catalog, audit, rollback, and kill-switch
   tests pass. Existing managed hooks require no data migration.

Existing native sessions may still require reset when their external process was
started without native interception. ADK-managed hooks do not require reset
because the dormant runner plugin is installed from session creation.

## Verification matrix

- A generic ADK agent with an ADK-managed function tool allows execution,
  denies before execution, rewrites arguments in place, and replaces a result.
- Arbitrary provider identifiers behave identically under the same capabilities,
  proving that no generic provider allowlist remains.
- An agent that emits function-call events but executes tools externally is
  observation only and cannot activate deny or rewrite guarantees.
- Existing Claude, Codex, and OpenCode native interception tests continue under
  `native_managed` capability declarations.
- A tool visible through both native and ADK/session signals invokes each hook
  once.
- Agent Plugins extension v1 remains compatible. Extension v2 accepts
  commands-only, hooks-only, and combined packages and rejects duplicate names,
  unknown fields, escaping paths, and invalid events.
- Install does not execute or activate a hook. Activation pins an exact revision;
  upgrade requires review; disable and removal stop new invocation immediately;
  rollback restores only an explicitly approved revision.
- Timeout, malformed JSON, executable failure, oversized output, and process-tree
  cancellation produce bounded audit evidence and the documented failure mode.
- Nested agent-tool execution is either covered by propagated plugin config or
  reported without a false enforcement capability.

## Invariants

- Balda uses the stock Google ADK runner and plugin API.
- Generic tool-hook code contains no provider allowlist.
- A deny or rewrite guarantee is exposed only when a synchronous enforcement
  capability is bound to the execution path.
- Agent Plugins 1.0 remains the package format; Balda extension schema v2 adds
  hook contributions without changing the portable manifest contract.
- Installing or upgrading a package never authorizes newly supplied executable
  code to run.
- Exactly one path owns authoritative hook execution for a tool call.

## References

- [Google ADK runner package](https://pkg.go.dev/google.golang.org/adk/v2/runner)
- [Google ADK plugin package](https://pkg.go.dev/google.golang.org/adk/v2/plugin)
- [Google ADK callbacks](https://google.github.io/adk-docs/callbacks/)
- [Agent Plugins specification](https://agent-plugins.org/specification)
