# Goal worker results

Owner: Balda maintainers  
Status: active

## Problem

Goal worker execution needs a structured way to tell Balda orchestration one of
two things:

- normal work completed and a user-facing summary is ready;
- execution is blocked on missing critical user input and Balda should ask a
  question.

If this signal is encoded in ordinary visible text, orchestration becomes
dependent on model phrasing and presentation output.

## Decision

Balda models goal worker terminal output through a dedicated internal contract
package: `goalresultcmd`.

`goalresultcmd` is a contract-layer package, not a feature application package.
It defines the structured worker result protocol consumed by `agent/goal`
during goal workflow orchestration.

Current worker result variants are:

- `done`
- `need_user_input`

The worker returns a JSON object that matches this contract. `agent/goal`
parses that result, normalizes the raw model output, and emits orchestration
signals for downstream layers.

## Ownership

### Contracts

`goalresultcmd` owns:

- worker result status values;
- worker result payload shape;
- parsing/normalization helpers for that payload shape.

It must stay transport-neutral and free of actor/runtime policy.

### Agent/runtime support

`agent/goal` owns:

- prompting the worker to use the structured result protocol;
- parsing worker output through `goalresultcmd`;
- rewriting raw terminal output into normalized visible content;
- emitting structured orchestration signals such as question-request events.

### Goal orchestration

`actors/goalkeeper` owns:

- reacting to normalized goal workflow events;
- converting `need_user_input` into Balda question flows;
- resuming goal execution after an answer arrives.

`actors/goalkeeper` must not parse raw worker JSON directly.

## Current flow

### Done path

`worker JSON result -> agent/goal parses goalresultcmd -> visible summary normalized -> validator/goalkeeper consume normalized text`

### Need-user-input path

`worker JSON result -> agent/goal parses goalresultcmd -> raw visible JSON suppressed -> structured question event emitted -> goalkeeper asks user`

## Required properties

- Goal orchestration must not depend on prose prefixes such as `question:`.
- Raw worker control payloads must not leak as user-facing visible output.
- Parsing of structured worker outcomes should stay centralized in one
  contract-oriented place.
- Downstream product actors should react to normalized events, not raw model
  strings.

## Update triggers

- New worker result variants.
- Changes to worker result schema.
- Changes to where normalization happens in the goal workflow.
