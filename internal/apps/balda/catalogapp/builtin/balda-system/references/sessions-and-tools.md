# Sessions and tools

## Session boundaries

Balda maps each supported chat conversation scope to its own provider session.
The channel type and session ID in the root instruction identify the current
scope. Durable deployments save session metadata for exact lazy restoration
after restart.

- `/reset` and `/new` cancel current work, clear that session's history, and
  create a fresh runtime at the same locator with current catalog capabilities.
- `/cancel` stops the active conversational turn and drops queued turns for the
  current session. It does not stop GoalKeeper.
- `/close` applies the supported chat/topic boundary and clears the current
  runtime. In worktree mode it cleans up the mounted worktree but does not export
  its changes.
- `/locator` reports the public transport locator. The internal Balda session ID
  is not a substitute for that locator in scheduler or webhook configuration.

Use chat commands only when the user asks for their behavior or invokes them.
Answer ordinary questions directly instead of redirecting the user to a command.

## Capability snapshots and skills

Commands, skills, and MCP definitions are bound to an immutable session snapshot.
Application catalog changes affect new, reset, or otherwise freshly replaced
sessions; they do not mutate a healthy active runtime.

The available-skills list is metadata for discovery. Skill content is user-level
guidance. Resolve ambiguous names with the advertised qualified identity. The
standard `balda.skill.load` tool binds the current authenticated session snapshot
and returns the retained exact revision; its input does not accept a revision or
host path.

MCP namespaces are capabilities, not promises. Use only tools exposed in the
current session and follow their schemas. Never infer that a documented Balda
service is enabled merely because this reference describes it.

## Durable memory

When `balda.memory` tools and memory guidance are present, save a fact only after
an explicit user request to remember it. Store concise durable facts or
preferences, not secrets, raw transcripts, temporary task state, or unsupported
inferences. A successful write affects later refreshed turns according to the
tool contract; do not claim persistence when the tool reports failure or a
nonterminal state.

## GoalKeeper

`/goalkeeper <objective>` starts durable goal work in isolated worker and
validator sessions. It does not reuse the conversational provider session or
its mutable state. Only one GoalKeeper run may be active for a session.

`/goalkeeper clear` stops that goal run. `/cancel` affects ordinary conversation
work and does not stop GoalKeeper.

With Git worktrees enabled, GoalKeeper uses a separate goal workspace and
exports validated passing work to the configured base branch. In direct mode it
works in the configured working directory and records a passing result as not
exported. Export failure preserves recovery information rather than reporting a
successful landing.

## Progress and external effects

If `balda.session.progress` is exposed, use it during authorized long-running
work when the user would otherwise wait without useful feedback. Report a real
milestone, a changed plan, a blocker, or the next action in concise and truthful
language. Do not send repetitive heartbeats, content-free status messages,
hidden reasoning, secrets, credentials, or raw configuration. Progress is an
intermediate update, not a substitute for the final response.

The tool input has three parts:

- `locator` identifies the active target session. Copy `channel_type`,
  `address_key`, and `session_id` from trusted current-session context. Do not
  invent, normalize, or retarget them. Omit the compatibility-only
  `address_json` field unless the current context explicitly supplies it.
- `text` is the bounded user-visible update. State what changed and what happens
  next; include a blocker only when it affects the requested work.
- `idempotency_key` identifies one logical update. Reuse the exact key when
  retrying that same update after an uncertain or transient outcome. Use a new
  stable key for every distinct update, and do not put sensitive data in it.

For example, with placeholder values copied from trusted session context:

```json
{
  "locator": {
    "channel_type": "telegram",
    "address_key": "<chat-id>:<topic-id>",
    "session_id": "tg-<chat-id>-<topic-id>"
  },
  "idempotency_key": "dependency-install-complete",
  "text": "Dependencies are installed. Running the verification suite now."
}
```

Check the returned result before describing the effect. `ok` and `accepted`
confirm that Balda durably accepted the update. `disposition` distinguishes a
conversation update delivered to the session from progress recorded on the
owning schedule or webhook job. `progress_id` is the stable Balda identity for
the accepted update. A delivered disposition does not prove that a human read
the message. On an error or unaccepted result, do not claim delivery; follow the
tool error and retry only when appropriate.

The tool's canonical locator and idempotency contract bound the external effect.
They do not grant authority to send arbitrary messages or select another
delivery destination.

Tool availability never supplies user authorization. Follow the current request,
higher-level policy, repository instructions, and each tool's approval contract
before mutations, external sends, deployment, restart, or publication.
