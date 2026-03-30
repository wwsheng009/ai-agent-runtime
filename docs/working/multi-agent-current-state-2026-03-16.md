# Multi-Agent Current State

Date: 2026-03-16
Scope: `runtime.yaml` provider/model resolution, `/api/agent/chat`, `spawn_team`, and current live verification status

> Migration note (2026-03-30):
> - This document records the runtime debugging state as of 2026-03-16.
> - The live `/api/agent/chat` and `/api/skills` service has since moved to `E:\projects\ai\ai-agent-runtime\backend`.
> - In the standalone runtime repo, many old paths under `internal/runtime/<subpkg>` were flattened to `internal/<subpkg>`.
> - When re-running anything below today, use `cmd/runtime-server` instead of a gateway-hosted runtime.
> - When this document mentions `configs/runtime.yaml` or `configs/config.yaml`, read them as `backend/configs/runtime.yaml` and `backend/configs/config.yaml`.
> - All `go test ./...` commands below should be run from `E:\projects\ai\ai-agent-runtime\backend`.

## Summary

The original runtime design had two core problems:

1. `configs/runtime.yaml` only had `defaultModel`, and runtime code implicitly reused that value to choose a provider.
2. `/api/agent/chat` built workspace context from the default workspace root when `workspace_path` was omitted, which caused very heavy scans and long request hangs.

Those two issues have been fixed in code.

There was also a third issue:

3. Real runtime tools such as `spawn_team` were not being passed through to the upstream model. The LLM adapter layer discarded actual tool definitions and only sent fixed MCP meta-tools. This explains the earlier behavior where the model replied that `spawn_team` was unavailable.

That issue has also been fixed in code.

There were then three more issues on the live path:

4. Codex tool-call responses were reaching the adapter, but `internal/runtime/llm/provider.go` only converted OpenAI-style nested `tool_calls` and dropped the Codex flattened form.
5. Follow-up Codex requests replayed `function_call_output` without replaying the original `function_call`, which caused upstream `HTTP 400` errors on the second step.
6. `agent_react` responses could execute tools successfully while `orchestration.tool_call_count` still reported `0`, because the orchestration summary only counted `llmResponse.ToolCalls`, not observed tool executions.

Those issues have also now been fixed in code.

This document has been synchronized with the current workspace state on 2026-03-16. The earlier note that the default provider was `codex_998` is no longer accurate for the checked-out tree.

## What Has Been Fixed

### 1. Explicit provider support

Added explicit provider handling through the runtime stack:

- `internal/runtime/config/manager.go`
- `internal/runtime/llm/runtime.go`
- `internal/runtime/bootstrap/manager.go`
- `internal/runtime/agent/agent.go`
- `internal/runtime/agent/loop.go`
- `internal/runtime/agent/orchestrator.go`
- `internal/runtime/agent/planner.go`
- `internal/api/skills/handler.go`
- `internal/api/skills/session_runtime_support.go`

Behavior now:

- `defaultProvider` is supported in `runtime.yaml`
- provider selection order is now explicit
- `defaultModel` is no longer abused as the provider selector
- request-level `provider` and `model` overrides are supported in `/api/agent/chat`

### 2. Default runtime config is currently aligned

`configs/runtime.yaml` was updated to:

```yaml
agent:
  defaultProvider: "CODEX_03"
  defaultModel: "gpt-5.2-codex"
```

Current repository state:

- `configs/runtime.yaml` now sets `defaultProvider = CODEX_03`
- `configs/config.yaml` defines the configured provider as `CODEX_03`
- fresh live verification with omitted `provider` now succeeds on the running service

Implication:

- explicit `provider` / `model` overrides work
- the current default-provider path is also verified when the runtime config uses canonical `CODEX_03`

### 3. `/api/agent/chat` no longer auto-scans the whole repo by default

Changed behavior in `internal/api/skills/handler.go`:

- if `workspace_path` is omitted, the handler no longer auto-uses `runtime.workspace.root`
- workspace scanning now only happens when `workspace_path` is explicitly provided

This prevents the handler from scanning the full repository on every request.

### 4. Real tool definitions are now sent to the LLM

Fixed in:

- `internal/runtime/llm/mcp_meta_tools_convert.go`
- `internal/runtime/llm/gateway_client.go`
- `internal/runtime/llm/provider.go`

Previous behavior:

- real runtime tools were dropped
- only MCP meta-tools were encoded

Current behavior:

- real runtime tool definitions are encoded and sent
- MCP meta-tools are appended as supplemental tools

This is required for tools like `spawn_team` to be visible to the model.

### 5. Codex tool-call translation and replay are fixed

Fixed in:

- `internal/runtime/llm/provider.go`
- `internal/runtime/llm/adapter/codex.go`
- `internal/runtime/llm/provider_wrapper_test.go`
- `internal/runtime/llm/adapter/codex_request_test.go`

Behavior now:

- Codex flattened `tool_calls` are preserved when converting adapter output back into runtime `LLMResponse`
- follow-up Codex requests replay both the original `function_call` and the later `function_call_output`
- tool-call-only assistant turns no longer collapse into empty/no-op runtime responses

### 6. `agent_react` orchestration metrics now count executed tools correctly

Fixed in:

- `internal/api/skills/handler.go`
- `internal/api/skills/handler_test.go`

Behavior now:

- `orchestration.tool_call_count` is populated from observed tool executions on the `agent_react` path
- successful `spawn_team` runs now report `tool_call_count = 1` instead of `0`

## What Was Verified

### Code-level verification

Historical verification from the earlier change set included:

```powershell
Set-Location 'E:\projects\ai\ai-agent-runtime\backend'
go test ./internal/llm ./internal/bootstrap
go test ./internal/agent
go test ./internal/api/skills
```

Freshly re-run during this synchronization pass:

```powershell
Set-Location 'E:\projects\ai\ai-agent-runtime\backend'
go test ./internal/llm ./internal/toolbroker ./internal/agent ./internal/api/skills
```

Additional fresh verification after the Codex/tool-call fixes:

```powershell
Set-Location 'E:\projects\ai\ai-agent-runtime\backend'
go test ./internal/llm/...
go test ./internal/api/skills
```

### Runtime/API verification

Fresh live verification against the currently running local server:

- `POST /api/agent/chat` with:
  - `provider=CODEX_03`
  - `model=gpt-5.2-codex`
  - `enable_react=true`
  - `max_steps=4`
  returned `200`
- response showed:
  - `source = agent_react`
  - `trace_id = trace_271e7a61-89fa-4a27-b36c-73b183bdff79`
  - `tool_call_count = 1`
  - `observations[0].tool = spawn_team`

- `POST /api/agent/chat` with:
  - omitted `provider`
  - `model=gpt-5.2-codex`
  - `enable_react=true`
  - `max_steps=4`
  returned `200`
- response showed:
  - `source = agent_react`
  - `trace_id = trace_5ba24314-80c8-4a23-84aa-bba5e3f3c3d2`
  - `tool_call_count = 1`
  - `observations[0].tool = spawn_team`
- follow-up verification confirmed:
  - `GET /api/skills/teams/team-941f734dbd13/teammates` returned the created teammate
  - `GET /api/skills/teams/team-941f734dbd13/tasks` returned the created task
  - `GET /api/skills/runtime/traces/trace_5ba24314-80c8-4a23-84aa-bba5e3f3c3d2` returned `tool.requested/tool.completed/tool.reduced`

- `POST /api/agent/chat` with:
  - `provider=nvidia`
  - `model=z-ai/glm5`
  - `enable_react=true`
  - `max_steps=4`
  using unique `team_id`, teammate id, and task id returned `200`
- response showed:
  - `source = agent_react`
  - `status = completed`
  - `trace_id = trace_2982e4d2-180d-4d63-9104-9e7ba92aba75`
  - `tool_call_count = 1`
  - `observations[0].tool = spawn_team`
- follow-up verification confirmed:
  - `GET /api/skills/teams/team-33fb48b4c7bf` returned the created team
  - `GET /api/skills/teams/team-33fb48b4c7bf/teammates` returned teammate `mate-e9a73ddfd688`
  - `GET /api/skills/teams/team-33fb48b4c7bf/tasks` returned task `task-7fc523bed284`
  - `GET /api/skills/runtime/traces/trace_2982e4d2-180d-4d63-9104-9e7ba92aba75` returned `tool.requested/tool.completed/tool.reduced`

- One repeated `nvidia` run failed with:
  - `insert task: UNIQUE constraint failed: team_tasks.id`
- This was caused by reusing fixed IDs in the test prompt, not by adapter or tool-call translation failure.

Historical runtime notes from earlier sessions should therefore be treated as stale unless re-run against the current server state, especially any result that depended on:

- `default_provider = codex_998`
- server restarts around standalone `runtime-server`

What is still supported by code and tests:

- `/api/agent/chat` returns `trace_id` for agent/ReAct executions
- agent results include `observations`
- runtime traces can be queried through:
  - `GET /api/skills/runtime/traces`
  - `GET /api/skills/runtime/traces/{trace_id}`
- `spawn_team` broker execution is covered by unit test in `internal/runtime/toolbroker/broker_team_test.go`
- Codex flattened tool-call parsing and replay are covered by:
  - `internal/runtime/llm/provider_wrapper_test.go`
  - `internal/runtime/llm/adapter/codex_request_test.go`

## Current Problem Still Open

The previously open live end-to-end verification for `spawn_team` is now closed for the current server build.

Fresh live evidence now confirms:

- current default-provider behavior works with `defaultProvider: CODEX_03`
- explicit `CODEX_03` works
- explicit `nvidia + z-ai/glm5` works
- `enable_react = true` can execute `spawn_team`
- team, teammate, and task records are created through the agent path

The main remaining caution is operational rather than architectural:

- repeated test prompts should avoid reusing fixed `team_id` / `task_id` values unless `allow_existing` behavior is intentional

## Remaining Work

### 1. Keep canonical provider naming consistent

- keep `configs/runtime.yaml` aligned with canonical `CODEX_03`
- avoid drifting back to mixed-case provider names across runtime config and service config

### 2. Use unique IDs when re-running live `spawn_team` verification

Recommended:

```powershell
$teamId = 'team-' + ([guid]::NewGuid().ToString('N').Substring(0, 12))
$mateId = 'mate-' + ([guid]::NewGuid().ToString('N').Substring(0, 12))
$taskId = 'task-' + ([guid]::NewGuid().ToString('N').Substring(0, 12))
```

This avoids false negatives caused by:

- SQLite unique constraints on `team_tasks.id`
- intentional reuse of existing teams/tasks during manual verification

### 3. Optional follow-up

If broader coverage is still desired, add:

- a handler-level regression that exercises `/api/agent/chat -> ReAct -> spawn_team`
- optional request logging that records outgoing provider tool definitions for easier future debugging

### 4. Optional broader verification

Repeat the current live verification matrix when provider/model config changes:

- omitted `provider`, `model=gpt-5.2-codex`
- `provider=CODEX_03`, `model=gpt-5.2-codex`
- `provider=nvidia`, `model=z-ai/glm5`

## Practical Conclusion

At this point the provider/model configuration bug is fixed.

The workspace auto-scan bottleneck is fixed.

The tool encoding bug that hid `spawn_team` from the model is fixed.

Fresh code-level verification is green in the current workspace.

Fresh live verification now confirms:

- the current default-provider configuration is live-usable
- `spawn_team` executes successfully on the `agent_react` path
- team, teammate, and task records are created for both `CODEX_03` and `nvidia`
- `orchestration.tool_call_count` now reports executed tools correctly
