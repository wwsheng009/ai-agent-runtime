# `spawn_subagents` 异步批处理与 Parent Supervisor 方案

> 状态：方案分析（未实施）  
> 日期：2026-08-19  
> 适用范围：`backend/internal/agent`、SessionActor/AICLI、supervision、runtime event、AICLI UI  
> 目标：让 Parent Agent 在子 Agent 批处理期间不再被同步 barrier 永久占用，并通过可恢复的生命周期事件、durable wake 和单飞 Parent turn 实现主动监督

## 1. 摘要

当前 `spawn_subagents` 的语义是同步工具调用：Parent ReAct loop 在 `loop.go:2078` 进入 `SubagentScheduler.RunChildren` 后，必须等 reader wave、writer 任务和全部 child 返回，才能产生 `tool.completed` 并继续下一轮 LLM。reader 虽然可以并发，但 Parent 不能同时推理、读取 child 状态或发起下一步调度。

这解释了 AICLI 中长期出现的：

```text
Running spawn_subagents agents=[2]
```

但它也暴露出当前架构的边界：

- `subagent.*` 和 `tool.progress` 主要是观测事件，不是 Parent 控制流；
- direct `SubagentScheduler` 的 child terminal event 没有稳定、统一地进入 supervision lifecycle projection；
- supervision wake 只在 Parent 可运行的状态转换点触发，不是常驻 patrol goroutine；
- wake delivery 在本地 host 中异步启动 `SubmitPrompt`，却立即 Resolve wake，存在投递尚未入队即丢 wake 的竞态；
- cancellation 可以向 child 传播，但 semaphore、provider、MCP/tool 和 `wg.Wait()` 仍可能使取消延迟；
- UI 的 Running 是等待 `tool.completed` 的 transient active cell，不能表达“后台批次已接受、正在运行、部分完成、失败待处理”等状态。

本方案建议把 `spawn_subagents` 演进为**可持久化的异步批处理任务**，并增加一个与 Team orchestrator 分离、但复用 supervision/wake 原则的 Parent Supervisor：

```text
Parent turn
   |
   | spawn_subagents(mode=background)
   v
BatchStore: queued/running
   |
   +-- child task 1
   +-- child task 2
   |
   +-- lifecycle projection -> durable notification/wake
                              |
                              v
                   Parent runnable transition
                              |
                              v
                  one supervised Parent turn
```

Parent 不再等待整个 batch 才能继续；child 结果通过结构化摘要、生命周期 digest 和下一次 Parent turn 注入。现有同步行为保留为兼容模式，逐步迁移到后台模式。

## 2. 现状证据与问题边界

### 2.1 同步 barrier

当前调用链：

- `backend/internal/agent/loop.go:849-877`：Parent ReAct loop 只有 `act` 返回后才继续；
- `backend/internal/agent/loop.go:2021-2128`：`spawn_subagents` 特殊分支同步调用 scheduler；
- `backend/internal/agent/loop.go:2073-2083`：先发 `subagent.batch.started`，再调用 `RunChildren`；
- `backend/internal/agent/loop.go:2084-2127`：只有 scheduler 返回后才产生 batch completed、tool completed 和 tool reduced；
- `backend/internal/agent/scheduler.go:140-227`：`RunChildren` 依次执行 reader wave 和 writer；
- `backend/internal/agent/scheduler.go:672-707`：reader wave 使用 `wg.Wait()` 作为 barrier。

因此，child 并发不等于 Parent 可并发巡查。

### 2.2 生命周期与 supervision 缺口

Direct scheduler 在以下位置发布 `subagent.started` / `subagent.completed`：

- `backend/internal/agent/scheduler.go:329-340`；
- `backend/internal/agent/scheduler.go:365-376`；
- `backend/internal/agent/scheduler.go:417-429`。

而当前 AICLI local actor bridge 主要监听 child 的：

- `runtimechat.EventSessionEnd`；
- `runtimechat.EventSessionInterrupted`。

对应代码为 `backend/cmd/aicli/commands/chat_actor_registry.go:1011-1017`，之后才会在 `1049`、`1080-1106` 附近调用完成投影和 wake。Direct scheduler 的 `subagent.completed` 本身不应被假定为已经进入 `ProjectAgentCompletion`。

Supervision 的自动 wake 入口在：

- `backend/internal/supervision/wake_consumer.go:14-19`：明确没有 resident polling goroutine；
- `wake_consumer.go:36-76`：只在 runnable transition 处 drain、deliver、resolve；
- `backend/cmd/aicli/commands/chat_actor_host.go:120-145`：Parent busy 时保留 wake，但 Deliver 使用后台 goroutine，立即返回成功。

### 2.3 取消和超时缺口

- `backend/internal/agent/scheduler.go:300-305`：child context 可以继承 Parent context，并可叠加 route timeout；
- `scheduler.go:677-689`：reader 获取 semaphore 使用不可取消的 `sem <- struct{}{}`；
- `scheduler.go:697`：wave 无条件 `wg.Wait()`；
- `backend/internal/agent/child_factory.go:31-33`：`Build` 显式丢弃 context；
- provider、MCP、外部 tool 是否及时响应 context 取决于下游实现。

### 2.4 UI 语义缺口

- `backend/cmd/aicli/commands/chat_runtime_events.go:3066-3080`：`tool.requested` 只更新 ActiveBand 的 Running 状态；
- `chat_runtime_events.go:3082-3093`：稳定身份的 `tool.progress` 只更新 mutable state，不写 durable timeline；
- `chat_runtime_events.go:5109-5118`：batch started、child started 不产生普通历史行，终态才有机会落入 timeline。

因此 Running 只表达“Parent tool call 尚未收到匹配的 final event”，不能表达异步批处理的真实状态。

## 3. 目标与非目标

### 3.1 目标

1. Parent 发起后台批次后立即恢复自己的 ReAct turn，不等待全部 child。
2. 每个 batch、task、child session 都拥有稳定 ID、状态机、版本和可恢复记录。
3. child 的 queued/running/progress/completed/failed/canceled/timeout 等生命周期能统一投影到 Parent supervision inbox。
4. Parent 只允许单飞：Parent 正在运行、等待 approval/input、compact 或 stopping 时，不启动并发的自动 turn。
5. Parent 空闲且存在未处理的 critical lifecycle 时，自动启动一个包含 digest 的 Parent turn。
6. wake 投递只有在 Parent command 真正 admission 后才 Resolve；投递失败、digest 构建失败、进程重启都不能静默丢失 wake。
7. Esc/Ctrl+C 能区分“取消当前 Parent turn”和“取消关联后台 batch”，并有明确的最终状态。
8. UI 能显示 batch handle、任务完成数、最近事件、耗时和终态，不再把后台任务伪装成无限 Running 的同步 tool call。
9. 保留现有同步模式、工具结果和事件兼容性，支持灰度、回滚和历史 session 恢复。

### 3.2 非目标

- 不把 Parent 变成多个并发 LLM turn；Parent 仍然严格单飞。
- 不让 EventBus 直接承载可靠任务队列；可靠状态必须落 durable store。
- 不把完整 prompt、reasoning、工具参数、工具结果或 provider body 写入普通生命周期事件。
- 不把 `spawn_subagents` 和 `spawn_team` 合并为同一个调度器。Team orchestrator 仍负责 Team 级 task/lease/mailbox；本方案只定义两者共享的生命周期、取消和 wake 约束。
- 不承诺强制杀死不响应 context 的第三方 goroutine；本方案通过 watchdog、状态标记、资源隔离和进程级恢复处理这类异常。
- 不在本阶段实现跨进程多 Parent 实例的任意抢占；需要沿用现有 lease/fencing 机制。

## 4. 目标架构

### 4.1 三个平面

| 平面 | 职责 | 可靠性 |
| --- | --- | --- |
| 执行平面 | 创建 child、并发/依赖调度、provider/tool 调用、结果归档 | 可取消、可重试、受 batch/task deadline 约束 |
| 控制平面 | BatchStore、task 状态、取消请求、owner lease、watchdog、幂等 | durable、可恢复、单调状态转换 |
| 观测/监督平面 | runtime event、lifecycle projection、digest、wake、Parent auto-turn | 事件可丢时可从 store catch-up；wake 不丢 |

EventBus 只作为低延迟通知；任何决定“是否完成”“是否需要 Parent 处理”“是否已经投递”的语义，都必须基于 durable state 或 durable notification。

### 4.2 新增 Batch Coordinator

建议在 `backend/internal/agent` 增加 host-neutral 的 `SubagentBatchCoordinator`（名称可在实现阶段调整），与当前 `SubagentScheduler` 分层：

```go
type SubagentBatchCoordinator interface {
    Start(ctx context.Context, req BatchStartRequest) (BatchHandle, error)
    Get(ctx context.Context, batchID string) (SubagentBatch, error)
    Cancel(ctx context.Context, batchID string, reason string) error
    Resume(ctx context.Context, batchID string) error
}
```

Coordinator 负责：

1. 校验任务、路由、depth、read-only、依赖和预算；
2. 创建 durable batch/task 记录并分配 `batch_id`；
3. 启动受 owner/lease 保护的 worker；
4. 让现有 reader/writer 算法作为执行内核复用，但不再把 batch 生命周期绑定在 Parent `act` 调用栈上；
5. 在每个状态转换点产生结构化事件和 lifecycle projection；
6. 在重启后扫描 `queued/running/cancel_requested` 记录并恢复、超时或标记 orphan；
7. 将结果写入受预算限制的摘要/产物引用，而不是把完整结果塞进 Parent tool result。

`SubagentScheduler.RunChildren` 可以在第一阶段保留为 `mode=wait` 的兼容 adapter，后台模式则调用 Coordinator 后立即返回 handle。

### 4.3 Batch 与 Task 数据模型

建议至少持久化以下字段：

```text
SubagentBatch
- batch_id
- root_scope_id
- parent_session_id
- parent_turn_id
- parent_tool_call_id
- trace_id
- execution_mode: wait | background
- status: queued | running | partially_completed | completed | failed | canceled | timed_out | orphaned
- task_count / queued_count / running_count / completed_count / failed_count / canceled_count
- created_at / started_at / updated_at / finished_at
- batch_deadline
- cancel_requested_at / cancel_reason
- owner_id / fencing_token / heartbeat_at
- version
- result_summary_ref / error_class

SubagentTaskRecord
- task_id
- batch_id
- parent_task_id / dependency_ids
- child_session_id
- role / difficulty / read_only
- status: pending | ready | running | succeeded | failed | canceled | timed_out | skipped
- attempt
- task_deadline
- started_at / updated_at / finished_at
- last_progress_at
- result_summary_ref / artifact_ref
- error_class / error_code
- version
```

状态转换必须使用版本/CAS 或 owner fencing，防止 worker、取消请求、恢复器和 late event 互相覆盖。

### 4.4 执行模式

在不破坏既有 tool schema 的前提下，为 `spawn_subagents` 增加可选的 `execution_mode`：

```text
wait       兼容当前同步语义，返回完整 reports 或 batch-level error
background 新目标语义，立即返回 batch handle，reports 通过后续 digest/查询提供
```

后台模式的工具结果示例：

```json
{
  "ok": true,
  "execution_mode": "background",
  "batch_id": "batch_01J...",
  "status": "queued",
  "task_count": 2,
  "parent_action": "continue_parent_turn; lifecycle_updates_will_be_delivered_by_supervision"
}
```

迁移期间未携带 `execution_mode` 的旧请求默认保持 `wait`，避免已有 prompt 依赖完整 reports 的模型行为被突然改变。完成灰度后，再由系统 prompt/route policy 对长任务默认选择 `background`。

## 5. Parent Supervisor 与生命周期投影

### 5.1 统一生命周期事件

建议定义版本化事件名和稳定 payload：

```text
subagent.batch.created
subagent.batch.started
subagent.batch.progress
subagent.batch.completed
subagent.batch.failed
subagent.batch.canceled
subagent.batch.timed_out

subagent.task.queued
subagent.task.started
subagent.task.progress
subagent.task.completed
subagent.task.failed
subagent.task.canceled
subagent.task.timed_out
```

所有事件至少包含：

```text
schema_version
batch_id
task_id
subagent_id
parent_session_id
parent_turn_id
parent_tool_call_id
root_scope_id
trace_id
status
version
timestamp
```

进度事件只允许低敏摘要，例如 `completed_count`、`total_count`、`percent`、`last_phase`、`elapsed_ms`、`last_progress_at`。不得默认携带 prompt、完整 tool args、完整输出或原始 HTTP body。

### 5.2 Terminal event 到 supervision 的单一入口

所有 child terminal path，包括：

- 正常 child loop 返回；
- provider/tool 错误；
- task timeout；
- Parent interrupt；
- worker crash/restart recovery；
- owner lease 失效；

都必须经过同一个 `ProjectSubagentTaskLifecycle`（名称可调整）适配器，再调用现有 supervision projection。禁止依靠某个特定 UI bridge 是否收到 `EventSessionEnd` 来决定是否创建 Parent notification。

建议规则：

- 成功完成：写 resolved/info lifecycle record，不自动唤醒 Parent；
- failed/timeout/blocked：写 unresolved/critical notification，并 ScheduleWake；
- canceled/interrupted：写 warning 或 canceled notification，是否 wake 由取消来源和策略决定；
- batch 从 partial 到 terminal：单独写 batch summary，避免 Parent 只看到多个重复 child error。

此适配器应做到 best-effort 不影响 child 结果，但必须记录 projection failure、重试信息和可恢复 outbox 状态。

### 5.3 Parent wake 语义

保持现有 `WakeConsumer` 的单飞原则，但把触发入口扩展为可靠的 lifecycle projection：

1. child 状态先 durable commit；
2. projection 写入 notification 和 deduplicated wake；
3. 若 Parent busy，wake 保留；
4. 若 Parent runnable，claim wake 并构建 digest；
5. Deliver 必须同步返回“Parent command 已 admission”的结果；
6. admission 成功后才 Resolve wake；
7. digest 构建、claim、delivery 任一步失败，都要释放 claim 或写入可恢复 retry 状态；
8. Parent turn 启动时以 `batch_id`/cursor 做幂等，重复 wake 不产生重复处理。

`chat_actor_host.go` 当前的：

```go
go func() { SubmitPrompt(...) }()
return nil
```

应改为可观测、可确认的 admission API，不能用“后台 goroutine 已启动”作为 delivery success。

### 5.4 Parent 自动 turn 的输入

Auto wake prompt 不应把所有 child 输出直接拼进 prompt。建议由 Parent turn preflight 注入：

```text
- batch_id
- changed task ids
- terminal status counts
- critical error classes
- lifecycle digest cursor
- result_summary_ref / artifact_ref
- recommended next actions
```

摘要必须有字符/字节预算、脱敏和 hash；Parent 需要完整结果时，再通过受控工具按 `batch_id/task_id` 读取。

## 6. 调度、超时、取消与恢复

### 6.1 读取 wave 与 writer 的兼容改造

后台 coordinator 可以复用现有依赖排序，但必须把执行和 Parent 调用栈解耦：

- reader admission 使用 `select { case sem <- token: case <-ctx.Done(): }`；
- 每个 task 有独立 context、deadline 和 cancel function；
- batch context 取消时，queued/ready task 直接标记 canceled；
- running task 进入 `cancel_requested`，等待有限 grace period；
- writer 仍遵守依赖顺序，但不应阻塞整个 Parent turn；
- `wg.Wait()` 由 coordinator worker 使用完成 channel 观察，必须有 watchdog 和 batch deadline；
- task worker 退出后再关闭 child Agent，`Close` 失败进入可观测 error，不无限阻塞 terminal transition。

对于无法响应 context 的下游调用，不假装已经取消：先持久化 `cancel_requested`，超时后标记 `orphaned`/`timed_out`，由恢复器和 operator action 处理。

### 6.2 Parent Esc/Ctrl+C 语义

区分三个动作：

| 动作 | 默认影响 |
| --- | --- |
| Interrupt current turn | 取消当前 Parent LLM/tool turn；后台 batch 继续运行 |
| Cancel associated batch | 取消当前 turn 关联、且仍未完成的 batch；需要明确 batch identity |
| Stop process/session | 取消 Parent 与其拥有的 batch，释放 lease，重启后恢复未决状态 |

AICLI 当前 `InterruptPreservePendingInput` 语义可继续保留；若用户要取消后台 batch，应通过明确的 batch action，而不是把所有子 session 按 user/session 模糊匹配后停止。

取消流程：

```text
user interrupt
  -> durable batch cancel_requested
  -> cancel coordinator context
  -> mark pending tasks canceled
  -> signal running child contexts
  -> wait bounded grace period
  -> terminal batch event
  -> UI finalize active/background cell
```

取消事件必须幂等；late success/failure 不能把已经 canceled 的 task 无条件改回 succeeded，除非通过新的 attempt/version 明确记录。

### 6.3 Timeout 与 watchdog

至少需要三层 deadline：

1. task route timeout；
2. batch deadline；
3. worker heartbeat/watchdog timeout。

watchdog 检查：

- `heartbeat_at` 是否推进；
- child provider/tool 是否仍有 in-flight request；
- task 是否卡在 admission、dependency、writer patch 或 `Close`；
- owner lease 是否仍有效。

watchdog 只负责状态转移、取消请求和通知，不直接从另一个 goroutine 强杀任意执行栈。

### 6.4 重启恢复

进程重启后：

- `queued/ready` 任务可重新 claim；
- `running` 任务根据 heartbeat/owner lease 判定 resume 或 orphan；
- `cancel_requested` 任务不可被新 worker 当作普通 ready 任务重新执行；
- 已 terminal 的 task 不能重复产生新的 lifecycle notification；
- batch summary、notification、wake 使用 idempotency key 去重；
- 恢复器必须产生 `batch.recovered` 或 `batch.orphaned` 可诊断事件。

## 7. UI 与 runtime event 方案

### 7.1 Tool cell 生命周期

后台模式下，Parent 的原始 tool cell 不能继续保持无限 Running：

```text
tool.requested
  -> accepted/background (短暂)
  -> batch card: queued/running
  -> progress: 1/2, 2/2, failed=1
  -> completed/failed/canceled
```

建议给 batch card 一个稳定 key：

```text
batch:{parent_session_id}:{batch_id}
```

所有 batch/task 事件都通过这个 key 更新同一 mutable cell；终态只写一次 durable timeline。late event 必须依据 `batch_id + version` 丢弃或合并，不能重新创建第二个 Running cell。

### 7.2 推荐显示内容

默认显示：

```text
Subagents batch_01J...  running 1/2  elapsed 00:42
  task-a completed
  task-b running (last update 3s ago)
```

终态显示：

```text
Subagents batch_01J...  completed 2/2
Subagents batch_01J...  failed 1/2: timeout
Subagents batch_01J...  canceled by user
```

不要默认渲染完整 child prompt、reasoning、工具参数或原始错误 body。详情通过显式 inspect 工具或受控 debug 模式查看。

### 7.3 兼容旧事件

第一阶段继续发布现有：

- `subagent.batch.started/completed`；
- `subagent.started/completed`；
- `tool.requested/completed/reduced`。

同时添加 `batch_id`、`execution_mode`、`status_version` 等可选字段。旧客户端忽略新增字段即可。新 UI 优先消费版本化 batch projection，旧 UI 仍看到短暂的 tool result。

## 8. API 与工具接口

### 8.1 Tool schema

建议为 `spawn_subagents` 增加：

```text
execution_mode: "wait" | "background"
wait_timeout_sec: optional integer
batch_idempotency_key: optional string
```

`agents` 任务结构保留现有字段，并允许每个 task 提供：

```text
timeout_sec
completion_requirement
```

后台返回 handle；同步模式继续返回 reports。非法 mode、重复 idempotency key、超过 batch budget 必须在创建前拒绝。

### 8.2 查询接口

若需要 runtime-server/API 侧查询，建议新增独立、只读且版本化的接口，不复用原始 `/events` payload：

```text
GET /api/runtime/subagent-batches/{batch_id}
GET /api/runtime/subagent-batches/{batch_id}/tasks
GET /api/runtime/subagent-batches/{batch_id}/events?after_seq=...
```

取消属于控制面，沿用现有鉴权、审计和 action contract；不要让普通观测 token 直接获得取消权限。

### 8.3 Agent 查询工具

Parent 可通过受限工具读取：

```text
get_subagent_batch(batch_id)
list_subagent_tasks(batch_id)
read_subagent_result(batch_id, task_id, summary_only=true)
```

工具默认只返回低敏摘要和状态。完整 artifact 读取必须经过现有 capability/read-only/policy 边界。

## 9. 与 `spawn_team` 的关系

`spawn_team` 使用 `internal/team/orchestrator.go` 的 team lease、ticker、mailbox/task wake 和 task lifecycle；它不是 `SubagentScheduler.RunChildren` 的别名。

本方案不把两者强行合并，而是共享以下协议：

- parent/root scope 关联；
- task/child terminal status vocabulary；
- owner lease/fencing；
- lifecycle projection；
- deduplicated durable wake；
- Parent/lead 单飞规则；
- cancel、timeout、recovery 和 audit 字段。

这样可以避免再次出现：Team path 有 orchestrator，但 direct `spawn_subagents` 仍然是同步 barrier 的语义分裂。

## 10. 分阶段实施计划

### Phase 0：契约和可观测性

- 定义 batch/task 状态机、事件 schema、版本和幂等键；
- 为现有同步路径补充 `batch_id`、task counts、elapsed 和 terminal status；
- 记录 `RunChildren` barrier duration、child duration、cancel latency、wake claim/delivery/resolve latency；
- 增加 direct scheduler completion 到 supervision projection 的集成测试；
- 明确旧 UI、旧 tool result 的兼容行为。

### Phase 1：持久化 Batch Coordinator

- 新增 BatchStore/SQLite schema 与迁移；
- 将 reader/writer 执行内核抽离到 coordinator worker；
- 实现 `execution_mode=background`，先由 feature flag 控制；
- 保留 `execution_mode=wait` adapter；
- 加入 owner lease、task timeout、batch deadline、watchdog 和重启恢复。

### Phase 2：统一生命周期与 Parent wake

- 所有 child terminal path 接入单一 lifecycle projector；
- 失败、timeout、blocked 事件写 supervision notification/wake；
- 修复 digest failure、delivery failure 的 claim release；
- 把 wake delivery 改为同步 admission confirmation；
- 增加 Parent single-flight CAS/fence，验证 busy Parent 不会并发 auto-turn。

### Phase 3：Parent turn 注入与查询工具

- preflight 注入 batch digest；
- 增加 batch/task/result summary 查询工具；
- 将完整结果从 tool result 中移至受预算控制的摘要/artifact reference；
- 让 Parent 能根据 partial failure 继续规划，而不是等待原始 batch 报告。

### Phase 4：AICLI UI 与 API

- 新增 stable batch card 和进度投影；
- 处理 late event、duplicate event、restart recovery、cancel finalization；
- 增加受鉴权的 batch 查询/控制接口；
- 保持 `/api/runtime/events` 和旧 timeline 兼容。

### Phase 5：默认策略迁移

- 对长耗时、多个独立 reader、存在 `completion_requirement` 的任务默认使用 background；
- 通过 metrics 和错误率确认后，再扩大默认范围；
- 同步模式继续保留为显式兼容选项；
- 逐步收紧“无 timeout 的长任务”策略，避免再次产生不可诊断的无限 Running。

## 11. 测试计划

### 11.1 Unit

- 状态机单调转换、CAS 冲突和 idempotency；
- reader semaphore 在 context cancel 下及时退出；
- task/batch timeout、cancel、retry 和 late result；
- dependency deadlock、writer 顺序和 partial result；
- lifecycle projection severity/resolution 规则；
- wake claim、digest failure、delivery failure、resolve 语义；
- UI batch card 的 duplicate/late/out-of-order event 合并。

### 11.2 Integration

1. 两个 child 后台启动后，Parent tool 立即完成并继续下一轮 LLM。
2. 一个 child 失败、另一个继续运行时，Parent 不产生并发 turn，但失败 notification durable。
3. Parent turn 结束后，pending wake 只启动一个 auto-turn。
4. Parent busy 时重复 child failures 只 coalesce 为一个 wake/digest。
5. `SubmitPrompt` admission 失败时 wake 不被 Resolve，后续可重新 delivery。
6. BuildDigest 失败、进程重启、owner lease 失效后 wake/batch 可恢复。
7. Esc 取消 Parent turn 不会误杀未关联的 batch；明确取消 batch 后所有 queued/running task 进入最终取消路径。
8. provider/MCP/tool 忽略 context 时 watchdog 能标记 timeout/orphan 并保持 UI 可解释。
9. direct scheduler 和 Team orchestrator 的 terminal lifecycle 都能生成统一 Parent digest。

### 11.3 E2E 与并发

- AICLI：`accepted -> queued -> running -> partial -> completed/failed/canceled` 全链路；
- session restore/reconnect 后 batch card 和 digest cursor 不重复；
- `go test -race` 覆盖 cancel function、wake delivery、event projection、UI mutable cell；
- 慢客户端、EventBus handler 延迟、SSE/观测流背压不阻塞 batch worker；
- 进程 shutdown/restart 期间验证 lease、worker 和 wake 清理。

## 12. 指标、日志与告警

建议增加：

```text
subagent_batches_started_total
subagent_batches_background_total
subagent_batches_completed_total{status}
subagent_tasks_completed_total{status}
subagent_batch_duration_seconds
subagent_task_duration_seconds
subagent_cancel_latency_seconds
subagent_watchdog_timeout_total
subagent_orphaned_total
subagent_lifecycle_projection_failures_total
supervision_wake_claim_total
supervision_wake_delivery_total{status}
supervision_wake_resolve_total{status}
supervision_wake_pending_age_seconds
parent_auto_turn_total{reason,status}
parent_auto_turn_rejected_busy_total
```

每个 batch 的结构化日志至少带：`batch_id`、`task_id`、`parent_session_id`、`parent_turn_id`、`trace_id`、`status_version`、`owner_id`。原始 prompt、tool args、provider body 和完整 child output 不进入默认日志。

告警条件：

- pending wake 超过阈值；
- batch heartbeat 长时间不推进；
- claim 后长期未 resolve；
- delivery admission 失败率升高；
- batch terminal 但 Parent digest 未产生；
- UI active cell 长期没有对应 durable batch state；
- orphaned worker 超过阈值。

## 13. 安全与权限

- Parent 只能取消自己拥有或被授权的 batch；
- read-only child 不能通过后台模式扩大工具面；
- batch/task 查询继续经过 session ownership、tool policy 和 root scope 校验；
- lifecycle payload 做 allowlist、长度限制和路径/secret 脱敏；
- wake prompt 不携带未经脱敏的 child output；
- control API 记录 operator/user、reason、before/after version 和结果；
- worktree、artifact、patch 的应用/丢弃仍遵循现有 capability 和显式确认规则。

## 14. 验收标准

方案实施完成后，至少满足：

1. `spawn_subagents(mode=background)` 在固定短时间内返回可查询的 `batch_id`，不随最慢 child 时长增长。
2. Parent 能在 batch 运行期间继续自己的下一轮 LLM，但任何时刻最多一个 Parent turn。
3. child terminal failure/timeout 会在 durable store 中产生可查询 notification，并在 Parent runnable 时生成一个且仅一个 auto-turn。
4. Parent busy 时 wake 不启动并发 turn，Parent 结束后能自动 drain；重启、delivery failure、digest failure 不会静默丢 wake。
5. Esc、batch cancel、process shutdown 的状态最终可区分，且不会把未关联的 child/session 误取消。
6. UI 能区分 accepted、queued、running、partial、completed、failed、canceled、timed_out、orphaned，并能处理 late/duplicate event。
7. 同步兼容模式的既有报告、事件和测试继续通过。
8. 具备 batch/task/wake/Parent turn 的 trace、metrics、日志和恢复诊断，能够回答“当前到底是在等待、执行、取消、投递失败还是 UI stale”。

## 15. 风险与回滚

| 风险 | 缓解 |
| --- | --- |
| 后台模式改变模型原本依赖完整 reports 的行为 | `wait` 兼容模式、显式 `execution_mode`、逐步迁移 system prompt |
| auto-turn 风暴 | durable dedup、Parent single-flight、rate limit、batch summary coalescing |
| child 结果丢失或重复 | BatchStore、outbox/idempotency、version/CAS、result reference |
| worker 泄漏 | task/batch deadline、watchdog、owner lease、shutdown recovery |
| UI 与 runtime 状态不一致 | stable batch key、durable snapshot、late-event version fencing |
| supervision store 不可用 | child outcome 不被改写；outbox/retry/告警；自然 Parent turn preflight 兜底 |
| 新旧 Team/Agent 语义继续分裂 | 共享 lifecycle/wake/cancel contract，但保留各自执行器 |

任何阶段都可以通过 feature flag 将新请求回退到 `execution_mode=wait`；已创建的 background batch 不应直接删除，应通过 cancel/recovery 完成状态收敛。

## 16. 最终设计决策

本项目不应通过增加更多 UI heartbeat 或 EventBus 订阅来“伪造” Parent 主动巡查。根本修复是：

```text
同步 tool call
    -> 持久化 background batch/job
    -> child lifecycle projection
    -> durable supervision wake
    -> Parent runnable single-flight turn
    -> digest/result summary 注入
```

也就是说，Parent 的主动监督必须是**异步批处理 + durable lifecycle + 可确认 wake + 单飞 Parent turn**的组合能力，而不是在当前同步 `RunChildren` 调用栈中再塞入一个临时巡查 goroutine。
