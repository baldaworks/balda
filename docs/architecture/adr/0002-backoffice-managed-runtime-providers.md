# ADR 0002: Backoffice-managed runtime providers

- Status: Proposed
- Date: 2026-10-17
- Owners: Balda maintainers
- Decision scope: managed provider definitions, protected values, session binding, and restore
- Depends on: ADR 0003, YAML-first provider driver registry
- Supersedes: none
- Approval: pending

## Context

Balda reads providers from `runtime.providers` and selects the root with
`balda.provider`. Providers may be hosted, ACP-backed, or ordered pools.
Operators currently edit YAML and restart Balda to change them.

Backoffice should manage an additional set of definitions without breaking
YAML. Changes apply to new sessions and `/reset`; active sessions keep their
selected graph.

ADR 0003 defines the runtime driver registry and YAML normalization. This
decision adds only the persistence, encryption, and session binding needed for
managed providers. Authentication, probing, model discovery, auth homes,
artifact retention, and custom executables are deferred.

## Decision

### 1. Use one namespace with two sources

| Source | Storage | Backoffice |
| --- | --- | --- |
| `configured` | Effective `runtime.providers` | redacted, read-only |
| `managed` | Shared SQLite or PostgreSQL state | create, edit, enable, disable, delete |

Provider IDs are immutable and share one namespace. A configured and managed
provider with the same ID is an error. Balda never chooses a source by
precedence.

`balda.provider` remains configuration-owned and may reference either source.
Moving a definition between sources uses a new provider ID.

YAML-only installations keep their current behavior. Balda does not copy YAML
providers into the database.

### 2. Store immutable managed revisions

Each managed edit creates an immutable revision. A provider record contains its
stable ID, current revision ID, enabled/deleted state, and optimistic version.
A global `provider_state_version` changes with every managed mutation.

One revision contains:

- canonical driver type and driver contract version;
- storage format version;
- normalized public `agentconfig.Config`;
- encrypted protected values, when present;
- immutable identity values;
- ordered pool member IDs;
- whether execution is reproducible from Balda-controlled inputs.

The runtime `DriverDescriptor` gains one `ContractVersion`. Balda uses the
runtime registry for canonical type, validation, normalization, and
`immutable_identity_fields`; it does not add a second driver registry.

Changing an immutable field requires a new provider ID. For ACP this includes
canonical type and `registry_id`. The first increment rejects `generic_acp`,
explicit `cmd`, and command overrides, so executable identity cannot change
under an existing managed ID.

Managed Registry ACP may remain floating. Such a revision is valid but marked
non-reproducible until a future artifact contract retains exact bytes.

### 3. Encrypt managed values with the application key

Balda moves the existing AES-GCM primitive behind an application-scoped
`SecretProtector` with narrow `Seal` and `Open` operations. MCP and provider
management use the same deployment key with different AAD domains. The existing
MCP credential-key setting remains the key source in this increment, so current
encrypted MCP data needs no migration or re-encryption.

Managed API keys and every managed ACP `env` value are write-only protected
values. Public definitions contain only environment names and presence
information.

One encrypted payload belongs to one provider revision. Its AAD binds:

```text
managed-provider-revision-v1
provider_id
revision_id
```

Changing a protected value creates a new revision and reseals the payload.
Plaintext never enters HTML, logs, audit records, revision IDs, session state,
or errors.

The key is optional for YAML-only installations and managed definitions without
protected values. A protected revision cannot be created or opened without the
matching key. Operators back up the key with the database. Key rotation is
deferred.

Configured provider secrets remain external deployment input. Balda neither
copies nor fingerprints them.

### 4. Resolve managed state directly from the database

There is no provider publication layer, in-memory provider catalog generation,
or activation intent.

A managed mutation:

1. reads `provider_state_version`;
2. validates the complete candidate graph;
3. atomically commits the revision or lifecycle state, audit record, and
   incremented state version using optimistic compare-and-swap.

The mutation conflicts if another provider mutation changed the version.
SQLite and PostgreSQL commit either the complete valid graph change or nothing.

A new session reads configured providers plus one consistent committed managed
state and compiles its graph. An implementation may cache compiled state by
`provider_state_version`, but the database remains authoritative and cache
invalidation is not part of correctness.

### 5. Persist the provider graph with the session

Provider revisions are not runtimecatalog contributions. Runtimecatalog keeps
ownership of commands, hooks, skills, and MCP.

The session stores a `ProviderBinding` value beside its catalog snapshot:

```text
ProviderBinding
  root_provider_id
  ordered entries:
    provider_id
    source
    revision_id
```

Managed entries reference immutable database revisions. Configured entries
reference the current configured provider by ID and a public-definition
revision; their secrets remain external and unpinned.

The graph compiler preserves pool order and rejects missing or disabled
providers, empty IDs or pools, cycles, duplicate members, source collisions,
unavailable driver contracts or protected values, identity violations, and
invalid runtime definitions.

Session creation persists `ProviderBinding` and the independent catalog
snapshot ID before exposure. No binding table or binding ID is introduced.

Managed revisions and tombstones are retained indefinitely in this increment.
Physical purge and revision rollback are deferred.

### 6. Build runtimes from the binding

Runtime construction loads the exact managed revisions named by the binding,
decrypts protected values only at build time, reconstructs typed configs, and
passes one scoped graph to `agentfactory`. It never selects newer managed
revisions by provider ID.

All provider consumers use the same bound graph, including ordinary and reset
sessions, scheduled and webhook sessions, pools, goal and session-memory
runtimes when applicable, and native hook provider validation.
`hooknative.Registry` remains only the native hook-adapter registry.

New sessions and `/reset` resolve current committed state. Active sessions
keep their runtime and binding.

### 7. Bound restore guarantees

Managed public definitions, explicit managed environment values, API keys, and
pool graphs are reproducible while their revisions, driver contracts, key, and
any required retained artifacts remain available.

Configured secrets, inherited process environment, agent auth homes, external
accounts and model availability, and floating Registry artifacts remain
external and mutable.

A configured entry restores using current YAML only while its public-definition
revision still matches. Managed entries restore from exact revisions. A
non-reproducible binding receives a computed warning; no restore-grade taxonomy
is persisted.

If a binding cannot be reconstructed, the existing bounded fresh-session
fallback may use current state. Balda records the fallback and does not call it
exact restore.

### 8. Keep validation and Backoffice passive

`balda validate` remains database-free. It validates configured definitions
and reports references that may come from managed state as deferred.

`balda preflight` and `balda start` open state and require the selected root
graph before conversational ingress. Collisions, invalid graphs, missing
contracts, and undecryptable required revisions fail startup.

Administration → Providers shows source, canonical type, public model or mode,
pool members, enabled/deleted state, revision, reproducibility warning, and
state version.

Configured rows are read-only. Managed rows support create, edit, enable,
disable, and delete. Protected fields accept replace or clear and otherwise
show presence only.

Listing, validation, mutations, startup compilation, and page loads do not
launch ACP, call hosted providers, authenticate accounts, fetch Registry
metadata, or discover models.

Mutations require current administrator authority, same-origin and CSRF checks,
optimistic state, and an audit event in the same transaction.

## Ownership

- `runtime/v2/agentconfig` owns provider types, driver descriptors,
  `ContractVersion`, validation, and normalization.
- A Balda provider service owns source reconciliation, graph validation,
  revisions, mutations, and binding resolution.
- The application-scoped `SecretProtector` owns encryption only.
- `internal/apps/balda/state` owns records, revisions, opaque ciphertext,
  state version, tombstones, and atomic audit writes.
- The session capability binder carries `ProviderBinding` beside the catalog
  snapshot.
- `internal/apps/balda/agent` builds runtimes from the binding.
- Backoffice owns HTTP and presentation through a provider-service port.
- Runtimecatalog remains unchanged.

```text
runtime.providers ----+
                     +--> provider resolver --> ProviderBinding --> runtime
managed state DB ----+
```

## Compatibility and migration

Existing YAML, root selection, pools, profiles, environment expansion, and
process inheritance keep their current meaning. YAML-only deployments require
no key or managed-provider migration.

Legacy sessions without a provider binding resolve the current configured graph
on first compatible load. If it differs or cannot be resolved, Balda uses the
existing fresh-session fallback.

Database migrations add provider records, immutable revisions, one provider
state-version row, and a provider-binding field in session state for SQLite and
PostgreSQL. They do not edit YAML.

Before rolling back, operators restore a fully configured YAML root graph.
Older binaries ignore managed-provider tables.

## Alternatives considered

- Replacing YAML was rejected because it breaks deployment ownership and
  offline validation.
- Source precedence was rejected because collisions could redirect a root or
  pool member.
- Runtimecatalog contributions were rejected because provider graphs and
  protected values have different lifecycle requirements.
- Provider publication and activation intents were rejected because new
  sessions can read committed state directly.
- A separate managed-driver registry was rejected because ADR 0003 already
  defines the driver registry.
- Separate binding records, rollback, and purge were deferred because inline
  bindings and indefinite retention satisfy the first increment.
- Arbitrary managed commands were deferred because they execute host code with
  protected environment values.
- Mandatory probes were rejected because offline definitions must work and ACP
  probes can mutate auth state.

## Consequences

The design adds one state version, immutable managed revisions, one inline
session binding, and one application encryption boundary. New sessions perform
a consistent provider-state read; an optional version-keyed cache may reduce
that cost.

Configured credentials retain legacy behavior and remain outside exact restore.
Managed auth, discovery, custom commands, artifact retention, rollback, purge,
and key rotation remain future work.

## Acceptance criteria

1. YAML-only installations behave as before.
2. Configured and managed IDs share one collision-free namespace.
3. Every managed mutation atomically commits revision/lifecycle state, audit,
   and one incremented `provider_state_version`.
4. A new session observes either the complete state before a mutation or the
   complete state after it.
5. Managed API keys and environment values are encrypted with revision-bound
   AAD and never enter public surfaces.
6. Existing encrypted MCP data remains readable with the current deployment
   key.
7. Session state contains the root and exact ordered transitive provider
   entries beside its catalog snapshot ID.
8. Runtime and native-hook provider resolution use the bound graph.
9. New sessions and `/reset` adopt current committed state; active sessions
   retain their binding.
10. Managed generic and explicit ACP commands are rejected.
11. Missing revisions, contracts, keys, or required artifacts fail exact
    restore without silently selecting newer managed revisions.
12. Listing, validation, mutations, and startup compilation do not execute
    providers, authenticate, or discover models.

## Deferred decisions

- Authentication and managed auth homes.
- Model and session-option discovery.
- Registry artifact resolution and retention.
- Custom executable authorization and isolation.
- Revision rollback and physical purge.
- Application-key rotation.
- Managed selection of `balda.provider`.
