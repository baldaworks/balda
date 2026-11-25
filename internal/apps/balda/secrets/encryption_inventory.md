# Encryption inventory

`testdata/encryption-inventory.tsv` is the checked inventory of every
application-owned at-rest encryption scope, including its exact registered
label. `testdata/crypto-exclusions.tsv`
classifies every cryptographic import that is not such encryption. The package
test mechanically reconciles both files with production Go sources.

The current implementation has one AES-256-GCM primitive and three persisted
payload owners:

- MCP revision environment and header values, with AAD binding connection and
  revision identity;
- MCP OAuth grant access, refresh, and client secrets, with AAD binding grant,
  connection, remote endpoints, client, generation, and status;
- Provider revision API keys and environment values, with AAD binding provider
  and revision identity.

All three use the same `balda.security.key` root. The scoped primitive emits
version-2 `version || nonce || ciphertext` frames using registered HKDF-SHA-256
labels and opens retained version-1 frames directly with the same root. The
obsolete `balda.mcp_management.credential_key` field is retained only to reject
it with migration guidance; it is never an active or fallback root.

No direct TLS or provider-owned credential-store cryptography exists in the
production Go sources. TLS is transport encryption and provider-owned stores
remain outside Balda application-at-rest encryption unless Balda supplies their
root material; adding either category requires updating the checked inventory.
The contributor and compatibility workflow is documented in
[`docs/architecture/application-encryption.md`](../../../../docs/architecture/application-encryption.md).

Run the audit with:

```bash
go test ./internal/apps/balda/secrets
```
