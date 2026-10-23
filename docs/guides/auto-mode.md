---
title: Use Auto mode
description: Enable automatic continuation, verify its status, steer a task, and stop it in one session.
category: Autonomous work
audience: user
status: stable
outline: deep
---

# Use Auto mode

## Before you start

Use an authorized Telegram, Zulip, or Mattermost session with a working agent
provider. Complete [onboarding](/reference/onboarding) first if you have not
connected your account. Send an ordinary message and confirm that Balda replies
before enabling the mode.

For Mattermost slash commands, enable `balda.mattermost.commands_enabled`, set
`balda.mattermost.commands_token` to the integration token, and register the
command with method `POST` and Request URL
`<balda.http.base_url><balda.http.base_path>/gateway/mattermost/commands`.
The Mattermost server must be able to reach that URL through the
[shared HTTP listener](/reference/configuration#shared-http-listener-and-public-urls).
For example, a public origin of `https://balda.example.com` and prefix `/balda`
give `https://balda.example.com/balda/gateway/mattermost/commands`.

Choose a concrete task and a completion condition. For behavior and limits,
see [Auto mode](/features/auto-mode).

## Enable it and give a task

1. In the session where you want work to continue, send:

   ```text
   /auto on
   ```

2. Check the reply for mode `on`, then send your task:

   ```text
   Review the API documentation against the current handlers, fix stale examples,
   and verify every changed link. Stop when the documentation checks pass.
   ```

3. Send `/auto` to inspect the mode, state, turn count and limit, and stop reason.
   Balda sends useful responses as it continues; it sends a short state update
   when the model reports completion or needs your input.

On Mattermost, use the command shape registered by your administrator. A single
root command uses `/balda auto on`, `/balda auto`, and `/balda auto off` instead.

## Steer or resume work

Send an ordinary message to clarify the task or change the next step. Messages
do not disable Auto mode. If the state is `waiting for user`, reply with the
missing information or approval decision. If it is `limit reached` or
`no progress`, review the result and give a concrete new instruction.

When a task finishes, the state returns to `idle` and the mode stays on. The
next task in this session can therefore continue automatically too.

## Stop automatic work

Send `/auto off` to disable future continuation scheduling. Check `/auto` for
mode `off`. This does not cancel a running turn or remove an already queued
continuation.

To stop active work and drop queued conversational turns as well, disable the
mode, then send `/cancel` in the same session. Cancellation is requested
asynchronously; inspect the resulting messages before starting another task.
The [command reference](/commands) describes these controls and their channel
availability. GoalKeeper work has its own `/goalkeeper clear` control.

## Change the turn cap

Ask your administrator to change `balda.features.automode.max_turns` and restart
Balda. The [configuration reference](/reference/configuration#auto-mode) owns
the setting, default, and environment override. Send `/auto on` again after the
change to apply the configured cap to this session, then verify it with `/auto`.
