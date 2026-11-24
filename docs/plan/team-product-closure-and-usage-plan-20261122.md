# Team 产品闭环与使用落地方案

- **日期**：2026-11-22
- **状态**：方案草案（产品与工程对齐）
- **补充审查基线**：[`team-product-closure-gap-review-and-roadmap-20261122.md`](./team-product-closure-gap-review-and-roadmap-20261122.md)
- **范围**：`aicli` Chat/TUI、Workspace Web、Team runtime API、Team routing 与可观测性
- **目标**：避免 Team 后端能力已经存在，但用户在前端或 TUI 中无法创建、运行、理解和收敛 Team

> 本文是产品闭环方案，不是本次代码实现记录。本轮根据补充 gap review 深化产品对象、生命周期、Web/TUI、嵌入、恢复和验收方案。补充审查文档中的建议不自动等于已实现事实；当前工作区的既有未提交修改不在本方案范围内，本次只编辑本文方案文档。

## 1. 摘要与结论

当前 Team 的主要问题不是“后端完全没有 Team”，而是**同一套能力在不同入口上的产品语义不一致，且 Web 入口尚未覆盖完整的多成员 Team 生命周期**。

经过代码与入口核对，结论如下：

1. **后端运行闭环基本存在**：`team_spawn` 可以创建/复用 Team、teammate 和 task；orchestrator 负责依赖、并发和路径 claim；teammate runner 触发 worker session；结果、事件、mailbox 和 final summary 可以持久化并读取。
2. **`aicli` Chat/TUI 是目前较完整的 Team 使用入口**：主要通过 `team_spawn`、`team_wait`、`team_send`、`team_mailbox` 及 Team task 工具完成协作，而不是通过一个独立的 `/team` 命令完成全部操作。
3. **Web 当前是“Team 详情 + fan-out 派发”入口，不是完整的多成员 Team builder**：可以查看已有 Team 的 roster、task、事件、mailbox、path claims 和 summary，也可以将任务派发到多个已有 Team；Provision 路径实际创建的是多个单成员 Team。
4. **多成员 Team 和 fan-out 多 Team 必须在产品上明确分开**：前者是一个 Team 内的多个 worker 通过 task graph 协作，后者是多个独立 Team 并行执行同一任务或其变体，用于批量比较或吞吐。
5. **路由不能被简化为 `/provider`、`/model`、`/reasoning` 直接设置每个 worker**：Team task 的有效 provider/model/reasoning 由 Team routing 或 subagent routing fallback、task difficulty、task type、role/profile、parent defaults 和健康门禁共同决定。需要展示 effective route 和 fallback，而不是制造一个未经后端支持的“worker 直接改模型”入口。
6. **最小闭环优先补 Web 的创建、成员绑定、task graph、route preview、运行控制和 task-level 结果审计**；不需要另造一套 orchestrator，也不应把 Web fan-out 误包装成多成员 Team。
7. **Team 与普通 subagent 是两个不同的产品模式**：subagent 解决一次有边界的子任务委派，Team 解决需要多个角色、依赖、协作消息和持久化结果管理的协作项目。入口应先帮助用户选择工作模式，再暴露 `team_spawn` 或 `subagent_spawn` 等内部术语。
8. **当前“Team”不能同时承担长期配置与一次执行的产品含义**：目标模型应分清可复用的 Team Definition/Template 与每次独立的 Team Run；当前实现是否有独立 Run ID、snapshot 和 run history，需与存储/API 契约另行确认，不能假设已经存在。
9. **Web Builder 应采用渐进式复杂度**：Quick Start 默认只要求目标和成功标准；高级用户再展开 teammate、task graph、session binding、routing、权限和预算，不把后端 schema 原样变成必填表单。
10. **交付顺序应先定产品契约，再交付完整的 Quick Start → Preflight → Run Monitor → Results 闭环**；随后补齐 TUI 可发现性与跨入口闭环、Embedded Team Experience、Reliability/Recovery/History，最后做实时与运营分析。

建议的产品定位是：

```text
aicli Chat/TUI：完整的 Team 编排与协作入口
Workspace Web：可视化的 Team 创建/配置/运行/监控/结果入口
Runtime API：两者共享的持久化状态、生命周期和审计契约
```

## 2. 背景、目标与非目标

### 2.1 背景

后端已经同时存在 Team 数据模型、Team tool broker、orchestrator、teammate runner、runtime API 和 Web 查询组件。若只以“是否有 API/函数”判断完成度，会得到“Team 已完成”的结论；但从用户旅程看，仍存在以下断点：

- 用户不知道如何从 TUI 进入 Team，而不是普通 `subagent_spawn`；
- 用户也缺少“什么时候使用普通 subagent、什么时候使用 Team”的可感知判断标准，容易因为看到“多个 agent”就误选 Team，或把 Team 当成可随时跟进的普通 child；
- `team_spawn` 返回后，调用方可能误用 `subagent_wait`，或把一次 `team_wait` timeout 误认为执行失败；
- Team roster 可以被后端保存和在 Web 展示，但 Web 没有清晰的多成员编排向导；
- Web Provision 的“Team 数量”容易被理解为一个 Team 的成员数，实际上是 N 个独立的单成员 Team；
- session binding、worker readiness 和 Team roster 没有在产品界面形成明确的可执行状态；
- route 配置、difficulty 和 fallback 影响真实执行，但 Team 详情尚未以 task 维度展示有效路由；
- 结果可以分散在 task、events、mailbox 和 final summary 中，用户缺少统一的 task outcome 与 terminal reason 视图。

### 2.2 目标

本方案要建立一个可验收的闭环：

1. 用户能够在普通委派、共享协作 Team、独立并行工作和 Team fan-out 之间按协作需求正确选择。
2. 用户能够看到每个 teammate 是否拥有可执行的 worker session，以及该 session 是否 ready、busy、blocked 或 offline。
3. 用户能够为同一 Team 编排多个 task、依赖、assignee、读写路径、deliverables 和 difficulty。
4. 用户能够在执行前看到 Team task 的 effective route、来源、fallback 和 warning。
5. 用户能够正确启动、观察、暂停/恢复或收敛 Team，并区分“观察窗口 timeout”和“执行失败”。
6. 用户能够在一个位置看到 task outcome、Team summary、事件、mailbox、path claim 冲突和 terminal reason。
7. TUI、Web 和 API 使用同一套 Team 状态语义，避免各入口各自解释 Team。
8. 用户能区分 Team 配置、一次 Run、Task 与 Attempt，并在复制/重跑时保留历史结果。

### 2.3 非目标

本方案不承诺或不包含以下事项：

- 不在本次任务中实现代码、修改 API 或改变既有 Team 调度算法；
- 不把 Team worker 当作普通 `subagent_spawn` child 管理；
- 不把 `team_spawn.tasks[]` 设计成直接接受任意 provider、model、reasoning 或 tool permission 参数；
- 不把当前 Web 描述为已经具备完整多成员 Team 管理能力；
- 不声称已经完成真实 provider 调用、完整端到端运行、真实数据库检查或跨进程故障验证；
- 不把 Team Definition 与 Team Run 的分离、Run snapshot、成本预估、预算执行或 embed API 写成当前已经实现；
- 不在没有明确聚合语义前，承诺为 fan-out 多 Team 自动生成一个等价于单 Team lead summary 的跨 Team summary；
- 不用历史文档中“Team 尚未实现”的结论覆盖当前已经存在的代码事实。

### 2.4 产品对象模型与定义/运行分离（目标契约）

为了避免“Team”既指配置又指一次执行，建议将用户可见对象拆成以下概念：

| 对象 | 产品含义 | 建议状态/生命周期 |
| --- | --- | --- |
| Team Definition / Template | 可复用的成员角色、默认限制、routing 引用和任务模式；不代表正在执行 | draft → saved → archived |
| Team Run | 对一个具体 goal 的一次执行；保存配置快照、成员绑定、task graph、events、attempts、outcomes 和 summary | created → preflight → running / waiting / blocked → terminal → summary-ready |
| Teammate | Team Run 中承担某类职责的逻辑协作角色，不等同于 session | configured → provisioned → ready → busy / idle / blocked / offline → retired |
| Worker Session | 实际承载 teammate task 的运行会话 | unbound → provisioning → ready → busy → stale / offline → rebound |
| Task | Run 内可分派、依赖或交接的工作单元 | draft → queued → ready → claimed → running / waiting / blocked → succeeded / failed / cancelled |
| Attempt | task 的一次具体执行尝试，retry 应新增 attempt，不覆盖旧尝试 | started → running → completed / failed / aborted |
| Outcome / Artifact | task 的结构化交付结论和可追溯产物 | submitted → accepted / rejected / needs-review；产物另有验证和保留状态 |

**与当前实现的边界**：当前代码已存在 Team、teammate、task、task outcome/event/summary 等对象；本方案不据此断言当前已有独立的 Team Definition、Run ID、不可变 Run snapshot 或完整 Run history。若底层仍复用一个 Team record，Web/TUI 也应先明确产品语义，并在实现前决定是否需要增加 Run 标识、snapshot、历史迁移与兼容策略。

建议默认规则：Definition 可复用；每次新执行创建新的 Run 语义与配置快照；修改 Definition 不回写已运行快照；“复制配置”“重新运行”“重试失败 task”是不同操作；删除/归档 Definition 不应无声地删除历史 Run 与结果。

### 2.5 端到端用户旅程与状态责任

统一旅程应从模式选择一直到结果再利用，而不止于调用 `team_spawn`：

```text
描述目标
  → 推荐模式（普通委派 / 协作 Team / 独立并行或 Fan-out）
  → 选择或创建 Team Definition
  → 输入成功标准与约束
  → 配置/自动建议成员与 task graph
  → Preflight（blocker / warning / info）
  → Run preview（成员、任务、有效 route、风险、可用的成本/时延估计）
  → 用户确认并启动 Run
  → Run Monitor（状态、事件、task attempt、成员活动、介入/恢复）
  → terminal 与 summary 分别收敛
  → 评审、接受/导出产物、归档或复制配置/重新运行
```

每一阶段都应解释“现在是什么对象、发生了什么、用户下一步能做什么”。Run/Task/Session/观察窗口的状态不得压成一个模糊的 Team status；尤其 `team_wait` timeout 是观察状态，不能覆盖 Team Run 的真实状态。

## 3. 当前实现事实

### 3.1 后端 Team 运行链路

当前可复用的后端链路可以概括为：

```text
team_spawn
  ├─ 创建或复用 Team
  ├─ 创建/补齐 teammate 与 worker session binding
  └─ 创建 task、依赖、读写路径与执行元数据
        ↓
Team supervisor / orchestrator
  ├─ reconcile active Team
  ├─ 处理依赖、ready task、并发上限
  ├─ 处理 writer/path claim
  └─ 触发 teammate runner
        ↓
worker session 执行 task
        ↓
Task outcome / mailbox / lifecycle events 持久化
        ↓
terminal state → final summary → team_wait / Runtime API / Web
```

相关核心代码包括：

- `backend/internal/toolbroker/broker.go`
- `backend/internal/toolbroker/types.go`
- `backend/internal/toolbroker/team_tools.go`
- `backend/internal/team/orchestrator.go`
- `backend/internal/team/teammate_runner.go`
- `backend/internal/team/terminal_state.go`
- `backend/internal/team/terminal_summary.go`
- `backend/internal/api/runtimeapi/team_handlers.go`
- `backend/internal/api/runtimeapi/team_lifecycle.go`

这意味着产品工作重点应放在“入口、状态和反馈闭环”，而不是重新实现一套 Web 专属 Team 执行器。

### 3.2 已有 Team tool 入口

Team tool 的规范名称包括：

```text
team_spawn
team_wait
team_send
team_mailbox
team_task_spec
team_task_context
team_task_report
team_task_block
```

其中：

- `team_spawn` 不要求已有 active Team，用于创建/复用 Team 并可自动启动；
- `team_wait` 用于等待 `team_spawn` 运行达到持久化的 `team.completed` / `team.summary` 状态；
- `team_send` 和 `team_mailbox` 以及 task 协作工具要求当前存在 active Team run；
- 没有 active Team run 时，部分 Team-only 工具会被隐藏或拒绝，这是有意的上下文约束，而不是工具缺失。

`team_spawn` 当前支持的核心字段包括：

- Team：`team_id`、`workspace_id`、`strategy`、`status`、`max_teammates`、`max_writers`、`allow_existing`、`auto_start`；
- teammate：`id`、`name`、`profile`、`session_id`、`state`、`capabilities`；
- task：`id`、`title`、`goal`、`inputs`、`read_paths`、`write_paths`、`deliverables`、`priority`、`assignee`、`depends_on`、`difficulty`、`difficulty_rationale`、`task_type`、`task_subject`。

当前 schema 已明确：

- `difficulty` 的基本值为 `easy`、`normal`、`hard`、`expert`；
- `task_type` 是受控枚举，具体值以 `modelrouting.TaskTypes()` 为准；
- task payload 不直接接受任意 provider/model/reasoning/tool policy；
- `write_paths` 会参与 writer 并发和 path claim；
- `auto_start` 默认行为为启动后台 Team loop；
- Chat-originated `team_spawn` 会将当前 Chat session 作为 lead session，调用方不应自行伪造 detached lead。

### 3.3 Team session binding 是执行前置条件

Team roster 记录与可执行 worker session 不是同一个概念。一个 teammate 只有在具备有效 `session_id`、并由 local actor/session registry 正确加载或 provisioning 后，才具备执行 task 的基础条件。

当前代码可以为缺少 session ID 的 teammate 生成规范化 ID，并在需要时创建或加载 runtime session；这降低了调用门槛，但不能替代产品层的状态说明。Web 需要明确展示：

```text
teammate roster entry
  ≠
worker session 已创建
  ≠
worker session 当前可执行
```

**关于“成员是提前创建还是临时创建”的明确结论：两种方式都支持，但要区分成员记录和执行会话。**

| 层次 | 当前行为 | 是否临时 |
| --- | --- | --- |
| Team roster 中的 teammate 记录 | `team_spawn` 可以在同一次请求中接收 `teammates[]` 并创建/更新记录；若指定已有 teammate ID，会走 upsert。Runtime API 也支持先创建 Team，再通过 `POST /teams/{id}/teammates` 单独创建/更新成员。 | 记录一旦写入 Team store 就是持久化 roster，不是只存在于一次 task 调用里的临时对象 |
| 未显式提供成员时 | TUI 的 `team_spawn` 在 `auto_start=true`、存在 task 且 Team 尚无 teammate 时，会根据 task assignee 合成成员；没有 assignee 时按 task 数量（受 `max_teammates` 限制）生成类似 `mate-1` 的默认成员。 | 是“按需自动补齐”，但补齐后仍会写入持久化 roster；不应在 UI 中称为一次性临时 worker |
| teammate 的 `session_id` | 可以绑定已有 session；缺少时，aicli 会生成 Team/成员相关的规范化 session ID。aicli 的 local actor registry 在需要提交任务或投递消息时可加载/创建 runtime session。 | session 的创建可能延迟到真正使用时；因此 roster 已存在不等于 session 已 ready |
| 实际执行 | `TeammateRunner.StartTask` 要求 teammate 已有非空 `session_id`，随后才向该 session 提交 task prompt。 | task 是按需启动的，但执行目标是已绑定/已确保的 session，不是每次 task 都重新随机创建一个成员 |

因此，产品推荐的默认体验不是“用户先去全局创建一批成员”，而是**创建 Team 草稿时配置 roster，启动前选择复用已有 session 或自动 provisioning，并明确显示 readiness**。对于简单 TUI 请求，可以让 `team_spawn` 自动补齐默认成员；对于 Web 的可审计工作流，建议在启动前把自动生成的成员名称、session 来源和可执行状态展示出来。

因此，未来 Team builder 至少应提供：

- session binding 来源（已有 session、自动 provisioning 或待绑定）；
- worker readiness；
- 最近 heartbeat/状态；
- offline、blocked、busy 时对 Team 启动的影响；
- 重新绑定或移除 stale teammate 的操作。

### 3.4 Runtime API 已有的 Team 表面

Runtime router 已注册以下主要 Team API：

| 能力 | 当前 API 表面 |
| --- | --- |
| Team CRUD | `POST /api/runtime/teams`；`GET/PATCH/DELETE /api/runtime/teams/{id}` |
| Team 列表/汇总 | `GET /api/runtime/teams`；`GET /api/runtime/teams/summary` |
| Team 事件/summary | `GET /api/runtime/teams/{id}/events`；`GET /api/runtime/teams/{id}/summary`；`GET /api/runtime/teams/{id}/summary/final` |
| teammate | `GET/POST /api/runtime/teams/{id}/teammates`；`PATCH /api/runtime/teams/{id}/teammates/{teammate_id}`；heartbeat、sweep |
| task | `GET/POST /api/runtime/teams/{id}/tasks`；`GET/PATCH /api/runtime/teams/{id}/tasks/{task_id}`；graph、dependencies、replan、retry、outcome、ready/claim/reclaim |
| 协作 | `GET/POST /api/runtime/teams/{id}/mailbox`；ack；path claims 查询、检查、prune |

前端 client 目前已经封装了 Team 创建、列表、summary、teammate 列表/upsert、task 创建、task/graph/event/mailbox/path claims 等若干读写操作，但没有把所有已注册的 CRUD、成员 PATCH、task lifecycle 和 outcome 操作都暴露成完整的 Team 管理体验。

### 3.5 Web 当前入口与真实语义

Workspace 侧的入口大致是：

```text
Workspace sidebar
  → Runtime Teams surface/dialog
     → Teams：Directory + Team Details
     → Dispatch：Provision + Dispatch Monitor
```

当前已有的详情能力包括：

- Team roster（名称、profile、state、capabilities）；
- task queue 与依赖图；
- event timeline；
- mailbox 读取、发送和 ack；
- path claims 与冲突检查；
- final summary。

当前 dispatch 能力包括：

- 选择多个已有 Team；
- 对每个 Team 分别创建 task；
- 以 `Promise.allSettled()` 处理多 Team 派发结果；
- 派发后约每 4 秒刷新 monitor，显示 task status、assignee、summary、相关事件和 mailbox preview。

但 Provision 的真实结构是：

```text
Team 1 ── lead session + 1 worker teammate + 1 task
Team 2 ── lead session + 1 worker teammate + 1 task
Team 3 ── lead session + 1 worker teammate + 1 task
```

因为每个 ordinal 都单独创建 Team，并设置 `max_teammates: 1`，然后只 upsert 一个 teammate。它不是：

```text
一个 Team
  ├─ Worker A
  ├─ Worker B
  └─ Worker C
```

因此，现有 Provision 应在 UI 文案中明确标记为 **Fan-out 批量派发**，而不是“创建 N 个 Team 成员”。

### 3.6 Routing 与有效路由来源

Team routing 的有效配置选择为：

```text
如果存在 aicli.teams.routing
    使用独立 Team routing
否则
    继承/复用 aicli.subagents.routing
```

`spawn_team` 的任务路由通常在 task 执行上下文中根据以下信息解析：

1. Team 独立 routing 或 subagent fallback；
2. 当前 lead session 的 parent provider/model/reasoning defaults；
3. task 的显式 difficulty 与 rationale；
4. task type 产生的 difficulty floor 或策略约束；
5. teammate profile 形成的 role hint；
6. `write_paths` 等只读/写入信息；
7. provider/model 健康门禁、fallback 与 warning。

Team task 的 route 结果可包含：

- difficulty 与 difficulty source；
- provider、model、reasoning effort；
- route source；
- warnings；
- fallback 是否发生及原因；
- candidate evaluation。

产品上应把这些字段称为 **effective route / route audit**。不要把当前 Chat 的 `/provider`、`/model`、`/reasoning` 命令解释成“逐个设置所有 Team worker”；它们主要影响当前会话或 parent defaults，Team task 最终仍由 Team/subagent routing 和 task metadata 决定。

> **历史文档提示**：[`spawn-team-teammate-model-routing-plan.md`](./spawn-team-teammate-model-routing-plan.md) 的早期版本记录过“Team 只记录 difficulty、尚未应用 route”的状态。该结论对应当时的实现阶段，不应直接作为当前能力判断；当前代码已在 local actor registry 解析 Team task route，并由 teammate runner 传递、审计 task execution route。实现状态仍应以当前代码和回归测试为准。

### 3.7 普通 subagent 与 Team 的静态契约边界

从当前工具定义和宿主代码可以确认以下边界（这是代码事实，不是对用户体验完成度的断言）：

- `subagent_spawn` 的工具说明把对象定义为从当前 agent 派生的 child session，普通 child 的 completion contract 只支持 `none`；其结果通过普通 child 的 wait/events/status 体系回收，parent 可以使用 send/follow-up/input 等工具继续控制 child。
- `team_spawn` 的工具说明和实现面向 Team、teammate、task graph 与 Team lifecycle；`team_wait` 明确是 Team 的等待面，工具描述要求不要用它管理普通 child，也要求不要用 `subagent_wait` 管理 Team teammate。
- `team_send`、`team_mailbox` 以及 Team task 工具要求 active Team run；普通 `subagent_spawn` child 不会因为存在于 parent 的子代理树中就获得 Team mailbox、assignee、dependency 或 Team summary 语义。
- Team teammate 由 Team orchestrator/teammate runner 管理，普通 child 由 parent 的 child-session 监督面管理。两者即使底层都使用 session，也不能在产品层显示成同一类可暂停、可等待或可关闭对象。

因此，产品上不能用“一个 agent 对多个 agent”的数量描述替代生命周期边界：普通 subagent 的核心价值是**低配置的一次委派与 parent 跟进**，Team 的核心价值是**持久化协作单元中的成员、任务、依赖、消息和结果收敛**。具体 provider/model、session ID 和内部 registry 仍属于实现细节，不应成为普通用户的模式选择门槛。

## 4. 能力差距矩阵

| 能力 | 后端/Runtime | `aicli` Chat/TUI | 当前 Web | 闭环差距 | 优先级 |
| --- | --- | --- | --- | --- | --- |
| 创建/复用 Team | 已有 `team_spawn` 和 API | 工具可用，lead 由当前 Chat 绑定 | 有 API client，缺少清晰创建向导 | Web 需要可解释的创建流程 | P0 |
| 多成员 roster | 数据模型和 API 支持 | `teammates[]` 可一次声明 | 详情只读，Provision 是单成员 fan-out | 缺 add/edit/remove/binding UI | P0 |
| worker session binding | runner 强依赖 session，local registry 可补齐 | 可由 host provisioning | Web 创建 worker session，但无独立 readiness 管理 | 需要显式 readiness 与修复路径 | P0 |
| task graph | 依赖、ready、claim、replan 等已有 | `tasks[]` 可声明依赖 | 可查看 graph，创建 task 较基础 | 缺同一 Team 的图形化编排 | P1 |
| difficulty / route | resolver、fallback、audit 已有 | `doctor subagent-route` 可 dry-run | Settings 有 routing preview，但未接入 Team builder | 缺 Team task 执行前预览与结果对照 | P0 |
| 自动启动/等待 | supervisor、`team_wait` 已有 | 使用闭环明确 | Web 主要靠 API 查询和 polling | 缺生命周期按钮与统一状态语义 | P0 |
| mailbox 协作 | 持久化、发送、ack 已有 | Team worker/lead 可使用 | 详情可读写 | 需要与 task context、unread 状态联动 | P1 |
| task outcome | API 有 outcome 端点，summary/events 可读 | 可由 task report/block/report outcome | Web 主要显示 task 摘要和 final summary | 缺结构化 outcome、失败原因、retry 入口 | P0 |
| 运行控制 | status、retry、reclaim 等 API 已有部分基础 | 由 Team lifecycle 管理 | 未形成完整控制面 | 需要可验证的状态转换 | P1 |
| fan-out | Web 已能批量创建/派发到多个 Team | 可通过多个 Team 调用实现 | 当前最清晰的 Web 场景 | 需要和多成员 Team 视觉分离 | P0 |
| Team / subagent 模式选择 | 两套生命周期和结果模型均存在 | 工具描述已区分，但主要依赖模型理解 | 没有面向用户的模式选择/推荐卡 | 用户容易把“多个 agent”误认为同一种能力；需要以任务场景引导选择 | P0 |
| Team Definition / Run | 当前主要以 Team record 承载配置与运行状态 | `team_spawn` 可创建/复用 Team，但没有面向用户的 Definition/Run 选择层 | Directory、Details、Dispatch 尚未区分模板、配置与运行历史 | 需要定义 Run ID、snapshot、复制/重跑/归档语义 | P0 |
| Quick Start / 模板 | Team schema 能力较完整 | 依赖自然语言或完整 `team_spawn` 参数 | 没有默认轻量流程、模板和渐进式配置 | 新用户不应先填写全部工程字段 | P0 |
| Preflight | 后端已有若干校验、claim、readiness 和 route 信息 | 可在调用前诊断部分 route | 没有统一、可解释的启动检查报告 | 需要 blocker/warning/info、证据和跳转修复 | P0 |
| Attempt / artifact | task outcome、route audit、事件可持久化；artifact 产品层未统一 | 可报告 task outcome，但结果展示依赖调用方 | 主要显示摘要与 final summary | 需要保留每次 attempt、产物、验证和 retry 关系 | P1 |
| 失败恢复 | retry、replan、reclaim 等 API 表面分散存在 | 可根据 Team task contract 处理 blocked/failed | 没有按错误分类的恢复向导 | 需要区分配置、provisioning、provider、依赖、权限、网络、summary 等错误 | P1 |
| 权限/预算/成本 | routing 和写路径约束已有部分基础；预算执行未确认 | 可传递 read/write 等任务边界 | 缺少显式权限确认、成本/时延透明度 | 不能提供没有 Runtime enforcement 支撑的“硬限制”按钮 | P1/P2 |
| 嵌入式 Team Experience | Runtime API 可作为共享后端表面 | Chat 有 lead/session 上下文 | 当前组件以 Workspace Teams 为中心，缺少 host context 契约 | 需要统一 shell、权限、导航和 UI 生命周期独立性 | P1 |
| Run history / clone / archive | 当前有 Team/事件/summary 查询，完整历史语义未确认 | 可保存 team/task 结果，但没有稳定历史入口 | Directory 不是完整 Run History | 需要配置快照、运行历史、复制配置、重新运行和归档 | P1 |
| 事件与运营指标 | persisted events、cursor、route audit 等基础存在 | `team_wait` 可返回序列和终态信息 | 主要 polling，缺统一 event contract、成本/成功率指标 | 先做 cursor/backoff/last-known-good，再评估流式事件 | P1/P2 |

---
## 5. 两种产品模式必须分开

### 5.1 多成员 Team：一个协作图

多成员 Team 的目标是把一个较大的目标拆成同一 Team 内的多个 task，由多个 teammate 协作完成。典型结构：

```text
Team A
  ├─ reviewer      → review
  ├─ implementer   → implement（depends_on: review）
  └─ verifier      → verify（depends_on: implement）
```

适合：

- review → implementation → verification；
- 研究 → 汇总 → 决策；
- 多个文件域由不同 worker 处理，但需要同一 Team 的 mailbox、path claim 和最终 summary；
- 任务之间存在明确依赖、交接或 handoff。

这个模式的产品要求是：一个 Team 具有多个 teammate，task graph、assignee、依赖和协作消息都在同一个 Team namespace 内可见。

### 5.2 Fan-out：多个独立 Team 的并行实验

Fan-out 是把一个任务派发给多个独立 Team，各 Team 独立运行：

```text
Team 1 ── Worker 1 ── Variant 1
Team 2 ── Worker 2 ── Variant 2
Team 3 ── Worker 3 ── Variant 3
```

适合：

- 对多个 provider/model 或 prompt 变体做比较；
- 多份独立审查意见；
- 同一输入的并行尝试；
- 批量吞吐或候选方案筛选。

Fan-out 不应伪装成一个多成员 Team，因为它没有同一 Team 内的依赖图和 mailbox 协作语义。Web 当前的 Provision/Dispatch 主要属于这个模式。

产品上建议使用两个明确的入口名称：

```text
创建协作 Team（一个 Team，多成员）
批量 Fan-out（多个独立 Team）
```

并在 fan-out 结果中明确显示“每一行是一个独立 Team/task”，不要把所有 worker 合并成一个 roster。

### 5.3 Team 与普通 subagent：产品职责边界

本节中的 **普通 subagent** 特指 `subagent_spawn` 创建的 child session，不把 `subagent_spawn_batch` 或多个独立 Team 的 fan-out 混为同一种机制。产品上应按“工作是否需要共享协调”来区分，而不是按 agent 数量或预计时长来区分。

下表是基于当前工具和运行契约的静态代码事实归纳；最后一列是产品建议，不代表已有 UI。

| 用户可感知维度 | 普通 subagent（`subagent_spawn`） | Team（`team_spawn`） | 产品含义 |
| --- | --- | --- | --- |
| 用户交付的工作单元 | 一个边界清楚、可独立委派的任务及其初始提示 | 一个有共同目标的协作单元，可包含多个 teammate 和多个 task | 只需“把这一件事交给一个执行者”时，默认选择普通委派 |
| 拆解与依赖 | child session 围绕自己的任务执行；父 agent 负责需要时的汇总，不自带 Team task graph | task 是持久化记录，可声明 assignee、依赖、读写路径、deliverables 等，由 Team orchestrator 调度 | 存在明确的 review → implement → verify、handoff 或依赖关系时，选择 Team |
| 协作关系 | 以 parent-child 为中心；父侧可监督、发送消息或 follow-up，不等于 Team 内 peer 协作 | 成员处于同一个 Team namespace，可使用 Team mailbox、task context/report/block 等 Team 协作机制 | “多个 agent”本身不是 Team 的充分条件；要看是否需要共享协作上下文 |
| 生命周期与控制 | 通过普通 child 的 wait/events/list 与相应管理工具监督；child 属于 parent 的子代理树 | Team supervisor/orchestrator 管理 Team/task；调用方用 `team_wait` 等 Team lifecycle 表面观察 | 两类工作必须显示为不同对象，不能互相套用状态、等待和取消语义 |
| 结果形态 | child 返回执行结果，由 parent 决定是否继续追问、整合或结束 | task outcome、Team events、mailbox 和 final summary 构成可追踪的协作结果 | 用户若需要 task-by-task 进度、依赖状态和统一 Team summary，选 Team |
| 常见适用场景 | 单点调查、独立审查、一个范围明确的实现/验证子任务；也可由 parent 管理多个独立 child | 多阶段工作流、成员间需要交接、任务依赖/资源约束需要统一管理、需要持久化协作状态 | 用最轻量且能满足协调要求的模式；Team 不是“更高级所以总该优先” |
| 额外心智与开销 | 需要理解 child 的结果和 parent 的监督，但通常无需先配置 roster/task graph | 需要定义 Team、成员、任务边界及依赖，并理解 Team 状态、summary 和协作消息 | 界面要讲清 Team 带来的价值及额外配置，不应将更多配置误作更强默认值 |

**产品选择规则（建议）**

```text
只需交付一个清晰、彼此独立的子任务
  → 普通委派（subagent）

有多个互不依赖的独立任务，只需要并行产出、之后由用户/parent 汇总
  → 独立并行委派（按入口能力使用 subagent batch 等机制）

任务之间需要依赖、handoff、共享 mailbox/context、统一 task 状态或 Team summary
  → 协作 Team

需要同一输入由多个隔离环境/配置分别尝试并横向比较
  → Fan-out（明确显示为多个独立运行单元，不冒充一个 Team）
```

这里的“subagent batch”是独立任务的批量派发机制，不是 `team_spawn` 的别名；其能否在具体产品入口中使用，应以该入口实际暴露能力为准。普通 subagent 也可以被 parent 多次跟进，Team 也可以包含简单 task，所以“短任务/长任务”或“一个/多个 agent”只能作为提示，不能成为硬性限制。

**面向用户的入口与文案（建议）**

- 默认入口使用“委派一个独立任务”，不要求用户先理解 `subagent_spawn`、session 或 child agent 等内部名词。
- Team 入口使用“创建协作 Team”，紧邻说明“多个成员、可设置任务依赖与交接、共享进度和最终汇总”。
- Fan-out 入口使用“并行比较多个独立运行”，说明每个结果互相隔离；避免把“团队人数”作为 fan-out 的字段名称。
- 在选择前展示简短的场景问题，例如“任务之间是否要等待彼此、共享发现或由同一个协作进度面板跟踪？”回答“是”时推荐 Team；仅仅需要多份独立答案时不推荐 Team。
- 入口提供“推荐模式”和“查看差异”即可，不要强迫新用户阅读工具名、API 名或 routing 配置。高级用户仍可在 TUI/高级设置看到实际工具与有效 route。
- 对不确定或简单的请求，优先选择普通委派；只有明确需要 task graph、跨成员协作或持久化 Team 结果时才推荐 Team，避免 Team 配置开销和并行成本被隐藏。

**渐进升级与降级（产品提案，当前未实现保证）**

- 若普通 subagent 执行中发现目标已扩展为多个有依赖的工作项，产品可提示：“这项工作现在需要共享任务和交接；要不要把剩余工作整理成一个协作 Team？”经用户确认后，以现有 child 的已完成结果作为新 Team 的输入/交接材料。
- 这类升级应创建一个可追踪的新 Team，并明确旧 child 的继续、完成或关闭策略；不得把 child session 静默改造成 Team teammate，也不得丢弃旧结果或暗示两种生命周期可无损互换。
- 若用户发现 Team 对当前工作过重，提供“后续任务改用普通委派”的建议；正在运行的 Team 只能通过受支持的 lifecycle 操作结束/收敛，不能假设可原地降级成 subagent。
- 当前代码事实仅证明两种机制各自存在及其不同的生命周期/协作表面；上述模式推荐、升级/降级引导和跨模式交接均为待设计产品能力，需单独定义确认、权限、取消和审计行为。

**产品契约与实现细节**

- 应作为产品契约保持稳定：用户创建的对象类型、协作边界、状态/等待/取消含义、task 与最终结果之间的关系，以及跨成员信息是否共享。
- 可演进的实现细节：工具名、内部 session/agent ID、具体 orchestrator 算法、route resolver 的字段组织和事件存储方式。除非用于调试，高级内部术语不应成为普通用户完成工作的前置条件。

## 6. `aicli` TUI 的实际使用旅程

### 6.1 启动与准备

从终端启动 Chat/TUI：

```text
aicli chat
```

TUI 中没有必要先调用普通 `subagent_spawn` 再手工拼 Team。应直接用自然语言说明：

- 需要的是一个独立委派、多个互不依赖的独立结果，还是需要共享协作状态的多成员 Team；
- teammate 的角色/profile；
- task goal、依赖、读写路径和 deliverables；
- 是否需要自动启动并等待完成；
- 是否只读、是否允许写入。

用户不需要记住工具名，可以按工作方式描述：

| 用户诉求示例 | 建议模式 |
| --- | --- |
| “请独立审查这个配置文件，给我一份风险清单。” | 普通 subagent：一个边界清楚的独立委派 |
| “请从安全、可维护性和测试覆盖三个角度分别给我独立意见，不需要彼此协调。” | 多个独立委派；如果入口支持批量派发，可并行执行，不因数量多就自动升级为 Team |
| “先 review，再按 review 结论修改，最后由另一个角色验证；每一步都要跟踪状态并交接结果。” | 协作 Team：需要共享 task graph 和 handoff |
| “让几个隔离运行分别尝试方案 A/B/C，然后比较结果。” | Fan-out：结果彼此独立，之后横向比较 |

这是产品推荐示例，不代表当前 TUI 一定会以固定 UI 卡片展示这些选项；实际应由自然语言意图和工具说明引导正确执行机制。

例如：

```text
请创建一个多成员 Team，按 review → implement → verify 执行当前仓库的配置改造。
reviewer 只读并输出审查意见，implementer 只修改 backend/internal/config，verifier 运行相关测试并报告失败。
review 完成后才能 implement，implement 完成后才能 verify。自动启动 Team，最后汇总每个 task 的结构化结果。
```

Chat-originated Team 的 lead 应由当前 Chat session 绑定。用户不需要、也不应要求模型伪造一个与当前对话脱离的 `lead_session_id`。

### 6.1.1 TUI 可发现性与可预测交互（产品建议）

自然语言应保留为主路径，但不应成为唯一入口。首次使用时的帮助或等价快捷入口至少要能回答：何时用 Team、Team 与 subagent/Fan-out 的区别、如何查看当前状态、如何继续等待、如何查看结果、如何发送消息和如何恢复失败。以下命令名只是产品设计候选，不代表当前已经存在：

```text
/team help
/team status
/team show <team-id>
/team tasks
/team graph
/team events
/team mailbox
/team summary
/team wait [duration]
/team retry <task-id>
/team cancel
/team route
```

如果最终不新增 `/team` 命令，应提供等价的 slash help、内置快捷键或工具发现机制，不能把 Team 的可用性完全依赖模型临时推理。

`team_spawn` 成功后，TUI 应立即展示结构化回执：Team/Run 标识（若 Run 概念已落地）、运行状态、成员 roster、session readiness、task graph、route preview/audit、blocker/warning 和下一步动作。`team_wait` 期间应显示正在等待 Team 更新、等待时长、最近事件和 cursor；timeout 应明确写成“观察窗口结束，Team 是否继续运行”，而不是“Team 失败”。terminal 后仍要单独展示 summary pending/ready。

工具调用策略应固定为：先判定模式，再 spawn；Team 创建成功后沿用同一 Team 标识；沿用 `latest_seq`/`after_seq` 继续观察；task outcome 使用结构化 report/block/handoff；task failed/blocked 先展示原因再建议 retry/replan；retry 前提示额外调用和成本风险；不让 Team teammate 被普通 child wait 管理。

### 6.2 在执行前诊断 Team route

Team route 需要区分“配置层 route”与“当前会话临时切换”。推荐在 TUI 外先做不调用模型的 dry-run：

```text
aicli doctor subagent-route --scope team --workflow spawn_team --task-id review --difficulty hard --goal "review authentication and configuration changes" --json
```

如需模拟 writer task，可以增加：

```text
aicli doctor subagent-route --scope team --workflow spawn_team --teammate implementer --task-id implement --difficulty normal --write-path backend/internal/config --goal "implement the approved configuration change" --json
```

应关注输出中的：

- `scope` 是否为 `team`；
- `routing_source` 是 `team_independent` 还是 `subagent_inherited`；
- `routing_enabled`；
- effective `difficulty`、`provider`、`model`、`reasoning_effort`；
- `source`、`warnings`、`fallback_used`、`fallback_reason`；
- task type/role 或 write path 是否提高了 difficulty floor。

配置来源的解释是：

```text
存在 aicli.teams.routing → Team 使用独立配置
不存在 aicli.teams.routing → Team fallback 到 aicli.subagents.routing
```

TUI 中的 `/routing show`、`/routing doctor` 以及状态栏 route 投影可用于查看当前会话的路由状态；但它们不能替代 Team task 的最终 route audit。若 Team 有独立 routing，不能简单把 `/routing sub` 的结果当成 Team 最终值。

### 6.3 调用 `team_spawn`

下面是一个**概念性调用示例**，用于说明 Team task graph。它不是本次已执行的命令，`session_id` 应替换为当前运行时可用的 worker session，或者由当前 host 的 provisioning 逻辑补齐：

```json
{
  "auto_start": true,
  "strategy": "review-implement-verify",
  "max_teammates": 3,
  "max_writers": 1,
  "teammates": [
    {
      "id": "reviewer",
      "name": "Reviewer",
      "profile": "architecture reviewer",
      "session_id": "<reviewer-session>",
      "state": "idle",
      "capabilities": ["read", "review"]
    },
    {
      "id": "implementer",
      "name": "Implementer",
      "profile": "implementation worker",
      "session_id": "<implementer-session>",
      "state": "idle",
      "capabilities": ["read", "write", "test"]
    },
    {
      "id": "verifier",
      "name": "Verifier",
      "profile": "test and verification worker",
      "session_id": "<verifier-session>",
      "state": "idle",
      "capabilities": ["read", "test"]
    }
  ],
  "tasks": [
    {
      "id": "review",
      "title": "Review the change",
      "goal": "Review the relevant implementation and produce an actionable design review.",
      "assignee": "reviewer",
      "read_paths": ["backend/internal/config"],
      "deliverables": ["review findings", "implementation constraints"],
      "difficulty": "hard",
      "difficulty_rationale": "The review must identify cross-package compatibility risks.",
      "task_type": "understand"
    },
    {
      "id": "implement",
      "title": "Implement the approved change",
      "goal": "Implement the approved changes without modifying unrelated files.",
      "assignee": "implementer",
      "depends_on": ["review"],
      "read_paths": ["backend/internal/config"],
      "write_paths": ["backend/internal/config"],
      "deliverables": ["implementation diff", "tests or test notes"],
      "difficulty": "normal"
    },
    {
      "id": "verify",
      "title": "Verify the result",
      "goal": "Run the relevant tests and report failures, regressions, or missing coverage.",
      "assignee": "verifier",
      "depends_on": ["implement"],
      "read_paths": ["backend/internal/config"],
      "deliverables": ["test result", "remaining risks"],
      "difficulty": "hard",
      "task_type": "test"
    }
  ]
}
```

注意事项：

- `task_type` 是受控枚举；示例值仅表示意图，实际调用前应以当前 schema 暴露的枚举为准；
- `difficulty` 是路由/审计元数据，不是直接指定 provider/model 的快捷参数；
- Team worker 的 session binding 是执行前置条件；不能只创建 roster 记录就假设 worker 一定会执行；
- `auto_start: true` 表示后台已开始运行，不能在工具返回后把执行当成完成，也不应先问用户“下一步是否启动”。

`team_spawn` 结果至少应保存：

```text
team_id
teammate_ids
task_ids
created_team
auto_started
```

后续所有 Team 查询和等待都以返回的 `team_id` 为准。

### 6.4 使用 `team_wait`，正确处理 timeout

启动 Team 后，使用 `team_wait`，不要使用普通 subagent 的等待工具：

```json
{
  "team_id": "<team-id>",
  "timeout_ms": 120000,
  "limit": 24,
  "require_summary": true
}
```

`team_wait` 的结果需要按以下字段解释：

- `terminal`：Team 是否已达到终态；
- `summary_ready`：要求 summary 时，summary 是否可用；
- `timed_out`：本次观察窗口是否结束；
- `latest_seq`：本次返回的最新 Team event sequence；
- `pending_count` / `terminal_count`：任务账本的当前概览；
- `execution_continues` / `next_action`：timeout 后是否仍在执行以及下一步建议。

如果出现 `timed_out: true` 且 `terminal: false`，正确处理是：

```text
Team 仍在执行；timeout 只结束了本次观察窗口，不是 Team 失败。
继续处理独立工作或查看当前状态，之后从 latest_seq 继续 team_wait。
```

继续等待时应携带上一次返回的 `latest_seq`：

```json
{
  "team_id": "<team-id>",
  "after_seq": 42,
  "timeout_ms": 120000,
  "limit": 24,
  "require_summary": true
}
```

在 Team 尚未终止时不要使用：

```text
subagent_wait
subagent_events
```

这些是普通 `subagent_spawn` child 的管理面，不是 Team teammate 的生命周期面。Team teammate 由 Team orchestrator 和 `team_wait` 管理。

如果 `terminal: true` 但 `summary_ready: false`，且调用方要求 summary，应按照 `next_action` 再观察 summary；不能只看到 terminal 就假设 final summary 已经生成。

### 6.5 使用 mailbox 和 task 协作工具

在 active Team run 中，lead 或 worker 可以使用：

```json
{
  "team_id": "<team-id>",
  "to_agent": "*",
  "kind": "info",
  "task_id": "review",
  "body": "审查已完成，建议 implementer 只修改配置解析层。"
}
```

读取 mailbox：

```json
{
  "team_id": "<team-id>",
  "limit": 10,
  "mark_read": true
}
```

`team_send` 先持久化 mailbox 消息，再尝试分发；即使即时 dispatch 失败，也应在结果中展示 `dispatch_error`，并保留消息可追溯性。

需要改变 task 状态时，应优先使用 Team task contract（例如 task report/block/context/spec），不要用普通 child session 的 follow-up 语义替代 Team outcome。

### 6.6 TUI 常见失败与恢复

| 现象 | 可能原因 | 正确处理 |
| --- | --- | --- |
| Team 创建成功但 worker 不执行 | teammate 没有有效 session binding 或 session 未 ready | 查看 roster/session readiness，补齐或重新绑定 session 后再启动/重试 |
| Team-only 工具不可见 | 当前上下文没有 active Team run | 先由 lead 调用 `team_spawn`，不要手工调用内部 Team 工具 |
| `team_wait` timeout | 观察窗口耗尽，Team 仍在跑 | 使用 `latest_seq` 继续观察，不把 timeout 当失败 |
| terminal 但没有 summary | summary 仍在生成或 lead planner 未 settle | 按 `require_summary` 再等待或展示明确的 summary pending 状态 |
| route 与预期不同 | Team 独立 routing、subagent fallback、difficulty floor 或健康门禁生效 | 运行 `aicli doctor subagent-route --scope team --workflow spawn_team ... --json`，检查 source/warnings/fallback |
| task 被 blocked/无法 claim | 依赖未完成、writer 上限或 path claim 冲突 | 查看 task graph 和 path claims，解决依赖/冲突后 replan/retry |
| 把 Team 当成普通 child | 使用了 `subagent_wait` 或普通 child status | 切回 `team_wait`、Team events 和 Team task outcome |

## 7. Web 前端用户旅程

### 7.1 当前 Web 可用路径

在不新增代码的前提下，当前 Web 能支持的实际路径是：

```text
打开 Runtime Teams
  → Directory 查看已有 Team
  → 选择 Team
  → 查看 roster / tasks / graph / events / mailbox / path claims / final summary
```

以及：

```text
打开 Dispatch
  → 选择多个已有 Team
  → 为每个 Team 分别创建 task
  → Dispatch Monitor 轮询 task / event / mailbox
  → 进入具体 Team 查看完整详情
```

当前 Web 用户应把第二条路径理解为 **Fan-out**，而不是一个 Team 添加多个 worker。若要运行真正的多成员 Team，目前更可靠的入口是 `aicli` Chat/TUI 或直接使用已存在的 Runtime API/后端 provisioning 能力。

从产品体验上，这个 Runtime Teams 页面目前首先呈现 Team 目录和 Fan-out，不是在用户提交工作前帮助其比较“普通委派 vs 协作 Team”。目标体验应先呈现任务模式选择，再进入相应表单；不应把所有用户都导入 Team builder。

### 7.2 目标 Web 路径：创建一个多成员 Team

目标中的 Web 应先提供模式选择，再进入对应工作流：普通独立委派（仅在该 Web surface 提供通用 agent 委派时展示）、协作 Team、或 Fan-out。选择 Team 后使用渐进式配置，而不是一开始展示全部 runtime 字段。

#### 7.2.1 Quick Start、Guided 与 Advanced

**Quick Start（默认）**只要求用户提供目标、成功标准/期望交付和必要约束；提供模板或推荐角色；可以建议 task 拆分和成员安排；自动选择 routing 与 session provisioning 策略，但启动前必须可查看、可修改并通过 Preflight。若 task 拆分或 provisioning 不能由当前后端可靠支持，应保留用户确认步骤，不得用前端假装已完成。

**Guided（引导配置）**允许用户编辑 teammate 数量/职责、task list 与 dependencies、assignee、read/write scope、deliverables、difficulty/task type，以及 route preview。

**Advanced（高级配置）**才暴露稳定 ID、profile/capabilities、session binding、max teammates/max writers、strategy、route source/fallback、path claims、budget/permission、API payload 和诊断字段。可复用默认值应折叠，避免把完整 schema 变成 Quick Start 的表单。

#### 7.2.2 可修改的 Team 模板

首批模板可包含：

| 模板 | 默认角色 | 典型任务关系 |
| --- | --- | --- |
| Research & Synthesis | Researchers、Synthesizer | 并行研究 → 汇总 |
| Review → Fix → Verify | Reviewer、Implementer、Verifier | review → implement → verify |
| Multi-file Implementation | Planner、Workers、Integrator、Tester | plan → 并行实现 → 集成 → 测试 |
| Architecture Review | Security、Reliability、Maintainability、Lead | 并行评审 → 汇总风险 |
| Debug & RCA | Investigator、Hypothesis Tester、Verifier | 假设 → 证据 → 验证 |

模板是可编辑的配置起点，不是硬编码执行策略。启动前显示会创建的成员/任务数、并行与依赖关系、默认读写范围、可获得的成本/时延提示及需要确认的权限。

#### 7.2.3 独立 Preflight 与启动预览

Preflight 是可重复运行的产品能力，不只是 Start 按钮前的一次校验。报告至少分为：

- **Blocker**：当前无法安全或有效启动，例如缺失/不可用 session、未知 assignee、循环依赖、无效 task；
- **Warning**：可启动但需要用户知情，例如 route fallback、写路径竞争、预算未设置或实际 route 可能变化；
- **Info**：说明默认值、自动补齐成员、并发限制等行为。

每条报告包含受影响对象、证据/原因、影响、建议动作及跳转修复入口；修复后允许重新运行检查。Run preview 汇总 task/member 数量、依赖、有效 routing、权限和风险；只有具备可信数据时才显示成本/时延估计，并明确其不是保证值。

### 7.2.4 创建向导的模式说明

创建向导必须在第一屏就说明：

```text
这是一个协作 Team：成员共享同一组任务、依赖和协作状态。
如果任务彼此独立且不需要交接，可选择独立并行委派。
如果要让多个隔离运行分别尝试并比较结果，请切换到 Fan-out。
```

### 7.3 Web route preview 的复用方式

仓库已经存在通用的 route preview API/client：

```text
POST /api/runtime/config/document/agent-route-preview
```

前端类型支持 `scope: "team"` 和 `workflow: "spawn_team"`，Settings 页面也已经能够发起 Team scope 的预览。Team builder 不应复制一套 resolver，而应复用该 API/类型和后端 `modelrouting.Resolver` 的结果。

建议 task editor 在以下输入变化后提供“预览路由”按钮：

- teammate profile/role；
- task type；
- difficulty；
- read-only/write paths；
- goal；
- parent defaults 或 Team routing scope。

预览必须标注“这是 dry-run 结果，不代表已经调用 provider”，执行完成后再用 task route audit 对照实际结果。

### 7.4 Web 运行和观察语义

Web 需要将以下状态分开显示：

```text
provisioning     正在创建 Team/session/teammate/task
ready            roster 与 task 已准备，但尚未运行
running          Team loop 或 task 正在执行
waiting          有依赖、审批或外部输入
blocked          需要用户或系统处理
terminal         Team/task 已到终态
summary pending  已终态但 final summary 尚未可用
failed           执行或状态转换失败
```

Dispatch Monitor 的轮询可以继续作为轻量观察面，但必须：

- 在界面中显示“最近一次刷新时间”和“仍在轮询/已停止轮询”；
- 明确它只是 task/event/mailbox preview，不等同于完整 Team summary；
- 在用户进入 Team Details 时加载完整 task、graph、events、mailbox、path claims 和 final summary；
- 对 event cursor 使用 `after`，避免每次刷新重复拉取同一批事件；
- 对 terminal 但 summary pending 的 Team 提供继续刷新或手动刷新入口。

### 7.5 Run Monitor：从状态面板到执行控制中心

Run Monitor 的信息结构建议固定为：

```text
Run Header
  ├─ Team/Run 标识、状态、耗时、最近更新时间
  ├─ pause/resume/cancel（仅显示后端支持的动作）
  └─ health、warning、usage/cost（有可靠数据时）

Progress
  ├─ task graph、状态计数、critical path/当前瓶颈
  └─ active teammate、blocked task、summary readiness

Activity
  ├─ event timeline
  ├─ teammate activity
  └─ mailbox / handoff

Task Inspector
  ├─ task 状态、assignee、依赖与 scope
  ├─ attempt history
  ├─ route audit / fallback
  ├─ outcome、artifact、block reason
  └─ retry/reclaim/replan（按权限和状态启用）

Summary
  ├─ final summary 状态
  ├─ findings / decisions
  ├─ deliverables / unresolved issues
  └─ 下一步动作
```

进度应同时显示任务数量与状态计数，并说明这只是 task progress，不等于 token 或实际工作量百分比。Task Detail 以 Attempt 为中心，不能只覆盖最后一次尝试；每次 retry 都要保留 route、错误、产物和时间线。

### 7.6 运行控制与状态动作矩阵

先定义后端允许的状态转换，再映射 UI。建议至少明确以下语义：

| 当前状态 | 操作 | 产品结果 | 必须提示 |
| --- | --- | --- | --- |
| draft/ready | Start | 创建/激活 Run | preflight、启动确认、幂等行为 |
| running | Pause | 停止新 task 调度；正在执行 task 的策略需明确 | 哪些 task 会继续、是否可恢复 |
| paused | Resume | 恢复 reconcile/scheduler | 将恢复哪些 task |
| running/blocked | Retry task | 创建新 attempt | 失败原因、输入是否复用、额外成本 |
| running | Replan | 重算未完成 graph | graph diff、影响范围 |
| running/blocked | Cancel Run | 受控终止 | 影响范围、不可逆操作确认 |
| terminal | Retry failed | 新 attempt 或新 Run，必须明确 | 保留旧结果 |
| terminal | Run again | 基于 snapshot 创建新 Run | 配置来源和新 Run 标识 |

如果当前后端尚未验证 pause/cancel/budget 等严格语义，UI 必须隐藏或 disabled 并说明原因，而不是提供点击后没有真实效果的“假控制”。

---
## 8. 最小 API/UI 改造清单

本节是“让后端能力真正可用”的最小闭环，不要求一次性完成所有高级编排能力。

### 8.1 P0：先让用户能创建、运行并理解一个多成员 Team

#### A. 明确区分普通 subagent、Team builder 与 Fan-out

- 将当前 Provision/Dispatch 改名或增加说明为“Fan-out 批量派发”；
- 新增“创建协作 Team”入口；
- 在“委派独立任务”“创建协作 Team”“并行比较多个独立运行”入口上分别解释协作方式、结果形态和适用场景；
- 通过场景问题推荐模式，不要求用户先知道 subagent、Team、fan-out 等后端术语；
- TUI 的工具说明/提示词也使用同一选择规则，避免相同请求仅因入口不同而切换到不同执行机制；
- 在 Team 列表中显示 Team 类型或创建来源，避免用户把 fan-out 的 N 个 Team 当成一个 Team 的 N 个成员。

#### B. Team 创建向导

复用现有 `POST /api/runtime/teams`，至少提供：

- workspace；
- strategy；
- max teammates；
- max writers；
- 初始 status；
- auto-start/保存草稿的产品语义。

前端需要补齐对 `GET/PATCH/DELETE /api/runtime/teams/{id}` 的 client 与状态管理，使编辑、暂停/恢复、归档/删除的能力不只停留在后端路由层。具体状态转换必须由后端现有合法状态语义约束，不能由前端任意拼接字符串。

#### C. teammate 管理与 session readiness

复用：

- `GET/POST /teams/{id}/teammates`；
- `PATCH /teams/{id}/teammates/{teammate_id}`；
- heartbeat、sweep；
- session runtime 状态读取。

前端需要增加：

- add teammate；
- edit profile/capabilities/name；
- replace/rebind session；
- remove/disable stale teammate；
- readiness badge；
- 最近 heartbeat 与 offline reason；
- 启动前阻止“roster 存在但没有可执行 session”的隐性失败。

当前 API client 已有 `upsertRuntimeTeammate`，但完整的成员生命周期需要显式的 PATCH、删除/禁用语义和 readiness 展示。若后端没有安全的物理删除语义，UI 应采用 disable/offline/retire，而不是自行删除持久化记录。

#### D. task graph 编辑器

复用现有 task API：

- `POST /api/runtime/teams/{id}/tasks`；
- `PATCH /api/runtime/teams/{id}/tasks/{task_id}`；
- dependencies/dependents；
- graph；
- ready/claim/replan/retry/outcome。

第一版可以不是复杂的画布编辑器，表单和列表即可支持：

- task title/goal；
- assignee；
- difficulty 与 rationale；
- task type；
- read paths/write paths；
- inputs/deliverables；
- depends_on；
- priority。

需要在保存前做本地校验：重复 ID、未知 assignee、自依赖、循环依赖、写路径重叠、writer 上限和空 goal。

#### E. Team route preview

在 Team task editor 中复用 `agent-route-preview`，对每个 task 显示：

```text
scope = team
workflow = spawn_team
routing source = team independent / subagent inherited
resolved difficulty
provider / model / reasoning effort
source
warnings
fallback used / fallback reason
```

该面板必须明确：

- route preview 是配置 dry-run；
- task payload 不能任意指定 provider/model/tool policy；
- 执行时的 route audit 才是本次 attempt 的事实；
- Team 独立 routing 不存在时，显示继承 subagent routing，而不是显示成“没有 Team 配置”。

#### F. 运行控制与终态

运行控制可以先复用已有 status、retry、replan、reclaim 和 outcome API；若直接 PATCH status 会绕过业务校验，则应增加后端统一的状态转换服务，但不应在前端自行实现状态机。

第一版至少要具备：

- start/activate；
- pause；
- resume/reconcile；
- retry failed/blocked task；
- cancel/close（按后端允许的终态）；
- refresh/continue summary；
- 明确显示 timeout 不等于失败。

### 8.2 P1：结果、协作和审计完整化

#### A. task-level outcome

Team Details 当前能显示 task 摘要，但需要增加结构化 outcome 面板：

- outcome 类型：done/failed/blocked/handoff 等；
- outcome summary；
- changed paths 或 deliverables；
- failure reason；
- attempt/retry count；
- route audit；
- associated events/mailbox；
- artifact reference、validation status、source attempt 和 summary 引用（若 runtime 能提供）。

如果 task outcome API 已存在但前端没有 client，应优先补 client 和 query invalidation，而不是新增另一套 result API。Outcome schema 必须先核对已有 `team_task_outcome_contract.md`，不要未经契约审查另造冲突字段。产品上应区分“执行完成”和“结果接受”：按任务风险选择自动接受、lead review、用户批准或 verifier review；不是所有 task 都需要人工批准。

Artifacts 应展示文件名/类型/大小（可得时）、来源 task/attempt、preview/open/download（按权限和存储能力）、changed paths、是否已验证、版本与保留规则。重试后新旧 artifact 的关系必须可追溯；无法提供的能力标为待补，不制造虚假链接。

#### B. event cursor 与实时性

Timeline 当前主要读取一批 persisted events，未形成完整的实时 SSE 体验。第一版可以保持 polling，但要：

- 记录 `after` cursor；
- 显示事件时间和来源；
- 终态后停止高频 polling；
- summary pending 时降低频率或提供手动刷新；
- 后续再评估 SSE/live channel，不把实时流作为 P0 的前置条件。

#### C. mailbox 与 task context 联动

详情页应支持：

- 按 task、agent、kind、unread 过滤；
- 从 task 直接发送消息给 assignee 或广播；
- 展示 message 是否已 ack；
- 将 mailbox message 与事件 timeline 互相跳转。

#### D. 失败分类与恢复向导

失败或 blocked 状态不应只显示一个通用 Retry 按钮。至少区分 configuration、provisioning、provider、execution、dependency、conflict、context、permission、runtime/network 和 summary failure，并在详情中说明：

- 发生了什么、影响哪个 task/Run；
- 是否可自动恢复、是否会产生额外成本；
- 推荐动作及其前置条件；
- retry task、修改输入后 retry、从依赖恢复、重试 provisioning、独立重试 summary、retry all（需确认）或 clone run 的差异；
- 恢复完成后会创建新 Attempt、改变 graph，还是创建新 Run。

自动 retry 必须由 Runtime 提供次数上限、退避和错误白名单；前端不能把可重试错误规则硬编码成调度器。

#### E. 权限、预算与透明度边界

- read-only task 默认最小权限；writer 必须明确 write paths，不能隐式扩大；
- task 权限变化、destructive action、approval 和 session rebind 需要后端检查并留下审计；
- cost、token、duration 和 concurrency 只有在底层数据可靠时才显示估算/统计；“无法预估”优于虚构金额；
- max budget、hard stop、pause 等严格限制必须由 Runtime enforcement 支撑，前端未获契约支持时显示为 unavailable 或 warning，不提供假按钮。

### 8.3 API client 缺口清单

当前 `frontend/src/api/runtime/teams.ts` 已有若干 Team 查询和创建方法，但建议补齐以下薄封装，保持 API 与后端路由一一对应：

| 前端 client | 用途 |
| --- | --- |
| `getRuntimeTeam` | builder 编辑与刷新单个 Team |
| `updateRuntimeTeam` | strategy、limits、status 等受控更新 |
| `deleteRuntimeTeam` 或 `retireRuntimeTeam` | 归档/删除语义，取决于后端状态契约 |
| `updateRuntimeTeammate` | 成员属性与 session binding 更新 |
| `list/getRuntimeTeamTask` | task editor 和 task outcome 详情 |
| `updateRuntimeTeamTask` | 编辑 goal、assignee、metadata、状态 |
| dependency/replan/retry/outcome client | task graph 和失败恢复 |
| route preview wrapper | Team builder 内部使用 `scope=team`、`workflow=spawn_team` |
| readiness/heartbeat summary | 启动前显示 worker 可执行性 |

这些方法的目标是**暴露已有后端契约**，不是把所有能力都搬到一个巨大 hook 中。建议按 Team、teammate、task、observation、lifecycle 分模块，并统一 query key 和错误对象。

## 9. 后端复用与边界设计

### 9.1 Web 不复制 orchestrator

推荐的调用关系：

```text
Web Team builder / dispatch
  → Runtime API
     → team store + lifecycle service
        → supervisor/orchestrator
           → teammate runner / session runtime
```

Web 只负责：

- 组装经过校验的 Team/task 请求；
- 展示 server state；
- 调用受支持的状态转换；
- 维护 event cursor 和查询缓存。

Web 不负责：

- 自己 claim task；
- 自己决定依赖完成；
- 自己实施 writer/path lock；
- 自己判定 terminal；
- 自己把 timeout 映射为 failed。

### 9.2 TUI 与 Web 使用同一状态口径

两种入口都应遵循：

| 语义 | 统一口径 |
| --- | --- |
| active/running | Team 仍可能有未完成 task，不能显示为完成 |
| timeout | 观察窗口结束，execution 是否继续由 `execution_continues`/Team state 决定 |
| terminal | Team 已达到终态，但 summary 可能仍 pending |
| summary ready | final summary 可被消费 |
| task failed | task outcome 失败，不一定等价于整个 Team failed |
| blocked | 需要依赖、资源或用户处理，不等于永久失败 |
| route fallback | 仍是有效 route，但必须显示 warning/reason |

### 9.3 Routing 的职责边界

推荐将 route 责任分成三层：

1. **配置层**：`aicli.teams.routing` 或继承 `aicli.subagents.routing`；
2. **解析层**：根据 scope、workflow、parent defaults、difficulty、task type、role 和健康状态得到 effective route；
3. **执行审计层**：在每次 task attempt 记录实际 provider/model/reasoning、source、warning 和 fallback。

不要让 Web 表单把“provider/model/reasoning”直接写入 Team task 作为长期 worker 属性。一个 teammate 可能先执行 easy research，再执行 hard implementation，route 更适合绑定 task execution attempt，而不是永久绑定 teammate。

### 9.4 Team Embedded Experience

如果 Team 要嵌入 Workspace Chat、项目页面、IDE 或任务详情，不应让每个 host 复制一套 Team UI。建议建立 `TeamExperience` shell 和 host adapter：

```text
TeamExperience
├─ ModeSelector
├─ TeamBuilder
│  ├─ GoalEditor / TemplatePicker
│  ├─ MemberEditor / TaskGraphEditor
│  ├─ RoutePreview / PreflightPanel
├─ TeamRunMonitor
│  ├─ RunHeader / TaskGraph / ActivityTimeline
│  ├─ MemberStatus / Mailbox / TaskInspector
├─ TeamResults
└─ TeamHistory
```

Host context 应明确提供并版本化：

| Host | context | Team UI 不应假设 |
| --- | --- | --- |
| Workspace | workspace ID、权限、文件树、route config | 默认可写整个 workspace |
| Chat | lead session、conversation context、active run | 可伪造 detached lead session |
| IDE | project、当前文件/selection、编辑状态 | 用户同意修改所有文件 |
| Project page | project、branch、repository 状态 | 当前 branch 一定可写或干净 |
| Standalone | 用户权限、默认 workspace、runtime capability | 一定存在当前 Chat session |

运行生命周期不得绑定组件生命周期：关闭 drawer、切换页面、刷新浏览器或 host unmount 不应意外取消后台 Run；Run/Team 标识应可通过 URL 或 host navigation 恢复。嵌入层只调用稳定 facade，权限仍由后端 enforcement，不能把前端 props 当作安全边界。

### 9.5 API capability、错误和幂等契约

Runtime API 应按用户操作而不只是 endpoint 组织 capability inventory：

```text
Create/Save draft
Provision/Rebind members
Create/Update tasks
Preflight
Start/Observe
Pause/Resume/Retry/Replan/Cancel
Outcome/Summary/Artifacts
History/Archive/Clone/Run again
```

每项能力都应说明支持状态、权限、合法状态、幂等键/版本要求和错误恢复方式。建议统一错误 envelope，至少包含稳定 `code`、`category`、`retryable`、结构化 `details`、可执行 `actions` 和 `request_id`：

```json
{
  "error": {
    "code": "TEAM_PREFLIGHT_SESSION_NOT_READY",
    "message": "A teammate session is not ready.",
    "category": "preflight",
    "retryable": false,
    "details": {
      "teammate_id": "verifier",
      "reason": "session_missing"
    },
    "actions": [
      {
        "type": "rebind_session",
        "label": "Bind a worker session"
      }
    ],
    "request_id": "..."
  }
}
```

UI 不应解析通用 message 文本决定流程。创建 Team/Run、Start、Retry、Replan、Cancel 和网络重试需要明确 idempotency key 或 optimistic concurrency/version；API timeout 后应先按 ID 查询真实状态，不能盲目重建。部分 provisioning 失败应留下可修复状态或提供回滚语义，并返回已创建对象的标识。

### 9.6 Run snapshot 与配置变更

目标 Run snapshot 至少应包含 Team settings、成员 profile/capability、session binding 来源、task graph、routing config/version、context references、permissions/write scopes、budget/limits 和创建来源（TUI/Web/API/template/fan-out）。运行中配置变化应分成：

- 不影响当前 Run 的 Definition 修改；
- 明确支持的 live update；
- 需要 replan 的修改；
- 不允许改变的 immutable 字段。

“Clone config”应只复制 Definition 不启动；“Run again”应由 snapshot 创建新的 Run；“Retry failed task”应在当前 Run 中创建新的 Attempt。三者的按钮、URL 和历史记录必须明确区分。

### 9.7 Event、History 与运营指标

目标事件模型应覆盖 Team/Run 的创建、Preflight、启动、暂停/恢复、取消、终态和 summary；teammate 的 provisioning/ready/offline/rebind；task 的 ready/claim/start/wait/block/complete/fail/retry/replan；attempt 的 route resolution/fallback/completion/failure；mailbox 的 sent/delivered/ack；以及 artifact 的 created/validated/accepted。事件至少应带 event ID、Team/Run/Task/teammate/Attempt ID、时间、sequence/cursor、source、actor、结构化 payload 和 request/correlation ID。具体字段需与已有事件契约合并，而不是由各 UI 另造格式。

首期以 snapshot + cursor polling、退避、jitter、可见性降频、终态停止高频刷新、summary pending 低频刷新和事件去重为目标；SSE/WebSocket 及 replay 放到后续阶段。网络错误时保留 last-known-good，不要用空响应覆盖真实状态。

Team Directory 与 Run History 应分开：Directory 管理 Team Definition/Template 的搜索、workspace、owner、archived 和最近运行状态；Run History 保留 goal、配置/roster/session/task graph snapshot、attempt、实际 route、events、mailbox（按权限）、outcome/artifact、summary、usage/cost 和 terminal reason。历史能力必须先确认存储与保留策略，不把当前 Team 查询误称为完整 Run History。

## 10. 里程碑与交付顺序

本路线按 gap review 的 Phase A–F 重排。先统一可见的产品对象和状态契约，再交付 Web/TUI 最小闭环，然后补齐可靠性、历史和嵌入，最后投入实时运营能力。这里描述的是目标交付，不表示对应功能已存在；每一阶段都要先盘点可复用 API，再决定缺少的后端契约，避免 UI 自行模拟执行能力或重写 orchestrator。

### Phase A：产品契约（P0，设计先行）

交付：

- 确认 Team Definition、Team Run、teammate、worker session、task、attempt、outcome、artifact 的对象职责和生命周期；
- 决定 Definition 与 Run 是否分离、一个 Definition 是否可多次 Run、Run ID 和 snapshot 的持久化/导航语义；
- 统一普通 subagent、协作 Team、独立并行委派和 Fan-out 的用户术语、推荐规则及升级边界；
- 定义状态图、状态转换表、timeout/terminal/summary 语义和允许的控制动作；
- 确认 API error taxonomy、幂等与并发更新契约、事件和 cursor schema；
- 确认 Host Context、最小权限、写路径、session provisioning/readiness 与 Run snapshot 边界；
- 对照现有 Runtime API、TUI tool 与前端 client，形成“已有、部分可用、缺失、待验证”清单。

完成标志：产品和工程可用同一套对象/状态图解释用户创建的究竟是定义还是运行，以及每个动作会改变哪个对象；所有未验证的后端能力均标记为待验证，而不是作为 UI 的既有前提。

### Phase B：Web Team Quick Start（P0）

交付：

- 协作模式选择；Team 与 Fan-out 分开入口；
- 从目标开始的 Quick Start，并可切换 Guided/Advanced 配置；
- 可编辑的 starter recipe/template（不要求首期建设共享模板库）；
- Team draft、roster/member provisioning/rebind/readiness；
- task list、assignee、dependency 和基础 graph；
- 可重复运行的 Preflight，提供 blocker/warning/info 和修复入口；
- 预览有效 route、权限/写入范围和可执行限制；没有后端 enforcement 的预算硬限制或控制动作不得呈现为可用；
- Start Run、Run Monitor、task 状态、summary/result 基础闭环；
- 稳定 URL/对象 ID，在刷新或离开页面后恢复可观察的 Run。

完成标志：用户能创建一个至少有两个 teammate、两个 task 和一个 dependency 的协作 Team；能区分 roster 记录与 session readiness，并在启动前发现不可执行项。Web 调用 Runtime API 与既有生命周期服务，不自建调度、claim 或终态判定。

### Phase C：TUI 体验闭环（P0/P1）

交付：

- `aicli chat` 中可发现的 Team/subagent/Fan-out 帮助或等价快捷入口；命令形式（如 `/team`）须先确认，不视为现有命令；
- 自然语言创建 Team 后输出对象摘要、成员/task 清单、`team_id` 和下一步；
- `team_spawn → team_wait → after_seq` 继续观察的清晰反馈；
- status/tasks/summary 等查看路径，以及 Team-only 工具的 active context 说明；
- timeout、task failure、Team terminal、summary pending 的区别与继续/恢复建议；
- routing preview、fallback 和实际 attempt route 的诊断入口；
- retry/replan/rebind 等动作仅在后端能力和语义确认后开放。

完成标志：用户不必记住内部工具名即可发起和跟踪协作 Team；高级用户仍可检查工具名、对象 ID、effective route 和事件 cursor。Team Run 由 Team lifecycle 表面管理，不与普通 child wait 混用。

### Phase D：Embedded Team Experience（P1）

交付：

- 可复用的 Team Experience shell 与 Host Context adapter；
- Compact Run Card、Task Inspector、完整 Monitor/Results 视图；
- Workspace 及至少一个额外 host 的集成验证；
- URL/navigation 恢复、loading/empty/error/offline、键盘与响应式交互；
- host unmount、切换页面或关闭 drawer 不取消后台 Run；订阅在卸载时清理；
- 权限由后端验证，host props 不作为授权边界。

完成标志：相同的 Team Run 在不同 host 中沿用同一对象 ID、状态和结果契约；host 上下文缺失时有明确降级路径，组件生命周期与后台 Run 生命周期分离。

### Phase E：Reliability、Recovery 与 History（P1）

交付：

- API 幂等键/并发版本、超时后查询真实状态、部分 provisioning 的恢复/回滚；
- 以 Attempt 为中心的 task history，保留失败记录和实际 route；
- 按失败类别提供 retry/replan/rebind/retry summary 等恢复流程，并显示影响范围、风险和成本提示；
- 结构化 outcome、artifact、验证状态、changed paths 和 summary 引用关系；
- cursor polling、退避、去重、last-known-good state 与 summary pending 低频刷新；
- Run history/snapshot、clone config、run again、当前 Run 内 retry 的不同语义；
- read/write scope、approval、事件审计和可用数据范围内的 usage/cost 透明度。

完成标志：网络重试不会意外创建重复 Run；重试会保留旧 Attempt；部分失败有可追踪恢复路径；用户能从 Run/Task 追溯配置快照、结果产物和验证证据。History、budget enforcement 和权限策略若后端不支持，需先补契约及 enforcement，不得只做前端展示。

### Phase F：Advanced Live Operations（P2）

候选交付：

- 在 polling 已验证为瓶颈后引入 SSE/WebSocket、event replay 和 cursor reconnect；
- 运营指标、route/latency/quality 分析及有可靠数据支撑的成本估算；
- Fan-out 横向比较与聚合、共享模板库、归档/保留策略；
- 大规模 Team 的搜索、分页、批量操作、通知和 approval queue；
- 多用户/RBAC 与运营审计深化。

完成标志：高级运营能力建立在统一事件、Run、权限和结果契约上；不把多个独立 Fan-out 运行伪合并为一个 Team。

## 11. 验收标准

### 11.1 TUI 验收

1. 在 `aicli chat` 中明确要求创建多成员 Team，模型调用 `team_spawn`，返回并保留 `team_id`。
2. Team 至少包含两个 teammate 和两个 task，其中一个 task 依赖另一个。
3. `auto_start=true` 时，调用方随后使用 `team_wait`，不使用 `subagent_wait`。
4. 人为缩短观察窗口后，返回 `timed_out=true` 时，UI/助手说明 Team 是否继续执行，且可以使用 `after_seq=latest_seq` 继续等待。
5. Team terminal 后，能够区分 `terminal`、`summary_ready` 和 task-level outcome。
6. `team_send`/`team_mailbox` 只能在 active Team context 使用，消息可追踪并显示 dispatch error（如果有）。
7. `aicli doctor subagent-route --scope team --workflow spawn_team ... --json` 能输出 Team scope、routing source、effective route 和 fallback/warning。
8. 缺失 worker session 或 route fallback 时，用户能看到可操作的原因，而不是只有“Team 已创建”。
9. Help/等价入口能解释 Team、普通 subagent、Fan-out 的区别；命令候选（如 `/team`）只有在实际实现并验证后才写成可用命令。
10. Team spawn 反馈包含对象摘要、成员/task 数、持久化 `team_id` 和下一步查看/等待方式。
11. 重试/replan/rebind 提示受影响范围、风险和可能的额外成本；不支持的动作不伪装为可用。

### 11.2 Web 验收

1. 用户能区分“创建协作 Team”和“Fan-out 批量派发”。
2. 创建协作 Team 时，一个 Team 下能添加至少两个 teammate，并显示每个 worker session readiness。
3. 用户能为同一 Team 创建至少两个 task、指定 assignee 和 dependency，并在 graph 中看到关系。
4. 启动前可执行性检查能报告：缺失 session、未知 assignee、循环依赖、path conflict、writer limit 等原因。
5. 每个 task 能预览 Team effective route，并显示独立 Team routing 或 subagent inherited 来源。
6. 运行中显示 Team/task 的真实状态；polling timeout 或刷新失败不会被渲染成 task failed。
7. Team Details 能从 task 进入 structured outcome、events、mailbox、path claims、route audit 和 final summary。
8. Fan-out Monitor 能同时展示多个独立 Team/task，并明确每行的 team boundary；详情页仍可单独查看完整信息。
9. terminal Team 的 summary pending 状态有明确刷新/等待动作。
10. Quick Start 能从目标起步，并允许进入 Guided/Advanced；模板/recipe 可编辑，不要求用户先了解 session ID 或 routing resolver。
11. Preflight 可重复运行，分别呈现 blocker、warning、info，并提供修复路径；结果由稳定后端契约支持。
12. Start Run 重复点击或网络重试不产生重复运行；刷新/导航后可通过稳定 URL/ID 恢复状态。
13. Monitor 保留 last-known-good 状态；轮询失败或观察 timeout 不覆盖为 task/Run failed。
14. Task inspector 可追溯 attempt、结构化 outcome、artifact/changed paths、验证状态和实际 route；缺失能力应清楚标记而非制造空成功。
15. pause/resume/cancel/retry/replan 等动作按后端状态转换契约展示，且 race/冲突时反馈最终 server state。

### 11.3 API/契约验收

- `team_spawn`、Runtime API 创建和 Web builder 创建出的 Team 使用同一持久化模型；
- 不同入口创建的 task graph、dependency、outcome 和 event 可以互相读取；
- session binding、route audit、terminal summary 不依赖某一个 UI 的内存状态；
- Team wait 的 `after_seq` 语义在 TUI 和 Web event 查询中一致；
- API 错误包含可行动的错误类别，而不是只返回通用 500。
- Web/TUI/Embed 使用一致的 Team 与 Run ID、状态、事件 cursor 和结果引用；入口来源可审计。
- Start、Retry、Replan、Cancel 等有明确幂等/版本策略；timeout 后可先查状态，不能盲目重建。
- Run snapshot 包含约定的配置、roster/session binding、task graph、route、permission scope 和创建来源；若未实现则作为待交付契约，不宣称当前已有。
- Worker/session provisioning 部分失败能查询已创建对象并重试、rebind 或回滚，不留下无法解释的半成品。
- UI 所显示的权限、预算、pause/cancel 能力由 Runtime enforcement 支撑；权限边界不能依赖前端传入的 host context。

### 11.4 产品易用性验收（待用户测试）

以下是需要通过可用性测试验证的目标，不是当前已经测得的结果：

1. 首次使用者只阅读“委派独立任务 / 创建协作 Team / 并行比较多个独立运行”的场景说明，就能选出合适模式，不需要先知道 `subagent_spawn`、`team_spawn` 或 `team_wait` 的名称。
2. 用户能用自己的话复述选择结果：普通 subagent 是一次有边界的委派，Team 是需要共享协调/依赖/汇总的协作项目，Fan-out 是多个隔离运行；不会把“成员数”当成唯一差异。
3. 用户能解释为什么系统推荐某种模式，并能在启动前主动改选另一种模式；推荐不是不可逆的隐藏决策。
4. 用户不会因为 Team 的 roster、task graph、route 和 summary 配置而误以为“Team 一定更强”或“普通 subagent 不能完成复杂工作”；界面应展示相应收益、额外配置和时延/成本提示。
5. 用户能区分 Team 的运行中状态、`team_wait` 的观察窗口 timeout、task 失败和 final summary pending，并能找到下一步动作。
6. 用户从普通 subagent 需求升级到协作 Team 时，能看到旧任务结果、交接内容和新 Team 的边界；系统不会静默转换或重复执行同一工作。

**建议的小样本首轮测试门槛**：邀请 5–8 名目标用户（至少包含不熟悉 agent 工具术语的用户），完成“单点独立委派”“多个独立意见”“依赖式多阶段工作”“多方案隔离比较”四类任务。建议至少 80% 的模式选择在不培训的情况下与预期协作需求相符；不得出现把 Team 当作普通 child、用普通 child 的 wait 管理 Team，或把 Fan-out 理解成同一 Team roster 的关键误解。样本较小时结果用于发现可用性问题，不宣称具有统计代表性。

### 11.5 Recovery、Results 与嵌入验收（待实现/待验证）

- Provisioning 部分失败能显示已创建的 Team/member/session 标识，并可重试、rebind 或按约定回滚；不能留下无提示的半成品。
- Task retry 会创建新 Attempt 并保留旧失败原因、route、产物和时间线；Retry、Replan、Run again、Clone config 的影响范围和目标对象不混淆。
- API timeout、刷新失败、浏览器离线后恢复时，先查询服务端状态；界面保留 last-known-good 信息，不重复创建 Run，也不把观察错误覆盖成任务失败。
- Summary generation 失败可独立呈现并按契约重试；summary 有 source task/outcome 引用，用户可追溯其依据。
- 同一 Team Experience 在 Workspace 与至少一个额外 host 复用；host context 缺失有 graceful fallback，host unmount/navigation 不影响后台 Run。
- 在 host 集成中验证键盘操作、responsive、loading/empty/error/offline 状态和订阅清理；授权由 Runtime enforcement 决定。

## 12. 测试策略

### 12.1 单元和契约测试

- `team_spawn` 参数默认值、当前 Chat lead binding、difficulty/task_type 校验；
- `team_wait` timeout、clamp、after_seq、summary pending；
- Team-only tool 的 active context gating；
- route scope 选择、Team 独立配置与 subagent fallback；
- session binding 缺失、重复 teammate/task ID、dependency cycle、path claim conflict；
- Web API client 的 request/response 类型和错误映射；错误 code/category/actions 与 retryable 映射；
- Definition/Run、Run snapshot、Run ID、幂等键和 optimistic concurrency 语义；若这些契约尚未实现，应将测试列为后续后端工作，不以 mock 假通过。

### 12.2 Runtime 集成测试

使用 fake store/session/dispatcher 验证：

- 一个 Team 多成员的依赖调度；
- fan-out 多 Team 的隔离；
- worker session offline、heartbeat stale、reclaim；
- task retry 后 attempt 和 route audit；
- terminal event、summary 生成和 teammate cleanup；
- mailbox 持久化成功但 dispatch 失败时的可追踪性；
- provisioning 部分失败后的恢复/回滚，API timeout 后按 ID 查询避免重复 Run；
- retry/replan/rebind/cancel race 的最终状态一致性，summary generation 独立失败与重试；
- Run snapshot、attempt history 和 outcome/artifact 关联在进程重启/重新查询后保持可追踪。

### 12.3 Web 测试

- builder 创建两个 teammate 和依赖 task graph；
- builder 与 fan-out 的视觉和文案分离；
- route preview 的 Team scope request；
- polling cursor 和 terminal 停止条件；
- task outcome、summary pending、失败恢复；
- 多 Team monitor 的部分成功/部分失败（`Promise.allSettled`）显示；
- Quick Start → Preflight → Start Run → refresh/navigation → Results 完整旅程；
- Preflight blocker/warning/info 分类、重复执行稳定性与修复后更新；
- 幂等 Start、last-known-good、cursor 去重/退避，以及 timeout/offline 不误判为 failed；
- Run action 状态矩阵、task attempt history、structured outcome/artifact 追溯与 summary pending/failure。

### 12.4 TUI 测试

- `team_spawn → team_wait → after_seq team_wait` 的工具序列；
- timeout 不触发错误终态；
- interactive prompt 在 Team pending 时不会提前恢复；
- Team teammate 不计入普通 subagent obligations；
- doctor route 的 Team scope dry-run 不调用模型；
- Help/等价快捷入口中的 Team、subagent、Fan-out 区分和推荐行为；
- team_spawn 输出 team_id、member/task 摘要和下一步；发生 timeout 后按 cursor 继续观察；
- Team-only 操作在 active context 正确可见/不可见，且不会把 Team teammate 计入普通 subagent obligations；
- retry/replan/rebind 建议与后端 capability/status 一致，不指示使用普通 child wait 管理 Team。

### 12.5 产品易用性测试

- 使用同一组任务分别测试 Web 和 `aicli` TUI：单点审查、多个独立意见、review → implement → verify、同一输入的隔离比较；
- 记录用户首次选择模式所需时间、是否读懂模式说明、是否误把 fan-out 当成 Team roster；
- 让用户在不查看工具名的情况下完成一次推荐模式创建，再让高级用户展开查看 `subagent_spawn`/`team_spawn`、对象边界和 effective route；
- 在 Team 运行中注入 timeout、summary pending 和 task failure，验证用户能找到正确的继续观察、刷新、重试或结束动作；
- 模拟普通 subagent 需求扩大为多阶段协作，验证升级提示不会丢失上下文、重复派发或混淆两个生命周期；
- 把用户反馈按“术语不懂、模式选错、状态误解、结果找不到、成本/时延意外”分类，作为 M0/M1 的文案和交互修正输入；
- 验证用户能否区分 Team Definition 与一次 Run，能否理解 roster/member provisioning 和 session readiness 不是同一状态；
- 验证 Quick Start 不要求暴露 session ID、route resolver 等内部字段，并允许用户检查/覆盖系统建议；
- 用 API timeout、刷新、切 host、部分 provisioning、summary failure 等场景检查是否出现重复派发、上下文丢失或假成功。

### 12.6 Embedded 与权限测试

- 同一 Team Experience 在 Workspace 和额外 host 下的上下文传递、导航恢复、授权一致性；
- host unmount/页面切换不取消 Run；事件订阅卸载后清理，重新进入后可从 cursor 恢复；
- missing host context 的 graceful fallback，以及键盘、响应式、loading/empty/error/offline 状态；
- 修改 host props 或伪造 workspace context 不能扩大后端读写权限；write scope 与 destructive action 的 enforcement 必须由 Runtime 验证。

## 13. 风险与开放问题

1. **session lifecycle 归属**：Team roster、runtime session、actor registry 和真实 worker 执行之间的生命周期必须继续统一，否则会出现“数据存在但没有 worker”的假成功。
2. **route 配置可见性**：Team 独立 routing 与 subagent inherited routing 容易被用户混淆，必须在 preview、状态栏和 task audit 中显示 source。
3. **task route 与 teammate route**：route 应按 task execution attempt 审计，不应把一次 hard task 的模型选择泄漏到同一 teammate 的下一次 easy task。
4. **终态与 summary 时序**：Team terminal 不一定代表 summary 已 ready；前端需要保留 summary pending 状态。
5. **polling 与实时事件**：当前 Web 轮询适合 P0，但大规模 Team 需要 cursor、退避和后续实时通道设计。
6. **跨 Team 聚合**：fan-out 的“最佳结果”需要明确评分、排序和人工确认，不能简单复用单 Team final summary。
7. **权限与写路径**：Web builder 如果允许写入，必须在启动前展示 write paths、writer limit 和 claim 冲突；不能让 UI 绕过后端 claim。
8. **状态动作安全性**：pause/resume/cancel/retry 可能影响正在运行的 session，必须使用后端受控状态转换并保留审计事件。
9. **provider 健康与 fallback**：route preview 与执行时健康状态可能不同，实际 attempt 的 route audit 应作为事实，preview 只能作为预期。
10. **模式选择的认知负担**：若把 subagent、Team、batch、fan-out 和 teammate 同时作为一级入口，用户会以为它们只是不同数量的 agent；应以协作需求做推荐，并把内部术语放入高级诊断层。
11. **Definition/Run 模型迁移**：当前 Team record 是否可承载多次 Run、Run ID/snapshot 如何兼容历史数据尚需确认；不能只改 UI 标签就宣称实现了独立运行历史。
12. **幂等与部分创建**：网络中断可能发生在服务端成功、客户端未收到响应之后；Start/Retry 必须能查询真实状态并避免重复派发，provisioning 部分失败需定义可恢复/回滚语义。
13. **Outcome/artifact 接受语义**：任务完成不一定代表结果已验证或被接受；schema、权限、版本、retention 和 retry 后的新旧 artifact 关系需要与已有契约对齐。
14. **Host context 与授权边界**：嵌入层会带入 workspace、session、文件选择等上下文，但它们不是授权凭证；所有读写权限必须在后端重新验证。
15. **运营数据可信度**：缺少可靠 usage/cost 或质量数据时，不应显示精确成本、预算保证或质量排名；分析结果还需考虑敏感 prompt、文件路径和 mailbox 内容的隐私/保留策略。

## 14. 本文的验证边界

本文基于仓库中的代码、API 注册、前端组件、routing 解析和只读审查结果整理，重点验证了入口和契约的静态事实。本文**没有**执行以下验证：

- 完整的 Team 端到端运行；
- 真实 provider/model 调用；
- 真实数据库/SQLite 内容核查；
- 多进程 owner lease、故障接管和网络异常验证；
- Web 浏览器中的完整创建、运行和结果回归；
- route preview 与实际 provider 响应的线上一致性验证。

因此，文中“当前已有”表示代码和注册表面已发现，“验收标准”表示后续应执行的验证，不表示本次已经通过验收。

## 15. 参考实现位置

### Team 后端与 TUI

- `backend/internal/toolbroker/broker.go`
- `backend/internal/toolbroker/types.go`
- `backend/internal/toolbroker/team_tools.go`
- `backend/internal/team/orchestrator.go`
- `backend/internal/team/teammate_runner.go`
- `backend/internal/team/terminal_state.go`
- `backend/internal/api/runtimeapi/handler.go`
- `backend/internal/api/runtimeapi/team_handlers.go`
- `backend/internal/api/runtimeapi/team_lifecycle.go`
- `backend/internal/modelrouting/config_resolver.go`
- `backend/internal/agentconfig/config.go`
- `backend/cmd/aicli/commands/chat_actor_registry.go`
- `backend/cmd/aicli/commands/chat_team_lifecycle.go`
- `backend/cmd/aicli/commands/chat_routing_status.go`
- `backend/cmd/aicli/commands/doctor_subagent_route.go`
- `backend/cmd/aicli/commands/chat_actor_agent_obligations.go`
- `backend/cmd/aicli/commands/chat_actor_team_obligations.go`
- `backend/cmd/aicli/commands/chat_actor_agent_obligations_teammate_test.go`

### Subagent / Team 边界

- `backend/internal/toolbroker/broker.go`
- `backend/internal/toolbroker/team_tools.go`
- `backend/cmd/aicli/commands/chat_actor_agent_obligations.go`
- `backend/cmd/aicli/commands/chat_actor_team_obligations.go`
- `backend/cmd/aicli/commands/chat_actor_child_completion_recovery.go`

### Web

- `frontend/src/api/runtime/teams.ts`
- `frontend/src/types/runtime/teams.ts`
- `frontend/src/types/runtime/config.ts`
- `frontend/src/components/workspace/runtime-teams.tsx`
- `frontend/src/components/workspace/runtime-teams/directory-view.tsx`
- `frontend/src/components/workspace/runtime-teams/use-runtime-team-dispatch.ts`
- `frontend/src/components/workspace/runtime-teams/use-team-details-loader.ts`
- `frontend/src/components/workspace/runtime-teams/runtime-team-details-panel/roster-section.tsx`
- `frontend/src/components/workspace/runtime-teams/runtime-team-details-panel/final-summary-section.tsx`
- `frontend/src/components/workspace/runtime-teams/runtime-team-details-panel/timeline-section.tsx`
- `frontend/src/components/workspace/settings/runtime-agent-routing-domain-editor`

### 相关现有方案

- [`spawn-team-teammate-model-routing-plan.md`](./spawn-team-teammate-model-routing-plan.md)
- [`spawn-agent-team-supervision-timeout-recovery-plan.md`](./spawn-agent-team-supervision-timeout-recovery-plan.md)
- [`task-difficulty-model-routing-plan.md`](./task-difficulty-model-routing-plan.md)
- [`../skill_runtime/team_task_outcome_contract.md`](../skill_runtime/team_task_outcome_contract.md)

---

**一句话落地结论**：后端 Team 已具备可复用的执行和持久化基础，产品应先按“独立一次委派（普通 subagent）/需要共享协调的协作 Team / 多个隔离运行的 Fan-out”帮助用户选模式；`aicli` TUI 应以自然语言引导正确工具并以 `team_spawn → team_wait → Team outcome/summary` 作为 Team 主路径，Web 则需要在保留现有 fan-out 和详情能力的基础上，补齐“一个 Team、多成员、可绑定 session、可编排 task graph、可预览 route、可解释结果”的产品闭环。
