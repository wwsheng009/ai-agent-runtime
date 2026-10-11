# 方案完整性审查与多端集成补充

> 审查对象：[`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md)
> 审查日期：2026-10-11
> 结论：原方案**后端领域层与调度机制覆盖良好**，但**多端入口、组件清单、与 workflow/后台作业的配合**三块缺失或过粗。本文补齐这三块，并给出修订后的分期计划。

---

## 0. 审查方法与证据

- 已核对本项目真实代码结构：`backend/internal/background`（后台作业）、`backend/internal/supervision`（durable wake）、`backend/internal/team` + `teamsupervisor` + `subagentbatch`（工作流）、`backend/internal/toolbroker`（工具）、`backend/internal/api/runtimeapi`（HTTP 路由）、`backend/internal/webui`（前端产物）、`frontend/`（React 源）、`backend/cmd/aicli/{ui,commands}`（TUI 与 micro web）。
- 以既有 **Jobs（后台任务）** 的端到端实现为"范式参照"：它有完整的后端 handler、React API/hook/panel、micro web 页签、i18n 文案。Schedule 应复用同一套分层。

---

## 1. 完整性评分表

| 维度 | 原方案覆盖 | 判定 | 缺口 |
|---|---|---|---|
| 后端领域逻辑（六种规则/时间语义） | §4–§5 | ✅ 充分 | 需补 cron 解析的具体实现库选型（自实现 vs 依赖） |
| 存储与迁移 | §4.2–§4.3 | ✅ 充分 | 需补 migration 版本化接入点（本项目现有 migration 机制） |
| 调度运行时 | §6 | ✅ 充分 | 已定：**独立守护进程 `cmd/scheduled` 持定时器**（不用 supervision wake；见 [`scheduler-daemon-design.md`](./scheduler-daemon-design.md)） |
| 工具层 | §7 | ✅ 基本充分 | 需补 toolbroker 的 schema 注册具体位置与参数审计（`broker_arg_kinds.go`） |
| HTTP API | §9.1 | ⚠️ 偏粗 | 未落位到 `internal/api/runtimeapi`；未给路由注册文件与命名规范 |
| **多端入口** | §9.2（只提 Web） | ❌ **缺失** | 未覆盖 **frontend React / micro web / aicli TUI / gateway console** 四端 |
| **组件清单** | 无 | ❌ **缺失** | 未给出新增/改动文件级清单与组件计数 |
| **与 workflow / 后台作业配合** | §14（仅概念对比） | ⚠️ 偏粗 | 未给组合场景、共享基础设施、冲突规避、工具可见性矩阵 |
| 权限边界 | §8 | ✅ 基本充分 | 需补本项目"委派深度"的真实判据来源（`agentcontrol.AgentRecord.Depth`） |
| 配置 | §10 P0-7 | ✅ 基本充分 | 需补 `config/manager.go` 校验/默认值接线细节 |
| 测试 | §12 | ✅ 充分 | 需补四端的验证脚本（micro web 用 `scripts/verify-micro-web-*.mjs` 范式） |

---

## 2. 后端逻辑：组件清单与文件映射

### 2.1 新增包 `backend/internal/schedule/`（约 9 个文件）

| 文件 | 职责 | 参照 |
|---|---|---|
| `types.go` | `ScheduleID`/`ScheduleRecord`/`ScheduleTask`/`ScheduleStatus`/视图/错误码 | dsh `types.ts` |
| `domain.go` | 六种 `Create*`、`ResolveOccurrence`、`View`、投递文本渲染 | dsh `domain.ts` |
| `cron.go` | 五字段解析 + 规范化 + DOM/DOW 语义 | dsh `canonicalizeCronExpression` |
| `timezone.go` | IANA 加载、DST 缺口/重叠判定、`LocalAtInput` 解析 | dsh 的 `Temporal` 用法（Go 用 `time` 等价实现） |
| `store.go` | SQLite 表 + migration + CAS 写 + 历史裁剪/分页 | `internal/background/store.go` |
| `runtime.go` | 单定时器 + 单写者队列 + 启动扫描 + 到期投递 | dsh `runtime.ts` |
| `service.go` | `Create/List/Catalog/History/Update/Delete/StopSessionTasks` | dsh `ScheduleService` |
| `events.go` | `schedule.changed` 事件定义与发布 | `internal/events` |
| `errors.go` | `ScheduleInputError` 等结构化错误 | `internal/errors` |

### 2.2 改动点（后端）

| 组件 | 文件 | 改动 |
|---|---|---|
| 工具名常量 | `internal/toolbroker/broker.go`（const 块） | 新增 4 个 `ToolSchedule*` 常量；**与作业工具分族**（作业建议 canonical 改 `job_*`，`task_*` 保留别名），命名/描述区分见主方案 §7.1 与治理文档 §2 |
| 工具 schema/执行 | 新增 `internal/toolbroker/schedule_tools.go` | 4 个工具注册 + 选择器/title 校验 + 子 Agent 拒绝 |
| 参数审计 | `internal/toolbroker/broker_arg_kinds.go`、`broker_arg_audit.go` | 新增 `schedule_*` 的字段类型表（否则参数规范化会漏） |
| HTTP 路由 | 新增 `internal/api/runtimeapi/schedule_handlers.go` + `schedule_routes.go` | 在 `/api/runtime` 子路由注册 catalog/list/history/update/delete |
| 配置 | `internal/config/manager.go`（`Background` 字段在 44 行、`BackgroundConfig` 在 373 行） | 新增 `Schedule ScheduleConfig` + `WithDefaults()` + 校验 |
| 装配（runtime-server） | `backend/cmd/runtime-server/main.go`（`BuildSupervisionControlPlane` 附近，1260–1300 行） | 构建 `schedule.Service`、`handler.SetScheduleService(...)` |
| 装配（aicli 宿主） | `backend/cmd/aicli/commands/chat_actor_host.go` | 同上，保证 TUI/micro web 路径可用 |
| 事件桥接 | `internal/api/runtimeapi/runtime_event_delivery.go` | 把 `schedule.changed` 纳入 SSE 事件投递 |
| 前端产物 | `internal/webui/dist`（构建产物） | 由 `frontend/` 构建写入，不手改 |

### 2.3 后端"服务依赖"清单

Schedule 服务需要注入（对齐 dsh 的 `inject`）：

| 依赖 | 本项目来源 | 用途 |
|---|---|---|
| 会话投递 | `chat.SessionHub.GetOrCreate` + `SessionActor.SubmitPromptAsync`（`chat/hub.go:157`、`chat/actor.go:593`） | 到期提交一条 user 消息（触发一次会话 run） |
| 单实例租约 | `internal/mesh`（`Acquire/Renew/Release/Takeover`） | daemon 单实例；**不用 supervision wake** |
| 存储 | SQLite（`internal/sqliteutil` / `background` 同款） | 任务与历史 |
| 会话存在性/委派深度 | `internal/agentcontrol`（`AgentRecord.Depth` / `Workflow`） | 子 Agent 拒绝、会话解析 |
| 事件总线 | `internal/events` | `schedule.changed` |
| 时钟 | 注入 `Clock` 接口 | 可测试 |

> **与原方案的差异**：原方案 §6.2 说"P0 假设单实例、P2 补租约"。审查后建议**P0 就复用 `background` 已有的 `state_version` CAS 与 owner 列**（其 migration 已存在），避免 P2 再改表结构。

---

## 3. 多端入口设计（原方案最大缺口）

### 3.1 端矩阵

本项目共有 **4 个用户可达前端** + 1 个统一后端 API：

| # | 端 | 代码位置 | 技术 | 构建 | 面向 |
|---|---|---|---|---|---|
| E1 | **frontend（主 Web UI）** | `frontend/src/` | React + Vite + TS + Tailwind | 有（产物进 `internal/webui/dist`） | runtime-server 用户 |
| E2 | **micro web client** | `backend/cmd/aicli/commands/web/` | 内嵌 HTML + 原生 JS（30 个模块） | 无（免构建） | 本机 aicli 会话 |
| E3 | **aicli TUI** | `backend/cmd/aicli/ui/` + `commands/chat_*.go` | Go 自绘终端 | 有（Go 编译） | 终端用户 |
| E4 | **gateway console** | `backend/cmd/gateway/console/` | PWA（原生 JS + SW） | 无 | 网关运维（可选） |
| A0 | **runtime API** | `backend/internal/api/runtimeapi/` | Go HTTP + SSE | — | E1/E4 的数据源 |

### 3.2 E1 frontend（React）——主入口，优先级最高

**路由**：`frontend/src/App.tsx` 目前有 `/logs`、`/usage`、`/analytics`、`/runtime/config`、`/runtime/skills`、`/workspace/*`。
→ 新增**全局页** `pages/schedule-page.tsx` + 路由 `/runtime/schedule`（与 `/runtime/skills`、`/runtime/config` 同族）。

**会话级面板**：`components/workspace/panel-registry.ts` 是右侧栏"面"的**单一事实来源**，现有 8 个面（artifacts/plan/checkpoints/files/git/sessionDetail/sessionMcp/plans）。
→ 新增 `schedule` 面（`requiresSession: true`，`widthClass: "content"`），并注册 `lazySurface`。

**数据层**（照抄 Jobs 的范式）：

| 层 | Jobs 既有文件 | Schedule 新增 |
|---|---|---|
| REST 客户端 | `api/runtime/jobs.ts` | `api/runtime/schedule.ts` |
| 类型 | `types/runtime/jobs.ts` | `types/runtime/schedule.ts` |
| hook | `hooks/workspace/use-background-jobs.ts` | `hooks/workspace/use-schedule-tasks.ts` |
| 面板组件 | `components/workspace/jobs-panel.tsx` / `jobs-panel-shared.ts` / `jobs-panel-job-actions.tsx` | `schedule-panel.tsx` / `schedule-panel-shared.ts` / `schedule-panel-task-actions.tsx` |
| 状态条 | `components/workspace/jobs-status-bar.tsx` | `schedule-status-bar.tsx`（顶栏"N 个自动化任务"） |
| i18n | `i18n/resources/zh-CN/workspace/panels-jobs.ts` | `panels-schedule.ts`（+ en） |
| 注册 | `panel-registry.ts` 一行 | 一行 |

**事件刷新**：沿用 `lastRuntimeEventType`/`runtimeEventCount` 的重载键（`WorkspacePanelSurfaceProps` 已提供），把 `schedule.changed` 纳入。

### 3.3 E2 micro web client（免构建，优先级中）

结构：`commands/web/index.html`（页签按钮 + `#tab-*` 面板）+ `app.js`（入口模块图）+ `js/*.js`（30 个功能模块）+ `style.css`。

**新增**：
- `js/schedule.js`（拉取 `/web/api/schedule/*` 或复用 runtime API、渲染列表/详情/删除确认）；
- `index.html` 新增页签按钮 `#tab-schedule-btn` 与 `#tab-schedule` 面板；顶部菜单加"自动化任务"项；
- `js/ui.js` 页签切换接线、`js/sse.js` 监听 `schedule.changed`；
- `style.css` 面板样式（**注意**：micro web 的静态不变量测试会校验"无 fallback 的 `var(--token)` 必须已定义"）。

**测试范式**：新增 `scripts/verify-micro-web-schedule.mjs`（迷你 DOM + 静态契约），与现有 20+ 个 `verify-micro-web-*.mjs` 并列。

### 3.4 E3 aicli TUI（优先级中）

- **slash 命令**：新增 `/schedule`（列表/查看/删除/新建引导），注册在 `commands/` 的 slash 命令表（现有 `/debug` 等的同族，见 `chat_slash_completion.go`、`command.go`）。
- **会话页头/状态条**：在 TUI 的 status/statusbar（`ui/statusbar.go`）显示活动任务计数；沿用 `commands` 中既有的 overlay/面板机制（如 `fullscreen_list.go`）做任务列表。
- **不新建渲染器**：复用 `cmd/aicli/ui` 既有组件（`fullscreen_list`、`statusbar`、`popup`）。

### 3.5 E4 gateway console（可选，优先级低）

`cmd/gateway/console/` 是独立 PWA。若网关产品需要，只需消费 A0 的同一套 API（无需新后端）；本方案不将其列为核心交付项，列为 **P6 可选**。

### 3.6 A0 后端 API（统一数据源）

在 `internal/api/runtimeapi` 新增 `schedule_handlers.go` + `schedule_routes.go`，挂在 `/api/runtime` 子路由：

```
GET  /api/runtime/schedule/catalog
GET  /api/runtime/schedule/list?session_id=
GET  /api/runtime/schedule/history?session_id=&id=&limit=&before=
POST /api/runtime/schedule/update
POST /api/runtime/schedule/delete
SSE  schedule.changed
```

> 命名规范对齐既有：本项目 REST 用 snake_case 查询参数（如 `session_id`），而 dsh 用 camelCase 字段——**以本项目规范为准**。

### 3.7 端一致性规则

1. **单一数据源**：四端都读 A0 的同一 catalog；前端不各自维护任务状态。
2. **删除确认在每端都必做**，且"新建"统一为"打开会话让模型创建"（不在 UI 内建表单），与 dsh 一致。
3. **展示口径统一**：频率文本、下次运行时间、状态（active/inactive）、回执（不代表模型执行完成）文案一致。
4. **能力分级**：E1/E2 支持全部管理；E3 至少支持 list/delete/详情；E4 可选。

---

## 4. 组件计数与依赖图

### 4.1 组件计数（新增为主）

| 层 | 组件数 | 明细 |
|---|---|---|
| 后端领域包 | 9 | `internal/schedule/*` |
| 后端工具 | 2 | `schedule_tools.go` + broker 常量/审计改动 |
| 后端 HTTP | 2 | `schedule_handlers.go` + `schedule_routes.go` |
| 后端配置/装配 | 3 | config、runtime-server、aicli host |
| 前端 React（E1） | ~9 | page + panel×3 + hook + api + types + i18n×2 + registry 行 |
| micro web（E2） | ~4 | `schedule.js` + index.html + ui.js + verify 脚本 |
| aicli TUI（E3） | ~2 | slash 命令 + 状态条接线 |
| **合计** | **≈ 31 个新增/改动单元** | 其中 P0 必需约 16 个，P1 约 10 个，P2/前端约 12 个 |

### 4.2 依赖图

```
config.ScheduleConfig ─┐
                       ▼
        internal/schedule.Service ◀── toolbroker(schedule_*)
                │  ▲                    ▲
                │  │                    │
   store ◀──────┘  └── runtime（cmd/scheduled 守护进程）──▶ mesh 租约（单实例）
                       │
                       └──▶ 投递：HTTP submit_prompt / embedded / mailbox
                │
                ▼
        events(schedule.changed) ──▶ runtimeapi(SSE)
                │
                ▼
   runtimeapi(schedule_handlers) ──▶ E1 frontend / E2 micro web / E3 TUI / E4 console
```

---

## 5. 与 workflow / 后台作业的配合

### 5.1 三者定位对比

| 维度 | 后台作业 Jobs（`internal/background`） | 工作流 Workflow（`internal/team` 等） | 自动化任务 Schedule（本方案） |
|---|---|---|---|
| 触发 | 调用即执行（提交后排队） | 调用即编排 | **到点触发** |
| 生命周期 | 进程托管（PID/进程组/探活/恢复） | 多 Agent 任务图（依赖/租约/成果） | 单条提醒（无进程） |
| 投递对象 | 无（产出在 job output） | 子 Agent 会话/邮箱 | **原会话（提交 prompt 触发一次 run）** |
| 存储 | `background.sqlite` | team/batch store | `schedule.sqlite` |
| 工具 | `background_task`/`task_*`（建议改 `job_*`，`task_*` 保留别名） | `spawn_agent`/`spawn_team`/team 工具（`task_*` 归团队） | `schedule_*` |
| 失败语义 | 可恢复/重跑/孤儿 | 依赖失败传播 | 未确认则下轮重试（可能重复） |
| 用户可见 | Jobs 面板（已存在） | Teams 面板（`runtime-teams`） | 自动化任务面板（本方案） |

### 5.2 共享基础设施（不重复造）

| 基础设施 | 位置 | 三者如何共享 |
|---|---|---|
| durable wake | `internal/supervision/wake_scheduler.go` | Schedule 到点唤醒复用；**不与 supervision 的 wake 抢同一预算键**（用独立 scope/key） |
| CAS + owner | `internal/background` 的 `state_version`/owner 范式 | Schedule 沿用同一写模型 |
| 会话提交 | `chat.SessionHub` + `SessionActor` | Schedule 经 `GetOrCreate`+`SubmitPromptAsync` 提交；`runMeta.source="schedule"` 与后台作业完成通知区分 |
| 事件总线 | `internal/events` | 各自发 `schedule.changed` / 作业事件，互不干扰 |
| 会话/委派深度 | `internal/agentcontrol` | 三者的会话绑定与子 Agent 判据同源 |

### 5.3 组合场景（"如何配合"）

| # | 场景 | 实现方式 | 注意 |
|---|---|---|---|
| C1 | 定时让模型跑一个后台命令 | 提醒投递到会话 → 模型读到后自行调 `background_task` | Schedule 不直接启动后台作业（保持"投递即消息"） |
| C2 | 定时让模型起一个子 Agent/团队 | 同上，模型调 `spawn_agent`/`spawn_team` | 子 Agent 会话本身**不能**再建提醒（委派深度拒绝） |
| C3 | 后台作业完成后到点再提醒 | 作业终态事件 → 创建 `at` 提醒（P2 可选增强） | 需要作业事件 → Schedule 的桥；P0 不做 |
| C4 | 团队任务按时间重试 | 由 team 自己的 lease/wake 处理 | **不要**用 Schedule 做重试调度（语义不同） |
| C5 | 一条提醒的内容是"检查某后台作业状态" | 提醒投递 → 模型调 `task_output` | 与 C1 同族 |

**核心原则**：Schedule **只负责"到点把一句话送进原会话"**；一切"执行"交给模型在会话内调用既有工具（`background_task`、`spawn_agent`、`spawn_team`）。这样三者零耦合、可组合。

### 5.4 工具可见性 / 权限矩阵

| 工具族 | 主 Agent | 子 Agent | 说明 |
|---|---|---|---|
| `background_task` / `task_*` | ✅ | 受限（只读子 Agent 无 shell） | 已有 |
| `spawn_agent` / `spawn_team` / team 工具 | ✅ | 按深度封顶 | 已有 |
| `schedule_create/list/update/delete` | ✅ | ❌（工具层 + 服务层双重拒绝） | 本方案 |

### 5.5 明确边界（不该做的）

1. **不做第二套定时器**：Schedule 的"下一个目标"与 supervision 的 wake 必须收敛到**同一唤醒机制**（推荐：Schedule 把下一目标登记为一条 wake，而不是自己常驻 timer + supervision 也常驻 timer）。
2. **不接管后台作业的恢复**：作业的 rerun/backoff 仍归 `background`。
3. **不做工作流编排**：多步依赖仍归 `team`。
4. **不新建投递栈**：复用 `chat.SessionHub`/`SessionActor` 的 prompt 提交路径（非 supervision 的 durable inbox）。
5. **不共享存储表**：三张表各自独立，避免生命周期语义互污。

---

## 6. 原方案缺口清单与补丁

| # | 缺口 | 补丁 |
|---|---|---|
| G1 | 只写了 Web UI，漏了 frontend/micro web/TUI/console 四端 | 见 §3（本文件） |
| G2 | 未给组件清单与计数 | 见 §2、§4 |
| G3 | HTTP API 未落位到 `internal/api/runtimeapi` | 见 §3.6，命名改用本项目 snake_case 规范 |
| G4 | 未说明与 workflow/后台作业的配合 | 见 §5 |
| G5 | 未给 cron 实现选型 | 建议**自实现五字段解析器**（不引第三方 cron 库），以便精确控制规范化与 Vixie DOM/DOW 语义；Go 无内置 cron |
| G6 | 未给 migration 接入点 | 复用 `background` 同款 SQLite migration（`internal/sqliteutil`），版本化 |
| G7 | 未给 micro web 静态不变量约束 | 见 §3.3 注（`var(--token)` 必须已定义） |
| G8 | 权限判据未落到本项目 | 用 `agentcontrol.AgentRecord.Depth > 0`（持久化列，冷恢复成立） |
| G9 | 未区分 `source` 标识 | 投递消息 `source = "schedule"`，与作业完成通知的 source 区分 |
| G10 | P0 单实例假设过弱 | 已由 **mesh 调度租约**取代单实例假设；`state_version` CAS + owner 列仍保留（见守护进程文档 §4） |

---

## 7. 修订后的分期计划（含前端）

> **一致性复核（2026-10-11）**：本节早期的 P0–P4 已被**主方案 §10 的统一路线图 P0–P6**取代（并入守护进程 D0–D3 与内嵌执行 E0–E3）。**以主方案 §10 为准**，下表仅保留前端视角的映射。

| 主方案阶段 | 前端交付 | 出口 |
|---|---|---|
| **P3** | **E1 frontend**（page + panel + api/hook/i18n/registry） | 主 Web UI 可用 |
| **P4** | **E2 micro web**（`js/schedule.js` + 页签 + verify 脚本）、**E3 aicli TUI**（`/schedule` + 状态条） | 四端一致 |
| **P6（可选）** | **E4 gateway console** | 组合场景 |

后端/调度阶段见主方案 §10：P0 领域+工具+前台 daemon、P1 时间语义+权限+API、P2 守护进程服务化、P5 内嵌执行。

---

## 8. 审查结论

- 原方案在**后端领域与调度**上是完整且可实施的；
- 但作为"跨多端产品能力"，它**缺少四端入口设计、组件清单、与既有自动化能力的配合说明**——这三块正是本文件补齐的内容；
- 建议按**主方案 §10 的统一路线图 P0→P6** 推进：P0/P1 纯后端可独立验收，P2 守护进程服务化，P3 起前端介入，P5 内嵌执行。
