# Backoffice application

This page is the authoritative application and Web UI foundation for
Backoffice. Read it before changing `cmd/balda` or
`internal/apps/backoffice`.

## Application boundary

- `cmd/balda` is the sole executable entrypoint. Its `start` command owns the
  process lifecycle; `backoffice bootstrap-admin` and `backoffice migrate-users`
  are offline maintenance subcommands, not separate runtime start modes.
- `internal/apps/balda` composes one state provider for bot and Backoffice.
- `internal/apps/backoffice` owns Backoffice application behavior, including
  HTTP routes, security enforcement, server-side rendering, view models,
  templates, static assets, and tests.
- The browser communicates only with Backoffice. Backoffice performs Assistant
  calls server-side; the browser must never call Assistant directly.
- `superadmin` is the interface name, not a new authorization role. Existing
  `administrator` and `operator` roles remain authoritative.

## Startup and configuration

`balda start` reads `.config/balda/config.yaml` once, applies `BALDA_*`
overrides, opens the database selected by `balda.database`, and applies its
embedded schema migrations. Backoffice uses that same provider and canonical
user store; it does not select a second database or run separate migrations.
The lifecycle checks legacy-user conversion and administrator bootstrap before
MCP or ingress, then binds Backoffice HTTP before enabling inbound transports.
A failed prerequisite or listener bind aborts startup. Shutdown closes ingress
before HTTP and the shared provider.

Commands:

- `balda validate` checks configuration and the application graph without
  opening or migrating the database. It is not a user-readiness check.
- `balda backoffice migrate-users --credentials-output <path>` performs the explicit
  forward-only owner/collaborator migration. The output file is created once
  with mode `0600`; it contains temporary plaintext credentials and must be
  distributed and deleted as sensitive material.
- `balda backoffice bootstrap-admin` reads a password from non-terminal stdin. It
  creates the first unbound primary administrator on a fresh database, or
  configures the selected credential-disabled administrator. Replacing a
  usable credential requires `--reset` and revokes all existing browser
  session families.
- `balda start` refuses pending legacy conversion or incomplete administrator
  bootstrap before binding any listener or ingress.

The safe defaults are loopback `127.0.0.1:8095`, public URL
`http://127.0.0.1:8095`, a 15-minute opaque access-token lifetime, and a
12-hour rotating refresh-token family lifetime. `access_token_ttl` must be
positive and shorter than `refresh_token_ttl`; both are bounded. A
non-loopback listener requires an HTTPS public URL.

Configure Backoffice in the existing Balda file; do not create a second
configuration or database section:

```yaml
balda:
  backoffice:
    listen_addr: "127.0.0.1:8095"
    public_url: "http://127.0.0.1:8095"
    access_token_ttl: "15m"
    refresh_token_ttl: "12h"
    qa_ui: false
```

Every field has the normal `BALDA_*` environment override, for example
`BALDA_BACKOFFICE_LISTEN_ADDR`, `BALDA_BACKOFFICE_PUBLIC_URL`,
`BALDA_BACKOFFICE_ACCESS_TOKEN_TTL`,
`BALDA_BACKOFFICE_REFRESH_TOKEN_TTL`, and `BALDA_BACKOFFICE_QA_UI`. Access
tokens may live from 1 minute through 1 hour. Refresh families must outlive
access tokens and may live for at most 30 days. The refresh deadline is an
absolute family deadline; rotation never extends it.

## Deployment and first administrator

The shipped Balda binary embeds the Backoffice UI; no second binary, frontend
toolchain, or runtime asset download is required:

```bash
mkdir -p ./bin
go build -trimpath -o ./bin/balda ./cmd/balda
./bin/balda init
./bin/balda validate
```

For a fresh database, supply the first administrator password over redirected
standard input. Never put a password in a command argument, shell history,
environment variable, log, or terminal paste:

```bash
./bin/balda backoffice bootstrap-admin \
  --username admin \
  --display-name "Balda administrator" < /run/secrets/backoffice-admin-password
./bin/balda start
```

The password file should be readable only by the service account and provided
by the deployment secret manager. `bootstrap-admin` deliberately refuses a
terminal as password input. Resetting an existing usable credential requires
an explicit `--reset`; it invalidates every browser session family for that
user.

For an existing installation with legacy owner/collaborator records, stop
Balda, take a consistent database backup, deploy the new binary, and run the
forward user conversion before start. The converted primary has a temporary
credential, so `--reset` sets its intended password and revokes any prior
browser refresh families. Skip conversion and reset when canonical users and
an active administrator are already ready:

```bash
./bin/balda backoffice migrate-users \
  --credentials-output /run/secrets/balda-migrated-users.txt
./bin/balda backoffice bootstrap-admin --reset < /run/secrets/backoffice-admin-password
./bin/balda start
```

The credentials path must not exist beforehand. Backoffice creates it
exclusively with mode `0600`, writes each generated temporary credential once,
and never prints a password to stdout. Distribute entries out of band to their
intended users, verify delivery, and then securely remove the manifest under
your organization's secret-retention policy. Never commit, upload, back up, or
attach the manifest to a ticket. Migrated bot bindings and roles become
canonical immediately; a temporary browser credential can reach only password
replacement and logout until it is changed.

User conversion is transactional and idempotent. A collision or interrupted
precondition fails instead of silently merging users. After it succeeds there
is no legacy runtime fallback. Rollback means restoring the pre-migration
database backup with the old binaries stopped; do not roll back only the binary
or re-enable legacy reads.

## Browser sessions and refresh rotation

Successful login creates a short-lived opaque access token and a longer-lived
refresh family. When access expires, Backoffice renders a continuation page;
the user submits its native POST form to rotate the single-use refresh token.
The server consumes generation N, creates generation N+1, replaces both
cookies, and preserves the family's original absolute expiry. The browser does
not silently replay the request that encountered expiry, especially an unsafe
mutation.

Submitting an already consumed refresh token is treated as verified replay.
Backoffice revokes the whole family and requires a new username/password login.
A genuine duplicate submit can therefore sign the user out; this is the
intentional fail-closed tradeoff. Invalid, expired, credential-stale, disabled,
or administratively revoked families also require re-login and receive only a
generic browser error.

Access administrators can revoke another browser family. Account owners can
revoke their own families, but revoking the current one requires explicit
confirmation. Family revocation invalidates the current access token and every
refresh generation in that lineage. Password reset, password replacement, and
user disablement revoke all affected families rather than leaving a refresh
credential that could restore access.

## Operations, recovery, and QA

- Run `balda validate` for read-only configuration/graph checks. `balda start`
  is the authoritative user-readiness gate and refuses to expose listeners
  until conversion and bootstrap are complete.
- Back up and restore the selected database as documented in
  [Balda state database](database.md). SQLite may be shared only by one Balda
  process; stop it for file backup or restore. PostgreSQL backups must include
  schema, data, sequences, and Goose
  migration history.
- Access is administrator-only. Account and Overview are available to active
  administrators and operators; Audit is administrator-only. The optional
  transport binding is read-only, and committed role/status changes immediately
  affect bot authorization.
- Set `qa_ui: true` only for a private development or review instance. It
  exposes deterministic repository-free fixtures at `/qa/ui/login`,
  `/qa/ui/refresh`, `/qa/ui/password`, `/qa/ui/overview`, `/qa/ui/access`,
  `/qa/ui/account`, and `/qa/ui/audit`. QA routes are GET/HEAD-only,
  `no-store`, and `noindex`; keep them disabled in production.
- Username/password is the only browser authentication provider in this
  release. OIDC, WebAuthn/passkeys, and MFA are intentionally deferred; no
  placeholder configuration or browser flow exists for them.

## Web UI foundation

The foundation is:

```text
AdminLTE presentation
        +
Go SSR as authority
        +
HTMX as optional enhancement
        +
server-side security boundary
```

The pinned stack is:

| Component | Version | Responsibility |
| --- | --- | --- |
| Go `html/template` | Go 1.26.6 | Authoritative SSR and HTML fragments |
| AdminLTE | 4.9.1 | Shell and UI components |
| HTMX | 2.0.10 | Progressive enhancement |
| Bootstrap Icons | 1.13.1 | Local icons |
| Vanilla JavaScript | Built in | HTMX lifecycle, focus, and sidebar behavior only |

Go templates are the only source of HTML. JavaScript must not assemble HTML.
Every screen must work without JavaScript through ordinary links and forms.
Every HTMX interaction must preserve an ordinary link or form as its fallback.

HTMX may improve navigation and interaction, but it must not own
authentication, authorization, validation, or domain state. Those concerns
remain server-side and authoritative.

## Packaging and frontend provenance

The Balda Go binary contains the Backoffice templates, CSS, JavaScript, icons, and
fonts. Node/npm, an SPA router, a frontend development server, and CDN-hosted
runtime assets are prohibited.

Every vendored frontend dependency must be pinned with its exact version,
license, and SHA-256 digest in
`internal/apps/backoffice/internal/webui/static/vendor/provenance.json`.

## Rendering contract

Return an HTML fragment only when all of these conditions hold:

```text
HX-Request: true
HX-Target: main-content
HX-History-Restore-Request != true
```

The fragment contains only:

```html
<title>...</title>
<main id="main-content">...</main>
```

Every other request, including an HTMX history restoration request, receives a
complete HTML document. Every rendered response contains exactly one
`main-content` element.

Use a buffered HTML writer. Do not commit response headers or status until the
template has rendered successfully.

## Mutations and errors

A successful mutation follows these response contracts:

| Request | Response |
| --- | --- |
| Ordinary form submission | `303 See Other` with `Location` |
| HTMX request | `204 No Content` with `HX-Location` |

Errors retain their original HTTP status. For an eligible HTMX fragment
request, an error replaces only `#main-content`; it must not replace the shell
or return a successful status.

Logout, CSV downloads, and WebAuthn interactions must disable HTMX boosting.
They use their native browser request and response behavior.

## Adding a page

Add pages in this order:

1. Implement authentication, authorization, CSRF protection, assurance,
   validation, and audit behavior in Go.
2. Add the server-side route.
3. Build navigation only from server-provided capabilities.
4. Use the shared AdminLTE shell and console view model.
5. Render through a buffered HTML writer.
6. Add the template to the explicit allowlist under
   `internal/apps/backoffice/internal/webui`.
7. Render exactly one `main-content` element.
8. Do not assemble HTML with JavaScript.
9. Preserve an ordinary link or form for every HTMX operation.
10. Verify the full page, fragment, non-JavaScript fallback, canonical URL, and
    error paths.

## Verification matrix

For every page or interaction, verify:

| Path | Expected behavior |
| --- | --- |
| Full-page navigation | Complete HTML document, canonical URL, one `main-content` |
| Eligible HTMX navigation | Only `title` and `main#main-content` |
| HTMX history restoration | Complete HTML document |
| JavaScript disabled | Equivalent navigation or mutation through links and forms |
| Ordinary mutation | `303` with `Location` |
| HTMX mutation | `204` with `HX-Location` |
| Validation or authorization error | Original error status and correct full-page or fragment shape |
| Logout, CSV, or WebAuthn | Native non-boosted behavior |
