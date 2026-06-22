# spawn_team Teammate-Level Provider/Model Routing 详细方案

更新时间: 2026-06-22

关联父方案: [task-difficulty-model-routing-plan.md](./task-difficulty-model-routing-plan.md)

## 1. 结论

`task-difficulty-model-routing-plan.md` 已经完成了全局任务难度路由模型、配置结构、resolver、`spawn_subagents` 和 `spawn_agent` 路径的主体设计与实现边界说明。但它对 `spawn_team` 只给出了方向性结论：第一阶段只记录 task difficulty metadata，不立即把 teammate 执行切到不同 provider/model/reasoning effort。

本文件补齐 `spawn_team` 这条执行面的详细工程方案。核心结论如下：

1. `spawn_team` 的 provider/model 路由应按 **task execution** 粒度解析，而不是永久绑定到 teammate。
2. route decision 应在 teammate runner 已经 claim task、即将触发 teammate session 执行前解析。
3. route decision 应绑定到 `team_id + task_id + attempt/version`，并写入 AgentControl task projection 与 runtime events。
4. 第一版复用现有 `modelrouting.Resolver` 和已注册 `LLMRuntime` provider，不新增 provider group / failover group。
5. `routing.enabled=false` 时必须保持旧行为：不改变 teammate session provider/model/reasoning，只保留已有 difficulty metadata。
6. `spawn_team` teammate-level 路由必须独立实施，不能混进 `spawn_subagents` scheduler 或 `spawn_agent` session controller 的现有实现。

## 2. 当前代码事实

### 2.1 已具备的基础

当前代码已经具备以下前置能力：

- `backend/internal/modelrouting` 已有 difficulty normalizer、route resolver、provider catalog、capability/fallback 策略。
- `backend/internal/agentconfig/config.go` 已有 `aicli.subagents.routing` 配置结构。
- `backend/internal/toolbroker/types.go` 的 `SpawnTaskSpec` 已有 `difficulty` 和 `difficulty_rationale`。
- `backend/internal/toolbroker/broker.go` 的 `spawn_team.tasks[]` 解码已校验 task difficulty。
- `backend/internal/team/types.go` 的 `Task` 已有 `Difficulty` 和 `DifficultyRationale`。
- `backend/internal/team/agent_task_registry.go` 已通过 AgentControl task seam 创建、更新、claim、release team tasks。
- `backend/internal/team/agent_projection.go` 已能把 team task 投影成 AgentControl task record。
- `backend/internal/agentcontrol` 的 agent registry 已有 provider/model/reasoning/difficulty/route metadata 字段。
- `backend/internal/api/skills/session_runtime_support.go` 已为 `spawn_agent` child session 接入 route resolver，并能把 route context 写入 session/AgentControl agent record。
- `backend/internal/team/teammate_runner.go` 的 `TaskTriggerRequest` 已携带 task difficulty，但尚未携带 resolved route decision。
- `backend/internal/team/teammate_runner.go` 当前为 teammate task run 设置 `RunMeta.PermissionMode="bypass_permissions"`；`backend/internal/agent/loop.go` 通过 run meta context 读取 permission mode；`backend/internal/team/task_dispatch_event.go` 已会把 permission mode 写入 dispatch audit payload。

### 2.2 当前缺口

当前 `spawn_team` teammate 执行链路大致是：

```text
spawn_team
  -> create team / teammates / tasks
  -> orchestrator marks ready tasks
  -> ClaimReadyTasks assigns task to teammate
  -> TeammateRunner.StartTask(team, mate, task)
  -> build task prompt
  -> trigger TaskTriggerClient or SessionClient
  -> teammate session executes with its existing provider/model/reasoning
  -> task outcome is reported and released
```

缺口是：

```text
task.Difficulty exists
  but
TeammateRunner does not resolve modelrouting.RouteDecision
  and
TaskTriggerRequest / session execution does not apply provider/model/reasoning overrides
```

也就是说，`spawn_team` 当前只记录 difficulty，不真正让 teammate 的请求走 difficulty route。

## 3. 目标

1. 让 `spawn_team` task difficulty 参与 provider/model/reasoning routing。
2. 让 teammate 执行某个 task 时使用该 task 的 resolved route decision。
3. route decision 可追踪、可恢复、可审计、可回滚。
4. 复用现有 `modelrouting.Resolver`，避免复制一套 team 专用路由逻辑。
5. `routing.enabled=false` 或未配置 routing 时保持当前 team 执行行为。
6. 支持 permissive fallback 和 strict failure 两种策略，语义与现有 resolver 对齐。
7. 将 route metadata 传播到 AgentControl task、team lifecycle event、mailbox/collab UI 和 debug/doctor dry-run。

## 4. 非目标

第一版不做以下事项：

- 不做 provider group、failover group、weighted provider routing。
- 不自动写回用户配置。
- 不为每个 teammate 创建独立 `LLMRuntime`。
- 不改变 team task dependency、lease、path claim、retry 的核心语义。
- 不让 prompt 或 task payload 自行提升到未经本地策略允许的 provider/model。
- 不强制 teammate lifetime 只能使用一个 provider/model。
- 不改变 teammate 的 `PermissionMode`、工具审批、shell/tool permission、workspace/path claim 或 mailbox ACL。
- 不把 lead session 的模型切换语义和 teammate task routing 混为一谈。
- 不实现跨进程/跨机器的 expert concurrency 协调。

## 5. 关键设计选择

### 5.1 路由粒度: task execution

选择 **task execution-level routing**：

```text
one route decision per team task execution attempt
```

不选择 teammate-level fixed route，原因：

- `spawn_team` 的工作单元是 task，不是 teammate。
- 同一个 teammate 可能先处理 easy research task，再处理 hard implementation task。
- difficulty 是 task 属性，把 provider/model 永久绑定到 teammate 会让后续任务难度无法生效。
- task-level route 更容易审计，也更容易在 retry 时决定复用或重新解析。

推荐绑定键：

```text
workflow=spawn_team
team_id
task_id
task_version 或 attempt
teammate_id
```

### 5.2 解析时机: claim 后、执行前

route decision 应在 task 已经 claim 到 teammate 后、调用 teammate session 前解析。

推荐流程：

```text
ClaimReadyTasks claims task
  -> assignment contains task + teammate
  -> Orchestrator launches StartTask
  -> TeammateRunner.ResolveRoute(team, mate, task)
  -> route metadata persisted/emitted
  -> trigger teammate session with route provider/model/reasoning
```

不建议在 `spawn_team` 创建任务时解析 route：

- task 可能长期排队，provider config 可能在执行前变更。
- lead planner / task update 可能补充或修改 difficulty。
- task 可能被 retry、reassigned、handoff。
- route 决策应反映执行时的 parent defaults 和 provider catalog。

### 5.3 route decision 是否持久化

第一版建议 **执行前解析并持久化 audit metadata**。

持久化目的不是让旧 task 永久锁死模型，而是让运行中和已完成 task 可解释：

- 这次执行用的 provider/model/reasoning 是什么。
- route source 是 difficulty level、role override、fallback 还是 disabled。
- 是否发生 provider/model/reasoning fallback。
- warning 是什么。

重试策略：

- 对同一 task 的 retry，应重新解析 route。
- 每次 retry 都应保留新的 route decision audit。
- 如果暂时没有 attempt 表，至少在 task graph event 中保留每次执行的 route payload。

### 5.4 session/actor 应用策略

优先采用 **request-level route override**，而不是永久修改 teammate session。

目标执行语义：

```text
teammate session identity/context remains stable
current task execution request uses route provider/model/reasoning
next task can resolve a different route
```

如果当前 actor/session API 只能从 session context 读取 provider/model/reasoning，需要补一个 team task trigger 专用入口，而不是直接把 teammate session 的长期 context 覆盖成当前 task route。否则 hard task 执行后，后续 easy task 可能继续沿用 hard route。

### 5.5 当前代码落点

当前执行链路里，`chat.SubmitPromptOption` 只有 image fields，`chat.SubmitPrompt` command 没有 route override 字段；`SessionActor.runLoop()` 固定用 `agent.NewReActLoop(a.agent, a.llmRuntime, a.loopConfig)`；`ReActLoop` 构造 `LLMRequest` 时 provider/model 来自 `loop.agent.config`，reasoning effort 来自 `loop.config.ReasoningEffort`。

因此 PR-3 不能只扩展 `team.TaskTriggerRequest`。必须增加一个 run-scoped route override 入口：

1. `team.TaskTriggerRequest` 携带 `TaskExecutionRoute`。
2. `sessionActorClient.TriggerTask()` 将 route 转成 `chat.SubmitPromptOption.RouteOverride`。
3. `chat.SubmitPromptOption` 和 `chat.SubmitPrompt` command 保存该 override。
4. `SessionActor.startSessionRun()` 把 override 传到 `runLoop()`。
5. `runLoop()` 克隆当前 `LoopReActConfig`，只在克隆副本上写入 provider/model/reasoning override。
6. `agent.LoopReActConfig` 增加可选 `Provider`、`Model` 字段；构造 `LLMRequest` 和 prompt preflight 时优先使用 loop config override，再 fallback 到 `agent.Config`。

不要在 PR-3 中直接修改 `a.agent.GetConfig().Provider/Model`，也不要把 route 写入全局 session config。这样可以保证同一个 teammate 连续执行 hard/easy task 时不会出现 provider/model 泄漏。

## 6. 数据模型设计

### 6.1 Route metadata 字段

统一使用以下字段名，和 `spawn_subagents` / `spawn_agent` 已有 metadata 对齐：

```text
difficulty
difficulty_source
difficulty_rationale
route_provider
route_model
route_reasoning_effort
route_source
route_warnings
fallback_used
fallback_reason
route_resolved_at
route_attempt
```

### 6.2 Team task 扩展

当前 `team.Task` 已有：

```go
Difficulty          string
DifficultyRationale string
```

建议新增一个独立 route audit 结构，不把 route 字段平铺进 `Task` 作为长期业务属性：

```go
type TaskRouteAudit struct {
    Difficulty          string
    DifficultySource    string
    DifficultyRationale string
    Provider            string
    Model               string
    ReasoningEffort     string
    Source              string
    Warnings            []string
    FallbackUsed        bool
    FallbackReason      string
    ResolvedAt          time.Time
    Attempt             int
}
```

第一版有两个实现选择：

| 方案 | 做法 | 优点 | 风险 |
|---|---|---|---|
| A | 给 team task 表增加 route audit 字段 | 查询简单 | 只保留最近一次 route，历史 attempt 丢失 |
| B | route audit 只写 task graph event / AgentControl event | 保留历史 | 当前 task list 不容易直接展示最新 route |
| C | task 表保留 latest route，task graph event 保留每次 route | 查询和审计兼顾 | 改动略多 |

推荐 **方案 C**。第一版可先实现 A+B 的最小子集：task record 保留 latest route，event 保留每次 execution route。

### 6.3 AgentControl task projection

`spawn_team` task 已经通过 `AgentControlTaskRegistry` 暴露给共享 AgentControl task seam。这里应增加或复用 route metadata 字段：

```text
provider
model
reasoning_effort
difficulty
difficulty_source
difficulty_rationale
route_source
route_warnings
fallback_used
fallback_reason
```

如果 `agentcontrol.TaskRecord` 当前没有 provider/model/reasoning/route 字段，需要添加。不要只把这些字段写到 `agentcontrol.AgentRecord`，因为 teammate-level routing 是 task execution 属性，不是 teammate identity 属性。

当前代码事实：`agentcontrol.TaskRecord` 只有 `Difficulty` / `DifficultyRationale`，`TaskCreateRequest` 和 `TaskUpdateRequest` 也只有 difficulty 字段。PR-1 应同时补齐：

- `TaskRecord` latest route read-model 字段。
- `TaskCreateRequest` 可选 route audit 字段，用于 task 创建后立即投影已有 route metadata 的兼容路径。
- `TaskUpdateRequest` 可选 route audit 字段，用于 route resolved 后更新 latest route。
- 或者新增专用 `TaskRouteAuditUpdateRequest`，避免把 route 审计和普通 task patch 混在一起。

推荐新增专用 update request。原因是 route audit 属于 execution attempt metadata，不应被普通 task title/goal/status patch 意外清空。

### 6.4 TaskTriggerRequest 扩展

当前 `TaskTriggerRequest` 已有：

```go
SessionID
TeamID
AgentID
TaskID
Difficulty
DifficultyRationale
Prompt
RunMeta
```

建议扩展为独立 route DTO，而不是继续在 request 上平铺所有字段：

```go
type TaskExecutionRoute struct {
    Difficulty          string
    DifficultySource    string
    DifficultyRationale string
    Provider            string
    Model               string
    ReasoningEffort     string
    Source              string
    Warnings            []string
    FallbackUsed        bool
    FallbackReason      string
    ResolvedAt          time.Time
    Attempt             int
    Error               string
}
```

`TaskTriggerRequest` 持有该 DTO：

```go
type TaskTriggerRequest struct {
    SessionID           string
    TeamID              string
    AgentID             string
    TaskID              string
    Difficulty          string
    DifficultyRationale string
    Route               *TaskExecutionRoute
    Prompt              string
    RunMeta             *RunMeta
}
```

序列化到 event / AgentControl task record 时仍使用 `route_provider`、`route_model`、`route_reasoning_effort` 等字段名，避免和 teammate/session identity provider 混淆。

### 6.5 RunMeta 扩展

如果 team task execution 通过 `RunMeta` 进入 chat actor，应在 `RunMeta.Team` 下增加 route metadata：

```go
type TeamRunMeta struct {
    TeamID                 string
    AgentID                string
    CurrentTaskID          string
    Difficulty             string
    DifficultySource       string
    DifficultyRationale    string
    RouteProvider          string
    RouteModel             string
    RouteReasoningEffort   string
    RouteSource            string
    RouteWarnings          []string
}
```

`RunMeta` 只能作为当前 run 的观测 metadata，不应作为 route override 的唯一生效来源。当前 runtime state 会持久化 `CurrentRunMeta` / `AmbientRunMeta`，因此必须遵守：

- route fields 只允许出现在当前 task run 的 `CurrentRunMeta`。
- 不要把 route fields 写入 `AmbientRunMeta`。
- run 结束清空 `CurrentRunMeta` 时，route fields 必须随 run meta 一起清空。
- session restore 时不能用旧 `RunMeta.Team.RouteProvider/RouteModel` 恢复 teammate session 的长期 provider/model。

推荐最终形态：`TaskExecutionRoute` 作为 `TaskTriggerRequest.Route` 和 `SubmitPromptOption.RouteOverride` 的生效来源；`RunMeta.Team` 只复制一份 route summary，用于事件、工具上下文和 debug。

## 7. Resolver 输入设计

### 7.1 Parent defaults

team task route 需要 parent defaults。推荐优先级：

1. team lead session provider/model/reasoning。
2. current chat/session provider/model/reasoning，如果 spawn_team 从当前 chat 启动且 lead session 可解析。
3. runtime configured default provider/model。
4. 空值交给 resolver 的 fallback/validation 处理。

`spawn_agent` 已在 `session_runtime_support.go` 里实现 parent defaults 解析，可以提取共享 helper：

```go
type RouteParentDefaultsProvider interface {
    ParentDefaultsForSession(sessionID string) modelrouting.ParentDefaults
}
```

第一版可在 API handler / session runtime support 层提供 team route resolver，避免 `team` 包直接依赖 chat session。

### 7.2 TaskHint 映射

team task 映射到 `modelrouting.TaskHint`：

```go
modelrouting.TaskHint{
    ID:                  task.ID,
    Role:                teammateRouteRole(mate, task),
    Goal:                firstNonEmpty(task.Goal, task.Title),
    Difficulty:          task.Difficulty,
    DifficultyRationale: task.DifficultyRationale,
    Provider:            "", // first version: do not accept provider override from spawn_team task
    Model:               "", // first version: do not accept model override from spawn_team task
    ReasoningEffort:     "",
    ReadOnly:            len(task.WritePaths) == 0,
}
```

Role 映射建议：

```text
if teammate.Profile != "" -> teammate.Profile
else if len(task.WritePaths) > 0 -> writer
else -> researcher/verifier heuristic
```

更保守的第一版：

```text
role = teammate.Profile
fallback role = writer if write_paths non-empty else researcher
```

### 7.3 Explicit override 策略

第一版不建议给 `spawn_team.tasks[]` 增加 provider/model/reasoning override。原因：

- `spawn_team` task payload 通常由模型生成，直接给 provider/model override 会扩大 prompt injection 面。
- 当前父方案已经强调 provider/model 最终由本地策略授权。
- task difficulty + teammate profile + local route config 已足够覆盖主要场景。

如果未来确实要支持，应满足：

- 字段 optional。
- 默认 disabled。
- 必须经过 `allow_explicit_*_override` 和 allowlist。
- route audit 必须记录 override denied / allowed。

## 8. 执行流程设计

### 8.1 新增接口

在 team package 内新增一个窄接口，不直接依赖 `agentconfig` 或 chat session：

```go
type TaskRouteResolver interface {
    ResolveTaskRoute(ctx context.Context, request TaskRouteRequest) (TaskRouteDecision, error)
}

type TaskRouteRequest struct {
    Team      Team
    Teammate  Teammate
    Task      Task
    Attempt   int
    SessionID string
}

type TaskRouteDecision struct {
    Decision modelrouting.RouteDecision
    Disabled bool
}
```

如果要避免 `team` 包 import `modelrouting`，可以复制一个 team-local DTO：

```go
type TaskRouteDecision struct {
    Difficulty          string
    DifficultySource    string
    DifficultyRationale string
    Provider            string
    Model               string
    ReasoningEffort     string
    Source              string
    Warnings            []string
    FallbackUsed        bool
    FallbackReason      string
}
```

推荐第一版允许 `team` 包依赖 `modelrouting`，因为当前 `team` 已经和 `agentcontrol` 等内部包耦合，保持 DTO 一致更有价值。

### 8.2 TeammateRunner 接入点

`TeammateRunner` 增加字段：

```go
type TeammateRunner struct {
    Sessions      SessionClient
    AgentControl  TaskTriggerClient
    RouteResolver TaskRouteResolver
    RouteAudit    TaskRouteAuditSink
    ...
}
```

`StartTask()` 中在 `buildTaskPrompt()` 前或后解析 route 均可，但推荐在 prompt 前解析，这样可把 runtime routing metadata 注入 task prompt：

```text
route, err := r.resolveTaskRoute(ctx, team, mate, task)
if err != nil { handle route failure }
prompt := buildTaskPrompt(..., route)
runMeta.Team.RouteProvider = route.Provider
request.Route = route
...
```

`handle route failure` 的语义必须显式实现：

- permissive failure：生成 parent/default fallback route，继续 build prompt 和 trigger session，并把 warning/fallback reason 写入 route audit。
- strict failure：不调用 `triggerTask()`，直接返回 `TaskRunResult{Success:true, Blocked:true, Outcome:TaskOutcomeBlocked, OutcomeApplied:true}`，`Summary`/`Blocker` 使用 scrubbed safe error。
- route audit 需要在 strict failure 时也写入，`Route.Error` 只保存 scrubbed message。

`route_attempt` 第一版可使用 task 的 retry count 或 claim attempt 推导；如果当前 store 没有独立 attempt 表，则在 route resolved event 中保存 `attempt`，latest task route 只保存最后一次。

### 8.3 Prompt 注入

team teammate prompt 当前已经显示 task difficulty。建议增加 route metadata 片段：

```text
Runtime routing:
- provider: <route_provider>
- model: <route_model>
- reasoning_effort: <route_reasoning_effort>
- source: <route_source>

Do not request a stronger model yourself; report if the assigned model seems insufficient.
```

注意：prompt 只做可观测和行为约束，不是安全边界。

### 8.4 TaskTriggerClient 应用 route

当前 `TaskTriggerClient.TriggerTask(ctx, request)` 是 team runner 进入 AgentControl / chat actor 的执行面。这里必须把 route provider/model/reasoning 真正写入下一次 LLM request。

推荐实现：

1. `TaskTriggerRequest` 携带 `TaskExecutionRoute`。
2. chat actor host 在处理 team task trigger 时构造 per-run config override。
3. override 只对当前 task run 生效，不长期改 teammate session context。
4. `LLMRequest.Provider/Model/ReasoningEffort` 最终来自 per-run override。

按当前代码，PR-3 的最小改动顺序应为：

1. 在 `team` 包定义 `TaskExecutionRoute`，`TaskTriggerRequest.Route *TaskExecutionRoute`。
2. 在 `chat` 包定义 `RunRouteOverride{Provider, Model, ReasoningEffort string}`，或直接复用 `team.TaskExecutionRoute` 的只读子集。
3. 扩展 `SubmitPromptOption` 和 `SubmitPrompt` command，新增 `RouteOverride`。
4. `sessionActorClient.TriggerTask()` 调用 actor 时传入 `SubmitPromptOption{RouteOverride: routeFromTaskExecutionRoute(request.Route)}`，不能再走不带 option 的 `SubmitPrompt()`。
5. `SessionActor.startSessionRun()` 接收 route override，并传给 `runLoop()`；`ContinueSession` 默认不继承 route override，除非是同一个未完成 task run 的恢复路径。
6. `runLoop()` 调用 `loopConfigForRun(a.loopConfig, override)`，返回克隆后的 `LoopReActConfig`。
7. `LoopReActConfig` 增加 `Provider`、`Model`，构造 `LLMRequest` 时使用 `firstNonEmpty(loop.config.Provider, loop.agent.config.Provider)` 和 `firstNonEmpty(loop.config.Model, loop.agent.config.Model)`。
8. prompt preflight 的 provider/model 解析也要接受同一 run loop config，否则 preflight 可能按旧模型预算 compact，而真实请求走新模型。

如果 teammate task 将来允许等待审批或人工输入后恢复，同一个未完成 task run 的 route override 需要从 `CurrentRunMeta.Team` 或 task latest route audit 重建；但恢复完成后仍必须清空，不能写成 ambient session default。

如果短期内 actor host 只能通过 session context 传值，则必须：

- 写入前保存旧值。
- 只在当前 run scope 内使用。
- run 完成后恢复旧 session context。
- 并发执行同一 teammate session 时必须拒绝或串行化，避免 context race。

优先不要走临时覆盖 session context 的方案，除非没有更小入口。

## 9. Fallback 和失败语义

### 9.1 routing disabled

当 `modelrouting.RoutingEnabled(cfg)==false`：

- 不改变 teammate execution provider/model/reasoning。
- 可以继续把 task difficulty 传到 prompt 和 event。
- route source 可为空或 `disabled`。
- 不应写入 route_provider/route_model，避免 UI 误以为发生模型调度。

### 9.2 permissive mode

默认推荐 permissive：

| 场景 | 行为 |
|---|---|
| missing difficulty | 使用 default difficulty，记录 `difficulty_missing_defaulted` |
| invalid difficulty | fallback default，记录 `difficulty_invalid_defaulted` |
| provider unresolved | fallback parent，记录 `provider_fallback_parent` |
| model unsupported | fallback parent 或 provider default，记录 warning |
| reasoning unsupported | 按 `unsupported_reasoning_policy` ignore/downgrade/fail |

permissive 下 route failure 不应直接导致 team 失败，除非 resolver 返回不可恢复错误。

### 9.3 strict mode

strict 下 route failure 建议让 task 进入 `blocked`，而不是直接让整个 team failed。

推荐 task state：

```text
status=blocked
summary="route resolution failed: <safe error>"
blocker="subagent route provider unavailable: <provider>"
```

同时事件 payload 带：

```text
route_error
route_source
difficulty
difficulty_source
```

只有当 route failure 发生在 team startup 的 mandatory lead task 上，才考虑 team-level failed。

## 10. Expert 并发治理

父方案已有 `max_expert_concurrency`。`spawn_team` 第一版应复用同一配置，但实现为 team runner 层的 process-local semaphore：

```text
if decision.Difficulty == expert:
    acquire expert slot before triggering teammate task
    release after task run completes
```

精确生命周期：

1. 先解析 route，确认最终 normalized difficulty。
2. 只有 `difficulty == expert` 且 routing policy 要求限制时 acquire。
3. acquire 点在写 `task.dispatch.started` route event 和调用 `triggerTask()` 之前。
4. `triggerTask()` 返回、strict blocked、context canceled、panic recovery 等路径都必须 release。
5. 如果 acquire 因 context canceled 失败，task 应保持可重试或返回 blocked/failed 的现有语义，不应写入已开始执行的 route started event。

范围建议：

- 第一版只限制当前进程内 team task execution。
- 不做跨进程锁。
- 不做 provider-level pool。
- 不限制 hard，除非后续配置扩展。

如果已有 internal `SubagentScheduler` 的 expert semaphore，不要直接跨包复用 scheduler 实现。应提取一个小的 shared limiter 或在 team runner 中独立实现，避免 team package 依赖 agent scheduler。

## 11. 事件与可观测性

### 11.1 Team lifecycle events

至少以下 event 应带 route metadata：

```text
task.route.resolved
task.dispatch.started
task.completed
task.failed
task.blocked
task.retried
```

如果当前 event 名是 `task.completed` / `task.failed` / `task.blocked`，保持现有名称，但 payload 增加 latest route 字段。

当前代码里 `Orchestrator.executeAssignment()` 在调用 `Runner.StartTask()` 之前发布 `task.started`。如果 route 仍在 `TeammateRunner.StartTask()` 内解析，这个早期 `task.started` 不可能携带 resolved route。

推荐第一版采用显式双事件语义：

- 保留现有 `task.started` 作为 assignment started event，可带 `route_status=pending`。
- route 解析完成后，由 runner/audit sink 追加 `task.route.resolved` 和 `task.dispatch.started`，payload 必须包含 route metadata。
- `task.completed` / `task.failed` / `task.blocked` 从 `TaskRunResult.Route` 或 latest route audit 补齐 route metadata。
- teammate session 发布 `approval_requested` 时，team/TUI 观测层应能关联到当前 `team_id/task_id/teammate_id`，并可投影为 `task.waiting_approval` 或等价状态提示；该投影只用于可见性，不改变底层 actor approval flow。

如果产品语义强要求 `task.started` 本身必须包含 resolved route，则 PR-3 需要拆出 `Runner.PrepareTaskExecution()`：orchestrator 先 prepare/resolve route，再发布 `task.started`，最后执行 prepared request。不要在 route 尚未解析时发布一个看似完整但 route 为空的 `task.started`。

### 11.2 AgentControl task graph events

task claim / start 时写 route audit：

```json
{
  "workflow": "spawn_team",
  "team_id": "team-1",
  "task_id": "task-1",
  "teammate_id": "member-1",
  "difficulty": "hard",
  "difficulty_source": "explicit",
  "difficulty_rationale": "Touches provider routing and team runner.",
  "route_provider": "codex",
  "route_model": "gpt-5.4",
  "route_reasoning_effort": "high",
  "route_source": "difficulty_level",
  "route_warnings": []
}
```

### 11.3 Mailbox / collab UI

When task completion is reflected into mailbox/collab output, include compact route metadata:

```text
difficulty=hard route=codex/gpt-5.4 reasoning=high source=difficulty_level
```

Warnings should be visible but concise:

```text
warnings=provider_fallback_parent,reasoning_effort_unsupported_downgraded
```

### 11.4 Debug / doctor

Extend existing dry-run route tooling with team-specific options:

```text
aicli doctor subagent-route --workflow spawn_team --team-id team-1 --teammate member-1 --task task-1
/agents routing test --workflow spawn_team --teammate member-1 --difficulty hard --write-path src/foo.go
```

Minimum acceptable first version:

```text
/agents routing test --role writer --difficulty hard
```

并在文档中明确 team-specific dry-run 暂未提供。

## 12. 兼容性

### 12.1 旧 spawn_team payload

不带 difficulty 的旧 payload 仍然有效：

```json
{
  "tasks": [
    {"id": "task-1", "goal": "Inspect docs"}
  ]
}
```

运行行为：

- difficulty 使用 routing 默认值，通常是 `normal`。
- routing disabled 时不改变模型。
- routing enabled 时默认 difficulty route 可以生效。

### 12.2 已持久化的 team

已有 team 行没有 route metadata。读取方必须把空 route 字段视为 unknown。

迁移规则：

- 使用 nullable columns 或 JSON defaults。
- 不要求 backfill。
- 已完成历史 task 不重新路由。
- 没有 route metadata 的 running task 继续按 legacy 行为运行，除非被 retry。

### 12.3 Session restore

恢复时：

- teammate identity/session 保持不变。
- 已运行 task 的 route decision 如果存在，应从 task/event metadata 读取。
- retry 应重新计算 route。
- 不要从旧 task route 全局设置 teammate session provider/model。

## 13. 安全与策略

1. `difficulty` 是模型/用户提供的 metadata，不是权限边界。
2. provider/model/reasoning 只能由本地 routing config 和 resolver policy 授权。
3. 第一版 `spawn_team.tasks[]` 不应接收 provider/model override。
4. prompt injection 不能改变 route config。
5. route warnings 不得包含 API key、带凭据的 base URL、完整 prompt 或原始配置 secret。
6. strict mode route errors 必须经过 secret scrubbing 后才可展示给用户。

### 13.1 权限处理边界

teammate-level provider/model routing 只决定“用哪个模型执行当前 task”，不能改变 teammate 的工具权限、文件权限、审批策略或 runtime permission mode。实现时必须把 route policy 和 execution permission policy 分开：

1. route decision 不得提升 `PermissionMode`。
   当前 `TeammateRunner` 会用 `RunMeta{PermissionMode: "bypass_permissions"}` 执行 team task。路由实现不能因为 difficulty 是 `hard` / `expert` 就扩大这个权限，也不能把 provider/model override 当作 bypass permission 的理由。
2. provider/model/reasoning override 只写入 per-run LLM config。
   不得写入 tool policy、approval policy、workspace allowlist、path claim、shell permission 或 MCP tool permission。
3. `spawn_team.tasks[]` 第一版不接收 provider/model/reasoning override，也不接收 permission override。
   即使将来允许显式 provider/model override，也必须继续禁止 task payload 直接设置 `PermissionMode`、tool allowlist、审批绕过、workspace root 或 shell policy。
4. task difficulty 不能降低审批要求。
   `expert` 只能影响 route selection / concurrency / audit，不应自动跳过 human approval、shell confirmation、write-path validation、path claim 或 tool broker policy。
5. teammate 执行权限应继承现有 team/session/tool policy。
   如果当前 team task 依赖 `RunMeta.PermissionMode`，PR 实现必须保留既有行为；如需调整，应另开权限方案，不和模型路由 PR 混合。
6. path / artifact / mailbox 权限仍由现有 team task graph、path claim、workspace policy 和 mailbox ACL 决定。
   route metadata 只能作为审计信息，不能作为读取其他 teammate mailbox、跨 team task graph、访问未声明路径或写入未授权 artifact 的依据。
7. route audit event 不得泄露权限相关敏感信息。
   event 可以记录 `permission_mode` 的安全摘要，例如 `inherited` / `bypass_permissions` / `approval_required`，但不得记录 API key、原始 policy 配置、完整 prompt、shell command secret 或凭据化 base URL。

推荐新增一个定向测试：route override 生效时，`PermissionMode`、tool approval policy、workspace/path claims 保持和未启用 routing 时一致。

### 13.2 TUI 审批处理

当前 `SessionActor` 已支持交互式 tool approval：permission engine 返回 ask 时，actor 会记录 `PendingTool` / `PendingApproval`，把 session 状态改为 `waiting_approval`，并发布 `approval_requested` event；TUI/CLI runtime event bridge 收到 event 后渲染审批提示，再调用 `ApproveTool` / `ApproveToolWithArgs` 把用户决定送回对应 session actor。

`spawn_team` teammate route 接入后，如果某个 teammate task 在 TUI 中触发审批，应按以下语义处理：

1. 审批归属是 teammate session，不是 team id。
   TUI 必须使用 `approval_requested` event 的 `session_id` 和 `request_id` 调用对应 actor 的 `ApproveTool`。不要拿 `team_id`、`teammate_id` 或 `task_id` 当 approval target。
2. team/task 状态不能因为等待审批被标记 failed。
   `waiting_approval` 是暂停态。orchestrator / wait_team / TUI panel 应显示 task 正在等待审批，并继续等待 approval resolved 后的 task outcome。
3. TUI 展示应包含 team 上下文。
   审批提示至少展示 `team_id`、`task_id`、`teammate_id/name`、tool name、risk/reason、permission mode；如果存在 route metadata，可展示紧凑 route summary，但不能把 route 作为授权依据。
4. route override 必须跨审批暂停保持一致。
   同一个 task run 在 approval 前后应继续使用同一个 `TaskExecutionRoute`。如果 actor 进程未重启，run-scoped override 可留在当前 goroutine；如果需要从持久 `waiting_approval` 恢复，应从 `CurrentRunMeta.Team` 的 route summary 或 task latest route audit 重建 per-run override。恢复完成后仍必须清空，不能写入 `AmbientRunMeta`。
5. 用户拒绝审批时，按现有 actor 语义恢复 pending tool 并生成 denied result。
   teammate 应把 denied result 转成结构化 task outcome；如果无法产出结构化 outcome，orchestrator 可按现有失败/阻塞恢复逻辑处理，但 route layer 不应把 denial 改写成 provider/model failure。
6. 非交互模式不应挂起。
   `--no-interactive` / headless 模式遇到 approval request 时应走现有 fast-fail/auto-deny 路径，并把错误明确标记为 `interactive approval required` 或等价结构化错误；不要让 `wait_team` 无限等待。
7. approval reuse 策略保持原语义。
   `/approval-reuse` 的 session/team readonly shell 复用策略可以继续工作，但 route metadata 不能扩大复用范围，也不能让 hard/expert route 自动获得更宽 approval reuse。

当前 `spawn_team` teammate runner 默认使用 `bypass_permissions`，正常情况下 team task 不会触发交互式审批。若后续引入可配置 teammate permission mode，必须先保证上述 TUI approval bridge 和恢复语义完整，再允许默认模式进入生产。

## 14. 实施计划

### PR-1: Team route DTO and audit fields, no behavior change

文件：

```text
backend/internal/team/types.go
backend/internal/team/agent_projection.go
backend/internal/team/agent_task_registry.go
backend/internal/agentcontrol/task.go
backend/internal/team/sqlite_store.go
backend/internal/team/sqlite_store_test.go
backend/internal/team/agent_projection_test.go
```

改动：

- 增加 task route audit DTO。
- 如果缺失，则给 AgentControl task record 增加 route metadata 字段。
- 增加专用 task route audit update request，或等价的 nullable route patch 入口。
- 以 nullable columns 或 JSON metadata 持久化 latest route metadata。
- 将 team task 的 route metadata 投影到 AgentControl task。
- 不改变 teammate 执行模型。

验收:

- 现有 team task 测试通过。
- 旧行 route metadata 为空时可以正常读取。
- AgentControl task list 能在 metadata 存在时包含 route 字段。

### PR-2: Resolver bridge for team tasks, still disabled-safe

文件：

```text
backend/internal/team/task_routing.go
backend/internal/api/skills/session_runtime_support.go
backend/internal/api/skills/team_handlers.go
backend/internal/team/teammate_runner.go
backend/internal/team/teammate_runner_test.go
```

改动：

- 增加 `TaskRouteResolver` interface。
- 实现使用 `modelrouting.Resolver` 的 API/session-layer resolver bridge。
- 将 `Team + Teammate + Task` 映射为 `modelrouting.TaskHint`。
- 增加 route decision payload builder。
- routing disabled 时返回 source `disabled`，且不产生 provider/model override。

验收:

- missing difficulty 能默认化且不失败。
- invalid difficulty 遵循 compatibility mode。
- role/difficulty route 产生预期 decision。
- routing disabled 不设置 route provider/model。

### PR-3: Apply route to teammate execution request

文件：

```text
backend/internal/team/teammate_runner.go
backend/internal/api/skills/team_handlers.go
backend/cmd/aicli/commands/chat_actor_host.go
backend/cmd/aicli/commands/chat_actor_host_test.go
backend/internal/chat/actor.go
```

改动：

- 扩展 `TaskTriggerRequest`，增加 `TaskExecutionRoute`。
- 扩展 `SubmitPromptOption` / `SubmitPrompt` command，增加 run route override。
- 扩展 `SessionActor.startSessionRun()` / `runLoop()`，将 route override 注入克隆后的 per-run loop config。
- 扩展 `LoopReActConfig` provider/model override，并让 prompt preflight 使用同一 override。
- 扩展 team task trigger path，将 route provider/model/reasoning 传入 per-run actor config。
- 确保 `LLMRequest.Provider`、`LLMRequest.Model`、`LLMRequest.ReasoningEffort` 只反映当前 task route。
- 避免永久修改 teammate session provider/model。
- 确保 route override 不改变 `PermissionMode`、tool approval policy、workspace/path claims 或 shell/tool permission。

验收:

- hard team task 路由到配置的 hard provider/model/reasoning。
- easy team task 可路由到不同 provider/model。
- 同一个 teammate 处理两个不同 difficulty task 时使用不同 request route。
- prompt preflight 和真实 `LLMRequest` 使用同一个 provider/model/reasoning。
- route override 生效时，execution permission policy 与未启用 routing 时一致。
- routing disabled 保留旧 teammate execution route。

### PR-4: Events, mailbox, debug/doctor

文件：

```text
backend/internal/team/orchestrator.go
backend/internal/events/bus.go
backend/cmd/aicli/commands/chat_runtime_events.go
backend/cmd/aicli/commands/chat_debug.go
backend/cmd/aicli/commands/doctor_subagent_route.go
backend/internal/toolbroker/agent_mailbox.go
```

改动：

- 保留早期 `task.started` 的 assignment 语义，并增加 `task.route.resolved` / `task.dispatch.started` route events。
- 给 team task completion/failure/blocked events 增加 latest route metadata。
- 将 teammate session 的 `approval_requested` / `approval_resolved` 投影到 team/TUI 可见上下文，显示 `team_id/task_id/teammate_id`。
- 给 AgentControl mailbox/collab rendering 增加 route metadata。
- 扩展 debug summary，加入 team route metadata counts。
- 可选增加 team-specific dry-run flags。

验收:

- Event summary 聚合 team route provider/model/reasoning。
- Warnings 简洁渲染。
- Event payload 不包含 secrets。
- TUI 中 teammate task 等待审批时显示 pending approval，不把 task/team 标记为 failed。
- approval resolved 后 task 继续等待 teammate outcome，route summary 不丢失。

### PR-5: Expert concurrency and fallback hardening

文件：

```text
backend/internal/team/teammate_runner.go
backend/internal/team/orchestrator.go
backend/internal/team/teammate_runner_test.go
backend/internal/modelrouting/resolver_test.go
```

改动：

- 增加 team task execution 的 expert concurrency limiter。
- strict route failure 返回 `OutcomeApplied=true` 的 blocked `TaskRunResult`，用安全错误阻塞 task。
- permissive fallback 继续执行 task 并记录 warnings。
- retry 重新计算 route 并记录新的 audit。

验收:

- 两个 expert task 遵守配置的 concurrency limit。
- strict provider missing 会阻塞 task。
- permissive provider missing fallback 到 parent 并继续执行。
- retry 记录新的 route decision。

## 15. 测试矩阵

### 15.1 单元测试

#### team task route bridge

| 测试 | 预期 |
|---|---|
| missing difficulty defaults normal | decision difficulty normal, warning present |
| explicit hard difficulty | hard route selected |
| teammate profile role override | role override wins over level route |
| write_paths role inference | writer-like route used when profile empty |
| routing disabled | no provider/model/reasoning override |
| provider unavailable permissive | parent fallback and warning |
| provider unavailable strict | error suitable for task blocked |
| reasoning unsupported downgrade | route reasoning downgraded |

#### teammate runner

| 测试 | 预期 |
|---|---|
| StartTask passes `TaskExecutionRoute` into TaskTriggerRequest | provider/model/reasoning set |
| StartTask disabled does not pass provider/model | legacy request |
| prompt includes runtime routing metadata | visible route source |
| route failure strict blocks task | no session trigger, `OutcomeApplied=true` |
| expert limiter serializes expert starts | max concurrency respected |
| route override preserves permission mode | `RunMeta.PermissionMode` unchanged |

#### chat actor route override

| 测试 | 预期 |
|---|---|
| SubmitPromptOption route override | next LLMRequest provider/model/reasoning match route |
| two sequential task prompts on same actor | second prompt does not inherit first route |
| routing disabled no override | actor base provider/model unchanged |
| prompt preflight under route override | preflight metadata resolves same provider/model as LLMRequest |
| route override does not alter execution permissions | approval/tool/workspace policy unchanged |
| approval pause under route override | pending approval preserves current task route |
| approval resume under route override | next LLMRequest still uses same task route |

#### TUI / approval bridge

| 测试 | 预期 |
|---|---|
| teammate approval requested | TUI prompt shows team/task/teammate context |
| approval allowed | actor receives `ApproveTool`, task continues |
| approval denied | denied result is surfaced, not route failure |
| no-interactive approval | fast-fail/auto-deny, `wait_team` does not hang |
| approval reuse enabled | reuse follows existing policy, route does not widen scope |

#### AgentControl projection

| 测试 | 预期 |
|---|---|
| task record persists route audit | route fields round-trip |
| old task without route metadata reads | zero values, no error |
| task list includes route metadata | JSON output includes route fields |
| retry recomputes route | new route attempt visible |

### 15.2 集成测试

使用 mock providers:

```text
local-small -> small-model -> reasoning low
remote-strong -> strong-model -> reasoning high
```

场景:

1. 配置 routing：

```yaml
aicli:
  subagents:
    routing:
      enabled: true
      default_difficulty: normal
      levels:
        easy:
          provider: local-small
          model: small-model
          reasoning_effort: low
        hard:
          provider: remote-strong
          model: strong-model
          reasoning_effort: high
```

2. 创建包含两个 task 的 team：

```json
{
  "tasks": [
    {"id": "read-docs", "goal": "Read docs", "difficulty": "easy"},
    {"id": "refactor-core", "goal": "Refactor core provider routing", "difficulty": "hard"}
  ]
}
```

3. 断言:

- easy task request 使用 `local-small/small-model/low`。
- hard task request 使用 `remote-strong/strong-model/high`。
- 两个 task 可以由同一个 teammate 处理，且不会发生 sticky route leakage。
- runtime events 包含 route metadata。
- AgentControl task records 包含 latest route metadata。

### 15.3 回归测试

必须保持:

- 不带 difficulty 的 `spawn_team` payload 仍可运行。
- `routing.enabled=false` 不改变 teammate execution model。
- task dependencies、path claims、lease renewal、release/retry 继续工作。
- team wait/read tools 仍能区分 `spawn_agent` sessions 和 `spawn_team` teammates。
- read-only / write path constraints 不受影响。

## 16. 发布与迁移

推荐发布顺序:

1. 先发布 route metadata storage/projection，不改变执行行为。
2. 在 `aicli.subagents.routing.enabled` 后面发布 resolver bridge。
3. 只有显式启用配置时才启用 execution route。
4. 默认保持 permissive fallback。
5. 在建议用户配置 hard/expert remote provider 前，先补齐 debug/doctor 可见性。
6. 权限行为必须保持二进制兼容：routing rollout 不得改变 `PermissionMode`、approval/tool policy、path claim 或 workspace policy。

迁移:

- SQLite migrations 增加 nullable route fields。
- 已有 task rows 不需要 backfill。
- 已有 team runs 继续 legacy 行为，直到 feature enabled 后某个 task 被 retry 或新 task 被 claim。
- 已持久化的 `CurrentRunMeta` / `AmbientRunMeta` 即使将来包含 route summary，也不得用来恢复或提升 execution permission。

回滚:

- 关闭 `routing.enabled` 后，teammate execution 回到旧 provider/model/reasoning 行为。
- 已写入的 route metadata 只保留为审计信息，不再作为 execution override 生效。
- 如果发现权限行为变化，优先关闭 execution route，并保留 route audit 以定位 override 是否错误写入了 `PermissionMode`、approval/tool policy、path claim 或 workspace policy。
- 回滚不应删除 task graph、mailbox、path claim 或 run meta 历史记录。

## 17. 待确认决策

| 决策 | 推荐第一版答案 |
|---|---|
| latest route 存在 task row 还是只存 event？ | task row 保存 latest route，event 保存每次 execution route。 |
| 按 teammate 还是 task 路由？ | task execution。 |
| 是否允许 task provider/model override？ | 第一版不允许。 |
| retry 时是否重新计算？ | 是。 |
| strict failure 结果是什么？ | 默认阻塞 task，不让整个 team failed。 |
| 是否通过 session context 应用 route？ | 尽量避免；优先 per-run override。 |
| route 是否可以修改 execution permission？ | 不可以；`PermissionMode`、approval/tool policy、path claim、workspace policy 均保持原语义。 |
| TUI approval 是否新建 team 专用审批通道？ | 不新建；复用 session actor `approval_requested` / `ApproveTool`，team 层只做上下文投影。 |
| Expert limiter 范围？ | 第一版 process-local。 |
| Team-specific doctor command？ | route application 落地后作为 nice-to-have。 |

## 18. 验收清单

满足以下条件时认为实现完成:

1. `difficulty=hard` 的 `spawn_team` task 可以使用不同于 easy task 的 provider/model/reasoning。
2. 同一个 teammate 可以运行两个不同 route decision 的 task，且不会发生 sticky provider/model leakage。
3. `routing.enabled=false` 保持当前行为。
4. prompt preflight 和实际 LLM request 使用同一个 run route override。
5. `RunMeta` route metadata 不写入 `AmbientRunMeta`，session restore 不会把旧 route 变成 teammate 默认模型。
6. `task.route.resolved` / `task.dispatch.started` 出现 route metadata，completion/failure/blocked events 出现 latest route metadata。
7. AgentControl task records 暴露 latest route metadata。
8. Route warnings 和 fallback reasons 可见且不泄露 secret。
9. Strict mode route failure 会用 `OutcomeApplied=true` 的 blocked result 阻塞 task。
10. Retry 会重新计算并记录 route。
11. Expert concurrency limit 应用于 expert team task execution。
12. Route override 不改变 `PermissionMode`、approval/tool policy、workspace/path claims 或 shell/tool permission。
13. TUI 中 teammate task 触发 approval 时能显示 team/task/teammate 上下文，approve/deny 后能恢复或安全结束，不导致 `wait_team` 无期限挂起。
14. 定向测试覆盖 resolver bridge、teammate runner、chat actor route override、TUI approval bridge、permission invariance、AgentControl projection、events 和 disabled compatibility。

## 19. 与父方案的关系

父方案仍然是以下内容的权威来源：

- difficulty taxonomy；
- `aicli.subagents.routing` 配置；
- `modelrouting.Resolver` 行为；
- explicit override policy；
- provider/model/reasoning capability policy；
- common route audit field names。

本文档是以下内容的权威来源：

- `spawn_team` teammate execution route granularity；
- team task route storage 和 event propagation；
- teammate runner injection point；
- team-specific fallback behavior；
- team-specific test matrix 和 rollout sequence。
