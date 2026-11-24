# Backoffice UI review

Use the embedded, synthetic preview gallery while developing Backoffice pages.
It runs without a Balda configuration, database, administrator password, or bot
transport. The preview reuses the production Go templates and local assets.

## Start a local preview

From the repository root, run:

```bash
go run ./cmd/balda backoffice qa serve --listen 127.0.0.1:8096
```

Open `http://127.0.0.1:8096/qa/ui/`. Review the shared foundation first: `/qa/ui/style-guide` covers the shared
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

Run the browser regression suite against the same synthetic gallery from the
repository root:

```bash
task backoffice-e2e
```

The task uses `scripts/qa/run-backoffice-e2e.sh` to install the lockfile-pinned
Chromium build together with Playwright's supported Linux libraries and fonts,
then runs the complete suite. In an already provisioned Linux image, use
`./scripts/qa/run-backoffice-e2e.sh --no-system-install`. Logs are retained under
`${BALDA_BACKOFFICE_E2E_ARTIFACTS:-${TMPDIR:-/tmp}/balda-backoffice-e2e}`. Set
`BALDA_BACKOFFICE_E2E_SCREENSHOTS=1` when full-page screenshots are useful.
GitHub Actions runs the same script on a clean Ubuntu runner and uploads its
diagnostic directory when the job fails.

The suite starts its own loopback preview on an ephemeral port. It checks every
gallery page and state at 1440×900, 1024×768, 768×1024 and 390×844. It checks
document overflow, consistent headings, light authentication controls, native/HTMX
geometry, top-bar visibility and alignment during navigation and scrolling,
browser history and focus, sidebar collapse/overlay/backdrop/Escape,
short-page footer position, grouped navigation, Audit details, and recovery actions.
It needs no application credentials or deployment access.

For the Providers surface, run its focused production-template gate:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 go test ./internal/apps/backoffice -run '^TestProvidersLayoutBrowser$' -count=1 -v
```

It covers inventory, empty, create, managed, configured read-only, and conflict
states at all four required viewports, for root and prefixed mounts. It also
checks full/fragment/history rendering, keyboard focus, contained mobile table
overflow, local assets, console failures, and secret-free HTML/screenshots.

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

## Authoritative production-form audit

The production contract contains **66 production forms in 17 families**. The
machine-checked source is
`internal/apps/backoffice/testdata/form-inventory.tsv`; route ownership is in
`form-route-inventory.tsv`, authenticated/template evidence is in
`form-evidence.tsv`, and exact state, field-classification, plus regression-source digests are in
`form-contract.tsv`. The gate probes every recorded method/path against the
production router, reconciles handler owners, and fails when a field or material
state changes without an explicit contract update. QA-only forms are listed
with reviewed reasons in `form-exclusions.tsv`; there is no basename-only silent
exclusion. Stable IDs are grouped below so a screen or conditional
variant cannot disappear behind a page-family summary.

| Family | Stable production form IDs |
| --- | --- |
| Access | `access-create`, `access-user-update`, `access-binding-delete`, `access-credential-reset`, `access-filter` |
| Account | `account-password`, `account-passkey-replace`, `account-passkey-disable`, `account-passkey-enable` |
| Aliases | `alias-editor`, `alias-delete` |
| Audit | `audit-filter` |
| Authentication | `shell-logout`, `login-password`, `forced-password-replace`, `forced-password-logout`, `session-refresh`, `webauthn-finish` |
| Bindings | `binding-invitation-cancel`, `binding-identity-refresh`, `binding-invitation-issue` |
| Chat archive | `chat-archive-filter` |
| Fact memory | `memory-filter`, `memory-editor`, `memory-delete` |
| Hooks | `hook-editor`, `hook-delete` |
| MCP | `mcp-diagnostic`, `mcp-editor`, `mcp-selection`, `mcp-delete`, `mcp-oauth-attempt-cancel`, `mcp-oauth-start`, `mcp-oauth-retry`, `mcp-oauth-disconnect`, `mcp-device-cancel` |
| Plugin marketplaces | `plugin-marketplace-create`, `plugin-marketplace-refresh`, `plugin-marketplace-remove` |
| Plugins | `plugin-install-filter`, `plugin-review-accept`, `plugin-selection`, `plugin-rollback`, `plugin-remove` |
| Providers | `provider-type-select`, `provider-editor`, `provider-selection`, `provider-delete` |
| Schedules | `schedule-editor`, `schedule-selection`, `schedule-delete`, `schedule-run-now` |
| Sessions | `session-revoke` |
| Skills | `skill-retry-publication`, `skill-refresh`, `skill-review-install`, `skill-accept`, `skill-review-update`, `skill-disable`, `skill-enable`, `skill-remove` |
| Webhooks | `webhook-editor`, `webhook-selection`, `webhook-rotate-secret`, `webhook-delete`, `webhook-test` |

The completed audit records **37 findings: 13 high and 24 medium, all closed**
in `form-findings.tsv`. Each row names the affected stable form IDs, repair
location, Prism child task, and resolving regression. A missing route owner,
evidence reference, state dimension, field classification, stale form ID, or
open finding fails `TestProductionFormInventoryBaseline`; documentation drift
fails `TestProductionFormAuditDocumentationIsCurrent`.

When adding or changing a form, update all six TSV contracts (inventory,
routes, evidence, state/field contract, exclusions, and findings) in the same
change, classify every named field and material state, and add authenticated
evidence. Record each discovered defect even when it is fixed immediately.
Do not mark a finding closed until its named regression passes. Page-family
matrix cells may use N/A only for a product lifecycle that does not exist; an
N/A never exempts a rendered `<form>` from the production inventory.

## Maintained page-family state matrix

This matrix is the inventory for the common gallery. `G:<route>` identifies a
deterministic synthetic fixture rendered by the production template. `A:<gate>`
identifies an isolated authenticated application gate. A Covered cell must keep
at least one of those identifiers; removing or renaming a fixture requires
updating the matrix and the browser inventory assertion in the same change.
All fixtures use synthetic values and must not contain deployment credentials,
cookies, tokens, real message bodies, or capability-bearing URLs.

| Page family | Inventory / empty | Create | Detail / edit | Validation / conflict / unavailable | Delete / lifecycle / history | Interaction evidence |
| --- | --- | --- | --- | --- | --- | --- |
| Authentication | Covered — `G:login`, `G:refresh` | N/A — authentication creates no product resource | Covered — `G:password`, `G:webauthn-register`, `G:webauthn-assert` | Covered — `G:login-error`, `G:refresh-error`, `G:refresh-conflict`, `G:password-error` | Covered — session recovery in `G:refresh`, `G:refresh-error`, `G:refresh-conflict`; resource deletion is N/A | `A:TestHTTPAppBrowserWorkflow`, `A:TestHTTPAppWebAuthnBrowserWorkflow` |
| Overview | Covered — `G:overview`, `G:overview-empty` | N/A — read-only aggregate | N/A — read-only aggregate | Covered — shared `G:error` recovery contract | N/A — no owned lifecycle | `A:TestHTTPAppBrowserWorkflow` |
| Agent / bindings | Covered — `G:agent`, `G:access` | Covered — `G:bindings-issued` is the one-time invitation result | Covered — `G:bindings-pending` | Covered — `G:bindings-unavailable`, `G:bindings-disabled` | Covered — pending/expired invitation lifecycle in `G:bindings-pending`; bindings are removed from Access detail | `A:TestHTTPAppBindingBrowserWorkflow` |
| Chat sessions | Covered — `G:chat-sessions`, `G:chat-sessions-empty` | N/A — passive archive never creates a runtime | Covered — `G:chat-sessions/after`, `G:chat-sessions/group` | Covered — `A:TestChatArchiveHTTPAuthority`, `A:TestChatArchiveHTTPTranscript` assert safe error handling | Covered — `G:chat-sessions/before`, `G:chat-sessions/after`, `G:chat-sessions/closed`; deletion is unsupported by product contract | `A:TestChatArchiveLayoutBrowser`, `A:TestChatArchiveHTTPTranscript` |
| Plugins | Covered — `G:plugins-installed`, `G:plugins/marketplaces`, `G:plugins-marketplaces-empty` | Covered — `G:plugins/marketplaces/new`, `G:plugins/install` | Covered — `G:plugins/installed/demo`, `G:plugins/installed/demo/upgrade` | Covered — `G:plugins-marketplace-invalid`, `G:plugins-marketplace-conflict`, `G:plugins-marketplace-unavailable`, `G:plugins-conflict`, `G:plugins-unavailable` | Covered — `G:plugins-disabled`, `G:plugins-degraded`, `G:plugins-pending-remove` and retained revisions on detail | `A:TestPluginsLayoutBrowser`, `A:TestBackofficePluginsBrowserWorkflow` |
| Skills | Covered — `G:skills`, `G:skills-empty` | Covered — `G:skills/install`, `G:skills-review` | Covered — `G:skills/managed/managed-synthetic`, `G:skills-update` | Covered — `G:skills-invalid`, `G:skills-conflict`, `G:skills-unavailable` | Covered — `G:skills-disabled`, `G:skills-pending`, retained revisions on managed detail | `A:TestSkillsLayoutBrowser`, `A:TestBackofficeSkillsBrowserWorkflow` |
| MCP | Covered — `G:mcp`, `G:mcp-empty` | Covered — `G:mcp/new` | Covered — `G:mcp/connections/qa-worker`, `G:mcp/connections/config:qa-worker` | Covered — `G:mcp-invalid`, `G:mcp-conflict`, `G:mcp-unavailable`, authorization/device recovery fixtures | Covered — `G:mcp-retained`; active/deleted/all application evidence in `A:TestBackofficeMCPBrowserWorkflow` | `A:TestBackofficeMCPBrowserWorkflow`, `A:TestBackofficeMCPOAuthBrowserWorkflow` |
| Schedules | Covered — `G:schedules`, `G:schedules-empty`, `G:schedules-deleted`, `G:schedules-all` | Covered — `G:schedules/new` | Covered — `G:schedules/daily-summary`, `G:schedules/deleted-summary`, `G:schedules-config`, `G:schedules-long` | Covered — `G:schedules-invalid`, `G:schedules-conflict`, `G:schedules-unavailable` | Covered — `G:schedules-disabled`, `G:schedules-deleted`, `G:schedules-all`, `G:schedules/deleted-summary`, `G:schedules-history` | `A:TestSchedulesAliasesLayoutBrowser`, `A:TestBackofficeSchedulesBrowserWorkflow` |
| Webhooks | Covered — `G:webhooks`, `G:webhooks-empty` | Covered — `G:webhooks/new`, one-time `G:webhooks-secret` | Covered — `G:webhooks-managed`, `G:webhooks-config` | Covered — `G:webhooks-invalid`, `G:webhooks-conflict`, `G:webhooks-unavailable` | Covered — `G:webhooks-disabled`, `G:webhooks-archived`, `G:webhooks-history`, `G:webhooks-history-empty` | common gallery Webhooks gate, `A:TestBackofficeWebhooksBrowserWorkflow` |
| Aliases | Covered — `G:aliases`, `G:aliases-empty` | Covered — `G:aliases/new` | Covered — `G:aliases/main_chat`, `G:aliases-long` | Covered — `G:aliases-invalid`, `G:aliases-conflict`, `G:aliases-unavailable` | Covered — delete confirmation on `G:aliases/main_chat`; deleted rows/history are N/A because deletion removes the mapping and recreation is allowed with a newer version | `A:TestSchedulesAliasesLayoutBrowser`, `A:TestAliasesBrowserWorkflow` |
| Agent hooks | Covered — `G:hooks`, `G:hooks-empty` | Covered — `G:hooks/new` | Covered — `G:hooks/guard`, `G:hooks-long` | Covered — `G:hooks-invalid`, `G:hooks-conflict`, `G:hooks-unavailable`, `G:hooks-unsupported` | Covered — enable/disable/delete in `A:TestHooksApplicationBrowser`; historical execution rows are not a Backoffice product surface | `A:TestHooksLayoutBrowser`, `A:TestHooksApplicationBrowser` |
| Fact memory | Covered — `G:memory`, `G:memory-empty`, search fixtures | Covered — `G:memory/new` | Covered — `G:memory/1`, `G:memory-long` | Covered — `G:memory-invalid`, `G:memory-forbidden`, `G:memory-missing`, `G:memory-conflict`, `G:memory-unavailable` | Covered — `G:memory-delete`, `G:memory-disabled`; per-fact deleted history is N/A by product contract | `A:TestFactMemoryLayoutBrowser`, `A:TestFactMemoryApplicationBrowser` |
| Providers | Covered — `G:providers`, `G:providers-empty` | Covered — `G:providers/new` | Covered — `G:providers/managed`, `G:providers/configured` | Covered — `G:providers-conflict`; unavailable behavior in `A:TestProvidersBrowserManagementAndSecretBoundary` | Covered — managed enable/disable/delete and configured reconciliation in `A:TestProvidersBrowserManagementAndSecretBoundary` | `A:TestProvidersLayoutBrowser`, `A:TestProvidersBrowserManagementAndSecretBoundary` |
| Access | Covered — `G:access-list`, `G:access-empty` | Covered — `G:access-create` | Covered — `G:access`, `G:access-primary`, `G:access-long` | Covered — `G:access-error`, `G:form-bad-request`, `G:form-forbidden`, `G:form-conflict`, `G:form-server-error` | Covered — user/session disable/revoke state in `G:access`, `G:account-session-states`; hard user deletion is unsupported | `A:TestHTTPAppAccessAdministrationNoJSHTMXAndRoleBoundary` |
| Audit | Covered — `G:audit`, `G:audit-empty` | N/A — append-only system evidence has no UI create | Covered — expandable row detail in `G:audit` | Covered — contextual filter recovery in `A:TestHTTPAppAuditFilteringPaginationRedactionAndRoleBoundary` | N/A — immutable audit events cannot be edited or deleted | `A:TestHTTPAppAuditFilteringPaginationRedactionAndRoleBoundary` |
| Account | Covered — `G:account`, `G:account-empty`, `G:account-many`, `G:account-many-next` | N/A — the current account already exists | Covered — `G:account-2fa-off`, `G:account-2fa-enabled`, `G:account-2fa-unavailable` | Covered — `G:account-error`, `G:password-error`, `G:webauthn-register`, `G:webauthn-assert` | Covered — `G:account-session-states`, `G:account-many`, `G:account-many-next` | `A:TestHTTPAppAccountRotationAndCurrentFamilyRevocation`, `A:TestHTTPAppWebAuthnBrowserWorkflow` |

Schedules and Aliases have a focused layout gate because their authenticated
workflows use mutable isolated state. The gallery gate owns deterministic
layout, fragment, history, focus, keyboard, confirmation and recovery evidence
at 1440×900, 1024×768, 768×1024 and 390×844 under both the root and prefixed
mounts. Authenticated gates continue to own authorization and durable mutation
semantics.

## Shared lifecycle and interaction review

For Providers, MCP, Schedules, and Webhooks, review **Active**, **Deleted**, and
**All** as distinct production states. Confirm that the selected lifecycle view
survives detail, create, HTMX, Back/Forward, and error navigation. Deleted
details must remain passive and expose only the safe retained metadata and
feature history described in the Backoffice reference. Recreate a deleted
managed identifier in the authenticated gate and verify that it advances the
generation, preserves historical evidence, does not inherit protected state,
and accepts only replacement input required by the new definition. A recreated
Webhook must issue a new secret. A stale pre-delete form must return 409. Also
verify that active and configured names,
including retained configured records, remain reserved.

Mutation feedback and table scrolling are presentation contracts, not domain
state. Pending HTMX mutations must expose `aria-busy`, a visible live status,
disabled submit controls, and duplicate-submit suppression, then settle on
success, error, replacement, and history restoration. Native submissions keep
their ordinary navigation. An overflowing table must show its server-rendered
scroll cue, become keyboard focusable, expose the cue through
`aria-describedby`, reach its rightmost column, and remain contained within the
page. A non-overflowing table must hide the cue and have no added tab stop or
scroll announcement.

Run the evidence in this order from the repository root. A later gate does not
replace an earlier one:

1. Validate the maintained matrix and production-template invariants.

   ```bash
   go test -race ./internal/apps/backoffice -run '^TestQAStateMatrixEvidenceIsCurrent$' -count=1
   go test -race ./internal/apps/backoffice/internal/webui -run '^TestHTMXMutationFormsUseSharedAccessibleStatus$' -count=1
   ```

2. Run the complete synthetic production-template gallery at all four
   viewports.

   ```bash
   task backoffice-e2e
   ```

3. Run the focused lifecycle, mutation, and mobile-table layout gates at both
   mounts. In an already provisioned browser image,
   `BALDA_BACKOFFICE_BROWSER_TEST=1` uses the lockfile-pinned Playwright browser.

   ```bash
   BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/backoffice -run '^(TestSchedulesAliasesLayoutBrowser|TestMobileTablesLayoutBrowser|TestMutationFeedbackBrowser)$' -count=1 -v
   ```

4. Only after the synthetic gates pass, run the authenticated application
   workflows against disposable state and normal login.

   ```bash
   BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/backoffice -run '^(TestAliasesBrowserWorkflow|TestProvidersBrowserManagementAndSecretBoundary)$' -count=1 -v
   BALDA_BACKOFFICE_BROWSER_TEST=1 go test -race ./internal/apps/balda -run '^(TestBackofficeMCPBrowserWorkflow|TestBackofficeSchedulesBrowserWorkflow|TestBackofficeWebhooksBrowserWorkflow)$' -count=1 -v
   ```

5. Finish with the affected service/store contracts and repository-required
   gates. Keep SQLite and PostgreSQL results separate when tagged integration
   jobs run; browser evidence does not replace either backend contract.

   ```bash
   go test -race ./internal/apps/balda/mcpmanage ./internal/apps/balda/providers ./internal/apps/balda/scheduledjobs ./internal/apps/balda/webhookmanagement ./internal/apps/balda/state
   go test -race -tags=integration,sqlite ./internal/apps/balda/state
   BALDA_TEST_POSTGRES_DSN='<disposable-postgres-dsn>' go test -race -tags=integration,postgres ./internal/apps/balda/state

   go test -race ./...
   go tool golangci-lint run
   go tool go-arch-lint check --project-path .
   git diff --check
   bd lint
   ```

Record synthetic and authenticated results separately. Use only reserved
example domains, invalid invitation values, temporary databases, deterministic
providers, and synthetic locators. Never capture passwords, cookies, tokens,
one-time Webhook secrets, deployment topology, prompt bodies, or live message
content.

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

## Lifecycle test rationalization map

The lifecycle audit below is intentionally limited to Backoffice and the MCP,
Provider, Schedule, and Webhook Core suites. A similar feature name does not
make evidence redundant when the layer, backend, authority boundary, browser
mount, viewport, history behavior, or failure signal differs.

The mechanically checked audit boundary scans every `*_test.go` below the nine
package directories enumerated by `rationalizationPackageDirectories` in
`internal/apps/backoffice/test_rationalization_test.go`, independent of a
hand-maintained filename list. Its checked-in inventory records all 430 valid
top-level Go tests, including build-tagged backend tests, with exactly one
resolved classification. The 80 direct Story candidates/owners have exact
per-test classifications; every remaining test has an explicit contract-based
protected or out-of-scope category. `TestLifecycleRationalizationInventoryIsComplete`
fails on an unmatched or overlapping rule, a new file/test, a missing owner,
wrong test signature, duplicate backend owner, changed exact count, or a
SQLite/PostgreSQL entrypoint/subtest chain that no longer calls the shared
contract runner. The 335/336 default-build counts below are execution inventory,
not the source-level audit boundary.

| Candidate | Contract and layer | Fixture, stimulus, and assertions | Unique failure signal | Decision and retained owner |
| --- | --- | --- | --- | --- |
| Removed-field checks inside `TestSchedulesBrowserManagement` | No supported contract; Backoffice handler/template blacklist for obsolete form fields | `schedulesHTTPFixture`; POST create with `report_to_key` and negative template checks for `target_kind`/`report_to_key` | None: they only named unsupported surfaces and duplicated the current positive form boundary | Remove the request and name-blacklist checks. The positive assertion now requires exactly one current `locator` field; current parsing, validation, CSRF/origin rejection, and owner dispatch remain in the same test. |
| `TestProviderLifecycleFiltersPassiveDetailAndRecreate` | Provider service lifecycle policy | In-memory provider store; delete, filter/detail, recreate, stale update/delete; asserts passive reads, new revision, retained pin, and no inherited environment | Service policy and retained environment-backed revision | Retain. Store atomicity remains separately owned by `TestSQLiteProviderContract` and `TestPostgresProviderContract`. |
| `TestRecreateDoesNotInheritAPIKey` | Provider service protected-field policy | Hosted provider with API key; delete and recreate without replacement key; asserts public revision and compiled config are empty | API-key handling is distinct from environment bindings | Retain; it is not covered by the environment case above. |
| `TestCreateRejectsConfiguredAndActiveProviderCollisions` | Provider service namespace policy | Configured and active managed IDs; create attempts; asserts conflict before replacement | Distinguishes configured reservation from active managed collision | Retain. |
| `TestActiveConfiguredManagedCollisionIsInvalid` | Provider service reconciliation failure boundary | Corrupt mixed-source in-memory state; inventory/detail/create; asserts invalid/conflict outcomes | Active split-brain state must fail closed | Retain; a deleted reconciliation tombstone is intentionally handled differently. |
| `TestConfiguredProviderOwnsDeletedManagedReconciliationTombstone` | Provider service deleted reconciliation policy | Configured provider plus deleted managed record; filtered reads/detail/create; asserts one configured projection and reserved name | Deleted tombstone reconciliation rather than active collision | Retain. |
| `TestDeletedDefinitionInventoryDetailAndRecreate` | MCP service lifecycle policy | Definition harness; delete/filter/detail/recreate/stale edit; asserts new generation and retained protected revision | Policy projection and immutable pin preservation | Retain. Adapter call shape remains in `TestOperationsPersistProtectedEditsAndProbeExactCandidate`; handler shape in `TestMCPInventoryUsesSafeSharedRendering`; authenticated interaction in `TestBackofficeMCPBrowserWorkflow`. |
| `TestSQLiteProviderContract/MCPConcurrentRecreateRetainsHistory` and `TestPostgresProviderContract/MCPConcurrentRecreateRetainsHistory` | MCP SQL atomicity and backend parity | Isolated SQLite/PostgreSQL contract databases; concurrent recreate; asserts one winner, conflict, history, and audit | Backend transaction/fencing rather than service projection | Retain both concrete backend cases. |
| `TestSQLiteProviderContract/Provider_ManagedProviderState` and `TestPostgresProviderContract/Provider_ManagedProviderState` | Provider SQL atomicity and backend parity | Contract databases; delete/concurrent recreate; asserts one winner and retained revisions | Durable transaction and backend-specific SQL behavior | Retain alongside `TestProviderLifecycleFiltersPassiveDetailAndRecreate` and handler owner `TestProvidersBrowserManagementAndSecretBoundary`. |
| `TestManagementLifecyclePreservesDisabledSelectionAndArchive` | Schedule management policy | Management fake; disable/delete/recreate and history projection | Service selection/history policy | Retain. SQL owners are `TestSQLiteProviderContract/Provider_ScheduleManagementAuthorityAndVersion` and `TestPostgresProviderContract/Provider_ScheduleManagementAuthorityAndVersion`; handler/browser owners are `TestSchedulesBrowserManagement`, `TestSchedulesAliasesLayoutBrowser`, and `TestBackofficeSchedulesBrowserWorkflow`. |
| `TestManagedRouteLifecycleAndOneTimeSecret`, `TestInventoryLifecycleFilters`, and `TestConfiguredNamesRemainReservedForManagedCreate` | Webhook service lifecycle, filtering, secret, and namespace policy | In-memory service store; lifecycle reads/recreate and managed create against active/deleted configured names | One-time secret and configured reservation policy | Retain each exact service owner. SQL owners are `TestSQLiteProviderContract/Provider_WebhookRouteManagement` and `TestPostgresProviderContract/Provider_WebhookRouteManagement`; handler/authenticated owners are `TestWebhooksBrowserManagement` and `TestBackofficeWebhooksBrowserWorkflow`. |
| `TestHTMXMutationFormsUseSharedAccessibleStatus` and `TestMutationFeedbackBrowser` | Production-template invariant and browser interaction | Embedded production templates versus live HTMX browser events; asserts marker/status presence versus pending/settled/focus/history behavior | Static template omission versus runtime interaction regression | Retain both; neither supersedes the other. |
| `TestSchedulesAliasesLayoutBrowser`, `TestMobileTablesLayoutBrowser`, and `TestMutationFeedbackBrowser` | Synthetic production-template layout | Root/prefixed mounts at 1440/1024/768/390; asserts state inventory, focus, history, confirmation, and contained overflow | Prefix and responsive layout regressions | Retain all mount/viewport cases. Authenticated mutation owners are `TestBackofficeSchedulesBrowserWorkflow`, `TestBackofficeMCPBrowserWorkflow`, and `TestBackofficeWebhooksBrowserWorkflow`. |
| `TestProjectHistoryKeepsOpaqueInputExact` | Supported stored-data compatibility | Current opaque body and older row without captured input; projection asserts lossless hex or bounded unavailable input | Compatibility with persisted older rows, not a removed UI surface | Retain. |
| Exact `service-security`, `service-authorization`, and `handler-authorization` members in `rationalizationScopes` | Security/authorization | Protected values, current browser authority, configured ownership, secret redaction | Disclosure or unauthorized mutation | Retain every enumerated member; examples include `TestMCPFormsFenceAuthorityAndNeverReflectValues`, `TestProvidersBrowserManagementAndSecretBoundary`, `TestManagedRouteValidationAndAuthority`, and `TestProbeRejectsMissingAndRevokedAuthorityBeforeLaunching`. |
| Exact `service-concurrency` and concrete backend-owner members in the manifest | Concurrency and stale-version fencing | Duplicate/manual admission, competing writes, stale generations | Duplicate execution or lost atomic winner | Retain `TestManagementManualRunIsIdempotentAndHistoryIsSafe`, `TestManagedScheduleManualRunFreezesAliasAndDuplicateSelection`, and all eight enumerated SQLite/PostgreSQL lifecycle owner IDs. |
| Exact `service-failure`, `migration-compatibility`, and adapter members in the manifest | Rollback, unavailable, recovery, and compatibility | Invalid definitions, publication/store failures, missing aliases, read-only configured rows, old stored input | Partial mutation, unsafe readiness, or unreadable retained data | Retain every enumerated member; exact owners include `TestProbeAndPublicationErrorsDoNotExposeCredentialsOrClaimReadiness`, `TestManualRunWithMissingAliasRecordsFailureWithoutChangingCron`, `TestConfiguredRouteReconciliationRetainsDisabledDeclarations`, and `TestOperationsInventoryRecoveryReadsNeverLaunch`. |

Baseline on 2026-11-16 was 335 top-level tests across the focused packages.
The focused race suite passed before cleanup; statement coverage was 61.0% for
`internal/webui`, 81.8% for Backoffice, 72.1% for `mcpbackofficeapp`, 83.7% for
`mcpmanage`, 86.0% for Providers, 70.2% for `scheduledjobs`, 41.8% for
`webhookbackofficeapp`, 81.4% for `webhookmanagement`, and 14.2% for `state`.
The cleanup removes one unsupported request scenario and two removed-name
assertions, with no supported contract owner lost. The post-cleanup inventory
has 336 top-level tests because it adds the single manifest-integrity validator;
the supported behavior suite is smaller while the ownership map is mechanically
enforced. Record the post-cleanup race, browser, coverage, lint, and diff results
in the task review; repository-wide gates remain a separate delivery gate.

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
matrix covers configuration-owned servers as read-only, OAuth-neutral entries,
managed servers with OAuth disabled as OAuth-neutral entries, successful managed
create-and-authorize, and saved managed creation followed by a discovery failure
and retry without duplicates. It exercises optional browser client registration,
confidential client settings, and account/password entry at the external issuer
only for managed servers with OAuth enabled.
It also covers browser/device flows, the native callback continuation with Strict
session cookies, root and
`/balda`, desktop/mobile and JavaScript enabled/disabled, denial/cancellation,
saved-grant attachment retry, changed/empty scopes, changed-file configuration
remaining OAuth-neutral, and actual hosted/ACP tool execution. Historical managed
revision payloads remain unchanged.
Narrowed shared grants fail incompatible retained scopes closed; restoring
authorization permits the original snapshot to invoke tools again. Existing
stdio subprocess/environment and hosted/ACP invocation coverage remains in
`TestStdioDiscoveryAndActualProvidersUseHostEnvironmentAndOverlay`.
Its private protocol inputs are excluded from diagnostics; no real
administrator, deployment or transport credentials are used. Synthetic gallery
results remain separate from this authenticated runtime evidence.

## Confirmation dialog gates

Use the optional Playwright installation above. Before authenticated application
verification, run the focused synthetic dialog checks:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BACKOFFICE_CONFIRMATION_SCREENSHOTS=/tmp/balda-confirmation-layout go test ./internal/apps/backoffice -run '^TestConfirmationsLayoutBrowser$' -count=1 -v
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_CONFIRMATION_MIGRATION_SCREENSHOTS=/tmp/balda-confirmation-actions go test ./internal/apps/backoffice -run '^TestConfirmationConditionsBrowser$' -count=1 -v
```

The first gate exercises native and HTMX forms with actual intercepted request
counts/payloads, named submitters, edited values, validation, keyboard focus,
Cancel/Close/Escape/backdrop, long escaped text, a completed error followed by
fresh consent, stale forms and newly swapped fragments. It checks root and
prefixed mounts at desktop/mobile widths; `npm test` also covers the shared
mechanism at all four gallery widths. The conditional gate uses production
Access, Account and Webhook templates and captures representative migrated
dialogs. It verifies effective bot-capability changes, inactive-role and
unbound edits, own/other credential and session revocation, disabled tests and
native MFA `confirm=on` with the entered password. These are synthetic previews
and intercepted requests, not persistence evidence.

Inspect the settled dialog images: legible contextual title/body, wrapped long
identifiers, reachable action/Cancel controls and visible keyboard focus. The
Plugins layout gate additionally captures Callee disable, retained rollback
and removal consequences at all four viewports and both mounts. Ordinary
settings remain editable checkboxes. Dismissing a dialog must send no mutation;
affirming it must send exactly one, and a repeated attempt asks again.

Then run the documented authenticated gates against disposable SQLite and
normal login. Invitation gates prove replacement/cancellation; WebAuthn gates
prove native passkey replacement/disable;
Schedules proves enabled and confirmed-disabled manual runs, cancellation,
exact retained destinations and unchanged next scheduled time. MCP OAuth also
checks native shared-authorization revoke cancellation, one confirmed request
and actual credential invalidation for current/retained contexts. Existing
missing-consent, authority, CSRF/origin and stale-state checks remain required.
Record actual commands, screenshots and distinct synthetic/application evidence
in Beads; finish with the repository quality gates in CONTRIBUTING.md.

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
database/process-owner reopen. Ordinary Reset preserves the session's exact
catalog snapshot while rotating only its local conversation lifetime.
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
layout gate before the application gate. Only synthetic local preview data is used.

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

Native hook review extends the hook layout gate to 11 states (88 viewport/mount
combinations), including pre-tool, post-tool and unsupported capability drafts.
Run the opt-in native engine gate from `agent-hooks.md` after the ordered layout
and application browser gates when changing provider adapters.

## Skills layout and application gates

Run the synthetic layout gate before the authenticated application gate, using
only the optional Playwright tools above:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_SKILLS_LAYOUT_SCREENSHOTS=/tmp/balda-skills-screens/layout go test ./internal/apps/backoffice -run '^TestSkillsLayoutBrowser$' -count=1 -v
```

It uses production Skills templates and assets at root and
`/balda/backoffice`, at 1440×900, 1024×768, 768×1024 and 390×844. Review the
mixed plugin/unmanaged/managed inventory, disabled entries, read-only details,
install and update review, confirmations, empty/long content, 400/409 errors
and accepted pending recovery. Checks cover exact single-main fragments,
no-store/private history, Source clearing, labels, keyboard focus, placeholder
contrast, document/table overflow, nested Skills navigation and actual 409
HTMX replacement after `htmx:afterSettle`. Inspect the generated screenshots
before accepting the layout or starting application verification. Fixtures are
synthetic and cannot mutate application state; Source inputs and credentials
are excluded from captures.


After the inspected Skills layout gate passes, run the real application gates:

```bash
go test -race ./internal/apps/balda -run '^TestBackofficeSkillsRuntimeContracts$' -count=1 -v
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_SKILLS_RUNTIME_SCREENSHOTS=/tmp/balda-skills-screens/application go test -race ./internal/apps/balda -run '^TestBackofficeSkillsBrowserWorkflow$' -count=1 -v
```

These gates use the shared production Fx catalog owners, real SQLite/ADK
session storage, local sources and deterministic file-Git repositories. The
runtime gate proves selected-subtree/resource retention, update while disabled,
old session pins and seeded ADK history/state across remove and process-owner
reopen, explicit reset,
pre-acceptance failures, pending publication recovery and read-only inventory
versus explicit unmanaged refresh. The provider itself is deterministic; no
LLM, external transport or production state is used.

The browser gate uses ordinary synthetic valid/invalid password login at root
and `/balda/backoffice`, 1440×900 and 390×844. It checks administrator/operator
authority, access refresh destination, Source clearing/history, single-main
HTMX replacement after `htmx:afterSettle`, exact review/install/update,
plugin ownership navigation, duplicate-source inventory, confirmation, stale
forms, CSRF/origin/revoked-session rejection, audit rollback, accepted
pending publication and recovery without source refetch. Controller checkpoints
verify actual durable selection/catalog and unchanged pinned-session metadata.
Capture only after Source clears and outside credential forms; inspect the
actual screenshots and record results separately from synthetic layout proof.

## Chat session archive layout gate

Run the synthetic gate before the authenticated archive application gate:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_CHAT_ARCHIVE_LAYOUT_SCREENSHOTS=/tmp/balda-chat-archive-layout go test ./internal/apps/backoffice -run '^TestChatArchiveLayoutBrowser$' -count=1 -v
```

The gallery links **Chat sessions** inventory, reset-separated before/after
lifetimes, closed, group, partial, pending, uncertain, unknown, empty transcript,
long/multiline escaped text, empty inventory and actual 400/403/404/503 previews.
All use the production archive projections/templates and embedded assets.
Synthetic identifiers and attachment labels are deliberately invalid; the QA
handler has no live archive reader and accepts GET/HEAD only.

Review root and `/balda/backoffice` at 1440×900, 1024×768, 768×1024 and
390×844. Check Workspace sidebar parent, native and HTMX inventory/detail/back,
previous/next sessions, older inventory and message windows, preserved filters
and inventory position, private history and fragment status, keyboard focus,
mobile navigation, top-bar alignment, contained table scrolling, readable long
names/locators/chat/attachments, loaded assets and browser errors.
Inspect actual images under `<directory>/<root|balda-backoffice>/<viewport>/`,
particularly `chat-sessions.png`, `chat-sessions-long.png`,
`chat-sessions-group.png`, and `older-messages-htmx.png`. The fixture matrix and
screenshots prove synthetic template/layout behavior; real administrator
login, current authority, storage, lifecycle and passive browsing remain the
separate authenticated application/integration gate. Record severity-ranked
findings, exact commands, timestamps and screenshot paths in Beads. Repair and
rerun this gate before starting the application gate.


## Chat session archive application gate

Only after the synthetic gate above passes and its screenshots are reviewed,
run the isolated authenticated workflow:

```bash
BALDA_BACKOFFICE_BROWSER_TEST=1 BALDA_CHAT_ARCHIVE_APPLICATION_SCREENSHOTS=/tmp/balda-chat-archive-application go test -race -timeout=20m ./internal/apps/balda -run '^TestBackofficeChatArchiveBrowserWorkflow$' -count=1 -v
go test -race -timeout=20m ./internal/apps/balda -run '^TestChatArchiveSharedProviderLifecycleAndTurns$' -count=1 -v
```

The application test uses a disposable SQLite provider shared by real ADK
session lifecycle, deterministic provider execution, archive admission/final
commit and production Backoffice authentication/SQL readers. It reopens the
database before browsing. Pagination-only older rows are explicitly synthetic;
the primary transcript comes from real admission and provider execution. No
external chat service or model is contacted. Fixture control endpoints belong
only to the ephemeral test server and are absent from the production listener.

The four documented viewports run at both root and `/balda/backoffice` mounts.
Normal username/password login, access-cookie refresh, admin/operator/anonymous,
demotion, disabled user, expired family and revocation are exercised against
full and partial requests. The workflow checks bounded SQL inventory/transcript
pagination, preserved filters/back navigation, empty replacement/no matches,
400/404/503 states, escaped multiline text and safe attachment labels, no-store,
CSP, one main element and page overflow. Provider/send counters and unchanged
live metadata/transcript counts prove browsing leaves chat state untouched.
Inspect `transcript.png`, `older-transcript.png`, `empty-replacement.png`,
`unavailable.png` and `refreshed-transcript.png` under each mount/viewport.
Every screenshot contains disposable synthetic chat, never deployment data.

The joined lifecycle test separately proves reset/close separation, exact
SQLite reopen continuity, fresh fallback, memory boot separation, retained
canceled input and committed-final replay after reset without another model
invocation. Tagged shared SQLite/PostgreSQL contract suites supply backend
migration, dedupe/conflict/fault and current-authority coverage. Run the complete
[Contributing gates](../../CONTRIBUTING.md), including Goose validation and
applicable projection/runtime/ingress jobs; browser success does not replace
them. Record exact commands, timestamps, logs, screenshot review and the full
REQ evidence mapping in Beads. Rendering repairs require rerunning the synthetic
gate before this application gate. Never waive a failed backend/security gate.
