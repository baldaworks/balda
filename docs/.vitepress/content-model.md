# Balda documentation content model

This file defines the information architecture for Balda documentation. It
lives inside `.vitepress` so VitePress does not publish it as a user page.

## Content types

| Directory | Reader question | Content contract | Website |
| --- | --- | --- | --- |
| `features/` | What can Balda do for me? | Explain value, use cases, user-visible behavior, limits, and next actions. | Published |
| `guides/` | How do I complete a task? | Give an outcome-oriented sequence with prerequisites and verification. | Published when present |
| `reference/` | What is the exact contract? | Record exhaustive commands, settings, accepted values, and operational details. | Published unless explicitly excluded |
| `operations/` | How do I run Balda safely? | Cover deployment, maintenance, recovery, and troubleshooting. | Published when present |
| `architecture/` | How is Balda implemented? | Preserve engineering decisions, runtime internals, and contributor contracts. | Repository only |
| `superpowers/` | What design work led here? | Preserve working specifications and historical design evidence. | Repository only |

Root-level pages may act as landing pages or focused compatibility guides. New
user-facing content should use one of the directories above rather than adding
another root-level category.

## Ownership of facts

Each fact has one canonical layer:

- feature pages own user-visible behavior and recommended use;
- guides own task sequences;
- reference pages own exhaustive syntax and configuration keys;
- architecture pages own implementation details and internal protocols.

Other pages summarize and link to the canonical fact. They should not copy
large sections that can drift independently.

Feature pages may show the current default for a setting when it changes the
user experience. The configuration reference remains authoritative for its
key, accepted values, and environment mapping.

## VitePress page metadata

Published pages use VitePress frontmatter. The required fields are:

```yaml
---
title: Page title
description: One sentence used in previews and search results.
category: Product area
audience: user
status: stable
outline: deep
---
```

`title`, `description`, and `outline` are native VitePress fields. `category`,
`audience`, and `status` are Balda taxonomy fields available to the VitePress
theme through page frontmatter.

Allowed `audience` values are `user`, `operator`, and `developer`. Allowed
`status` values are `experimental`, `stable`, and `deprecated`.

## Routes and links

VitePress derives routes from file paths. Use `index.md` for a section landing
page and do not add a separate slug field. Write internal links without `.md`
or `.html`, using site routes such as `[Auto mode](/features/auto-mode)`.

The site build excludes `architecture/**`, `superpowers/**`, the legacy
technical index, and implementation-focused reference pages listed in
`config.mts`. Published pages must not depend on an excluded page to explain
required user behavior; they may link to repository-only material as
supplemental engineering context.

The Markdown renderer converts links to existing repository-only Markdown
files into GitHub source links, preserving query strings and fragments.
Published-page links remain local, and VitePress dead-link checking stays
enabled. Missing repository files are not exempted from that check. Inline
code renders literally, including Go template expressions such as `{{.Name}}`,
without modifying protected document bodies.
