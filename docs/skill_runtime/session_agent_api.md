# Session Agent HTTP API

> 迁移说明（2026-03-30）：
> - 该 HTTP API 现在由独立 `runtime-server` 提供，代码位于 `E:\projects\ai\ai-agent-runtime\backend`
> - `ai-gateway` 已不再挂载 `/api/runtime/sessions/{id}/agents*`
> - 文中示例默认访问 `http://127.0.0.1:8081`

## Scope

This document describes the lightweight child-agent control plane exposed under an existing session:

- `POST /api/runtime/sessions/{id}/agents`
- `POST /api/runtime/sessions/{id}/agents/wait`
- `GET /api/runtime/sessions/{id}/agents/{agent_id}`
- `POST /api/runtime/sessions/{id}/agents/{agent_id}/input`
- `GET /api/runtime/sessions/{id}/agents/{agent_id}/events`
- `POST /api/runtime/sessions/{id}/agents/{agent_id}/close`
- `POST /api/runtime/sessions/{id}/agents/{agent_id}/resume`

实现入口位于：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\session_runtime_handlers.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\session_runtime_support.go`

它们对应运行时里的轻量 agent tools：

- `spawn_agent`
- `send_input`
- `wait_agent`
- `read_agent_events`
- `close_agent`
- `resume_agent`

## Concepts

- `parent session`
  - 路径中的 `{id}`，代表父会话。
- `child agent`
  - 实际上是一个轻量 child session。
  - 当前 `agent_id` 与返回结果里的 `session_id` 等价，可直接当 child session ID 使用。
- `ready state`
  - `wait` 认为以下状态已就绪：`idle`、`waiting_approval`、`waiting_input`、`stopped`、`missing`。
- `event cursor`
  - `events` 使用 `after_seq` 增量读取，并支持 `wait_ms` 长轮询。

## Endpoint Summary

### `POST /api/runtime/sessions/{id}/agents`

创建 child agent / child session。

Request body:

- `id` / `session_id`
  - 可选，自定义 child session ID；两者任填一个即可。
- `message`
  - 可选，若提供则会异步提交首条 prompt。
- `agent_type`
  - 可选，记录 child agent 类型，如 `explorer` / `worker`。
- `model`
  - 可选，记录期望模型。
- `fork_context`
  - 可选，`true` 时复制父 session history 到 child session。

Response:

```json
{
  "agent": {
    "id": "session_child",
    "session_id": "session_child",
    "parent_session_id": "session_parent",
    "agent_type": "explorer",
    "status": "idle",
    "exists": true,
    "created": true,
    "queued": false
  }
}
```

Status code:

- `201 Created`
  - 创建成功且未立即排队执行。
- `202 Accepted`
  - 创建成功且已通过 `message` 异步排队执行。

### `POST /api/runtime/sessions/{id}/agents/{agent_id}/input`

向已有 child agent 发送新的输入。

Request body:

- `id` / `session_id`
  - 可选；不传时默认使用路径里的 `{agent_id}`。
- `message`
  - 必填，新的 prompt。
- `interrupt`
  - 可选；若 child 当前处于忙碌状态，必须显式传 `true` 才会先中断再提交新输入。

Response:

```json
{
  "agent": {
    "session_id": "session_child",
    "queued": true
  }
}
```

Status code:

- `202 Accepted`

说明：

- `agent.status` 是提交后的即时快照，可能是 `running`、`idle`，也可能是其他当前状态。

### `POST /api/runtime/sessions/{id}/agents/wait`

等待一个或多个 child agent 进入 ready state。

Request body:

- `id` / `session_id`
  - 单个 child agent 标识。
- `ids` / `session_ids`
  - 批量 child agent 标识。
- `timeout_ms`
  - 可选，默认 `30000`。

Response:

```json
{
  "result": {
    "agent": {
      "session_id": "session_child",
      "status": "idle",
      "output": "child done"
    },
    "agents": [
      {
        "session_id": "session_child",
        "status": "idle",
        "output": "child done"
      }
    ],
    "matched_id": "session_child",
    "matched_session_id": "session_child",
    "ready_count": 1,
    "pending_count": 0,
    "timed_out": false
  }
}
```

说明：

- 批量模式下，任一 child ready 就会返回。
- 超时不会报错；而是返回当前快照并带 `timed_out=true`。

### `GET /api/runtime/sessions/{id}/agents/{agent_id}`

读取单个 child agent 的当前状态快照。

Response:

```json
{
  "agent": {
    "id": "session_child",
    "session_id": "session_child",
    "parent_session_id": "session_parent",
    "agent_type": "explorer",
    "status": "idle",
    "exists": true,
    "message_count": 3,
    "output": "latest assistant output",
    "session_state": "active",
    "current_turn_id": "turn_xxx",
    "pending_tool_name": "",
    "pending_tool_call_id": "",
    "last_message_role": "assistant",
    "last_message_preview": "latest assistant output"
  }
}
```

### `GET /api/runtime/sessions/{id}/agents/{agent_id}/events`

读取 child session 的 runtime events。

Query params:

- `after_seq`
  - 可选，从指定序号之后开始读；默认 `0`。
- `limit`
  - 可选，默认 `20`。
- `wait_ms`
  - 可选，默认 `0`；大于 `0` 时会长轮询到新事件或超时。

Response:

```json
{
  "result": {
    "session_id": "session_child",
    "events": [],
    "count": 0,
    "latest_seq": 0,
    "timed_out": true
  }
}
```

每条 event 当前可能包含：

- `seq`
- `type`
- `trace_id`
- `session_id`
- `tool_name`
- `agent_name`
- `timestamp`
- `payload`

### `POST /api/runtime/sessions/{id}/agents/{agent_id}/close`

停止 child actor，并尝试关闭 child session。

Response:

```json
{
  "agent": {
    "session_id": "session_child",
    "status": "stopped"
  }
}
```

### `POST /api/runtime/sessions/{id}/agents/{agent_id}/resume`

重新挂起/恢复一个已有 child session 的 actor。

Response:

```json
{
  "agent": {
    "session_id": "session_child",
    "status": "idle"
  }
}
```

## Common Result Fields

### `agent`

`spawn` / `status` / `input` / `close` / `resume` 共用 `AgentStatusResult`。

常用字段：

- `id`
- `session_id`
- `parent_session_id`
- `agent_type`
- `status`
- `exists`
- `created`
- `queued`
- `timed_out`
- `pending_approval`
- `pending_question`
- `message_count`
- `output`
- `error`
- `session_state`
- `current_turn_id`
- `pending_tool_name`
- `pending_tool_call_id`
- `last_message_role`
- `last_message_preview`

### `result` from `wait`

`wait` 返回 `AgentWaitResult`：

- `agent`
  - 首个 ready child 的快照。
- `agents`
  - 本次检查到的全部 child 快照。
- `matched_id`
- `matched_session_id`
- `timed_out`
- `ready_count`
- `pending_count`

### `result` from `events`

`events` 返回 `AgentEventsResult`：

- `session_id`
- `events`
- `count`
- `latest_seq`
- `timed_out`

## Curl Quick Start

下面示例假定：

- 已在 `E:\projects\ai\ai-agent-runtime\backend` 启动：
  `go run ./cmd/runtime-server --listen 127.0.0.1:8081`
- 本地安装了 `jq`

```bash
BASE_URL=http://127.0.0.1:8081
```

### 0. Create parent session

```bash
PARENT_SESSION_ID=$(curl -sS -X POST "$BASE_URL/api/runtime/sessions" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"demo-user","title":"child-agent-demo"}' \
  | jq -r '.session.id')
```

### 1. Spawn child agent

```bash
AGENT_ID=$(curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents" \
  -H "Content-Type: application/json" \
  -d '{
    "agent_type":"explorer",
    "fork_context":true
  }' \
  | jq -r '.agent.session_id')
```

如果想在创建时直接排队首条消息，可以把 `message` 一并带上；这时通常会返回 `202 Accepted`：

```bash
curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents" \
  -H "Content-Type: application/json" \
  -d '{
    "agent_type":"explorer",
    "message":"Summarize the parent session context first."
  }'
```

### 2. Send input

```bash
curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/$AGENT_ID/input" \
  -H "Content-Type: application/json" \
  -d '{
    "message":"Reply with exactly: child done"
  }'
```

若 child 正在运行且你要抢占当前 run：

```bash
curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/$AGENT_ID/input" \
  -H "Content-Type: application/json" \
  -d '{
    "message":"Stop current work and summarize progress.",
    "interrupt":true
  }'
```

### 3. Wait for completion or pause

单个 child：

```bash
curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/wait" \
  -H "Content-Type: application/json" \
  -d "{
    \"id\":\"$AGENT_ID\",
    \"timeout_ms\":10000
  }"
```

批量 child：

```bash
curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/wait" \
  -H "Content-Type: application/json" \
  -d "{
    \"ids\":[\"$AGENT_ID\",\"another-child-session-id\"],
    \"timeout_ms\":10000
  }"
```

### 4. Read status snapshot

```bash
curl -sS "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/$AGENT_ID"
```

### 5. Read events with cursor

立即读取最近事件：

```bash
curl -sS "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/$AGENT_ID/events?after_seq=0&limit=20&wait_ms=0"
```

长轮询等待新事件：

```bash
curl -sS "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/$AGENT_ID/events?after_seq=20&limit=20&wait_ms=5000"
```

### 6. Resume child agent

```bash
curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/$AGENT_ID/resume"
```

### 7. Close child agent

```bash
curl -sS -X POST "$BASE_URL/api/runtime/sessions/$PARENT_SESSION_ID/agents/$AGENT_ID/close"
```

## Behavior Notes

- `spawn` 会创建真正的 child session，结果里的 `session_id` 就是后续所有控制操作的主标识。
- `fork_context=true` 会复制父 session history，但 child 仍然是独立 session。
- `send_input` 默认不做队列；若 child 正忙且未显式 `interrupt=true`，会返回 `400`。
- `wait` 是轮询语义，不是 SSE 推送；适合做轻量 orchestration barrier。
- `events` 也是拉模型；推荐使用 `after_seq + wait_ms` 做增量消费。
- `close` 之后仍可对已有 child session 调 `resume`，前提是底层 session 仍可恢复。

## Error Notes

常见 `400 Bad Request`：

- 缺少 `id` / `agent_id`
- `send_input` 缺少 `message`
- child 正忙但未设置 `interrupt=true`
- `after_seq` / `limit` / `wait_ms` 非法
- 自定义 child `session_id` 已存在

常见 `503 Service Unavailable`：

- handler 没有接上 `session manager` / `agent session controller`
