# Slack Agent Mode

Owner: Balda maintainers  
Status: active

## Problem

Balda already supports Slack as a chat-style channel integration built on Slack
Events API, slash commands, DMs, and channel threads. That integration maps
well to ordinary bot-style conversation, but Slack AI Agents use a different
transport and UX model:

- different ingress/event surfaces;
- different response lifecycle primitives such as status and streaming;
- different conversation/message correlation rules;
- different rollout constraints because agent features require Slack paid plans
  or the Slack developer sandbox.

Trying to extend the current Slack chat path in-place would couple two distinct
transport contracts and leak Slack agent semantics into shared application
packages.

## Decision

Balda keeps the current Slack integration as `slack_chat` and adds a separate
`slack_agent` channel path alongside it.

The Slack Agent feature boundary is:

- `internal/apps/balda/channel/slackagent`
- `internal/apps/balda/channel/slackagent/slackagentfx`

`channel/slackagent` owns all Slack Agent-specific transport behavior.
`channel/slackagent/slackagentfx` is the only composition boundary exported to
the rest of the application.

Everything outside that boundary must stay transport-neutral and must not branch
on Slack Agent-specific UX, rendering, or correlation rules.

## Scope split

### `slack_chat`

Use the existing Slack channel integration for:

- DMs;
- `app_mention` conversational threads;
- slash commands such as `/balda topic`;
- bot-style plain or `mrkdwn` final responses.

This path remains the compatibility/default Slack mode and keeps existing
wire/storage contracts:

- `balda.slack.*` public config;
- locator refs with channel type `slack`;
- persisted `channel_type = "slack"`;
- owner/auth subjects like `slack:<team_id>:<user_id>`.

### `slack_agent`

Use a separate integration for Slack AI Agents mode:

- agent-native inbound events;
- agent conversation identity and context;
- status/progress lifecycle;
- optional streaming and suggested prompts;
- agent-specific response affordances.

This mode must not repurpose `slack_chat` handlers or overload Slack
chat-specific delivery semantics.

## Shared core vs channel boundary

### Shared Balda core

The following stay shared and Slack-mode-agnostic:

- `pkg/actorlayer`;
- `internal/apps/balda/execution`;
- `internal/apps/balda/jobs`;
- `internal/apps/balda/actors`;
- `internal/apps/balda/session`;
- `internal/apps/balda/questions`;
- `internal/apps/balda/scheduledjobs`;
- `internal/apps/balda/state`.

These packages own runtime, actor, session, question, permission, and wait
semantics. They do not own Slack Agent transport behavior.

### Slack Agent channel boundary

`internal/apps/balda/channel/slackagent` owns:

- ingress decoding and verification for Slack Agent requests;
- normalization of raw provider payloads into Slack Agent-local contracts;
- Slack Agent conversation/message correlation;
- Slack Agent-specific delivery semantics;
- Slack Agent-specific responder behavior;
- Slack Agent-specific presentation rendering;
- Slack Agent-specific prompt-format injection;
- capability reporting needed for transport wiring.

`internal/apps/balda/channel/slackagent/slackagentfx` owns:

- `fx` module wiring;
- DI registration of Slack Agent ingress adapters;
- DI registration of Slack Agent delivery adapters;
- DI registration into `deliveryfmt.PromptRegistry`;
- DI registration into `deliveryfmt.StructuredMessageRegistry`;
- lifecycle wiring for Slack Agent ingress/runtime pieces.

This `slackagentfx` module is the only external composition surface for the
Slack Agent channel package.

## Public boundary

### `internal/apps/balda/channel/slackagent`

The root package is the stable channel boundary. Outer Balda code may import it
for channel-facing contracts only.

Keep here:

- `ChannelType`;
- stable locator/address helpers;
- stable normalized transport contracts:
  - `ConversationRef`;
  - `Event`;
  - `MessageRef`;
  - `Capabilities`;
  - `Responder`;
- top-level adapter/service entrypoints intended to be wired by `slackagentfx`.

Do not treat the root package as a grab bag for generic application logic.

### `internal/apps/balda/channel/slackagent/slackagentfx`

This package is the DI/composition boundary.

It registers Slack Agent behavior into the process and prevents outer packages
from manually assembling Slack Agent internals.

Application root wiring should depend on `slackagentfx`, not on individual
Slack Agent implementation subpackages.

## Internal layout

The exact subpackage set may evolve, but ownership stays inside the
`channel/slackagent` subtree.

Examples of valid internal subpackages:

- `ingress`: HTTP/event verification, raw payload decode, normalization,
  dedupe identity extraction;
- `delivery`: concrete provider send/update/respond behavior;
- `presentation`: deterministic rendering for structured service messages;
- `prompt`: prompt-format injection for model-authored text;
- `correlation`: provider-local message and conversation correlation helpers.

These are internal implementation details of the Slack Agent channel. The rest
of Balda must not depend on them directly.

## Integration rules

### Handlers

`internal/apps/balda/handlers` owns only the generic ingress bridge:

- receive HTTP/event input;
- call a DI-provided Slack Agent ingress adapter;
- run auth/session preconditions;
- publish actor work.

Handlers must not own:

- raw Slack Agent payload structs;
- Slack Agent payload normalization logic;
- Slack Agent reply-correlation helpers;
- Slack Agent presentation decisions.

### Delivery presentation

Transport-neutral presentation packages such as:

- `questionfmt`
- `permissionfmt`
- `progressfmt`

own only:

- typed payloads;
- message descriptors/kinds;
- transport-neutral registration contracts.

They must not contain Slack Agent rendering branches.

Concrete Slack Agent renderers are registered from inside the Slack Agent
channel boundary through `slackagentfx`.

### Questions, permissions, wait, and turn execution

The following remain transport-neutral:

- `questions`
- `permissions`
- `sessionturnapp`
- `actors`
- `controlapp`

They may emit generic question, delivery, progress, or permission requests, but
they must not branch on Slack Agent UX details.

Question is an application contract that works through any transport. It is not
a Slack Agent-owned feature.

## Import rules

Allowed imports from outside the Slack Agent subtree:

- `internal/apps/balda/channel/slackagent`
- `internal/apps/balda/channel/slackagent/slackagentfx`

Disallowed imports from outside the subtree:

- `internal/apps/balda/channel/slackagent/ingress`
- `internal/apps/balda/channel/slackagent/delivery`
- `internal/apps/balda/channel/slackagent/presentation`
- `internal/apps/balda/channel/slackagent/prompt`
- `internal/apps/balda/channel/slackagent/correlation`
- any future Slack Agent implementation subpackage

If outer code needs Slack Agent behavior, it must get it through root contracts
or through `slackagentfx` DI wiring.

## Required contracts

Balda owns Slack Agent-local transport contracts in
`internal/apps/balda/channel/slackagent`:

- `ConversationRef`: stable conversation identity for agent mode;
- `Event`: normalized inbound event contract;
- `Capabilities`: startup/runtime capability snapshot;
- `Responder`: final response and UX-affordance boundary;
- `MessageRef`: provider message/thread correlation for question and wait flows.

These contracts are transport-facing but must avoid leaking raw Slack request
payload shapes into higher-level Balda actor/session code.

## Execution model

### Slack chat

`Slack chat ingress -> session turn envelope -> session actor -> delivery actor -> Slack chat adapter`

### Slack agent

`Slack agent ingress adapter -> session turn envelope -> session actor -> delivery actor or Slack agent responder boundary`

The actor/session core still owns conversation semantics. Slack Agent-specific
rendering, status behavior, and correlation stay inside the Slack Agent channel
boundary.

## Current implementation direction

The target architecture requires moving Slack Agent-specific logic inward from
shared packages and outer adapters.

Initial migration targets:

- Slack Agent-specific inbound decoding currently living in handlers;
- Slack Agent-specific structured rendering branches currently living outside
  the channel boundary;
- Slack Agent-specific prompt injection registered outside channel ownership;
- Slack Agent-specific correlation helpers used by question and wait flows;
- Slack Agent-specific turn presentation branches implemented in shared turn
  orchestration paths.

## Question and wait

`slack_agent` must support the same product features as other Balda sessions:

- question delivery must target the same Slack agent conversation;
- replies must settle against provider conversation/message references, not text
  heuristics;
- wait wake-ups must return to the same conversation context with preserved
  timing metadata.

Ownership split stays explicit:

- question and wait lifecycle remain shared application behavior;
- Slack Agent reply correlation and delivery rendering remain inside the Slack
  Agent channel boundary.

Slack agent support is not considered complete until question and wait behavior
are verified end-to-end in the same conversation lane with stable provider
correlation.

## Capability gating

Balda must not guess at runtime whether Slack agent mode is available.

Startup/preflight should explicitly model:

- whether `slack_chat` is enabled;
- whether `slack_agent` is enabled;
- whether the configured workspace/app appears capable of Slack agent mode.

Mode mismatch should produce explicit diagnostics rather than silent fallback.

## Consequences

- Balda keeps a free-plan-compatible Slack bot path through `slack_chat`.
- Slack AI Agents evolve behind a real channel boundary instead of leaking into
  shared application packages.
- outer layers remain transport-neutral;
- DI wiring becomes explicit through `slackagentfx`;
- future extraction of Slack Agent behavior into a more separable feature or
  process becomes easier because transport-local logic is kept together.

## Acceptance criteria

- `slack_chat` remains behaviorally compatible.
- `slack_agent` has a separate channel boundary.
- all Slack Agent-specific rendering lives under `channel/slackagent`.
- handlers no longer implement Slack Agent payload normalization directly.
- transport-neutral presentation packages no longer branch on `slack_agent`.
- no Slack agent-specific transport semantics leak into actorlayer or shared
  application lifecycle packages.
- composition root wiring imports `slackagentfx` instead of assembling Slack
  Agent internals ad hoc.

## Update triggers

- New Slack transport mode or capability checks.
- New question/wait routing requirements for Slack.
- Any change to Slack session identity or response lifecycle.
- Any change to the DI/export boundary of the Slack Agent channel package.
