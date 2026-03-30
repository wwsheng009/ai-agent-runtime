# Team Task Outcome Contract

## Scope

This document describes the canonical Team task outcome API and its compatibility aliases.

Canonical HTTP entrypoint:

- `POST /api/skills/teams/{id}/tasks/{task_id}/outcome`

Compatibility aliases:

- `POST /api/skills/teams/{id}/tasks/{task_id}/complete`
- `POST /api/skills/teams/{id}/tasks/{task_id}/fail`
- `POST /api/skills/teams/{id}/tasks/{task_id}/block`

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

## Compatibility HTTP Aliases

`/complete`, `/fail`, and `/block` remain available for compatibility.

Current behavior:

- they reuse the same internal outcome handling path as `/outcome`
- they return `Warning` compatibility headers
- they return `X-AI-Gateway-Canonical-Entrypoint` pointing to `/outcome`
- `/complete` / `/fail` still accept legacy summary-only request bodies
- `/block` still accepts the older blocked-style request body

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

## Notes

- Non-structured teammate model output is still parsed separately by the teammate runner via the shared teammate outcome contract.
- The HTTP and broker entrypoints now share the same apply layer for task status changes, mailbox side effects, claim release, and replanning.
