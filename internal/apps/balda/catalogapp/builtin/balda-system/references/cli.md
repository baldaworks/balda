# Balda CLI

The `balda` executable is an operator interface. It is distinct from chat slash
commands and from model-callable MCP tools. Use it only when shell execution is
available and the requested operation is authorized.

Run `balda --help` or a subcommand's `--help` for the exact surface of the
installed binary. The important lifecycle commands are:

- `balda init`: create initial configuration and state for a new installation;
  it does not overwrite an existing configuration.
- `balda validate`: parse and validate configuration and application graph
  construction without opening or migrating the state database.
- `balda preflight`: validate app wiring, start and stop the base runtime without
  enabling ingress, and leave the selected agent provider unresolved and
  unlaunched.
- `balda doctor`: run validation and preflight as one operator readiness check.
- `balda start`: open state, apply embedded migrations, initialize the runtime,
  then enable configured channels, Backoffice, webhooks, and other ingress.
- `balda skill ...`: inspect or maintain standalone skills.
- `balda plugin ...`: inspect or maintain plugins and marketplaces.
- `balda --version`: report the binary version without starting the runtime.

## Configuration selection

Use the config path reported in the current root instruction for this running
session. Do not reconstruct it from the workspace path.

For an explicit configuration root, pass `--config-dir <root>`. The root
contains `balda/config.yaml`; for example, `--config-dir /config` selects
`/config/balda/config.yaml`. The flag is the highest-priority config root.
Profiles and `BALDA_*` environment overrides may still affect resolved values.

Configuration may contain credentials or expanded environment values. Do not
print raw configuration, secrets, provider environments, or authentication
material when inspecting or reporting failures.

## Catalog maintenance

Read-only `balda skill list/show` and plugin inspection expose bounded metadata.
Managed mutations use their existing review and confirmation contracts. Built-in
skills such as `builtin/balda/skill/balda-system` are read-only and are rejected
by managed mutation paths.

Catalog publication does not rewrite an active session's pinned capabilities.
After an accepted catalog change, use `/reset` or `/new` only when the user wants
the current conversation locator to adopt the new catalog and clear its prior
history.

When validating a newly built Balda binary, invoke that candidate binary for
`validate`, `preflight`, and version checks. Running the installed binary would
validate the old implementation instead.
