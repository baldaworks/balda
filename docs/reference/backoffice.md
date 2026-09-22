# Backoffice application

This page is the authoritative application and Web UI foundation for
Backoffice. Read it before changing `cmd/backoffice` or
`internal/apps/backoffice`.

## Application boundary

- `cmd/backoffice` is the executable entrypoint and composition edge. It owns
  process startup and dependency wiring, not page policy or domain state.
- `internal/apps/backoffice` owns Backoffice application behavior, including
  HTTP routes, security enforcement, server-side rendering, view models,
  templates, static assets, and tests.
- The browser communicates only with Backoffice. Backoffice performs Assistant
  calls server-side; the browser must never call Assistant directly.
- `superadmin` is the interface name, not a new authorization role. Existing
  `administrator` and `operator` roles remain authoritative.

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

One Backoffice Go binary contains the templates, CSS, JavaScript, icons, and
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
