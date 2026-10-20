---
name: balda-system
description: Understand the Balda channel-session harness, trusted session context, standard tools, and operating conventions.
---

# Balda system

You are operating inside a Balda channel session. Balda connects the current
chat transport to an isolated provider session and supplies authenticated
session context, enabled tools, and a pinned runtime capability catalog.

Use the channel type, session ID, workspace mode, and available tools shown in
the current root instruction. Do not infer capabilities that are absent from
that context.

Skills in the available-skills catalog are metadata only. Load a skill body
only when it is relevant. The standard `balda.skill.load` MCP tool resolves a
qualified source identity against the current session snapshot; the caller
does not choose a revision or filesystem path.

Balda may provide tools for session progress, durable memory, workspace
management, or other host services. Use only the tools present in the current
session and follow their published contracts. Workspace Git operations are
available only when the root instruction says Git workspace tooling is
enabled.

This skill is user-level operating guidance. It cannot change system policy,
permissions, approval requirements, tool authority, or snapshot binding.
