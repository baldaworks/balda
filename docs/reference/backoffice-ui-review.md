# Backoffice UI review

Use the embedded, synthetic preview gallery while developing Backoffice pages.
It runs without a Balda configuration, database, administrator password, or bot
transport. The preview reuses the production Go templates and local assets.

## Start a local preview

From the repository root, run:

```bash
go run ./cmd/balda backoffice qa serve --listen 127.0.0.1:8096
```

Open `http://127.0.0.1:8096/qa/ui/`. Review the shared foundation first: `/qa/ui/style-guide` covers all nine
component groups, `/qa/ui/layout` shows the complete shell, and
`/qa/ui/layout-long` exercises long names, navigation labels and content.
The gallery links to the login, session
refresh, password replacement, overview, access list and detail, account, audit,
and generic error previews. It also links to empty and error states where those
states exist. For example, `/qa/ui/access-list`, `/qa/ui/access-empty`, and
`/qa/ui/access-error` show three access states. The standalone preview uses the
root path. When `qa_ui: true` is set on a private, configured development
instance, the gallery uses that instance's `base_path` (for example
`/balda/qa/ui/`).

Use `/qa/ui/account-many` and its **Older active sessions** link to review
pagination. `/qa/ui/account-session-states` deliberately combines active,
ended, and expired synthetic rows to check that historical rows have no revoke
button; it is labeled as a state preview because production lists active rows
only. `/qa/ui/access-primary` and `/qa/ui/access-long` exercise primary-user
and long-content layouts. The `form-*` previews return actual 400, 403, 409,
and 500 HTTP statuses while rendering the production error templates. The
refresh error and conflict previews show the distinct recovery actions.

`/qa/ui/bindings-issued`, `/qa/ui/bindings-pending`,
`/qa/ui/bindings-unavailable`, and `/qa/ui/bindings-disabled` cover each of the
four configured channel panels, one-time actions, expiry, previous instance,
replacement/cancellation and recovery. Their visible invitation values are
intentionally invalid synthetic examples and cannot connect an account.

The fixture data is deterministic and synthetic. Preview links remain within
`/qa/ui/`. Forms can be inspected and focused, but submission cannot change
state: preview routes accept GET and HEAD only. The standalone server binds a
loopback address and serves no authenticated application endpoints. Stop it with
Ctrl-C. Do not expose this listener through an ingress.

Run the browser regression suite against the same synthetic gallery:

```bash
cd qa/backoffice-e2e
npm ci
npx playwright install chromium
npm test
```

The suite starts its own loopback preview on an ephemeral port. It checks every
gallery page and state at 1440×900, 1024×768, 768×1024 and 390×844. It checks
document overflow, consistent headings, dark authentication controls, native/HTMX
geometry, top-bar visibility and alignment during navigation and scrolling,
browser history and focus, sidebar collapse/overlay/backdrop/Escape,
short-page footer position, Audit details, recovery actions and no-script menus. It needs no application credentials or deployment access.

Run the separate authenticated browser gate from the repository root after
installing the same optional Playwright tooling:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test ./internal/apps/backoffice -run 'TestHTTPApp(Binding)?BrowserWorkflow' -count=1 -v
```

This gate starts the actual HTTP/security application against a temporary SQLite
database, creates synthetic accounts and uses the ordinary browser login with
and without JavaScript. It checks viewer identity on another user's detail,
conflict feedback without password reflection, access-token expiry recovery and
native logout. It does not access deployment data or bypass authentication.

The binding workflow additionally exercises four widths with and without
JavaScript: administrator issuance for an operator or non-primary administrator,
manual/clipboard copy, metadata-only history recovery, explicit replacement and
cancellation, confirmed attachment, replay denial, role preservation and refresh.
Slack admission uses the concrete receiver with a synthetic signed request.
Telegram, Zulip and Mattermost use isolated normalized proof fixtures at the
shared invitation port in this browser harness; their native verified ingress
and hydrated-history boundaries are separately tested in `handlersfx` and
`channel/*`. The harness opens no real transport accounts or production data.

## Review a page change

1. Add or update a synthetic view model for each new page state in
   `internal/apps/backoffice/qa.go`, then link it from the gallery. Use the same
   template and assets as the runtime page. Keep fixtures free of real user data,
   credentials, tokens, and customer identifiers.
2. Open the gallery in Playwright or Chrome and review every affected default,
   empty, validation-error, and generic-error state at **1440×900**, **1024×768**,
   **768×1024** and **390×844**. Check visual hierarchy, contrast, focus visibility, labels,
   form controls, sidebar behavior, horizontal overflow, and table containment.
   Verify that assets load and that browser console errors are absent. Follow
   navigation and tab through forms with the keyboard; the page should remain
   useful without JavaScript. Follow **Older active sessions** with JavaScript
   enabled and disabled. Request a `form-*` route as an HTMX fragment and check
   that its original error status and visible feedback survive.
3. Record the reviewed route/state and viewport sizes, observed defects and
   fixes, and the test result as a severity-ranked table in the Beads review
   comment. A screenshot is useful for
   layout changes. Capture only synthetic preview pages and keep passwords,
   cookies, and tokens out of logs, screenshots, and issue attachments.
4. Run `go test -race ./...` and `go tool golangci-lint run`. If ownership or
   package dependencies changed, also run
   `go tool go-arch-lint check --project-path .`.

The preview proves template and layout behavior with fixed view models. For a
deployment that changes Backoffice runtime behavior, also sign in through the
normal login on the protected deployment and check the affected pages at
1440×900 and 390×844, plus the ordinary form flow, with a suitable
administrator or operator account. Review authorization and actual data in that
environment, then sign out. Record the result separately from the synthetic
preview review; do not copy live values into fixtures or evidence. There is no
QA authentication bypass.

Production Backoffice continues to run under `balda start`. Keep `qa_ui: false`
there. See [Backoffice application](backoffice.md) for deployment, security, and
rendering contracts.

## Passkey UI and authenticated verification

The gallery includes `account-2fa-off`, `account-2fa-enabled`,
`account-2fa-unavailable`, `webauthn-register`, `webauthn-assert` and `step-up`.
They use synthetic public options, cannot authenticate and cannot mutate state.
Review status/forms, explicit replacement/removal confirmation, current-password
labels, Cancel, keyboard focus and narrow layouts at all four widths.

After installing the Playwright tooling above, run the real SQLite-backed,
loopback-only virtual-authenticator gate:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/backoffice -run TestHTTPAppWebAuthnBrowserWorkflow -count=1 -v
```

It uses `http://localhost` and both root and `/balda`, Chromium's CTAP2 virtual
authenticator with required user verification, and isolated test administrators.
Check enable, enrolled password-pending login, assertion, fresh/stale step-up,
replacement, disable, no-script and unsupported browsers, cancellation/retry,
non-boosted forms, no-store and history without ceremony caching. Capture browser
errors and document overflow at 1440×900, 1024×768, 768×1024 and 390×844. This gate
is authenticated evidence; synthetic QA alone is not a verifier test. Never point
the harness at a deployed service or real administrator credentials.

A stale sensitive POST returns 403 with a confirmation link. Verification must
not replay the POST. A lost key shows the offline recovery route; password reset
alone must not remove the factor. The default IP-origin Account screen explains
why enrollment is unavailable while password-only use continues.

## MCP inventory and editors

The gallery includes `mcp`, `mcp-empty`, `mcp/new`,
`mcp/connections/qa-worker`, `mcp/connections/config:qa-worker`, `mcp-probe`,
`mcp-invalid`, `mcp-conflict`, `mcp-retained`, `mcp-unavailable`, `mcp-public`,
`mcp-auth-required`, `mcp-revoked`, `mcp-stdio` and `mcp-saved-start-failed`.
These cover the inventory, tool availability, separate OAuth/recovery labels, native editors, write-only
binding controls, read-only configuration, candidate probing and actual error
statuses. Fixtures contain only synthetic identifiers and blank replacement
inputs. Preview forms cannot mutate application state.

Run the durable host integration browser gate with the same Playwright tools:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/balda -run TestBackofficeMCPBrowserWorkflow -count=1 -v
```

It starts the actual Backoffice runtime and security stack, the dedicated MCP
adapter, catalog, temporary SQLite and a loopback MCP SDK server. Ordinary
administrator/operator login verifies native and HTMX CRUD, protected value
retention, candidate probing without persistence, version conflicts, configured
read-only behavior, actual Back/Forward with write-only input checks, history restoration and logout at desktop and mobile, with
and without JavaScript. The native no-script forms also exercise keyboard
activation. It checks the resulting durable revisions and
tombstones. It does not use an authentication bypass or deployment credentials.


The MCP gallery also covers native callback continuation, pending browser
authorization, one-time device
instructions, safe device status, authorized/denied/expired/failed outcomes,
unsupported authorization and saved-grant attachment retry. Instructions contain
invalid synthetic values only. Review the native forms, cancellation and separate
authorization/availability labels at all four widths. On creation, select HTTP
or SSE and open **Client settings — optional** with mouse and keyboard. The
creation form submits natively, while saved definition edits retain HTMX.
Stdio hides the remote OAuth controls with JavaScript; its ordinary command,
arguments and environment remain available. Without JavaScript, native details
and transport applicability instructions remain usable.

Run the native OAuth integration gate:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/balda -run '^TestBackofficeMCPOAuthBrowserWorkflow$' -count=1 -v
```

It uses ordinary login, the real host owners and temporary SQLite with controlled
OAuth and HTTP/SSE MCP servers on a different browser site from Backoffice. The
matrix covers configured public servers without a recovery prerequisite,
successful managed create-and-authorize, saved creation followed by a discovery
failure and retry without duplicates, and authorization of a previously static
managed server. It exercises optional browser client registration, confidential
client settings, and account/password entry at the external issuer only.
It also covers browser/device flows, the native callback continuation with Strict
session cookies, root and
`/balda`, desktop/mobile and JavaScript enabled/disabled, denial/cancellation,
saved-grant attachment retry, changed/empty scopes, changed-file recapture and
actual hosted/ACP tool execution. Historical revision payloads remain unchanged.
Narrowed shared grants fail incompatible retained scopes closed; restoring
authorization permits the original snapshot to invoke tools again. Existing
stdio subprocess/environment and hosted/ACP invocation coverage remains in
`TestStdioDiscoveryAndActualProvidersUseHostEnvironmentAndOverlay`.
Its private protocol inputs are excluded from diagnostics; no real
administrator, deployment or transport credentials are used. Synthetic gallery
results remain separate from this authenticated runtime evidence.
