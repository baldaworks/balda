# Backoffice UI review

The QA preview exercises the production standalone `browseredge` without Core,
database access, migrations, or an operator token. It is a local layout and
navigation aid, not a production server.

Start it on loopback:

```bash
go run ./cmd/balda backoffice qa serve --listen 127.0.0.1:8096
```

Open `http://127.0.0.1:8096/qa/ui/`. Any non-empty preview credentials are
accepted. The preview covers the real login, cookie, authenticated overview,
account navigation, and responsive page shell. Feature behavior is covered by
`browseredge` tests against generated-client service ports and by Management API
integration tests.

Run the pinned Chromium smoke suite with:

```bash
./scripts/qa/run-backoffice-e2e.sh
```

Use `--no-system-install` on a host that already has Playwright system
dependencies. Logs are written under
`${BALDA_BACKOFFICE_E2E_ARTIFACTS:-${TMPDIR:-/tmp}/balda-backoffice-e2e}`.
Set `BALDA_BACKOFFICE_E2E_SCREENSHOTS=1` to retain screenshots when the suite
adds a failure capture.

The smoke test verifies:

- loopback-only preview startup;
- login and opaque cookie navigation through the real standalone edge;
- account-page reachability;
- no browser exceptions or mobile horizontal overflow.

Production validation uses a compatible Core/Backoffice pair:

```bash
balda-backoffice validate
balda-backoffice preflight
```

`validate` is config-only. `preflight` checks the authenticated Management API
compatibility endpoint before the browser listener binds.
