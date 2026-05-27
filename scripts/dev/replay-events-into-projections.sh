#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

PATTERN='TestEventProjectorReplayAfterRestartRemainsIdempotent|TestEventProjectorProjectsTaskEventIdempotently|TestEventProjectorProjectsCommandEventForTask|TestEventProjectorProjectsCommandDecodeFailedEventForTask'

exec go test ./internal/apps/balda/swarm -run "${PATTERN}" "$@"
