# Skills API Result Contract

> 迁移说明（2026-03-30）：
> - 本文描述的 HTTP 契约当前由 `E:\projects\ai\ai-agent-runtime\backend` 中的独立 `runtime-server` 提供
> - `ai-gateway` 已不再挂载 `/api/agent`、`/api/skills`

## Scope

This document describes the JSON response contract for:

- `POST /api/runtime/skills/{name}/execute` (admin/debug; not the primary entry)
- `POST /api/agent/chat` (non-stream mode; canonical)
- `POST /api/agent/chat` (non-stream mode; compatibility alias)

The implementation lives in `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go`.

**入口说明**

- `POST /api/agent/chat` 是主入口，面向业务调用。
- `POST /api/agent/chat` 是兼容别名，响应会带 compatibility warning header。
- `POST /api/runtime/skills/{name}/execute` 仅用于 admin/debug（未来会进一步收口）。

## Request Additions

`POST /api/agent/chat`（兼容：`POST /api/agent/chat`）目前额外支持：

- `workspace_path`: 可选，本地 workspace 路径；若提供，会在入口构建 workspace context
- `planning_mode`: 可选，当前支持 `planner_preferred`
- `execute_planned_subagents`: 可选，开启后允许系统尝试自动执行 planner 推导出的子任务图
- `allow_write_planned_subagents`: 可选，仅在显式允许时才会自动执行含 writer 的计划图

当 `planning_mode=planner_preferred` 时，响应可能包含：

- `result.planning`
- `result.orchestration.planning_*`
- `result.subagent_summary`
- `result.subagent_results`

## `POST /api/runtime/skills/{name}/execute`

> **注意**：此端点仅用于 admin/debug；建议使用 `/api/agent/chat` 作为统一入口。

### Top-level response

```json
{
  "skill": "echo-skill",
  "status": "completed",
  "result": {
    "success": true,
    "output": "[step_1]: tool:echo_tool",
    "skillName": "echo-skill",
    "observations": []
  },
  "session_id": "session_xxx"
}
```

### Fields

- `skill`: executed skill name
- `status`: `completed` or `failed`
- `result`: raw skill executor result
- `session_id`: optional session identifier when session persistence is used

### `result`

Current fields are inherited from the runtime execute result:

- `success`
- `output`
- `skillName`
- `observations`
- `usage`
- `error`
- `error_code` (optional)
- `error_context` (optional)

This endpoint currently returns the executor-native result shape rather than the richer `AgentChat` wrapper.

For Go consumers using `pkg/skillsapi`, prefer:

- `ExecuteSkillResponse.DecodeResult()`
- `ExecuteSkillResult.GovernanceSummary()`
- `Observation.Governance()`

When the runtime can classify a failure as a structured runtime error, `result` may also contain:

- `error_code`: for example `AGENT_PERMISSION`
- `error_context`: machine-readable context such as required permissions or sandbox policy details

For workflow-backed skill execution, each `observations[*].metrics` entry may also include runtime governance hints such as:

- `mcp_name`
- `mcp_trust_level`
- `execution_mode`

## `POST /api/agent/chat` (non-stream)

> 兼容别名：`POST /api/agent/chat`

### Top-level response

```json
{
  "session_id": "session_xxx",
  "agent_id": "api-agent",
  "result": {
    "kind": "llm",
    "source": "llm_fallback",
    "success": true,
    "output": "hello from llm",
    "model": "test-model",
    "tool_calls": [],
    "reasoning": "",
    "metadata": {},
    "orchestration": {},
    "planning": {}
  },
  "source": "llm_fallback",
  "status": "completed"
}
```

### Top-level fields

- `session_id`: resolved or created session ID
- `agent_id`: current API agent name, currently `api-agent`
- `result`: normalized result payload
- `source`: copied from `result.source` for quick branching
- `status`: request completion status, currently `completed`
- `workspace_path`: request-only field; not echoed by default
- `planning_mode`: request-only field; influences optional `planning` payload

For Go consumers using `pkg/skillsapi`, prefer:

- `AgentChatResponse.DecodeResult()`
- `AgentChatResult.DecodeOrchestration()`
- `AgentChatResult.DecodePlanning()`
- `AgentChatResult.GovernanceSummary()`

## `result.kind`

### `kind = "llm"`

Produced when the request is answered through the LLM path.

```json
{
  "kind": "llm",
  "source": "llm_fallback",
  "success": true,
  "output": "fallback response",
  "usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  },
  "model": "test-model",
  "tool_calls": [],
  "reasoning": "",
  "metadata": {},
  "orchestration": {},
  "planning": {}
}
```

Fields:

- `kind`: fixed to `llm`
- `source`: one of:
  - `llm_fallback`
  - `llm_stream` (streaming contract only)
- `success`
- `output`
- `usage`
- `model`
- `tool_calls`
- `reasoning`
- `metadata`
- `orchestration`
- `planning` (optional)

Go client 可直接用 `AgentChatResult.DecodeToolCalls()` 读取 typed `tool_calls`。
Go client 也可用 `AgentChatResult.DecodeUsage()` 读取 typed `usage`。
对于弱约束 `metadata`，Go client 推荐使用 `MetadataValue/String/Bool/Int/Map` accessor。

### `kind = "agent"`

Produced when the request is answered through the agent/skill route.

```json
{
  "kind": "agent",
  "source": "agent_route",
  "success": true,
  "output": "[step_1]: tool:echo_tool",
  "skill": "route-skill",
  "steps": 0,
  "observations": [],
  "state": {
    "currentStep": 0,
    "running": false,
    "errors": [],
    "context": null
  },
  "usage": null,
  "duration": {
    "start": "2026-03-09T09:20:25Z",
    "end": "2026-03-09T09:20:25Z"
  },
  "error": "",
  "orchestration": {},
  "planning": {}
}
```

Fields:

- `kind`: fixed to `agent`
- `source`: one of:
  - `agent_route`
  - `agent_direct`
- `success`
- `output`
- `skill`
- `steps`
- `observations`
- `state`
- `usage`
- `duration`
- `error`
- `orchestration`
- `planning` (optional)

Go client 可直接用 `AgentChatResult.DecodeDuration()` 读取 typed `duration`。
Go client 也可用 `AgentChatResult.DecodeState()` 读取 typed `state`。
- `subagent_summary` (optional)
- `subagent_results` (optional)

## `result.source`

Current non-stream source values:

- `agent_route`: route enabled and a skill result was used
- `agent_direct`: agent path used directly without LLM fallback
- `agent_planned_subagents`: planner 生成的子任务图被自动执行，结果由 parent 汇总返回
- `llm_fallback`: route attempt did not produce a usable skill result, so the LLM answered

## `result.orchestration`

Shared summary object for both `kind = "llm"` and `kind = "agent"`.

Fields:

- `source`
- `route_attempted`
- `route_matched`
- `candidate_count`
- `route_candidates`
- `capability_candidates`
- `capability`
- `fallback_reason`
- `skill`
- `model`
- `success`
- `steps`
- `tool_call_count`
- `observation_summary`
- `output_preview`
- `planning_attempted` (optional)
- `planning_source` (optional)
- `plan_step_count` (optional)
- `subagent_task_count` (optional)
- `subagent_execution_requested` (optional)
- `subagent_execution_eligible` (optional)
- `subagent_execution_blocked_reason` (optional)
- `subagent_execution_attempted` (optional)
- `planning_error` (optional)

## `result.planning`

当 `planning_mode=planner_preferred` 时，响应可能带有 `result.planning`。

字段：

- `mode`
- `attempted`
- `planning_source`
- `planning_error`
- `step_count`
- `subagent_task_count`
- `subagent_execution_requested`
- `subagent_execution_eligible`
- `subagent_execution_blocked_reason`
- `subagent_execution_attempted`
- `subagent_execution_error`
- `subagent_result_count`
- `goal`
- `steps`
- `subagent_tasks`

每个 `steps` 项包含：

- `id`
- `description`
- `tool`
- `depends_on`
- `priority`

每个 `subagent_tasks` 项包含：

- `id`
- `role`
- `goal`
- `tools_whitelist`
- `depends_on`
- `read_only`

## `result.subagent_summary`

当 agent 结果中存在子代理执行时，可能返回聚合摘要：

Go client 可直接用 `AgentChatResult.DecodeSubagentSummary()` 读取 typed 结构。

- `batches`
- `count`
- `successful`
- `failed`
- `roles`
- `patch_count`
- `patch_paths`

## `result.subagent_results`

当 agent 结果中存在子代理执行时，可能返回结构化明细列表。

每项包含：

- `id`
- `role`
- `session_id`
- `parent_session_id`
- `parent_tool_call_id`
- `read_only`
- `budget_tokens`
- `success`
- `summary`
- `patches`
- `findings`
- `error`

## Tool Mutation Hints (`mutated_paths`)

部分工具（例如 shell / script 类工具）名称上不带明显的写入动词，但仍然可能修改文件。  
为了保证 checkpoint 与审计可用，runtime 会把以下字段视作 **mutation hints**：

- `mutated_paths` / `mutated_files`
- `changed_paths` / `changed_files`
- `patch` / `diff`

当 tool call 的参数中出现上述字段且非空时，ReAct loop 会把该 tool 视为“有可能修改”，并触发 checkpoint manager：

- **Before**: 按 hint 中的路径捕获预变更快照
- **After**: 执行结束后再采集快照并写入 checkpoint metadata

这套机制**不依赖 tool 名称**是否包含 `write/edit/patch/...`，因此适用于 `execute_shell_command` 这类“名义上不可判定是否写入”的工具。

Checkpoint 记录中的 `files` 元数据会包含这些路径的快照信息；它们不会自动出现在顶层 `result` 字段中。

### `route_candidates`

Each item contains:

- `skill`
- `score`
- `matched_by`
- `details`
- `chosen`
- `selection_reason`

### `capability_candidates`

Each item contains:

- `descriptor`
- `score`
- `matched_by`
- `details`

### `capability`

Selected capability descriptor when a skill is chosen.

### `observation_summary`

Fields:

- `count`
- `successful`
- `failed`
- `tools`
- `failed_tools`
- `failed_details`
- `step_durations_ms`
- `total_duration_ms`
- `max_duration_ms`
- `average_duration_ms`

These fields are also exposed through typed SDK structures:

- `OrchestrationSummary`
- `RouteCandidate`
- `ObservationSummary`
- `PlanningSummary`
- `PlanningStep`

## Compatibility Notes

- `source` is duplicated at the top level and inside `result`
- `result.kind` should be used as the primary branch for consumers
- `result.orchestration` is the stable summary layer and is preferred over inferring behavior from raw fields
- `ExecuteSkill` and `AgentChat` do not yet share the exact same `result` envelope

## Representative Tests

- `internal/api/skills/handler_test.go`
- `TestExecuteSkill_RunsWorkflow`
- `TestAgentChat_UsesLLMAndPersistsSession`
- `TestAgentChat_WorkspacePathBuildsContextAndStillUsesLLM`
- `TestAgentChat_PlannerPreferredIncludesPlan`
- `TestAgentChat_RouteFallbackIncludesOrchestration`
- `TestAgentChat_AgentRouteFailureIncludesObservationDetails`
