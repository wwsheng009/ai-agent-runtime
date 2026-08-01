# Spawn Agent / Team 主动监督、超时与故障恢复优化方案

更新时间：2026-07-31（2026-07-31 完整性审查后修订）

修订记录：

- 2026-07-31：完整性审查修订。补充 fencing token 落地规则、progress 埋点清单、幂等声明机制、审计记录、续跑入口监督、数据保留/GC、索引与性能测试、开放问题章节；修正 `actor.go` lease 引用；将风险等级改为 R0/R1/R2 以消除与实施阶段 P0–P6 的编号歧义。

## 1. 文档定位

本文针对当前项目中 `spawn_agent`、`spawn_team` 及其子 Session、Team Orchestrator、Teammate Task 的生命周期可靠性进行专项优化设计，重点回答并解决以下问题：

1. 父 Agent 发出多个子 Agent 后，谁负责发现子 Agent 长时间无返回、进程丢失或执行卡死？
2. 一个宿主发出多个 Team 后，谁负责确认所有 active Team 都存在可工作的 Orchestrator loop？
3. `wait_agent` / `wait_team` 超时与真实执行超时如何区分？
4. Team heartbeat、task lease、Session lease 如何形成一致的故障判断，而不是彼此独立？
5. task lease 到期后，如何避免旧执行仍在运行、同一任务被重复执行及晚到结果覆盖新结果？
6. 子执行结束或进入 timeout、stalled、orphaned、invalid 等异常状态后，如何可靠通知主 Agent / 主 Team？
7. 主控制面如何一次获得直属 child 与全部 descendant 的结构化状态，而不是逐个被动调用 `wait_agent` / `wait_team`？
8. 主 Agent / Team 如何安全执行 cancel、close、cancel subtree、retry、reassign、acknowledge 或 defer，并确认动作已完成？
9. 哪些普通完成只需批量进入 mailbox，哪些关键异常应调度父 turn，同时避免 turn 风暴和成本失控？

本文是以下文档的专项补充，不替代已有的 AgentControl 收敛、生产就绪和统一执行内核规划：

- `docs/plan/multi-agent-framework-codex-comparison-plan.md`
- `docs/plan/multi-agent-agentcontrol-convergence-plan.md`
- `docs/plan/multi-agent-production-readiness-plan.md`
- `docs/plan/aicli-agent-runtime-reliability-optimization-plan.md`
- `docs/plan/spawn-team-teammate-model-routing-plan.md`
- `docs/plan/task-difficulty-model-routing-plan.md`
- `docs/working/light-agent-control-plane-2026-03-18.md`

本文只新增方案文档，不代表相关代码已经实现。文中标注为“建议新增”的字段、接口、配置和状态均属于目标设计。

现状说明：`backend/internal/execution/` 包已存在 `timeout.go`（`TimeoutSource`、`TimeoutBudget`、`WithTimeoutSource`，含 `TimeoutSourceAgentRunDeadline` 等来源枚举）。本文第 5.5、7.2、11.3 节引用的 `timeout_source` 诊断语义与该包直接对应；第 17 章建议新增的 execution 文件应视为对该包的**扩展**，而不是全新建包，避免重复定义 deadline 来源语义。

---

## 2. 当前实现基线

### 2.1 `spawn_agent` 当前是异步执行与完成事件推送

API 侧 `sessionAgentController.Spawn` 创建或 fork child Session、登记 AgentControl、订阅完成事件，然后通过 `SubmitPromptAsync` 异步启动 child run：

- `backend/internal/api/skills/session_runtime_support.go:479`
- `backend/internal/api/skills/session_runtime_support.go:573-585`

本地 CLI 在 `localActorRegistry.Spawn` 中采用相同思路：创建 child Session、注册关系、订阅完成事件并异步提交 prompt。

child Session 发布 `session_end` / `session_interrupted` 后，完成订阅会：

- 关闭对应的 AgentControl child 记录；
- 向父 Session 投递 completion mailbox；
- 向父 Session event store 写入 `subagent.completed`；
- 发布 runtime event。

API 侧关键位置：

- `backend/internal/api/skills/session_runtime_support.go:885-969`

这意味着正常完成通知是 push，而不是依赖父 Agent 持续轮询。但是完成消息只进入父 Session 的 mailbox/event store，不会自动启动父 LLM 的新 turn。

### 2.2 `wait_agent` 是调用期等待，不是常驻 watchdog

`wait_agent` 在一次工具调用内：

- 订阅 child Session 事件；
- 读取 child snapshot；
- 事件到达时立即唤醒；
- 每 500ms 做一次 fallback 检查；
- 默认约 30 秒返回或按 `timeout_ms` 返回。

关键位置：

- `backend/internal/api/skills/session_runtime_support.go:1513-1587`
- `backend/internal/api/skills/session_runtime_support.go:1648` 起

`wait_agent.timeout_ms` 只限制等待调用，不会 interrupt 或 close child Session。父模型不调用 `wait_agent` 时，也不存在一个父 Agent 定时遍历所有 child 的模型层循环。

### 2.3 Agent run 支持 deadline，但当前默认可无限运行

ReAct loop 已支持 `MaxRunDuration`：

- `backend/internal/agent/loop.go:348-351`

API 与 CLI 会把 runtime `agent.timeout` 传入 Agent 配置，但当前默认配置为：

```yaml
agent:
  timeout: 0s
```

位置：

- `backend/configs/runtime.yaml:3-10`

因此默认运行没有全局时长上限。Session lease 能证明执行 owner 仍在续租，但不能证明 LLM、Tool 或 Agent 状态持续产生进展。

### 2.4 Session lease 是 owner 存活信号，不是进度 watchdog

Session runtime store 提供跨进程 ownership lease：

- 默认 TTL 约 2 分钟；
- 获取 lease 后启动续租 goroutine；
- 默认每约 TTL/3 续租；
- lease 过期后，inspection 可以修复崩溃遗留的 running 状态。

关键位置：

- `backend/internal/chat/session_runtime_store.go:48-76`
- `backend/internal/chat/session_runtime_store.go:91-94`
- `backend/internal/chat/session_runtime_store.go:109-128`（`AcquireSessionLease` 获取并启动续租）
- `backend/internal/chat/session_runtime_store.go:153` 起（`renewLoop` 周期性续租，默认每约 TTL/3）
- `backend/internal/chat/actor.go:2374` 起（`LoadRuntimeStateForInspection` 读取 lease 判断 crash 遗留 running 并修复）

如果进程和续租 goroutine 存活，但 Provider 或 Tool 永久阻塞，Session lease 仍可能正常，系统不能仅凭该 lease 判断“执行健康”。

### 2.5 `spawn_team` 已有每 Team 一个后台 Orchestrator loop

API 侧 `handlerTeamLifecycleService.SyncLoops` 扫描 active teams，为每个 Team 启动独立 goroutine：

- `backend/internal/api/skills/team_lifecycle.go:43-104`

本地 CLI 有对应的 `localTeamLifecycleService.SyncLoops`：

- `backend/cmd/aicli/commands/chat_team_lifecycle.go:131-187`

Team Orchestrator 使用事件唤醒加定时 tick 的混合模式：

- 默认 `TickInterval = 1s`；
- 监听 lifecycle wake、AgentControl mailbox wake、task wake；
- ticker 作为事件丢失或没有事件时的兜底。

关键位置：

- `backend/internal/team/orchestrator.go:32-41`
- `backend/internal/team/orchestrator.go:49-177`

每次 tick 会回收过期 task lease、修复失败依赖、标记 ready task、claim 并启动 assignment，最后检查 Team 是否进入终态：

- `backend/internal/team/orchestrator.go:517-543`

所以 `spawn_team` 不是纯被动等待；它已经具备基础设施层主动编排循环。

### 2.6 Team task lease 与 teammate heartbeat 当前彼此独立

Team task 默认 lease 约 10 分钟。Orchestrator 每次 tick 调用 `LeaseManager.ReclaimExpired`，把过期 running task 放回 ready：

- `backend/internal/team/orchestrator.go:367-418`
- `backend/internal/team/lease.go:28-101`

TeammateRunner 运行 task 时每 5 秒更新 teammate heartbeat：

- `backend/internal/team/teammate_runner.go:361-395`

但当前正常 heartbeat loop 只更新 `last_heartbeat`，不会自动续租当前 task lease。Orchestrator tick 也不根据 teammate heartbeat 判断失联。项目已有 `SweepTeammates` API，可按 stale heartbeat 标记 offline 并可选回收任务，但它不是 Orchestrator 自动执行路径：

- `backend/internal/api/skills/team_handlers.go:4252` 起
- stale 判断与回收：`backend/internal/api/skills/team_handlers.go:4364-4414`

### 2.7 `wait_team` 是被动状态读取，不拥有 Team 生命周期

`wait_team` 默认每 250ms 读取持久 Team 状态和事件，默认约 30 秒超时：

- `backend/internal/toolbroker/broker.go:3284-3344`

它在开始等待时会触发 lifecycle sync，并执行一次 terminal reconcile，但等待超时只返回 `timed_out=true`，不会取消 Team 或停止 Teammate。

### 2.8 当前架构判断

当前能力可以概括为：

| 对象 | 主动机制 | 被动机制 | 主要缺口 |
|---|---|---|---|
| 普通 child Agent | 完成事件 push、Session lease renewal | `wait_agent` / event read | 无常驻 hang/deadline supervisor，默认执行无限时长 |
| Team | 每 Team Orchestrator 事件唤醒 + 1s tick | `wait_team` 轮询持久状态 | loop 缺少宿主级持续补位，heartbeat/lease/cancel 未闭环 |
| Team task | task lease 过期回收 | task outcome 上报 | heartbeat 不续 lease，回收不保证取消旧执行 |
| 父 Session | completion mailbox/event | 后续 turn 或显式 wait 消费 | completion 不自动触发父模型继续推理 |

---

## 3. 问题定义与风险等级

### R0：可能造成重复执行或永久不收敛

1. 普通 `spawn_agent` 在默认无 deadline 时可能永久保持 running。
2. Team task lease 到期后会重回 ready，但旧执行使用 detached context，可能继续运行。
3. heartbeat 正常但 task lease 未续租，长任务可能被误判过期并重复分配。
4. 旧 attempt 的晚到结果可能与新 attempt 竞争写入终态。
5. active Team 的 Orchestrator loop 因非 SQLite 错误退出后，只能等待下一次 `SyncLoops` 触发恢复。

### R1：可能造成错误恢复或不可解释状态

1. Session lease、teammate heartbeat、task lease、runtime progress 没有统一判断矩阵。
2. `wait_agent` / `wait_team` 的等待超时容易被误解为执行超时。
3. heartbeat 数据已存在，但主要通过人工/API sweep 消费。
4. orphan child 被修复为 stopped 后，父 Session 不一定得到结构化 orphan 原因。
5. 自动 retry 缺少 side-effect 与幂等分类，不能对所有 child/task 一刀切重试。
6. 父子关系主要围绕 child Session 使用，缺少统一的 Agent / Team descendant graph 和跨层级状态 rollup。
7. 基础设施即使发现异常，也缺少 `detected -> notified -> acknowledged/actioned -> resolved -> parent informed` 的持久闭环。
8. 主 Team 的 Orchestrator 只调度本 Team task graph，不天然监督由 Lead 或 task 再创建的 child Team。

### R2：运维与体验问题

1. 缺少统一视图回答“谁在监督、最近一次进展、为何仍在 running”。
2. 缺少 active Team 与 live Orchestrator loop 的一致性指标。
3. 父 Agent不显式 wait 时，完成结果可能长期只存在 mailbox 中。
4. 当前配置没有完整暴露 supervisor、progress timeout、orphan policy、cancel grace 等运维参数。
5. 父 turn 启动时没有强制 preflight，可能在存在未处理 child 异常时继续基于旧上下文决策。
6. 状态读取通常只描述“发生了什么”，未统一返回 `recommended_action`、`allowed_actions` 与动作安全条件。

---

## 4. 设计目标与非目标

### 4.1 设计目标

1. 所有长运行实体都有明确 owner、deadline、heartbeat、progress、attempt 与终态。
2. 等待超时、执行超时、无进展超时、owner 丢失四种语义严格分离。
3. 普通 child Agent 和 Team task 都由基础设施监督，而不是依赖父 LLM 定时调用工具。
4. Team Orchestrator loop 退出后能被宿主级 supervisor 自动发现并恢复。
5. task reclaim 前先阻止旧 attempt 继续产生有效副作用或写入有效结果。
6. 正常长任务通过 heartbeat + lease renewal 继续执行，不因固定 lease 到期被误回收。
7. 所有 Agent / Team 创建都形成 durable parent edge，主控制面可查询直属 child 与完整 descendant graph。
8. 所有超时、orphan、invalid、retry、cancel、fencing 结果都进入 durable lifecycle inbox，并可确认通知、动作与解决状态。
9. 每个父 turn 在执行前自动获得未处理 child lifecycle digest；关键异常可按策略调度父 turn，无需父模型先调用 wait。
10. 主 Agent / Team 可以在授权 scope 内查询诊断并执行 cancel、close、subtree cancel、retry、reassign、acknowledge 与 defer。
11. 状态快照同时返回 `recommended_action`、`allowed_actions`、动作前置条件和自动动作结果。
12. 保持 `wait_agent` / `wait_team` 的观察者语义，不让 wait API 成为生命周期 owner。
13. API runtime 与本地 CLI 使用相同状态机和恢复规则。
14. 能以观测模式灰度上线，再逐步启用自动 cancel/retry 和关键异常 wake。

### 4.2 非目标

1. 不让父 LLM 本身运行永久定时器；定时监督属于 runtime 基础设施。
2. 不为每个普通 completion 默认启动独立父 LLM turn；普通完成应批量投递，关键异常采用独立 wake 策略。
3. 不默认重试所有失败任务；有外部副作用的执行必须采用保守策略。
4. 不以删除现有 Session lease、Team lease 或 AgentControl 为目标；应复用并收敛现有能力。
5. 不要求第一阶段引入外部队列、远程数据库或分布式协调服务。
6. 不把 `SessionHub` idle actor sweep 当作执行 watchdog；它只负责资源回收。
7. 不把“主 Agent / Team 主动感知”解释为 LLM 自己轮询；主动性由 runtime 的状态聚合、可靠推送、preflight 注入和受控 wake 提供。

---

## 5. 目标架构

### 5.1 总体结构

建议在 AgentControl 与现有 Session/Team store 之上同时增加基础设施级 `ExecutionSupervisor` 和面向父/Lead 的 `Parent/Lead Supervision Control Plane`。前者持续巡检和执行安全策略，后者维护 descendant graph、聚合状态、可靠通知并接收主 Agent / Team 控制动作：

```text
Runtime Host
├─ ExecutionSupervisor
│  ├─ durable execution scan
│  ├─ event/wake subscriptions
│  ├─ deadline/progress/orphan evaluation
│  ├─ cancel/retry/reconcile actions
│  └─ metrics + lifecycle events
├─ Parent/Lead Supervision Control Plane
│  ├─ descendant execution graph + status rollup
│  ├─ critical lifecycle inbox + acknowledgement
│  ├─ parent turn preflight digest
│  ├─ critical event wake scheduler
│  └─ scoped control actions + action result tracking
├─ SessionHub
│  └─ SessionActor runs
├─ TeamLifecycleService
│  └─ Team Orchestrator loops
├─ AgentControl
│  ├─ agent registry
│  ├─ task registry
│  ├─ mailbox
│  └─ wake sequence
└─ RuntimeStore / TeamStore
```

职责边界：

- `ExecutionSupervisor`：判断执行是否健康、是否超时、是否 orphan，并触发 cancel/retry/reconcile。
- `Parent/Lead Supervision Control Plane`：把基础设施判定转化为父/Lead 可感知、可确认、可操作的结构化闭环，不自行执行 LLM 业务决策。
- `SessionActor`：执行单个 Agent run，报告状态和 progress，不负责监督其他 Agent。
- `TeamLifecycleService`：维护 TeamID 到 Orchestrator loop 的本地映射。
- `Team Orchestrator`：调度本 Team task graph；child Team 的 parent edge、状态 rollup 与 subtree 控制由控制面补足。
- `wait_agent` / `wait_team`：只观察 durable 状态，不拥有 child/team，不因等待方退出而自动取消。
- 父 LLM / Team Lead：通过自动 preflight、critical wake 与查询工具主动掌握后代状态，在授权范围内做业务控制决策，不执行基础设施 heartbeat polling。

### 5.2 两级 supervisor

建议拆成两个层级，避免一个组件同时承担本地 goroutine 与持久执行恢复：

#### Host Supervisor

负责当前进程内资源：

- 确保每个应由当前 host 管理的 active Team 都有 live Orchestrator loop；
- 检查 loop goroutine 是否退出、退出原因和 restart backoff；
- 检查当前 SessionActor run cancel handle 是否仍存在；
- host shutdown 时按顺序停止 scan、loop、actor 和 store watcher；
- 防止 goroutine 泄漏与重复启动同一 Team loop。

#### Durable Execution Supervisor

负责跨进程可恢复状态：

- 扫描 child Agent run 与 task attempt 的 durable 状态；
- 判断 execution deadline、progress timeout、owner lease expired；
- 创建并推进 cancel intent；
- 采用 CAS/fencing 关闭旧 attempt；
- 生成 retry attempt 或最终失败；
- 在 runtime 重启后继续未完成的恢复动作；
- 补发未投递的 parent completion 和 critical lifecycle notification。

第一阶段可以由同一进程实现 Host Supervisor、Durable Execution Supervisor 与父/Lead 控制面，但接口和持久状态应保持分层，避免未来多实例运行时重写状态机。

### 5.3 监督对象统一模型

建议新增统一的执行记录，名称可选 `execution_runs` 或 AgentControl execution registry。若暂不新增表，也应先以接口定义统一模型，再分别投影到 runtime/team store。

建议字段：

```text
run_id                  唯一执行实例 ID
kind                    agent_run | team_loop | team_task_attempt
workflow                spawn_agent | spawn_team
root_session_id
root_team_id
parent_session_id
parent_run_id
parent_team_id
parent_task_id
session_id
team_id
agent_id
teammate_id
task_id
attempt                  从 1 递增
status                   queued | running | waiting_approval | waiting_input |
                         cancel_requested | canceling | succeeded | failed |
                         canceled | timed_out | orphaned | invalid | superseded
owner_id                 当前 host/process owner
owner_lease_until
started_at
last_heartbeat_at        owner 存活
last_progress_at         有意义的运行进展
progress_seq             单调递增
execution_deadline_at
progress_deadline_at
lease_until              task/path claim lease
cancel_requested_at
cancel_deadline_at
finished_at
retry_policy
max_attempts
fencing_token            单调递增或不可复用 token
version                  CAS version
result_ref
error_code
error_metadata
created_at
updated_at
```

核心约束：

1. `run_id + attempt` 唯一。
2. 每个通过 `spawn_agent` / `spawn_team` 创建的对象必须记录不可歧义的父 edge 与 root scope；父对象结束不等于自动删除 edge。
3. child Team 必须记录 `parent_team_id` 或创建它的 `parent_session_id/parent_run_id`，不能只依赖普通 Team task graph 推断。
4. 只有持有最新 `fencing_token` 的 attempt 可以写有效终态、提交 patch 或释放/续租 claim。
5. terminal 状态只能通过条件更新进入，重复 terminal write 幂等。
6. heartbeat 更新不能替代 progress 更新。
7. wait API 的 timeout 不写入 `execution_deadline_at`。
8. graph 查询必须防环并按授权 root scope 过滤，禁止跨 root 控制不属于当前父/Lead 的对象。

### 5.4 三种时间信号必须分离

#### Heartbeat

回答：执行 owner 或 runner 是否仍活着？

示例：

- Session lease renewal；
- teammate heartbeat；
- Team Orchestrator owner lease。

#### Progress

回答：执行是否仍产生有意义进展？

可作为 progress 的事件：

- LLM response/delta 完成；
- tool call start/end；
- approval/input 状态迁移；
- task outcome、checkpoint 或 assistant message；
- Team task/依赖状态变化。

不应作为 progress 的事件：

- supervisor 自己的扫描；
- 无条件 heartbeat touch；
- 重复写入同一状态；
- UI/read API 查询。

#### Progress 埋点落地清单

为避免 P3/P4 实施时重新设计埋点，以下为建议的具体更新点（最终以源码确认为准）：

```text
agent_run（普通 child Agent）：
  - backend/internal/agent/loop.go：LLM response/delta 完成、tool call start/end、
    每步 ReAct 迭代结束（含 limit 触发与 run_timeout 结果）写入 last_progress_at/progress_seq
  - backend/internal/chat/actor.go：interrupt、approval/input 状态迁移、session_end
    统一经 progress recorder 上报
  - backend/internal/api/skills/session_runtime_support.go：spawn/followup/send_input
    提交时生成 run_id 并初始化 progress 基线
  - backend/internal/toolbroker/broker.go：wait 不更新 progress；工具调用结果写入
    仅当执行路径真实产生 assistant/tool 消息时

team_task_attempt：
  - backend/internal/team/teammate_runner.go：renewal loop 内仅当有有效进展时更新
    last_progress_at/progress_seq（见 8.4），无条件 touch 只更新 heartbeat
  - backend/internal/team/orchestrator.go：task 依赖状态迁移、outcome 上报为 progress

team_loop：
  - last_successful_tick_at 即 loop 级 progress；仅成功完成一次调度/回收周期才更新，
    tick 空转不更新
```

埋点统一走 `ProgressRecorder` 接口（`Record(ctx, runID, kind, event)`），避免各模块直接写时间戳导致口径漂移；`progress_seq` 由 recorder 单调分配，同一 run 内乱序到达按 seq 去重。

#### Lease

回答：谁有权继续修改执行状态、task 和 path claim？

Lease必须与 fencing token 配合。仅依靠时间戳 lease，不足以阻止旧执行在 lease 到期后提交晚到结果。

#### Fencing token 落地规则

本文多处依赖 fencing，但 token 本身不能只停留在原则层。落地规则如下：

1. **存储**：token 作为 `execution_runs.fencing_token` 与 `task_attempt.fencing_token` 的单调递增整数列；单机 SQLite 用 `MAX(fencing_token)+1` 或 `AUTOINCREMENT` 分配，跨表共用同一序列，保证任意两个 attempt 的 token 可比较。
2. **生成时机**：每次 claim task、启动新 attempt、reclaim 提升 token 时分配一次；同一个 attempt 生命周期内不变。
3. **与 CAS `version` 的关系**：`version` 用于任何状态写入的条件更新（写前校验、失败返回 conflict）；`fencing_token` 用于**跨 owner/attempt 的权威判定**（谁能提交终态、续租 claim、写结果）。写终态时同时校验：`version` 匹配当前行 + `fencing_token` 等于当前权威 token，两者任一失败即拒绝。
4. **校验点**（必须有 token 校验的最小集合）：
   - attempt 写 Task 终态（done/failed/blocked）；
   - apply worktree patch / artifact 落盘；
   - 释放或续租 task/path claim；
   - Team Orchestrator claim 新 task（校验 Team owner token，见 8.1）。
5. **旧 owner 恢复**：持有旧 token 的执行在任意校验点失败后进入 `superseded`，不再允许任何有效写；其晚到结果只写审计。
6. **迁移兼容**：存量 running task 无 token 时（见 15.1），禁止自动 enforce reclaim；首次观察时补发 token 并标记 `legacy_imported=true`。

### 5.5 统一健康判定矩阵

| Heartbeat | Progress | Execution deadline | 建议判定 | 动作 |
|---|---|---|---|---|
| 新鲜 | 新鲜 | 未到 | healthy | 无 |
| 新鲜 | stale | 未到 | stalled | 告警；超过 grace 后 cancel |
| stale | 任意 | 未到 | orphan suspected | 校验 owner/session lease；进入 cancel/recovery |
| 任意 | 任意 | 已到 | execution timed out | cancel；终止或按策略 retry |
| stale | stale | 已到 | orphaned timeout | fencing + reclaim + retry/fail |
| 新鲜 | 等待审批/输入 | 未到 | blocked but healthy | 使用独立 approval/input deadline，不应用普通 progress timeout |
| 任意 | 任意 | 任意 | invalid | 停止新动作；隔离或cancel；critical通知父/Lead并请求诊断 |

特殊规则：

- `waiting_approval` / `waiting_input` 不应因为没有 token/tool progress 被误判 stalled；它们使用独立 deadline。
- 对 Provider/tool 已声明有内部 deadline 的执行，Supervisor deadline 应作为更外层上限，但必须在诊断中记录最终生效来源。
- 多条件冲突时，优先记录最具体原因，例如 `approval_timeout`、`provider_timeout`、`owner_lease_expired`，而不是统一写 `context canceled`。
- 每个非 healthy 判定都必须生成可幂等投影到父/Lead lifecycle inbox 的事件；是否自动cancel/retry由策略决定，但父控制面感知不能依赖模型主动wait。

### 5.6 审计记录

16.10 要求所有状态迁移可审计。建议新增统一 `supervision_audit_log`，所有 timeout/cancel/orphan/reclaim/retry/ack/defer/wake/action 判定写入同一张表，供 `/debug`、diagnostics 与一致性审计（`agentcontrol` 已有 `consistency_audit` 可复用模式）：

```text
audit_id
run_id / task_id / team_id        至少一项
attempt
event_type                         复用 11.3 事件名
actor_kind                         supervisor | orchestrator | parent_agent | team_lead | operator | wait_api
actor_id
action                             observe | cancel | close | retry | reassign | ack | defer | wake | reclaim | fence
reason
decision_source                    deadline | progress_stale | heartbeat_stale | owner_lease_expired | explicit | policy
old_version
new_version
old_fencing_token
new_fencing_token
notification_id / action_id       可空，关联 inbox 或 action record
root_scope_id
payload                            JSON 快照（结果摘要、错误元数据）
created_at
```

约束：

1. 审计写入与状态迁移在同一事务，或经同一 outbox 最终一致，禁止"状态已变但审计丢失"。
2. 审计为 append-only，不更新不删除；retention 到期后整体归档（见第 10 章数据保留策略）。
3. wait API 读状态不产生审计；只有显式 ack/defer/action 或自动判定才写入。

---

## 6. 主 Agent / 主 Team 状态感知与控制闭环

基础设施持续巡检只是第一层。若 timeout、stalled、orphaned、invalid 等状态只写入后台 metrics，而主 Agent / Team Lead 无法及时获知并控制后代，系统仍未形成监督闭环。

本章定义第二层 `Parent/Lead Supervision Control Plane`。它不要求父 LLM 常驻运行 timer，而是由 runtime 维护父子关系、聚合状态、可靠推送异常、在父 turn 前注入摘要，并在必要时调度父 turn；父/Lead 获得受约束的查询和控制能力。

### 6.1 Durable descendant execution graph

`spawn_agent`、`spawn_team`、Team task 内再次 spawn，以及 child Agent 继续 spawn 时，都必须写入 durable edge。不能只依赖当前进程的 actor registry，也不能仅从 task graph 猜测 Team 层级。

建议关系记录：

```text
edge_id
root_session_id
root_team_id
parent_kind            session | agent_run | team | team_task
parent_id
parent_session_id
parent_run_id
parent_team_id
parent_task_id
child_kind             agent_session | agent_run | team | team_task_attempt
child_id
relation               spawned | delegated | retried | reassigned
created_by
created_at
closed_at
status                 active | detached | closed
version
```

约束：

1. 创建 child execution 与写 parent edge 应处于同一事务，或通过幂等 outbox 最终补齐。
2. 每个 edge 都可追溯到 root scope；查询必须防环、限制深度并支持分页。
3. Team 由某个 Team Lead、Team task 或 Agent 创建时，必须显式记录 `parent_team_id`，不能把所有 Team 都视为顶层独立对象。
4. retry 创建新 attempt，但不创建虚假的新业务父子关系；attempt 通过原 execution node 关联。
5. 父 Session / Team 终态后不立即删除 graph；edge 至少保留到审计与通知 retention 到期。
6. `detach` 必须是显式动作并记录 actor/reason；不能因父 runtime 重启而隐式失去 parent scope。

graph 至少支持：

- 直属 child 查询；
- 全部 descendant 查询；
- 从任意异常对象反查 parent chain；
- 以 root Session 或 root Team 为范围的状态 rollup；
- 对 Agent、Team 或整个 subtree 施加控制动作。

### 6.2 结构化 supervision snapshot 与状态 rollup

建议新增统一 `supervision_snapshot` 读模型，而不是要求父模型分别调用 `list_agents`、Team status、`wait_agent`、`wait_team` 后自行拼接。

示例：

```json
{
  "scope": {
    "root_session_id": "parent-session",
    "root_team_id": "team-root",
    "mode": "descendants"
  },
  "snapshot_seq": 184,
  "generated_at": "2026-07-31T10:00:00Z",
  "summary": {
    "running": 3,
    "blocked": 1,
    "stalled": 1,
    "timed_out": 1,
    "canceling": 1,
    "terminal_unacknowledged": 2,
    "action_required": 1
  },
  "descendants": [
    {
      "kind": "agent_run",
      "id": "run-42",
      "parent_path": ["parent-session", "child-1", "run-42"],
      "execution_status": "canceling",
      "supervision_state": "execution_timed_out",
      "heartbeat_age_ms": 3200,
      "progress_age_ms": 420000,
      "execution_deadline_at": "2026-07-31T09:59:00Z",
      "reason": "execution_deadline_exceeded",
      "auto_action": {
        "action": "cancel",
        "status": "in_progress",
        "action_id": "act-77"
      },
      "recommended_action": "inspect_cancel_result",
      "allowed_actions": ["inspect", "acknowledge", "defer", "close"],
      "action_required": false,
      "last_change_seq": 181
    }
  ]
}
```

快照要求：

1. 返回 `as_of` / `snapshot_seq`，避免不同 store 读取时间不一致时伪造精确状态。
2. 状态至少覆盖 healthy、running、blocked、stalled、timed_out、orphan_suspected、orphaned、invalid、cancel_requested、canceling、terminated、recovered。
3. 每项包含 deadline、heartbeat age、progress age、异常原因、最近状态变化和诊断引用。
4. `recommended_action` 是 policy evaluator 的建议，不等于动作已经获批。
5. `allowed_actions` 必须根据对象状态、fencing、side-effect class、调用者权限和当前版本计算，不能由客户端硬编码。
6. 已由 runtime 执行安全自动动作时，返回 `auto_action` 及结果，避免父模型重复 cancel/retry。
7. 默认优先返回变化项、异常项和 action-required 项；完整健康后代可按分页读取，防止大量 child 占满上下文。
8. 多个 child 同时异常时按 root scope 聚合，父模型收到一份稳定摘要而不是事件风暴。

兼容实现可以先扩展 `list_agents` 和 Team status：

```text
scope: children | descendants
include_teams: true
include_terminal: false
after_seq: <last_seen_seq>
health: any | abnormal | action_required
```

长期建议提供一个统一 snapshot API/tool，避免 Agent 与 Team 两套 rollup 继续分叉。

### 6.3 Critical lifecycle inbox

completion outbox 只解决“终态结果是否送达”。监督闭环还需要 durable `lifecycle_notifications`，承载非终态关键变化、推荐动作、通知状态和确认状态。

建议字段：

```text
notification_id
root_scope_id
target_parent_session_id
target_parent_team_id
subject_kind
subject_id
subject_version
event_seq
event_type
severity                  info | warning | critical
supervision_state
reason
diagnostic_ref
recommended_action
allowed_actions
auto_action_id
delivery_state            pending | delivered | seen
decision_state            unacknowledged | acknowledged | deferred | actioned
resolution_state          unresolved | recovered | closed | failed
defer_until
delivered_at
seen_at
acknowledged_at
resolved_at
created_at
updated_at
```

投递规则：

1. durable lifecycle event 是事实源，notification 是面向特定父/Lead 的投影视图。
2. 以 `root_scope + subject + subject_version + event_type` 作为幂等键，允许至少一次派发而不重复业务生效。
3. completion、warning、critical 分通道和优先级，关键异常不能被普通 completion backlog 阻塞。
4. 父会话 busy、waiting approval、运行时重启或 mailbox 暂时失败时，notification 保持 pending 并重试。
5. `seen` 仅表示摘要已注入或被 API 读取，不表示父/Lead 已接受风险。
6. `acknowledged` 表示无需进一步父决策；`deferred` 必须带原因和期限；`actioned` 关联 durable action record。
7. unresolved critical 通知即使已 seen，也应在后续 preflight 中以压缩摘要继续出现，直到 acknowledged、actioned 或 resolved。
8. terminal/recovery 后应生成 resolution notification，让父/Lead 知道 cancel、close、retry 或自动恢复的最终结果。

### 6.4 父 turn preflight lifecycle digest

每次父 Session 或 Team Lead Session 准备开始新 turn 时，runtime 必须先执行 preflight：

1. 读取该 root scope 中 `after_seen_seq` 的 lifecycle changes。
2. 合并所有 unresolved critical、action-required 和最近 terminal 变化。
3. 生成 deterministic、结构化、限制大小的 lifecycle digest。
4. 以 runtime/system context 注入本 turn；不得依赖模型先想起调用 wait。
5. 记录 `delivered_seq` 与 `seen_seq`，但不能把“已注入”自动当成 acknowledged。
6. 若超过上下文预算，优先保留 critical、action-required、auto-action failure，并提供 snapshot cursor 供模型继续查询。

digest 至少回答：

- 哪些 child / descendant 发生了变化；
- 当前是健康、无进展、超时、失联、无效、取消中还是已终止；
- runtime 是否已经自动采取动作；
- 哪些异常仍需要父/Lead 决策；
- 可以执行哪些安全动作；
- 上次摘要之后哪些问题已经恢复或关闭。

建议注入结构：

```text
[Child lifecycle preflight]
snapshot_seq: 184
critical_unresolved: 1
auto_actions_in_progress: 1
resolved_since_last_turn: 2

- team child-team-2: orphaned; no live orchestrator owner;
  recommended=cancel_subtree; allowed=[inspect,cancel_subtree,defer]
- agent run-42: timeout; runtime cancel is in_progress;
  no duplicate cancel required

Use supervision_snapshot(after_seq=184) for full diagnostics.
```

preflight 是“父模型一旦运行就不能不知道异常”的最低门槛；它与是否自动启动新 turn 是两个独立能力。

### 6.5 普通完成与关键异常的分级 wake 策略

推荐默认策略从单一 `mailbox_only` 调整为分类策略：

| 事件类别 | 默认投递 | 是否调度父 turn | 说明 |
|---|---|---|---|
| 普通 progress | timeline/rollup | 否 | 只更新 snapshot |
| 普通 completion | mailbox batch | 否 | 在下一 turn preflight 汇总 |
| blocked/warning | lifecycle inbox | 可配置 | approval/input 使用独立期限 |
| timeout/stalled/orphaned/invalid | critical lifecycle inbox | 父会话 idle 时默认调度 | debounce 后合并同 root 异常 |
| 自动 cancel/reclaim 失败 | critical lifecycle inbox | 是 | 需要父/Lead 或 operator 介入 |
| 明确安全策略已自动执行 | critical inbox + result | 通常是 | 告知动作与最终结果，不等待父模型才能止损 |

关键 wake scheduler 必须：

1. 按 root Session / Team debounce 和 batch，多个 child 异常只调度一个父 turn。
2. 检查父 Session 是否 running、waiting approval/input 或正在 compact；不可并发启动第二个 turn。
3. 父会话 busy 时写 durable `wake_pending`，当前 turn 结束或下一次可运行时再调度。
4. 限制单位时间 auto turn 数和连续监督 turn 数，超过上限升级 operator 告警但不丢 notification。
5. wake prompt 只引用 lifecycle digest，不把不可信 child 原文直接提升为 system instruction。
6. 记录 `wake_reason`、触发 notification sequence、去重键和调度结果。
7. operator 可以关闭 critical auto wake，但无法关闭 durable notification 和下一 turn preflight。

建议配置：

```yaml
parent_supervision:
  completion_delivery: mailbox_batch
  critical_delivery: lifecycle_inbox
  critical_resume_mode: schedule_turn
  critical_resume_when: parent_idle
  debounce: 2s
  batch_window: 5s
  max_auto_turns_per_window: 2
  auto_turn_window: 10m
  inject_preflight: true
```

#### 实现接入点

策略需要落到具体代码位置，否则 P2 实施时仍需重新定位：

```text
preflight hook：
  - API：backend/internal/api/skills/session_runtime_support.go 的 turn 启动路径
    （SubmitPromptAsync 前读取 lifecycle digest 注入 context）
  - CLI：backend/cmd/aicli/commands/chat_actor_registry.go 对等 hook
  - Team Lead：Team Orchestrator 准备 Lead task 时注入

parent_idle 判定来源：
  - Session state：SessionIdle / waiting approval / waiting input / compact 由
    backend/internal/chat/actor.go 的 RuntimeState 提供；busy 指 running 状态
  - Team Lead：以当前 Lead task 状态为准

wake 调度入口：
  - 新增 wake_scheduler 订阅 lifecycle inbox；turn 结束/可运行时在
    actor 状态迁移点检查 wake_pending
  - 不新增独立常驻 LLM 轮询 goroutine；wake 只是把 pending digest
    投递到既有 turn 启动路径

turn 去重：
  - 以 wake_reason + notification seq + parent session 为键；并发 turn 检查
    actor running 状态，冲突时写 durable wake_pending（见 6.5 规则 3）
```

### 6.6 主控制动作与 durable action record

主 Agent / Team Lead 不应只能读到“timed out”。控制面需要提供统一动作入口，并把请求、执行、结果都持久化：

```text
action_id
root_scope_id
requested_by_kind
requested_by_id
target_kind
target_id
action                    inspect | acknowledge | defer | cancel | close |
                          cancel_subtree | retry | reassign
cascade_mode              none | descendants
reason
expected_version
expected_fencing_token
status                    requested | accepted | executing | completed |
                          partially_completed | rejected | failed
result
created_at
started_at
finished_at
```

动作语义：

- `inspect`：返回诊断、parent path、最近事件、owner、deadline、progress 和 pending action。
- `acknowledge`：确认已知晓且当前无需进一步动作；不能伪造 child terminal。
- `defer`：把父决策延后到指定时间或条件；Supervisor 仍继续执行基础安全策略。
- `cancel`：对当前 execution 发出协作式取消，并等待 acknowledgement / grace。
- `close`：关闭已无 live execution 的 Agent / Team 资源；若仍在运行，默认拒绝或先走显式 cancel。
- `cancel_subtree`：按 descendant graph 冻结新 spawn、先 fence/cancel 叶节点，再由下至上关闭；返回逐节点结果。
- `retry`：创建新 attempt；必须通过 side-effect 与 fencing 策略校验。
- `reassign`：仅适用于 Team task/child Team ownership 可迁移场景；先解除或 fence 旧 assignee。

通用约束：

1. 所有变更动作携带 `expected_version`；状态已变化时返回 conflict 和新 snapshot，不盲目重放。
2. `allowed_actions` 由服务端计算并再次校验；模型传入动作不代表自动授权。
3. subtree 操作必须先把 root 标为 `closing/canceling`，阻止并发新增 descendant。
4. cascade 中部分失败时返回 `partially_completed`，保留未完成节点和推荐后续动作。
5. `force kill` 不作为普通模型工具默认能力；仅 operator 权限可用，并仍需写审计。
6. 父/Lead 只能控制自己的 root scope；跨 Team / Session 控制需要显式授权。
7. close/cancel 后必须产生 resolution notification，而不是控制工具返回成功后就丢失后续状态。

### 6.7 主 Team 对 child Team 的监督

Team Orchestrator 当前负责本 Team task graph，但嵌套 Team 需要单独的 durable supervision edge 和 Lead rollup：

1. `spawn_team` 在 Team Lead Session、Team task 或 child Agent 上下文中调用时，记录 `parent_team_id` 与创建 run。
2. 主 Team snapshot 同时聚合本 Team task、直属 child Agent、直属 child Team 及所有嵌套 descendant。
3. child Team 的 Orchestrator owner loss、degraded loop、terminal/summary failure 进入主 Team lifecycle inbox，并路由给当前 Lead Session。
4. Team Lead 可以 cancel/close 单个 child Team，也可以对 Team subtree 执行 cancel；不能只依赖停止某个 Teammate。
5. 主 Team terminal reconcile 前必须明确处理仍 active 的 child Team：等待、detach、cancel 或以策略允许的 partially completed 收敛。
6. Team Lead 更换时，unacknowledged notification、wake pending 和 action-required 项转移给新 Lead，不因内存 actor 更换而丢失。
7. 若 Lead Session 不可用，runtime 继续执行预先批准的安全动作，并把需要业务判断的项升级给 root parent 或 operator。

### 6.8 主动感知与自动动作的职责边界

闭环状态应至少为：

```text
detected
  -> notification_pending
  -> delivered / wake_pending
  -> seen
  -> acknowledged | deferred | action_requested | auto_actioned
  -> canceling / closing / retrying / recovering
  -> closed | recovered | failed
  -> resolution_notified
  -> resolved
```

职责划分：

- runtime 必须自动做：检测、状态聚合、通知持久化、preflight 注入、策略允许的 deadline cancel、fencing、防止旧 attempt 生效、可靠投递。
- runtime 可按显式策略做：read-only retry、orchestrator restart、critical parent wake、安全 reclaim。
- 父 Agent / Team Lead 决策：涉及目标取舍、是否放弃 subtree、非幂等副作用 retry、reassign、降级交付和资源关闭策略。
- operator 决策：强制 kill、跨 root 接管、策略覆盖、持续 store outage 或 Supervisor 自身失效。

因此，“基础设施主动检测”与“主 Agent / Team 主动感知和控制”不是二选一：前者提供可靠时钟、状态和止损，后者通过控制面获得上下文并完成业务决策。

### 6.9 工具/API 建议

推荐新增或扩展：

```text
supervision_snapshot(
  scope=children|descendants,
  root_session_id?,
  root_team_id?,
  after_seq?,
  health=any|abnormal|action_required,
  include_terminal?,
  limit?,
  cursor?
)

control_descendant(
  target_kind,
  target_id,
  action=cancel|close|cancel_subtree|retry|reassign,
  reason,
  expected_version,
  cascade?
)

ack_lifecycle(
  notification_ids? | through_seq?,
  decision=acknowledge|defer,
  defer_until?,
  note?
)
```

短期兼容路径：

- `list_agents` 增加 `scope=descendants` 和 supervision 字段；
- Team status 增加 child Team rollup；
- 现有 `close_agent` / Team cancel 入口接入统一 action service；
- `wait_agent` / `wait_team` 复用 snapshot，但仍保持等待者语义；
- parent turn runner 增加 preflight hook；
- mailbox/timeline 增加 lifecycle sequence 与 acknowledgement cursor。

所有 API 都应返回：

```text
snapshot_seq
recommended_action
allowed_actions
action_required
auto_action
pending_action
notification_ids
next_action
```

---

## 7. `spawn_agent` 优化设计

### 7.1 增加 child run durable identity

当前 child Session identity 与一次具体 run 的 identity 不应混为一体。一个 child Session 可以被 `followup_task` 或 `send_input` 多次运行，因此建议每次提交生成 `run_id`：

```text
session_id = child-1
run_id     = run_<uuid>
attempt    = 1
```

`spawn_agent` 返回值建议增加：

```json
{
  "id": "child-1",
  "session_id": "child-1",
  "run_id": "run_xxx",
  "execution_deadline_at": "...",
  "supervision_policy": "interrupt_then_fail"
}
```

后续 `wait_agent` 可以继续以 child id 查询兼容快照，同时可选接受 `run_id` 获取精确一次运行的状态。

#### 续跑入口（followup_task / send_input / resume_agent）同样纳入监督

7.1 只覆盖了 `spawn_agent` 首次提交。child Session 可以被 `followup_task`、`send_input`、`resume_agent` 多次运行，若这些入口不生成 run_id，则监督存在"第二扇门"缺口。规则：

1. 每次 `followup_task` / `send_input` 提交都生成新的 `run_id` 与 `attempt`（沿用 5.3 模型），并继承 child 的 parent edge 与 root scope。
2. 新 run 重新初始化 deadline、progress 基线、cancel state；不继承旧 run 的 terminal 状态。
3. `resume_agent` 恢复 actor 时：若存在未终态 run，继续该 run（不新建）；若旧 run 已 terminal（如 timed_out/orphaned），则新建 run，且必须先把旧 run 的 late write 全部 fence。
4. 新 run 的 timeout/stalled/orphaned/terminal 与首次 run 一样进入同一 lifecycle inbox 与 preflight，父控制面无需区分入口。
5. `wait_agent` 以 child id 查询时返回**最近一次 run** 的状态，同时列出历史 run 摘要（含 `run_id`），避免多 run 语义混淆。

### 7.2 明确四类超时

建议 `spawn_agent` 增加以下可选参数，默认值由配置解析：

```json
{
  "timeout_sec": 1800,
  "progress_timeout_sec": 300,
  "approval_timeout_sec": 3600,
  "cancel_grace_sec": 15
}
```

语义：

- `timeout_sec`：真实 execution deadline；到期后 Supervisor 请求 interrupt。
- `progress_timeout_sec`：运行状态下无有效 progress 的最大时间。
- `approval_timeout_sec`：等待审批/输入的独立期限。
- `cancel_grace_sec`：发出 interrupt 后等待 actor 结束的时间。

兼容规则：

1. 未传参数时读取 runtime 配置。
2. 显式 `0` 表示不限制，仅在 operator 允许 unbounded 时生效。
3. `wait_agent.timeout_ms` 仍只控制等待。
4. tool/provider 更短 deadline 可以先发生，但诊断必须记录 `timeout_source`。

### 7.3 child Agent watchdog 状态机

```text
queued
  -> running
  -> waiting_approval / waiting_input
  -> running
  -> succeeded / failed

running
  -> cancel_requested      deadline/progress/orphan
  -> canceling             actor interrupt 已发送
  -> canceled/timed_out    grace 内退出
  -> orphaned              actor/owner 无法确认或无法中断
```

Supervisor 动作：

1. 发现 execution deadline 到期或 progress stale。
2. CAS 把 run 标为 `cancel_requested`，写入 `cancel_source`。
3. 调用 SessionActor interrupt；不存在 live actor 时检查 Session lease。
4. 等待 `cancel_grace_sec`。
5. child 正常发出 `session_end` 时映射为 `timed_out` 或 `canceled`。
6. grace 到期仍未结束时提高 fencing token，将 run 标为 orphaned/superseded。
7. 向父 mailbox 只投递一次结构化 completion。

#### 非 deadline 类结束的映射规则

child 不一定总是因 deadline/progress/orphan 结束。Supervisor 需要为所有 `session_end` / `session_interrupted` 变体定义统一的 supervision 映射，避免同一结果在不同入口被解释为不同状态：

| child 结束事件 | supervision 状态 | lifecycle 严重度 | 说明 |
|---|---|---|---|
| 正常完成（session_end, result ok） | succeeded | info | completion outbox 投递 |
| 运行错误 / provider 错误终态 | failed | warning | 记录 error_code/error_metadata |
| ReAct limit 触发（maxSteps/maxToolCalls） | failed（reason=limit） | warning | 与 deadline 区分，limit 是业务上限不是超时 |
| 用户/operator 手动 stop | canceled（reason=user_stop） | info | 记录 actor，不视为异常 |
| Ctrl+C / host shutdown | canceled（reason=shutdown） | warning | 与 Supervisor cancel 来源区分（见 14.4） |
| approval/input 超时（approval_timeout_sec 到期） | timed_out（reason=approval_timeout） | critical | 独立 deadline，见 7.2 |
| 显式 cancel/close 工具调用 | canceled | info | 关联 action_id |
| 进程崩溃后 lease 过期被修复 | orphaned | critical | 父收到 orphan 结构化原因 |

映射必须由同一 reconcile 函数执行并写入审计（5.6），不得在各 handler 内各自解释。

### 7.4 父会话完成通知 outbox

目前完成事件订阅已可向父 mailbox 投递结果。建议进一步采用 durable outbox，避免以下窗口：

```text
child 已终态
→ 进程在 parent mailbox append 前崩溃
→ 父会话永远看不到完成
```

建议流程：

1. child terminal transition 与 completion outbox row 同事务或以幂等键写入。
2. dispatcher 异步发送到父 AgentControl mailbox。
3. 成功后记录 `delivered_at` 与 parent mailbox sequence。
4. Supervisor 扫描未投递 outbox 并重试。
5. 幂等键建议：`subagent_completion:<run_id>:<terminal_version>`。

`subagent.completed` session event 继续作为 display mirror，不作为唯一完成权威。

### 7.5 父 Agent 感知与 wake 衔接

child Agent 的 completion outbox、critical lifecycle inbox 和父 turn preflight 必须同时接入，不能再用一个 `parent_resume_mode` 混合表达“如何投递”与“是否启动 turn”。

默认策略：

```yaml
completion_delivery: mailbox_batch
critical_delivery: lifecycle_inbox
critical_resume_mode: schedule_turn
critical_resume_when: parent_idle
inject_preflight: true
```

行为：

- 普通 completion 批量写 mailbox，不为每个 child 单独启动父 turn。
- timeout、stalled、orphaned、invalid、cancel failure 写 critical inbox；父会话 idle 时按 debounce 调度监督 turn。
- 父会话 busy、waiting approval/input 或正在 compact 时不并发启动 turn，而是保留 durable `wake_pending`。
- 每次自然发生或自动调度的父 turn 都先注入 unresolved lifecycle digest。
- runtime 已按策略 cancel/fence 时，父 Agent收到动作状态而不是再次盲目 cancel。
- operator 可以关闭 critical auto wake，但不能关闭 durable notification 和 preflight 摘要。

第一阶段至少实现 lifecycle inbox 与 preflight；critical `schedule_turn` 可以在 observe 模式验证去重、预算和 busy-state 行为后分批 enforce，不能长期停留在只有后台 metrics 或父模型主动 wait 才可见的状态。

### 7.6 Retry 策略

普通 child Agent 自动 retry 必须保守：

| 任务类型 | 默认策略 |
|---|---|
| read-only / 无副作用研究 | 可在 owner loss、provider transient error 后自动 retry |
| worktree isolation 写任务 | 可创建新 attempt，但必须保留旧 worktree 并使用 fencing |
| main workspace 写任务 | 默认不自动 retry，先进入 `needs_review` |
| 具有外部 API/发布/支付等副作用 | 禁止自动 retry，除非工具声明 idempotency key |
| waiting approval/input | 不 retry，保持可恢复等待或按独立 deadline 取消 |

### 7.7 幂等声明与检测机制

7.6 与 16.7 依赖"工具/任务声明幂等性"，但缺少定义会让自动 retry 的安全门禁没有落点。落地规则：

1. **声明位置**：工具 schema 增加 `idempotency: {supported: true, key_source: "caller_supplied" | "auto_generated", key_param: "<参数名>"}`；Team task 在 `task_attempt` record 或 task spec 上声明 `retry_class: read_only | idempotent | isolated_workspace | external_side_effect`。
2. **校验**：retry 决策前，Policy evaluator 只对 `retry_class` 为 read_only / idempotent / isolated_workspace 的 task 自动 retry；`external_side_effect` 且未声明 key 的一律进入 `needs_review`。
3. **key 使用**：带 `key_source=caller_supplied` 时，重试请求必须携带同一 key，执行侧按 key 幂等去重（已有结果直接返回）；`auto_generated` 表示 provider/工具内部按请求指纹去重，runtime 不重复生成。
4. **未声明保护**：无幂等声明、无隔离工作区、非 read-only 的 task，默认视为有副作用，禁止自动 retry；不允许通过"估计没有副作用"绕过。
5. **审计**：每次 retry 判定写入审计（5.6），包含 `retry_class`、key、依据，便于事后核对误判。

---

## 8. `spawn_team` 优化设计

### 8.1 为 Team Orchestrator 增加 owner lease

当前 `SyncLoops` 维护进程内 `loopCancels`/`loopSignals`，建议增加持久 owner lease：

```text
team_id
owner_id
lease_until
heartbeat_at
fencing_token
last_tick_at
last_successful_tick_at
restart_count
last_error
```

单进程阶段也应写 owner lease，为未来 runtime-server 多实例或 CLI/runtime 并存提供正确边界。

启动 loop 前：

1. 原子 acquire Team Orchestrator lease。
2. 获取 fencing token。
3. 只有 lease owner 可以 claim 新 task。
4. loop 每 `lease_ttl/3` 续租并更新 `last_tick_at`。
5. lease 失效后立即停止 claim；旧 owner 的 claim 因 fencing token 无效而失败。

#### paused Team 的 lease 生命周期

8.2 提到"terminal/paused Team 停止 loop 并释放 owner lease"，但 resume 路径必须闭环：

1. **pause**：Orchestrator 停止 tick 前先释放 owner lease 并写 `pause_reason`；active task 的 attempt lease 不立即回收，而是保留 `paused` 标记，避免 resume 前被误 reclaim。
2. **resume**：重新走 8.1 的原子 acquire 流程（新 owner lease + 新 fencing token）；已 paused 的 attempt 若超时，按 8.5 安全 reclaim 而不是直接续租旧 token。
3. **pause 期间**：Supervisor 对 paused Team 的 task 只做 observe 判定，不执行 reclaim/cancel（避免恢复后状态与动作矛盾）；超过配置的 `pause_max_duration` 后可升级 operator 决定关闭或强制 reclaim。
4. **与 child Team 关系**：parent Team pause 不自动 pause child Team；child Team 继续由自己的 owner loop 调度，parent 只停止向 child 派发新任务。

### 8.2 增加宿主级 Team loop reconciler

`SyncLoops` 继续作为立即触发入口，同时增加常驻 reconciler，例如每 5 秒检查：

```text
active teams in store
vs.
local live loops
vs.
durable orchestrator owner leases
```

处理：

- active Team 无 loop、无有效 owner：尝试 acquire 并启动；
- active Team 本地 loop 意外退出：按 backoff 重启；
- terminal/paused Team仍有 loop：cancel 并释放 owner lease；
- 本地 loop 存在但 owner lease 丢失：停止 loop；
- 重复 loop：只有最新 fencing token 可调度，旧 loop自行退出。

重启退避建议：

```text
1s, 2s, 5s, 10s, 30s, 1m，最大 5m
```

连续失败超过阈值后 Team进入 `orchestrator_degraded` 诊断状态，但不应直接把业务 Team 标为 failed，除非恢复策略耗尽或 operator 明确配置。

### 8.3 Team task attempt 化

当前 task ID 与一次执行 attempt 应拆分：

```text
task_id = task-42
attempt = 1, 2, 3...
run_id  = taskrun_<uuid>
fencing_token = <monotonic>
```

Task record保留业务状态，attempt record保存：

- assignee/session；
- start/end；
- heartbeat/progress；
- lease；
- route/provider/model；
- result/error；
- cancel/reclaim reason；
- fencing token；
- artifact/worktree reference。

只有当前 attempt 可以把 Task从 running 写入 done/failed/blocked。旧 attempt 返回时写入：

```text
attempt.status = superseded
attempt.late_result_ref = ...
```

但不得覆盖 Task 当前状态。

### 8.4 heartbeat 与 task lease 联动

TeammateRunner执行 task 时，建议启动统一 renewal loop，而不是只更新 teammate heartbeat：

```text
每 renewal_interval：
  1. touch teammate heartbeat
  2. touch attempt heartbeat
  3. 若有有效进展，更新 last_progress_at/progress_seq
  4. renew task lease
  5. renew path claims
  6. 验证 fencing token 仍是当前 token
```

默认值建议：

```text
task_lease_ttl       = 2m
renewal_interval     = 30s
heartbeat_timeout    = 90s
progress_timeout     = 10m（可按 difficulty/route 覆盖）
cancel_grace         = 15s
```

这里建议把当前 10 分钟固定 lease 缩短为可持续续租的 lease。短 TTL + 周期续租比“长 TTL 且不续租”更容易快速发现 owner loss，也不会误伤正常长任务。

续租失败处理：

- 临时 SQLite lock：短退避重试，不立即 cancel；
- fencing token mismatch：立即停止执行并标记 superseded；
- owner lease lost：请求当前 SessionActor interrupt；
- store 长时间不可用：进入 degraded，超过 store outage grace 后停止产生新副作用。

### 8.5 安全 reclaim 流程

不再采用“发现 lease 过期后直接 ready”的单步恢复，建议改为：

```text
running
  -> reclaim_pending
  -> cancel_requested
  -> canceling
  -> superseded/canceled
  -> ready (new attempt)
```

详细步骤：

1. Supervisor发现 task lease expired。
2. 检查 teammate heartbeat、Session lease、attempt progress 与 Team Orchestrator owner。
3. 若 heartbeat/progress 新鲜，仅修复或补续 lease，不 reclaim。
4. 若确认 stale，CAS 写 `reclaim_pending` 并提升 fencing token。
5. 向旧 SessionActor发送 interrupt，等待 cancel grace。
6. 释放旧 attempt 的 task/path claims。
7. 根据 retry policy 创建新 attempt，Task回到 ready。
8. 旧 attempt 晚到结果只能写 attempt审计记录，不得完成 Task。
9. 向 Team lead mailbox 写入一次 `task.reclaimed` 结构化消息。

### 8.6 heartbeat stale sweep 收敛

现有 `SweepTeammates` API 应保留为运维入口，但核心 stale 判断应下沉为可复用 service，例如：

```go
type TeammateHealthEvaluator interface {
    Evaluate(ctx context.Context, teamID string, asOf time.Time) ([]HealthDecision, error)
}
```

调用方：

- Durable Execution Supervisor 自动调用；
- HTTP `SweepTeammates` 复用同一 evaluator，支持 dry-run；
- `/debug`、agents panel 读取 decision，不自己复制判断；
- 测试直接覆盖 evaluator 的状态矩阵。

这样可避免 API sweep 与后台 supervisor 对 stale 的定义发生漂移。

### 8.7 Team terminal、summary 与 child Team 收敛

Supervisor和 Orchestrator都可能触发 terminal reconcile，必须保证幂等：

- `team.completed` 每个 terminal version 只持久化一次；
- `team.summary` 采用幂等锁/键；
- summary失败时写 `team.summary.failed` 并生成 deterministic fallback；
- retry/superseded attempt 不得让已 terminal Team重新 active，除非显式 resume；
- `wait_team` 读取 durable terminal/summary，不依赖某个 in-memory loop 是否仍在。
- Team terminal 前检查 durable descendant graph；仍 active 的 child Team 必须明确进入 wait、detach 或 cancel_subtree。
- child Team 的 owner loss、degraded、summary failure、terminal 与 recovery 进入 parent Team lifecycle inbox，并路由给当前 Lead Session。
- Team Lead 变更后，未确认 notification、pending action 和 wake cursor 按 Team identity 接管，不能绑死在旧 lead actor 内存中。
- parent Team 的 rollup 只有在所有 required child Team 已收敛后才能标记 done；允许遗留 child 时必须给出 partially completed 原因。

---

## 9. 等待工具语义优化

### 9.1 `wait_agent`

保持观察者语义，新增更明确的输出：

```json
{
  "timed_out": true,
  "wait_timeout_ms": 30000,
  "execution_status": "running",
  "execution_deadline_at": "...",
  "last_heartbeat_at": "...",
  "last_progress_at": "...",
  "supervision_state": "healthy",
  "recommended_action": "none",
  "allowed_actions": ["inspect", "cancel"],
  "pending_notifications": 0,
  "next_action": "child continues running; do not interpret wait timeout as execution timeout"
}
```

建议事件优先，500ms fallback 保留为 durable catch-up 兜底。若引入 execution run watcher，`wait_agent` 可直接等待 run sequence，而不是反复重建综合 snapshot。

### 9.2 `wait_team`

当前每 250ms 读取 TeamStore，可以逐步改为：

1. 首次读取 durable snapshot；
2. 订阅 Team event/task wake sequence；
3. event到达后按 `after_seq` catch-up；
4. 低频 fallback polling，例如 1～2 秒；
5. timeout 后返回完整当前 snapshot，而不是只返回 `TeamID + TimedOut`。

输出建议增加：

- `orchestrator_owner`；
- `orchestrator_lease_until`；
- `last_successful_tick_at`；
- running/blocked/reclaim_pending task count；
- oldest progress age；
- degraded reason；
- child Team/descendant rollup；
- unresolved critical notification count；
- `recommended_action` / `allowed_actions` / pending auto action；
- execution continues标志。

### 9.3 禁止 wait API 隐式改变执行策略

除“确保 lifecycle loop 已同步”之外，wait API 不应：

- 自动延长 execution deadline；
- 改变 retry policy；
- 因 wait caller cancel 而取消 child/team；
- 把 wait timeout 写成 task/agent timeout；
- 以 wait调用次数作为健康信号。

wait API 可以复用统一 supervision snapshot 并返回 lifecycle cursor，但“读取到”不应自动等同于 `acknowledge`；只有显式 ack/action 或既定自动确认策略才能推进 notification decision state。

---

## 10. 配置设计

建议在 runtime 配置中增加统一 supervisor 配置，并为 Agent/Team提供覆盖项。示例：

```yaml
execution_supervisor:
  enabled: true
  mode: observe            # observe | enforce
  scan_interval: 5s
  store_outage_grace: 2m
  cancel_grace: 15s
  orphan_grace: 30s
  restart_backoff: [1s, 2s, 5s, 10s, 30s, 1m]
  max_restart_backoff: 5m
  completion_outbox_interval: 1s

parent_supervision:
  graph_enabled: true
  inject_preflight: true
  completion_delivery: mailbox_batch
  critical_delivery: lifecycle_inbox
  critical_resume_mode: schedule_turn   # mailbox_only | schedule_turn
  critical_resume_when: parent_idle
  debounce: 2s
  batch_window: 5s
  max_auto_turns_per_window: 2
  auto_turn_window: 10m
  unresolved_digest_limit: 50
  notification_retention: 30d

agents:
  execution_timeout: 30m
  progress_timeout: 5m
  approval_timeout: 1h
  allow_unbounded: false
  retry:
    max_attempts: 1
    retry_read_only_orphan: true
    retry_write_tasks: false

teams:
  orchestrator:
    owner_lease_ttl: 30s
    renewal_interval: 10s
    reconcile_interval: 5s
  tasks:
    lease_ttl: 2m
    renewal_interval: 30s
    heartbeat_timeout: 90s
    progress_timeout: 10m
    cancel_grace: 15s
    max_attempts: 2
```

配置原则：

1. `observe` 模式只记录 health decision，不 cancel/retry；durable notification、snapshot 和 preflight 仍应工作，否则无法验证父控制面。
2. `enforce` 模式才执行 cancel/retry/reclaim 等状态迁移；critical wake 可用独立开关灰度。
3. 显式 per-run 参数优先于 profile，再优先于全局默认。
4. 所有生效值必须写入 execution record，避免配置变化后无法解释历史执行。
5. `allow_unbounded=false` 时，`timeout=0` 应被解析为 operator 默认，而不是永久运行。
6. 测试环境可以使用更短 interval，但生产默认不应因高频扫表造成 SQLite竞争。
7. operator 可以禁用 `critical_resume_mode=schedule_turn`，但不得禁用 durable lifecycle inbox 和父 turn preflight。
8. notification retention 到期前，未解决 critical 项不得物理删除；应归档或升级 operator。

#### 数据保留与清理（retention / GC）

本文新增的持久表（execution_runs、graph edges、attempts、actions、audit、outbox、notifications）都必须有保留与清理策略，否则 SQLite 长期运行会无限膨胀：

| 数据 | 默认保留 | 清理规则 |
|---|---|---|
| lifecycle notification | 30d（已配置） | 未解决 critical 项到期前归档并升级 operator，不得物理删除 |
| execution_runs（terminal） | 30d | 到期后归档；被 graph edge 引用的 run 随 edge 一起归档 |
| descendant graph edges | 30d | `closed` 的 edge 到期删除；`active` 的 edge 不删除（受父/子 run 生命周期约束） |
| completion outbox | 7d（delivered 后） | delivered 且已确认 parent mailbox seq 后清理；pending 不清理 |
| action records | 90d | terminal 的 action 到期归档；`executing/partially_completed` 不清理（需先到安全终态） |
| supervision audit log | 90d | append-only，到期整体归档（文件/表级），不逐行删除 |
| task attempt（superseded） | 30d | 与 Task 一起归档；late_result_ref 保留 |

通用规则：

1. 所有清理必须按 `root_scope_id` 或 `run_id` 整体进行，禁止只清子表留下孤儿引用。
2. GC 由 Supervisor 的 `retention_interval` tick 触发（建议 1h），与 scan 复用同一 goroutine，避免单独调度器。
3. 清理前检查：相关 notification 是否已 resolved、action 是否 terminal、outbox 是否 delivered；不满足则跳过并记录。
4. 归档使用独立表/文件（`*_archive`），保留可恢复查询路径，不直接删除业务证据。
5. 保留期可通过 `execution_supervisor.retention` 覆盖；未解决 critical 项在 retention 到期前必须升级 operator 而非静默删除。

---

## 11. API、工具与状态兼容

### 11.1 工具参数兼容

现有参数保持不变，新增字段全部 optional：

```text
spawn_agent:
  timeout_sec
  progress_timeout_sec
  approval_timeout_sec
  retry_policy

spawn_team:
  task_timeout_sec（可由每个 task 覆盖）
  task_progress_timeout_sec
  max_task_attempts
  parent_team_id（通常由调用上下文自动填充）

list_agents / team status:
  scope=children|descendants
  after_seq
  health
  include_terminal

supervision_snapshot:
  root_session_id | root_team_id
  scope
  after_seq
  cursor

control_descendant:
  target_kind
  target_id
  action
  expected_version
  cascade

ack_lifecycle:
  notification_ids | through_seq
  decision=acknowledge|defer
  defer_until
```

现有 `timeout` / `timeout_sec` 若已用于模型路由 hint，必须避免直接复用为执行 deadline，除非规范明确。建议在 schema 中分别命名 `routing_timeout_hint_sec` 与 `execution_timeout_sec`，并在兼容层记录来源。

`retry_policy` 参数结构（7.6/7.7 的落点）：

```yaml
retry_policy:
  max_attempts: 1              # 总尝试次数（含首次），0 表示不重试
  retry_classes:               # 允许自动重试的任务类别，见 7.7
    - read_only
    - idempotent
    - isolated_workspace
  backoff: [1s, 5s, 30s]       # 重试间隔；耗尽后按最后一项
  retry_on_reason:             # 仅这些失败原因可自动重试
    - owner_lease_expired
    - provider_transient_error
    - progress_timeout          # 仅 read_only 类
  never_retry_on_reason:        # 显式禁止清单，优先级高于 retry_on_reason
    - approval_timeout
    - invalid
    - external_side_effect_unknown
```

未配置 `retry_policy` 时使用第 10 章 `agents.retry` 全局默认；`max_attempts` 与 5.3 的 `attempt` 计数一致。

兼容原则：

1. `parent_team_id`、root scope 等关系默认从可信运行上下文写入，不能允许普通模型伪造跨 root 归属。
2. 现有 `close_agent`、Team cancel/reassign 入口继续保留，但内部调用统一 action service 并返回 `action_id`。
3. 老客户端不知道 lifecycle cursor 时仍可读取简化状态；新客户端获得 snapshot sequence、未确认通知数和动作状态。
4. API runtime 与本地 CLI 必须共享 authorization、allowed-actions evaluator、CAS 和 cascade 语义。

### 11.2 状态兼容

外部已有简化状态仍保留：

- Agent：idle/running/waiting/stopped；
- Team：active/paused/done/failed/partially_completed/canceled；
- Task：pending/ready/running/blocked/done/failed/canceled。

Supervisor细状态放在新增字段：

```text
supervision_state
attempt_status
cancel_state
orphan_reason
retry_scheduled
root_scope_id
parent_path
snapshot_seq
recommended_action
allowed_actions
action_required
auto_action
pending_action
unacknowledged_notifications
```

这样不会迫使所有 UI/API 立即理解新状态，同时新排障入口可看到完整语义。

### 11.3 事件建议

建议新增或规范化以下 durable 事件：

```text
execution.started
execution.heartbeat_stale
execution.progress_stale
execution.deadline_exceeded
execution.cancel_requested
execution.cancel_acknowledged
execution.cancel_grace_exceeded
execution.orphaned
execution.superseded
execution.retry_scheduled
execution.completed
completion.delivery.retry
completion.delivered
execution.edge.created
execution.edge.detached
lifecycle.notification.pending
lifecycle.notification.delivered
lifecycle.notification.seen
lifecycle.notification.acknowledged
lifecycle.notification.deferred
lifecycle.resolution.notified
parent.preflight.injected
parent.wake.pending
parent.wake.scheduled
parent.wake.suppressed
descendant.action.requested
descendant.action.completed
descendant.action.partially_completed
descendant.action.failed
team.orchestrator.owner_acquired
team.orchestrator.owner_lost
team.orchestrator.restarted
team.child.attached
team.child.detached
task.lease.renewed
task.lease.renewal_failed
task.reclaim_pending
task.reclaimed
task.late_result_rejected
```

事件 payload 至少包含：

```text
run_id, workflow, root_scope_id, parent_session_id, parent_run_id,
parent_team_id, session_id, team_id, task_id, attempt, event_seq,
notification_id, action_id, fencing_token, reason, timeout_source,
recommended_action, owner_id, trace_id, timestamp
```

---

## 12. 可观测性与运维面

### 12.1 Metrics

建议增加：

```text
execution_supervisor_scan_total
execution_supervisor_scan_duration_seconds
execution_supervisor_decision_total{kind,decision}
execution_running_gauge{kind}
execution_stalled_gauge{kind}
execution_orphaned_total{kind,reason}
execution_cancel_total{kind,source,result}
execution_retry_total{kind,reason}
execution_completion_outbox_pending
execution_completion_delivery_latency_seconds
lifecycle_notification_pending{severity,decision_state}
lifecycle_notification_delivery_latency_seconds{severity}
parent_preflight_injected_total{result}
parent_critical_wake_total{result,reason}
descendant_action_total{action,result}
descendant_graph_edge_gauge{kind,status}
team_orchestrator_active_loops
team_orchestrator_active_teams
team_orchestrator_loop_gap
team_orchestrator_restart_total{reason}
team_task_lease_renew_total{result}
team_task_reclaim_total{reason}
team_task_late_result_rejected_total
```

关键告警：

- active teams > live owner leases；
- active owner leases > local/cluster live loops；
- completion outbox持续积压；
- critical lifecycle notification 长时间未投递或未解决；
- 父会话存在 `wake_pending` 但长时间没有可运行 turn；
- descendant action 长时间停留在 executing / partially_completed；
- graph 出现无 parent、成环或跨 root edge；
- stale execution 数连续增长；
- late result rejected出现；
- 同一 task attempt数超过阈值；
- supervisor scan失败或 store outage超过 grace。

### 12.2 Debug/API/TUI

`/debug`、execution diagnostics 和 `/agents panel` 建议展示：

- Supervisor enabled/mode/last scan/last error；
- child Agent run deadline、heartbeat age、progress age；
- Team Orchestrator owner、lease、last tick、restart count；
- task current attempt、lease、fencing token、retry count；
- cancel/reclaim状态；
- pending completion outbox；
- root scope、parent path、直属 child 与 descendant rollup；
- unresolved critical lifecycle inbox、last seen/ack sequence；
- parent preflight 与 critical wake 的最近调度结果；
- `recommended_action`、`allowed_actions` 与 pending action；
- child Team owner、degraded reason 与 subtree 状态；
- 为什么系统尚未判定超时。

诊断文本必须能明确区分：

```text
wait call timed out; execution healthy and continues
execution deadline exceeded; interrupt requested
owner heartbeat stale; awaiting orphan grace
old task attempt superseded; late result ignored
critical child lifecycle change pending; parent wake scheduled
child Team cancel_subtree partially completed; inspect remaining nodes
runtime auto-cancel is in progress; duplicate cancel is not allowed
```

---

## 13. 分阶段实施计划

### P0. 语义冻结与观测补齐

目标：在不改变现有执行行为前，先统一术语、输出和证据。

实施项：

1. 明确 wait timeout 不等于 execution timeout，并更新工具描述/结果。
2. 在 Agent/Team诊断中展示现有 Session lease、teammate heartbeat、task lease、last event/progress时间。
3. 给 Orchestrator loop 增加 start/stop/error/restart原因事件。
4. 给 `SyncLoops` 增加 active team/live loop gap 统计。
5. 为 child completion增加幂等 delivery key，即使暂未实现完整 outbox。
6. 修正文档中旧的“纯轮询”描述为“事件唤醒 + fallback polling”。

主要文件：

- `backend/internal/toolbroker/broker.go`
- `backend/internal/toolbroker/types.go`
- `backend/internal/api/skills/session_runtime_support.go`
- `backend/cmd/aicli/commands/chat_actor_registry.go`
- `backend/internal/api/skills/team_lifecycle.go`
- `backend/cmd/aicli/commands/chat_team_lifecycle.go`
- `backend/internal/api/skills/execution_diagnostics.go`

验收：

- 不改变默认 cancel/retry行为；
- wait timeout输出明确说明 execution是否继续；
- 可查到每个 active Team是否有 loop；
- API/CLI字段和语义一致。

### P1. Host Team Loop Supervisor

目标：active Team的 Orchestrator loop异常退出后可以自动补位。

实施项：

1. 抽取 API/CLI 共用的 Team loop registry/reconciler。
2. 增加常驻 reconcile ticker与立即 wake。
3. 增加 restart backoff和抖动。
4. 记录 last exit error、restart count、next restart at。
5. host shutdown 确保 ticker、loop和watcher顺序退出。
6. 单进程先实现，保留 owner lease接口。

验收：

- 注入 Orchestrator错误后，active Team无需外部调用即可恢复 loop；
- paused/terminal Team不会被重启；
- 同一 Team在单 host内始终最多一个有效 loop；
- 无 goroutine/ticker泄漏。

### P2. Parent/Lead Supervision Control Plane

目标：即使父 Agent / Team Lead 未主动调用 wait，也能可靠感知所有 child / descendant 变化，并可安全执行控制动作。

实施项：

1. 建立 Agent / Team 共用的 durable descendant graph，记录 root scope、parent edge 与 child Team 关系。
2. 实现 supervision snapshot / status rollup，统一返回 health、deadline、heartbeat/progress age、异常原因、`recommended_action` 和 `allowed_actions`。
3. 实现 critical lifecycle inbox、幂等 notification、seen/ack/defer/resolution sequence。
4. 为父 Session 与 Team Lead Session 增加 turn preflight hook，自动注入未解决 lifecycle digest。
5. 实现 ordinary completion mailbox batch 与 critical lifecycle wake 分级策略。
6. 实现 durable action service，统一承接 cancel、close、cancel_subtree、retry、reassign 及动作结果跟踪。
7. 为 child Team 增加 parent Team edge、Lead rollup、Lead 迁移和 terminal 前 descendant reconcile。
8. API 与 CLI 共用 scope authorization、CAS、allowed-actions evaluator 和 cascade 语义。

验收：

- 父 Agent 不调用 `wait_agent`，下一次 turn 仍会看到 timeout/orphan/invalid 摘要。
- 父会话 idle 时 critical event 能按 debounce 调度一个父 turn；busy/waiting approval 时 notification 和 `wake_pending` 不丢失。
- 多个 child 同时异常只产生一个聚合 digest，且可通过 cursor 获取完整明细。
- 主 Agent 可 cancel/close child Agent；主 Team Lead 可 cancel child Team 或整个 subtree。
- `seen` 不等于 `acknowledged`；ack 后不重复注入已解决项，defer 到期后重新进入摘要。
- runtime 自动 cancel/fence 后，父快照显示动作状态并禁止不安全的重复动作。
- Lead 更换或 runtime 重启后，未确认 notification、pending action 与 descendant graph 均可恢复。

### P3. Child Agent Execution Supervisor

目标：普通 `spawn_agent` 具备真正 execution deadline、progress timeout和可靠 completion投递。

实施项：

1. 为每次 child run生成 durable `run_id`。
2. 保存 deadline、heartbeat、progress、cancel state。
3. 将 Agent loop/tool/session事件映射为 progress sequence。
4. 实现 observe-only decision扫描。
5. 实现 interrupt + cancel grace。
6. 实现 terminal completion outbox与幂等父 mailbox投递。
7. 将 deadline、stalled、orphaned、terminal 变化投影到统一 lifecycle inbox。

验收：

- blocking Provider在 execution deadline后被 interrupt；
- `wait_agent` 超时不会取消 child；
- runtime在 child终态和父 mailbox投递之间崩溃，重启后仍能补投；
- waiting approval不被普通 progress timeout误杀；
- completion只投递一次。

### P4. Team Task Lease Renewal 与 Fencing

目标：正常长任务持续续租，stale task安全回收，旧 attempt无法覆盖新 attempt。

实施项：

1. 增加 task attempt durable record。
2. claim时生成 attempt和 fencing token。
3. TeammateRunner统一 heartbeat + task lease + path claim renewal。
4. terminal outcome、claim release、artifact/apply路径校验 fencing token。
5. late result写审计记录并拒绝更新 Task。
6. 将现有 `LeaseManager.ReclaimExpired` 改为 `reclaim_pending -> cancel -> retry` 状态机。
7. 让 `SweepTeammates` 与 Supervisor共用 health evaluator。

验收：

- 正常运行超过旧 10 分钟边界的 task不会被重分配；
- 停止 renewal后，task在预期时间内被回收；
- 旧 attempt晚到结果不会覆盖新 attempt；
- path claims不会被旧 attempt续租或释放；
- SQLite lock短暂发生时不会误回收健康任务。

### P5. Durable Orchestrator Ownership

目标：支持 runtime重启及未来多实例下的单 Team有效调度 owner。

实施项：

1. 增加 Team Orchestrator owner lease与 fencing token。
2. claim task时校验 Team owner token。
3. owner续租失败时停止新 claim。
4. 新 owner接管后恢复 active task健康判断、completion outbox与terminal reconcile。
5. 加入两个 runtime实例竞争同一 Team的集成测试。

验收：

- 两个 host同时运行时，同一 Team只有一个有效调度 owner；
- owner进程崩溃后，另一实例在 lease TTL + grace内接管；
- 旧 owner恢复后不能继续 claim或提交有效调度状态；
- Team terminal/summary不重复发布。

### P6. Wait/Event 与运维体验优化

目标：减少短周期 polling，提供可解释的监督状态。

实施项：

1. `wait_team` 接入 Team event/task wake sequence。
2. `wait_agent` 输出 execution run监督字段。
3. `/debug`、diagnostics、TUI展示 deadline/heartbeat/progress/attempt。
4. 增加 supervisor告警与runbook。
5. 完善 supervision snapshot cursor、preflight digest、critical wake 和 action 诊断。

验收：

- wait调用在事件到达后快速返回；
- watcher丢事件时可按 durable sequence catch-up；
- operator能从单一视图判断“仍健康、等待审批、无进展、owner丢失、正在回收”；
- wait timeout与execution timeout在UI和JSON中无歧义。

---

## 14. 测试计划

### 14.1 单元测试矩阵

#### Child Agent

- `wait_agent` timeout不取消运行。
- execution deadline触发 interrupt并产生 `timed_out`。
- progress更新会推迟 progress timeout，但不会推迟 execution deadline。
- heartbeat新鲜、progress stale触发 stalled。
- Session lease stale触发 orphan判断。
- waiting approval/input使用独立 deadline。
- completion outbox重复派发幂等。
- child terminal后父 mailbox写入失败，重试成功。

#### Parent/Lead Control Plane

- descendant graph 正确返回直属 child、全部 descendant 与 parent path，并拒绝成环或跨 root edge。
- 多 store 聚合返回稳定 `snapshot_seq`，分页/cursor 不重复或遗漏状态变化。
- timeout、stalled、orphaned、invalid 生成幂等 critical notification。
- 父 turn preflight 自动注入未解决摘要，`seen` 不会错误推进为 `acknowledged`。
- acknowledge 后已解决项不重复注入；defer 到期后重新进入 action-required。
- 父会话 busy、waiting approval/input、compact 或 runtime 重启时，`wake_pending` 不丢失。
- 同一 root 多个 critical event 在 debounce 窗口内只调度一次父 turn。
- `allowed_actions` 随状态、fencing、side-effect class 和调用者 scope 正确变化。
- cancel/close/retry/reassign 使用 expected version；并发动作只有一个 CAS 成功。
- cancel_subtree 阻止新 spawn，由叶到根推进，并能返回 partially completed。
- child Team Lead 切换后，notification cursor 和 pending action 正确接管。

#### Team Orchestrator

- active Team缺 loop时自动启动。
- loop错误退出后按 backoff重启。
- paused/terminal Team停止 loop。
- owner lease丢失后旧 loop停止 claim。
- mailbox/task wake与fallback tick都能推进任务。

#### Team Task

- renewal同时更新 teammate heartbeat、attempt heartbeat、task lease和claims。
- healthy heartbeat + progress不会被 reclaim。
- lease expired但 owner/progress新鲜时先修复而非重复执行。
- stale owner进入 reclaim_pending并收到 cancel。
- cancel grace后创建新 attempt。
- fencing token mismatch拒绝 terminal outcome。
- late result只写审计，不修改Task。
- retry max attempts耗尽后进入可解释 failed/blocked终态。

### 14.2 并发与故障注入

- 两个 Supervisor同时扫描同一执行，只允许一个CAS成功。
- 两个 Orchestrator竞争同一 Team owner lease。
- Store短暂 lock、写失败、watch channel关闭。
- runtime在以下位置崩溃：
  - child terminal前；
  - terminal后、completion outbox前；
  - outbox写入后、parent mailbox前；
  - lifecycle event后、notification row写入前；
  - notification写入后、父 preflight / wake前；
  - action accepted后、cancel_subtree第一个节点前；
  - subtree部分节点关闭后、action terminal前；
  - task fencing提升后、旧 actor cancel前；
  - 新 attempt创建后、旧 attempt晚到结果前；
  - Team terminal后、summary前。
- 两个控制面 dispatcher 同时投递同一 notification，父侧只看到一个业务项。
- 父 turn 与 critical wake 并发创建，只允许一个有效 turn，另一请求保持去重或 pending。
- Team Lead 切换与 child Team 异常同时发生，通知必须到达新 Lead 或 root parent。
- interrupt无响应、Provider忽略ctx、Tool不返回。
- host时钟轻微偏差；尽量使用数据库/CAS version避免仅靠本地时钟排序。

### 14.3 集成测试

1. `spawn_agent -> wait_agent timed_out -> child继续 -> completion补投`。
2. 多 child并行，其中一个blocked、一个timeout、一个成功，父 mailbox收到三条唯一终态。
3. `spawn_team(auto_start=true)` 后销毁本地 loop，Supervisor自动恢复并完成 Team。
4. 正常长 task持续续租超过多个TTL。
5. 模拟 teammate进程崩溃，旧 attempt被cancel/fence，新 attempt完成。
6. runtime重启后恢复 active Team owner、pending outbox和orphan child判断。
7. 双实例共享SQLite，验证 owner lease与fencing。
8. `wait_team` 在等待调用超时后 Team仍继续，后续再次wait获得terminal summary。
9. 父 Agent从未调用 `wait_agent`，child timeout后其下一次自然 turn仍自动收到 lifecycle digest。
10. 父会话 idle 时多个 critical child异常触发一个聚合监督 turn；父会话 busy/waiting approval时延迟但不丢失。
11. 父 Agent通过 control action取消并关闭 child，随后收到 resolution notification。
12. 主 Team创建嵌套 child Team，Lead snapshot可看到完整层级并成功 cancel child Team/subtree。
13. ack 后通知不再重复注入；defer 到期后重新出现；自动恢复后收到 resolved 摘要。
14. subtree cancel中一个节点失败，动作返回 partially completed，重启后继续剩余节点。

### 14.4 真实 Provider / 终端验证

在现有 multi-agent真实终端runbook基础上增加：

- 一个明显超过 wait timeout但未超过execution deadline的 child；
- 一个短 execution deadline child；
- 一个需要approval的 child；
- 一个正常长 Team task，观察续租；
- 人工终止 teammate/runtime进程并确认接管；
- 不调用 wait，确认父 turn preflight仍显示 child关键异常；
- idle父会话被critical lifecycle event受控唤醒，busy父会话只记录wake pending；
- 从Team Lead执行child Team/subtree cancel并观察逐节点动作结果；
- Ctrl+C 与Supervisor cancel来源区分；
- Windows Terminal/TUI中显示健康、stalled、canceling、reclaimed、unacknowledged和action-required状态。

### 14.5 性能与容量测试

监督体系自身不能成为性能瓶颈。建议加入以下容量/性能验证：

1. **规模上限**：单 root scope 下 100/500/1000 个 child 时，`supervision_snapshot` 全量 vs 分页读取的延迟；确认默认优先返回变化项/异常项（6.2 规则 7）不随健康 child 数线性增长。
2. **扫描负载**：`execution_supervisor.scan_interval=5s` 时对 SQLite 的写放大（WAL 大小、锁等待次数）；验证与生产默认高频扫表之间留有安全余量（10 章原则 6）。
3. **通知风暴**：同一 root 100 个 child 同时异常时，debounce/batch 后只产生 1 个聚合 digest 与 1 次 wake；inbox 写放大与 preflight 注入耗时。
4. **digest 预算**：`unresolved_digest_limit=50` 时 digest 的 token 估算，确认不挤占上下文预算；超限后 cursor 分页路径的可用性。
5. **并发 CAS 压力**：多 Supervisor/Orchestrator 竞争同一批 task 的 CAS 冲突率，确认 conflict 路径不会造成活锁（配合 14.2）。
6. **GC 影响**：retention tick 清理大量过期行时对正常 scan 的延迟影响；确认按批删除且可中断。
7. **wait 快速返回**：`wait_agent`/`wait_team` 在事件驱动路径下的平均/95 分位返回延迟，验证 P6 目标。

性能指标纳入 P3–P6 各自验收项；不达标时优先调低 scan 频率或开启批量读，而不是移除监督语义。

---

## 15. 数据迁移与灰度策略

### 15.1 向后兼容

1. 旧 Session/Task没有 execution/attempt记录时，首次观察时创建 projection，标记 `legacy_imported=true`。
2. 旧 running task无 fencing token时，禁止自动 enforce reclaim，先进入 observe/needs_review。
3. 新字段均允许为空，旧API响应保持兼容。
4. 旧 AgentControl/task状态继续可读，execution record作为增强权威逐步接管。

### 15.2 灰度顺序

#### 阶段 A：observe

- 只计算健康 decision；
- 不 interrupt、不 reclaim、不 retry；
- 建立 descendant graph 和 snapshot projection；
- lifecycle notification、父 turn preflight 与 acknowledgement 先启用，但 critical wake 只记录“本应调度”；
- 对比现有 task lease回收结果；
- 记录潜在误判。

#### 阶段 B：parent visibility 与 completion reliability

- 启用 completion outbox；
- 启用 critical lifecycle inbox、preflight digest 和 resolution notification；
- 启用显式 inspect/ack/defer；变更型 control action 仍为 dry-run；
- 启用 Team loop自动补位；
- 不自动cancel业务执行。

#### 阶段 C：critical wake 与 read-only enforcement

- 对 idle 父会话启用 debounce 后的 critical wake；busy/waiting approval 使用 durable wake pending；
- 对 read-only child/task启用deadline、cancel、有限retry；
- 启用 cancel/close 和只读 subtree 的 action service；
- 写任务仍observe。

#### 阶段 D：fenced task enforcement

- task attempt/fencing完整上线后启用自动reclaim；
- 对main workspace与外部副作用任务保持保守策略。

#### 阶段 E：multi-instance ownership

- 启用 durable Team owner lease；
- 启用 child Team subtree control、Lead切换接管和跨实例 notification/action dispatcher；
- 通过双实例测试后再允许水平扩展。

### 15.3 回滚

- Supervisor支持运行时切回 `observe`。
- 关闭enforce不得停止现有执行，只停止新的自动cancel/retry。
- Fencing一旦启用不可通过回滚恢复旧 attempt写权限；旧attempt继续视为superseded。
- completion outbox可独立保留，即使其他Supervisor动作回滚。
- 关闭 critical auto wake 只停止新自动 turn，不删除 lifecycle inbox、preflight 或 pending notification。
- control plane回滚不得删除 descendant edge、ack cursor或已接受 action；未完成 action须继续到安全终态或明确标记需要人工接管。

### 15.4 存储与索引设计

新增表需要明确的 DDL 与索引，否则按 root_scope/status/deadline 的扫描查询会在数据量增长后退化：

```text
execution_runs：
  主键 run_id
  索引 (root_scope_id, status)
  索引 (status, execution_deadline_at)         -- Supervisor 超时扫描
  索引 (status, progress_deadline_at)          -- progress stale 扫描
  索引 (parent_session_id, parent_run_id)      -- descendant 查询
  索引 (fencing_token)                          -- 校验与诊断

descendant_edges：
  主键 edge_id
  索引 (parent_id, parent_kind)
  索引 (root_scope_id, status)                  -- rollup 与清理
  唯一索引 (parent_id, child_id, relation)      -- 防止重复 edge

lifecycle_notifications：
  主键 notification_id
  索引 (root_scope_id, decision_state, created_at)
  索引 (subject_id, subject_version)            -- 幂等键去重
  索引 (target_parent_session_id, delivery_state)

completion_outbox：
  主键 outbox_id（或 idempotency_key 唯一）
  索引 (delivery_state, created_at)             -- 补投扫描

task_attempts：
  主键 (task_id, attempt)
  索引 (status, lease_until)                    -- reclaim 扫描
  索引 (fencing_token)

supervision_audit_log：
  主键 audit_id
  索引 (run_id, created_at)
  索引 (root_scope_id, created_at)              -- retention 归档
```

约束：

1. 迁移使用与现有 store 相同的 SQLite 迁移版本机制（`user_version` 或等效），禁止隐式建表。
2. 所有时间列统一 UTC 存储；`lease_until`/deadline 比较使用数据库时间或传入的 `asOf`，避免 host 时钟偏差（14.2 原则）。
3. 索引数量以实际查询模式为准，P2/P3 联调后按 14.5 性能测试结果增删；不预先建冗余索引。
4. 双实例共享 SQLite 时（P5），关键写入（CAS、fencing、owner lease）必须是单条条件 UPDATE，不能拆成 read-modify-write。

---

## 16. 安全与一致性约束

1. **至少一次投递，至多一次业务生效。** mailbox/event可重试，业务terminal通过幂等键/CAS保证一次生效。
2. **Lease必须配合 fencing。** 过期时间本身不能阻止旧执行晚到写入。
3. **先 fence，后重试。** 新 attempt启动前必须使旧 attempt失去写权限。
4. **先请求取消，再回收资源。** 直接清数据库状态不能替代停止旧执行。
5. **Progress不可由Supervisor伪造。** 只有执行路径产生的有意义事件才更新progress。
6. **等待者不是owner。** wait context取消不得传播为child/team cancel。
7. **副作用任务默认不自动retry。** 除非工具/任务声明幂等性或运行在隔离工作区。
8. **普通完成不等于自动唤醒。** completion默认批量进入mailbox；critical lifecycle wake单独配置、去重、限流，并在父会话idle时默认调度。
9. **通知可见不等于风险已接受。** delivered/seen/acknowledged/actioned/resolved必须分开，preflight注入不能自动ack。
10. **控制权限跟随root scope。** descendant graph查询与动作必须鉴权、防环、校验expected version和fencing token。
11. **主控制动作持久化。** cancel/close/subtree/retry/reassign必须有action record、逐节点结果和resolution notification。
12. **状态迁移可审计。** 每次timeout/cancel/orphan/reclaim/retry/ack/defer/wake包含actor、reason、old/new version和时间。
13. **终态不可被旧attempt反转。** Team/Task已terminal时，late result只能作为附加审计证据。

---

## 17. 实施文件建议

以下是建议修改或新增的代码范围，最终以实施时源码确认结果为准：

### 复用/扩展

- `backend/internal/agentcontrol/`
  - execution registry、descendant graph、lifecycle inbox、CAS、wake、fencing与action接口。
- `backend/internal/chat/session_runtime_store.go`
  - Session run ownership/heartbeat读取与诊断。
- `backend/internal/chat/actor.go`
  - run_id、progress事件、interrupt acknowledgement、parent turn preflight hook。
- `backend/internal/api/skills/session_runtime_support.go`
  - child run/parent edge登记、completion outbox、snapshot、control action、wait输出。
- `backend/cmd/aicli/commands/chat_actor_registry.go`
  - 本地CLI对等graph、snapshot与action实现。
- `backend/internal/team/orchestrator.go`
  - owner token校验、task attempt、reclaim状态机。
- `backend/internal/team/teammate_runner.go`
  - heartbeat/lease/claim统一renewal。
- `backend/internal/team/lease.go`
  - 从直接retry改为安全reclaim。
- `backend/internal/api/skills/team_lifecycle.go`
  - loop reconciler/restart/backoff、parent Team edge与Lead notification路由。
- `backend/cmd/aicli/commands/chat_team_lifecycle.go`
  - 本地CLI对等实现。
- `backend/internal/toolbroker/broker.go`
  - wait、supervision snapshot、ack/control tool schema与结果语义。
- `backend/internal/api/skills/execution_diagnostics.go`
  - Supervisor诊断。

### 建议新增

```text
backend/internal/execution/supervisor.go
backend/internal/execution/types.go
backend/internal/execution/policy.go
backend/internal/execution/store.go
backend/internal/execution/outbox.go
backend/internal/execution/health.go
backend/internal/execution/supervisor_test.go
backend/internal/execution/graph.go
backend/internal/execution/snapshot.go
backend/internal/execution/lifecycle_inbox.go
backend/internal/execution/action_service.go
backend/internal/execution/parent_preflight.go
backend/internal/execution/wake_scheduler.go

backend/internal/team/orchestrator_lease.go
backend/internal/team/task_attempt.go
backend/internal/team/task_health.go
backend/internal/team/child_team_rollup.go
```

如果不希望新增顶层 `execution` package，也可以先在 `agentcontrol` 中实现通用execution substrate，但应避免把Supervisor逻辑散落在API handler与CLI command中形成第三套分叉实现。

注：`backend/internal/execution/` 包**已存在** `timeout.go`（`TimeoutSource`、`TimeoutBudget`、`WithTimeoutSource`）。上表新增文件是该包的扩展；实施时应先复用其 deadline 来源语义与测试（`timeout_test.go`），不重复定义。

---

## 18. 完成标准

本方案完成后，必须满足：

1. 普通 `spawn_agent` 即使父 Agent不调用wait，也会被基础设施按deadline/progress/orphan策略监督。
2. 所有 child Agent / Team 都有 durable parent edge；主 Agent / Team可查询直属 child 与完整 descendant graph。
3. 主控制面可一次读取健康度、deadline、heartbeat/progress age、异常原因、`recommended_action` 和 `allowed_actions`。
4. 父 Agent不调用wait时，下一次父 turn仍会通过preflight自动看到未解决critical lifecycle摘要。
5. timeout、stalled、orphaned、invalid和自动动作失败按策略写critical inbox并可受控唤醒idle父/Lead turn；busy或waiting approval时不丢失。
6. 主 Agent可cancel/close child Agent；主 Team可cancel/close child Team或subtree，并得到durable逐节点动作结果。
7. lifecycle通知完成`detected -> delivered/seen -> acknowledged/deferred/actioned -> closed/recovered -> resolution_notified`闭环。
8. ack后已解决通知不重复注入，defer到期重新出现；seen绝不自动等于acknowledged。
9. `wait_agent` / `wait_team` 超时只影响等待，并明确返回“执行是否继续”。
10. 每个 active Team都有有效Orchestrator owner；loop退出能自动恢复。
11. 正常长Team task通过renewal持续持有lease，不被误回收。
12. stale task回收前完成fencing和cancel，旧attempt晚到结果不能生效。
13. runtime重启或Lead切换后能恢复active Team、descendant graph、pending completion、lifecycle notification、wake和action。
14. approval/input等待使用独立deadline，不被普通progress timeout误杀。
15. observe/enforce/critical-wake灰度可控，副作用任务默认不自动retry。
16. API runtime与本地CLI通过同一测试矩阵，状态、权限和恢复语义一致。
17. 核心并发、故障注入、runtime重启、双实例与真实Provider验证均通过。
18. `/debug`、diagnostics或TUI能解释每个执行为何仍在运行、由谁监督、父控制面是否已感知、下一步可执行什么动作。

---

## 19. 建议优先级

建议按以下顺序推进：

1. **P0观测和语义冻结**：最低风险，先消除wait/execute timeout歧义。
2. **P1 Team loop自动补位**：复用现有`SyncLoops`，收益高且改动相对独立。
3. **P2 Parent/Lead监督控制面**：先打通descendant graph、snapshot、lifecycle inbox、preflight和控制动作，避免监督结果只存在后台。
4. **P3 child deadline + completion outbox**：解决普通`spawn_agent`永久不返回和完成丢失，并把异常投影给父控制面。
5. **P4 task renewal + fencing**：解决Team长任务误回收与重复执行，是一致性核心。
6. **P5 durable Team owner lease**：为重启恢复、多实例和child Team稳定rollup做准备。
7. **P6 wait/event与运维体验**：在状态权威稳定后降低polling并完善诊断。

其中 P4 的 fencing 完成前，不应把现有 lease reclaim 扩展为激进自动重试；P2 的通知与preflight可以先上线，但写任务subtree cancel/retry仍需等待P4安全门禁。

---

## 20. 最终目标状态

目标不是让主 Agent不断询问所有子 Agent，也不是让 runtime在后台静默处理一切，而是形成“基础设施检测 + 主控制面感知 + 主 Agent / Team决策与控制”的闭环：

```text
主 Agent / Team Lead
       ├─ spawn_agent / spawn_team
       ├─ supervision snapshot：直属child与全部descendant状态
       ├─ preflight digest：每个turn自动感知未解决变化
       ├─ critical wake：关键异常时受控调度决策turn
       └─ control action：cancel/close/subtree/retry/reassign/ack/defer
                    │
Parent/Lead Supervision Control Plane
       ├─ durable descendant graph与状态rollup
       ├─ lifecycle inbox、ack cursor与resolution
       ├─ recommended/allowed actions
       └─ durable action orchestration
                    │
Runtime基础设施持续监督
       ├─ heartbeat：owner是否存活
       ├─ progress：执行是否前进
       ├─ deadline：是否超过业务上限
       ├─ lease + fencing：谁有权提交结果
       ├─ cancel/retry/reconcile：按安全策略先行止损
       └─ outbox：completion与关键状态可靠送达

wait_agent / wait_team只负责等待观察，不承担保活和恢复；
父/Lead即使不调用wait，也通过preflight、critical inbox和wake获得状态
```

最终应实现：**runtime主动巡检、主控制面可靠汇总与推送、主 Agent / Team可查询可决策可控制；事件驱动优先、低频扫描兜底、持久状态为权威、等待与执行解耦、回收前先fence与cancel、所有通知和恢复动作可确认且可审计。**

---

## 21. 开放问题与待决事项

以下问题在实施启动前或相应阶段开始时必须决策，避免被当作"已设计"直接编码：

| # | 开放问题 | 影响阶段 | 建议决策时限 | 备注 |
|---|---|---|---|---|
| 1 | fencing token 分配序列：单机 SQLite 用 `MAX+1` 还是独立 sequence 表；是否需要预留跨进程（P5）扩展 | P4 | 实施 P4 前 | 5.4 落地规则的细化 |
| 2 | `execution_runs` 与 AgentControl registry / Team store 的关系：是独立新表还是 registry 上投影 | P2/P3 | 实施 P2 前 | 5.3 已列两个候选，需定稿 |
| 3 | progress 事件最小粒度：是否在 `loop.go` 每一步都写库，还是内存聚合后按 interval 落盘 | P3 | 实施 P3 前 | 影响 14.5 写放大 |
| 4 | 工具幂等声明的 schema 归属：toolbroker types 还是 provider adapter 层 | P3（retry） | 实施 P3 retry 前 | 7.7 需与工具 schema 规范对齐 |
| 5 | critical wake 在 waiting approval 父会话上的默认行为：推迟到 approval 结束后立即触发，还是只写 wake_pending | P2 | 实施 P2 前 | 6.5 规则 3 的两个选项 |
| 6 | `close` 与 `cancel_subtree` 的默认 cascade 深度：默认 descendants 还是显式指定 | P2 | 实施 P2 前 | 6.6 动作语义细化 |
| 7 | child Team 的 Lead 切换与 parent Team terminal 的优先级：parent 终态是否强制要求 child 收敛 | P2/P5 | 实施 P2 child Team 前 | 6.7 规则 5 的落点 |
| 8 | 审计日志量级：是否所有 observe 判定都写审计，还是仅写状态迁移动作（5.6 建议后者） | P0 | 实施 P0 前 | 影响 14.5 写放大与 retention |
| 9 | SQLite 迁移版本机制与多实例并发迁移的锁策略 | P2 | 实施 P2 前 | 15.4 约束 1 的细化 |
| 10 | `pause_max_duration` 默认值与 operator 升级通道 | P1 | 实施 P1 后、enforce 前 | 8.1 paused 生命周期 |

处理规则：

- 每项决策后回写本文对应小节，保持文档为唯一权威；
- 未决策项不得进入 enforce 灰度；observe 模式可先行验证但需在验收中标注 open。
