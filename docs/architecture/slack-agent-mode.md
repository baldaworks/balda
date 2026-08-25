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
- normalization of raw provider payloads into Slackagent-local contracts;
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

`internal/apps/balda/handlers` owns only the generic ingress bridge:

- receive HTTP/event input;
- call a DI-provided Slackagent ingress adapter;
- run auth/session preconditions;
- publish actor work.

Handlers must not own:

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

`Slackagent ingress adapter -> session turn envelope -> session actor -> delivery actor or Slackagent responder boundary`

The actor/session core still owns conversation semantics. Slackagent-specific
rendering, status behavior, and correlation stay inside the Slackagent channel
boundary.

## Current implementation direction

The target architecture requires moving Slackagent-specific logic inward from
shared packages and outer adapters.

Initial migration targets:

- Slackagent-specific inbound decoding currently living in handlers;
- Slackagent-specific structured rendering branches currently living outside
  the channel boundary;
- Slackagent-specific prompt injection registered outside channel ownership;
- Slackagent-specific correlation helpers used by question and wait flows;
- Slackagent-specific turn presentation branches implemented in shared turn
  orchestration paths.

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
