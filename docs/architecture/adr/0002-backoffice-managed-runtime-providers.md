# ADR 0002: Backoffice-managed runtime providers

- Status: Proposed
- Date: 2026-10-17
- Owners: Balda maintainers
- Decision scope: managed provider definitions, provider revision binding,
  protected values, publication, and session restore
- Depends on: ADR 0003, YAML-first provider driver registry
- Supersedes: none
- Approval: pending

## Context

Balda reads provider definitions from `runtime.providers` and selects the
ordinary root with `balda.provider`. A provider may be hosted, ACP-backed, or
an ordered pool whose members refer to other provider IDs. Configuration is
loaded once and copied into runtime factories, so changing a provider requires
editing deployment configuration and restarting Balda.

Backoffice already presents source-aware resources: configuration-owned rows
are visible and read-only, while managed rows use durable revisions, guarded
mutations, audit records, and recoverable publication. Providers additionally
form transitive graphs, contain protected values, and may execute local or
Registry-selected programs. Existing sessions must not silently adopt a
changed provider graph.

ADR 0003 defines the YAML-first runtime driver registry and deterministic
normalization. This decision adds Balda-owned persistence and session binding
around it. Browser authentication, model discovery, executable probes, managed
auth homes, and Registry artifact materialization are deferred.

## Decision drivers

- Preserve existing YAML, profiles, and environment overrides.
- Make managed definitions usable as roots and pool members.
- Keep one deterministic provider namespace without source precedence.
- Bind sessions to immutable transitive provider graphs.
- Encrypt managed values with the application-scoped secret key.
- Recover publication after a committed mutation or crash.
- State precisely which inputs Balda can restore.
- Keep provider bindings separate from runtime catalog contributions.

## Terminology

- **Provider record** is a stable provider ID, source, lifecycle state, and
  optimistic version.
- **Provider revision** is one immutable, versioned provider definition.
- **Effective provider view** is the collision-free set of enabled configured
  and managed revisions eligible for new bindings.
- **Provider binding** is an immutable root plus the exact ordered transitive
  revisions selected for one session.
- **Catalog snapshot** is the existing commands, hooks, skills, and MCP
  snapshot. It contains no provider definitions.
- **Applied generation** is the provider view published to runtime consumers.
  It may briefly lag durable desired state while recovery is pending.

## Decision

### 1. Compile one source-aware provider view

| Source | Storage | Backoffice | Lifecycle |
| --- | --- | --- | --- |
| `configured` | Effective `runtime.providers` | redacted, read-only | reconstructed at startup |
| `managed` | SQLite or PostgreSQL | managed | durable revisions and recoverable publication |

Provider IDs occupy one namespace and are immutable after creation. A
configured and managed provider with the same ID is an error. Startup,
preflight, and publication reject collisions instead of choosing a source.

YAML-only installations remain valid without managed records or an application
secret key. No migration copies YAML definitions into the database.

`balda.provider` remains configuration-owned and may name an enabled provider
from either source. Moving ownership uses a new provider ID; both sources
cannot contain the same ID during transition.

### 2. Add a versioned managed-driver contract

The runtime driver registry remains authoritative for canonical provider type,
typed validation, normalization, and its declared immutable identity fields.
Balda adds a closed managed-driver adapter for every driver allowed in
Backoffice:

```text
ManagedDriverAdapter
  canonical_driver_id
  driver_contract_version
  normalized_definition_version
  immutable_identity_fields
  public/protected field split
  managed execution policy
  passive presentation metadata
```

The adapter prepares a complete `agentconfig.Config` and delegates validation
and execution semantics to runtime. It does not duplicate provider execution.

Every revision stores:

- provider ID and immutable revision ID;
- canonical driver ID and driver contract version;
- normalized-definition version and normalized public definition;
- protected-payload format version and ciphertext, when present;
- immutable identity values and ordered direct pool references;
- reproducibility flags and retained artifact identity, when applicable;
- creation metadata without plaintext protected values.

Retained bytes are interpreted only by their recorded contract version. If it
is unavailable, reconstruction fails closed rather than renormalizing with
newer semantics.

Runtime `immutable_identity_fields` are the minimum boundary. Executable
origin is also identity for managed ACP. Changing canonical type,
`registry_id`, or executable selection requires a new provider ID so a new
executable cannot inherit old secrets or auth state.

### 3. Restrict managed executable configuration initially

The first increment supports hosted providers, pools, and trusted built-in ACP
launch paths exposed by approved managed-driver adapters. It rejects
`generic_acp`, explicit `cmd`, and command overrides for built-in ACP types.

Arbitrary managed commands plus protected environment values are host code
execution with application credentials. Supporting them requires a later
decision covering a separate high-privilege capability, provenance, review,
and isolation.

Registry-backed ACP follows its adapter policy. A floating Registry launch may
be enabled but is marked non-reproducible until an exact artifact is retained.

### 4. Use one application-scoped secret-protection service

Balda owns an application-scoped secret-protection service created at
composition with one deployment root key. It exposes narrow seal, open, and
keyed-fingerprint operations. MCP and provider management consume this service
through separate cryptographic domains; they do not own separate keys or
implementations.

Managed API keys and every value in managed ACP `env` are write-only
protected values. Environment names, binding kinds, and presence indicators
may be public. Values never appear in public definitions, HTML, logs, audit
records, revision IDs, snapshots, bindings, or errors.

One provider revision owns one canonical encrypted payload. Its authenticated
context binds at least:

```text
domain = managed-provider-revision
protected_format_version
provider_id
revision_id
```

Changing any protected value creates a revision and reseals the complete
payload. Sessions pin the revision ID and never persist plaintext or duplicate
ciphertext.

The application key is optional only while no encrypted managed data is
created or required. Creating, publishing, restoring, or using a protected
revision requires the matching key. A missing or incorrect key makes that
revision unavailable.

The existing MCP credential-key setting remains a compatible key input during
migration. Existing MCP revisions and grants must remain readable without bulk
reencryption. Conflicting app and legacy inputs fail configuration. Key IDs,
online rotation, and crash-safe rewrapping are deferred; operators retain the
same key with the database.

Configured YAML secrets are not copied. With an application key, Balda may
persist a domain-separated keyed fingerprint only to verify that the current
secret still matches a retained binding. Plain hashes are forbidden. Without
a verifiable match, exact configured-secret restoration is unavailable.

### 5. Store provider bindings outside runtimecatalog

Provider revisions do not become runtimecatalog sources or contributions.
`runtimecatalog` continues to own commands, hooks, skills, and MCP.

```text
ProviderBinding
  binding_id
  root_provider_id
  root_revision_id
  ordered transitive provider revision IDs
  provider-view generation
  restore grade and limitation flags
```

The binding compiler preserves pool order and rejects missing or disabled
members, empty IDs or pools, cycles, normalized duplicates, source collisions,
unavailable driver contracts or protected payloads, identity violations, and
invalid runtime definitions.

Session state persists the runtime catalog snapshot ID and provider binding ID
before exposure. The in-memory `SessionCapabilityBinding` carries both. A
composite storage operation prevents observing one without the other.

Disablement and deletion prevent new selection but retain tombstones,
revisions, ciphertext, and bindings required by sessions. Physical purge is a
separate guarded operation and fails while bindings reference the data.

### 6. Build runtimes from the bound graph

A provider-binding resolver loads exact revisions, decrypts only at build
time, rebuilds typed configs, verifies driver contract versions, and supplies
one scoped graph to `agentfactory`. Runtime construction does not reread the
startup provider map or select current revisions by provider ID.

Plaintext exists only as needed to construct a hosted client or child process
environment. It is not cached in catalog state, session metadata, inventory,
or audit payloads.

The same binding graph is used by ordinary, reset, scheduled, webhook, goal,
and session-memory runtimes when they select a managed provider; by pool member
construction; and by native hook provider validation and traversal.
`hooknativeapp` resolves provider identity from this graph while
`hooknative.Registry` remains solely the native hook-adapter registry.

New sessions and `/reset` adopt the applied provider view. Active sessions
retain their existing runtime and binding. Publication does not rebuild or
terminate healthy runtimes.

### 7. Publish through durable activation intents

A SQL commit and in-memory publication are not atomic. Every managed lifecycle
mutation follows:

1. authorize and verify optimistic state;
2. prepare and validate the complete candidate view;
3. atomically commit revision/lifecycle state, current selection, audit,
   desired generation, and pending activation intent;
4. publish the exact durable desired view;
5. complete the intent and record the applied generation.

Failure before commit rejects the mutation. Failure after commit returns
**Accepted; recovery pending** and the client must not repeat it. Explicit
recovery and startup reconciliation republish durable desired state
idempotently and complete intents before conversational ingress.

Inventory exposes desired and applied generations and pending recovery. New
bindings use only the applied generation; existing bindings remain valid.

### 8. Bound exact restore

| Input | Guarantee |
| --- | --- |
| Managed public definition and pool graph | Exact while revision and driver contract are retained |
| Managed API keys and explicit `env` values | Exact while ciphertext and matching application key exist |
| Configured public values | Exact only while current normalized values match |
| Configured secrets | Current-match verification only with a keyed fingerprint |
| Inherited process environment | External and mutable; excluded |
| Agent auth home and refreshed tokens | External and mutable; excluded |
| External account and model availability | External and mutable; excluded |
| Floating Registry artifact | Excluded |
| Retained content-addressed artifact | Exact bytes under a future artifact contract |

Each binding records a restore grade such as `exact-balda-inputs`,
`current-match-required`, or `floating-external-input`. It does not claim
external services or accounts are unchanged.

Restore requests the persisted provider binding and catalog snapshot. Missing
revisions or contracts, configured-secret mismatch, decryption failure, or an
absent required artifact prevents exact restore. The existing bounded
fresh-session fallback may use the current applied view while preserving the
stable Balda session identity and replacing provider state and history. The
fallback is recorded and never described as exact.

### 9. Define validation and startup

`balda validate` remains database-free. It validates configured definitions
and references resolvable from configuration. A syntactically valid reference
that may come from managed state is reported as deferred. Validation does not
decrypt managed data, fetch Registry metadata, download or execute agents,
authenticate, or discover models.

`balda preflight` and `balda start` open state, recover pending provider
activations, compile the effective view, and require the selected root graph
before conversational ingress. Collisions, unavailable contracts,
undecryptable required revisions, invalid graphs, and missing roots fail
startup.

Inventory, validation, publication, and startup compilation remain passive and
do not launch a provider or contact a hosted API.

### 10. Keep the first Backoffice surface passive

Administration → Providers shows source, canonical type, public model or mode,
pool members, lifecycle state, revision, restore grade, desired/applied
generation, and recovery status.

Configured rows are redacted and read-only. Managed rows support create, edit,
enable, disable, delete, rollback, and publication recovery through
server-owned adapters. Protected fields accept replace or clear and otherwise
show presence only.

The page performs schema and graph validation only. Authentication, executable
probes, hosted API tests, Registry refresh, and model discovery require a
future ADR because they can mutate external accounts or local state.

Mutations require current administrator authority, same-origin and CSRF
checks, optimistic state, and committed audit. Browser input never supplies
authoritative revision, graph, encryption, or publication evidence.

### 11. Assign ownership

- `runtime/v2/agentconfig`: typed provider schema, runtime driver registry,
  passive validation, and normalization.
- Managed-provider application service: adapters, source reconciliation,
  records, revisions, graph compilation, lifecycle, and activation recovery.
- Application secret-protection service: deployment key, domain-separated
  encryption, and keyed fingerprints, without provider or MCP policy.
- `internal/apps/balda/state`: records, revisions, opaque ciphertext,
  bindings, tombstones, activation intents, and atomic audit writes.
- Session capability binder: provider binding beside the independent catalog
  snapshot.
- `internal/apps/balda/agent`: exact bound-graph runtime construction.
- `hooknativeapp`: bound provider identity; `hooknative.Registry`: native
  hook adapters only.
- Backoffice: routes, forms, redaction, and presentation through narrow ports.
- `runtimecatalog` and `catalogapp`: commands, hooks, skills, and MCP only.

```text
runtime.providers ---- configured adapter ----+
                                             |
Backoffice -> managed providers -> state + activation intent
                                             |
                                     applied provider view
                                             |
                    ProviderBinding + catalog SnapshotID
                                             |
                                      scoped runtime
```

## Compatibility and migration

Existing provider IDs, root selection, pools, profiles, environment expansion,
and process inheritance keep their current meaning. YAML-only installations
require neither managed rows nor an application key.

Legacy sessions without a provider binding adopt a configured binding on first
compatible load when current values can be verified. Otherwise Balda uses the
fresh-session fallback. Plaintext configured secrets are never copied into
session rows.

The application secret service accepts the existing MCP deployment key as a
compatibility source. Existing ciphertext remains readable. The database and
matching key must be backed up together; lost or replaced keys make protected
history unavailable.

Migrations add provider records, revisions, protected payloads, bindings,
tombstones, generations, and activation intents for SQLite and PostgreSQL.
They do not edit YAML or publish a provider by themselves.

Before rolling back to a binary without managed providers, operators restore a
fully configured YAML root graph. Older binaries ignore managed tables, which
remain dormant for later upgrade.

## Alternatives considered

- Replacing YAML was rejected because it breaks deployment ownership, offline
  validation, profiles, and rollback.
- Source precedence was rejected because collisions could redirect roots or
  pool members.
- Runtimecatalog contributions were rejected because provider graphs, secrets,
  identity, and restore differ from catalog contributions.
- Pinning only a pool root was rejected because member edits would change
  restored behavior.
- Plain secret hashes and snapshot copies were rejected because hashes cannot
  restore values and copies multiply sensitive storage.
- Publishing inside SQL was rejected because the two operations cannot be
  atomic.
- Arbitrary managed commands were deferred pending stronger authorization and
  isolation.
- Mandatory probes were rejected because offline configuration must work and
  ACP probes can mutate auth state.

## Consequences

Managed providers adopt cleanly at session boundaries, retain an exact
Balda-controlled graph, encrypt values under one application key, and recover
post-commit publication failures. The cost is a second session binding,
versioned managed-driver adapters, scoped runtime construction, additional
state and migrations, and replacement of static provider maps in all
consumers.

External accounts, auth homes, inherited environment, remote model state, and
floating Registry artifacts remain outside exact restore.

## Acceptance criteria

1. YAML-only installations behave as before without managed migration or key.
2. Source collisions fail before ingress or publication.
3. Revisions pin canonical driver, contract versions, identity, public
   definition, and opaque revision-bound protected payload.
4. Managed API keys and environment values never enter public or diagnostic
   surfaces.
5. Existing encrypted MCP data remains readable through the app-scoped
   service using the existing deployment key.
6. Provider binding captures the root and ordered transitive revisions and is
   persisted atomically with the catalog snapshot.
7. Runtime and native-hook provider resolution use the bound graph.
8. New sessions and reset adopt the applied view; active sessions retain it.
9. Post-commit publication failure reports **Accepted; recovery pending**.
10. Startup recovers pending activations before ingress.
11. Missing contracts, keys, revisions, or artifacts fail exact restore
    without selecting current bytes silently.
12. Restore reporting distinguishes Balda-controlled, current-match, and
    floating external inputs.
13. Managed generic and explicit ACP commands are rejected initially.
14. Listing, validation, startup compilation, and publication do not execute
    providers, authenticate, or discover models.

## Deferred decisions

- Browser and CLI authentication, including terminal auth.
- Model and session-option discovery.
- Managed auth homes and generations for agent-owned state.
- Registry refresh, artifact materialization, retention, and offline replay.
- Custom executable authorization, provenance, sandboxing, and secret access.
- Application-key IDs, rotation, and crash-safe rewrapping.
- Managed selection of `balda.provider`.
