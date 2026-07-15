# Slack Agent Mode

Owner: Balda maintainers  
Status: proposed

## Problem

Balda already supports Slack as a chat-style channel integration built on Slack Events API, slash commands, DMs, and channel threads. That integration maps well to ordinary bot-style conversation, but Slack AI Agents use a different product model:

- different ingress/event surfaces;
- different response lifecycle primitives such as status and streaming;
- different UX shell and capability model;
- different rollout constraints because agent features require Slack paid plans or the Slack developer sandbox.

Trying to extend the current Slack chat path in-place would couple two different transport contracts and leak Slack agent semantics into the existing Slack chat runtime.

## Decision

Balda keeps the current Slack integration as `slack_chat` and adds a separate `slack_agent` ingress/runtime path alongside it.

Both modes reuse the same Balda core:

- actor runtime and durable dispatch;
- session turns;
- jobs, wait, and question flows;
- auth and ownership;
- provider execution;
- observability and read models.

The modes diverge only at Slack-specific ingress and response boundaries.

## Scope split

### `slack_chat`

Use the existing Slack channel integration for:

- DMs;
- `app_mention` conversational threads;
- slash commands such as `/balda topic`;
- bot-style plain or `mrkdwn` final responses.

This path remains the compatibility/default Slack mode and keeps existing wire/storage contracts:

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
- future agent-specific response affordances.

This mode must not repurpose `slack_chat` handlers or overload Slack chat-specific delivery semantics.

## Layering

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

### Slack-mode-specific boundaries

- `internal/apps/balda/handlers` owns Slack ingress adapters.
- `internal/apps/balda/channel/slack` owns current Slack chat delivery behavior.
- future `slack_agent` response primitives stay behind Slack agent-local contracts and do not become generic actorlayer concepts.

## Required contracts

Balda should introduce Slack agent-local contracts before implementing the full mode:

- `SlackConversationRef`: stable conversation identity for agent mode;
- `SlackAgentEvent`: normalized inbound event contract;
- `SlackAgentCapabilities`: startup/runtime capability snapshot;
- `SlackAgentResponder`: final response and UX-affordance boundary;
- `SlackAgentMessageRef`: provider message/thread correlation for question and wait flows.

These contracts should be transport-facing but must avoid leaking raw Slack request payload shapes into higher-level Balda actor/session code.

## Execution model

### Slack chat

`Slack chat ingress -> session turn envelope -> session actor -> delivery actor -> Slack chat adapter`

### Slack agent

`Slack agent ingress -> session turn envelope -> session actor -> delivery actor or Slack agent responder boundary`

The actor/session core still owns conversation semantics. Slack agent-specific rendering/status behavior stays outside product actor business logic.

## Question and wait

`slack_agent` must support the same product features as other Balda sessions:

- question delivery must target the same Slack agent conversation;
- replies must settle against provider conversation/message references, not text heuristics;
- wait wake-ups must return to the same conversation context with preserved timing metadata.

Slack agent support is not considered complete until question and wait work in the same conversation lane.

## Capability gating

Balda must not guess at runtime whether Slack agent mode is available.

Startup/preflight should explicitly model:

- whether `slack_chat` is enabled;
- whether `slack_agent` is enabled;
- whether the configured workspace/app appears capable of Slack agent mode.

Mode mismatch should produce explicit diagnostics rather than silent fallback.

## Rollout plan

### Milestone 1: MVP

- config and preflight wiring;
- normalized Slack agent contracts;
- ingress skeleton;
- session mapping;
- thinking/status lifecycle;
- final response path;
- basic observability.

Exclude:

- streaming;
- suggested prompts;
- advanced context search;
- rich feedback UI.

### Milestone 2: parity with Balda interaction model

- question integration;
- wait integration;
- stable provider message correlation.

### Milestone 3: richer Slack agent UX

- streaming;
- suggested prompts;
- optional richer agent affordances.

## Consequences

- Balda can keep a free-plan-compatible Slack bot path through `slack_chat`.
- Slack AI Agents can be added without breaking current Slack storage and operator contracts.
- Transport-specific agent semantics remain out of `pkg/actorlayer` and out of shared session/product contracts.
- Future Slack work is clearer: chat compatibility and agent UX evolve independently.

## Acceptance criteria

- `slack_chat` remains behaviorally compatible.
- `slack_agent` has a separate ingress/runtime slot.
- no Slack agent-specific transport semantics leak into actorlayer.
- question and wait eventually work in agent mode with stable conversation correlation.
- architecture docs and preflight explain the two-mode model clearly.

## Beads breakdown

1. Architecture
   - ADR and architecture docs for `slack_chat` vs `slack_agent`.
2. Contracts
   - `SlackConversationRef`, `SlackAgentEvent`, `SlackAgentCapabilities`, `SlackAgentResponder`, `SlackAgentMessageRef`.
3. Config and preflight
   - mode toggles, capability checks, diagnostics.
4. Ingress
   - request verification, normalization, dedupe.
5. Session mapping
   - stable conversation-to-session binding.
6. MVP responder
   - thinking/status and final response.
7. Question integration
   - conversation-local question/answer settlement.
8. Wait integration
   - conversation-local wake-up continuation.
9. Observability
   - logs, counters, diagnostics.
10. Streaming and richer UX
   - optional streaming and suggested prompts.

## Update triggers

- New Slack transport mode or capability checks.
- New question/wait routing requirements for Slack.
- Any change to Slack session identity or response lifecycle.
