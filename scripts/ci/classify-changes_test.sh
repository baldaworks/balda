#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
classifier="$root/scripts/ci/classify-changes.sh"

check() {
  local name=$1 input=$2 expected=$3 output
  output=$(printf '%b' "$input" | "$classifier")
  if [[ "$output" != "$expected" ]]; then
    printf 'FAIL %s\nwant:\n%s\ngot:\n%s\n' "$name" "$expected" "$output" >&2
    exit 1
  fi
}

check backoffice 'M\tcmd/backoffice/main.go\n' $'backoffice=true\ncore=false\nfull=false'
check backoffice-package 'M\tinternal/apps/backoffice/browseredge/app.go\n' $'backoffice=true\ncore=false\nfull=false'
check maintenance 'M\tinternal/apps/backoffice/maintenance.go\n' $'backoffice=false\ncore=true\nfull=false'
check core 'D\tinternal/apps/balda/state/store.go\n' $'backoffice=false\ncore=true\nfull=false'
check shared 'M\tapi/management/v1/openapi.yaml\n' $'backoffice=true\ncore=true\nfull=false'
check docs 'M\tdocs/reference/backoffice.md\n' $'backoffice=false\ncore=false\nfull=false'
check unknown 'A\ttools/new-tool.txt\n' $'backoffice=true\ncore=true\nfull=true'
check rename 'R100\tinternal/apps/backoffice/browseredge/old.go\tinternal/apps/balda/new.go\n' $'backoffice=true\ncore=true\nfull=false'
check ci 'M\t.github/workflows/test.yml\n' $'backoffice=true\ncore=true\nfull=true'
check malformed 'broken\n' $'backoffice=true\ncore=true\nfull=true'

echo 'classify-changes: ok'
