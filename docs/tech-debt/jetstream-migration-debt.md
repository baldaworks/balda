# JetStream Migration Debt

Status: active  
Owner: Balda maintainers

## Debt items

1. Keep architecture docs synchronized with runtime/test contract updates.
2. Expand deterministic scenario fixtures and replay diagnostics for failure triage.
3. Keep internal operator docs aligned with the reduced public Telegram/Webhook UX.
4. Tighten contract tests so removed task/debug/status surfaces do not reappear in code or docs.

## Exit criteria

- Every runtime contract change updates architecture docs in the same PR.
- Regression tests cover each new command/event subject.
- Internal docs and tests keep enough metadata to debug replay/retry/DLQ flows without code reads.
