---
description: Locate and follow the authoritative Backoffice application foundation.
---

## Backoffice Application

- `cmd/balda` is the only executable and composes Backoffice into `balda start`.
- `internal/apps/balda` owns the shared provider and startup order.
- `internal/apps/backoffice` owns Backoffice application behavior, including the Web UI.
- Before changing these areas, you MUST read and follow `docs/reference/backoffice.md`.
- `docs/reference/backoffice.md` is authoritative if this summary and the reference differ.
