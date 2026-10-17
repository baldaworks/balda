# ADR 0002: Backoffice-managed runtime providers

- Status: Proposed
- Date: 2026-10-17
- Owners: Balda maintainers
- Decision scope: runtime provider definitions, Backoffice management, runtime
  catalog selection, session restore
- Supersedes: none
- Approval: pending

## Context

Balda currently reads named provider definitions from
`runtime.providers` in `.config/balda/config.yaml`. The runtime library decodes
each entry as an `agentconfig.Config`; providers may be ACP, hosted API, or an
ordered pool whose members refer to other provider IDs. `balda.provider`
selects the root provider used for ordinary Balda sessions.

The provider map is loaded during process construction and copied into agent
factories. Backoffice can display the selected provider, but cannot create,
edit, disable, or delete provider definitions. Changing a provider therefore
requires editing deployment configuration and restarting Balda.

Backoffice already uses a source-aware management pattern for resources such
as MCP connections, schedules, and webhooks: configuration-owned records are
visible but read-only, while administrator-managed records live in the shared
state database with guarded mutations and audit history. Provider management
should follow the same operator model without making old configuration invalid.

Providers differ from ordinary settings in three important ways:

- pools form a directed graph of provider references;
- provider definitions may contain credentials and executable commands;
- changing a provider under a stable name must not silently change the runtime
  of an existing conversation or make exact restore use a different backend.

## Decision drivers

- Preserve existing `runtime.providers` and environment override behavior.
- Make database-managed definitions usable anywhere a configured provider is
  usable, including as pool members.
- Keep one deterministic provider namespace and reject ambiguous ownership.
- Preserve exact session behavior across edits, disablement, deletion, and
  process restarts.
- Keep secrets write-only in Backoffice and out of HTML, logs, audit payloads,
  and revision hashes.
- Apply changes without interrupting healthy conversations.
- Keep provider policy in Balda rather than in the generic runtime library or
  the Backoffice HTTP layer.

## Decision

### 1. Compile one source-aware provider catalog

Balda compiles an effective provider catalog from two sources:

| Source | Storage | Backoffice behavior | Lifecycle |
| --- | --- | --- | --- |
| `configured` | `runtime.providers` after profile and environment overrides | visible, redacted, read-only | refreshed at process start |
| `managed` | shared SQLite or PostgreSQL state | create, edit, enable, disable, delete | available after a committed mutation |

Both sources use the existing `agentconfig.Config` schema as the provider
definition contract. Management metadata such as source, stable record ID,
revision ID, version, enabled state, and deletion tombstone stays outside that
schema.

Provider IDs occupy one effective namespace and are immutable after creation.
Backoffice rejects creation with an ID owned by configuration or another
managed record. Startup and preflight fail if configured and managed active
records collide. Balda does not silently choose a winner because that would
make behavior depend on load order and could redirect a pool member or root
provider.

Configured providers remain fully supported. An installation that never
creates a managed provider observes the same effective definitions and
selection behavior as before this decision.

### 2. Keep the root selection in deployment configuration

This decision manages the definitions that duplicate `runtime.providers`.
The root selection `balda.provider` remains configuration-owned. It may name
either a configured or an enabled managed provider in the effective catalog.

Keeping root selection outside the first provider-management increment avoids
turning a browser mutation into an implicit change of the application's
default execution backend. A later decision may add a guarded managed root
selection if operational experience justifies it.

Migration from a configured definition to a managed one uses a new provider
ID: create and validate the managed provider, change `balda.provider` or pool
references to the new ID as needed, then remove the old configured definition.
The same ID cannot be present in both sources during the transition.

### 3. Store immutable revisions and pin the transitive provider graph

Each accepted managed definition change creates an immutable revision. The
managed record points to its current revision and carries an optimistic
version. Disablement and deletion change selection metadata; deletion leaves a
tombstone and retained revisions.

Creating a managed provider produces a disabled record by default. A disabled
record must be locally valid but may refer to providers that are not yet
available, allowing a pool to be assembled before publication. Enabling or
changing an enabled record validates the complete resulting effective graph.
Disabling or deleting a record is rejected while the current root or another
enabled provider depends on it. The state change, audit record, and publication
of the new current revision form one guarded operation.

At startup, Balda canonicalizes each configured definition into an immutable
configured revision. Its identity is derived from the effective normalized
definition and protected-value hashes, not from YAML formatting or secret
plaintext.

Provider revisions become runtime catalog contributions. When Balda creates a
session capability snapshot, it resolves the selected root and recursively
captures every pool member as an exact provider revision. The same snapshot
already used to restore session capabilities becomes the authority for
reconstructing the provider graph. A pool revision is therefore insufficient
by itself; its snapshot must also contain the exact revisions of all reachable
members.

The compiler rejects:

- a missing or disabled root or pool member;
- an empty member ID;
- an empty pool;
- a provider-reference cycle;
- duplicate pool members after normalization;
- an unsupported or invalid `agentconfig.Config`;
- a source-name collision;
- a managed definition whose protected values cannot be resolved.

Validation errors identify provider IDs and fields but never include protected
values.

### 4. Adopt managed changes at session boundaries

A successful managed mutation publishes a new current catalog view. It does
not rebuild, replace, or terminate an already constructed runtime.

- A new session selects the current enabled revisions.
- `/reset` starts a new session and adopts the current revisions.
- An active or restorable session continues with its pinned snapshot.
- Disabling or deleting a provider prevents new selection but retains revisions
  required by existing pins.
- If an exact pinned revision cannot be reconstructed, the existing session
  restore fallback may create a fresh session from the current eligible
  catalog. Balda must report the fallback and must not describe it as exact
  restore.

This is the same user-visible adoption boundary used by other immutable
runtime capabilities. It prevents an administrator edit from changing the
model, command, credential, or pool membership halfway through a conversation.

Provider processes remain lazy. Listing providers or opening a Backoffice page
does not launch ACP commands or contact hosted APIs. An explicit bounded
diagnostic may construct a candidate runtime, but its result is transient and
does not publish, enable, or claim durable readiness for the definition.

### 5. Guard mutations and protect credentials

Only administrators with the provider-management permission may mutate managed
providers. Browser mutations use the existing Backoffice protections:
authenticated current authority, same-origin and CSRF checks, optimistic
versioning, and an audit event committed with the state change.

Provider configuration is split into public definition data and protected
values. API keys and any future credential-bearing fields use Balda's existing
encrypted state protection and deployment-key policy. Backoffice accepts a new
value or an explicit clear operation; it never returns an existing plaintext
value. Edit forms show only whether a protected value is present.

Audit records contain record identity, source, operation, actor, version, and
revision identity. They do not contain credentials, full command environments,
system instructions, or complete serialized definitions.

ACP command and argument fields are configuration, not shell text. Runtime
construction continues to execute argv directly under existing provider and
hook policies. Backoffice does not add shell interpolation.

### 6. Separate ownership by layer

The responsibilities are divided as follows:

- `github.com/baldaworks/runtime/v2/agentconfig` remains the generic provider
  schema and validation boundary. It does not gain Balda persistence, browser,
  authorization, or source-precedence policy.
- A Balda provider-management application service owns source reconciliation,
  provider IDs, revision creation, graph validation, protected-field handling,
  enablement, deletion, and mutation policy.
- `internal/apps/balda/state` owns managed records, immutable revisions,
  protected values, optimistic versions, tombstones, and atomic audit writes.
- `internal/apps/balda/runtimecatalog` and `catalogapp` own immutable provider
  contributions, snapshot selection, exact-revision lookup, and runtime factory
  binding.
- `internal/apps/backoffice` owns routes, forms, view models, redaction, and
  presentation through a narrow provider-operations port. It does not construct
  agents or decide provider-selection policy.
- Runtime factories receive a resolved provider graph for one snapshot rather
  than retaining the startup `runtime.providers` map as their only source.

Conceptually:

```text
runtime.providers ---- read-only adapter ---+
                                           |
Backoffice -> provider management -> state +--> provider catalog compiler
                                                   |
                                                   v
                                      immutable runtime snapshot
                                                   |
                                                   v
                                        scoped provider factory
```

### 7. Define validation and startup behavior

`balda validate` remains database-free. It validates the configured provider
source and all provider references that can be resolved from that source. If
`balda.provider` or a pool member is intentionally supplied by managed state,
validation reports that effective provider validation is deferred rather than
claiming the provider exists.

`balda preflight` and `balda start` open the selected state provider, load
managed revisions, compile the effective catalog, and require the selected
root graph to be complete before enabling conversational ingress. A missing,
ambiguous, undecryptable, or invalid effective root aborts startup before chat
work is accepted.

Backoffice does not introduce a provider-recovery degraded mode in this
decision. Initial installation still creates a usable configured provider.
An operator who wants a database-only definition first creates and validates
it while Balda is running, updates the file-owned root selection, and then
restarts. This keeps startup failure semantics explicit and avoids exposing a
partially initialized application mode.

### 8. Provide a source-aware Backoffice experience

Backoffice adds an Administration → Providers inventory with source, type,
model or mode when non-sensitive, pool membership, enabled state, revision,
and validation state.

Configured rows are read-only and explain that changes belong in
`runtime.providers`. Managed rows support create, edit, enable, disable, delete,
and explicit candidate diagnostics. The UI warns that accepted changes affect
new sessions and sessions reset afterward. It does not offer an action that
mutates or resets existing conversations in bulk.

Pool editing uses provider IDs from the unified inventory. The server repeats
all validation and authorization; browser choices are never authoritative.

## Compatibility and migration

### Existing installations

No data migration copies `runtime.providers` into the database. On first start,
configured definitions are projected as read-only catalog entries. Existing
provider IDs, `balda.provider`, pools, profiles, and environment overrides keep
their current meaning.

Existing sessions that predate provider revision pinning receive a legacy
configured-provider capture during state migration or first compatible load.
The capture records the provider ID, normalized public definition, protected
field hashes, and the transitive pool graph available at that time. It does not
copy plaintext secrets into session or catalog rows.

Exact legacy restore is allowed only when the current configured values match
the retained capture. If they do not match, Balda follows the existing bounded
fresh-session fallback and records that exact provider restoration was not
possible.

### Rolling forward

The schema migration creates managed-provider, revision, protected-value, and
snapshot-association storage for both SQLite and PostgreSQL. It does not alter
the configuration file. Deployment order is binary first, automatic schema
migration on provider open, then optional Backoffice-managed definitions.

### Rolling back

Configured providers remain the rollback path. Before using managed providers
as the selected root or required pool members, operators keep or restore an
equivalent configured graph. An older binary ignores the new provider tables
but cannot run a root that exists only in managed state.

Database rollback after managed revisions have been created requires restoring
a pre-migration database backup with the older binary stopped. Removing only
the new tables is not a supported rollback because session snapshots may refer
to retained provider revisions.

## Consequences

### Positive

- Existing configuration remains a first-class, read-only source.
- Administrators can manage provider definitions without editing deployment
  files.
- New sessions can adopt a provider change without interrupting active work.
- Exact revisions make provider edits and pool changes auditable and
  reproducible.
- One namespace and fail-closed conflicts avoid hidden precedence rules.
- The generic runtime library remains independent of Balda product policy.

### Costs and risks

- Provider definitions and transitive pool graphs become part of runtime
  snapshot retention and garbage-collection policy.
- Runtime factories must resolve snapshot-bound definitions instead of relying
  on one immutable startup map.
- Database-free `validate` cannot prove managed references; operators must use
  state-aware preflight for the effective graph.
- Database-only provider deployments cannot be repaired through Backoffice when
  the selected graph prevents process startup; the configured fallback and
  staged migration procedure remain operationally important.
- Retaining old credential-bearing revisions increases the importance of key
  rotation and protected-state backup procedures.

## Alternatives considered

### Let managed definitions override configured names

Rejected. Silent precedence makes a browser mutation capable of redirecting a
configured root or pool member and makes rollback dependent on load order.

### Let configuration always override managed names

Rejected. The managed record would appear enabled while being ineffective, and
removing the configured entry could unexpectedly activate stale database state.

### Rewrite `config.yaml` from Backoffice

Rejected. It breaks immutable/container deployments, loses environment-override
semantics, couples the browser to filesystem permissions and formatting, and
does not provide immutable history for session restore.

### Apply edits immediately to active sessions

Rejected. Replacing a provider during a conversation can change model behavior,
credentials, tools, or pool failover order without a session boundary and
cannot provide exact restore semantics.

### Require a restart for every managed edit

Rejected as the default. The runtime catalog already has an immutable snapshot
boundary suitable for new-session adoption. A restart remains necessary for
file-owned root-selection changes and rollback to configured definitions.

### Move source merging into `runtime/v2`

Rejected. Persistence sources, administrator authority, audit, Backoffice, and
session migration are Balda product concerns. The runtime library should keep
accepting resolved provider definitions.

## Acceptance criteria

- An unchanged configuration-only installation builds the same effective
  provider graph and can start and restore sessions.
- Backoffice lists configured providers as redacted and read-only.
- An authorized administrator can create, revise, enable, disable, and delete a
  managed provider with optimistic concurrency and an atomic audit record.
- A configured/managed ID collision is rejected consistently by mutation,
  preflight, and startup.
- Pools can reference configured and managed providers, and snapshots capture
  the exact transitive graph.
- Existing sessions retain their pinned provider revisions after a managed
  edit, disable, or deletion; a new session and `/reset` select current enabled
  revisions.
- Inventory reads launch no provider process and make no external provider
  request.
- Protected values never appear in HTML, logs, errors, audit payloads, or
  plaintext revision data.
- SQLite and PostgreSQL enforce equivalent revision, version, tombstone,
  authority, and audit behavior.
- `balda validate` clearly identifies deferred managed references, while
  `balda preflight` validates the complete effective root graph.
- Architecture enforcement keeps management policy out of Backoffice handlers,
  `state`, and `runtime/v2`.

## Follow-up decisions

- Whether `balda.provider` itself should become Backoffice-managed.
- Retention and garbage collection for provider revisions no longer referenced
  by sessions, audits, or current definitions.
- Whether candidate diagnostics should only validate construction or also run a
  bounded model interaction, with corresponding cost and data-disclosure
  controls.
