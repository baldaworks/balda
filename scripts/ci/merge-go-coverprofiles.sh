#!/usr/bin/env bash

set -euo pipefail

output="${1:?usage: merge-go-coverprofiles.sh OUTPUT PROFILE...}"
shift
if (( $# == 0 )); then
  printf 'at least one coverage profile is required\n' >&2
  exit 2
fi

tmp="${output}.tmp"
trap 'rm -f "$tmp"' EXIT
printf 'mode: atomic\n' >"$tmp"
for profile in "$@"; do
  if [[ ! -s "$profile" ]] || [[ "$(head -n 1 "$profile")" != 'mode: atomic' ]]; then
    printf 'invalid atomic coverage profile: %s\n' "$profile" >&2
    exit 1
  fi
  tail -n +2 "$profile" >>"$tmp"
done
mv "$tmp" "$output"
go tool cover -func="$output" >/dev/null
