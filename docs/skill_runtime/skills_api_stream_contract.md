# Skills API Stream Contract

> 迁移说明（2026-03-30）：
> - 本文描述的 SSE 契约当前由 `E:\projects\ai\ai-agent-runtime\backend` 中的独立 `runtime-server` 提供
> - `ai-gateway` 已不再挂载 `/api/agent`、`/api/skills`

## Scope

This document describes the SSE contract for `POST /api/agent/chat` when:

- request body includes `"stream": true`, or
- request header includes `Accept: text/event-stream`

The implementation lives in `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\handler.go`.

## Request Additions

`POST /api/agent/chat`（兼容：`POST /api/skills/agent/chat`）的流式请求目前额外支持：

- `workspace_path`
- `planning_mode`
- `execute_planned_subagents`
- `allow_write_planned_subagents`

当前 `planning_mode` 取值：

- `planner_preferred`

流式执行分两种路径：

- `llm_stream`：LLM token 流式输出，触发 `chunk` / `reasoning` / `tool_*` 等事件。
- `static_sse`：用于 agent 路由/编排结果（含 planned subagents）。返回的是静态 SSE 事件序列，不提供增量 token 与 tool delta。

当 `execute_planned_subagents=true` 时，流式请求 **支持执行 planned subagents**，并通过 `static_sse` 返回 `planning / orchestration / subagent / result / done` 等事件。

## Transport

- Response content type: `text/event-stream`
- Cache headers: disabled
- Event format: standard SSE

Each event uses:

```text
event: <event_name>
data: <json>
```

## Stable Envelope

Every SSE payload includes an `_event` object:

```json
{
  "_event": {
    "name": "chunk",
    "schema_version": "skill_runtime.sse.v1",
    "timestamp": "2026-03-09T09:20:25.8099167+08:00",
    "sequence": 3
  }
}
```

Fields:

- `name`: event name
- `schema_version`: current stream schema version
- `timestamp`: RFC3339Nano timestamp
- `sequence`: per-response monotonic sequence number

## Event Types

### `meta`

Sent once at stream start.

Common fields:

- `session_id`
- `agent_id`
- `source`
- `kind`
- `status`
- `orchestration`
- `planning` (optional)

Additional LLM fields may include:

- `model`

### `chunk`

Compatibility event for incremental consumers.

For text output:

```json
{
  "index": 5,
  "type": "text",
  "content": "hello ",
  "total_chars": 6,
  "text": {
    "content": "hello ",
    "total_chars": 6
  }
}
```

For non-text stream items, `chunk` mirrors the richer event payload.

### `reasoning`

Explicit reasoning delta event.

```json
{
  "index": 1,
  "type": "reasoning",
  "content": "thinking...",
  "reasoning": {
    "content": "thinking...",
    "delta": "thinking...",
    "length": 11
  },
  "metadata": {}
}
```

### `tool_start`

Tool-start event with normalized tool payload.

```json
{
  "index": 2,
  "type": "tool_start",
  "content": "search",
  "tool": {
    "id": "tool-1",
    "name": "search",
    "args": {
      "query": "weather"
    },
    "status": "tool_start",
    "content": "search"
  },
  "tool_call": {
    "id": "tool-1",
    "name": "search",
    "arguments": {
      "query": "weather"
    }
  },
  "delta": null,
  "metadata": {}
}
```

### `tool_call`

Tool-call delta event.

```json
{
  "index": 3,
  "type": "tool_call",
  "content": "",
  "tool": {
    "id": "tool-1",
    "name": "search",
    "args": {
      "query": "weather"
    },
    "status": "tool_call",
    "content": ""
  },
  "tool_call": null,
  "delta": {
    "id": "tool-1",
    "name": "search",
    "arguments": {
      "query": "weather"
    }
  },
  "metadata": {}
}
```

### `tool_end`

Tool-end event with normalized tool payload.

### `orchestration`

Structured orchestration summary. Present in both stream and non-stream results.

Fields include:

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

Example:

```json
{
  "source": "agent_route",
  "route_attempted": true,
  "route_matched": true,
  "candidate_count": 1,
  "route_candidates": [
    {
      "skill": "route-skill",
      "score": 1.2,
      "matched_by": "keyword:route",
      "details": "keyword match",
      "chosen": true,
      "selection_reason": "selected"
    }
  ],
  "fallback_reason": "",
  "skill": "route-skill",
  "model": "",
  "success": true,
  "steps": 0,
  "tool_call_count": 0,
  "observation_summary": {
    "count": 1,
    "successful": 1,
    "failed": 0,
    "tools": ["echo_tool"],
    "failed_tools": [],
    "failed_details": [],
    "step_durations_ms": {
      "step_1": 12
    },
    "total_duration_ms": 12,
    "max_duration_ms": 12,
    "average_duration_ms": 12
  },
  "output_preview": "[step_1]: tool:echo_tool"
}
```

### `planning`

可选 planning 摘要事件。

当 `planning_mode=planner_preferred` 时发出。

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

每个 `subagent_tasks` 项可能包含：

- `id`
- `role`
- `goal`
- `tools_whitelist`
- `depends_on`
- `read_only`

### `route`

Emitted for `static_sse` responses when route metadata is available.

Fields:

- `source`
- `skill`
- `route_attempted`
- `route_matched`
- `candidate_count`
- `route_candidates`

### `observation`

Emitted for `static_sse` responses when observations are available.

One event per observation.

Fields:

- `index`
- `step`
- `tool`
- `success`
- `error`
- `duration_ms`
- `input`
- `output`
- `metrics`

### `result`

Structured final result payload.

For `llm_stream`:

- `kind`
- `source`
- `success`
- `output`
- `model`
- `reasoning`
- `tool_events`
- `orchestration`
- `planning` (optional)

For `agent_route` / `agent_direct`:

- `kind`
- `source`
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
- `subagent_summary` (optional)
- `subagent_results` (optional)

### `subagent`

Emitted for `static_sse` responses when `subagent_results` are available.

Fields:

- `index`
- `id`
- `role`
- `session_id`
- `read_only`
- `budget_tokens`
- `success`
- `summary`
- `findings`
- `patches`
- `error`

### `done`

Terminal success event.

Fields:

- `session_id`
- `agent_id`
- `source`
- `status`
- `content`
- `result`

### `error`

Terminal error event for stream failures.

Fields:

- `index`
- `message`
- `source`

## Source Values

Current `source` values:

- `agent_route`
- `agent_direct`
- `agent_planned_subagents`
- `llm_fallback`
- `llm_stream`

## Compatibility Notes

- `chunk` remains available for older consumers.
- Richer event types (`reasoning`, `tool_*`, `orchestration`, `route`, `observation`, `result`) are additive.
- `planning` is additive and only appears when planning preview is enabled.
- `subagent` is additive and only appears when planned subagents are executed.
- `static_sse` responses emit a single `chunk` with the full output (not token deltas).
- Consumers should prefer `_event.schema_version` over ad-hoc field guessing.

## Go Client Typed Helpers

For Go consumers using `pkg/skillsapi`, prefer the typed stream helpers over manual `map[string]interface{}` decoding:

- `Stream.NextDecoded()`
- `Stream.Consume()`
- `StreamHandlers`
- `StreamEvent.DecodeTyped()`
- `StreamEvent.DecodeEnvelopeMeta()`
- `StreamEvent.DecodeMetaPayload()`
- `StreamMetaPayload.DecodeOrchestration()`
- `StreamMetaPayload.DecodePlanning()`
- `StreamEvent.DecodeChunkPayload()`
- `StreamChunkPayload.DecodeText()`
- `StreamChunkPayload.DecodeReasoning()`
- `StreamChunkPayload.DecodeTool()`
- `StreamChunkPayload.DecodeToolCall()`
- `StreamChunkPayload.DecodeDelta()`
- `StreamChunkPayload.MetadataValue/String/Bool/Int/Map`
- `StreamEvent.DecodePlanningPayload()`
- `StreamEvent.DecodeOrchestrationPayload()`
- `StreamEvent.DecodeRoutePayload()`
- `StreamEvent.DecodeObservationPayload()`
- `StreamEvent.DecodeResultPayload()`
- `StreamEvent.DecodeDonePayload()`
- `StreamEvent.DecodeErrorPayload()`

For terminal success events:

- `StreamDonePayload.DecodeResult()`
- `AgentChatResult.DecodeOrchestration()`
- `AgentChatResult.DecodePlanning()`
- `AgentChatResult.DecodeSubagentSummary()`
- `AgentChatResult.DecodeToolCalls()`
- `AgentChatResult.DecodeUsage()`
- `AgentChatResult.DecodeDuration()`
- `AgentChatResult.DecodeState()`
- `AgentChatResult.MetadataValue/String/Bool/Int/Map`

This keeps SSE consumers aligned with the same typed result helpers used by non-stream responses.

For the highest-level path, `Stream.NextDecoded()` dispatches the next SSE item into a `DecodedStreamEvent` with exactly one of `Meta / Chunk / Planning / Orchestration / Route / Observation / Result / Done / Error` populated for known event types.

If consumers want to avoid the loop entirely, `Stream.Consume(StreamHandlers{...})` provides callback-based dispatch over the same typed event set and closes the stream on terminal `done` / `error` events.

## Test Coverage

Representative tests:

- `internal/api/skills/handler_test.go`
- `TestAgentChat_StreamSSE`
- `TestAgentChat_StreamSSE_AgentRouteResult`
- `TestAgentChat_StreamSSE_AgentRouteResult_WithPlanning`
- `TestAgentChat_StreamSSE_LLMResult_WithPlanning`
- `TestAgentChat_RouteFallbackIncludesOrchestration`
