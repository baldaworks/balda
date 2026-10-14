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

Timeouts are 1–120 seconds per command. The complete event has a two-minute
budget, including time waiting for one of the application's 16 process slots.
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

## Ownership and verification

`hookcmd` owns neutral values and validation. `hooks` owns management and
execution policy behind consuming ports. `state` owns guarded KV/audit
persistence. `session` and `sessionturnapp` own their lifecycle boundaries;
`hookfx` connects those ports to the shared runner. Backoffice owns only HTTP
ingress and presentation through `hookbackofficeapp`.

Run the ordered synthetic layout and authenticated application gates described
in the [Backoffice UI review runbook](backoffice-ui-review.md), then the required
[contribution checks](../../CONTRIBUTING.md).
