# Backoffice application

This page is the authoritative application and Web UI foundation for
Backoffice. Read it before changing `cmd/balda` or
`internal/apps/backoffice`.

## Application boundary

- `cmd/balda` is the sole executable entrypoint. Its `start` command owns the
  production process lifecycle; `backoffice bootstrap-admin` and
  `backoffice recover-2fa` are offline
  administrator maintenance subcommands.
  `backoffice qa serve` is a local preview command without application state.
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
embedded Goose schema and data migrations. Backoffice uses that same provider
and canonical user store; it does not select a second database or run separate migrations.
Provider opening automatically converts legacy owner/collaborator data.
The lifecycle checks canonical administrator bootstrap before
MCP or ingress, then binds Backoffice HTTP before enabling inbound transports.
A failed prerequisite or listener bind aborts startup. Shutdown closes ingress
before HTTP and the shared provider.

The selected provider may remain explicitly unavailable while authenticated
management starts for three current MCP authorization states: a valid selected
OAuth revision needs worker authorization, a fresh configured remote returns a
bounded same-origin OAuth challenge, or a changed remote file requires a new
capture of its valid retained OAuth definition. Tools are never omitted to
construct a partial runner. Every selected MCP blocker is inspected; an ordinary
remote failure, malformed or foreign challenge, missing historical revision,
invalid selected provider, unavailable protection material, or cancellation
still aborts startup. The credential bridge and catalog retain their existing
position before provider construction.

Saving the same worker authorization retries only its failed exact MCP
attachments. It preserves the definition revision, capability snapshot, and
ready runners. A failed retry leaves the saved grant non-ready and retryable.
A first or changed binding follows ordinary immutable catalog publication.
Changing a file-owned remote to stdio uses the current stdio declaration;
removing it excludes it from new selections. Retained remote captures, grants,
and session pins remain available under their exact identities.

Commands:

- `balda validate` checks configuration and the application graph without
  opening or migrating the database. It is not a user-readiness check.
- `balda init` creates the first administrator and prints its generated password
  once, alongside the owner token. Its username is `superuser`.
- `balda backoffice bootstrap-admin` generates and prints a password once by
  default; it also accepts an operator-provided password on non-terminal stdin.
  It creates the first unbound primary administrator on a fresh database, or
  configures the selected credential-disabled administrator. Replacing a
  usable credential requires `--reset` and revokes all existing browser
  session families.
- `balda start` applies schema and data migrations, then refuses incomplete
  administrator bootstrap before binding any listener or ingress. Migration
  failures also abort startup.

The safe defaults are loopback `127.0.0.1:8095`, public URL
`http://127.0.0.1:8095`, a 15-minute opaque access-token lifetime, and a
30-day rolling refresh-token lifetime. `access_token_ttl` must be
positive and shorter than `refresh_token_ttl`; both are bounded. A
non-loopback listener requires an HTTPS public URL.

Configure Backoffice in the existing Balda file; do not create a second
configuration or database section:

```yaml
balda:
  backoffice:
    listen_addr: "127.0.0.1:8095"
    public_url: "http://127.0.0.1:8095"
    base_path: ""
    access_token_ttl: "15m"
    refresh_token_ttl: "720h"
    qa_ui: false
```

Every field has the normal `BALDA_*` environment override, for example
`BALDA_BACKOFFICE_LISTEN_ADDR`, `BALDA_BACKOFFICE_PUBLIC_URL`,
`BALDA_BACKOFFICE_BASE_PATH`,
`BALDA_BACKOFFICE_ACCESS_TOKEN_TTL`,
`BALDA_BACKOFFICE_REFRESH_TOKEN_TTL`, and `BALDA_BACKOFFICE_QA_UI`. Access
tokens may live from 1 minute through 1 hour. Refresh families must outlive
access tokens and may live for at most 30 days. Every successful refresh renews the deadline to the effective rotation time
plus `refresh_token_ttl`; the default is 30 elapsed days (`720h`), rather than a
calendar month. Explicit YAML and environment values continue to override the
default.

`base_path` is an optional canonical absolute path without a trailing slash,
for example `/balda`. Leave `public_url` as the HTTPS origin without a path.
Backoffice serves its browser pages, assets, and session endpoints beneath the
base path and scopes browser cookies to it. The empty default preserves root
URLs for existing installations. A reverse proxy must forward the path unchanged.

## Deployment and first administrator

The shipped Balda binary embeds the Backoffice UI; no second binary, frontend
toolchain, or runtime asset download is required:

```bash
mkdir -p ./bin
go build -trimpath -o ./bin/balda ./cmd/balda
./bin/balda init
./bin/balda validate
```

For a fresh database, `init` creates the administrator with username and display
name `superuser` and prints its password once. Store the output securely, then start:

```bash
./bin/balda start
```

`bootstrap-admin` generates a new password when run without redirected input.
The optional stdin path remains available for an operator-provided password;
terminal input is never read or echoed. Never put passwords in command
arguments, shell history, environment variables, or logs. Resetting an existing
usable credential requires an explicit `--reset`; it invalidates every browser
session family for that user.

For an existing installation with legacy owner/collaborator records, stop
Balda, take a consistent database backup, and deploy the new binary. Opening
the selected state provider automatically runs the forward-only Goose data
migration for SQLite or PostgreSQL. The migration preserves users, roles,
bot bindings, and profiles without generating passwords or credential files.

Newly converted users have active bot access and disabled browser credentials.
The primary administrator has username and display name `superuser`.
Set its first browser password through the separate bootstrap operation:

```bash
./bin/balda backoffice bootstrap-admin
./bin/balda start
```

The bootstrap command opens the provider, so conversion completes before it
sets and prints the administrator password once. No `--reset` is needed for
an administrator whose browser credential is disabled. Other converted users
can receive browser credentials through the administrator's Access credential
reset flow.

On an already-converted database, the data migration leaves canonical users,
credentials, and browser sessions unchanged. Start directly when the primary
administrator is ready. Replacing an existing usable password requires
`bootstrap-admin --reset`, which revokes that user's browser refresh families.

Legacy conversion copies a Telegram collaborator's stored username and first
name into separate optional binding fields. Legacy owner records contain neither
field, so conversion leaves both empty. After a verified Telegram message or
command from a bound principal, Balda refreshes those fields from Telegram.
The schema upgrade backfills already-converted Telegram collaborator bindings
from retained legacy collaborator rows. If those rows are unavailable, the
fields stay empty until a verified event arrives.
The numeric Telegram principal remains the authorization key; provider profile
fields never replace the Backoffice username or display name.

User conversion and its Goose version marker commit in one transaction.
Repeated provider opening creates no duplicates. Invalid source records or
unmarked legacy records mixed with canonical users fail instead of silently
merging users; a failure rolls back conversion and does not advance its version.
After it succeeds there is no legacy runtime fallback. Rollback means restoring the pre-migration
database backup with the old binaries stopped; do not roll back only the binary
or re-enable legacy reads.

## Browser sessions and refresh rotation

Successful login creates a short-lived opaque access token and a longer-lived
refresh family. When access expires, the browser submits a guarded refresh form
automatically and returns to the page the user was opening. A manual form
remains available when JavaScript is disabled. The server consumes generation
N, creates generation N+1, replaces the access, refresh and CSRF cookies, and
renews the family deadline to 30 days after that successful refresh by default.
The active token, family deadline and success audit commit in one transaction;
refresh and CSRF cookies use that committed deadline. Historical generations keep
their original expirations for replay detection. A valid token can refresh just
before its current deadline; at or after that deadline it requires a new login.
If the host clock moves backward, rotation timestamps never precede stored last
activity or token issuance. A correction alone cannot extend the last renewed
deadline. A stale concurrent rotation receives a conflict and can be retried. The browser does not replay the request that
encountered expiry, especially an unsafe mutation.

A duplicate refresh within 30 seconds of rotation receives a conflict without
clearing cookies or revoking the family; another browser request may already
have installed the new pair. Reuse of an older consumed token is treated as
verified replay: Backoffice revokes the family and requires a new login.
Invalid, expired, credential-stale, disabled, or administratively revoked
families also require re-login.

When upgrading, set existing explicit `refresh_token_ttl: "12h"` values or
`BALDA_BACKOFFICE_REFRESH_TOKEN_TTL=12h` overrides to `720h` to use the monthly
window. Changing the default does not replace an explicit setting. An existing,
still-valid family adopts the effective configured lifetime on its next
successful refresh; an already-expired family requires sign-in.

Provider opening automatically applies a forward Goose migration for SQLite or
PostgreSQL that permits renewal of the family deadline. It leaves stored sessions
and immutable token histories unchanged. Take the usual consistent database backup
before upgrading. After rolling generations have been issued, use a binary that
supports their different historical expirations; reverting only to an older binary
with fixed-deadline validation is not a supported downgrade.

Access administrators can revoke another browser family. Account owners can
revoke their own families, but revoking the current one requires explicit
confirmation. Family revocation invalidates the current access token and every
refresh generation in that lineage. Password reset, password replacement, and
user disablement revoke all affected families rather than leaving a refresh
credential that could restore access.

Account and Access detail list active browser families only. When viewing your
own sessions, the current family appears first; other active families are in
descending order of their last recorded sign-in or refresh. The
**Older active sessions** link continues
through further active families; historical revoked and expired rows remain in
storage for authorized investigation but do not crowd the default list.
**Last sign-in or refresh** is not a record of every page visit. The session
card shows a short browser/platform label when the request User-Agent can be
recognized; old or unrecognized sessions show an unknown device. The
**Connection peer** is the direct socket address and may be a reverse proxy.
Forwarded IP headers are ignored because Backoffice has no configured trusted
proxy chain. Neither field is used to authorize or identify a user, and the
raw User-Agent and token values are never displayed.

## Operations, recovery, and QA

- Run `balda validate` for read-only configuration/graph checks. `balda start`
  is the authoritative user-readiness gate and refuses to expose listeners
  until provider migrations and administrator bootstrap are complete.
- Back up and restore the selected database as documented in
  [Balda state database](database.md). SQLite may be shared only by one Balda
  process; stop it for file backup or restore. PostgreSQL backups must include
  schema, data, sequences, and Goose
  migration history.
- Access is administrator-only. Account and Overview are available to active
  administrators and operators; Audit is administrator-only. Account places
  **Change password** before profile details. A successful change creates a
  fresh browser session and revokes previous access and refresh credentials.
  Each configured Telegram, Slack Agent (`slackagent`), Zulip, or Mattermost
  integration has its own account-binding panel. An administrator generates a
  single-use `bind_<token>` invitation for the selected existing user. The bot's
  verified instance and sender identity determine attachment; browser input
  cannot choose the principal or instance. User role and primary designation
  stay unchanged. No owner token is required. Invitations expire after 24 hours;
  issuing over a pending invitation requires explicit replacement confirmation.
  Cancel requires confirmation and leaves confirmed bindings intact. Disabled
  users cannot receive invitations, and disabling revokes pending invitations.
  Unavailable bot identity offers Retry connection instead of issuance. Telegram
  provides a start deep link and command, Slack a verified workspace DM and native
  command, Zulip a realm bot DM and start command, and Mattermost a `/msg @bot`
  DM action with a slash command only when its receiver is enabled. Mattermost
  uses the existing Direct and locator contract, including D and G channels.
  Refresh bindings shows confirmed principals and pending/expired metadata only.
  The invitation value is shown once in the issuance POST response, never in
  Backoffice navigation, session storage, server logs, or persisted state.
  The explicit Telegram bot deep link carries the payload as its intended
  provider action; it is never used as a Backoffice redirect or history URL. Only its
  digest is stored. Generic webhooks are not user bindings. Removing a confirmed
  binding requires confirmation of the affected principal's bot access. Other
  bindings, the browser account, and browser sessions remain.
  Committed role/status changes immediately affect bot authorization for every
  attached principal. Telegram bindings show the provider username and first
  name separately, with empty values when the provider has not supplied them.
- Access opens with the user list and filters for name, username, role, and
  status. User detail separates profile, chat bindings, credential reset, and
  browser sessions. Audit lists newest events first by event time, then stable
  ID, with actor, action, target, outcome, and filters that persist on the next
  page. Technical IDs remain available in event details. Overview lists
  configured integrations only; a configured card makes no claim about live
  connectivity or health.
- A terminal refresh failure offers sign-in as the primary action. A
  concurrent-refresh conflict offers a safe page reopen because another
  request may already have installed new cookies. Failed native and HTMX forms
  retain their HTTP status and show an actionable message. Ordinary links and
  forms remain available without JavaScript.
- Keep `qa_ui: false` in production. A private development instance may enable
  the same synthetic previews under `/qa/ui/`, but the preferred local workflow
  uses `balda backoffice qa serve` without configuration or database access.
  QA routes accept GET/HEAD only and send `no-store` and `noindex` headers.
  Follow the [Backoffice UI review runbook](backoffice-ui-review.md) for routes,
  browser checks, and the separate authenticated runtime check.
- Username/password remains the browser sign-in provider. Administrators can
  optionally enable a WebAuthn passkey as their second factor in Account; it is
  off by default for each user. OIDC remains deferred.

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

The shell adapts the AlaTooGuide Backoffice layout: a sticky utility top bar with the
current authenticated username, Account and native CSRF-protected sign-out;
a branded permission-derived sidebar; a shared content frame; and a footer
in normal grid flow. The top bar and sidebar brand share the same height;
navigation scrolls content below the top bar so page headings remain visible.
The viewer identity is independent of an inspected user.
Desktop collapse hides the sidebar and expands content. Mobile navigation uses
an overlay with backdrop, Escape and focus containment; ordinary menu links
remain available without JavaScript.

The theme is explicitly dark on console, authentication and recovery pages.
`data-lte-color-mode="off"` disables OS-driven mutation. Shared CSS tokens and
primitives own surfaces, headings, controls, statuses, cards, tables, empty and
danger states. Review the synthetic component and full-layout examples before
changing runtime page layouts; see the UI review runbook.

Go templates are the only source of HTML. JavaScript must not assemble HTML.
Every screen must work without JavaScript through ordinary links and forms.
Every HTMX interaction must preserve an ordinary link or form as its fallback.

HTMX may improve navigation and interaction, but it must not own
authentication, authorization, validation, or domain state. Those concerns
remain server-side and authoritative.

## Packaging and frontend provenance

The Balda Go binary contains the Backoffice templates, CSS, JavaScript, icons, and
fonts. Node/npm as a frontend build or runtime dependency, an SPA router, a
separate frontend development server, and CDN-hosted runtime assets are
prohibited. The local QA preview is served by the same Go binary and uses the
same embedded templates and assets.
Application CSS and JavaScript use content-versioned asset URLs so a browser or
edge cache receives the matching files after a deployment. Static asset responses
use immutable caching; a change to either application file changes its URL.

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
`main-content` element. Links and forms targeting `#main-content` use
`hx-swap="outerHTML"` because the response includes the main element itself.

Use a buffered HTML writer. Do not commit response headers or status until the
template has rendered successfully.

## Mutations and errors

A successful mutation follows these response contracts:

| Request | Response |
| --- | --- |
| Ordinary form submission | `303 See Other` with `Location` |
| HTMX request | `204 No Content` with JSON `HX-Location`: local `path`, `target: "#main-content"`, `swap: "outerHTML"` |

Invitation issuance is the deliberate exception: return `200 OK` with the full
page for native forms or `#main-content` for HTMX, and `Cache-Control: no-store`.
Reveal the newly generated value in that response only; a redirect would need
credential persistence. Protected GET of the issuance URL redirects to user
detail without a secret. Reload, refresh, and history recovery expose only safe
metadata. The shared `.app-main` is the explicit HTMX history element so
complete history-recovery responses restore content without re-executing shell
scripts; HTMX history caching is disabled on binding detail and secrets are
cleared on pagehide. Native no-store POST history may require reopening the
issuance URL as GET; it redirects to metadata-only user detail. Cancel and identity retry retain the ordinary contracts.

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
7. Render exactly one `main-content` element. Links and forms targeting `#main-content` use
`hx-swap="outerHTML"` because the response includes the main element itself.
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

## Optional administrator passkey 2FA

2FA is off by default, including after automatic upgrades. Each administrator
can enable it in **Account → Two-factor authentication**; operators continue
using password authentication. Bot bindings and admission are independent.
One passkey is active at a time. Use a browser with JavaScript and WebAuthn support
and an authenticator that supports user verification (PIN or biometric).
Password-only accounts remain usable without JavaScript.

Set `balda.backoffice.public_url` to the exact HTTPS origin, for example
`https://lab.example.org`, or `http://localhost:8095` for local development.
`base_path: /balda` remains a separate setting. The RP ID is the origin's hostname;
only that exact origin is accepted. IP origins, including the existing default
`http://127.0.0.1:8095`, cannot enroll keys. The default still starts and supports
password sign-in, and Account explains the unavailable enrollment capability.
Changing the hostname changes the RP: recover and register a new key rather than
expecting the old key to work. An enrolled administrator never falls back to
password-only access when verification is unavailable.

```yaml
balda:
  backoffice:
    public_url: https://lab.example.org
    base_path: /balda
    ceremony_ttl: 5m
    step_up_ttl: 15m
```

`ceremony_ttl` must be positive and at most 15 minutes; `step_up_ttl` must be
positive and at most one hour. Environment overrides are
`BALDA_BACKOFFICE_CEREMONY_TTL` and
`BALDA_BACKOFFICE_STEP_UP_TTL`. Ceremony state is one-use, expiring and
bound to its browser, CSRF token, purpose, user and current authority.

- **Enable:** confirm the current password, then register and verify a passkey.
  The setting becomes enabled only after successful completion. Old browser
  sessions are revoked and the current browser receives verified access.
- **Sign in:** submit the password, then verify the passkey. Until verification
  finishes, the browser has no usable access or refresh credentials. Temporary
  passwords still require password replacement after both factors succeed.
- **Sensitive actions:** Access, Audit and privileged mutations require recent
  passkey verification. Use the explicit confirmation screen after it expires.
  A rejected mutation returns 403 and is not applied or automatically replayed;
  return and submit it again after confirmation. Step-up keeps the refresh
  lineage and currently renewed session deadline. Refresh does not extend
  passkey verification freshness.
- **Replace:** explicitly confirm replacement, verify the current key, then
  register the new key. Only successful completion replaces the key and revokes
  old sessions. **Disable:** explicitly confirm removal, provide the current
  password and verify the current key. Successful removal returns to password
  authentication and revokes old sessions.
- **Cancel or retry:** cancelling the authenticator or leaving the ceremony
  keeps the factor setting unchanged. Retry while the ceremony is live, or
  cancel and start again. Unsupported/no-JavaScript browsers show guidance;
  they cannot bypass an enrolled factor. Authentication pages and ceremonies
  are native, non-boosted and excluded from HTMX history; responses use no-store.

Password change, Access password reset and `bootstrap-admin --reset` preserve
an enrolled passkey. Losing the key requires an explicitly confirmed operation
by an administrator with host/database access, using the normal configuration:

```bash
balda backoffice recover-2fa --username superuser --confirm
```

Use the exact normalized username of an existing active enrolled administrator.
Unknown, disabled, operator, already-off and unconfirmed targets fail. This
maintenance operation opens and upgrades the selected database without starting
HTTP, MCP or channels. It atomically disables the factor, advances authority,
revokes browser sessions and writes an audit event. It leaves the password hash
and bot bindings intact and prints no password or session credential. Sign in
with the existing password and register a replacement key from Account.

Factor transitions and verification appear in Audit. The verifier dependency
and source reuse are documented in the
[WebAuthn dependency review](backoffice-webauthn-dependency-review.md).
