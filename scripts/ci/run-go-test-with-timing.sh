#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mode="${1:-}"
artifacts_root="${BALDA_CI_TIMING_DIR:-$repo_root/.tmp/ci-timing/$mode}"

case "$mode" in
  test)
    title="General Go test"
    command=(go test -json -v -race -covermode=atomic -coverprofile=coverage.out -timeout=20m ./...)
    inventory=(go list ./...)
    ;;
  sqlite)
    title="SQLite integration"
    command=(go test -json -race -tags=integration,sqlite ./internal/apps/balda/state)
    inventory=(go list -tags=integration,sqlite ./internal/apps/balda/state)
    ;;
  *)
    printf 'usage: %s {test|sqlite}\n' "$0" >&2
    exit 2
    ;;
esac

mkdir -p "$artifacts_root"
raw_json="$artifacts_root/go-test.jsonl"
plain_log="$artifacts_root/test.log"
summary="$artifacts_root/timing.md"
inventory_file="$artifacts_root/inventory.txt"
summary_tool="$artifacts_root/gotestsummary"

cd "$repo_root"
{
  printf 'mode=%s\n' "$mode"
  printf 'command='
  printf '%q ' "${command[@]}"
  printf '\npackages:\n'
  "${inventory[@]}" | LC_ALL=C sort
} >"$inventory_file"

go build -o "$summary_tool" ./scripts/ci/gotestsummary
set +e
"${command[@]}" 2>&1 \
  | tee "$raw_json" \
  | "$summary_tool" -title "$title" -output "$summary" -log "$plain_log"
status=$?
set -e
rm -f "$summary_tool"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" && -f "$summary" ]]; then
  cat "$summary" >>"$GITHUB_STEP_SUMMARY"
fi
if (( status != 0 )); then
  printf '%s failed; timing artifacts: %s\n' "$title" "$artifacts_root" >&2
fi
exit "$status"
