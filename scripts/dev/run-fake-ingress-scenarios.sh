#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

PATTERN='TestTelegramCommandHandler|TestTelegramStartHandler|TestService_Accept_|TestReceiver_HTTPHandling'

exec go test ./internal/apps/balda/handlersfx ./internal/apps/balda/channel/webhook ./internal/apps/balda/webhookapp -run "${PATTERN}" "$@"
