# Standalone skill management

Balda provides two different skill interfaces:

- `balda skill ...` is the host-local management CLI described here;
- `/skill <skill> [prompt...]` in chat selects an installed skill for one turn.

Backoffice **Operations → Skills** and the CLI use the same managed lifecycle.
The CLI is useful for shell automation and installations where Backoffice is
unavailable.

## Inspect skills

```bash
balda skill list
balda skill show <selector>
```

Both commands also accept `--json`. Inventory includes managed, unmanaged and
plugin-owned contributions without source origins, credentials, local source
paths or skill bodies.

A managed skill's stable selector looks like
`user-skill/managed-<uuid>`. Use that ID for automation. A skill name is also
accepted when exactly one visible contribution has that name. Ambiguous names
fail and report candidate IDs.

## Install a skill

Stop Balda before a mutation. A mutation takes exclusive ownership of
`<state_dir>/balda.lock`; if Balda or another maintenance command owns that
state directory, the command fails with `runtime_active`.

Install one subtree from a pinned Git ref:

```bash
balda skill install https://github.com/obra/superpowers.git \
  --ref v7.0.0 \
  --path skills/systematic-debugging
```

The CLI captures the selected subtree, displays its name, content revision,
resolved commit and impact, then asks for confirmation. For automation or any
non-interactive invocation, add `--yes`. JSON mutations always require
`--yes`:

```bash
balda skill install https://github.com/obra/superpowers.git \
  --ref v7.0.0 \
  --path skills/systematic-debugging \
  --yes --json
```

Do not put tokens or passwords in source URLs. Configure an SSH agent or Git
credential helper for the runtime account. Local directories use the same
review and bounded capture:

```bash
balda skill install /srv/balda-skills/review --yes
```

Only one skill subtree is accepted per command. Changed bytes between review
and acceptance produce `conflict`; rerun the command to review the new content.

## Change managed skills

```bash
balda skill update <selector>
balda skill enable <selector>
balda skill disable <selector>
balda skill remove <selector>
```

Each command shows the target and impact before confirmation and accepts
`--yes` and `--json`. Update reuses the private recorded origin and preserves
enablement. Enable, disable, update and remove apply only to managed standalone
skills. Plugin-owned and unmanaged filesystem skills remain visible to
`list/show` but return `not_owned` for these mutations. Remove retains immutable
revisions and existing session pins.

Refresh unmanaged global roots explicitly:

```bash
balda skill refresh --yes
```

## Publication and recovery

A successful mutation reports two independent states:

- **accepted and published** means desired state and the durable catalog agree;
- **accepted; publication pending** means the desired change is already
  durable, but catalog publication did not finish.

The pending case returns a non-zero exit status with
`error.code: publication_pending` while retaining the accepted result in the
JSON envelope. Automation must run recovery instead of repeating the mutation.

Do not repeat an accepted install or update. Recover the existing accepted
change instead:

```bash
balda skill recover
```

Recovery does not require `--yes`, fetch the managed origin or create another
mutation audit.

Maintenance mutations require the normal runtime to be stopped. A later normal
`balda start` loads the published catalog. Existing restored sessions keep their
pinned capability snapshot, including across `/reset` and `/new`. Those commands
create empty local conversations; they do not adopt the current catalog.

Plugin-owned skills are installed through the shared managed
[`balda plugin` lifecycle](plugins.md#host-local-plugin-cli), not the standalone
skill installer. After a published plugin install, a fresh `balda skill list/show`
sees its enabled skills from managed state. Files under a plugin directory alone
do not prove registration, and a process restart cannot replace explicit managed
registration of a stranded package.

## JSON contract

JSON output is a single envelope. Success sets `ok: true` and one of
`inventory`, `entry`, or `result`; mutation success also includes `review`.
Failure sets `ok: false` and a bounded `error.code`, such as `invalid`,
`conflict`, `not_owned`, `not_found`, `ambiguous`, `runtime_active`,
`confirmation_required`, or `unavailable`.

Private source origins, credentials, browser authority, protected host paths,
skill bodies and raw internal errors are excluded from both JSON and plain
output.
