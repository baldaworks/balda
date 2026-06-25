# Multi-Channel Token Auth

This is a design artifact, not a full ADR.

## Problem

Balda can run on Telegram, Slack, and Zulip, but owner and collaborator auth used
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
- Slack: `slack:<team_id>:<user_id>`
- Zulip: `zulip:<sender_id>`

One owner record can have multiple channel bindings. Legacy owner fields remain
readable so existing installs continue to work.

## Channel Flows

- Telegram consumes owner-bind tokens through `/start <balda_token>` or
  `https://t.me/<bot_username>?start=<balda_token>`.
- Slack consumes owner-bind tokens when the user DMs Balda the exact token or
  sends `/balda start <balda_token>` in a DM.
- Zulip consumes owner-bind tokens when the user DMs Balda the exact token or
  sends `/start <balda_token>` in a DM.

Existing syntax stays compatible:

- `/start owner=<token>` and `/start invite=<token>`
- Telegram deeplink payloads `owner_<token>` and `invite_<token>`
- Slack `/balda start owner=<token>` and `/balda start invite=<token>`
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

## Removed Static Whitelist

Slack and Zulip `allowed_owners` static whitelist auth is removed. Ownership is
claimed through the first-owner setup token or through generated channel-bind
tokens.

## Acceptance Criteria

- A Telegram owner can generate and consume `balda_...` tokens to bind Slack or
  Zulip accounts.
- Slack and Zulip can consume exact-token DMs before normal message routing.
- Legacy owner and invite token syntax continues to work.
- A numeric Telegram user ID does not authorize a Zulip user with the same
  numeric ID.
- `allowed_owners` config, docs, and auto-claim runtime behavior are absent.
