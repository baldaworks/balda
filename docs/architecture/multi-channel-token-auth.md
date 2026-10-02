# Multi-Channel Token Auth

This is a design artifact, not a full ADR.

## Problem

Balda can run on Telegram, Slackagent, and Zulip, but owner and collaborator auth used
to be tied to channel-specific command syntax. Telegram used `/start owner=...`
and deeplink payload prefixes, Slack used `/balda start owner=...`, and Zulip
used `/start owner=...`. That made it hard to treat one human owner as the same
principal across many channels.

## Selected Model

Balda uses transparent bearer tokens for cross-channel auth. A token is an
opaque `balda_...` value; storage metadata defines what it does. Channel
handlers extract possible tokens from normal onboarding paths and pass them to a
shared auth service before normal command or message routing.

Channel-qualified subjects identify accounts:

- Telegram: `telegram:<user_id>`
- Slackagent: `slackagent:<team_id>:<user_id>`
- Zulip: `zulip:<sender_id>`

One owner record can have multiple channel bindings. Legacy owner fields remain
readable so existing installs continue to work.

The canonical user store also permits multiple `(channel_type, principal)`
bindings for one user, while each pair belongs to at most one user. Backoffice
administrators manage invitations and confirmed bindings for configured
Telegram, Slack Agent, Zulip, and Mattermost integrations. A bound principal resolves the user's current role and
status at authorization time. A verified Telegram event may refresh the
binding's optional username and first name; neither profile field grants access.

## Backoffice-selected account proof

Backoffice invitations are a separate `bind_<32 URL-safe characters>` contract.
An administrator selects the exact existing user and one configured integration.
The browser supplies neither a transport principal nor an integration instance.
Each adapter verifies its bot identity and exposes safe metadata through
`auth.BindingChannels`; Backoffice consumes local ports wired by the host.
`usercmd` owns the shared invitation, proof and issued-value contracts.

`auth.BindingInvitations` generates a 24-hour, single-use credential. State owns
its dedicated digest-only table and the transaction that attaches the verified
sender, consumes the invitation and records the audit event. It preserves the
selected user's role and primary designation. Issuance/replacement/cancellation
use current administrator and target/version checks; disabling a user revokes
pending invitations. No owner bootstrap credential is required.

Telegram `/start` and exact DM, signed Slack DM/native start, token-verified Zulip
webhook DM/start, and authenticated Mattermost WebSocket DM/native start admit
proof before ordinary unbound-user rejection. Mattermost retains its existing
Direct and canonical locator semantics for D and G conversations. Its slash
option is offered only when configured; the bot DM remains usable otherwise.
Wrong instance, channel, replay, expiry and principal conflicts fail closed;
failed attachment leaves a still-valid invitation available.

Invitation payloads, including mixed or damaged credential messages, are
quarantined before model/command queues. Slack and Mattermost fetched history
filters credential messages before context hydration. Telegram and Zulip's
current intake does not fetch provider history; quoted webhook/message input is
filtered too. Logging redaction applies the shared credential recognizer.
Only the issuance POST reveals the value; later GET returns pending metadata.
See [Backoffice application](../reference/backoffice.md) for browser contracts.

## Channel Flows

- Telegram consumes owner-bind tokens through `/start <balda_token>` or
  `https://t.me/<bot_username>?start=<balda_token>`.
- Slackagent consumes owner-bind tokens when the user DMs Balda the exact token or
  sends `/balda start <balda_token>` in a DM.
- Zulip consumes owner-bind tokens when the user DMs Balda the exact token or
  sends `/start <balda_token>` in a DM.

Existing syntax stays compatible:

- `/start owner=<token>` and `/start invite=<token>`
- Telegram deeplink payloads `owner_<token>` and `invite_<token>`
- Slackagent `/balda start owner=<token>` and `/balda start invite=<token>`
- Zulip `/start owner=<token>` and `/start invite=<token>`

When an already authenticated owner uses the normal onboarding command again,
Balda can return single-use owner-bind tokens for channels that are not yet
connected.

## Token Handling

Generated channel tokens are single-use, expiring bearer credentials. Balda
stores only a hash of the raw token in app KV state and deletes the record after
successful consumption.

Current token purpose:

- `owner_bind`: binds the consuming channel subject to the existing owner.

Collaborator invite tokens keep the existing invite flow and command
compatibility.

Interactive permission controls are additionally bound to the exact user who
initiated the active turn. Owner status and collaborator status permit normal
bot access but do not override that responder binding. If authorization state
is unavailable, callback handling fails closed.

## Removed Static Whitelist

Slackagent and Zulip `allowed_owners` static whitelist auth is removed. Ownership is
claimed through the first-owner setup token or through generated channel-bind
tokens.

## Acceptance Criteria

- A Telegram owner can generate and consume `balda_...` tokens to bind Slackagent or
  Zulip accounts.
- Slackagent and Zulip can consume exact-token DMs before normal message routing.
- Legacy owner and invite token syntax continues to work.
- A numeric Telegram user ID does not authorize a Zulip user with the same
  numeric ID.
- `allowed_owners` config, docs, and auto-claim runtime behavior are absent.
