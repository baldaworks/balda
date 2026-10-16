# Backoffice chat session archive

Status: proposed for user approval after builder review.
Tracking: `balda-ax16`.
Feature base: `origin/main` at `3a58254bb05747fdeecde070b1828e770ed589a4`.

## Intent and accepted scope

Port AlatooGuide's conversation browsing experience into Balda Backoffice,
using individual session lifetimes as the primary records. The user wants to
open a session and read its user/agent chat. Archive retention is essential:
reset and close must not remove the transcript from Backoffice.

The user accepted a synthetic interactive mockup containing a session inventory
and a separate chat detail page. It shows two sessions at the same location
before and after reset, rather than one continuous location transcript. The user
explicitly approved administrator-only access to all archived sessions.

Location metadata includes channel kind, user name when known, and conversation
classification such as private or topic. These are display metadata bound to a
session, not its identity. The implementation uses the existing Go SSR,
AdminLTE, HTMX and embedded assets, shared state provider, and browser security.

## Authoritative foundations

Read these together before implementation:

- [Runtime contract](../../architecture/runtime-contract.md).
- [Session and messaging behavior](../../reference/messaging.md).
- [Topic sessions](../../reference/topic-sessions.md).
- [Command reference](../../commands.md), especially reset, close and cancel.
- [Application zones](../../architecture/application-zones.md).
- [Backoffice application](../../reference/backoffice.md).
- [Backoffice UI review](../../reference/backoffice-ui-review.md).
- [State database](../../reference/database.md) and [contribution workflow](../../../CONTRIBUTING.md).

AlatooGuide references are `../alatooguide/backoffice/docs/conversation-projection.md`,
`internal/web/conversation_views.go`, `internal/webui/templates/conversations.html`
and `internal/transcript`. Its location-level conversation aggregate is useful
prior art for presentation, but does not define Balda archive identity.

## Identity and lifecycle

A location, a stable Balda routing/session ID, a provider session ID, and an
archived session lifetime are distinct concepts. Durable Balda creation and
fresh-session fallback can reuse the same ADK session ID. A provider ID alone
cannot distinguish archived sessions after reset.

Introduce an opaque, durable archive session ID for each fresh runtime lifetime.
Persist its current binding with Balda session metadata and carry it through
accepted conversational work and final-response capture. Never change public
locators, actor lane keys, stable Balda IDs or provider ID construction to make
the archive work. Browser detail URLs use the archive ID.

| Runtime action | Archive behavior |
| --- | --- |
| First successful creation | Create a new archive session, even before its first user message. |
| Successful exact restore after restart | Reuse the persisted archive ID and transcript. |
| Reset | Retain/end the old archive; successful fresh creation binds a new archive at the same location. |
| Close | Retain/end the old archive; a later fresh runtime receives a new archive ID. |
| Automatic fresh-session fallback | End the old archive as rotation and bind a new archive after successful fresh creation. |
| Cancel | Keep the same archive session and retain accepted messages. |
| Shutdown with durable conversation persistence | Preserve the current binding for lazy restore; shutdown alone does not end the logical session. |
| Process-local conversation persistence | Keep transcripts durable, but a new process-local runtime receives a new archive ID; do not merge across lost runtime state. |

An active archive means an unended logical session, including one awaiting lazy
restore. It does not claim that a provider process is currently running.
Runtime unloading or temporary suspension must not be mistaken for destructive
close simply because an existing observer reports a close-like boundary.

Session lifecycle remains owned by `session.Manager`. Its existing
`BoundaryObserver` reports the old identity before cleanup and includes a
transition ID; it is an integration seam, not proof that a transition succeeded.
Add the narrow activation/completion observations needed to distinguish fresh
creation, exact restore, destructive close and suspension. Bindings and archive
state changes must follow successful lifecycle outcomes, with idempotent durable
transition records and recovery for crashes between runtime operations and SQL
commits. Do not mark a failed reset as successfully completed or expose a failed
runtime creation as an active session. A pending/uncertain transition must be
recoverable and visibly distinct from an ordinary active/archived record.

Archive messages already committed are never removed by runtime cleanup, even
if a boundary observer fails. Do not rely on copying ADK history during reset.
Lifecycle retries must not create several archive rows for one successful
activation. Persist activation identity before acknowledging creation to callers.

## Archived data and metadata

Use dedicated SQL archive tables through the existing selected state provider,
with SQLite and PostgreSQL Goose migrations. They have no cascading deletion
from live session metadata, runtime history, users or transport bindings.

An archive session contains its ID, stable Balda and observed provider IDs,
canonical channel/address identity, label, start/end timestamps, end reason,
predecessor/successor relationship where known, and conversation metadata.
Store audience (`personal`, `group`, `unknown`) independently from subdivision
(`root`, `topic`, `thread`, `unknown`). A personal topic is not a group chat.
Classification comes from trusted normalized transport information or existing
channel codec metadata, not parsing assumptions in Backoffice.

Capture known location titles and human display names/usernames as snapshots.
Provider usernames and canonical Backoffice display names remain separate
fields, as required by the existing binding contract. Group sessions can have
multiple speakers; record the actual sender per message and do not label one
participant as owner of every message. Missing metadata stays unknown. No
provider API calls are made while browsing; renaming/removing a user or binding
does not erase historical sender labels. Safe verified observations can fill
missing location metadata without rewriting old message sender snapshots.

Each message has an immutable identity, archive session ID, role (`user` or
`agent`), visible text, sender snapshot where applicable, occurrence timestamp,
stable source identity, and sequence/order metadata. Store only allowlisted
display metadata for an attachment-only message and show a neutral attachment
label; copying binary files, signed URLs or provider credentials is outside scope.

Retain messages until an explicit future retention feature is designed; there
is no expiry, delete, edit, export or bulk-clear operation in this increment.
The archive is durable even when `balda.sessions.persistence: memory` is used.
Document the resulting disk usage and data-retention behavior without adding
another database or an optional episodic-memory dependency.

## Message capture

The builder's proposed definition of chat is accepted human input and final
visible agent replies produced for that session. It does not assert that the
external transport delivered or the person read an agent reply. Delivery
settlement remains a separate existing concern. Do not add delivered/read
indicators without actual evidence.

Capture user input durably at the serialized conversational acceptance boundary,
before execution can fail or cancellation can discard queued work. Capture each
original logical user message once, including steering constituents. Use stable
inbound dedupe identities, not retry attempt numbers or transient timestamps.
Preserve the user's visible input separately from Slack history enrichment,
quoted context, skill instructions, prompts and other runtime augmentation.
Concrete adapters may normalize the display metadata; they do not own archival
policy or persistence.

Capture final agent text at the turn execution owner's final-response selection
seam, before downstream fallible bookkeeping can discard it. Do not derive it
by reading all ADK events into a browser model. Store final selected text once
per logical response, including visible final responses from failed turns when
actually produced. Never capture reasoning, tools, internal prompts, state
deltas, progress, typing, partial drafts, or provider errors as chat messages.
Plain browser rendering escapes text and preserves whitespace; rich Markdown
rendering is not required for the first increment.

Durable archival admission failures before execution are retryable with the
same message identity. A capture failure after a reply was already selected
must use durable pending capture/recovery rather than rerunning the model or
resending a message solely to repair history. The implementation plan must
identify the commit and replay boundary for each path; a warning-only hook is
not sufficient. This feature does not change delivery outbox selection or claim
exactly-once external delivery.

Pin archive identity into durable work when the current session is resolved.
Late replies and replayed envelopes retain that identity across reset; they
cannot consult the location's new current binding and append to the next
session. Stale queued work must follow existing reset/cancel semantics instead
of executing against the replacement runtime. Archived rows may accept late
observations belonging to their original lifetime; their end timestamp/reason
does not change merely because an earlier operation completes late.

The optional `session_memory` integration is not the archive: it filters for
eligible completed turns, may be disabled, and owns semantic memory rather than
complete chat retention. Preserve its existing observers and ordering.

First-increment scope is ordinary conversational sessions on Balda's supported
chat transports. Private scheduler, webhook and GoalKeeper execution internals
remain in their existing job/result surfaces. A report delivered to a chat does
not turn its private execution into that chat's session. Bot command chatter,
welcome banners and system progress are not new transcript roles.

## Backoffice pages

Add administrator-only **Chat sessions** under Workspace. Operators neither see
the navigation link nor access the list/detail routes. Use normal browser
authentication, current-role checks, refresh recovery and private/no-store HTML,
including HTMX fragments. No extra passkey ceremony or new permission role.

`GET <backoffice_path>/chat-sessions` lists sessions by most recent message
activity, falling back to start time for empty sessions, then a stable tie-breaker.
Show label, start time, channel, location kind/title, user when meaningful,
active/archived status and end reason, last activity and message count. Group
sessions must not imply a single user; use a group/participant summary instead.
Search known label/user/location metadata; filter by channel and active/archived
state. Preserve filters in bounded cursor pagination. Transcript full-text
search is outside scope.

`GET <backoffice_path>/chat-sessions/{archive_id}` shows session/location
metadata above the chronological user/agent chat. The newest bounded message
window is the default; **Older messages** follows a stable cursor. Keep an
explicit empty-chat state, unavailable state and back link. Link to preceding
or following archived sessions at that location where the relationship exists.
Long names, locators and multiline messages must wrap. A malformed cursor is
400, an unknown archive ID is 404, and unavailable storage is a safe 503.
Neither opening page creates/restores a runtime nor contacts a chat provider.

Adapt the accepted dark mockup to production Go templates and shared CSS tokens.
Keep default filters and the two-page layout; do not turn the prototype's
JavaScript-generated HTML into production code. The accepted prototype is
throwaway design evidence, not a shipped asset or a new frontend dependency.

## Ownership and enforcement

Proposed package names are builder choices, while these ownership constraints
are fixed:

| Owner | Responsibility |
| --- | --- |
| `session` | Decide lifecycle transitions and expose small local observation ports. |
| Dedicated archive contract package (for example `chatarchivecmd`) | Transport-neutral archive IDs, metadata, message values and queries. |
| Dedicated archive application owner (for example `chatarchive`) | Capture validation, identity/idempotency policy, lifecycle projection and browsing use cases. |
| `state` | SQL migrations, durable archive records, transitions and pending capture, guarded persistence and read models. |
| `chatapp` / `actors` / `sessionturnapp` | Invoke local consuming ports at their existing acceptance/execution seams; preserve ordinary turn ownership. |
| Dedicated composition adapters | Connect lifecycle, turn and Backoffice ports to the archive owner without concrete cross-layer imports. |
| Backoffice | Administrator routes, escaped view models, production templates and synthetic QA fixtures. |
| `channel/*` | Provider-specific normalization only. |

Do not place shared archive types in `session` or concrete channels, add
Backoffice logic to transport adapters, or modify generic actorlayer for Balda
product policy. Update `.go-arch-lint.yml` with the new contracts, owner and
wiring in the implementation, tightening dependencies instead of granting broad
application access. Preserve config/startup order and lazy restore.

## Upgrade and failure recovery

The first increment captures forward from upgrade. It does not reconstruct
already-reset sessions, backfill full ADK events, or treat existing episodic
memory as complete historical chat. Adopt each existing live/restorable session
into one archive lifetime on its first supported observation, without eager
provider restoration. Mark it as captured-since-upgrade so start time and history
are not falsely presented as complete. Adoption is idempotent and commits before
any subsequent destructive lifecycle operation; if reset is its first observed
operation, preserve an empty adopted archive for the old lifetime.

Additive schema migration runs during ordinary state-provider opening before
ingress. Startup/replay recovers pending lifecycle/capture operations from their
durable identities without launching user work or re-sending replies merely for
archive repair. A crash or ambiguous transition must never guess a different
session's identity. Back up the selected database before deployment. There is
no engine transfer or schema downgrade feature; rollback uses a compatible
binary and database backup. Archive data is included in the existing database
backup procedure.

## Acceptance and implementation validation

The implementation must prove:

- Reset yields separate old/new archive IDs with one location and preserved
  old messages; close and automatic fresh fallback retain separate lifetimes.
- Exact restore keeps archive identity, and ordinary shutdown does not split a
  durable session; memory-mode runtime replacement does split it.
- Failed/retried activation and boundary operations, crash recovery, late output
  and replay never merge lifetimes or duplicate logical messages.
- Accepted user messages survive failed/canceled turns; batched and steering
  inputs remain distinct, and only final visible agent text becomes agent chat.
- Metadata snapshots distinguish personal/group and root/topic/thread, retain
  actual speakers, tolerate unknown fields, and survive binding deletion.
- SQLite/PostgreSQL stores and migrations retain the archive across live/runtime
  session deletion, database reopen and cursor pagination.
- Administrator authorization, operator denial, escaping, root/prefixed mounts,
  native/HTMX navigation and refresh recovery work on actual authenticated pages.
- Synthetic production-template previews cover list/detail, reset/close,
  group/unknown metadata, empty/no-match, pagination and safe error states at the
  four documented viewport sizes, followed by the authenticated browser gate.

Run the required race suite, linter and architecture gate using current
`CONTRIBUTING.md` budgets, both tagged backend integration suites for persistence
changes, and the ordered browser layout/application checks. Record evidence in
Beads. Update `README.md`, `docs/reference/backoffice.md`, the UI review runbook,
session/messaging retention references and architecture ownership/enforcement
with implementation. No product Go code changes are part of this design commit.

## Calls left to the builder

The proposals for indefinite retention, forward-only capture, plain escaped
message text, original accepted input plus generated final reply semantics,
attachment-only labels, filters, cursor sizes and package naming are builder
calls for user review, rather than requirements explicitly dictated by the user.
The implementation plan chooses SQL layout/indexes, narrow port methods,
bounded query/capture limits and durable transition/pending-capture mechanics
that satisfy the outcomes above. It must not leave reset identity, replay
behavior or archival durability to warning-only best effort.
