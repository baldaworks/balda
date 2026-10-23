---
title: Auto mode
description: Continue multi-step work in the current chat session, with explicit controls and limits.
category: Autonomous work
audience: user
status: stable
outline: deep
---

# Auto mode

Auto mode lets Balda continue the current task across several conversational
turns without a new prompt for each step. Use it for a clear task that needs
several rounds of investigation, editing, or verification.

Auto mode is off by default and belongs to one session. Enabling it in a topic
or thread does not enable it in other conversations. It uses that session's
history and tools and remains enabled after a task finishes.

To enable it, check status, steer work, or stop, follow the
[Auto mode guide](/guides/auto-mode). The command is available to authorized
session users in Telegram, Zulip, and Mattermost. Slack does not expose `/auto`.
See the [command matrix](/commands#invocation-and-access).

## What you see

After a normal turn completes, Balda can schedule automatic continuation.
Useful responses and progress appear in the conversation. Internal continuation
instructions and decision tokens are hidden; completion or a request for input
produces a short status notification.

Ordinary messages steer the conversation without disabling Auto mode. A new
ordinary turn starts a new continuation count. Enabling the mode alone does
not start a task.

## Understand the status

`/auto` shows mode on/off, state, consecutive automatic turns and their limit,
last-turn time, and the last stop reason. Read the mode flag alongside the state:
`idle` can also appear when the mode is off or has just been enabled.

| State | Meaning | Next action |
| --- | --- | --- |
| `idle` | No active continuation chain; a completed task returns here. | Send another task, or disable the mode. |
| `running` | A task or its continuation chain is running. | Wait for a result or send steering input. |
| `waiting for user` | The model reported that it needs input, clarification, or approval. | Supply the missing information or decision. |
| `limit reached` | The chain reached its automatic-turn limit. | Review the work, then send a new instruction if needed. |
| `no progress` | An automatic turn repeated the previous visible output. | Review the result and give a different next step. |

## Limits and permissions

The default cap is **10 consecutive automatic turns**, in addition to the
ordinary turn that starts a chain. Completion and waiting decisions also use
an automatic turn. A new ordinary turn starts a new chain.

Balda stops scheduling continuations when the model reports completion or
needs user input, when the turn cap is reached, when consecutive output is
identical after trimming whitespace, or when you disable the mode. Repetition
detection is a guard against identical output; it does not evaluate whether
different responses make useful progress.

Auto mode uses the normal session runtime and permission controls. It does not
run a separate background job or grant extra permissions. Turning it off
prevents future scheduling; it does not cancel running or already queued work.
See [how to stop](/guides/auto-mode#stop-automatic-work).

Administrators can change the cap through the
[configuration reference](/reference/configuration#auto-mode).

## Choose Auto mode or GoalKeeper

Use Auto mode to continue work inside the current conversation, with ordinary
messages for steering. Use [GoalKeeper](/features/#goalkeeper) for a separate
worker-and-validator loop with its own goal lifecycle.

Supplemental contributor contracts are in the repository's
[Auto mode specification](https://github.com/baldaworks/balda/blob/main/docs/architecture/auto-mode.md).
