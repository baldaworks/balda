#!/usr/bin/env bash
set -euo pipefail

backoffice=false
core=false
full=false
saw_path=false

classify_path() {
  local path=$1
  saw_path=true
  case "$path" in
    docs/*|*.md) ;;
    cmd/backoffice/*|\
    internal/apps/backoffice/browseredge/*|\
    internal/apps/backoffice/loginlimit/*|\
    internal/apps/backoffice/managementclient/*|\
    internal/apps/backoffice/preview/*|\
    internal/apps/backoffice/standalone/*|\
    qa/backoffice-e2e/*)
      backoffice=true
      ;;
    # The root package is Core-owned offline maintenance/config projection.
    # Unknown future Backoffice subpackages fail safely into the Core suite.
    internal/apps/backoffice/*)
      core=true
      ;;
    api/management/*|internal/managementapi/*)
      backoffice=true
      core=true
      ;;
    cmd/balda/*|internal/apps/balda/*|pkg/*|sessionmemory/*)
      core=true
      ;;
    go.mod|go.sum|go.work|go.work.sum|.github/*|scripts/ci/*|Taskfile.yml|Dockerfile*)
      full=true
      ;;
    *)
      full=true
      ;;
  esac
}

while IFS=$'\t' read -r status first second extra; do
  [[ -z "${status}${first}${second}${extra}" ]] && continue
  if [[ -n "${extra:-}" || -z "${first:-}" ]]; then
    full=true
    continue
  fi
  case "$status" in
    R*|C*)
      [[ -n "${second:-}" ]] || { full=true; continue; }
      classify_path "$first"
      classify_path "$second"
      ;;
    A|M|D|T)
      [[ -z "${second:-}" ]] || { full=true; continue; }
      classify_path "$first"
      ;;
    *)
      full=true
      ;;
  esac
done

if [[ "$full" == true ]]; then
  backoffice=true
  core=true
fi

{
  echo "backoffice=$backoffice"
  echo "core=$core"
  echo "full=$full"
} | tee "${GITHUB_OUTPUT:-/dev/null}"
