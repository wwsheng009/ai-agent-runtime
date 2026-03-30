# Light Agent Control Plane 实现状态

Date: 2026-03-18
Scope: 当前仓库新增的轻量 child-agent 控制工具

> 迁移说明（2026-03-30）：
> - 这批轻量 child-agent 控制能力的 live HTTP/runtime 实现已迁到 `E:\projects\ai\ai-agent-runtime\backend`
> - 文中旧的 `internal/runtime/<subpkg>` 路径，在独立 runtime 仓里对应为 `internal/<subpkg>`
> - `internal/api/skills/*` 与 `cmd/aicli/*` 的 runtime 侧实现也应以 `ai-agent-runtime` 仓库为准
> - 如需按当前仓库定位代码，请优先映射到 `backend/internal/*` 与 `backend/cmd/aicli/*`

## 已实现

当前系统已在 `toolbroker` 增加第一批轻量 agent 控制工具：

- `spawn_agent`
- `send_input`
- `wait_agent`
- `read_agent_events`
- `close_agent`
- `resume_agent`

这些工具不是替代现有：

- `spawn_subagents`
- `spawn_team`

而是补出一层更接近 Codex 风格的轻量控制面。

---

## 设计位置

### 1. 工具定义与分发

核心位置：

- `E:\projects\ai\ai-agent-runtime\backend\internal\toolbroker\types.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\toolbroker\broker.go`

新增了：

- 轻量 agent tool constants
- `AgentSessionController` 接口
- `SpawnAgentArgs / SendAgentInputArgs / WaitAgentArgs / ReadAgentEventsArgs`
- `AgentStatusResult / AgentWaitResult / AgentEventsResult`

### 2. API 侧适配器

核心位置：

- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills\session_runtime_support.go`

这里实现了：

- 基于 `SessionManager + SessionHub` 的 child session 控制
- child session 创建
- `fork_context` 时复制父 session history
- child session 的 `agent_type / model` 上下文记录
- wait/poll 状态读取
- 批量 `wait_agent`（`ids` / `session_ids`）
- child session event store 读取

### 3. CLI / 本地运行时适配器

核心位置：

- `E:\projects\ai\ai-agent-runtime\backend\cmd\aicli\commands\chat_actor_registry.go`
- `E:\projects\ai\ai-agent-runtime\backend\cmd\aicli\commands\chat_actor_host.go`

这里实现了：

- 本地 `SessionStore + SessionHub` 的 child session 控制
- 本地 child session 的 `agent_type / model` 透传

### 4. SessionActor 支撑

核心位置：

- `E:\projects\ai\ai-agent-runtime\backend\internal\chat\actor.go`

新增：

- `SubmitPromptAsync`

这样 `spawn_agent` / `send_input` 可以非阻塞地启动 child run。

---

## 当前行为

### `spawn_agent`

做的事：

1. 创建 child session
2. 可选复制 parent history（`fork_context=true`）
3. 记录：
   - `agent_parent_session_id`
   - `agent_type`
   - `agent_requested_model`
4. 可选异步提交首条 prompt

### `send_input`

做的事：

1. 找到现有 child session actor
2. 如果指定 `interrupt=true`，先尝试中断活跃 run
3. 异步提交新 prompt

### `wait_agent`

做的事：

1. 支持单个 id 或批量 `ids / session_ids`
2. 轮询 child session actor 状态
3. 在以下状态返回：
   - `idle`
   - `waiting_approval`
   - `waiting_input`
   - `stopped`
   - `missing`
4. 批量模式下，任一 child 进入 ready 状态即返回
5. 超时会返回当前快照并标记 `timed_out=true`

### `read_agent_events`

做的事：

1. 从 session event store 读取 child session 的 runtime events
2. 支持：
   - `after_seq`
   - `limit`
   - `wait_ms`
3. `wait_ms > 0` 时会轮询直到出现新事件或超时

### `close_agent`

做的事：

1. 停止内存中的 actor
2. 尝试关闭 session

### `resume_agent`

做的事：

1. 对已有 session 重新创建 actor
2. 返回当前状态快照

---

## 当前限制

这批工具是第一阶段实现，还不是完整 Codex multi-agent 等价物。

### 已支持

- child session 生命周期最小闭环
- parent -> child prompt 派发
- 批量 wait/close/resume
- API 与本地 CLI 双侧接线
- richer status：
  - `current_turn_id`
  - `pending_tool_name`
  - `pending_tool_call_id`
  - `last_message_role`
  - `last_message_preview`
  - `session_state`
- child session event read / poll

### 未完全支持

- `items` 多模态输入
- 真正的 queued mailbox 模型（当前更像直接再次 submit）
- provider/model/profile 的完整 role layering
- rich final status（仅返回轻量 session 快照，不是完整 thread transcript）
- parent/child session 之间的强一致 push 订阅（当前是 event store 轮询）

### 与现有多代理层的关系

- `spawn_subagents`：适合单 turn 内的结构化 fan-out
- `spawn_team`：适合持久化任务协作
- `spawn_agent`：适合轻量 child session 控制

三者现在是分层共存，而不是互相替代。

---

## 回归测试

当前新增工具已覆盖：

- `E:\projects\ai\ai-agent-runtime\backend\internal\toolbroker\broker_agent_test.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\chat` 相关异步/状态测试主链
- `E:\projects\ai\ai-agent-runtime\backend\internal\policy\capability_test.go`
- `E:\projects\ai\ai-agent-runtime\backend\internal\api\skills`
- `E:\projects\ai\ai-agent-runtime\backend\cmd\aicli\commands`

---

## 后续建议

下一阶段若继续对齐 Codex，建议优先做：

1. `spawn_agent` 的 richer status/result contract
2. `send_input` 的明确 queue/interrupt 语义
3. child session events 的 push 订阅/统一等待机制
4. provider/model/profile role layering
5. 更接近 Codex 的 final status/result contract（如 completed/failed artifact refs）
