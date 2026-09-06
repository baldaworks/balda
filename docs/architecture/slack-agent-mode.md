# Slackagent Mode

Owner: Balda maintainers  
Status: active

## Problem

Balda supports Slackagent as a dedicated channel integration with its own
transport and UX model:

- different ingress/event surfaces;
- different response lifecycle primitives such as status and streaming;
- different conversation/message correlation rules;
- different rollout constraints because agent features require Slack paid plans
  or the Slack developer sandbox.

Trying to spread Slackagent behavior across shared packages would couple
distinct transport contracts and leak Slackagent semantics into application
code that must stay transport-neutral.

## Decision

Balda exposes Slackagent through its own `slackagent` channel path.

The Slackagent feature boundary is:

- `internal/apps/balda/channel/slackagent`
- `internal/apps/balda/channel/slackagent/slackagentfx`

`channel/slackagent` owns all Slackagent-specific transport behavior.
`channel/slackagent/slackagentfx` is the only composition boundary exported to
the rest of the application.

Everything outside that boundary must stay transport-neutral and must not branch
on Slackagent-specific UX, rendering, or correlation rules.

## Scope

### `slackagent`

Use the Slackagent integration for:

- agent-native inbound events;
- signed `/balda` slash-command ingress;
- agent conversation identity and context;
- status/progress lifecycle;
- optional streaming and suggested prompts;
- agent-specific response affordances.

This mode must not overload shared handlers or transport-neutral delivery
semantics.

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
semantics. They do not own Slackagent transport behavior.

### Slackagent channel boundary

`internal/apps/balda/channel/slackagent` owns:

- ingress decoding and verification for Slackagent requests;
- normalization of Events payloads into Slackagent-local contracts and slash
  payloads into the shared `commandapp.Request` contract;
- Slackagent conversation/message correlation;
- Slackagent-specific delivery semantics;
- Slackagent-specific responder behavior;
- Slackagent-specific presentation rendering;
- Slackagent-specific prompt-format injection;
- capability reporting needed for transport wiring.

`internal/apps/balda/channel/slackagent/slackagentfx` owns:

- `fx` module wiring;
- DI registration of Slackagent ingress adapters;
- DI registration of Slackagent delivery adapters;
- DI registration into `deliveryfmt.PromptRegistry`;
- DI registration into `deliveryfmt.StructuredMessageRegistry`;
- lifecycle wiring for Slackagent ingress/runtime pieces.

This `slackagentfx` module is the only external composition surface for the
Slackagent channel package.

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

It registers Slackagent behavior into the process and prevents outer packages
from manually assembling Slackagent internals.

Application root wiring should depend on `slackagentfx`, not on individual
Slackagent implementation subpackages.

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

These are internal implementation details of the Slackagent channel. The rest
of Balda must not depend on them directly.

## Integration rules

### Handlers

`internal/apps/balda/handlers` owns the generic chat and command handlers:

- accept transport-neutral `chatapp.Request` and `commandapp.Request` values;
- run application authorization and session preconditions;
- settle question replies;
- publish actor work through actorlayer contracts.

Handlers must not own:

- Slackagent HTTP endpoints or signature verification;
- raw Slackagent payload structs;
- Slackagent payload normalization logic;
- Slackagent reply-correlation helpers;
- Slackagent presentation decisions.

### Delivery presentation

Transport-neutral presentation packages such as:

- `questionfmt`
- `permissionfmt`
- `progressfmt`

own only:

- typed payloads;
- message descriptors/kinds;
- transport-neutral registration contracts.

They must not contain Slackagent rendering branches.

Concrete Slackagent renderers are registered from inside the Slackagent
channel boundary through `slackagentfx`.

### Questions, permissions, wait, and turn execution

The following remain transport-neutral:

- `questions`
- `permissions`
- `sessionturnapp`
- `actors`
- `controlapp`

They may emit generic question, delivery, progress, or permission requests, but
they must not branch on Slackagent UX details.

Question is an application contract that works through any transport. It is not
a Slackagent-owned feature.

## Import rules

Allowed imports from outside the Slackagent subtree:

- `internal/apps/balda/channel/slackagent`
- `internal/apps/balda/channel/slackagent/slackagentfx`

Disallowed imports from outside the subtree:

- `internal/apps/balda/channel/slackagent/ingress`
- `internal/apps/balda/channel/slackagent/delivery`
- `internal/apps/balda/channel/slackagent/presentation`
- `internal/apps/balda/channel/slackagent/prompt`
- `internal/apps/balda/channel/slackagent/correlation`
- any future Slackagent implementation subpackage

If outer code needs Slackagent behavior, it must get it through root contracts
or through `slackagentfx` DI wiring.

## Required contracts

Balda owns Slackagent-local transport contracts in
`internal/apps/balda/channel/slackagent`:

- `ConversationRef`: stable conversation identity for agent mode;
- `Event`: normalized inbound event contract;
- `Capabilities`: startup/runtime capability snapshot;
- `Responder`: final response and UX-affordance boundary;
- `MessageRef`: provider message/thread correlation for question and wait flows.

These contracts are transport-facing but must avoid leaking raw Slack request
payload shapes into higher-level Balda actor/session code.

## Execution model

### Slackagent

`Slackagent ingress -> optional thread hydration -> chatapp.Handler -> SessionTurn envelope -> session actor -> Slackagent delivery adapter`

The actor/session core still owns conversation semantics. Slackagent-specific
rendering, status behavior, and correlation stay inside the Slackagent channel
boundary.

For an `app_mention` inside an existing channel thread, the ingress adapter
loads a bounded snapshot through `conversations.replies` and formats it as
author-attributed untrusted background before invoking the transport-neutral
`chatapp.Handler`. The generic handler owns session preparation, question
continuation settlement, and the first durable `SessionTurn` dispatch. History
requests, Slack timestamps, pagination, error classification, and prompt
enrichment remain inside `channel/slackagent`; `slackagentfx` owns only their DI
composition.

There is no Slack-specific product actor or intermediate actor command. The
first durable actor command remains `SessionTurn`, and shared `turncmd`,
`session`, `actors`, `execution`, and actorlayer contracts remain Slack-neutral.
Ordinary channel callbacks never become turns; every channel turn requires a
fresh mention, independent of existing session state.

## Dependency boundary

The Slackagent transport root depends only on transport-neutral contracts such
as `chatapp`, `deliverycmd`, `deliveryfmt`, `questioncmd`, `turncmd`, and
actorlayer. It must not import concrete application services such as `session`,
`questions`, `controlapp`, `ingressapp`, or `handlers`.

`slackagentfx` may see both sides of the boundary to bind generic application
handlers and lifecycle groups to Slackagent ports. It must not contain reusable
workflow policy; its adapters translate calls only.

## Question and wait

`slackagent` must support the same product features as other Balda sessions:

- question delivery must target the same Slackagent conversation;
- replies must settle against provider conversation/message references, not text
  heuristics;
- wait wake-ups must return to the same conversation context with preserved
  timing metadata.

Ownership split stays explicit:

- question and wait lifecycle remain shared application behavior;
- Slackagent reply correlation and delivery rendering remain inside the
  Slackagent channel boundary.

Slackagent support is not considered complete until question and wait behavior
are verified end-to-end in the same conversation lane with stable provider
correlation.

## Capability gating

Balda must not guess at runtime whether Slackagent mode is available.

Startup/preflight should explicitly model:

- whether `slackagent` is enabled;
- whether the configured workspace/app appears capable of Slackagent mode.

Mode mismatch should produce explicit diagnostics rather than silent fallback.

## Consequences

- Slackagent evolves behind a real channel boundary instead of leaking into
  shared application packages.
- outer layers remain transport-neutral;
- DI wiring becomes explicit through `slackagentfx`;
- future extraction of Slackagent behavior into a more separable feature or
  process becomes easier because transport-local logic is kept together.

## Acceptance criteria

- `slackagent` has a separate channel boundary.
- all Slackagent-specific rendering lives under `channel/slackagent`.
- handlers no longer implement Slackagent payload normalization directly.
- transport-neutral presentation packages no longer branch on `slackagent`.
- no Slackagent-specific transport semantics leak into actorlayer or shared
  application lifecycle packages.
- composition root wiring imports `slackagentfx` instead of assembling Slack
  Agent internals ad hoc.

## Update triggers

- New Slack transport mode or capability checks.
- New question/wait routing requirements for Slack.
- Any change to Slack session identity or response lifecycle.
- Any change to the DI/export boundary of the Slackagent channel package.
