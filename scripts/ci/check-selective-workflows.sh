#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
test_workflow="$root/.github/workflows/test.yml"
lint_workflow="$root/.github/workflows/lint.yml"

require() {
  local pattern=$1 file=$2
  grep -Eq -- "$pattern" "$file" || {
    printf 'missing workflow contract %s in %s\n' "$pattern" "$file" >&2
    exit 1
  }
}

require '^  changes:$' "$test_workflow"
require '^  test:$' "$test_workflow"
require 'force-full:' "$test_workflow"
require 'schedule:' "$test_workflow"
require 'backoffice-unit:' "$test_workflow"
require "needs\.changes\.outputs\.core == 'true'" "$test_workflow"
require "needs\.changes\.outputs\.backoffice == 'true'" "$test_workflow"
require '^    name: lint$' "$lint_workflow"
require 'docs-only: no product lint selected' "$lint_workflow"
require 'force-full:' "$lint_workflow"
require 'schedule:' "$lint_workflow"

echo 'selective workflow contracts: ok'
