#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
generated_path="internal/managementapi/v1/management.gen.go"
temporary_directory="$(mktemp -d)"
trap 'rm -rf "${temporary_directory}"' EXIT

cd "${repository_root}"

cp "${generated_path}" "${temporary_directory}/expected.go"
go generate ./internal/managementapi/v1
if ! cmp -s "${temporary_directory}/expected.go" "${generated_path}"; then
	diff -u "${temporary_directory}/expected.go" "${generated_path}" || true
	echo "management OpenAPI generated output is stale" >&2
	exit 1
fi

cp "${generated_path}" "${temporary_directory}/first.go"
go generate ./internal/managementapi/v1
if ! cmp -s "${temporary_directory}/first.go" "${generated_path}"; then
	diff -u "${temporary_directory}/first.go" "${generated_path}" || true
	echo "management OpenAPI generation is not deterministic" >&2
	exit 1
fi

go test ./internal/managementapi/v1
