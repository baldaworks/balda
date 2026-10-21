# ADR 0003: YAML-first provider driver registry

- Status: Accepted
- Date: 2026-10-17
- Owners: Balda maintainers
- Decision scope: runtime provider types, YAML configuration, provider
  resolution, ACP process environment
- Supersedes: none
- Approval: Balda channel approval, 2026-10-17

## Context

Balda configures execution providers under `runtime.providers`. Each entry has
an immutable provider ID, a `type`, and one type-specific configuration block.
The runtime library currently recognizes provider types through repeated
switches in configuration validation, normalization, metadata projection, and
factory construction.

That representation is insufficient for the next provider capabilities:

- ACP agents store authentication and configuration under different
  environment-selected homes, such as `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, or
  the XDG directories;
- generic and Registry ACP agents may need arbitrary environment overlays;
- provider types differ in model discovery, authentication, executable
  resolution, session options, and concurrency behavior;
- `registry_acp` selects an entry from the external ACP Registry and may need
  an agent-specific adapter after `registry_id` is known;
- future consumers need one authoritative description of these differences
  instead of duplicating type switches.

The first consumer of this work is YAML configuration and ordinary runtime
construction. Backoffice management, database-owned definitions, browser
authentication, model discovery UI, and managed auth workspaces are deferred
until the YAML and runtime contract is complete.

## Terminology

This decision distinguishes three registries:

- **configured provider map**: the instances declared at
  `runtime.providers.<provider_id>`;
- **provider driver registry**: the in-process registry of supported provider
  types and their behavior;
- **ACP Registry**: the external catalog addressed by `registry_acp.registry_id`.

The provider driver registry describes how a provider type is validated and
resolved. It does not contain configured provider instances and does not fetch
the ACP Registry.

## Decision drivers

- Make YAML capable of expressing the process environment required by every
  existing ACP provider type.
- Preserve all configuration that works before this decision.
- Keep configuration loading and validation passive and deterministic.
- Establish one provider-type registry that later discovery and management
  surfaces can consume without moving product policy into HTTP handlers.
- Keep provider resolution in the generic runtime library and Balda-specific
  persistence or management policy in Balda.
- Avoid promising reproducible Registry ACP artifacts before artifact
  resolution and retention exist.

## Decision

### 1. YAML remains the first authoritative source

The first implementation extends the existing `runtime.providers` YAML
contract. It does not introduce database-owned providers, browser forms, or a
second configuration model.

The selected profile and environment substitutions continue to be applied
before provider validation. `balda validate` and ordinary runtime construction
consume the same normalized provider definitions. Base preflight remains
provider-independent.

Example:

```yaml
runtime:
  providers:
    codex-work:
      type: codex_acp
      codex_acp:
        model: gpt-5.4
        inherit_env: true
        env:
          CODEX_HOME: /state/providers/codex-work

    claude-work:
      type: claude_code_acp
      claude_code_acp:
        env:
          CLAUDE_CONFIG_DIR: /state/providers/claude-work

    opencode-work:
      type: opencode_acp
      opencode_acp:
        env:
          XDG_CONFIG_HOME: /state/providers/opencode-work/config
          XDG_DATA_HOME: /state/providers/opencode-work/data
          XDG_STATE_HOME: /state/providers/opencode-work/state
          XDG_CACHE_HOME: /state/providers/opencode-work/cache
```

Paths are deployment configuration. The runtime does not derive state paths,
create credential generations, migrate an existing CLI home, or infer which
account a directory contains in this phase.

### 2. Register built-in provider drivers by runtime type

The runtime library owns a closed registry of built-in provider drivers keyed
by the canonical `agentconfig` type. The registry has exactly one descriptor
for each canonical hosted, ACP, and pool type. Compatibility aliases resolve
to that same descriptor. Duplicate canonical keys fail registry construction.

Drivers are registered by trusted Go composition. YAML, plugins, provider
commands, and ACP Registry metadata cannot register executable driver code.
Adding a new driver remains a source-code and release operation.

A driver descriptor exposes the information and behavior needed by runtime
consumers:

```text
DriverDescriptor
  type
  class: hosted | acp | pool
  configuration block
  aliases
  immutable identity fields
  process environment capability
  executable resolution capability
  authentication capability source
  session option capability source
  model discovery capability source
  concurrency policy
```

The descriptor may delegate validation and resolution to typed functions. The
registry does not require configuration schemas to become untyped maps.
`agentconfig.Config` and its typed blocks remain the serialized YAML contract.

The existing validation, normalization, metadata, and factory switches are
migrated behind this registry incrementally. During migration, behavior must
remain identical and every public operation must resolve the same canonical
driver for a type.

### 3. Add common ACP process-environment fields

The common `ACPConfig` gains:

```yaml
inherit_env: true
env:
  NAME: value
```

All ACP aliases carry these fields through resolution, including
`generic_acp`, `registry_acp`, `codex_acp`, `claude_code_acp`, `opencode_acp`,
`gemini_acp`, `copilot_acp`, `grok_acp`, and `agy_acp`. Compatibility aliases
such as `claude_acp` and `antigravity_acp` retain their existing canonical
configuration blocks and receive the same environment behavior.

Environment construction follows these rules:

1. omitted `inherit_env` means `true`;
2. when inheritance is enabled, the ACP child starts from the Balda process
   environment captured at launch;
3. when inheritance is disabled, the ACP child starts with an empty
   environment;
4. entries in `env` overlay the selected base by exact variable name;
5. an empty value sets the variable to the empty string; it does not remove the
   variable;
6. names must be non-empty and contain neither `=` nor NUL; values must not
   contain NUL;
7. the final environment is emitted in deterministic key order;
8. values are never written to logs, errors, metadata projections, or command
   arguments.

The same resolved environment is used for every launch path of the provider,
including lazy construction and future explicit provider probes. A diagnostic
may add bounded internal marker variables, but it cannot
change or omit the provider overlay.

`cmd` and `extra_args` remain argv. Environment values do not introduce shell
evaluation or argument interpolation.

### 4. Keep home-variable selection explicit in YAML

The first phase does not automatically assign provider homes. Operators select
paths explicitly through `env` because deployment persistence, ownership,
backup, and container mounts belong to the deployment.

Drivers may advertise well-known environment names for validation,
documentation, and future management clients. This metadata is advisory and
does not inject values. Initial descriptors include at least:

| Driver | Well-known home variables |
| --- | --- |
| `codex_acp` | `CODEX_HOME` |
| `claude_code_acp` | `CLAUDE_CONFIG_DIR` |
| `opencode_acp` | `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME` |
| generic and unknown Registry ACP | `HOME` and the XDG variables as portable fallbacks |

The table documents environment routing, not credential ownership. The
runtime does not inspect, copy, hash, expose, or delete files beneath those
paths.

### 5. Resolve providers to one runtime-facing contract

Driver resolution produces a runtime-facing provider value containing the
canonical type, resolved command or hosted configuration, session settings,
and process environment policy. Conceptually:

```text
runtime.providers YAML
        |
        v
typed agentconfig.Config
        |
        v
ProviderDriverRegistry.Resolve(type, config)
        |
        v
ResolvedProvider
  command
  environment
  hosted settings
  session option selections
  driver identity
        |
        v
agent factory
```

The agent factory consumes the resolved value. It does not rediscover aliases,
reapply environment policy, or choose a different driver.

Pool providers resolve member IDs through the configured provider map. A pool
has no ACP environment or model discovery of its own; each reachable leaf is
resolved by its own driver.

### 6. Treat Registry ACP as a base driver plus an optional adapter

`registry_acp` remains a built-in provider driver. Its existing YAML fields and
default launcher behavior remain valid:

```yaml
runtime:
  providers:
    registry-agent:
      type: registry_acp
      registry_acp:
        registry_id: amp-acp
        bridge_version: 0.1.6
        env:
          HOME: /state/providers/registry-agent
```

The base driver validates common ACP fields and resolves the configured
launcher. After a non-empty `registry_id` is known, the registry may select a
trusted built-in adapter for that exact agent ID. An adapter may refine
well-known environment names, static capability metadata, and concurrency
policy. It cannot change the configured provider ID, silently replace the
selected Registry agent, or read network metadata during YAML validation.

An unknown `registry_id` remains valid and uses the generic ACP behavior. It
does not acquire guessed authentication or model-discovery capabilities.

An explicit `cmd` continues to override the default Registry launcher. When it
does, executable identity comes from the command configuration; Registry
metadata is descriptive only.

### 7. Describe capability sources without performing discovery

The driver registry records where a capability can be learned:

```text
CapabilitySource
  none
  static
  hosted_api
  acp_initialize
  acp_session_options
  external_registry
```

This phase uses the metadata for validation and inspection only. Loading YAML,
listing configured providers, and running `balda validate` do not:

- contact a hosted provider;
- fetch the ACP Registry;
- download or execute an agent;
- call ACP `initialize` or `session/new`;
- authenticate an account;
- discover models.

Observed ACP authentication methods and session options remain authoritative
over static hints once probing is implemented. The driver registry must not
claim that a particular account is authenticated or a particular model is
available.

### 8. Preserve current Registry ACP artifact semantics explicitly

This decision does not make `registry_acp` artifacts immutable. The default
`acprun` command may resolve Registry metadata or packages again at launch.
Pinning `bridge_version` pins the launcher version, not necessarily the
selected agent artifact.

Provider inspection must therefore distinguish a configured command from a
materialized immutable launch artifact. Exact artifact resolution,
content-addressed retention, checksums, and offline restoration require a
separate decision and implementation before managed provider revisions can
promise reproducible Registry ACP execution.

### 9. Define ownership boundaries

- `runtime/v2/agentconfig` owns typed provider configuration, built-in driver
  descriptors, passive validation, and deterministic resolution.
- `runtime/v2/agentfactory` constructs agents only from resolved providers and
  passes the resolved process environment to the ACP adapter.
- `go-adk-acpagent` owns applying the supplied environment to its child
  process without interpreting provider-specific variables.
- Balda owns profile and environment substitution, provider selection, pool
  graph policy, and future persistence or management policy.
- Backoffice does not participate in this phase.

The generic ACP process layer does not know that `CODEX_HOME` belongs to Codex
or that an XDG directory may contain authentication. It receives an already
resolved environment contract.

## Compatibility

Existing YAML remains valid without changes. When `inherit_env` and `env` are
both omitted, an ACP process inherits the same Balda process environment it
inherited before this decision. Default commands, aliases, session settings,
pool behavior, and Registry ACP launcher selection remain unchanged.

Environment substitutions keep their current YAML behavior. Operators may use
deployment environment references such as `${CODEX_HOME}`; missing-variable
handling remains owned by the existing configuration loader.

Configured providers may continue sharing one process home. Isolation occurs
only when the operator supplies distinct environment paths or disables
inheritance. No automatic migration moves existing credentials.

## Consequences

### Positive

- Every ACP provider can receive the home and credential-routing variables it
  needs directly from YAML.
- Existing deployments preserve their environment inheritance.
- Provider-type knowledge gains one authoritative registry.
- Registry ACP can gain trusted per-agent adapters without rejecting unknown
  registry entries.
- Later CLI or Backoffice work can consume stable driver metadata instead of
  rebuilding type switches.

### Costs and risks

- YAML environment values may contain secrets and must be redacted throughout
  normalization, diagnostics, and metadata.
- `inherit_env: true` retains the current risk that an ACP child can read the
  Balda process environment.
- `inherit_env: false` may require operators to supply `PATH`, locale, proxy,
  certificate, or package-manager variables needed by a command.
- Two providers may still point to the same home and race on agent-managed
  files; this phase exposes the configuration but does not serialize access.
- Registry ACP remains floating unless the configured command itself selects
  an immutable artifact.

## Alternatives considered

### Add `env` without a provider driver registry

Rejected. It solves immediate process launch but leaves capability,
validation, alias, and Registry ACP behavior duplicated across switches that
future consumers would need to reproduce.

### Automatically derive home directories from provider IDs

Rejected for the YAML-first phase. The runtime does not own deployment mounts,
backup policy, filesystem ownership, account migration, or credential purge.

### Use only `HOME` for every ACP agent

Rejected. Several supported agents use dedicated variables and may ignore
`HOME` for their primary configuration directory.

### Replace typed configuration blocks with arbitrary driver maps

Rejected. It weakens compile-time validation, obscures compatibility, and
creates a plugin-style configuration surface before dynamic drivers exist.

### Fetch capabilities during validation

Rejected. Validation must remain deterministic, database-free, network-free,
and free of provider process execution.

## Acceptance criteria

- Every canonical provider type has exactly one built-in driver descriptor,
  and every compatibility alias resolves to the intended descriptor.
- Existing provider YAML normalizes and launches with unchanged behavior.
- Every ACP configuration block accepts the common `inherit_env` and `env`
  fields.
- Environment overlay and inheritance semantics are identical across direct,
  alias, Registry, explicit provider probes, and lazy ACP launch paths.
- Provider validation performs no network access and starts no child process.
- `registry_acp` accepts unknown registry IDs through generic ACP behavior and
  applies the configured environment to the launcher and its child.
- Pool resolution applies each member's own driver and introduces no pool-level
  model or ACP environment.
- Diagnostics and metadata never expose environment values.
- Runtime and ACP adapter tests prove inherited, isolated, overridden, and
  empty environment values in the actual child process.

## Deferred decisions

- Immutable ACP Registry artifact resolution and content-addressed retention.
- Authentication workflows, auth-scope generations, and credential purge.
- Model and session-option probing.
- Provider concurrency enforcement.
- Database-managed provider definitions.
- Backoffice provider management and discovery UI.
