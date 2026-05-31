# Goalkeeper

Balda `/goal <objective>` starts the Goalkeeper workflow in the current session and workspace.

The workflow uses:

- one ADK `LoopAgent` workflow agent named `Goalkeeper`
- one worker child agent named `GoalkeeperWorker`
- one validator child agent named `GoalkeeperValidator`

Both child agents are built from the configured `balda.provider`. They use the same workspace, Balda MCP server set, and ADK session as the current chat session.

## Workflow

The loop is fixed:

- the worker receives the goal and performs the requested work in the current workspace
- the worker final visible response is persisted in ADK session state as `app:goalkeeper_worker_output`
- the validator runs after the worker and validates the result against the same goal
- the validator prompt is wrapped by Balda so validation sees the latest worker summary even when session transcript context is limited
- if the validator final visible response starts with `verdict: pass`, the loop exits
- otherwise the worker and validator retry until `balda.goal.max_iterations` is exhausted

Balda sends:

- a start message with the objective and max iteration count
- step updates for worker and validator progress
- a final completion or max-iterations message

## Prompt Contract

Balda converts `/goal <objective>` into this workflow prompt:

```text
Goal:
<objective>
```

The worker returns a concise plain-text summary and evidence. The validator must start its final response with exactly one of:

```text
verdict: pass
```

```text
verdict: fail
```

`verdict: pass` means the objective is complete. `verdict: fail` means the objective is not complete yet and the loop should continue until the configured iteration cap.

Thought parts are ignored when checking the validator verdict. Only visible final response text is considered.

## Runtime Notes

The ADK workflow stream includes metadata-only `session.Event` records around each worker and validator step. These events have no `Content`, are persisted in ADK session history, and identify `step_started`, `step_completed`, or `step_failed` in `CustomMetadata["norma.goalkeeper.event"]`.

A passing validation is detected from Norma Goalkeeper's escalation marker, which is set when the validator's visible final response starts with `verdict: pass`. Malformed verdicts, missing verdicts, and `verdict: fail` do not pass validation.

## Not Used

Goalkeeper does not use:

- Taskmaster queues
- scheduled tasks
- PDCA phase agents
- structured PDCA JSON contracts
- planner/executor/reviewer role actors
- the removed single-root prompt loop that asked for `STATUS: done|continue`
