#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

cd "${REPO_ROOT}"

PATTERN='TestCommandHandlerPublishesActorOwnedCommands|TestCommandHandlerAccessDerivation|TestInboundWebhookReceiver_AcceptsAndPublishesCommand|TestInboundWebhookReceiver_SessionModePublishesSessionCommand'

exec go test ./internal/apps/balda/handlers -run "${PATTERN}" "$@"
