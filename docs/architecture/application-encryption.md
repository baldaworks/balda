# Application encryption

Balda has one operator-configured root for application-owned at-rest
encryption: `app.security_key`, overridden only by
`BALDA_APP_SECURITY_KEY`. The value is strict standard base64 encoding of
exactly 32 bytes. Feature-local encryption keys and implicit key generation are
forbidden.

## Ownership and data flow

`internal/apps/balda/secrets` decodes the root once and derives one
AES-256-GCM key per registered scope with HKDF-SHA-256. The derivation salt is
`balda/application-encryption/v1`; scope labels are stable, unique, versioned
compatibility contracts. Trusted composition selects the scope before
persisted input reaches the protector. MCP and Provider packages own their AAD,
schemas, authorization, redaction, and immutable revision policy; the secrets
package owns only derivation and versioned AEAD framing.

New writes use:

```text
version 2 || 12-byte GCM nonce || ciphertext and authentication tag
```

Version 2 opens only with the selected scoped key and feature AAD. Retained
version-1 frames open with a private legacy AEAD built from the same
`app.security_key`; consumers cannot request it for new writes. Wrong roots,
scopes, AAD, unknown versions, truncated frames, and authentication failures
return the same bounded unavailable result and never partial plaintext.

The registered scopes and persistence owners are checked in
`internal/apps/balda/secrets/testdata/encryption-inventory.tsv`. The companion
exclusions file classifies hashing, random tokens, signing, TLS, password
hashing, and provider-owned stores that are not Balda at-rest encryption.

## Startup, backup, and rollback

Normal startup and maintenance preflight derive the same protectors and
passively open all retained encrypted MCP revisions, OAuth grants, and managed
Provider revisions, including historical and deleted rows. Validation does not
rewrite ciphertext. An empty root remains valid only when no protected state or
write requires it.

The database and exact application key form one backup unit. Stop writers and
take a consistent database backup before moving the identical legacy MCP key to
`app.security_key`. Retained v1 rows require no rewrite; ordinary new revisions
and grant updates write v2. Once any v2 payload exists, rollback to a v1-only
binary is unsupported. Restore the matching pre-upgrade database and key while
Balda is stopped instead of rolling back only the binary.

## Adding a scope

A contributor adding Balda-owned encrypted state must:

1. add one stable versioned label to the central scope registry;
2. add a known-answer derivation vector and cross-scope/AAD/root failure tests;
3. inject only the scoped protector at the feature boundary and keep root
   material in composition;
4. preserve bounded plaintext clearing, write-only projections, and
   feature-owned AAD;
5. add exactly one inventory row and classify any new cryptographic imports;
6. add retained-state startup/preflight and SQLite/PostgreSQL restart evidence;
7. update backup, compatibility, and rollback documentation.

Repository gates reject unregistered scopes, encryption consumers, primitive
owners, root constructors, root-like config fields, and feature-specific root
environment variables. Do not add another config key for a new scope.
