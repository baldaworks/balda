# Changelog

This file records notable changes to Balda.

## [0.9.0] - 2026-10-22

### Added

- Added locator-scoped progress reporting for active sessions, with durable
  server ownership and ephemeral Telegram delivery
  ([#88](https://github.com/baldaworks/balda/pull/88),
  [#94](https://github.com/baldaworks/balda/pull/94),
  [#95](https://github.com/baldaworks/balda/pull/95)).
- Added the protected `balda-system` skill and runtime guides for workspace,
  CLI, session, and tool behavior
  ([#90](https://github.com/baldaworks/balda/pull/90),
  [#91](https://github.com/baldaworks/balda/pull/91),
  [#93](https://github.com/baldaworks/balda/pull/93)).
- Added the structured `/status` command for inspecting the current session and
  turn without changing runtime state
  ([#96](https://github.com/baldaworks/balda/pull/96)).

### Changed

- Decoupled startup and preflight from agent-provider launch so configuration
  and base runtime readiness can be checked without starting the selected
  provider ([#92](https://github.com/baldaworks/balda/pull/92)).
- Replaced Backoffice acknowledgement checkboxes with Bootstrap confirmation
  dialogs and added Baldaworks branding
  ([#87](https://github.com/baldaworks/balda/pull/87),
  [#97](https://github.com/baldaworks/balda/pull/97)).
- Made Backoffice browser tests reproducible and reduced CI duration with
  timing artifacts, sharded race-enabled tests, and reused SQLite migration
  fixtures ([#98](https://github.com/baldaworks/balda/pull/98)).

### Fixed

- Reconciled session deletion after a reset deadline so timed-out cleanup can
  recover safely ([#89](https://github.com/baldaworks/balda/pull/89)).

[0.9.0]: https://github.com/baldaworks/balda/compare/v0.8.0...v0.9.0
