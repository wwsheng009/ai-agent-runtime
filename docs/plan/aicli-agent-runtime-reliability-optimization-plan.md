# aicli / AI Agent Runtime 可靠性与智能质量优化方案

更新时间：2026-07-13

## 1. 文档定位

本文基于一次覆盖前端构建、Go 嵌入打包、runtime-server 启动、浏览器运行时错误、工具调用、后台任务、Goal/Todo、子 Agent 与 Team 的真实长会话，对本项目 `ai-agent-runtime` 的 `aicli` 和 Agent Runtime 做系统性复盘，并形成可实施的优化计划。

本文不是单一缺陷修复清单，而是希望把当前已经存在但相对分散的能力，收敛为一套统一、持久化、可恢复、可观察的 Agent 执行模型。

相关代码与文档范围：

- `backend/internal/toolbroker/`
- `backend/internal/background/`
- `backend/internal/contextmgr/`
- `backend/internal/compactruntime/`
- `backend/internal/agentcontrol/`
- `backend/internal/team/`
- `backend/internal/chat/`
- `backend/internal/policy/`
- `backend/cmd/aicli/commands/`
- `docs/skill_runtime/current_architecture.md`
- `docs/working/light-agent-control-plane-2026-03-18.md`
- `docs/plan/multi-agent-agentcontrol-convergence-plan.md`
- `docs/multi-agents/agents/plan/architecture.md`

## 2. 背景与目标

当前项目已经具备一个较完整 Agent Runtime 所需的大部分组件：

- CLI 与 runtime-server 两类运行入口；
- Chat、Session、Context Compact 与流式输出；
- Tool Broker、Shell、文件、网络等工具；
- Approval 与权限模式；
- Background Job；
- Child Agent、Team、Mailbox、Task Graph；
- Goal、Todo 与 Skills；
- Web UI 以及前端静态资源嵌入 Go 二进制的发布方式。

从功能覆盖面看，项目已经超出普通聊天 CLI，接近一个本地 Agent 操作系统。但真实长会话暴露出：这些能力仍存在生命周期、状态权威、超时、恢复、可观测性和发布验证方面的边界不一致。

本方案的总目标是：

> 将 Tool、Background Job、Approval、Child Agent、Team、Goal 和 Context 统一到一个持久化、可恢复、可观察的执行内核中，使 aicli 在长会话、失败重试、多 Agent 协作和生产发布场景下仍保持行为一致、上下文清晰、结果可解释。

具体目标：

1. 工具调用具备统一的超时、取消、重试、幂等和结果协议。
2. 长会话压缩前后，Goal、Todo、事实与执行状态保持一致。
3. 子 Agent 与 Team 在审批、失败、取消和部分完成时仍能稳定返回结构化结果。
4. 所有长时间运行的执行实体均可跨用户回合查询、恢复和审计。
5. runtime-server 能准确暴露自身后端版本和所嵌入前端的构建来源。
6. 发布门禁能发现“单元测试通过、生产嵌入包运行失败”这一类问题。

## 3. 当前能力与总体评价

### 3.1 已具备的优势

#### 工具系统

- Tool Broker 已形成明确的工具入口，而不是把所有行为散落在聊天命令中。
- 工具参数趋向结构化，支持 timeout、工作目录、权限模式和输出限制。
- 文件工具与 Shell 工具职责已经分离，便于细化权限和审计。
- 存在 Approval 机制，具备向最小权限模型演进的基础。

#### 上下文系统

- 已有 Context Manager 和 Compact Runtime，不是简单依赖模型完整上下文窗口。
- 会话中能保留工具调用、执行结果和用户目标，为长任务提供基础。
- Goal 和 Todo 已作为显式概念出现，具备从“聊天记录”升级为“任务状态”的条件。

#### Multi-Agent 系统

- Child Agent 与 Team 已有独立抽象。
- AgentControl、Mailbox、Task Graph 和 Team lifecycle 已覆盖多 Agent 调度的核心概念。
- 已经开始区分 parent/child session、team task 和协作事件。
- 存在等待、消息投递、恢复与审批中继等工具接口。

#### 工程与发布

- Go 服务和前端可被统一打包，部署形态简单。
- runtime-server 已有健康或运行时接口。
- 前后端代码在一个仓库内，适合建立嵌入式二进制端到端发布门禁。

### 3.2 总体评价

当前主要问题不是“缺少功能”，而是“多个功能拥有相似但不完全一致的生命周期语义”。例如：

- Shell 有 timeout，Background Job 也有 timeout，但实际生效来源可能不同；
- Tool 有执行状态，Child Agent 有运行状态，Team 有任务状态，但终态和结果契约不统一；
- Goal、Todo 和聊天 Session 都能表达任务信息，但状态作用域未完全绑定；
- Context Compact 能压缩文本，却不一定能校验压缩后的事实状态与持久化执行状态是否一致。

因此下一阶段不应继续横向堆叠更多工具，而应优先建设统一 Durable Execution Kernel。

## 4. 本次真实会话暴露的问题

### 4.1 显式超时未成为唯一权威

会话中对 Shell 设置过 `3m`、`10m` 等显式 timeout，但执行仍出现“超过 30s”被终止的消息。

这说明至少存在两层超时：

- 工具调用参数中的业务 timeout；
- Tool Broker、Shell runner、RPC、Chat Turn 或宿主进程中的默认 30 秒 timeout。

如果最终生效的是外层默认值，而日志只展示内层配置，用户和 Agent 都无法正确推断失败原因，也无法决定应该重试、改为后台运行还是拆分命令。

### 4.2 Background Job 无法跨用户回合稳定查询

某个后台任务创建后返回 Job ID，但进入下一用户回合后再读取时出现：

```text
unknown background job reference
```

这表明 Job 引用可能只存在于当前进程内存、当前 turn registry 或临时工具上下文，而不是会话级持久状态。后台任务如果不能跨回合查询，本质上仍是延迟返回的前台任务，不能成为可靠的异步执行原语。

### 4.3 Goal 与 Todo 状态作用域不一致

会话中 `get_goal` 返回 `null`，但 Todo 状态却暴露了无关的历史 ResourceManager 任务。

可能原因包括：

- Goal 绑定当前 Session，而 Todo 绑定全局或旧 Session；
- Compact/恢复后 Goal 未重建，但 Todo 从其他存储恢复；
- Goal/Todo 的 owner key、workspace key 或 session key 不一致；
- UI/工具读取了不同的状态权威。

这会造成严重上下文漂移：Agent 可能依据旧 Todo 继续执行与当前用户目标无关的任务。

### 4.4 Read-only Child Agent 仍进入 Approval，并在等待中丢失

一个只读 Reviewer Agent 进入 `waiting_approval`，随后变成 `stopped/context canceled`。

这里暴露了三个边界问题：

1. 只读任务的能力声明没有自动映射到预授权策略；
2. Child Agent 的审批请求未稳定中继给 Parent；
3. Parent turn 或上下文取消导致 Child Agent 一并被取消，等待审批状态缺少持久恢复能力。

### 4.5 Team 失败时缺少可消费的部分结果

一个三成员 Review Team 最终状态为 `failed`，但没有稳定返回最终 summary，也没有聚合已经完成成员的部分结果。

Team 是复合执行单元，成员部分成功是常见情况。只返回 `failed` 会丢失已付出的 token、时间和有效证据。Team 终态必须区分：

- 全部成功；
- 部分完成；
- 因依赖阻塞；
- 因审批阻塞；
- 因系统错误失败；
- 因用户取消。

并且每一种终态都必须生成结构化 summary。

### 4.6 运行端口可能仍指向旧二进制

新打包二进制已经生成，但端口 `8101` 上仍运行旧进程。健康接口只返回服务名和路由，无法判断：

- 当前进程的 Git commit；
- 二进制构建时间；
- 前端资源 hash；
- 前端构建时间；
- 是否为 dirty build；
- 当前配置来源。

这会把“版本未更新”误判为“代码修复无效”。

### 4.7 Prism 单测通过但生产 chunk 失败

开发或单元测试路径正常，但访问嵌入 Go 二进制的生产前端时出现：

```text
Uncaught ReferenceError: Prism is not defined
```

这说明测试未覆盖真实的 production bundle、chunk 拆分、加载顺序和静态资源嵌入结果。前端成功执行 `build` 不等于嵌入式应用可运行，必须从最终 Go 二进制启动浏览器 E2E。

## 5. 工具调用系统优化

### 5.1 建立统一 Tool Invocation 记录

每一次工具调用都应先创建持久化 `tool_invocation`，再进入审批和执行，而不是只保留在模型消息或临时内存中。

建议字段：

```text
id
session_id
turn_id
agent_run_id
parent_invocation_id
tool_name
arguments_digest
arguments_ref
capability_scope
permission_mode
state
created_at
queued_at
started_at
finished_at
timeout_requested_ms
timeout_effective_ms
timeout_source
cancel_source
attempt
max_attempts
idempotency_key
result_ref
artifact_refs
error_code
error_message
```

其中 `timeout_effective_ms` 和 `timeout_source` 必须由系统在执行前解析完成并记录。例如：

```text
timeout_requested_ms = 600000
timeout_effective_ms = 30000
timeout_source = chat_turn_deadline
```

如果上层 deadline 更短，应在执行前明确提示，而不是运行 30 秒后才给出模糊超时。

### 5.2 统一 timeout 与 cancel 传播

建议 deadline 计算规则：

```text
effective_deadline = min(
  invocation_timeout,
  agent_run_deadline,
  turn_deadline,
  runtime_shutdown_deadline
)
```

要求：

1. 每一层只负责贡献 deadline，不直接覆盖下层参数。
2. 最终有效 deadline 在工具开始前可观察。
3. timeout 与 cancel 使用不同错误码。
4. cancel 必须记录来源：用户、Parent Agent、Team、runtime shutdown 或 policy。
5. 长命令若不适合 turn deadline，应自动建议或切换为 durable background execution。

建议错误码：

```text
TOOL_TIMEOUT
TURN_DEADLINE_EXCEEDED
AGENT_RUN_CANCELED
USER_CANCELED
RUNTIME_SHUTDOWN
APPROVAL_EXPIRED
OUTPUT_LIMIT_EXCEEDED
```

### 5.3 将 Background Job 升级为持久执行实体

Background Job 必须满足：

- Job ID 在同一 workspace/session 的后续回合仍可查询；
- CLI 重启后可识别 running、orphaned 或 completed 状态；
- stdout/stderr 分块写入 artifact store；
- 记录 OS PID、process group、启动命令摘要和工作目录；
- 支持增量 offset 读取；
- 支持 cancel、retry、adopt/reconcile；
- 进程消失但没有终态记录时标记 `orphaned`，而不是返回 unknown；
- 通过 retention policy 清理，而不是 turn 结束即丢失。

建议后台任务查询顺序：

1. 按 Job ID 查询持久存储；
2. 若状态为 running，检查 process handle/PID；
3. 若进程已退出，补写终态；
4. 若无法确认，标记 orphaned 并给出恢复建议；
5. 只有 ID 从未存在时才返回 `JOB_NOT_FOUND`。

### 5.4 增加幂等和安全重试

工具应声明重试属性：

```text
retry_class = never | safe | idempotency_key_required | compensatable
```

示例：

- `view`、`grep`、`glob`：通常为 `safe`；
- 固定 URL 的 `fetch`：可有限重试；
- `write`、`apply_patch`：要求前置版本或幂等键；
- Shell：默认 `never`，除非调用方明确声明；
- 创建 Child Agent/Team：使用稳定 request ID 防止重复创建。

### 5.5 能力范围驱动动态工具面

不同 Agent 不应默认获得全部工具。建议根据任务声明生成最小工具集合：

```text
capabilities:
  - workspace.read
  - workspace.write:docs/plan/**
  - shell.test
  - network.fetch
  - agent.spawn
```

收益：

- 降低模型选错工具的概率；
- 减少 prompt 中工具 schema 数量和 token；
- 只读 Agent 可预授权只读工具；
- Approval 可以基于 capability scope，而不是逐工具硬编码。

### 5.6 标准化工具结果契约

工具结果至少包含：

```json
{
  "ok": true,
  "state": "succeeded",
  "summary": "...",
  "data": {},
  "artifacts": [],
  "warnings": [],
  "metrics": {
    "duration_ms": 0,
    "output_bytes": 0,
    "attempt": 1
  }
}
```

失败结果必须区分 retryable 和 terminal，避免 Agent 只能解析自然语言错误。

## 6. 上下文、Goal 与 Todo 优化

### 6.1 明确状态层次

建议统一层次：

```text
Workspace
  └─ Chat Session
      └─ Goal
          └─ Agent Run / Turn
              └─ Todo / Execution Node
```

约束：

- 一个 Todo 必须属于明确的 `goal_id`；
- 一个 Goal 必须属于明确的 `session_id` 和 `workspace_id`；
- 默认查询只能返回当前 Goal 的 Todo；
- 跨 Goal/Session 查询必须显式指定 scope；
- 不允许 `get_goal = null` 时默认注入历史 Todo。

### 6.2 建立结构化 Fact Ledger

不要只依赖自然语言 compact summary 保留关键事实。建议为每个 Session/Goal 建立 Fact Ledger：

```text
fact_id
scope
kind
subject
predicate
value
source_event_id
evidence_ref
confidence
valid_from
invalidated_by
updated_at
```

事实类型示例：

- 用户约束：只新增文档，不修改其他文件；
- 工作区事实：当前工作树有未提交改动；
- 运行事实：8101 上的进程 build ID；
- 决策事实：选用 Go embed 发布前端；
- 执行事实：某测试通过或失败；
- 待办事实：仍需运行 production browser E2E。

Compact 时优先重建结构化事实摘要，而不是从全文再次猜测。

### 6.3 Context Compact 后执行一致性对账

每次 compact 完成后运行 reconciliation：

1. 当前 Goal 是否存在且 active；
2. Todo 是否全部属于当前 Goal；
3. 正在运行的 Tool/Job/Agent/Team 是否仍被引用；
4. 用户约束是否保留；
5. 已完成项是否被误标为 pending；
6. 失败项是否保留错误原因和证据；
7. 文本摘要与持久化状态冲突时，以持久状态为权威并生成 correction event。

### 6.4 将上下文拆分为稳定层和临时层

建议 prompt assembly 分层：

1. System/Policy：系统规则和权限。
2. Workspace Profile：仓库、语言、构建入口等稳定信息。
3. Active Goal：当前目标、范围和验收标准。
4. Fact Ledger：经过验证的关键事实。
5. Active Execution：正在运行或等待处理的 Tool/Job/Agent/Team。
6. Recent Turns：最近若干原始对话。
7. Retrieved Evidence：按当前决策检索的文件片段或日志。

避免把所有历史工具输出永久塞入上下文。

### 6.5 为决策绑定证据

重要结论应引用 evidence：

```text
decision_id
statement
evidence_refs[]
assumptions[]
confidence
supersedes
```

例如“8101 运行的是旧二进制”必须关联进程启动时间、build info 接口和文件 hash，而不是仅存在于自然语言推断中。

## 7. 子 Agent 与 Team 健壮性优化

### 7.1 统一 Child Agent Run 状态机

建议状态：

```text
created
queued
awaiting_approval
approved
running
streaming
blocked
succeeded
failed
partially_completed
timed_out
canceled
orphaned
```

状态迁移必须由持久事件驱动，并满足：

- 每次迁移包含 actor、reason 和 correlation ID；
- Parent turn 结束不自动取消 durable child；
- 需要跟随 Parent 取消的 child 显式声明 `lifetime=attached`；
- 可跨 turn 运行的 child 声明 `lifetime=durable`；
- runtime 重启后 running 状态必须 reconcile。

### 7.2 Approval Relay 成为正式协议

Child Agent 审批流程：

1. Child 创建持久化 approval request；
2. Agent Run 进入 `awaiting_approval`；
3. Parent mailbox 收到结构化事件；
4. Parent/UI 调用 resolve；
5. 结果写入 approval record；
6. Child 被 durable wake 唤醒并继续执行；
7. 到期时进入 `blocked` 或 `failed`，并写明 `APPROVAL_EXPIRED`。

只读任务若能力限定为 workspace read，应支持策略预授权，避免无意义等待。

### 7.3 标准 Child Agent Result Contract

所有 Child Agent 无论成功、失败或取消，都应返回：

```json
{
  "status": "succeeded",
  "summary": "完成了什么",
  "findings": [],
  "changes": [],
  "artifacts": [],
  "evidence": [],
  "remaining_work": [],
  "errors": [],
  "usage": {
    "input_tokens": 0,
    "output_tokens": 0,
    "tool_calls": 0,
    "duration_ms": 0
  }
}
```

即使 Agent 因审批或取消终止，也必须给出已完成工作和剩余工作，不能只返回 `context canceled`。

### 7.4 Team 必须支持部分成功

Team 聚合器应独立于 Lead Agent 的自然语言总结。Team 进入任何终态时，系统都从成员和任务记录生成机器可读 summary。

建议 Team 状态：

```text
succeeded
partially_completed
failed
blocked
timed_out
canceled
```

Team summary 至少包含：

- 总任务数及各状态数量；
- 成功成员的 findings/artifacts；
- 失败成员的错误码和最后 checkpoint；
- 未满足的依赖；
- 是否可重试及推荐重试范围；
- Lead summary（如果生成成功）；
- 系统 fallback summary（始终可生成）。

### 7.5 Checkpoint 与恢复

Child/Team 应在以下节点写 checkpoint：

- 开始执行；
- 完成一次关键工具调用；
- 生成关键发现；
- 进入审批等待；
- 上下文 compact 前；
- 被取消或超时前；
- 终态前。

恢复时不应简单重放完整 prompt，而应加载：

- 当前 Goal；
- capability scope；
- 最近 checkpoint；
- 已完成 Tool Invocation；
- 未完成 Todo；
- 相关 evidence/artifact。

### 7.6 调度与资源治理

增加：

- Workspace、Session、Team 三级并发上限；
- Token、工具调用次数、墙钟时间预算；
- 相同任务去重；
- 依赖感知调度；
- Writer lease，避免多个 Agent 同时改同一文件；
- Agent 心跳和 orphan 检测；
- 队列等待、执行、审批等待分别计时。

## 8. Durable Execution Kernel 目标架构

### 8.1 统一执行层次

```text
Chat Session
  └─ Goal
      └─ Chat Turn
          └─ Agent Run
              ├─ Tool Invocation
              ├─ Approval
              ├─ Background Job
              ├─ Child Agent Run
              └─ Team Run
                  └─ Team Task / Member Agent Run
```

每个节点都必须具备：

- Stable ID；
- 明确状态机；
- Durable Event；
- Parent/Root correlation；
- Deadline 与 cancel source；
- Retry/idempotency policy；
- Artifact/result reference；
- Resume/recovery 语义；
- Metrics 与 audit fields。

### 8.2 统一事件模型

建议事件 envelope：

```json
{
  "event_id": "evt_...",
  "event_type": "tool.invocation.succeeded",
  "aggregate_type": "tool_invocation",
  "aggregate_id": "ti_...",
  "workspace_id": "ws_...",
  "session_id": "ses_...",
  "goal_id": "goal_...",
  "turn_id": "turn_...",
  "agent_run_id": "run_...",
  "causation_id": "evt_...",
  "correlation_id": "corr_...",
  "sequence": 12,
  "occurred_at": "2026-07-12T00:00:00Z",
  "payload": {}
}
```

要求：

- 同一 aggregate sequence 单调递增；
- 消费者按 event ID 幂等；
- UI、CLI wait、Team aggregator 使用同一事件源或同一持久 projection；
- display event 只能是 projection，不能成为执行权威。

### 8.3 状态权威与 Projection

建议：

- 执行记录与 durable event 为写入权威；
- CLI/TUI/Web UI 使用 read model；
- Mailbox 是协作投递机制，不替代执行状态；
- Context summary 是派生视图，不替代 Goal、Todo 和 Run 状态；
- runtime 启动时执行 reconcile，修复 running 但实际进程不存在等不一致。

### 8.4 Artifact Store

长输出、日志、补丁、浏览器截图和 Agent 报告不要直接塞入事件或上下文，统一存储为 artifact：

```text
artifact_id
kind
content_type
size
sha256
storage_uri
producer_type
producer_id
created_at
retention_policy
```

事件与结果只保存引用和摘要。

## 9. P0：可靠性收敛

P0 优先解决“任务能否稳定完成和解释失败”的问题。

### P0.1 统一超时与取消语义

实施项：

1. 梳理 Chat Turn、Tool Broker、Shell runner、Background Job、Child Agent 和 Team 的所有 deadline 来源。
2. 增加 effective timeout 计算和日志。
3. 移除隐式 30 秒硬编码，或将其作为明确的上层 deadline 暴露。
4. 标准化 timeout/cancel 错误码。
5. 增加 3 分钟和 10 分钟显式 timeout 的回归测试。

验收：

- 请求 timeout 不短于上层 deadline 时，实际运行时间与请求一致；
- 被更短上层 deadline 截断时，结果明确显示来源；
- timeout 不再被报告为普通 context canceled。

### P0.2 持久化 Tool Invocation 与 Background Job

实施项：

1. 建立 Tool Invocation 和 Job 存储模型。
2. Job 输出写入分块 artifact。
3. Job ID 支持跨 turn 和 CLI 重启查询。
4. 增加 running/orphaned reconcile。
5. 增加 retention 和清理策略。

验收：

- 后台任务创建后，在至少三个用户回合后仍可查询；
- runtime 重启后可读取完成结果或识别 orphaned；
- 不再对曾存在的 Job 返回 unknown reference。

### P0.3 Goal/Todo 同域绑定

实施项：

1. Goal、Todo 增加统一 workspace/session/goal owner key。
2. 默认 Todo 查询必须由当前 active goal 限定。
3. 增加 legacy 数据迁移或隔离策略。
4. Compact/Resume 后运行状态对账。

验收：

- `get_goal = null` 时不会展示其他 Goal 的 Todo；
- 切换 Session 不会泄露旧 Todo；
- 恢复 Session 后 Goal 和 Todo 一致。

### P0.4 稳定 Approval Relay

实施项：

1. Approval Request 持久化。
2. Parent mailbox 接收 approval 事件。
3. waiting_approval 不因 turn 结束自动取消。
4. 只读 capability 支持预授权。
5. 增加审批超时与恢复。

验收：

- Child Agent 等待审批跨 turn 保持有效；
- Parent 可明确批准或拒绝；
- 只读 Reviewer 在策略允许时不触发写权限审批。

### P0.5 Team 终态强制结构化总结

实施项：

1. Team aggregator 从持久任务状态生成 fallback summary。
2. 引入 `partially_completed`。
3. 失败时保留成功成员结果。
4. wait_team 对所有终态返回 summary。

验收：

- Team 无论何种终态都返回结构化 summary；
- 单成员失败不会抹除其他成员结果；
- 可针对失败任务重试，而不是重跑整个 Team。

### P0.6 构建来源可观察

runtime status 增加：

```json
{
  "service": "ai-agent-runtime",
  "backend": {
    "version": "...",
    "git_commit": "...",
    "git_dirty": false,
    "build_time": "..."
  },
  "frontend": {
    "asset_manifest_hash": "...",
    "build_time": "...",
    "entry_asset": "..."
  }
}
```

验收：

- 可仅通过 HTTP 判断 8101 当前运行的二进制和前端来源；
- 打包脚本输出的 manifest hash 与运行时接口一致。

## 10. P1：Agent 智能质量

### P1.1 Capability-scoped 动态工具面

- 根据角色、任务和路径生成最小工具集合；
- 把只读、写入、Shell、网络和 Agent 管理能力分开；
- 将 capability 直接用于 Approval policy。

### P1.2 结构化 Fact Ledger

- 对用户约束、工作区事实、决定、测试结果和未完成工作建模；
- 每个事实关联来源事件和 evidence；
- 支持失效和 supersede，不直接覆盖历史。

### P1.3 Compact 后一致性修复

- Compact 完成后对 Goal/Todo/Run/Job 做 reconciliation；
- 发现摘要与持久状态冲突时生成 correction；
- 建立 context drift 指标。

### P1.4 标准 Agent Result Contract

- Child、Team Member、Lead 使用同一结果骨架；
- findings、changes、artifacts、errors、remaining_work 分开；
- UI 和 Parent Agent 不再解析任意自然语言来判断完成状态。

### P1.5 证据关联的决策与回答

- 关键结论必须关联文件、命令输出、测试结果或运行时接口；
- Context 中只注入摘要和相关证据；
- 支持从最终回答回溯到 execution event。

## 11. P2：评估与发布门禁

### P2.1 Tool Reliability Evals

覆盖：

- 显式 timeout 与上层 deadline 冲突；
- Tool 超时后重试；
- 大输出截断与 artifact 保留；
- Background Job 跨 turn 查询；
- runtime 重启后的 orphan reconcile；
- 写工具幂等保护。

### P2.2 长会话与 Compact Evals

构造 50、100、200 回合任务，验证：

- 用户约束保留率；
- Goal/Todo 一致性；
- 已完成任务不会重复执行；
- 旧 Session Todo 不泄露；
- 执行中的 Job/Agent 引用不丢失；
- Compact 前后关键事实差异。

### P2.3 Child Agent / Team 故障恢复 Evals

注入：

- Approval 延迟；
- 单成员超时；
- Lead Agent 失败；
- runtime 重启；
- Mailbox 重复投递；
- 任务依赖失败；
- Parent turn 提前结束。

验证是否能返回部分结果并恢复。

### P2.4 嵌入式 runtime-server 浏览器 E2E

发布门禁必须从最终产物启动，而不是只测试 Vite dev server：

1. 安装锁定的前端依赖；
2. 构建 production assets；
3. 将 assets 嵌入 Go；
4. 构建 runtime-server；
5. 在随机空闲端口启动最终二进制；
6. 使用真实浏览器访问 `/workspace/chats/new`；
7. 断言首页内容和关键组件可见；
8. 断言 console 无 `error` 和 `ReferenceError`；
9. 断言动态 chunks 全部返回 200；
10. 验证 Prism/代码块、路由刷新和 API 请求；
11. 保存截图、console、network trace；
12. 校验运行时 build info 与当前产物一致。

### P2.5 发布门禁

发布必须同时通过：

- Go 单元/集成测试；
- 前端单元测试；
- production frontend build；
- Go embed 构建；
- embedded-binary browser E2E；
- build provenance 校验；
- `git diff --check`；
- 关键 Agent reliability eval。

## 12. 数据模型与状态机建议

### 12.1 通用 Execution Node

可先建立通用字段，再由不同实体扩展：

```text
execution_nodes
  id
  node_type
  parent_id
  root_id
  workspace_id
  session_id
  goal_id
  turn_id
  state
  state_reason
  capability_scope_ref
  deadline_at
  timeout_source
  cancel_source
  attempt
  idempotency_key
  result_ref
  checkpoint_ref
  created_at
  started_at
  finished_at
  updated_at
```

`node_type` 可包含：

```text
agent_run
tool_invocation
background_job
approval
team_run
team_task
```

### 12.2 通用状态

建议统一为：

```text
created
queued
awaiting_approval
approved
running
streaming
blocked
succeeded
failed
partially_completed
timed_out
canceled
orphaned
```

实体不支持的状态可不使用，但不得另造语义重复状态，例如 `done`、`complete`、`finished` 同时存在。

### 12.3 允许的核心迁移

```text
created -> queued
queued -> awaiting_approval | running | canceled
awaiting_approval -> approved | blocked | canceled
approved -> queued | running
running -> streaming | blocked | succeeded | failed | timed_out | canceled | orphaned
streaming -> running | blocked | succeeded | failed | timed_out | canceled | orphaned
blocked -> queued | running | failed | canceled
orphaned -> queued | failed | canceled
```

`partially_completed` 主要用于复合执行实体 Team，也可用于已经产生有效 artifact 但未完成全部目标的 Agent Run。

### 12.4 Approval 数据模型

```text
approval_requests
  id
  execution_node_id
  requester_agent_run_id
  capability
  tool_name
  arguments_digest
  risk_level
  state
  requested_at
  expires_at
  resolved_at
  resolver
  decision
  patched_arguments_ref
  reason
```

### 12.5 Checkpoint 数据模型

```text
execution_checkpoints
  id
  execution_node_id
  sequence
  summary
  completed_todo_refs
  pending_todo_refs
  fact_snapshot_ref
  artifact_refs
  created_at
```

## 13. 可观测性与诊断

### 13.1 统一 trace/correlation

一次用户请求应可按下列链路追踪：

```text
request_id
  -> session_id
  -> turn_id
  -> agent_run_id
  -> tool_invocation_id / child_run_id / team_run_id
  -> artifact_id
```

### 13.2 建议指标

可靠性：

- `tool_invocation_success_rate`
- `tool_timeout_rate_by_source`
- `background_job_recovery_rate`
- `orphan_execution_count`
- `approval_wait_duration`
- `child_agent_recovery_rate`
- `team_partial_completion_rate`
- `team_summary_generation_rate`

质量：

- `goal_todo_consistency_rate`
- `context_drift_rate`
- `duplicate_action_rate`
- `evidence_coverage_rate`
- `task_success_rate`
- `tokens_per_successful_task`
- `tool_calls_per_successful_task`

发布：

- `embedded_e2e_pass_rate`
- `frontend_console_error_count`
- `build_provenance_mismatch_count`

### 13.3 诊断接口

建议 `/api/runtime` 或 debug 接口提供：

- build/frontend provenance；
- 当前持久存储和 projection mode；
- active/orphan Tool、Job、Agent、Team 数量；
- approval backlog；
- event consumer lag；
- 最近 reconcile 时间和结果；
- Goal/Todo consistency 状态。

敏感参数和完整 prompt 不应直接暴露，只返回 digest、状态和受控摘要。

## 14. 验收指标

### 14.1 P0 验收指标

| 指标 | 目标 |
| --- | --- |
| 显式 timeout 正确生效率 | 100% |
| timeout 来源可解释率 | 100% |
| Background Job 跨 turn 可查询率 | 100% |
| runtime 重启后 Job 可恢复或可解释率 | >= 99% |
| Goal/Todo scope 一致率 | 100% |
| Approval 请求可恢复率 | >= 99% |
| Team 任意终态 summary 生成率 | 100% |
| Team 部分结果保留率 | 100% |
| 运行时 build provenance 可识别率 | 100% |

### 14.2 P1 验收指标

| 指标 | 目标 |
| --- | --- |
| Compact 后关键约束保留率 | >= 99% |
| 跨 Goal Todo 泄露率 | 0 |
| 标准 Agent Result Contract 覆盖率 | 100% |
| 关键结论 evidence 覆盖率 | >= 95% |
| 只读 Agent 无意义审批降低比例 | >= 80% |
| 重复工具动作率 | 相比基线降低 >= 50% |

### 14.3 P2 验收指标

| 指标 | 目标 |
| --- | --- |
| Embedded browser E2E 发布通过率 | 100% 才允许发布 |
| 关键页面 console error | 0 |
| 长会话 eval Goal 漂移 | 0 个阻断级问题 |
| 故障注入后部分结果保留率 | 100% |
| 可恢复场景自动恢复率 | >= 95% |

## 15. 推荐实施顺序

### 第一阶段：建立事实基线

1. 为现有 Tool、Job、Approval、Child、Team 状态画出当前状态机。
2. 搜索全部默认 timeout 和 `context.WithTimeout` 来源。
3. 记录 Goal/Todo 当前 owner key 和存储位置。
4. 建立本次会话暴露问题的回归测试。
5. 为 runtime-server 增加 build provenance，先解决“运行的是谁”这一诊断问题。

### 第二阶段：持久执行最小闭环

1. 引入通用 execution node/event envelope，但先只接 Tool Invocation 和 Background Job。
2. 打通跨 turn 查询、artifact 输出和启动 reconcile。
3. 收敛 timeout/cancel 错误码。
4. 确保 CLI/TUI 读取持久 projection。

### 第三阶段：Goal/Context 收敛

1. Goal/Todo 同域绑定。
2. 引入 Fact Ledger 最小模型。
3. Compact 后执行 reconciliation。
4. 增加长会话 eval。

### 第四阶段：Child/Team 收敛

1. Child Agent Run 迁移到统一 execution 状态。
2. Approval Relay 持久化。
3. 实施标准 Agent Result Contract。
4. Team 支持 `partially_completed` 和系统 fallback summary。
5. 加入 checkpoint、恢复和故障注入测试。

### 第五阶段：生产发布门禁

1. 建立最终 Go 二进制浏览器 E2E。
2. 校验 frontend manifest hash 和 runtime build info。
3. 将 Agent reliability eval 纳入 CI。
4. 设置质量阈值并阻止不满足条件的发布。

## 16. 实施原则与非目标

### 实施原则

1. **先统一语义，再增加功能。** 优先消除 timeout、状态和结果契约的不一致。
2. **持久状态优先于上下文文本。** Context 是视图，不是执行权威。
3. **终态必须可解释。** 所有失败都应回答“谁取消、哪里超时、能否重试、保留了什么”。
4. **部分结果也是结果。** Team 或 Agent 失败不得抹除已完成工作。
5. **默认最小权限。** 能力声明既控制工具暴露，也控制审批策略。
6. **最终产物验证。** 不以单元测试或开发服务器替代嵌入式二进制 E2E。
7. **渐进迁移。** 可先通过 projection/adapter 兼容旧接口，避免一次性重写所有模块。

### 非目标

- P0 不要求立即引入外部分布式队列或远程数据库。
- 不要求把所有历史 Chat 消息改造成 Event Sourcing。
- 不要求一次性删除现有 Mailbox、AgentControl 或 Team 数据模型。
- 不把“增加模型上下文窗口”作为上下文一致性的替代方案。
- 不把“自动批准所有工具”作为解决 Approval 卡住的方案。
- 不把 Lead Agent 的自然语言总结作为 Team 唯一结果来源。

## 17. 预期结果

完成本方案后，aicli 应具备以下用户可感知能力：

- 工具调用的 timeout 与取消原因稳定、明确、可重试；
- 后台任务不会因为用户发送下一条消息而失联；
- 长会话压缩后仍能记住当前目标、约束、已完成工作和运行中的任务；
- 只读 Reviewer 不会无意义地卡在写权限审批；
- Child Agent 等待审批或 runtime 短暂重启后能够继续；
- Team 即使部分失败，也会返回已完成成员的发现和下一步建议；
- 用户能通过运行时接口确认 8101 上实际运行的后端与前端版本；
- CI 能在发布前发现类似 `Prism is not defined` 的 production chunk 问题；
- 系统的成功率、恢复率、上下文漂移和 token 效率可以被持续测量。

最终，项目将从“功能丰富的 Agent CLI”进一步演进为“具有统一执行语义、持久恢复能力和生产验证体系的 Agent Runtime”。

## 18. 实施进展：统一 aicli Runtime 运行核心

截至 2026-07-13，aicli 的 Chat、Exec、Resume 正常执行链已统一到 `SessionActor` Runtime 核心：

```text
local aicli
  core      = session_actor
  transport = in_process

runtime-server
  core      = session_actor
  transport = http
```

统一核心合约版本为 `1`，固定声明以下语义：

- lifecycle：`durable_session_actor`；
- state authority：`runtime_state_store`；
- event protocol：`session_runtime_events`；
- approval protocol：`runtime_command_relay`；
- background durable：`true`。

本轮收口包括：

1. aicli Executor 必须提供 Runtime Descriptor 和工具可用性能力，并在进入正常发送链前通过统一核心合约校验。
2. 本地执行和 runtime-server 执行只允许传输层不同，不再通过具体 Executor 类型推断事件协议或工具面。
3. legacy shared tool loop 只保留底层兼容测试入口，不能进入 Chat、Exec、Resume 的正常执行链。
4. aicli 不再从 `/api/runtime/.../runtime/commands` 降级到 `/api/agent/chat`，避免远端执行语义退回旧核心。
5. runtime-server 会话状态刷新失败时直接返回错误，不再由 CLI 拼接本地历史伪造远端权威状态。
6. runtime-server `/healthz` 和 `/api/runtime` 诊断结果暴露 `execution_core`；aicli 建立 HTTP 执行器前必须验证该合约。
7. aicli JSON 输出暴露 `runtime_core`、`runtime_contract_version` 和 `runtime_transport`，便于脚本和诊断工具识别实际执行路径。

当前验收结果：

- Go 全仓测试通过；
- Go Vet 全仓通过；
- Reliability Evals：`18/18`；
- runtime-server Browser E2E：`12/12`；
- 前端测试：`54` 个测试文件、`208` 个测试通过；
- 完整发布门禁：`10/10`。

### 18.1 Subagent / Team 模型路由核心统一

截至 2026-07-15，aicli、runtime-server 和 Web 配置页已经共用同一套 Agent 模型路由语义，避免 CLI 诊断、服务端实际执行和 Web 预览分别维护 Provider、模型与作用域解析逻辑。

统一后的核心职责如下：

1. `internal/modelrouting.ConfigCatalog` 从配置文档构建 Provider、默认模型、模型别名、模型能力和 Reasoning 能力目录。
2. `internal/modelrouting.ResolveConfigScope` 统一解析 `subagent`、`team_independent`、`subagent_inherited` 三种策略来源。
3. `internal/modelrouting.ResolveParentDefaults` 统一解析父 Agent 的 Provider、模型和 Reasoning 默认值。
4. aicli `doctor subagent-route`、runtime-server 路由试算和 Web 草稿预览使用相同的 `modelrouting.Resolver`，共享 fallback、override、难度分档和能力校验规则。
5. Team 未启用独立路由时继承 Subagent 四档策略；启用后使用独立的 `easy / normal / hard / expert` Provider、模型和 Reasoning 配置。

模型目录还收紧了别名解析的确定性：

- Provider 名称优先于模型别名；
- Provider 默认模型和显式模型映射使用同一套能力检查；
- 多个 Provider 共享同一模型别名时不再依赖 Go map 的随机迭代顺序；
- 歧义别名会明确解析失败，并进入统一 fallback 或错误返回路径，避免同一任务在不同进程中随机选中不同 Provider。

### 18.2 Web 有效路由试算与配置健康检查

Web 配置页已经支持直接编辑 Subagent 与 Team 的四档路由，并通过以下接口对未保存草稿执行真实路由试算：

```text
POST /api/runtime/config/document/agent-route-preview
```

试算不会写入磁盘，返回以下有效决策信息：

- 策略作用域和来源；
- 路由是否启用；
- 父 Agent Provider、模型和 Reasoning；
- 最终 Provider、模型、Reasoning 和难度；
- 难度来源、路由来源和 fallback 原因；
- runtime warning code。

用户可以在保存前验证任务角色、任务目标、难度、Provider/模型 override、token budget、timeout 和只读约束。配置页同时显示 Provider 缺失或禁用、默认模型缺失、模型不兼容、Reasoning 不受支持、Team 继承关系等健康状态，降低“配置可以保存但运行时不可用”的概率。

试算请求使用前端请求代次进行一致性保护：当用户在请求完成前修改草稿、作用域、难度、角色或目标时，旧请求会立即失效，其返回结果和错误都不能覆盖新输入对应的界面状态。

本阶段验收结果：

- `internal/agentconfig`、`internal/modelrouting`、`internal/runtimeserver`、`internal/api/skills`、`internal/team`、`cmd/aicli/commands` 测试通过；
- 上述核心包 `go vet` 通过；
- 前端 ESLint 和 TypeScript 构建检查通过；
- 前端 Vitest：`54` 个测试文件、`208` 个测试通过；
- 前端生产构建通过，共转换 `2181` 个模块；
- 桌面端与移动端真实浏览器验证通过，控制台错误和警告均为 `0`。

为避免 Windows 上 Vitest 默认并发导致 pagefile 和 `VirtualAlloc` 枯竭，测试配置默认将 Windows worker 数限制为 `2`，并允许通过 `VITEST_MAX_WORKERS` 覆盖。该限制只影响测试并发，不改变生产构建和运行时行为。
