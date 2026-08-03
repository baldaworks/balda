# Session-memory HTTP/JSON v1

`internal/apps/balda/sessionmemoryhttp` is the replaceable reference adapter for
the portable `sessionmemory.Provider` contract. It is intentionally unaware of
Balda channels, session managers, JetStream, and MCP.

## Endpoints

The provider receives the versioned core JSON shape at:

- `POST /v1/turns` with a `sessionmemory.Turn`;
- `POST /v1/boundaries` with a `sessionmemory.Boundary`;
- `POST /v1/search` with a normalized `sessionmemory.SearchRequest`, returning
  `sessionmemory.SearchResponse`.

The base URL may include a deployment path, for example
`https://memory.example/internal`. The adapter appends the endpoint path to
that prefix. Requests use `Content-Type: application/json` and
`Accept: application/json`.

Turn and boundary writes set `Idempotency-Key` to the core `ExportID`. The
provider must treat that key idempotently beyond any JetStream duplicate
window. A documented `409` response with `code` or `kind` equal to
`duplicate`, `already_exists`, or `idempotent_replay` is success.

An optional configured token is sent as `Authorization: Bearer <token>`. The
token and request/response bodies are never included in adapter errors.

## Outcomes

HTTP 2xx is success. `408 Request Timeout`, `429 Too Many Requests`, `5xx`,
transport timeouts, and other transport failures are classified as retryable.
Other `4xx` responses are permanent. Response bodies are bounded and are not
copied into diagnostics.

Search responses are validated against the request before being returned. The
echoed scope must match exactly, and every result must carry the same exact
scope key, a session ID, and non-empty text. A foreign scope is returned as
`sessionmemory.CodeScopeViolation`; recalled text remains data and is not
executed or injected into a prompt by this adapter.

The adapter applies a bounded per-request timeout (ten seconds by default) and
supports an injected `http.Client` for in-process tests or deployment-specific
transport configuration.
