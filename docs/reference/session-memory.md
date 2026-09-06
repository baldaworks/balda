# Durable session memory

## Overview

`balda.session_memory` is an optional episodic-memory integration. It is not a
replacement for the existing fact store: `balda.memory.read` and
`balda.memory.remember` remain global-per-instance KV operations in
`${balda.state_dir}/state.db`. Session memory sends eligible conversation text
through `sessionmemory/app` to the public canonical Badger adapter and recalls
it through a rebuildable Bleve projection only on demand. This is an in-process
extraction boundary, not a remote service.

Session-memory backend state is grouped below `balda.state_dir`:

```text
${balda.state_dir}/session-memory/
├── badger/       # authoritative canonical state
└── bleve/        # rebuildable search projection
```

The canonical Badger path is authoritative and remains stable across restart.
On upgrade, the old direct-child paths
`${balda.state_dir}/session-memory.badger` and
`${balda.state_dir}/session-memory-bleve` are relocated into this subtree
before session memory opens. The local move is idempotent and preserves
canonical data; an old/new path conflict fails closed rather than silently
shadowing data. If no canonical path exists, enabled session memory starts
empty. The removed SQLite session-memory domain tables are not imported;
migration `00033` drops those six legacy tables while preserving the ingress
outbox and audit tables from migrations `00030`–`00032`. The migration `Down`
section recreates an empty legacy schema, but dropped rows cannot be recovered.

## Enablement and configuration

The feature is disabled unless `balda.session_memory.enabled=true`. Disabled
mode is deliberately inert: malformed optional memory values are ignored,
no memory stream or consumer is created, and
the existing fact-memory/session-restore path is unchanged.

The smallest enabled configuration is:

```yaml
balda:
  session_memory:
    enabled: true
    # Empty uses balda.provider; set memory_fast as shown below for a separate model.
    provider: ""
    trusted_tools:
      - calendar.lookup
    derivation:
      timeout: 30s
      max_output_bytes: 262144
    stream: BALDA_SESSION_MEMORY
    consumer: BALDA_SESSION_MEMORY_WORKER
```

All `balda.session_memory.*` keys also accept the corresponding
`BALDA_SESSION_MEMORY_*` environment override (nested keys use underscores).

`balda.session_memory.provider` references an entry in `runtime.providers` and
controls only the isolated fact/semantic extraction runtime. `balda.provider`
continues to own chat and GoalKeeper. If the session-memory selector is empty,
enabled memory uses `balda.provider` for backwards compatibility; if both are
empty, startup fails. The selected provider's existing `model` and
`reasoning_effort` settings are applied without copying chat settings. Omit
`reasoning_effort` to make no explicit reasoning request. Resolution and
provider schema/factory validation are local and structural; the provider
process remains lazy and no remote probe is performed.

Example with a cheaper extraction model and no explicit reasoning:

```yaml
runtime:
  providers:
    codex:
      type: codex_acp
      codex_acp: {}
    memory_fast:
      type: codex_acp
      codex_acp:
        model: gpt-5-mini
        # reasoning_effort intentionally omitted

balda:
  provider: codex
  session_memory:
    enabled: true
    provider: memory_fast
```

The shipped example in `cmd/balda/balda.yaml` lists every default. The enabled
defaults are:

| Key | Default | Meaning |
|---|---:|---|
| `derivation.timeout` / `derivation.max_output_bytes` | `30s` / `262144` | Bounded isolated Norma derivation call and output. |
| `stream` / `consumer` | `BALDA_SESSION_MEMORY` / `BALDA_SESSION_MEMORY_WORKER` | Dedicated JetStream names; collisions with command/event/DLQ names fail startup. |
| `ack_wait` / `fetch_wait` | `5m` / `1s` | JetStream acknowledgement deadline and worker fetch wait. |
| `publish_timeout` / `publish_attempts` | `2s` / `3` | Bounded pre-PubAck handoff retry. |
| `max_age` / `max_bytes` / `max_msg_size` | `7d` / `-1` / `-1` | Stream retention; `-1` means unlimited for byte/message limits. |
| `max_concurrent_scopes` / `max_queued_per_scope` | `4` / `32` | Maximum independent provider lanes and unresolved exports buffered per exact scope. |
| `search_timeout` | `5s` | MCP search deadline. |
| `retry.max_attempts` | `5` | Worker attempts per export before DLQ. |
| `retry.base_delay` / `max_delay` | `250ms` / `5s` | Exponential retry delay bounds. |
| `retry.progress_interval` | `30s` | `InProgress` heartbeat while native derivation or retry is running. |
| `retry.fetch_error_delay` | `100ms` | Delay after a transport fetch error. |
| `retry.shutdown_timeout` | `30s` | Maximum worker drain/close interval. |

Enabled mode validates derivation bounds and stream/consumer identifiers.
Values are parsed before the runtime starts, so an invalid enabled
configuration fails fast rather than silently disabling export. No provider
URL, token, or external HTTP service is required.

## What is exported and how scopes split

After ADK emits `TurnComplete`, Balda exports one completed turn only when it
has non-empty visible user text and final visible assistant text. Thought
parts, tool calls/results, binary parts, partial responses, and interrupted or
non-completed turns are excluded. The export contains the stable Balda session
identity, current provider-session identity, optional lineage, source turn ID,
completion time, and the two text messages. A native processor failure is logged as a
bounded diagnostic and does not suppress the already completed user-facing
reply.

The exact canonical locator string, produced by `locatorref`, is the canonical
Badger partition and authorization key:

```text
<channel_type>:<address_key>
```

The scope kind (`personal` or `group`) is metadata classified by the concrete
channel codec; it never replaces the exact key. A personal root locator,
personal-topic locator, group locator, and group-topic locator are separate
partitions. Topic identity is orthogonal to audience. There is no
owner, collaborator, channel-type, chat-ID, or topic inheritance, and no
cross-scope fallback. Ambiguous or unsupported locator classification fails
closed. Group conversation text is sent only to the exact group locator's
trusted canonical scope.

Session reset, close, rotation, and bounded application shutdown also publish a
boundary export with the old session identity before that identity is removed
or rebound. Boundary order follows completed-turn order, so a later session
cannot overtake an unresolved earlier export.

## Search and trace MCP contract

When enabled, the bundled MCP server registers
`session_memory.search` and `session_memory.trace`. Search input
keeps the original query/limit contract and accepts additive filters:

```json
{"query":"release checklist","limit":10,"memory_kind":"state","category":"decision","as_of":"2026-08-06T10:00:00Z"}
```

Trace input is only a native revision identity and a bounded node count:

```json
{"item_id":"…","revision_id":"…","max_nodes":64}
```

The current authenticated Balda runtime supplies the locator and stable
session headers out of band; callers cannot select an arbitrary locator in the
tool schema. The response echoes the exact scope and returns bounded
references. Each reference is explicitly `untrusted_reference` data. Balda
does not execute recalled text, turn it into a prompt/system instruction, or
interpret it as a transport/command request. Invalid query/scope, unsupported
 scope, disabled service, timeout, unavailable canonical store, and foreign results
produce stable structured tool errors without leaking raw memory content.

The resolver is fail-closed: a request without an active broker capability gets
`invalid_scope` and cannot fall back to a caller-provided locator. Each enabled
Balda session gets an isolated provider runtime whose bundled MCP URL carries
an opaque server-side capability; the locator and session identity are injected
only by the broker. Trace validates the complete provenance closure and refuses
forgotten sources, cycles, foreign scopes, and over-bound graphs.

## JetStream durability, retry, and operations

Session-memory transport uses a producer-local durable ingress outbox before
JetStream publish and a separate canonical post-mutation delivery outbox. It
does not add a transport-owned `state.Provider` surface. The canonical Badger
Store owns memory records, operation outcomes, projection checkpoints, and
delivery state. When enabled, Balda creates:

- file-backed work-queue stream `BALDA_SESSION_MEMORY` for
  `balda.v1.session_memory.turn` and `balda.v1.session_memory.boundary`;
- `DiscardNew` retention to preserve older pending exports under configured
  age/byte/message pressure;
- durable explicit-ack consumer `BALDA_SESSION_MEMORY_WORKER` with
  `MaxAckPending=1`, so one FIFO delivery is processed at a time;
- terminal diagnostics on `BALDA_DLQ` before the source message is
  `Term`-inated.

The capture path first persists a stable `export_id` in the ingress outbox,
publishes it with a JetStream message ID, and settles the outbox only after
`PubAck`. A crash before acknowledgement leaves the same export available for
retry after restart; duplicate publication with the same ID is safe. The
worker retries transient native processor failures in place, sends `InProgress`
heartbeats during slow calls and backoff, acknowledges only after processor
success, and publishes a redacted diagnostic before terminating permanent or
exhausted failures. Canonical post-mutation delivery uses its own bounded
outbox and does not change the user-visible reply boundary.

Operators can inspect backlog without reading message bodies by pointing the
NATS CLI at the running runtime:

```bash
nats --server "$NATS_URL" stream info BALDA_SESSION_MEMORY --json
nats --server "$NATS_URL" consumer info BALDA_SESSION_MEMORY BALDA_SESSION_MEMORY_WORKER --json
nats --server "$NATS_URL" stream info BALDA_DLQ --json
```

The first two commands show stream messages, pending deliveries,
ack-pending deliveries, age, and redelivery counters. The DLQ consumer name is
operator-chosen; create a read-only durable consumer filtered to
`balda.v1.dlq.command` when inspecting terminal diagnostics. Diagnostics carry
export ID, kind, subject, sequence, delivery count, stable error code/class,
and a safe reason—never conversation text or model response bodies. After
fixing the processor/configuration, replay must be an explicit operator action
from a trusted source (or re-run the source turn); there is no automatic DLQ
replay that could duplicate unreviewed conversation data. Stable export IDs
make a reviewed replay idempotent at the canonical Badger/processor boundary.

## Privacy, trust, and shutdown

The canonical Badger store is a raw conversation-data trust boundary. Restrict
access to the Balda state directory and configure state/JetStream retention to
match the deployment's policy. Balda minimizes the payload to visible text,
bounds model/retrieval outputs, and redacts worker/DLQ diagnostics. ForgetSource
and ForgetScope replace source content with identity-only tombstones, deny
recall immediately, remove matching projection candidates, and invalidate all
dependent revisions in the exact scope; global fact KV remains separate.

Shutdown preserves ordering: channel ingress stops accepting new work, the
turn dispatcher stops producing turns, active session identities publish
shutdown boundaries while JetStream is still live, and only then does the
serialized memory worker drain/close the native processor. The drain is bounded by
`retry.shutdown_timeout`; the final backlog and any abandoned work are
observable in the worker shutdown report. A slow derivation therefore cannot
hold the process indefinitely or block the final chat response.

## Operator verification runbook

Use this runbook after a configuration change and before treating session
memory as available to users. It separates deterministic, credential-free
proof from a live provider/channel smoke test. The deterministic proof is
required evidence. The live tier is `EXECUTED`, `FAILED`, or `UNEXECUTED`; a
missing provider credential, channel credential, second authorized locator, or
NATS CLI is a reason to record `UNEXECUTED`, never a pass.

Do not copy credentials, capability-bearing MCP URLs, message bodies, recalled
text, or DLQ payloads into verification evidence. Use only synthetic,
non-sensitive markers and metadata such as scope key, item/revision IDs,
counts, classifications, stream/consumer names, and bounded error codes.

### 1. Preflight and deterministic proof

Stop Balda before changing or copying its state. Back up the configured
`balda.state_dir`, then set `balda.state_dir` to a dedicated empty directory for
the live smoke test. Keep that directory on the same class of persistent,
access-controlled storage intended for production. Do not reuse a production
state directory, and abort if the proposed verification directory is not
empty. In `.config/balda/config.yaml`, enable session memory with the minimal
configuration above and retain the shipped stream and consumer names unless
the deployment intentionally uses different non-colliding names.

The live tier also requires:

- one configured Balda chat provider (and, when selected, one configured
  session-memory extraction provider) plus one configured chat channel,
  supplied by the normal protected config or environment path rather than
  command-line arguments;
- two authenticated test locators, A and B, where B is a genuinely different
  root/topic/thread scope from A;
- `nats`, `jq`, and `timeout`, with `NATS_URL` set to the runtime's NATS
  endpoint (the embedded development default is `nats://127.0.0.1:4222`); and
- two terminals: one for Balda and one for metadata observation.

First run the credential-free restart and isolation proofs from the repository
root. Bound each command to five minutes. Use the current positive package
contracts; the removed runtime and recall integration tests are not part of the
supported suite:

```bash
timeout 5m go test -race ./sessionmemory/... ./internal/apps/balda/sessionmemoryapp/... ./internal/apps/balda/sessionmemorymcp/...
timeout 5m go test -race ./internal/apps/balda/state -run 'TestSQLiteSessionMemoryIngressOutbox'
```

Pass only if both commands exit zero before their timeout. A failure or timeout
is a hard abort for the live tier. These tests cover portable canonical
processing, public MCP contracts, Balda broker/context unit behavior, Badger
reopen/recall positives, and ingress outbox durability. They do not prove
live provider/channel behavior or restart recall; those require the live tier
below and must not be inferred from this deterministic suite.

### 2. Start an isolated live runtime

Start Balda in terminal A through the supported embedded-runtime launcher; it
loads `.config/balda/config.yaml` and the repository `.env` in the normal way:

```bash
./scripts/dev/run-balda-embedded-runtime.sh
```

Allow at most 60 seconds for startup. Pass when logs show the configured
channel ready and no session-memory, migration, stream, consumer, provider, or
channel initialization error. Abort on any initialization error, missing
credential, stream/consumer collision, unwritable state directory, or startup
timeout. Record only component names and stable error codes/classes, not
configuration values.

In terminal B, confirm the expected resources without reading messages:

```bash
export NATS_URL="nats://127.0.0.1:4222"
nats --server "$NATS_URL" stream info BALDA_SESSION_MEMORY --json | jq '{name:.config.name, messages:.state.messages}'
nats --server "$NATS_URL" consumer info BALDA_SESSION_MEMORY BALDA_SESSION_MEMORY_WORKER --json | jq '{name:.name, pending:.num_pending, ack_pending:.num_ack_pending, redelivered:.num_redelivered}'
nats --server "$NATS_URL" stream info BALDA_DLQ --json | jq '{name:.config.name, messages:.state.messages}'
```

If the deployment uses configured non-default names, substitute those exact
names. Pass when all three commands finish within 15 seconds, the expected
names exist, the session-memory consumer has at most one ack-pending delivery,
and the DLQ count has not increased. Abort if a resource is absent, metadata
cannot be read, `ack_pending` exceeds one, or the DLQ count rises unexpectedly.
Record the initial counts as the baseline.

### 3. Capture and settle one safe marker

In locator A, send `/locator` and record its public
`<channel_type>:<address_key>` value. Create a unique non-sensitive marker
locally and send it once as an ordinary eligible turn whose assistant response
also contains visible final text:

```bash
MARKER="balda-memory-smoke-$(date -u +%Y%m%dT%H%M%SZ)-${RANDOM}"
printf '%s\n' "$MARKER"
```

For example, send `Remember this verification marker as conversation context:
<MARKER>. Reply with marker received.` Do not use a real user statement, secret,
or production identifier.

For at most 60 seconds, rerun only the three metadata commands from step 2 at
intervals no shorter than two seconds. Pass when the session-memory stream and
consumer settle to `messages=0`, `pending=0`, and `ack_pending=0`, with no DLQ
increase. A nonzero redelivery count is acceptable only if it stops increasing
and the backlog still settles within the bound. Abort on a rising DLQ count,
an ack-pending value above one, a counter that grows without bound, or the
60-second deadline. Do not inspect stream or consumer message bodies.

### 4. Prove reset recall and provenance

In locator A, send `/reset` and allow at most 60 seconds for the fresh runtime
session notice. Then make one request that tells Balda to call
`session_memory.search` for the exact marker and report only the returned
scope key, result count, classification, item ID, and revision ID. In the same
locator, ask it to call `session_memory.trace` for that item/revision and
report only node count plus item/revision IDs. Do not ask it to repeat recalled
text in the evidence.

Allow at most the configured `search_timeout` plus 15 seconds for each chat
request. Pass when search returns at least one `untrusted_reference`, the scope
equals locator A exactly, and trace closes entirely within that same scope.
Abort on `invalid_scope`, timeout, foreign scope, missing provenance, a result
classified as instructions, or any unexpected content execution. Wait up to
60 seconds for the post-reset boundary/turn backlog to settle as in step 3
before stopping the process.

### 5. Prove clean restart recall

Send `SIGTERM` (normally Ctrl-C) to terminal A. Allow at most the configured
`retry.shutdown_timeout` plus 15 seconds for exit. Pass when the process exits
cleanly and no new ingress is accepted after shutdown begins. If it exceeds the
bound, preserve the state directory and metadata for diagnosis; do not force a
destructive cleanup or claim a clean drain.

Restart with the same command, config, and isolated state directory. Allow at
most 60 seconds for readiness. From locator A, repeat the metadata-only search
and trace request from step 4. Pass when the same exact scope can recall and
trace the marker after the canonical store and JetStream reopen. Abort on startup error,
missing recall, changed/foreign scope, or provenance failure.

### 6. Prove foreign-locator isolation

In locator B, send `/locator` and confirm its public value differs from locator
A. Make exactly one search request for the marker and require metadata-only
output. Do not repeat this query: its own completed turn can later become valid
memory for locator B. Root/topic/thread and personal/group scopes never inherit
from one another; every distinct canonical locator is foreign to locator A.

Pass when that first search returns zero results (or the stable fail-closed
scope error appropriate to an unsupported locator) and never returns locator
A's item/revision IDs. Abort on any cross-scope result, locator inheritance, or
fallback. Settle the final backlog for at most 60 seconds using metadata only.

### 7. Triage, recovery, rollback, and cleanup

Use the following checkpoints; preserve the isolated state directory until the
cause and evidence status are recorded.

| Condition | Safe evidence | Required action | Maximum wait / outcome |
|---|---|---|---|
| Pre-`PubAck` publish failure | Capture error code/class and unchanged metadata counts | Correct NATS/config availability, then let the ingress outbox retry the same export | The configured `publish_timeout` multiplied by `publish_attempts`, plus the worker retry bound. |
| Transient processor failure | Pending/ack-pending/redelivery counters and redacted component error class | Keep the runtime available while bounded retry and `InProgress` heartbeats run | 60 seconds for this smoke; pass only if counters settle and DLQ does not rise. |
| Permanent or exhausted failure | DLQ count increase and redacted diagnostic code/class | Fix the processor/configuration before any replay | Abort the smoke immediately; do not read or copy the DLQ body. |
| Reviewed replay is required | Export ID and redacted diagnostic metadata from a trusted restricted system | Use a trusted operator-controlled source to replay the original envelope with its stable export ID, or submit a new reviewed turn and accept that it is a new export | No automatic replay exists; verify one action at a time and settle within 60 seconds. |
| Shutdown exceeds its bound | Process state plus stream/consumer counts | Preserve state and diagnose; never delete the state directory to clear a backlog | `retry.shutdown_timeout` plus 15 seconds, then mark the live tier `FAILED`. |

To roll back availability, set `balda.session_memory.enabled=false` and restart
Balda. This prevents new session-memory capture, processing, and MCP bindings
but preserves the canonical Badger directory, rebuildable projection, ingress
outbox/audit state, global fact memory, session history, and the isolated state
directory. Confirm the ordinary channel/session flow still works within 60
seconds. Retain or dispose of the verification
directory only under the deployment's data-retention procedure after Balda is
stopped and the path has been independently checked; the marker is
conversation data.

Record the live result as `EXECUTED` only when every applicable checkpoint
passes, `FAILED` with the first failed checkpoint and safe metadata, or
`UNEXECUTED` with the missing prerequisite and next operator action. Always
record the exact tested revision, configuration profile name (not values),
deterministic command outcomes, locator *kinds* (not private addresses), and
whether cleanup or retention remains. Never infer a live pass from the
credential-free integration suite.

## Extraction path

The extraction-ready implementation is split into public packages and Balda
host adapters:

1. `sessionmemory` owns portable records, semantic validation, canonical
   extraction/reconciliation, temporal state, provenance, recall, trace, and
   forget contracts. It imports no Balda transport, MCP, Fx, NATS, or SQLite.
2. `sessionmemory/app` owns the portable `Runtime`, `Deriver`, typed turn and
   boundary orchestration, canonical hydration, projection coordination, and
   lifecycle ports.
3. `sessionmemory/store/badger` owns canonical Badger persistence and
   `sessionmemory/index/bleve` owns the rebuildable lexical projection.
4. `sessionmemory/mcp` registers only the neutral `session_memory.search` and
   `session_memory.trace` tools. An injected resolver supplies exact
   authenticated scope; no locator is accepted from tool arguments.
5. Balda-only integrations remain in `internal/apps/balda`: `sessionmemorycmd`
   envelopes, `sessionmemoryapp` capture/redaction/outbox/worker wiring,
   `sessionmemorymcp` broker context, and `eventbus/nats` delivery policy.

A later story may move the public subtree into a separate module or service
adapter. This story intentionally ships no remote service, vector/Vecgo index,
encryption, or memory SDK, and does not move `pack/callee/**`. The global
fact-memory package remains outside the extraction.

## Delivery formatting

Turn and delivery commands persist only a transport capability. One immutable
startup registry resolves that capability to both agent prompt instructions and
a process-local formatter, so generation and delivery use the same route.

| Transport | Durable capability | Registered formatter | Delivery behavior |
| --- | --- | --- | --- |
| Telegram | `rich_markdown` | `telegram_rich_markdown` | Telegram rich Markdown |
| Telegram | `rich_html` | `telegram_rich_html` | sanitized Telegram Rich HTML |
| Telegram | `none` | `plain_text` | literal text without `parse_mode` |
| Slackagent | `mrkdwn` | `slack_mrkdwn` | Slack `mrkdwn` |
| Slackagent | `none` | `plain_text` | literal plain text |
| Zulip | `markdown` | `zulip_markdown` | Zulip Markdown |
| Zulip | `none` | `plain_text` | literal plain text |

Registered formatter names are process-local implementation details and never
enter the durable wire contract. The public Telegram setting selects one of
its three capabilities; Slack and Zulip do not use Telegram mode names. A
missing capability route, prompt definition, or formatter is a startup error.
