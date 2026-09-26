#!/usr/bin/env bash
# Правит проект по текстовым дельтам (см. tools/apply-project-deltas.py).
# $1 — глагол: check | apply | list
# $2 — файл с JSON-дельтами
# $3 — файл состояния
set -euo pipefail
MODE="${1:-check}"
DELTAS="${2:?нужен файл дельт}"
STATE="${3:?нужен файл состояния}"
ROOT="/Users/admin/Developer/balda"
PY="/Users/admin/Developer/balda/tools/apply-project-deltas.py"
case "$MODE" in
  check)  exec python3 "$PY" "$DELTAS" --root "$ROOT" --state "$STATE" --check ;;
  list)   exec python3 "$PY" "$DELTAS" --root "$ROOT" --state "$STATE" --list ;;
  apply)  exec python3 "$PY" "$DELTAS" --root "$ROOT" --state "$STATE" ;;
  *) echo "неизвестный глагол: $MODE (check|apply|list)" >&2; exit 2 ;;
esac
