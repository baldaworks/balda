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
geometry, browser history and focus, sidebar collapse/overlay/backdrop/Escape,
short-page footer position, Audit details, recovery actions and no-script menus. It needs no application credentials or deployment access.

Run the separate authenticated browser gate from the repository root after
installing the same optional Playwright tooling:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test ./internal/apps/backoffice -run TestHTTPAppBrowserWorkflow -count=1 -v
```

This gate starts the actual HTTP/security application against a temporary SQLite
database, creates synthetic accounts and uses the ordinary browser login with
and without JavaScript. It checks viewer identity on another user's detail,
conflict feedback without password reflection, access-token expiry recovery and
native logout. It does not access deployment data or bypass authentication.

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
