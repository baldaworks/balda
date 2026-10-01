# User onboarding reference

## Primary onboarding path

The primary onboarding path runs Balda as a single app with its built-in
command/event runtime and local SQLite state. The runtime is bundled inside the
Balda process by default, so first-time setup does not require operating an
external queue service.

SQLite remains product/read-model state (owner/collaborator, session metadata,
task views, memory state, scheduler metadata, delivery outbox), not a command
queue.

npm remains the shortest install path:

```bash
npm install -g -y @baldaworks/balda
balda init
balda start
```

For repo-local development, run:

```bash
task dev
```

To exercise fake ingress scenarios (Telegram/webhook/scheduler paths), run:

```bash
task scenarios
```

To inspect the runtime streams and consumers, run:

```bash
task runtime-state
```

To replay projection events through the deterministic projector replay suite,
run:

```bash
task projection-replay
```

`balda init` requires a Telegram bot token, detects supported provider CLIs
(`codex`, `opencode`, `copilot`, `gemini`, `claude`), writes
`.config/balda/config.yaml`, initializes `.config/balda/state.db`, and prints
both an owner auth command and Telegram auth link, plus the generated
Backoffice administrator password. Its username is `superuser`; save the password securely. The default token storage is
CWD `.env` as `BALDA_TELEGRAM_TOKEN`.

On an existing installation, stop Balda, back up the selected database, and
deploy the new binary. Goose automatically converts owner/collaborator records
when the state provider opens. Conversion preserves roles and bot bindings;
it creates disabled browser credentials without generating passwords.
Run `balda backoffice bootstrap-admin` to set the converted primary
administrator's (`superuser`) first browser password, then restart. The command
generates and prints the password once; optional non-terminal stdin can provide
an operator-chosen password. Already-converted users retain their passwords;
replacing a usable administrator password requires `--reset`.
`balda start` applies embedded schema and data migrations and refuses bot ingress
or Backoffice HTTP until canonical users and administrator credentials are ready.

Owner onboarding is completed in a direct message with the bot by opening the
printed auth link or sending:

```text
/start owner=<owner_token>
```

For Backoffice-managed account binding, Telegram accepts a generated
`bind_<opaque_token>` invitation through `/start`, a bot deep link or an exact
direct message. The verified sender is attached to the invitation's selected
existing user, preserving its role and primary status; no owner bootstrap token
is required. A token is scoped to the configured bot, expires after 24 hours and
is consumed atomically with the binding and audit. Replace or cancel a pending
invitation in Backoffice instead of reusing a lost or already consumed token.

After owner auth, users can send normal direct messages to the bot's main DM
session or create a named topic session:

```text
/topic <name>
```

The supported Docker Compose onboarding path uses the shipped root
`Dockerfile` and `compose.yaml`:

```bash
docker compose build balda
docker compose run --rm balda init
docker compose up -d balda
```

The Compose service bind-mounts the current directory as `/workspace`, so the
container uses the same `.env`, `.config/balda/config.yaml`,
`.config/balda/state.db`, and `.git` as host execution.
