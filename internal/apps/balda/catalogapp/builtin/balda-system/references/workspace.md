# Workspace runtime

Balda has a configured workspace policy and a resolved per-process mode. Keep
the two concepts separate.

## Configuration policy

`balda.workspace.mode` accepts:

- `on`: require a Git repository and allocate a worktree per session; startup
  fails when the working directory is not a Git repository.
- `off`: run sessions directly in `balda.working_dir` and do not expose the
  `balda.workspace` MCP namespace.
- `auto`: enable worktrees when `balda.working_dir` is a Git repository and
  otherwise resolve to `off`.

The root instruction reports resolved session behavior rather than repeating
the raw configuration value:

- `Mode: git-worktree` identifies an isolated session worktree.
- `Mode: direct` identifies the configured working directory without Balda
  worktree management.

Use the displayed mode, path, branches, and tool inventory. Do not infer live
mode from a config file or from a `base_branch` value that is irrelevant to a
direct session.

## Git worktree mode

Balda records the worktree path and session branch in durable session metadata.
Session branches normally use `norma/balda/<session_id>`. The configured base
branch is the sync and export target.

When the `balda.workspace` namespace is exposed:

- call `balda.workspace.import` with the current session ID before major edits
  or after long idle to rebase the session branch onto the base branch;
- call `balda.workspace.export` with the current session ID and a Conventional
  Commit message to squash-merge session work into the base branch;
- use those tools for base-branch landing instead of manually merging through
  raw Git;
- if export reports that the main repository is on the wrong branch, ask the
  operator to restore the configured base branch.

`/close`, process shutdown, and ordinary session cleanup remove the mounted
worktree. They do not export changes. Export is a separate explicit operation.
On restore, Balda attempts to sync with the base branch. A conflict can leave
the saved session branch mounted without the new base changes; retry with
`balda.workspace.import` after resolving the cause.

## Direct mode

Direct mode has no Balda-managed session branch and no `balda.workspace`
namespace. Work occurs at the path shown in the root instruction.

Do not interpret disabled workspace tooling as disabled Git. Shell access,
filesystem access, repository presence, branch policy, and permission to commit
or push are separate facts. Inspect the available tools and repository, then
follow repository instructions and the user's authorization.

GoalKeeper follows the same configured workspace policy but uses its own worker
and validator sessions. With worktrees enabled, passing goal work is exported
to the base branch. In direct mode it works in `balda.working_dir` and records a
passing run as not exported.
