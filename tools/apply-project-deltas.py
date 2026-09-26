#!/usr/bin/env python3
"""Апплаер проектных дельт: правит файлы проекта по текстовым дельтам с якорями.

Философия: НЕ использовать литеральные значения в командах. Каждая дельта описывает:
  - file: файл проекта (относительно --root)
  - search: уникальный литеральный фрагмент текущего содержимого
  - replace: литеральный фрагмент замены
  - note: человекочитаемое описание правки (для отчёта)

Если `search` в тексте уже нет, а `replace` есть — дельта идемпотентно пропускается.
Если нет ни того, ни другого — ошибка "anchor not found" с указанием, что именно искали.
Состояние (sha256 применённых якорей) хранится в JSON рядом со скриптом.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def load_state(state_path: Path) -> dict:
    if not state_path.exists():
        return {"applied": {}}
    try:
        return json.loads(state_path.read_text(encoding="utf-8"))
    except json.JSONDecodeError:
        return {"applied": {}}


def save_state(state_path: Path, state: dict) -> None:
    state_path.write_text(
        json.dumps(state, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )


def apply_delta(state: dict, root: Path, delta: dict) -> tuple[str, str]:
    """Возвращает (status, detail). status: applied|skipped|error."""
    rel = delta["file"]
    search = delta["search"]
    replace = delta["replace"]
    note = delta.get("note", "")
    path = root / rel
    if not path.exists():
        return "error", f"{rel}: файл не найден ({note})"
    cur = path.read_text(encoding="utf-8")
    key = f"{rel}::{sha256_text(search)[:12]}"
    if key in state["applied"]:
        if replace in cur:
            return "skipped", f"{rel}: уже применено ({note})"
        return "error", f"{rel}: состояние говорит 'применено', но replace отсутствует ({note})"
    if replace and replace in cur and search not in cur:
        state["applied"][key] = sha256_text(cur)
        return "skipped", f"{rel}: replace уже присутствует — состояние досоздано ({note})"
    if search not in cur:
        return "error", f"{rel}: ЯКОРЬ НЕ НАЙДЕН ({note})"
    if cur.count(search) != 1:
        return "error", f"{rel}: якорь встречается {cur.count(search)} раз, нужен уникальный ({note})"
    new = cur.replace(search, replace, 1)
    path.write_text(new, encoding="utf-8")
    state["applied"][key] = sha256_text(new)
    return "applied", f"{rel}: {note}"


def main() -> int:
    ap = argparse.ArgumentParser(description="Апплаер проектных дельт")
    ap.add_argument("deltas", help="путь к JSON с дельтами")
    ap.add_argument("--root", required=True, help="корень проекта")
    ap.add_argument("--state", required=True, help="файл состояния")
    ap.add_argument("--check", action="store_true", help="только проверить, не писать")
    ap.add_argument("--list", action="store_true", help="список дельт, ничего не делать")
    args = ap.parse_args()

    deltas = json.loads(Path(args.deltas).read_text(encoding="utf-8"))
    root = Path(args.root).expanduser().resolve()
    state_path = Path(args.state).expanduser().resolve()

    if args.list:
        for i, d in enumerate(deltas):
            print(f"{i:02d}  {d['file']}: {d.get('note', '')}")
        return 0

    state = load_state(state_path)
    errors = 0
    for i, d in enumerate(deltas):
        path = root / d["file"]
        cur = path.read_text(encoding="utf-8") if path.exists() else ""
        if args.check:
            if d["replace"] in cur:
                print(f"{i:02d}  OK       {d['file']}: {d.get('note', '')}")
            elif d["search"] in cur:
                print(f"{i:02d}  TODO     {d['file']}: {d.get('note', '')}")
            else:
                print(f"{i:02d}  MISSING  {d['file']}: {d.get('note', '')}")
                errors += 1
            continue
        status, detail = apply_delta(state, root, d)
        print(f"{i:02d}  {status.upper():8s} {detail}")
        if status == "error":
            errors += 1
    if not args.check:
        save_state(state_path, state)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
