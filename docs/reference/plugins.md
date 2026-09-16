# Plugins and session capabilities

This page is the canonical reference for Balda's Agent Plugins package support,
Balda-specific command extension, and session capability lifetime. Marketplace
repository layout is documented separately in the
[marketplace format ADR](../architecture/plugin-marketplace-format.md).

## Package boundaries

An installed package uses the Agent Plugins 1.0 `plugin.json` as its portable
manifest. Portable `skills/` and `mcp.json` components keep their Agent Plugins
meaning. Balda chat commands are not portable Agent Plugins commands: they are
client-specific data under the stable Balda client namespace
`extensions["dev.baldaworks.balda"]`.

The namespace and schema identity are deliberately different:

- client extension key: `dev.baldaworks.balda`;
- schema title: `balda-extension`;
- schema `$id`:
  `https://baldaworks.dev/schemas/balda-extension/1.0.0/schema.json`.

Balda embeds that Draft 2020-12 schema and validates the extension offline. It
does not resolve the schema `$id` over HTTP.

## Balda extension version 1

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "release-tools",
  "extensions": {
    "dev.baldaworks.balda": {
      "schema_version": 1,
      "commands": [
        {
          "name": "release",
          "description": "Run the release workflow",
          "instruction": "Inspect the release configuration and execute the release workflow."
        }
      ]
    }
  }
}
```

Both the extension object and every command object are closed. Version 1
requires `schema_version` and `commands`; every command requires exactly
`name`, `description`, and `instruction`. Names are normalized lowercase command
tokens. Arrays and strings are bounded by the embedded schema, and duplicate
normalized names invalidate the extension.

A command owns its inline instruction. It does not reference a skill, command
file, agent, model, permission set, or MCP server. Invocation arguments and the
instruction are formatted as user-level input and enter the ordinary durable
session-turn path. They cannot replace Balda's system policy or change access,
approval, retry, workspace, delivery, or runtime configuration.

## One capability snapshot per session

Balda compiles commands, skill metadata, and MCP server descriptors into an
immutable effective catalog snapshot. Creating a session selects one non-empty
snapshot ID and persists it with the session before the session is exposed.

That snapshot ID fixes all three capability classes for the session lifetime:

- command ingress and `CommandActor` resolve command descriptors from the exact
  retained snapshot;
- provider-visible skill metadata and explicit `$skill:` resolution use that
  snapshot, while selected skill bodies and resources are loaded lazily from
  its exact archived source revision;
- the MCP server list is derived from the same snapshot and supplied when the
  provider session is created, resumed, or loaded. Its acquisitions live until
  that Balda session runtime closes.

MCP is not supplied again with each prompt. A command does not receive an MCP
list and cannot add tools; it merely executes inside the already-created
provider session, where the model may use the tools that session already has.
Every command, ordinary, and selected-skill turn uses the existing
`TopicSession` runner. No turn constructs or swaps a provider runtime.

Catalog activation affects newly created sessions only. Restoring a session
uses its exact persisted snapshot and fails closed if that retained snapshot is
unavailable. A legacy session without a pin adopts the current effective
snapshot once and persists it. `/reset` closes the old runtime and creates a
fresh one, so reset is the explicit boundary at which an existing conversation
adopts current commands, skills, and MCP servers.

Immutable command instructions are retained in snapshot descriptors. Archived
skill bytes remain readable by exact revision. MCP processes are the only
capability resources with a live acquisition that must be released.

## Session instruction contributions

Balda assembles one provider-neutral session instruction before constructing a
provider runtime. Trusted host extensions may contribute bounded, named
sections through the `agent.SessionInstructionContributor` port. Contributions
receive the immutable session snapshot identity and safe session context, are
ordered by stable contributor ID, and fail session construction on invalid
IDs, duplicates, errors, or total-size overflow.

This is a host extension boundary, not a portable plugin-data surface. A
contributor cannot replace the built-in Balda instruction, select a different
snapshot, or write provider-specific session metadata. The provider adapter
remains responsible for mapping the assembled instruction to its native
instruction mechanism. ACP providers without such a mechanism receive the
adapter's documented first-prompt fallback.

## Transport behavior

Plugin command names are projected into each compatible transport's local
command registry. Transport parsing and authentication remain provider-specific,
but accepted invocations publish the same transport-neutral command payload.
`CommandActor` is the sole command executor and the generic plugin adapter
publishes one deduplicated normal session turn.

Built-in names remain reserved, duplicate plugin aliases are omitted, and each
transport still applies its own command-token syntax. See
[Command architecture and runtime internals](command-runtime.md) for the
transport matrix, durable routing, retries, and settlement behavior.

## Relation to other hosts

Codex, OpenCode, and other hosts may define their own native plugin or command
configuration. Those formats do not define Balda's extension. A portable
package can carry host-specific values under separate client namespaces;
Balda reads only `dev.baldaworks.balda` and validates it with `balda-extension`.
