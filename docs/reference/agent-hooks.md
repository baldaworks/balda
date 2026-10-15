# Managed agent hooks

Administrators configure agent hooks in **Operations → Agent hooks** at
`<balda.http.base_path>/backoffice/hooks`. These hooks respond to agent lifecycle
events. They are separate from incoming HTTP webhook routes.

## Events

| Event | Boundary | Decisions |
| --- | --- | --- |
| `SessionStart` | A new or lazily restored Balda runtime is prepared, before the session becomes active | Allow or deny activation |
| `UserPromptSubmit` | Before the provider receives an ordinary, skill, scheduled, webhook or automatic session turn | Allow or deny; optionally append context |
| `Stop` | The provider emits its terminal turn event, before Balda captures or submits the final answer | Allow or deny final acceptance |
| `SessionEnd` | The session runtime is being closed, including reset and shutdown | Best-effort notification; cleanup always continues |

`SessionStart.source` is `startup` for a new runtime, including reset recreation,
or `resume` for lazy restore. `SessionEnd.source` is `close`. Submission sources
are the existing turn source values. `Stop.source` is `success`, `failed` or
`interrupted`, following the provider's terminal status. A stream without a
terminal event does not emit `Stop`.

A denied start never exposes the candidate session. Denial during restore
preserves a successfully restored runtime's workspace and history. If upstream
restore recovery had already created a fresh replacement, denial removes that
candidate and retains the previous session metadata. A denied `Stop`
fails the current turn; it does not start an automatic retry or undo earlier
provider actions or streamed progress. `SessionEnd` failure is logged without
command output and cannot prevent reset, close or shutdown cleanup.

## Configuration and lifetime

Create a named definition, select its event and executable, and supply arguments
as a JSON array of strings. Definitions start disabled unless **Enable this
hook** is checked. The executable must be installed in the environment running
Balda; the Web UI does not upload or install programs. Enabled commands execute
with Balda's operating-system privileges and inherit its environment.

An optional provider ID matches the configured Balda provider ID exactly. An
optional source matcher is a regular expression; an empty matcher matches all
sources. Hooks execute sequentially in name order. Each invocation selects one
complete configuration record. Saved changes apply to the next matching event;
already-running invocations keep their selected definitions.

**Timeout in seconds** accepts nonnegative whole seconds: for example, `30`,
`300` or `3600`. `0` disables the hook's own deadline. There is no fixed overall
event deadline; matching hooks run sequentially with their individual timeouts.
A positive timeout includes waiting for one of the application's 16 process slots.
Values must fit the host's integer and Go duration representation (up to
9,223,372,036 seconds on 64-bit hosts); overflowing values are rejected.
The caller's cancellation or earlier deadline always wins, including with `0`.
Session-end notification shares the ten-second session cleanup budget. Unix
timeout/cancellation terminates the hook process group. Standard output and
standard error are each bounded to 64 KiB. Event JSON is bounded to 256 KiB;
combined added context is bounded to 32 KiB. A configured hook that cannot run,
times out, or produces malformed output fails the protected event closed.

Editing and deleting carry the loaded global configuration version. Any
intervening accepted write makes that form stale: a 409 preserves the draft and
offers **Reopen this hook**. Deletion requires its explicit confirmation.
Operators and anonymous visitors cannot read or change definitions. Mutations
use the existing administrator session, assurance, same-origin and CSRF guards.
SQL checks current user, credential, MFA and browser-session versions again and
commits the configuration with its body-free audit event in one transaction.

Definitions live in the existing application KV table for SQLite or PostgreSQL;
no new table or migration is needed. Restart reads the same durable record.
Hook policy is live configuration and does not replace the session's pinned
commands, skills or MCP capabilities. Browser responses are private/no-store,
and editor content is excluded from HTMX history. Inventory and audit never
include executable arguments or event payloads.

## Command protocol

Balda supplies one JSON document on standard input:

```json
{
  "hook_event_name": "UserPromptSubmit",
  "session_id": "tg-123-0",
  "agent_session_id": "tg-123-0",
  "provider": "codex",
  "cwd": "/srv/balda/session-workspace",
  "source": "chat",
  "prompt": "Review this change"
}
```

`Stop` also includes `response`. No prompt or response is interpolated into the
executable or its argument list. Arguments are passed directly, without implicit
shell evaluation. A shell can be selected explicitly by the administrator.

Exit 0 with empty output or `{}` allows the event. Structured output is:

```json
{
  "decision": "deny",
  "reason": "The request does not meet the configured policy."
}
```

`decision` may be `allow` or `deny`; omission means allow. Exit 2 also denies the
event. Other nonzero exits fail execution. Output must be a single JSON object
with only supported fields; trailing documents and unknown fields are rejected.
`reason` is bounded to 1,024 bytes and is retained as trusted policy feedback,
but is not included in public exception messages or audit.

On `UserPromptSubmit`, `additional_context` appends bounded context to the
provider input. That field is rejected for the other lifecycle events. It does
not weaken Balda's existing permissions or change the selected runtime.

## Usage examples

These examples use Python 3 and the standard library. Save scripts on the
machine/container running Balda, outside agent-writable workspaces, and make
them readable by the Balda operating-system user. Use the actual absolute Python
executable path on that host; `/usr/bin/python3` below is illustrative. Scripts
need no executable bit when passed as an argument to Python.

Only protocol JSON belongs on stdout. Send diagnostics to stderr; a nonzero
exit other than `2` means execution failure. Programs called by a script must
have their stdout captured or redirected so it cannot corrupt the verdict.
Saving a definition does not run its executable; verify it manually before
enabling it. Provider IDs and paths below are examples, not configuration that
Balda creates automatically.

### Add project working conventions to each prompt

Save `/opt/balda/hooks/project_context.py`:

```python
import json
import sys

event = json.load(sys.stdin)
if event["hook_event_name"] != "UserPromptSubmit":
    raise SystemExit(2)
json.dump({
    "additional_context": (
        "Follow the project's existing formatting and naming conventions. "
        "Report which verification commands actually ran and their results."
    )
}, sys.stdout)
```

In **Operations → Agent hooks → Add hook**, enter:

| Field | Value |
| --- | --- |
| Name | `project-context` |
| Event | `UserPromptSubmit` |
| Executable | `/usr/bin/python3` |
| Arguments | `["/opt/balda/hooks/project_context.py"]` |
| Provider ID | Leave empty, or enter an existing Balda provider ID |
| Source matcher | Leave empty |
| Timeout in seconds | `30` |
| Enable this hook | Checked |

Verify the command independently, then submit an ordinary Balda turn. The
provider input will contain the original prompt followed by this context:

```bash
printf '%s\n' '{"hook_event_name":"UserPromptSubmit","session_id":"example","cwd":"/srv/project","prompt":"Explain the project"}' \
  | /usr/bin/python3 /opt/balda/hooks/project_context.py
```

The result is one JSON object with `additional_context`. This does not replace
existing agent instructions, permissions, or the user's prompt. Disable the
definition to stop adding context on subsequent turns.

### Record a session-close event with a local program

Save `/opt/balda/hooks/record_session_end.py`:

```python
import datetime
import json
import os
import sys

event = json.load(sys.stdin)
if event["hook_event_name"] != "SessionEnd":
    raise SystemExit(2)
entry = {
    "at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "event": event["hook_event_name"],
    "provider": event.get("provider", ""),
    "source": event.get("source", ""),
}
fd = os.open(sys.argv[1], os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
with os.fdopen(fd, "a", encoding="utf-8") as log:
    log.write(json.dumps(entry) + "\n")
json.dump({"decision": "allow"}, sys.stdout)
```

Create the destination directory and grant the Balda user write access before
enabling this definition:

| Field | Value |
| --- | --- |
| Name | `record-session-end` |
| Event | `SessionEnd` |
| Executable | `/usr/bin/python3` |
| Arguments | `["/opt/balda/hooks/record_session_end.py", "/var/lib/balda/hooks/session-events.jsonl"]` |
| Provider ID | Leave empty |
| Source matcher | `^close$` |
| Timeout in seconds | `5` |
| Enable this hook | Checked |

Verify it using synthetic input:

```bash
printf '%s\n' '{"hook_event_name":"SessionEnd","session_id":"example","cwd":"/srv/project","provider":"example-provider","source":"close"}' \
  | /usr/bin/python3 /opt/balda/hooks/record_session_end.py /tmp/example-session-events.jsonl
```

The command returns `{"decision":"allow"}` and appends a JSON line to the
selected file. The example deliberately omits prompts, responses and tool
arguments. Closing or resetting an active Balda session emits the real event;
closing an unactivated lazy session does not. Keep the command short: session
cleanup continues after its ten-second budget even if this hook requests a
longer or unlimited timeout. This is a best-effort local observation, not a
durable delivery queue. Log rotation and retention belong to the operator.

## Ownership and verification

`hookcmd` owns neutral values and validation. `hooks` owns management and
execution policy behind consuming ports. `state` owns guarded KV/audit
persistence. `session` and `sessionturnapp` own their lifecycle boundaries;
`hookfx` connects those ports to the shared runner. Backoffice owns only HTTP
ingress and presentation through `hookbackofficeapp`.

Run the ordered synthetic layout and authenticated application gates described
in the [Backoffice UI review runbook](backoffice-ui-review.md), then the required
[contribution checks](../../CONTRIBUTING.md).
