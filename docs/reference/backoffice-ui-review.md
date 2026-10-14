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
instance, the gallery uses that instance's Backoffice mount path (for example
`/balda/backoffice/qa/ui/`).

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
short-page footer position, grouped navigation, Audit details, and recovery actions.
It needs no application credentials or deployment access.

Run the separate authenticated browser gate from the repository root after
installing the same optional Playwright tooling:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test ./internal/apps/backoffice -run 'TestHTTPApp(Binding)?BrowserWorkflow' -count=1 -v
```

This gate starts the actual HTTP/security application against a temporary SQLite
database, creates synthetic accounts and uses the ordinary JavaScript-enabled browser login. It checks viewer identity on another user's detail,
conflict feedback without password reflection, access-token expiry recovery and
native logout. It does not access deployment data or bypass authentication.

The binding workflow additionally exercises four widths: administrator issuance for an operator or non-primary administrator,
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
   For navigation changes, check Workspace, Operations and Administration in the
   administrator sidebar, omit empty groups for operators, and verify Account
   appears only in the top bar. Check the active sidebar link on nested Access,
   Plugins, MCP, Schedules, Webhooks, Aliases, and Fact memory pages after both HTMX navigation and browser history changes.
   Verify that assets load and that browser console errors are absent. Follow
   navigation and tab through forms with the keyboard. Follow **Older active sessions**
   in the browser. Request a `form-*` route as an HTMX fragment and check
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

## Webhooks layout and application gates

Review Webhooks in two ordered browser gates. First run the synthetic gallery
suite with `npm test` in `qa/backoffice-e2e`. Its Webhooks fixtures reuse the
production templates and assets for inventory, empty inventory, managed create
and edit, config read-only detail, disabled and archived details, populated and
empty history, one-time secret, and 400/409/503 errors. Fixtures use visibly
invalid values and cannot accept POSTs. At 1440×900, 1024×768, 768×1024 and
390×844, inspect active Operations navigation, **Report to** labels in the
form and grid, keyboard focus, placeholder contrast, dedupe-header visibility,
asset and console errors, document overflow, and horizontal scrolling within
the table. Record state/viewport results and synthetic screenshots in the
Beads review comment. Repair and rerun this layout gate before starting the
authenticated application gate.

After the gallery passes, run the shared-listener layout proof before any
application E2E. It checks canonical URLs for config and managed routes,
readonly URL fields, valid/invalid slug live preview, optional public origin,
empty and `/balda` prefixes, and desktop/mobile layout. The following optional
screenshot variable captures list, config detail, valid create preview and
saved managed detail after the one-time secret is cleared:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_WEBHOOKS_LAYOUT_SCREENSHOTS=/tmp/balda-webhook-slug-screens/layout go test ./internal/apps/balda -run '^TestSharedHTTPLayoutBrowser$' -count=1 -v
```

After both layout checks pass, run the isolated application gate from the
repository root with the same Playwright installation:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_WEBHOOKS_RUNTIME_SCREENSHOTS=/tmp/balda-webhook-slug-screens/application go test -race ./internal/apps/balda -run '^(TestSharedHTTPApplicationBrowser|TestBackofficeWebhooksBrowserWorkflow|TestWebhookActorRuntimeFromHTTP)$' -count=1 -v
```

The browser workflow signs in normally as a synthetic administrator against
loopback Backoffice, webhook receiver and temporary SQLite. It covers root and
`/balda`, desktop and mobile, config and managed inventory, route lifecycle,
one-time secret and rotation, external authentication and rejection, config and
managed Test POST, history/pagination/archive, and new admission after a fresh
receiver and database reopen. The shared-listener workflow changes the prefix
from `/balda` to empty on restart, then checks the new canonical config/managed
URLs, unchanged secrets, and preserved initial input/output history. No route
has an editable path; each uses `<base_path>/webhooks/<name>`. Without explicit
`balda.http.base_url`, the UI shows the canonical path and its missing-origin
explanation rather than advertising the browser's authentication origin.
Its deterministic publisher writes completed
job and delivery fixtures to keep UI checks isolated; those browser rows alone
do not prove actor execution. The paired actor test sends an HTTP POST through
the real receiver, durable bus, JobActor, private SessionActor turn with a
deterministic provider, DeliveryActor and transport, then checks persisted
output, frozen recipient and sent delivery. PostgreSQL migration/store behavior
is verified separately in state integration tests. Use only synthetic values
and local temporary state; never point this gate at a deployment or capture
the one-time secret in screenshots. Record its evidence separately from the
layout gate.

## Fact memory layout and application gates

Run the synthetic layout gate before the authenticated application gate. Both
require the optional Playwright installation described above and use only
isolated loopback listeners and synthetic facts; neither targets a deployment.

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_FACT_MEMORY_LAYOUT_SCREENSHOTS=/tmp/balda-fact-memory-screens/layout go test ./internal/apps/backoffice -run '^TestFactMemoryLayoutBrowser$' -count=1 -v
```

The gallery fixtures use production templates/assets for inventory, long and
multiline facts, search/no matches, empty/disabled, add/edit/delete confirmation,
and 400/403/404/409/503 responses. Review root and `/balda/backoffice` at
1440×900, 1024×768, 768×1024 and 390×844. The browser checks full/HTMX shell and
statuses, prefixed assets, **Fact** form/grid labels, Operations navigation,
escaping, wrapping/overflow, distinct placeholder colors, keyboard focus and
mobile sidebar. It also navigates to a nested editor through HTMX, renders an
actual 409 fragment with retained text/version, and follows **Reopen this fact**.
Inspect the actual desktop/mobile screenshots, including
`<base>/<viewport>/memory-long.png` and `memory-conflict-htmx.png`; passing HTTP
or markup tests alone is not layout evidence. Fix and rerun this gate before
starting application verification.

After independent layout acceptance, run:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_FACT_MEMORY_APPLICATION_SCREENSHOTS=/tmp/balda-fact-memory-screens/application go test -race ./internal/apps/backoffice -run '^TestFactMemoryApplicationBrowser$' -count=1 -v
go test -race ./internal/apps/balda/sessionturnapp -run '^TestGlobalFactChangesRefreshActiveAndRestoredSession$' -count=1 -v
go test -race ./internal/apps/balda -run '^TestMemorySnapshotReaderPreservesCurrentBoundary$' -count=1 -v
```

The application workflow signs in normally as synthetic administrator/operator
accounts against real temporary SQLite. It covers root/prefixed desktop/mobile
CRUD/search, a complete multiline file imported as one fact, validation,
confirmation/cancel, actual MCP and MemoryActor appends to the UI's shared Store,
stale editor recovery, CSRF/origin/operator/revoked-session denials, safe backend
failure and a genuinely disabled memory service. It asserts persisted versions,
body-free audit, initialized final-empty state and no reimport after reopening
the Store. Failure injection and writer triggers exist only in the test harness.
Screenshots contain synthetic facts, never login credentials or tokens.

The paired queued-turn test uses a deterministic ADK provider/session fixture to
prove next-turn create/edit/final-delete refresh, lazy restore, unchanged empty
cursor behavior and retained history. The production adapter regression uses
real SQLite to prove content/version/timestamp forwarding for nonempty and
initialized empty records. These are separate runtime proofs, not claims that
the browser drives a live assistant or external transport. PostgreSQL/SQLite
guarded authority and persistence contracts run separately under the database
integration gates.

Record exact commands, current logs, layout-before-application timestamps,
screenshots and independent findings in Beads. The feature's initial evidence is
tracked in `balda-foyr.6` (layout), `balda-foyr.7` (application/runtime) and
`balda-foyr.2` (both persistence providers). Re-run affected ordered gates after
repairs; old screenshots do not prove a changed implementation.

## Passkey UI and authenticated verification

The gallery includes `account-2fa-off`, `account-2fa-enabled`,
`account-2fa-unavailable`, `webauthn-register` and `webauthn-assert`.
They use synthetic public options, cannot authenticate and cannot mutate state.
Review status/forms, explicit password-confirmed replacement/removal, current-password
labels, Cancel, keyboard focus and narrow layouts at all four widths.

After installing the Playwright tooling above, run the real SQLite-backed,
loopback-only virtual-authenticator gate:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/backoffice -run TestHTTPAppWebAuthnBrowserWorkflow -count=1 -v
```

It uses `http://localhost` and both root and `/balda`, Chromium's CTAP2 virtual
authenticator with required user verification, and isolated test administrators.
Check enable, enrolled password-pending login, assertion, continued access after refresh,
replacement, disable, unsupported browsers, cancellation/retry,
non-boosted forms, no-store and history without ceremony caching. Capture browser
errors and document overflow at 1440×900, 1024×768, 768×1024 and 390×844. This gate
is authenticated evidence; synthetic QA alone is not a verifier test. Never point
the harness at a deployed service or real administrator credentials.

A valid refreshed session completes ordinary sensitive actions without another
passkey prompt. A lost key shows the offline recovery route; password reset
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
read-only behavior, actual Back/Forward with write-only input checks, history restoration and logout at desktop and mobile. Native forms also exercise keyboard
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

## Plugins layout and application gates

Run Plugins verification in this order, using the same optional Playwright
installation described above. First run the synthetic layout gate:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_PLUGINS_LAYOUT_SCREENSHOTS=/tmp/balda-plugins-screens/layout go test ./internal/apps/backoffice -run '^TestPluginsLayoutBrowser$' -count=1 -v
```

It checks 21 states at 1440×900, 1024×768, 768×1024 and 390×844, at the root
and `/balda/backoffice` mounts. Inspect installed/marketplace inventory,
Name/Source/Ref/Sparse Add, discovery, install/upgrade review, safe contributions,
retained rollback, confirmations, empty/long content, validation, conflict,
failed discovery and pending activation. The gate checks actual assets,
placeholder contrast, table scrolling, keyboard/focus, single-main HTMX
rendering, private history and settled nested Plugins navigation. Captures are
under the selected directory by mount/viewport. These fixed preview models
cannot mutate an application.

After accepting the layout and screens, run the real owner/session contracts:

```bash
go test -race ./internal/apps/balda -run '^TestBackofficePluginsRuntimeContracts$' -count=1 -v
```

This uses the production shared Fx plugin owner, catalog and session
create/restore/reset composition, real SQLite, isolated local/file-Git packages
and a loopback MCP SDK server. It verifies exact retained commands, lazy skill
bytes, MCP descriptors and actual tools after upgrade/disable/remove and
database/process-owner reopen. Ordinary Reset selects the current catalog.
Fault proofs distinguish preparation from durable acceptance and recovery;
no live provider, transport account or deployment is involved.

Then run the authenticated application browser gate:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_PLUGINS_RUNTIME_SCREENSHOTS=/tmp/balda-plugins-screens/application go test -race ./internal/apps/balda -run '^TestBackofficePluginsBrowserWorkflow$' -count=1 -v
```

Four cases cover root and `/balda/backoffice` at 1440×900 and 390×844, using
local and deterministic Git sources. Normal login exercises administrator and
operator authorization, anonymous entry, actual access rotation returning to
Plugins without step-up, CSRF/origin rejection, cleared/redacted Source and
escaped descriptions. Browser actions cover Add/Refresh/install/inspect/upgrade,
enable/disable, exact retained rollback, confirmed removal and valid names
`install`, `marketplaces` and `new`. Lifecycle checkpoints compare real SQL
and current catalog contributions while a real older session keeps its pin.

Verify contextual errors after `htmx:afterSettle`: 400 clears Source; 409 exposes
the exact reopen link; 503 distinguishes audit rollback from accepted pending
state and suppresses replay; revocation while waiting on the real catalog gate
returns 403 without changing durable state or the application snapshot. Failed
Refresh shows last-attempt failure separately from stale successful discovery,
and candidate review recovers only after successful Refresh.

Application captures use names such as `root-390-installed-v1.png`,
`prefixed-390-accepted-pending.png`, `prefixed-390-failed-refresh.png` and
`prefixed-1440-upgrade-review.png`. The harness captures only after Source is
cleared and outside credential forms. Keep timestamps, actual commands/results,
reviewed screenshots and defect/reopen evidence in Beads. If an implementation
defect changes rendered behavior, repair its owning task and rerun the affected
layout gate before the application gate. No SSH/asus or live data is used.

## Agent hooks layout and application gates

First run `BALDA_BACKOFFICE_BROWSER_TEST=1 go test ./internal/apps/backoffice
-run "^TestHooksLayoutBrowser$" -count=1 -v`. It checks inventory, empty and
long content, create/edit, validation, conflict and unavailable states at
1440×900, 1024×768, 768×1024 and 390×844, with root and prefixed mounts.
Screenshots are written under `BALDA_HOOKS_LAYOUT_SCREENSHOTS` or
`/tmp/balda-hooks-screens/layout`. Review placeholder contrast, required labels,
keyboard focus, private history, fragments, active navigation and overflow.

Then run `BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/backoffice
-run "^TestHooksApplicationBrowser$" -count=1 -v` against isolated SQLite and
ordinary synthetic administrator/operator login. It proves actual definitions,
execution, disablement, conflicts, deletion, authorization and persistence.
Record both reviews and their screenshot findings in Beads.
