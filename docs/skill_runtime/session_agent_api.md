# Session Agent HTTP API

> 迁移说明（2026-03-30）：
> - 该 HTTP API 现在由独立 `runtime-server` 提供，代码位于 `E:\projects\ai\ai-agent-runtime\backend`
> - `ai-gateway` 已不再挂载 `/api/runtime/sessions/{id}/agents*`
> - 文中示例默认访问 `http://127.0.0.1:8081`

## Scope

This document describes the lightweight child-agent control plane exposed under an existing session:

- `POST /api/runtime/sessions/{id}/agents`
- `POST /api/runtime/sessions/{id}/agents/wait`
- `GET /api/runtime/sessions/{id}/agents/events`
- `GET /api/runtime/sessions/{id}/agents/{agent_id}`
- `POST /api/runtime/sessions/{id}/agents/{agent_id}/input`
- `GET /api/runtime/sessions/{id}/agents/{agent_id}/events`
- `POST /api/runtime/sessions/{id}/agents/{agent_id}/close`
- `POST /api/runtime/sessions/{id}/agents/{agent_id}/resume`
- `GET /api/runtime/sessions/{id}/agent-control/mailbox`

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
- `fork_turns`
  - 可选，精细控制复制多少轮父 session history。当前实现支持 `none`、`all` 或数字字符串/数值；未传时由 runtime config 的 `defaultForkTurns` 决定。

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
    "matched_id": "session_child",
    "matched_session_id": "session_child",
    "ready_count": 1,
    "ready_ids": ["session_child"],
    "waited_ms": 125,
    "next_action": "consume_ready_outputs"
  }
}
```

说明：

- 批量模式下，任一 child ready 就会返回。
- 单目标结果通过 `agent` 返回；批量结果同时返回轻量的匹配 `agent` 和完整
  `agents` 列表。同一份较大的 `output` 只序列化一次。
- 超时不会报错；而是返回当前快照并带 `timed_out=true`。
- `ready_ids` / `pending_ids` 给出精确集合；`waited_ms` 给出实际等待时间；
  `next_action` 给出下一步调度动作。超时且仍有 pending child 时，应先继续可独立
  工作，不要立即对未变化的目标重复等待。
- 当请求体没有 `id` / `session_id` / `ids` / `session_ids` 时，HTTP handler 会把目标设为父 session，并启用 `mailbox_only` 模式；这用于等待 parent mailbox / collab event，而不是等待某个 child session。

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

### `GET /api/runtime/sessions/{id}/agents/events`

读取父 session mailbox / collab event。该路由复用 `ListSessionAgentEvents`，因为没有路径级 `agent_id`，会启用 `mailbox_only` 模式。

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

### `GET /api/runtime/sessions/{id}/agent-control/mailbox`

读取 durable AgentControl mailbox rows，而不是把 mailbox 转换成 legacy runtime event 形态。

Query params:

- `after_seq`
- `limit`
- `wait_ms`

Response:

```json
{
  "result": {
    "session_id": "session_parent",
    "messages": [],
    "count": 0,
    "latest_seq": 0,
    "source": "agent_control_mailbox",
    "after_seq": 0,
    "control_only": true,
    "timed_out": true
  }
}
```

该入口适合需要读取 AgentControl 原生 mailbox seq / control seq 的调用方；只需要 child-agent 事件形态时继续使用 `/agents/events` 或 `/agents/{agent_id}/events`。

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
- `ready_ids`
- `pending_ids`
- `waited_ms`
- `next_action`

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
  `go run ./cmd/runtime-server serve --listen 127.0.0.1:8081`
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

## Agent Tool Argument Contract (model-facing)

本节记录运行时 agent tools 的参数契约。HTTP endpoint 与工具一一对应（见上文 endpoint summary），但工具层额外做了 fail-closed 校验，避免"参数被静默丢弃"导致 child 拿到空任务或指向错误会话。

### 通用规则

- 引用已有 child session 的工具（`close_agent` / `resume_agent`）用 `id` 或 `session_id` 定位目标；值必须是 `spawn_agent` / `list_agents` 返回的真实 session id，或 `session_ref_*` handle。
- 下面这些取值一律按"未提供"处理并报 `id is required`，不会再去创建或恢复任何会话：
  - 缺键，或 JSON `null`；
  - 从渲染结果里被复制回来的占位文本：`<nil>`、`nil`、`null`、`undefined`（大小写与首尾空格不敏感）。
- 该规则修掉了一个已知缺陷：`resume_agent {"id": null}` 曾把 `null` 渲染成字面量 `<nil>` 并当成 session id 使用，最终产生名为 `<nil>` 的会话行。需要重新取 id 时先调 `list_agents`。

### `spawn_agent`

- `message`（必填）：child 的初始任务 prompt。
- 别名：`goal` / `task` / `prompt`。`spawn_subagents`、`spawn_team` 用 `goal` 表达同一概念，为这些工具写好的参数可直接复用；命中别名时结果 metadata 会带 `arg_aliases: ["goal->message"]`。
- 缺少任务正文（或只有空白）时整个调用被拒绝：`message is required (accepted aliases: goal, task, prompt)`，并且不会创建 child session —— 空 prompt 的 child 无从知道自己的任务。
- 未知参数 fail-closed，并尽量给出替代方案：
  - `tools_whitelist` / `tools` / `whitelist` → `spawn_agent does not restrict the child tool surface; use read_only/permission_mode, or spawn_subagents for per-task tools_whitelist`；
  - `description` / `content` / `text` / `instruction(s)` / `task_description` → `did you mean message`；
  - `objective` / `task_goal` / `goal_text` → `did you mean message (alias goal)`。
- 支持的可选参数（allowlist）：`id`、`session_id`、`agent_type`、`difficulty`、`difficulty_rationale`、`provider`、`model`、`reasoning_effort`、`thinking_effort`、`permission_mode`、`completion_requirement`、`isolation`、`read_only`、`fork_context`、`fork_turns`、`timeout_sec`、`progress_timeout_sec`、`approval_timeout_sec`、`cancel_grace_sec`。

### `spawn_team`

- 每个 task 必须提供 `goal` 或 `title`；两者都空时报错并提示 `spawn_agent` 用 `message`、team task 用 `goal`。
- teammate / task 的其余字段见 `SpawnTeammateSpec` 与 `SpawnTaskSpec`。

### `spawn_subagents`

- 批量委派时每个子任务用 `goal`，并可在子任务上写 `tools_whitelist` 等逐任务参数；需要"逐任务工具白名单"时应使用 `spawn_subagents`，`spawn_agent` 不接受该参数。
- 模型直接给出的子任务在解码阶段按 planner 同款契约 fail-closed（此前只有 planner 生成的图会校验）：
  - `difficulty` 必须是可识别难度（`easy|normal|hard|expert`，以及 `low|medium|high`、`simple|standard|default`、`trivial` 等同义词，大小写与 `-`/空格不敏感），命中后归一化为规范值；无法识别时报错并回显原值，不再静默降级为默认档。
  - `id` 不能为空白；同一批内 `id` 不能重复（重复 id 会让 `depends_on` 无法唯一定位，因此直接拒绝）；省略 `id` 仍由运行时生成 `subagent_N`。
  - `completion_requirement` 只接受 `none|complete_task`（含 `complete-task` / `completetask` / 驼峰 `completionRequirement` 写法），命中后归一化为规范值；其他取值直接报错并回显原值（`want none|complete_task`），不再被折叠成 `none` 而静默关掉该子任务的收尾约束。
  - 逐任务字段的 JSON 类型同样 fail-closed：`budget_tokens`/`timeout` 必须是数字，`read_only` 必须是布尔，`tools_whitelist`/`depends_on`/`patches` 必须是数组；`tools_whitelist`/`depends_on` 的元素必须是非空字符串，`patches` 的元素必须是对象。类型不符时报错并回显实际类型（`must be a JSON number` 等），省略该字段即使用运行时默认值。此前 `depends_on: "writer"`（字符串而非数组）会被整段丢弃，使本该等待 writer 的 verifier 立刻开跑，是这批静默折叠里最危险的一个。
  - `depends_on` 只能引用同一批内已声明的 `id`；自依赖与循环依赖会被拒绝，未知依赖的错误信息会回显已知 id 列表。
  - 为什么必须失败而不是忽略：调度器把"依赖 id 不存在"与"依赖尚未完成"当作同一状态（`scheduler.go` 的 `dependenciesSatisfied`），一个拼错的 `depends_on` 过去会让该子代理永远停在未就绪状态——既不失败也不会被调度。
  - `budget_tokens` / `timeout` 按路由计划 §32.2 保持非致命（不因为取值不可用而让整批失败），但折叠不再静默：非整数会被截断（`*_truncated_to_integer`）、0 或负数按"未设置"处理并回落到路由/宿主默认（`*_ignored_non_positive`）、超过 `2147483647` 会被裁剪（`*_clamped_to_range`），三种情况都会写入该子任务的 `route_warnings`（可见于子代理记录与团队任务结果）。裁剪同时修掉了一个真实故障：过去 `timeout: 1e30` 经 `int` 转换 + `time.Duration(秒)*time.Second` 溢出成负值，语义从"超长超时"翻转成"立刻超时"。要"不限"就省略字段，不要写 `0`。

### 未知参数的可观测性（`ignored_args`）

- 除 `spawn_agent`（未知键直接失败）外，其他 broker 工具不会因为多传键而让整个调用失败，但也不会再静默丢弃：结果 metadata 会带 `ignored_args: ["<key>", ...]`，并把可操作提示合并进 `next_action`（例如把 `goal` 传给 `wait_agent` → 提示改用 `spawn_agent` / `send_message`；把 `tools_whitelist` 传给不支持的工具 → 提示改用 `spawn_subagents` 的逐任务参数）。运行时会把 `next_action` 作为后续动作提示渲染给模型，因此拼错的键下一轮就能被纠正，而不是被反复重试。
- 工具定义（`properties`）里出现的属性一定属于该工具的合法参数：有测试守护 schema 与实现读取的键保持一致，避免"schema 宣传了但代码从不读取"的再次出现。
- 创建类工具对显式 id 的占位值做了区分处理：
  - `spawn_agent` 的 `id` / `session_id`、`spawn_team` 的 `team_id` 若收到渲染占位文本（`<nil>` / `null` / `nil` / `undefined`，大小写与空格不敏感）会直接报错，避免创建名为 `<nil>` 的会话或团队；省略该字段即由运行时生成。
  - `teammates[].id`、`teammates[].session_id`、`tasks[].id`、`workspace_id` 这类可省略字段遇到占位值会被丢弃并由运行时生成真实 id。
