# 自动化作业通用化设计（脱离会话绑定 / Job 抽象 / 无人值守执行）

> 目标：把「自动化任务」从「绑定一个会话的定时提醒」升级为**通用自动化作业**——
> 作业可以**不绑定创建时的会话**、由调度面在**无人值守**下执行、结果**回投**给创建者；
> 并支持用户只下达**目标**（如「明天做系统检查」），由系统**规划 → 执行**。
>
> 定位：`scheduler-daemon-design.md`（调度面）+ `embedded-execution-design.md`（执行面）+
> [`runtimehost-executor-extraction-plan.md`](./runtimehost-executor-extraction-plan.md)（执行核）之上的
> **能力层**设计。本方案依赖执行核抽取（路径 B）落地后才有意义。
>
> 结论：分四步——**① 执行上下文抽象（脱离会话）→ ② headless 执行 → ③ Job 抽象（goal+plan）→
> ④ 自然语言规划入口**。每步独立可交付，旧行为零迁移。

---

## 1. 现状与约束（核验）

| 事实 | 位置 | 含义 |
|---|---|---|
| 任务**必绑会话**：`Task.SessionID` 必填 | `internal/schedule/types.go` | 作业生命周期被会话绑死 |
| 创建接口以会话为第一参数 | `schedule.Service.Create(sessionID, req, origin)` | 控制面必须已有会话 |
| 投递语义 = 把 prompt 送进**原会话** | `internal/schedule/runtime.go` `HTTPDeliverer` | 没有「无会话」的执行路径 |
| 任务只有「时间规则 + prompt」 | `Record{Kind, ScheduledAt, Prompt, …}` | 没有 goal / plan / 上下文 / 策略 |
| 已有 `job_*`（后台作业） | `background.Manager`；`implementation-status.md` §P8 | 持久作业 + 状态机**已存在**，但与 `schedule_*` 割裂 |
| 已有 team / subagent / supervision | `team_*` / `subagent_*` / `supervision` | 多步编排与终态投影**已存在**，可复用 |
| 创建会话能力已存在 | `handler.go:2999 CreateSession`；`sessionManager.GetOrCreate` | headless 执行可按需建会话 |
| 执行核抽取中 | `runtimehost-executor-extraction-plan.md` | headless 执行的前提 |

**结论**：通用化**不需要新造执行/编排/存储基础设施**，需要的是
① 给作业加「执行上下文」与「目标/计划」两个维度，② 把执行核暴露成 headless 能力。

---

## 2. 目标模型

### 2.1 Job 抽象

```
Job = 触发(trigger) + 目标(goal) + 计划(plan) + 上下文(context) + 策略(policy) + 状态(state)
```

| 维度 | 含义 | 现状对应 |
|---|---|---|
| **trigger** | 何时触发（after/at/every/daily/weekly/cron） | ✅ `Record.Kind` 全支持 |
| **goal** | 要达成什么（自然语言） | ❌ 新增（prompt 只是文本，不是目标） |
| **plan** | 怎么达成（步骤/工具/检查项），创建时由模型产出 | ❌ 新增 |
| **context** | 在哪执行（会话 / 工作区 / headless） | ⚠️ 只有「会话」 |
| **policy** | 权限、审批、工具面、并发/时长上限 | ⚠️ 部分（`execution.profile`） |
| **state** | 生命周期与结果（pending/running/done/failed + 产物） | ⚠️ `Status` 只有 active/inactive |

> 命名契约：`schedule_*`（定时域）与 `job_*`（后台作业域）在
> [`tool-surface-inventory-and-naming-review.md`](./tool-surface-inventory-and-naming-review.md) 中已明确区分。
> 本方案不合并工具名，而是让 **`schedule_*` 支持「作业化」参数**（goal/plan/context），
> 后台作业仍归 `job_*`。

### 2.2 执行上下文（`target`）——脱离会话绑定的核心

| `target.kind` | 语义 | 执行时 | 兼容 |
|---|---|---|---|
| `session`（默认/现状） | 投递进指定会话 | 会话必须存在 | 旧数据零迁移 |
| `workspace` | 绑定工作区 + profile | 每次运行**创建临时会话** | 新增 |
| `headless` | 不绑任何会话，纯目标 | 运行产物落**结果收件箱** | 新增 |

- `target` 与既有 `origin`（model/manual）正交。
- 旧行无 `target_kind` → 默认 `session`（迁移与 `origin` 同款做法，见 migration v3）。

---

## 3. 关键机制设计

### 3.1 单执行者保护（作业级租约）

脱离会话后，**没有会话租约可借**（§见 `runtimehost-executor-extraction-plan.md` §2.5a：
宿主的 `chat.SessionLeaseStore` 是会话粒度的）。因此需要**作业级执行租约**：

| 层 | 锁 | 保护 | 归属 |
|---|---|---|---|
| 触发 | 全局 mesh 租约 | 单 daemon 触发 | 已有 |
| **作业执行** | **`job-<id>` 租约**（同一 `SessionLeaseStore` 域，或独立 lease 表） | **同一作业同时只有一个执行者** | **新增** |
| 状态推进 | `state_version` CAS | 并发写只有一个成功 | 已有 |

- 复用宿主的租约存储实现（同一 DB / 同一 TTL 与续租语义），**不要**再引入第三种锁域。
- 执行前 `Acquire(job-<id>)`：拿到才跑；拿不到说明已有执行者 → 跳过本次（或排队）。
- 会话类作业（`target.kind=session`）**双保险**：作业级租约 + 会话租约。

### 3.2 headless 执行

```
到点 → 取 job 租约
      → 解析 target：
           session   → 投递/执行进原会话
           workspace → Executor.CreateSession(workspace, profile) → 执行 → 结束后按策略归档
           headless  → Executor.CreateEphemeralSession(profile) → 执行 plan
      → 结果回投
      → 释放 job 租约
      → commit（推进 trigger / 落终态）
```

- 依赖 `runtimehost.Executor` 暴露**建会话**能力（runtime-server 已有 `CreateSession`；
  执行核把它作为一等方法）。
- headless 会话建议**运行完即归档**（避免会话表膨胀）；归档策略可配置。

### 3.3 结果回投（结果收件箱）

headless 作业没有「原会话」可投递，需要结果通道：

| 方式 | 说明 |
|---|---|
| **回投创建者会话** | `Job.CreatedBySessionID`（创建时的会话）→ 把结果作为一条消息投递回去 |
| **结果收件箱** | 新增 `schedule_job_results`（或复用 pending 机制）供前端/CLI 读取 |
| **通知** | 复用 supervision wake / 事件（`schedule.changed` 同族） |

> 建议：**默认回投创建者会话**（用户体验最自然：我在 aicli 说"明天检查"，明天结果回到这个会话），
> 同时写一份到结果表供前端列表展示。

### 3.4 状态机扩展

现状 `Status` 只有 `active | inactive`（调度生命周期）。作业化需要**运行态**：

```
scheduled ──到点──▶ running ──成功──▶ done
                       │
                       └──失败──▶ failed ──重试──▶ running
```

- 调度态（scheduled/done/failed/inactive）与运行态（running）分离：一个 `every` 作业可以
  有多次运行记录（每次运行一条 run 记录，含起止/结果/产物）。
- 建议新增 `schedule_job_runs` 表（run 级历史），与既有 `schedule_delivery_history`（投递历史）区分：
  前者是**执行**历史，后者是**投递**历史。

---

## 4. 自然语言 → 作业规划 → 自动执行

### 4.1 用户视角

```
用户（aicli）："明天做系统检查"
   └─ 模型（交互态，有工具）：理解为目标 → 产出规划 → 创建 Job
        trigger: at 明天 09:00
        goal:    做一次系统检查
        plan:    [检查磁盘/内存/服务状态/日志错误…]
        context: workspace=<当前工作区>, profile=scheduled
        policy:  只读优先；无人值守审批降级
   └─ 到点：调度面在 headless 上下文执行 plan → 结果回投到本会话
```

### 4.2 分工（关键设计原则）

| 阶段 | 谁做 | 为什么 |
|---|---|---|
| **规划** | **创建时的模型**（交互态，有完整工具面/上下文） | 规划质量依赖模型 + 当前上下文；创建时信息最全 |
| **执行** | **调度面**（daemon/runtime-server，headless，受限 profile） | 执行要无人值守、可重放、权限受限 |

**原则**：**"规划在创建时完成，执行在到点时进行"**——避免在无人值守时让模型临时决定做什么
（不可预测、无法审批）。到点执行的是**已确定的 plan**（可带自适应步骤，但边界在创建时划定）。

### 4.3 与「模型创建」产品原则的关系

现有产品原则是「创建始终由模型完成」（`implementation-status.md` C3 结论：daemon 无会话创建权限）。
本方案**保持该原则**：规划仍由创建时的模型完成；daemon 只执行已存的 plan，**不新建作业**。
区别只是：作业可以**不绑创建时的会话**，以及**有 plan**。

---

## 5. 分期（J0–J4）

> 与既有编号解耦：P=主方案、D=调度守护进程、E=内嵌执行、J=作业通用化。
> 依赖：J2 依赖 E3（执行核）；J1 可先行。

| 阶段 | 交付 | 验收 |
|---|---|---|
| **J0** | Job 字段扩展：`goal` / `plan` / `target_kind` / `workspace` / `profile`（migration，旧行默认 `session`） | 旧作业零迁移；新字段可读写；工具/HTTP/前端契约同步 |
| **J1** | **执行上下文**：`target=workspace/headless` 的存储与解析（尚未 headless 执行，先能定义） | 能创建不绑会话的作业；调度面识别 target 并走对应分支（无执行核时落 mailbox/待执行） |
| **J2** | **headless 执行**：`runtimehost.Executor` 建临时会话 + 跑 plan + 归档 | headless 作业到点自动执行一次 plan；产物可查 |
| **J3** | **结果回投 + 运行历史**：`schedule_job_runs` + 回投创建者会话/结果收件箱 | 执行结果回到创建者会话；前端可列 run 历史 |
| **J4** | **自然语言规划入口**：创建时模型产出 plan（工具参数扩展 + 提示词/校验） | 「明天做系统检查」→ 一条带 plan 的 headless 作业 → 自动执行 → 结果回投 |

> **J0 进展（本批次）——后端存储/领域/工具/HTTP 已完成**：
> - `Record` 新增 `Goal` / `Plan []string`；`Task` 新增 `TargetKind` / `TargetWorkspace` /
>   `TargetProfile`；新增 `TargetKind` 常量与 `NormalizeTargetKind`（空/未知 → `session`）。
> - store migration **v4**：`goal` / `plan_json` / `target_kind` / `target_workspace` /
>   `target_profile`（全部 DEFAULT，旧行零迁移）；`Put`/`UpdateCAS`/`scanTask` 全部读写；
>   `recordsEqual` 纳入 `Goal`/`Plan`（编辑可被检出）。
> - `CreateRequest` 接收 `Goal`/`Plan`/`TargetKind`/`TargetWorkspace`/`TargetProfile`；
>   `Service.Create` 清洗（trim + 去空 plan 项）后落库。
> - **工具**：`schedule_create` 支持 `goal` / `plan` / `target_kind` / `target_workspace` /
>   `target_profile`（`internal/toolbroker/schedule_tools.go`，含 `stringSliceArg`）。
> - **HTTP**：`POST /api/runtime/schedule/create` 请求体接受同名字段
>   （`internal/api/runtimeapi/schedule_handlers.go`）。
> - 测试：`internal/schedule/job_fields_test.go`（往返 + `UpdateCAS` + `CreateRequest` 落库 +
>   **旧行回落 session/空计划**）。
> - **待做**：前端（创建表单/展示 `goal`/`plan`/`target`）。调度面 `target` 分支见下方 J1。

> **J1 进展（本批次）——执行上下文语义已落地（执行核待 J2）**：
> - **不绑会话**：`Service.Create` 对非 `session` 目标**丢弃会话绑定**（`SessionID=""`）；
>   `workspace` 目标要求 `target_workspace` 非空（否则 `invalid_rule`）。
> - **调度面分支**：`ListActiveDue` 只选 `target_kind='session'` 的任务——workspace/headless
>   目标**暂不参与到期选择**（避免无会话可投递时空转），但仍出现在 `Catalog()` 中供 UI 展示「待执行」。
> - **入口放宽**：HTTP `POST /api/runtime/schedule/create` 仅在 `target_kind=session` 时要求
>   `session_id`；工具侧 `schedule_create` 的 `target_kind` 参数同步生效。
> - 测试：`internal/schedule/job_fields_test.go`（headless 不绑会话 + 不参与到期 + 目录可见；
>   workspace 缺工作区被拒）。
> - **待做（J2）**：`workspace`/`headless` 真正执行（`runtimehost.Executor` 建临时会话 → 跑 plan → 归档），
>   并把它们纳入到期选择。

> **J2 进展（本批次）——headless 执行链路已打通（归档待做）**：
> - `schedule.HeadlessExecutor` + `HeadlessTurnRequest`（job/goal/plan/prompt/profile/workspace/target）。
> - `Runtime.drive`：非 `session` 目标走 `driveHeadless`（**不经 delegate**）；成功即 `commit`
>   推进任务；失败按既有退避重试；**无 headless 执行能力时延后 5 分钟**（不改 `attempts`、
>   不计死信、不落 pending），避免空转。
> - `ListActiveDue` 恢复包含全部目标；`groupFor` 不再把非会话 `every` 任务合并成组。
> - daemon：`scheduleExecutorAdapter` 实现 `ExecuteHeadlessTurn`——先用
>   `bootstrap.Manager.SessionManager().Create` 建临时会话（合成用户 `scheduled-daemon`），
>   再把 goal/plan 渲染进 prompt 提交（`dont_ask`）；无会话工厂时 fail-closed。
> - 测试：`internal/schedule/headless_test.go`（执行并推进 / 无能力时延后不空转）、
>   `cmd/scheduled/executor_test.go`（建会话 + goal/plan 渲染 + fail-closed）。
> - **已补（本批次）**：headless 临时会话在 turn 完成后**归档**（`SessionManager.Archive`，
>   best-effort；仅 `Completed` 时归档，挂起会话留给后续清理）。
> - **待做**：前端展示（结果回投与运行历史见 J3）。

> **J3 进展（本批次）——结果回投 + 运行历史（后端）**：
> - `Task.CreatedBySessionID`（migration **v5**，旧行默认空）：非 `session` 目标也保留
>   创建者会话，作为结果回投目标。
> - 新表 `schedule_job_runs` + `AppendJobRun` / `ListJobRuns`；`HeadlessExecutor` 现返回
>   `HeadlessTurnResult{SessionID, Output}`。
> - `Runtime.driveHeadless`：执行后落运行历史（`done`+output / `failed`+error），成功时把
>   `[SCHEDULE JOB RESULT]` 结果**回投创建者会话**（best-effort；失败只记日志）。
> - daemon：headless 执行返回临时会话 id 与输出文本。
> - 测试：`TestHeadlessJobRecordsRunAndDeliversResult`（运行历史含输出 + 回投创建者会话）。
> - **已补（本批次）**：HTTP `GET /api/runtime/schedule/runs?id=&session_id=&limit=`
>   （归属校验：绑定会话或创建者会话匹配）；回投失败**落持久 pending**（与提醒投递同一兜底通道）。
>   ⚠️ 实现中发现并修复：`commit` 的 pending 清理会抹掉刚写的回投兜底 → 回投必须排在 `commit` **之后**。
> - **待做**：前端展示 run 历史；headless 临时会话的归档/保留策略。

> **J4 进展（本批次）——规划入口（提示词 + 校验）**：
> - 按设计原则「**规划在创建时完成**」，规划由**创建时的模型**完成（daemon 只执行已存的 plan），
>   因此 J4 的代码面 = **工具提示词 + 校验**，不引入自动规划器。
> - **提示词**：`schedule_create` 描述新增作业语义引导——面向「目标式」自动化请求时，模型应在创建时
>   自行产出 `goal` + 简短声明式 `plan`；无人值守用 `target_kind=headless`（或 `workspace` + `target_workspace`）；
>   并说明**执行结果会回投本会话**。
> - **校验**：非 `session` 目标**必须有 `goal`**（`goal is required when target_kind is workspace or headless`）；
>   会话目标不受影响（向后兼容）。
> - 测试：`TestNonSessionTargetRequiresGoal`（headless 无 goal 被拒；会话目标不要求）。
> - **端到端**：用户说「明天做系统检查」→ 模型产出 `goal`+`plan`+`target_kind=headless` 调 `schedule_create`
>   → 存储 → daemon 到点建临时会话执行 → 落运行历史 + 结果回投原会话（J0–J3 已打通）。
> - **待做**：前端（创建表单/展示）；HTTP run 历史端点；规划质量的提示词调优与评测。

> **前端进展（本批次）**：
> - **类型**：`RuntimeScheduleRecord`/`Task` 增 `goal`/`plan`/`targetKind`/`targetWorkspace`/
>   `targetProfile`/`createdBySessionId`；新增 `RuntimeScheduleJobRun`/`RunsResponse`；
>   `RuntimeScheduleCreateRequest` 增 `goal`/`plan`/`target_kind`/`target_workspace`/`target_profile`。
> - **API**：`normalizeScheduleRecord`/`normalizeScheduleTask` 归一化新字段（宽容 camel/Pascal/snake）；
>   新增 `fetchScheduleJobRuns(sessionId, id, limit)` → `GET /api/runtime/schedule/runs`。
> - **面板**：任务行展示 goal、plan（有序列表）与非 session 目标徽标。
> - **i18n**：zh-CN + en-US 同步新增 `goalLabel`/`planLabel`/`targetLabel`/`target.*`/`runHistory`/
>   `runStatus.*`/`runEmpty`/`runOutput`/`runError`。
> - **验证**：`tsc -b` **无新增错误**（仓库既有 3 个错误与本批无关）；
>   `verify-frontend-i18n` ✅（990 键，0 违规）；`vitest` schedule 面板 6/6 ✅。
> - **面板运行历史**：任务行「运行历史」按钮 → 按需拉取 `GET /schedule/runs` 并展示
>   （状态徽标 + 完成时间 + 输出/错误；折叠保留缓存）。
> - **创建表单**：新增「执行上下文」（session/headless/workspace）与「目标」输入；
>   仅 session 目标要求会话 ID，非 session 目标要求 goal（与后端校验一致）。
> - **micro web**：`web/js/schedule.js` 任务行展示 goal / plan（有序列表）/ 非 session 执行上下文；
>   `scripts/verify-micro-web-schedule.mjs` 增断言，并**修复既有沙箱缺陷**（缺
>   `documentElement`/`body` 导致 `ui.js` 顶层 `applyTheme` 崩溃）→ 现全部断言通过。
> - **缺口修复**：`CatalogEntry` 原不含 `TargetKind` → 两个前端都拿不到执行上下文；已补
>   `TargetKind`/`TargetWorkspace`/`TargetProfile` 并在 `Service.Catalog()` 填充。
> - **待做**：plan 输入（人工表单；当前 plan 由模型工具传入）；面板 `createIncomplete` 文案随 target 变化。

---

## 6. 风险与开放问题

| # | 风险 | 缓解 |
|---|---|---|
| 1 | **无人值守执行不可预测** | plan 在创建时确定；执行 profile 受限；审批预置拒绝/降级；时长上限 |
| 2 | **作业级租约引入第三把锁** | 复用宿主 `SessionLeaseStore` 实现与 TTL/续租语义；不新造锁域 |
| 3 | **headless 会话膨胀** | 运行完归档；可配置保留策略 |
| 4 | **结果回投的会话已归档** | 回投失败则落结果收件箱（最终一致） |
| 5 | **plan 漂移**（执行环境与创建时不同） | plan 存为**声明式步骤**而非模型自由发挥；失败按 run 记录可重放 |
| 6 | **`schedule_*` 与 `job_*` 边界模糊** | 保持工具名分离（命名契约）；作业化参数进 `schedule_*`，后台长任务仍 `job_*` |
| 7 | **前端/契约兼容** | 新字段可选、旧行默认；契约测试钉住旧请求/响应 |

**开放问题**：

1. `plan` 的表达格式：结构化步骤（工具+参数）vs 自然语言子目标？倾向**两者结合**（结构化主干 + 每步可含自然语言说明）。
2. 作业是否允许「执行时再规划一次」（自适应）？倾向**受限**：仅当创建时显式授权。
3. 结果回投的目标：创建者会话 vs 固定「作业收件箱」——建议默认前者、可选后者。
4. 作业级租约的粒度：`job-<id>`（每次运行一把）vs `job-<id>-<occurrence>`（允许重试并发）——倾向前者（更安全）。

---

## 7. 与既有文档的关系

| 文档 | 关系 |
|---|---|
| [`scheduler-daemon-design.md`](./scheduler-daemon-design.md) | 调度面不变；本方案扩展「作业」语义与执行上下文 |
| [`embedded-execution-design.md`](./embedded-execution-design.md) | 本方案的 J2 依赖其 E0/E3（执行核） |
| [`runtimehost-executor-extraction-plan.md`](./runtimehost-executor-extraction-plan.md) | 提供 headless 执行核；其 §2.5a 租约域问题同样是本方案 J2 的前置 |
| [`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md) | 主方案 §6 的「提醒」语义 → 扩展为「作业」 |
| [`tool-surface-inventory-and-naming-review.md`](./tool-surface-inventory-and-naming-review.md) | `schedule_*` / `job_*` 命名边界保持不变 |
| [`implementation-status.md`](./implementation-status.md) | C3 结论（创建由模型完成）在本方案中继续成立 |
| [`frontend-business-chain.md`](./frontend-business-chain.md) | 新增：headless 作业的创建/结果/运行历史四端展示 |
