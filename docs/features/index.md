---
title: Features
description: Choose a Balda workflow for conversational tasks, autonomous work, tools, and automation.
category: Overview
audience: user
status: stable
outline: deep
---

# Features

Balda is a self-hosted engineering agent for team chat. Ask questions, request
repository changes, review results, and steer work in the same conversation.
What it can execute depends on your configured provider, tools, workspace, and
permission policy.

## Work in chat

Connect Telegram, Zulip, Slack, or Mattermost. Topics and threads keep focused
sessions separate; people working in the same session share its conversation
history and capabilities. Start with [onboarding](/reference/onboarding), then
use the [command reference](/commands) for channel availability and access.

Use `/status` to inspect a session, `/cancel` to stop its conversational work,
and `/reset` to start fresh. Slack uses `/balda status` and `/balda reset` and
has its own stop interaction. It does not expose every slash command.

## Continue multi-step tasks

[Auto mode](/features/auto-mode) continues the current session across multiple
turns until the model reports completion or needs input, the configured cap is
reached, or output repeats. It stays enabled for later tasks in that session.
Follow the [Auto mode guide](/guides/auto-mode) to enable, check, steer, and stop
it. Auto mode is available in Telegram, Zulip, and Mattermost.

## GoalKeeper

Send `/goalkeeper <objective>` for goal work with separate worker and validator
sessions, progress updates, and a final result. Only one goal run can be active
per chat session. Use `/goalkeeper clear` to stop that goal; `/cancel` controls
ordinary conversational work.

With workspace mode enabled, a passing goal attempts to export its work to the
configured base branch. With workspace mode off, it works directly in the
configured working directory. Review the final result, including export or
validation failures. The [command reference](/commands#sessions-and-automation)
records syntax and access. The repository's
[goal workflow contract](https://github.com/baldaworks/balda/blob/main/docs/goal-workflow.md)
provides supplemental runtime and export details.

## Add skills, plugins, and tools

Use `/skill <skill> [prompt...]` to apply an installed skill to an ordinary
turn; Slack uses `/balda skill`. Administrators can install standalone skills
or plugins and configure MCP servers for external tools. Existing sessions keep
their selected capabilities; use `/reset` when you deliberately want a fresh
session with the current catalog.

See [standalone skill management](/reference/skills),
[plugins and session capabilities](/reference/plugins), and
[MCP configuration](/reference/configuration#mcp-server-configuration).

## Start work from schedules or webhooks

Schedules start recurring work; authenticated webhook events can start work
from other systems. Both run in private sessions independent of the chat that
receives a report. A report destination is optional; when configured, it
receives the final output or a failure message rather than progress or
interactive permission questions. Work that needs a live permission decision
cannot obtain one through that report destination.

Administrators manage definitions in Backoffice or deployment configuration.
See the [configuration reference](/reference/configuration#balda-settings)
for schedule and webhook settings. These are separate workflows from Auto mode.

## Manage a deployment

Backoffice provides browser-based administration for users, providers, plugins,
MCP servers, skills, schedules, webhooks, fact memory, and retained chat
sessions. Available pages depend on the signed-in user's role. Chat archives
are read-only; browsing them does not resume work.

Start with [onboarding](/reference/onboarding) and
[deployment configuration](/reference/configuration). Supplemental operational
and engineering contracts remain in the repository's
[Backoffice reference](https://github.com/baldaworks/balda/blob/main/docs/reference/backoffice.md).
