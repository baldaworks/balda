#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}"

if ! command -v nats >/dev/null 2>&1; then
  echo "nats CLI is required. Install from https://docs.nats.io/running-a-nats-service/configuration/nats_admin"
  exit 1
fi

NATS_URL="${NATS_URL:-nats://127.0.0.1:4222}"
COMMANDS_STREAM="${BALDA_COMMANDS_STREAM:-BALDA_COMMANDS}"
EVENTS_STREAM="${BALDA_EVENTS_STREAM:-BALDA_EVENTS}"
DLQ_STREAM="${BALDA_DLQ_STREAM:-BALDA_DLQ}"
WORKER_CONSUMER="${BALDA_WORKER_CONSUMER:-BALDA_WORKER_COMMANDS}"
PROJECTOR_CONSUMER="${BALDA_PROJECTOR_CONSUMER:-BALDA_EVENT_PROJECTOR}"

dump_stream() {
  local stream="$1"
  echo "=== stream:${stream} ==="
  nats --server "${NATS_URL}" stream info "${stream}" --json
}

dump_consumer() {
  local stream="$1"
  local consumer="$2"
  echo "=== consumer:${stream}/${consumer} ==="
  nats --server "${NATS_URL}" consumer info "${stream}" "${consumer}" --json
}

dump_stream "${COMMANDS_STREAM}"
dump_consumer "${COMMANDS_STREAM}" "${WORKER_CONSUMER}"
dump_stream "${EVENTS_STREAM}"
dump_consumer "${EVENTS_STREAM}" "${PROJECTOR_CONSUMER}"
dump_stream "${DLQ_STREAM}"
