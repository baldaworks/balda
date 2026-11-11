#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
output_dir="${1:?usage: make-go-test-shards.sh OUTPUT_DIR}"
mkdir -p "$output_dir"

cd "$repo_root"
go list ./... | LC_ALL=C sort >"$output_dir/all.txt"
: >"$output_dir/shard-0.txt"
: >"$output_dir/shard-1.txt"

while IFS= read -r package; do
  case "$package" in
    github.com/baldaworks/balda/internal/apps/balda/catalogapp|\
    github.com/baldaworks/balda/internal/apps/backoffice|\
    github.com/baldaworks/balda/internal/apps/balda/pluginapp|\
    github.com/baldaworks/balda/internal/apps/balda/handlersfx|\
    github.com/baldaworks/balda/internal/apps/balda/scheduledjobs|\
    github.com/baldaworks/balda/internal/apps/balda/jobs|\
    github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp)
      shard=0
      ;;
    github.com/baldaworks/balda/internal/apps/balda|\
    github.com/baldaworks/balda/internal/apps/balda/session|\
    github.com/baldaworks/balda/internal/apps/balda/actors|\
    github.com/baldaworks/balda/cmd/balda|\
    github.com/baldaworks/balda/internal/apps/backoffice/security|\
    github.com/baldaworks/balda/internal/apps/balda/state|\
    github.com/baldaworks/balda/internal/apps/balda/skillapp)
      shard=1
      ;;
    *)
      checksum="$(printf '%s' "$package" | cksum)"
      checksum="${checksum%% *}"
      shard=$((checksum % 2))
      ;;
  esac
  printf '%s\n' "$package" >>"$output_dir/shard-$shard.txt"
done <"$output_dir/all.txt"

cat "$output_dir/shard-0.txt" "$output_dir/shard-1.txt" | LC_ALL=C sort >"$output_dir/combined.txt"
if ! cmp -s "$output_dir/all.txt" "$output_dir/combined.txt"; then
  printf 'generated shards do not preserve the complete package inventory\n' >&2
  exit 1
fi
if [[ "$(cat "$output_dir/shard-0.txt" "$output_dir/shard-1.txt" | sort | uniq -d | wc -l)" -ne 0 ]]; then
  printf 'generated shards contain duplicate packages\n' >&2
  exit 1
fi
