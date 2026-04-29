# Multi-Agent Processing Chain Analysis & Trigger Guide

Date: 2026-03-16
Scope: API `/api/agent/chat` + Team orchestration APIs + `aicli chat` actor-first local orchestration

## 1. Entry Surfaces (What Exists Today)

API (multi-agent capable):
- `/api/agent/chat` (canonical)
- `/api/skills/agent/chat` (compat alias)

API (team orchestration):
- `/api/skills/teams/*` (create team, plan tasks, orchestrate teammates, mailbox, leases, path claims)

CLI:
- `aicli chat` (default local actor-first orchestration host)
- optional dedicated `aicli agent` is still not implemented

## 2. Processing Chain: `/api/agent/chat` (Multi-Agent Path)

1. Request accepted with `messages` and optional orchestration flags.
2. Profile resolution runs (profile/agent/workspace prompt + tool policy + skill dirs).
3. Session is loaded or created; profile prompt is injected into session system messages.
4. Routing & planning:
   - If `enable_routing=true`, router attempts skill route.
   - If no route or planning is requested, orchestration chooses:
     - `planner_preferred`, `route_preferred`, `agent_only`, `llm_only`.
5. Planner builds a plan and subagent task graph (if planning enabled).
6. Subagent execution (optional):
   - `execute_planned_subagents=true` triggers scheduler.
   - `allow_write_planned_subagents=true` allows writer subagents to execute writes.
   - Tool policy / sandbox / read-only can still block execution.
7. Each subagent runs its own session + context, uses tool policy and returns a structured report.
8. Patch governance:
   - Patch decision is computed from subagent reports.
   - `patch_decision_policy` controls strict/warn behavior.
   - `approve_blocked_patches` or `patch_approval` can override.
9. Response is assembled:
   - Non-stream: `planning` + `orchestration` blocks included in JSON.
   - Stream: SSE events include `planning`, `subagent`, `tool`, `result`.
10. Session is persisted.

## 3. Processing Chain: Team Orchestration APIs

1. Create team: `POST /api/skills/teams`.
2. Upsert teammates: `POST /api/skills/teams/{id}/teammates`.
3. Plan tasks: `POST /api/skills/teams/{id}/plan` or create tasks directly.
4. Orchestrator loop starts for active teams and runs continuously.
5. Orchestrator claims ready tasks, dispatches to teammate runners.
6. Teammate runner uses session actor to execute, writes outcomes.
7. Mailbox and path claims provide coordination + locking.
8. Outcome APIs finalize tasks, trigger replanning if blocked.

Note: Team orchestration can now be triggered either from the API or from local `aicli chat`.
The difference is where the orchestration host lives: service-side for `/api/agent/chat`,
or in-process inside the CLI for `aicli chat`.

## 4. Processing Chain: `aicli chat` (Local Actor-First CLI)

1. CLI parses flags, loads global config.
2. Profile is resolved (optional `--profile --agent`).
3. Runtime config + MCP config + skill dirs are resolved.
4. Local runtime host is built:
   - `SessionActor` hub
   - runtime/tools adapter
   - local team store + orchestrator
   - runtime event bridge
5. Session is restored from local storage, including ambient team binding and permission mode.
6. The actor turn runs:
   - route-first skill path first
   - otherwise ReAct/tool execution
7. Tool calls execute against builtin tools, MCP tools, and local team tools.
8. `spawn_team` or team task tools can create/update active local teams.
9. Planning/tool/subagent events are rendered directly in the CLI.
10. Session plus runtime metadata are persisted back to local storage.

Operational note: `aicli chat` still does not proxy to `/api/agent/chat` by default;
the orchestration now happens locally in the CLI host instead of being absent.

## 5. Implemented vs Missing (Current State)

Implemented:
- Profile resolution shared by CLI + API.
- Agent orchestrator (route-first / planner / LLM fallback).
- Planner -> subagent task graph mapping.
- Subagent scheduler with read-only + single-writer constraints.
- Patch decision + approval fields in planning output.
- Static SSE streaming events for `planning`/`subagent`.
- Team store + orchestrator + mailbox + path claims + leases.

Missing / Gaps:
- Optional dedicated `aicli agent` CLI surface.
- Subagent profile externalization (subagent still role-default).
- Automatic subagent routing (task classifier + profile routing).
- Streaming is static SSE; no incremental subagent streams.
- Patch apply/verification “落盘闭环” is incomplete in design docs.

## 6. How to Trigger via API

### 6.0 Auto-create team + teammates (spawn_team tool)

Use `/api/agent/chat` and instruct the model to call `spawn_team`. Enable ReAct so tool calls are allowed.
Example request:

```
{
  "messages": [{"role":"user","content":"Create a team with 2 teammates (planner/executor) and 2 tasks. Use the spawn_team tool now."}],
  "enable_react": true
}
```

Expected:
- Tool call `spawn_team` appears in the tool call list.
- Tool result includes `team_id`, `teammate_ids`, `task_ids`.
- Team loop auto-started when available.

### 6.1 Plan only (no subagent execution)

POST `/api/agent/chat`
```
{
  "messages": [{"role":"user","content":"plan workflow for me"}],
  "enable_routing": true,
  "planning_mode": "planner_preferred"
}
```

Expected:
- `planning.mode = planner_preferred`
- `planning.step_count` and `planning.subagent_task_count` populated.

### 6.2 Plan + execute subagents (with write allowed)

POST `/api/agent/chat`
```
{
  "messages": [{"role":"user","content":"plan execute verify flow"}],
  "enable_routing": true,
  "planning_mode": "planner_preferred",
  "execute_planned_subagents": true,
  "allow_write_planned_subagents": true
}
```

Expected:
- `planning.subagent_execution_attempted = true`
- `planning.subagent_result_count > 0`
- `planning.patch_decision` present.

### 6.3 Streaming (SSE)

POST `/api/agent/chat`
```
{
  "messages": [{"role":"user","content":"plan and stream"}],
  "planning_mode": "planner_preferred",
  "execute_planned_subagents": true,
  "allow_write_planned_subagents": true,
  "stream": true
}
```

Expected:
- SSE events: `planning`, `subagent`, `tool`, `result`.

### 6.4 Patch decision controls

Supported policies:
- `strict` (default)
- `warn`

Override options:
- `approve_blocked_patches: true`
- `patch_approval: { approved: true, ticket_id, approver, reason }`

## 7. How to Trigger via `aicli chat`

### 7.1 Prereqs

- Enable skills runtime in `configs/config.yaml`:
  - `skills_runtime.enabled: true`
  - default `skill_dir` already points to `./.agents/skills`
- Ensure MCP toolkit is enabled:
  - `configs/mcp.yaml` has `toolkit` enabled and `bin/toolkit-mcp.exe` exists.

### 7.2 Smoke Test (Keyword Trigger)

Run:
```
.\aicli.exe chat --skills-dir .\.agents\skills --skills-mode prefer --skills-debug --no-interactive --message "smoke test"
```

Expected:
- Skill `skill_runtime_smoke` is exposed.
- Output contains `SKILL_RUNTIME_OK`.

### 7.3 Verify Skill Exposure (Debug)

Use `--skills-debug` to show routing candidates and which skills are exposed for a prompt.
This is the most reliable way to confirm keyword-trigger routing in CLI.

### 7.4 Important Limitation

`aicli chat` does not run subagents or team orchestration.  
To validate multi-agent orchestration, use `/api/agent/chat` and check `planning/subagent` outputs.
