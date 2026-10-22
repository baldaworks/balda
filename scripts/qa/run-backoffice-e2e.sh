#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
suite_dir="$repo_root/qa/backoffice-e2e"
artifacts_dir="${BALDA_BACKOFFICE_E2E_ARTIFACTS:-${TMPDIR:-/tmp}/balda-backoffice-e2e}"
install_system=true

usage() {
  cat <<'EOF'
Usage: scripts/qa/run-backoffice-e2e.sh [--no-system-install]

Installs the lockfile-pinned Playwright browser and runs the complete Backoffice
browser regression suite. By default Playwright also provisions supported Linux
system dependencies and fonts. Use --no-system-install in a pre-provisioned
environment.

Set BALDA_BACKOFFICE_E2E_ARTIFACTS to choose the log directory.
Set BALDA_BACKOFFICE_E2E_SCREENSHOTS=1 to retain full-page screenshots.
EOF
}

for argument in "$@"; do
  case "$argument" in
    --no-system-install)
      install_system=false
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$argument" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$(uname -s)" != "Linux" ]]; then
  printf 'Backoffice E2E provisioning currently supports Linux only\n' >&2
  exit 1
fi
for command in go node npm; do
  if ! command -v "$command" >/dev/null 2>&1; then
    printf 'required command is unavailable: %s\n' "$command" >&2
    exit 1
  fi
done

mkdir -p "$artifacts_dir"
log_file="$artifacts_dir/test.log"
: >"$log_file"
trap 'status=$?; if (( status != 0 )); then printf "Backoffice E2E failed; diagnostics: %s\n" "$artifacts_dir" >&2; fi' EXIT

run_logged() {
  "$@" 2>&1 | tee -a "$log_file"
}

cd "$suite_dir"
run_logged npm ci --no-audit --no-fund

if [[ "$install_system" == true ]]; then
  run_logged ./node_modules/.bin/playwright install --with-deps chromium
else
  run_logged ./node_modules/.bin/playwright install chromium
fi

run_logged node preflight.cjs

cd "$repo_root"
run_logged go build -o "$artifacts_dir/balda-qa-preflight" ./cmd/balda
rm -f "$artifacts_dir/balda-qa-preflight"
cd "$suite_dir"

if [[ "${BALDA_BACKOFFICE_E2E_SCREENSHOTS:-}" == "1" ]]; then
  export BACKOFFICE_E2E_SCREENSHOTS="$artifacts_dir/screenshots"
fi

run_logged npm test
