# Encryption inventory

`testdata/encryption-inventory.tsv` is the checked inventory of every
application-owned at-rest encryption scope. `testdata/crypto-exclusions.tsv`
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

All three currently use the same `balda.mcp_management.credential_key` root and
version-1 `version || nonce || ciphertext` frame. That configuration ownership
is recorded as the defect being migrated by this Story, not as an approved
second root.

No direct TLS or provider-owned credential-store cryptography exists in the
production Go sources. TLS is transport encryption and provider-owned stores
remain outside Balda application-at-rest encryption unless Balda supplies their
root material; adding either category requires updating the checked inventory.

Run the audit with:

```bash
go test ./internal/apps/balda/secrets
```
