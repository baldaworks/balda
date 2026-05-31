#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

export BALDA_NATS_EMBEDDED="${BALDA_NATS_EMBEDDED:-true}"
export BALDA_SWARM_ENABLED="${BALDA_SWARM_ENABLED:-true}"

exec go run ./cmd/balda start "$@"
