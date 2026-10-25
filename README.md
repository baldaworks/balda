# Balda

[![test](https://github.com/baldaworks/balda/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/baldaworks/balda/actions/workflows/test.yml)
[![lint](https://github.com/baldaworks/balda/actions/workflows/lint.yml/badge.svg?branch=main)](https://github.com/baldaworks/balda/actions/workflows/lint.yml)

**A self-hosted engineering agent for team chat.**

Give Balda a task in Telegram, Zulip, Slack, or Mattermost. It works inside
your project with your chosen agent and tools, keeps the conversation context,
and returns work you can review: an answer, changed files, validation results,
or a commit.

Use it for everyday engineering questions, focused tasks in separate topics,
and longer goals that need several passes. Schedules and webhooks can start
work from external events, too.

## What you can do

- **Work from chat.** Ask questions, request changes, and continue with follow-up
  messages in the same conversation.
- **Keep tasks separate.** Give each topic or thread its own session and context.
- **Run longer goals.** Start a GoalKeeper run with progress updates, validation,
  and a final result to review.
- **Use your preferred agent.** Connect Codex, Claude Code, OpenCode, Copilot,
  Gemini, or a configured hosted provider.
- **Connect your tools.** Add MCP servers, skills, and plugins for your workflow.
  [Skill commands](docs/commands.md#skill-skill-prompt) continue the shared topic
  conversation regardless of which participant invokes them.
- **Automate recurring work.** Start private runs from schedules and webhooks
  and send the results back to chat.
- **Manage it in a browser.** Backoffice brings together users, chat archives,
  integrations, schedules, and tools.

Balda runs on infrastructure you control, alongside your repositories and
configured agent runtimes.

## Get started

The guided setup below uses Telegram. You need Node.js/npm, a Telegram bot
token, and a supported agent CLI installed and authenticated. The Balda package
does not bundle agent CLIs. For Zulip, Slack, or Mattermost, follow the
provider setup guides below.

Install Balda and initialize it in your project:

```bash
npm install -g @baldaworks/balda
balda init
balda start
```

`balda init` creates your configuration, detects available agent CLIs, and
prints onboarding instructions and a generated Backoffice administrator
password. Save that password and follow the printed instructions to connect
your chat account.

Send your first task to the bot, then continue the conversation as you would
with a teammate. For a focused topic or a longer goal:

```text
/topic release-notes
/goalkeeper Prepare release notes for the changes since the last release
```

Use `/cancel` to stop the current turn and `/reset` to start a fresh session.
Use `/status` to inspect the provider, model, reasoning effort, session state,
and current turn without changing the runtime.
Command availability varies by chat provider; see the
[command guide](docs/commands.md).

For the full setup, including Docker Compose, see
[getting started](docs/reference/onboarding.md).

## Connect your team chat

| Provider | Conversation scope | Setup |
| --- | --- | --- |
| Telegram | Direct messages and personal/group topics | [Onboarding](docs/reference/onboarding.md) |
| Zulip | Streams and topics | [Zulip guide](docs/zulip-webhook.md) |
| Slack | Agent DMs and mentioned channel threads | [Slack guide](docs/slack.md) |
| Mattermost | DMs and mentioned channel threads | [Mattermost guide](docs/mattermost.md) |

## Make it fit your workflow

Choose your agent, model, and integrations in `.config/balda/config.yaml`.
Backoffice lets administrators manage supported integrations and capabilities
from the browser.

- [Configuration](docs/reference/configuration.md)
- [Telegram formatting](docs/telegram-formatting.md)
- [Backoffice](docs/reference/backoffice.md)
- [Schedules](docs/features/schedules.md)
- [Webhooks](docs/features/webhooks.md)
- [Goal-driven work](docs/goal-workflow.md)
- [Standalone skills](docs/reference/skills.md)
- [Plugins](docs/reference/plugins.md)
- [Agent hooks](docs/reference/agent-hooks.md)

## Documentation and development

Start with the [documentation index](docs/balda.md) for the complete reference,
or the [operations guide](docs/reference/operations.md) when maintaining an
installation.

Want to contribute? See [CONTRIBUTING.md](CONTRIBUTING.md) and the
[architecture map](docs/architecture/index.md).

[GitHub Releases](https://github.com/baldaworks/balda/releases) ·
[npm](https://www.npmjs.com/package/@baldaworks/balda) ·
[MIT license](LICENSE)
