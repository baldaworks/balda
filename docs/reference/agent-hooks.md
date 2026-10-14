# Managed agent hooks

Administrators configure agent hooks in **Operations → Agent hooks** at
`<balda.http.base_path>/backoffice/hooks`. These hooks respond to agent lifecycle
and native tool events. They are separate from incoming HTTP webhook routes.

## Events

| Event | Boundary | Decisions |
| --- | --- | --- |
| `SessionStart` | A new or lazily restored Balda runtime is prepared, before the session becomes active | Allow or deny activation |
| `UserPromptSubmit` | Before the provider receives an ordinary, skill, scheduled, webhook or automatic session turn | Allow or deny; optionally append context |
| `Stop` | The provider emits its terminal turn event, before Balda captures or submits the final answer | Allow or deny final acceptance |
| `SessionEnd` | The session runtime is being closed, including reset and shutdown | Best-effort notification; cleanup always continues |
| `PreToolUse` | Before a supported native tool invocation | Allow/deny; optionally replace input |
| `PostToolUse` | After a supported native tool completes | Allow/reject result; optionally append context |

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
optional matcher is a Go regular expression over the native tool name for tool
events, or the source for lifecycle events. An empty matcher matches everything. Hooks execute sequentially in name order. Each invocation selects one
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

## Native adapters

Native interception supports **Linux and macOS**. Lifecycle command hooks retain
the existing host platform support. Unsupported native providers/platforms cannot
be enabled in the Web UI; disabled drafts remain editable. Every reachable pool
member must have a supported adapter.

| Provider | Integration | Requirement |
| --- | --- | --- |
| `claude_code_acp` | SDK `extraArgs` forwards inline native `--settings` hooks | Claude ACP 0.16.2 or later with command hooks |
| `codex_acp` | Bridge forwards native `-c` definitions and exact trusted hashes | Codex ACP 1.10.1 or later; CLI supports trusted command hooks and `hooks/list` |
| `opencode_acp` | Private V1 plugin implements `tool.execute.before/after` | OpenCode 1.19.0 or later with V1 plugin support |

Activation requires a new session: use `/reset` for an existing session after
first enabling a tool hook. New turns without the required adapter fail with an
explicit reset requirement. In-flight turns/tools are unaffected. Installed
adapters read live definitions at each event. With no enabled tool hooks,
provider launch retains its existing behavior.

Provider arguments, models, modes, MCP selections and native hook definitions
are retained. User/project settings files, credentials and history are not
modified. Codex discovery verifies the adapter's exact command hashes and places
only their trust state in process flags; global hook trust and permissions are
not bypassed. Explicit conflicting Codex hook flags are rejected.

A private startup acknowledgment proves the native hook/plugin actually loaded
before accepting the ACP session response. Disabled/ignored hook engines or
unsupported bridges fail explicitly. This acknowledgment does not run Balda's
`SessionStart` hook again. Native provider policies and tool coverage remain
authoritative; hosted tools that emit no native hook are outside this contract.

Bindings use private 0700 directories, 0600 files, random bearer capabilities and
a loopback-only endpoint. The host fixes the Balda session, provider and CWD.
Close revokes capabilities and cancels commands. Process groups are registered
only after checking parentage, the proxy's binding argument and start identity;
close cleans them up even after a proxy crash. Tokens/plugin files are ephemeral
and are not exposed in SQL, audit or Web UI.

Tool stdin adds `tool_name`, `tool_call_id`, `tool_input` and, for post events,
`tool_response`. `agent_session_id` is the native session. Names and argument
schemas remain provider-native: `Bash`, `exec_command` and `bash` are distinct
matcher values. A portable shell matcher can use `^(Bash|exec_command|bash)$`.

`updated_input` is a complete JSON object replacement accepted only for
`PreToolUse`. Subsequent matching Balda hooks inspect the replacement:

```json
{"updated_input":{"command":"printf 'approved operation'"}}
```

Preserve the tool's required fields. Codex uses its documented native `allow`
verdict to apply a rewrite. Claude uses `ask`, retaining its permission callback
for the rewritten invocation. Ordinary allow without a rewrite emits no native
permission override. OpenCode replaces the plugin's mutable arguments.

Claude/Codex accept `additional_context` on native pre/post events. OpenCode
appends it to a post-tool result; pre-tool context is unsupported and rejected
before execution. Post denial cannot undo an operation that already ran. Hooks
execute approved host programs and are not an operating-system security boundary.

## Native engine verification

The opt-in suite runs actual published backends against loopback model fixtures
with isolated native config directories and synthetic credentials. It verifies
denial before a filesystem effect without contacting an external model:

```bash
BALDA_NATIVE_HOOK_TEST=1 go test -race ./internal/apps/balda/hookprovider -count=1 -v
```

Install Codex CLI and Node/npm first. The suite uses Claude ACP 0.16.2 and
OpenCode 1.19.0 and discovers installed Codex hook capabilities. Ordinary tests
also verify result translation, input rewrites, isolation, activation and cleanup
after proxy `SIGKILL`.

## Ownership and verification

`hookcmd` owns neutral values and validation. `hooks` owns management and
execution policy behind consuming ports. `state` owns guarded KV/audit
persistence. `session` and `sessionturnapp` own their lifecycle boundaries;
`hookfx` connects those ports to the shared runner. Backoffice owns only HTTP
ingress and presentation through `hookbackofficeapp`.

`hookprovider` owns native protocol adaptation and private process/binding
lifetimes. `agent` consumes a session-runtime factory port; `catalogapp` supplies
private launch defaults and agent-owned release. Session/transport packages do
not own native provider contracts.

Run the ordered synthetic layout and authenticated application gates described
in the [Backoffice UI review runbook](backoffice-ui-review.md), then the required
[contribution checks](../../CONTRIBUTING.md).
