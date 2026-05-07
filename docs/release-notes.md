# Release Notes

## Next Patch Release

- Keeps `/start owner=<owner_token>` for owner bootstrap and `/start invite=<invite_token>` for collaborator onboarding.
- Keeps Telegram-safe deep-link payloads as `owner_<token>` and `invite_<token>`.
- Reprints the owner bootstrap command and deeplink on `relay start` while the relay is still unclaimed, then stops exposing them again after the first successful owner auth.
- Fails startup when a registered owner session cannot be restored or created.
- Restores owner sessions through persisted session metadata, including workspace branch/path state, before falling back to fresh create.
- Surfaces ACP plan updates in Telegram progress and keeps them configurable via `relay.telegram.plan_updates`.
- Restores workspace-backed sessions after restart even when base-branch auto-sync conflicts, with a warning to retry via `relay.workspace.import`.
- Adds `relay --version` output with release version, commit, and build date metadata.
- Aligns CI linting with the project-local Go toolchain and adds `govulncheck` to the release-readiness gate.
