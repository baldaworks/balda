# Unified HTTP ingress and managed webhook URLs

## Intent and scope

Balda Assistant on `ssh asus` must publish Backoffice and HTTP ingress through
one Balda listener and one public origin. A Backoffice administrator creating a
webhook should see the complete URL that an external sender will call. The
route name determines the path, so a second editable Path field adds no useful
choice. The change must keep existing callback URLs and stored webhook
definitions working while the deployment moves to the shared listener.

This design changes HTTP hosting and managed webhook address presentation. It
does not change how webhook jobs, reports, secrets, deduplication, aliases,
browser authorization, or chat transports behave.

## Address contract

- `balda.backoffice.public_url` remains the configured public **origin** with
  no path. It supplies the origin displayed for managed webhook URLs; request
  routing never trusts an inbound `Host` or forwarded header to construct it.
- `balda.backoffice.base_path` supplies the public path prefix for Backoffice
  and new managed ingress routes. It also scopes browser pages, assets,
  cookies, and callbacks. On asus it remains `/balda`. An empty value mounts
  these routes at the origin root for local installations. Existing routes
  with explicit paths remain exceptions for compatibility.
- A new Backoffice-managed route named `orders` is stored at the canonical
  path `<base_path>/inbound/webhooks/orders`, which is
  `/balda/inbound/webhooks/orders` on asus. Its displayed and copyable URL is
  `https://lab.metalagman.dev/balda/inbound/webhooks/orders`. The
  `inbound` segment separates external POSTs from Backoffice management at
  `/balda/webhooks/{name}`. The name is immutable. Neither create nor edit
  accepts an independently chosen path for a new managed route.
- A managed route created before this change retains its stored path. Its
  current URL remains visible and copyable, and editing other fields never
  rewrites that path. Config-owned routes retain their explicit `path` and are
  still read-only in Backoffice. This needs no database migration.
- `<base_path>/gateway/...` denotes a chat transport callback namespace, not
  generic application webhooks. Existing configured transport paths remain
  valid; this change does not silently move Slack, Telegram, Zulip, or
  Mattermost callbacks. A later callback URL migration can use that namespace
  explicitly.

The Webhooks inventory and detail use **Webhook URL** for the full address.
The creation form has Route name and a read-only live URL preview, followed by
the existing prompt, optional Report to, delivery, and deduplication controls.
The editor shows the stored URL as read-only. Its secret continues to appear
only after creation or explicit rotation. The preview is computed from the
configured public origin, base path, and validated route name; the service,
rather than browser JavaScript, enforces the canonical stored path. JavaScript
updates the preview while typing and enables copying, but the server-rendered
response is authoritative.

## HTTP ownership and routing

The Balda composition root owns one TCP listener, `http.Server`, route
composition, readiness, and shutdown. Backoffice supplies its existing
browser-protected `http.Handler`; each inbound adapter supplies its own
transport-authenticated handler. The composition root binds all enabled paths
and starts serving before it enables inbound transport clients or registers
external callbacks. It stops inbound clients before draining the shared HTTP
server and provider. No Backoffice package imports a concrete transport.

The shared bind address is `balda.http.listen_addr`. The old per-adapter
`listen_addr` settings are removed from the supported config contract in the
same release; validation identifies them by key and reports how to move to
`balda.http.listen_addr`. Existing path and external URL settings remain.
The local default is `127.0.0.1:8095`, consistent with the current Backoffice
default. A non-loopback bind retains Backoffice's HTTPS public-origin
requirement. The listener exists even when no generic webhook route is
enabled, so Backoffice and health checks remain available.

The composition root registers exact configured transport callback paths,
Backoffice under its configured base path, and the generic webhook receiver
as the fallback for other paths. Known route collisions fail during preflight
or startup; a generic config route may not occupy a Backoffice endpoint or
configured transport callback. The current asus Slack callbacks under
`/balda/slack/...` remain exact routes and therefore take precedence over the
Backoffice path subtree during transition. Unknown paths return 404 and never
start jobs. Each handler keeps its current HTTP method, body limit,
signature/token authentication, browser session/CSRF checks, and security
headers. The common server uses the strictest applicable header/read limits
and a write timeout long enough for existing webhook delivery acknowledgements;
per-request deadlines protect shorter operations.

Route-selection and admission policy stay in `webhookapp`, durable route
management in `webhookmanagement`, transport HTTP parsing in
`channel/webhook`, and persistence in `state`. Listener and mux wiring belong
to `internal/apps/balda`, with a small handler exposure port for each adapter.
The architecture lint rules change with these boundaries.

## Startup, failures, and compatibility

Config load, database migrations, bundled MCP lifecycle, provider creation,
and channel runtime retain their required order. Administrator bootstrap and
route validation finish before the shared listener accepts requests. A bind
failure or route collision aborts startup. Telegram webhook registration
cannot occur before the listener is accepting requests. A failed later startup
stage rolls back registered ingress and closes the server. Active requests are
drained on shutdown before closing the shared provider.

New managed-route creation is atomic with its generated secret and canonical
path. Existing managed and config routes are read from their stored paths;
no SQL migration or live-state rewrite is used. If a previously valid custom
path conflicts with a Backoffice or transport route on a unified listener,
preflight reports the exact conflict and deployment stops before replacing the
running instance. The operator resolves that conflict in config or with a
managed route edit in the old version before rolling forward.

## Asus deployment

The running pod currently exposes Service ports 8091 (Slack) and 8095
(Backoffice). `balda-lab` routes `/balda/slack/events` and
`/balda/slack/commands` to 8091 and `/balda` to 8095. Generic webhook routes
are not externally published. The live Deployment is Helm-managed, while
`balda-lab` was applied with `kubectl`. The remote Terragrunt image digest and
chart Service template lag the live Deployment, so applying them unchanged
would roll back the image or remove a live Service port.

Rollout uses a new immutable image built from the merged Balda commit. First
reconcile the assistant chart/Terragrunt inputs with the running release;
record the live image digest and all live HTTPRoute matches. Set
`balda.http.listen_addr` to `0.0.0.0:8095`, remove obsolete per-adapter bind
settings, and expose one Service port 8095. Point the existing Slack callback
matches at 8095; the existing `/balda` PathPrefix then covers new managed
webhook URLs too. Apply the image and routing together so current Slack URLs
remain reachable. Verify Backoffice assets/auth, Slack event and command challenge
responses, a managed webhook test POST, and the public
`/balda/inbound/webhooks/<name>` URL.
Roll back image, values, Service, and HTTPRoute as one unit if health or
ingress checks fail; no database rollback is needed for this change.

## Verification

First run layout E2E with JavaScript: create form preview, read-only existing
URL, URL copying, inventory/detail consistency, and Backoffice under
`/balda`. Then run application E2E: create a managed route, submit an
authenticated external POST to its derived path, inspect history, edit it
without URL drift, and verify a pre-existing custom-path route still accepts
POSTs. Exercise Slack and browser endpoints on one socket, startup collision
and rollback behavior, and disabled/unknown route responses. Run
`go test -race ./...`, `go tool golangci-lint run`, and
`go tool go-arch-lint check --project-path .` before release. Finally verify
the asus pod, HTTPRoute acceptance, public URL, and its live image digest.
