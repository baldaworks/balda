# Balda command reference

This is the authoritative reference for Balda's current chat command surface.
Recurring work is configured through `balda.scheduler.jobs`; it is not a chat
command.

## Invocation and access

Telegram and Zulip use one slash command per action, such as `/locator`.
Slack registers one workspace slash command, `/balda`, and the first word of
its text selects the Balda command. The current Slack command surface contains
`/balda locator` and `/balda reset`; `/balda` with no command or any other
subcommand returns the enabled-command usage.

`owner` means the account bootstrapped with the deployment owner token.
`collaborator` means an account connected through an owner-created invite.
Slack's locator command accepts a signed request from a workspace member and
does not use Telegram or Zulip owner records.

| Command | Telegram | Zulip | Slack | Access | Context |
|---|---:|---:|---:|---|---|
| `/start ...` | yes | yes | no | onboarding | direct message |
| `/help` | yes | no | no | anyone | current chat |
| `/topic <name>` | yes | yes | no | owner, collaborator | Telegram direct message; Zulip stream |
| `/goalkeeper ...` | yes | yes | no | owner, collaborator | current session |
| `/auto [on\|off]` | yes | yes | no | owner, collaborator | current session |
| `/usage` | yes | yes | no | owner, collaborator | current session |
| `/reset` | yes | yes | `/balda reset` | owner, collaborator; signed Slack workspace member | current session |
| `/locator` | yes | yes | no | owner, collaborator | current session |
| `/balda locator` | no | no | yes | workspace member | current conversation |
| `/close` | yes | yes | no | owner, collaborator | direct message |
| `/cancel` | yes | yes | no | owner, collaborator | current session |
| `/user ...` | yes | yes | no | owner | direct message recommended |
| `/plugin ...` | yes | no | no | owner | current chat |

Arguments shown in angle brackets are required. Arguments in square brackets
are optional. Commands that accept no arguments return a usage response when
extra text is supplied.

## Onboarding and help

### `/start owner=<owner_token>`

Registers the first owner and bootstraps the owner's direct-message session.
It is valid only in a direct message. A bad or already-consumed token does not
change ownership. Telegram deep links may encode the same argument as
`owner_<owner_token>`.

### `/start invite=<invite_token>`

Consumes an active collaborator invitation and connects the current transport
account to the owner. It is valid only in a direct message. Telegram deep links
may use `invite_<invite_token>`. Invalid, expired, and already-consumed tokens
do not grant access.

### `/start <balda_token>`

Connects the current Telegram or Zulip account to an existing owner with a
generated channel token. It is valid only in a direct message. This does not
replace the owner or create a second owner.

### `/help`

Shows the commands available to the current Telegram user. The owner view also
includes administration and plugin commands. `/help` does not create, restore,
reset, or cancel a session.

## Sessions and automation

### `/topic <name>`

Creates a new session labeled `<name>` with the configured Balda provider.
On Telegram it creates a topic from a direct-message context. On Zulip it
creates or selects the named topic in the current stream; Zulip rejects this
command in a direct message. The name is a session label, not a provider name.
An empty name returns `Usage: /topic <name>`.

### `/goalkeeper <objective>`

Starts durable goal work from the current session context. GoalKeeper uses its
own worker/validator state and, when workspace mode is enabled, an isolated
workspace. It emits started, validation, and final updates. A second active run
for the same session is rejected. The command does not reset ordinary session
history. See [Goal workflow](goal-workflow.md) for execution and export rules.

### `/goalkeeper clear`

Stops active GoalKeeper work for the current session. It does not cancel the
ordinary conversational turn queue and does not affect goal runs in other
sessions. Only the exact single argument `clear` is control syntax; for
example, `clear old deployment` is treated as a new objective.

### `/auto`, `/auto on`, `/auto off`

Reads, enables, or disables automatic continuation for the current session.
The state is session-scoped. Unsupported arguments return the three valid
forms. Toggling auto mode does not reset history or start GoalKeeper.

### `/usage`

Shows the most recently recorded provider usage for the current session. If no
snapshot exists, Balda says so. It does not query a provider or start a turn.

### `/reset`

This command cancels current session work, clears that session's
history, and immediately creates a fresh runtime session at the same locator.
It does not close the underlying chat or topic and accepts no arguments.

### `/close`

Resets the current direct-message session. When the direct-message transport
supports a topic session, it closes that topic after resetting its history.
It does not export workspace changes. Stream or public-chat invocation is
rejected, and arguments are not accepted.

### `/cancel`

Requests cancellation of the current conversational turn and drops queued
turns for the current session. It does not reset history and does not stop an
active GoalKeeper run. It accepts no arguments.

## Locator

### `/locator` and `/balda locator`

Returns the current transport and the canonical public locator in
`<channel_type>:<address_key>` form, followed by paste-ready scheduler/webhook
configuration. Telegram and Zulip require owner or collaborator access. Slack
uses the conversation identified by the signed slash-command payload; Slack
slash commands have no thread timestamp, so the result is conversation-scoped.

The public value is derived from the CommandActor's transport-neutral
`deliverycmd.Locator`. `actorlayer.ActorAddress`
is internal envelope-routing metadata and is never used as the displayed
public locator.

Slack renders `/balda locator` as `mrkdwn`:

````text
📍 *Balda Locator* • *Transport:* `slackagent` • *Locator:* `slackagent:c:T0BFTRBFA94:C0BU4LKUB6W`

*Scheduler / webhook configuration*
```
target: locator
key: slackagent:c:T0BFTRBFA94:C0BU4LKUB6W
```
````

The two configuration lines can be pasted directly into a scheduler job or
webhook route envelope. Telegram selects `rich_markdown`; Zulip selects
`markdown`; both render the same semantic fields with their transport-owned
Markdown syntax.

Successful locator rendering is deterministic and does not create or restore
an agent turn, change session history, or update session status. Invalid
arguments return `Usage: /locator` or `Usage: /balda locator`. If the structured
renderer is absent or fails, Balda sends no partial or plain fallback locator
response. Operators should use the checks in
[Balda operations](reference/topic-sessions.md#locator-response-delivery).

## Command execution architecture

Each transport owns its command syntax and explicit whitelist. For the
actor-migrated commands in this change, the path is:

```text
transport parser + whitelist
  -> ingress access check + durable publish
  -> CommandActor
  -> exact name handler (locator or reset)
  -> session/delivery ports
```

`commandcmd` owns the neutral envelope. `actors/command` owns exact-name
routing and command policy. `commandfx` wires actor ports. Transport packages
do not import those actor or application packages. Other commands retain their
existing handlers until migrated in their own scoped changes.

## User administration

### `/user add`

Creates a collaborator invite link. `/user invite` is retained as an alias.
Only the owner can use it.

### `/user list`

Lists collaborators and active invites. It does not include expired or
consumed invitations as active. Only the owner can use it.

### `/user remove <user_id>`

Removes the collaborator with the exact transport user ID. It does not remove
the owner or reset existing session history. Only the owner can use it.

## Plugin administration

Plugin commands are currently available through Telegram to the owner:

| Command | Effect |
|---|---|
| `/plugin list` | List installed plugins. |
| `/plugin list --available` | List plugins available from configured marketplaces. |
| `/plugin show <plugin[@marketplace]>` | Show resolved plugin details. |
| `/plugin install <plugin[@marketplace]>` | Install a plugin. |
| `/plugin remove <plugin>` | Remove an installed plugin. |
| `/plugin marketplace add <source>` | Add a marketplace source. |
| `/plugin marketplace list` | List configured marketplaces. |
| `/plugin marketplace show <name>` | Show marketplace details. |
| `/plugin marketplace upgrade [name]` | Refresh one or all marketplace snapshots. |
| `/plugin marketplace remove <name>` | Remove a marketplace source. |

An unavailable plugin backend returns an explicit unavailable/not-implemented
response and makes no plugin change. Invalid subcommands or argument counts
return the complete `/plugin` usage.

## Common failures

- A caller outside the command's role receives the existing access-denied
  response; Balda does not run the command action.
- A command used in the wrong chat context receives the command's direct-message
  or stream-only response.
- Missing required arguments and disallowed extra arguments return usage text.
- Runtime, storage, provider, or delivery failures return a concise failure
  message where the transport can reply; detailed context is written to
  structured logs.
- An unknown Zulip command returns `Unknown command: /<name>`. Slack reports
  only the currently enabled `/balda` forms.
