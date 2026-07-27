# Team Task Outcome Contract

## Scope

This document describes the canonical Team task outcome API and the broker/tool compatibility surface.

Canonical HTTP entrypoint:

- `POST /api/runtime/teams/{id}/tasks/{task_id}/outcome`

Current HTTP surface:

- The current `runtime-server` route table registers `/outcome` as the HTTP entrypoint.
- Older docs and tests may mention `/complete`, `/fail`, and `/block`; those are not current live HTTP routes in `backend/internal/api/skills/handler.go`.
- The Go client methods `CompleteTask`, `FailTask`, and `BlockTask` are convenience wrappers that set `task_status` and still call the canonical `/outcome` endpoint.

Canonical broker tool:

- `report_task_outcome`

Compatibility broker alias:

- `block_current_task`

## Status Model

Supported structured `task_status` values:

- `done`
- `failed`
- `blocked`
- `handoff`

Shared contract rules:

- `summary` is required for every structured outcome
- `blocker` is required for `failed`, `blocked`, and `handoff`
- `handoff_to` is required only for `handoff`
- `result_ref` is optional and currently relevant for `done` / `failed`

## Canonical HTTP Request

```json
{
  "task_status": "handoff",
  "summary": "pass to reviewer",
  "blocker": "need security review",
  "handoff_to": "mate-2",
  "result_ref": "artifact://optional",
  "teammate_id": "mate-1",
  "notify_lead": true,
  "auto_replan": false
}
```

Fields:

- `task_status`: required on `/outcome`
- `summary`: required for structured requests
- `blocker`: required for `failed` / `blocked` / `handoff`
- `handoff_to`: required for `handoff`
- `result_ref`: optional artifact or result pointer
- `teammate_id`: optional teammate identity for state transitions
- `notify_lead`: optional; for blocked/handoff outcomes controls mailbox notification
- `auto_replan`: optional; for blocked/handoff outcomes controls lead replanning

## HTTP Responses

### Done / Failed

```json
{
  "task": {
    "id": "task-1",
    "team_id": "team-1",
    "status": "done",
    "summary": "artifact published",
    "result_ref": "artifact://build-1"
  }
}
```

### Blocked / Handoff

```json
{
  "task": {
    "id": "task-1",
    "team_id": "team-1",
    "status": "blocked",
    "summary": "pass to reviewer"
  },
  "message_id": "mail-1",
  "auto_replan": false,
  "replan_error": "",
  "handoff_to": "mate-2"
}
```

Blocked responses may also include:

- `planned_tasks`
- `planned_dependencies`
- `planned_summary`

## Compatibility Notes

`/complete`, `/fail`, and `/block` are historical HTTP aliases from earlier design notes.

Current behavior:

- server-side live route: `POST /api/runtime/teams/{id}/tasks/{task_id}/outcome`
- typed client convenience wrappers: `CompleteTask`, `FailTask`, `BlockTask`
- broker compatibility alias: `block_current_task`

If HTTP alias compatibility is needed again, the route table must be updated first; documentation alone should not assume those paths exist.

## Broker Tool Contract

Canonical tool:

```json
{
  "task_status": "done",
  "summary": "task finished",
  "result_ref": "artifact://done-task"
}
```

`report_task_outcome` supports all four statuses.

`block_current_task` is kept as a compatibility alias for `blocked` / `handoff`.

## Worker `completionRequirement` harness

Team task workers can require a structured task outcome before a run finishes cleanly. This harness aligns with the tools above; it does **not** invent a second completion API or expose Team outcome tools to an ordinary child session.

Supported values (normalized):

- `none` — default for ordinary chat / non-worker runs; no terminal tool is required
- `complete_task` — the run must observe a successful `report_task_outcome` or `block_current_task` before finishing cleanly

Aliases accepted on spawn / agentdef input: `complete-task`, `completetask` → `complete_task`. Unknown values are treated as unset on spawn (ignored) and as `none` after loop normalize. These compatibility inputs do not by themselves create Team task identity; the loop only activates `complete_task` for a per-run `RunMeta` containing both `TeamID` and `CurrentTaskID`.

### Where it is set

| Path | Default / behavior |
| --- | --- |
| Team teammate worker (`TeammateRunner`) | Injects `complete_task` together with `TeamID`, `AgentID`, and `CurrentTaskID` at the assignment boundary |
| `spawn_agent` | Accepts `completion_requirement` / `completionRequirement` for compatibility and may resolve an agentdef default, but never inherits the parent Team worker contract or task identity |
| `spawn_subagents` task item | Optional per-task `completion_requirement` / `completionRequirement` → child loop config |
| Spawn route context | Explicitly writes `none` when the child did not request a requirement, overriding any value copied by session fork |
| Session actor loop | Treats profile/session/legacy `complete_task` as `none` unless the current per-run Team task identity is complete |
| `cloneLoopConfigForRun` | Honors explicit `complete_task` only when the same `RunMeta` contains non-empty `TeamID` and `CurrentTaskID` |

Session context key: `completion_requirement` (`toolbroker.AgentSessionContextCompletionRequirement`).

### Loop behavior when `complete_task` is active

1. While the model ends a turn without a successful outcome tool observation, the loop injects a **system reminder** and grants a limited recovery turn (default 1).
2. Reminder text points at `report_task_outcome` (`task_status` `done|failed|blocked|handoff` + `summary`) and notes `block_current_task` as the blocked/handoff compatibility alias.
3. If recovery is exhausted without a successful outcome observation, the result is marked unsatisfied (`CompletionSatisfied=false`) with a clear error message—same tool names as the team outcome contract, no dual write path.

This keeps worker completion, broker tools, and HTTP `/outcome` on one semantic surface: structured task status + summary (+ blocker/handoff fields when required).

Forking copies conversation history and session context, not ownership of the parent's Team assignment. Both the local CLI controller and the HTTP/API session controller apply the child spawn route context after cloning so an omitted child requirement is persisted as `none`. Follow-up, send-input, and resume rebuild `RunMeta` from the child session itself and cannot recover the parent's `complete_task` contract.

## Notes

- Non-structured teammate model output is still parsed separately by the teammate runner via the shared teammate outcome contract.
- `report_task_outcome` and `block_current_task` remain hidden without an active Team run; ordinary `spawn_agent` children do not receive a synthetic Team identity or an agent-session-only outcome implementation.
- The HTTP and broker entrypoints now share the same apply layer for task status changes, mailbox side effects, claim release, and replanning.
- Worker harness recovery only requires that one of the outcome tools succeeded; it does not re-validate HTTP field rules already enforced by the broker/apply layer.
