# Transport presentation boundary

Owner: Balda maintainers  
Status: active

## Problem

Balda supports multiple delivery transports with very different output models.
Some transports need prompt-level formatting guidance for model-authored text.
Some also support deterministic structured service messages such as permission
requests, interactive questions, and progress updates.

Without an explicit boundary, transport-specific presentation logic tends to
leak into shared application packages. That causes two failures:

- shared packages branch on concrete transport UX;
- transport packages become impossible to isolate, replace, or run separately.

## Decision

Balda separates transport-neutral message semantics from transport-specific
presentation and delivery behavior.

The generic rule is:

- shared application packages own semantic message types and typed payloads;
- transport packages own all rendering, prompt injection, ingress, and delivery
  details for that transport;
- composition root wiring registers transport capabilities through DI rather
  than through direct imports of transport internals.

## Ownership split

### Shared application layer

Shared application packages may own:

- semantic message kinds and typed payload descriptors;
- transport-neutral contracts for structured service messages;
- transport-neutral routing/registry interfaces;
- workflow policy for when a message should be emitted.

Shared application packages must not own:

- transport-local formatting rules;
- transport-local prompt text;
- transport-local structured renderers;
- provider SDK payload shapes;
- provider message correlation behavior.

Examples:

- `questionfmt`, `permissionfmt`, `progressfmt`, and `locatorfmt` define transport-neutral
  message descriptors and payloads;
- `deliveryfmt` defines registry interfaces and routing contracts;
- `locatorapp` owns canonical locator projection and structured resolution;
- `deliveryfx` assembles shared transport-neutral presentation wiring.

### Transport package subtree

Each concrete transport subtree owns all transport-specific presentation and
delivery behavior for that transport.

That includes:

- ingress decode and normalization;
- delivery/update/respond semantics;
- prompt injection for model-authored text;
- structured renderers for service/control messages;
- provider-local message and conversation correlation.

If a transport needs multiple internal packages, they still stay inside that
transport subtree.

Examples of valid internal subpackages:

- `presentation`
- `prompt`
- `ingress`
- `delivery`
- `correlation`
- `<transport>fx`

## DI boundary

Outer Balda code must not call transport implementation subpackages directly.

The only allowed external surface for a transport is:

- its root transport package for stable transport-facing contracts;
- its `fx` module for DI registration and lifecycle wiring.

This keeps transport internals replaceable and allows a transport to be moved
toward a separate process boundary later without rewriting shared application
code.

Inbound transport implementations invoke generic `chatapp.Handler` and
`commandapp` contracts. Session creation, question continuation, command
policy, and actor publication stay on the application side of that seam.

For example, Slack `/balda` ingress verifies and normalizes the signed form into
`commandapp.Request`, including provider-neutral access and invocation facts.
It calls `commandapp.Handler` without interpreting the subcommand. The shared
command handler owns authorization and validation, delegates locator projection
through its `LocatorResponseRenderer` port, and dispatches the resolved
presentation. `handlersfx` binds that port to `locatorapp.Service`.

## Rendering rule

There are two output paths:

### Model-authored text

Model-authored text stays on the prompt-format path. Transport packages may
inject formatting instructions for their own output constraints.

### System-authored service messages

System-authored messages use typed structured descriptors with deterministic
renderers registered by each transport.

Structured renderers are registered per transport and per message kind. They
must not live in shared message contract packages.

## Locator as an ownership example

The locator response divides ownership as follows:

| Concern | Owner |
|---|---|
| Public `<channel_type>:<address_key>` syntax | `locatorref` |
| Typed `Transport` and `Locator` response contract | `locatorfmt` |
| Canonical projection and registry lookup | `locatorapp` |
| Ingress authorization, arguments, and dispatch | `handlers` and provider ingress adapters |
| Provider markup | each `channel/<transport>/presentation` package |
| Renderer registration | each transport's `<transport>fx` package |
| Formatted delivery payload | `deliverycmd` |

The public destination comes from `commandapp.Request.Locator` /
`deliverycmd.Locator`. `actorlayer.ActorAddress` remains internal routing
metadata for the delivery envelope. Moving either the public locator contract
or rendering policy into `session`, `handlers`, or a concrete channel package
would violate this ownership split.

The descriptor-to-adapter mechanics, registrations, formats, and failure path
are documented once in the
[locator worked flow](presentation-routing.md#worked-flow-locator-response).

## Generic invariant

For any transport `X`:

- `X` may depend on shared contracts;
- shared contracts must not depend on `X`;
- outer application wiring may import `X` root and `Xfx`;
- outer application wiring must not import `X` internal presentation/delivery
  packages;
- all `X`-specific UX behavior stays inside `X`.

## Consequences

### Positive

- transport packages become coherent feature boundaries;
- shared application code stays generic across transports;
- prompt injection and structured rendering can coexist cleanly;
- transport extraction into a separate process stays realistic.

### Negative

- each transport must register its own renderers explicitly;
- DI wiring becomes more deliberate;
- some message kinds need both shared descriptors and per-transport renderers.

## Slackagent as one instance of the rule

`channel/slackagent` follows this generic boundary:

- Slackagent-specific rendering, prompt injection, ingress, and delivery stay
  inside `internal/apps/balda/channel/slackagent`;
- `internal/apps/balda/channel/slackagent/slackagentfx` is the DI boundary;
- shared packages such as `questions`, `permissions`, and `deliveryfmt` stay
  transport-neutral.

Slackagent is an instance of the rule, not the rule itself.

## Telegram as one instance of the rule

`channel/telegram` follows the same boundary:

- Telegram-specific ingress, delivery, question callback handling, structured
  rendering, and prompt-format contributions stay inside
  `internal/apps/balda/channel/telegram`;
- `internal/apps/balda/channel/telegram/telegramfx` is the DI boundary for
  Telegram transport registrations;
- shared packages such as `deliveryfx`, `questionfmt`, `permissionfmt`, and
  `progressfmt` keep only transport-neutral contracts plus non-Telegram shared
  defaults.

That means Telegram rich-format prompt rules and Telegram structured service
renderers are transport-owned registrations, not shared application logic.

## Zulip as one instance of the rule

`channel/zulip` owns Zulip Markdown presentation and delivery. Its `zulipfx`
package registers structured renderers and supplies the ingress adapter. The
current Zulip ingress has its own command normalization path, but it consumes
the same locator application service and does not construct locator markup.
