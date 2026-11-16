# runtime-server 与 aicli TUI 核心逻辑同源性审查与收敛方案

> **审查日期**：2026-11-14
>
> **审查对象**：`backend/cmd/runtime-server`、`backend/cmd/aicli`（TUI chat 路径）及其共享下层
>
> **审查方式**：静态代码审查 + 关键符号交叉核对；本次未执行全量测试与真实终端 E2E。
>
> **代码基线**：工作区 `HEAD`（`371e43b0 fix(chat): verify new provider models before enabling`），工作树干净。
>
> **定位**：本文回答三个问题：
> 1. 两者在主要核心功能上是否使用同一套逻辑？
> 2. 如果不是，哪些是**传输/宿主差异**，哪些是**实质功能分叉**？
> 3. 对实质分叉给出可落地的收敛方案、阶段划分与验收标准。
>
> **与既有文档的关系**：
> - `docs/automation-tasks/runtimehost-executor-extraction-plan.md`（E3 执行核抽取）
> - `docs/automation-tasks/aicli-executor-reuse-design.md`（E3.4 aicli 复用 actor 工厂）
> - `docs/plan/runtime-server-aicli-shared-session-mechanism-plan.md`（共享会话/身份/租约）
> - `docs/plan/aicli-goal-driver-unification-plan-20261018.md`（goal driver 单源化）
> - `docs/architecture/unified-tool-surface-registry.md`（工具面/profile 双落点）
> - `docs/plan/runtime-store-event-persistence-batching-and-async-plan-20260918.md`（事件落盘桥）
> - `docs/ANALYSIS-sqlite-lock-problem.md`（多进程共享 SQLite 的死锁边界）

---

## 一、结论摘要

### 1.1 一句话结论

**不是“同一套逻辑”，而是“共享同一个会话执行引擎的多个宿主装配，其中 runtime-server 自己还存在一条 non-actor 直连执行路径”。**

截至当前代码：

- **TUI 本地模式与 runtime-server 的 actor command 路径**已经共享 `chat.SessionActor` + `runtimehost.BuildSessionActor`（E3.4b 默认开启），这是同源程度最高的部分。
- **但两者之上仍有两套完整的宿主装配与控制面**：
  - Agent/Loop/Profile/Prompt/工具策略：两套；
  - Subagent/Team/Supervision/Goal/Background/Memory 控制面：两套；
  - 事件落盘桥、运行时存储默认值、远程命令能力：两套或不对称。
- **runtime-server 的 `/api/agent/chat`（Web 文本主路径）根本不走 SessionActor**，而是请求级直连 ReAct loop；它有自己的 agent 装配、历史 checkpointer、在途回合注册表和自动 compact。这意味着“runtime-server 与 TUI 同源”只对 **runtime command API 路径**成立，对 **Web 文本主路径**不成立。

因此准确表述应为：

> **底层执行核部分同源；宿主装配层、控制面层、Web 主执行路径仍存在实质分叉。**

### 1.2 结论分级

| 层级 | 现状 | 判定 |
|---|---|---|
| **L3 单 turn 执行核**：SessionActor 状态机、Agent ReAct、工具执行、权限引擎、compact、plan mode 状态 | 共享 `internal/chat`、`internal/agent`、`internal/toolbroker`、`internal/compactruntime`、`internal/planmode` | ✅ **同源** |
| **L2 会话装配**：agent config、system prompt、profile、MCP、skill、工具策略、loop 配置 | 两套实现 + Web 第三条内联装配 | 🟠 **双实现，已出现漂移** |
| **L1 宿主控制面**：hub、提交/中断、AgentControl、Team、Supervision、Goal、Background、Memory、事件落盘 | 两套实现 | 🟠 **双实现，部分语义已分叉** |
| **L0 传输/交互**：TUI 渲染、SSE/HTTP、审批 UI、认证/多用户 | 本质不同 | ⚪ **合理差异，但需显式化** |
| **Web 主路径**：`/api/agent/chat` | non-actor 直连 loop | 🔴 **独立执行路径** |
| **运行时状态存储**：session_runtime / team / agentcontrol / artifact / background | 类型共享，默认路径/持久化策略不同 | 🟠 **数据面部分分叉** |
| **TUI 远程模式控制命令**：/plan、/memory、/agents、/team、/schedule、/supervision | 多数仍走本地宿主或直接不可用 | 🔴 **功能覆盖不对称** |

### 1.3 最高风险项（按影响排序）

| # | 风险 | 级别 | 证据 |
|---|---|---|---|
| R1 | Web 文本主路径不走 actor，缺少跨请求审批/挂起语义；本轮已安装权限引擎并 fail-closed，但仍无 actor 的 pending approval/resume | **P0（部分缓解，见 §11 F3；完整修复仍需 P3）** | `handler.go:1024, 2298-2378, 2586`；`agent/loop.go:3824-3836`；`chat/actor.go:6647-6662` |
| R2 | Agent/Loop/工具策略双实现，已出现 `ContextAccountingMode`、`TurnBudgetTokens`、`allowed_roots`、goal guidance、子代理 agentdef 解析等漂移 | **P0** | `session_agent_build.go:24-233`；`chat_actor_host.go:2393-3428` |
| R3 | AgentControl/Team/Goal/Background 控制面双实现，修复只落一边即产生跨宿主行为差 | **P0** | 见 §5.3；`toolbroker/types.go:1507-1523` |
| R4 | 事件落盘默认策略分叉：TUI 默认批量 + AsyncDispatch；runtime-server 默认关闭批量且只读 `BatchingEnabled` | **P1（本轮已修复，见 §11 F1/F2）** | `chat_actor_host.go:1124-1142`；`handler.go:4971-4988`；`config/manager.go:525-531` |
| R5 | 同一 session 在两宿主的 runtime state/event store 不一定是同一份；TUI local 与 runtime-server 默认可指向不同文件 | **P1** | `sessionruntime/paths.go:200-210`；`backend/configs/runtime.yaml:169-181` |
| R6 | TUI 远程模式对多数控制命令没有 HTTP 适配，部分命令会静默操作本地数据或给出误导性“未启用” | **P1（本轮已部分修复：显式拒绝 + /schedule 接 server，见 §11 F8/F9）** | `chat_schedule_command.go:35-46`；`chat_memory_command.go:24-35`；`chat_plan_command.go:39`；`chat_runtime_server.go:1895-1913` |
| R7 | Goal driver 两套：TUI 默认启用，server 默认关闭（示例配置另开）；仅靠 CLI 冷却窗启发式互斥 | **P1** | `chat_goal_driver.go:40-57`；`session_goal_driver.go:8, 582-590`；`chat_setup.go:207` |
| R8 | TUI 本地 `PrepareRun` 覆盖了默认租约获取；run stall 释放租约后，下一次 run 可能在**无租约**状态下执行 | **P0（本轮已修复，见 §11 F10）** | `runtimehost/actor_factory.go:75-80,129-145`；`chat_actor_executor_deps.go:173-174`；`chat_actor_host.go:2720-2724` |
| R9 | TUI 远程状态轮询路径写错：客户端请求 `/runtime/state`，服务端实际路由是 `/runtime`；404 被当作“state unavailable”忽略 | **P0（本轮已修复，见 §11 F11）** | `chat_runtime_server.go:1512-1516`；`handler.go:1187`；`chat_runtime_server.go:1796-1801` |
| R10 | 关闭清理不对称：TUI 会 `StopAll` hub、等待 async/subagent；runtime-server 优雅关闭只 flush 事件缓冲，不停 hub/actor | **P1** | `chat_actor_host.go:356-384,1601-1653`；`cmd/runtime-server/main.go:1557-1629`；`handler_executor.go:82-92` |

---

## 二、审查范围、方法与证据口径

### 2.1 范围

**覆盖**：

- runtime-server：`cmd/runtime-server`、`internal/api/runtimeapi`、`internal/runtimeserver`
- aicli TUI：`cmd/aicli/commands/chat*`、`cmd/aicli/commands/agent_stdio*`（仅作为同宿主参考）
- 共享下层：`internal/runtimehost`、`internal/chat`、`internal/agent`、`internal/toolbroker`、`internal/policy`、`internal/compactruntime`、`internal/planmode`、`internal/sessionruntime`、`internal/background`、`internal/schedule`、`internal/subagentbatch`、`internal/agentcontrol`、`internal/team`、`internal/supervision`、`internal/memoryruntime`、`internal/memorystore`
- 前端证据：仅用于证明 Web 主路径行为，不作为 TUI 逻辑判断依据

**不覆盖**：

- gateway / bots / console 的独立进程
- `aicli exec` / ACP 的完整行为（仅在与 TUI 共用 helper 时引用）
- TUI 渲染器、终端输入、主题、布局
- runtime-server 的认证、租户、管理员 API（这些是合理的宿主专属能力）

### 2.2 判定口径

| 判定 | 含义 |
|---|---|
| **同源** | 同一函数/同一包/同一状态机，宿主只传参 |
| **参数化差异** | 同一实现，通过 `Deps`/`HostProfile` 显式选择不同参数（如 lease 生命周期） |
| **双实现** | 语义近似但代码是两份；修复/新增必须改两处 |
| **独立路径** | 不同执行路径（如 actor vs non-actor direct loop），不仅是参数差异 |
| **传输/UI 差异** | 只影响“怎么交互/怎么显示”，不应影响“跑什么、允不允许、状态是什么” |

本文只把 **双实现 / 独立路径 / 已影响语义的数据面差异** 视为需要方案的“功能分叉”；传输/UI 差异不要求合并，但要求**显式声明能力边界**。

### 2.3 证据方式

- 关键结论均给出 `文件:行号`；行号以本次审查的 HEAD 为准。
- 重复度统计为辅助证据：用 Python `difflib.SequenceMatcher` 对同名函数/配置块做文本相似度抽样，仅用于说明“复制后漂移”的方向，不作为行为等价的证明。
- 未执行全量 `go test`/E2E；涉及运行期才能确认的（尤其是 Web 直连路径的审批行为）在文中标记“需运行期确认”。

---

## 三、总体架构现状

### 3.1 当前实际执行路径

```text
                           ┌──────────────────────────────────────────────┐
                           │              runtime-server 进程             │
                           │                                              │
  Web 文本  ── POST /api/agent/chat ──► 请求级 Agent + ReAct 直连 loop    │
                           │            （无 SessionActor / 无跨请求挂起） │
                           │                                              │
  Web 图片  ── POST /runtime/sessions/{id}/runtime/commands ──┐            │
  TUI 远程  ── 同一 command API + /runtime/stream ────────────┤            │
                           │                                  ▼            │
                           │                        chat.SessionHub         │
                           │                        + chat.SessionActor     │
                           │                        + runtimehost.BuildSessionActor
                           └──────────────────────────────────────────────┘

  TUI 本地  ── localChatRuntimeHost.SessionHub ──► chat.SessionActor
                                                   + runtimehost.BuildSessionActor（默认）
                                                   + 本地 localActorRegistry / 本地控制面

  scheduled ── runtimeapi.Handler.Executor() ──► handlerExecutor ──► 同一 Hub/Actor
```

### 3.2 关键事实

1. **`runtimehost.Executor` 不是三宿主的统一入口**
   - 接口定义：`internal/runtimehost/executor.go:19-29`
   - 唯一生产调用方是 `cmd/scheduled`（`cmd/scheduled/executor.go:156`）
   - runtime-server HTTP 路径仍直接操作 `Handler.getSessionHub()` / `buildSessionActor`
   - aicli TUI 仍直接操作 `localChatRuntimeHost.SessionHub`
   - 因此当前共享的是 **`runtimehost.BuildSessionActor` 这个工厂**，不是完整执行核。

2. **TUI 本地 actor 工厂默认已复用 runtimehost**
   - `chat_actor_host.go:1874-1877` 默认走 `runtimehost.BuildSessionActor`
   - `AICLI_EXECUTOR_REUSE=0|false|off|no` 是逃生门（`chat_actor_executor_deps.go:186-198`）
   - 旧装配路径仍保留到 `chat_actor_host.go:1879-1997`，尚未删除（E3.4c 待做）

3. **runtime-server 的 Web 文本主路径不经过 actor**
   - 路由：`handler.go:1024`
   - 请求级 agent 装配：`handler.go:2298-2378`
   - 直连 loop：`handler.go:2586`、`handler.go:2927`
   - 在途回合注册表：`handler.go:2564`
   - 没有 `SessionActor`、没有 actor 的 durable pending approval/question/plan 状态

4. **健康检查声称的 execution core 大于实际 Web 路径**
   - `/healthz` 与 `/api/runtime/health` 返回 `chat.SessionActorRuntimeCore()`（`handler.go:10928`、`cmd/runtime-server/main.go:2073`）
   - 这只能证明“服务具备 actor 能力”，不能证明“Web 文本回合走 actor”
   - 该语义差应在健康检查/诊断中显式化，否则调用方会误判同源

---

## 四、主要核心功能同源矩阵

> 说明：`TUI remote` 指 `aicli chat --runtime-server`，其 `LocalRuntimeHost=nil`（`chat_runtime_server.go:1895-1913`）；`TUI local` 指默认本地 actor-first 模式。

| # | 能力 | runtime-server | aicli TUI local | aicli TUI remote | 判定 |
|---|---|---|---|---|---|
| 1 | SessionActor / Hub | `getSessionHub` + `buildSessionActor` → `runtimehost.BuildSessionActor`（`session_runtime_support.go:4706-4737`） | 自有 `SessionHub` + 同一 `BuildSessionActor`（`chat_actor_host.go:1598-1600,1874-1877`） | HTTP command API → 服务端 actor | 🟡 同实现，不同所有者 |
| 2 | 单 turn 执行 | `chat.SessionActor`（actor path）/ direct ReAct loop（web path） | `chat.SessionActor` | 服务端 `chat.SessionActor` | 🔴 Web 主路径独立 |
| 3 | Agent 装配 | `buildSessionAgentForContext`（`session_agent_build.go:24-162`）+ direct 内联（`handler.go:2298-2378`） | `buildLocalChatAgent`（`chat_actor_host.go:2393-2718`） | 复用服务端 actor 装配 | 🟠 双实现 + 第三内联 |
| 4 | Loop 配置 | `buildSessionLoopConfigFor` / `buildSessionLoopConfig`（`session_agent_build.go:197-233`、`session_runtime_support.go:4846-4872`） | `buildLocalChatLoopConfig`（`chat_actor_host.go:3234-3278`） | 复用服务端 actor 配置 | 🟠 双实现，已漂移 |
| 5 | Profile / Prompt | `resolveProfileSessionState` + `buildRuntimeInstructionMessages`（`profile_support.go:198-251`、`instruction_messages.go:12-27`） | `ChatSession.Profile*` + `composeLocalChatSystemPrompt`（`chat_actor_host.go:2895-2952`） | 服务端解析 | 🟠 双实现 |
| 6 | 工具面 / 执行策略 | `applyAgentExecutionPolicy`（`handler.go:5773-5862`）+ `runtimeServerToolSurfaceForSession` | `buildLocalChatToolPolicy`（`chat_actor_host.go:2990-3042`） | 服务端策略 | 🟠 共享 policy/工具实现，装配不同 |
| 7 | Skills / MCP | `bootstrap.Manager` + `MCPManager` + `runtimeServerToolSurfaceForSession` | 同一 `bootstrap.Manager` + `runtimetools.Manager` + session MCP | 服务端能力面 | 🟡 同下层，解析链不同 |
| 8 | 权限 / 审批 | actor path：`SessionActor.RequestApproval`；web direct：未安装 AskHandler | actor `AskHandler = actor`（`chat/actor.go:6647-6662`） | 服务端 actor 审批 + command API | 🔴 actor / direct 语义不同 |
| 9 | Plan mode | actor `EnterPlanMode/ExitPlanMode`；direct 使用 `interactiveControllers` | actor plan mode + `/plan` 本地命令 | `/plan` 未接服务端 HTTP | 🟠 状态机共享，入口/控制器分叉 |
| 10 | Ask user question | actor 挂起 + `answer_question` command；direct 有 inline waiter | actor + TUI 交互 | command API | 🟡 共享 actor，direct 另路 |
| 11 | Compact | actor `Compact`；direct `maybeAutoCompactSessionHistory` | actor 自动 compact + 本地 `/compact` | HTTP `type=compact` → actor | 🟡 共享 `compactruntime`，自动触发分叉 |
| 12 | Backtrack / Rewind | actor `Rewind` + HTTP API | actor 本地命令 | HTTP turns/audit/preview/apply | ✅ 同一 actor 语义 |
| 13 | 事件落盘 | `attachRuntimeEventBridge`（`handler.go:4928-4968`） | `bindRuntimeEventPersistence`（`chat_actor_host.go:1157-1217`） | 服务端事件库 | 🟠 双桥接 + 默认策略不同 |
| 14 | 事件读取 / 续传 | `/runtime/stream` + event store | 本地 `EventBus` + bridge | SSE/poll → bridge | ⚪ 传输差异；状态源需同源 |
| 15 | 会话租约 | `chat.SessionLeaseStore`，run-scoped | 同一 store，actor-scoped | 服务端 actor 持锁 | 🟡 同 store，LeaseMode 不同（已显式化） |
| 16 | Subagent 控制 | `sessionAgentController`（83 个方法） | `localActorRegistry`（104 个方法） | 服务端 controller | 🟠 双实现，同一 `toolbroker.AgentSessionController` 接口 |
| 17 | Team 生命周期 | `handlerTeamLifecycleService`（215 行/14 函数） | `localTeamLifecycleService`（724 行/34 函数） | 服务端 team service | 🟠 双实现，写序已知不同 |
| 18 | Supervision | `runtimeserver.SupervisionControlPlane` + handler 接线 | 同一 control plane + 本地接线 | 服务端 control plane | 🟡 共享控制面，宿主接线分叉 |
| 19 | Goal driver | `session_goal_driver.go`（33 函数，默认关） | `chat_goal_driver.go`（11 函数，默认开） | 服务端 driver + CLI 冷却窗 | 🔴 两套驱动，默认不一致 |
| 20 | Background jobs | `background.Manager` + `handleBackgroundEvent` | `background.Manager` + `localBackgroundJobEventRelay` | 服务端 job API | 🟠 共享 manager，构造/投影双实现 |
| 21 | Schedule | `schedule.Service` + HTTP `/schedule/*` | `newLocalScheduleService` + 本地 `/schedule` | `/schedule` 未接 HTTP | 🟠 共享 service，远程命令缺位 |
| 22 | Memory | `EnableMemoryRuntime`（`memory_runtime_wiring.go:137-220`） | `initializeMemoryRuntime`（`chat_memory_runtime.go:18-105`） | 服务端 memory API | 🟠 近乎复制（配置块 99.6% 相同） |
| 23 | Usage analytics | `usageanalytics` + handler | 同一 `usageanalytics` + 本地 host | 服务端分析 API | 🟡 共享 store，接线不同 |
| 24 | Session history / identity | `chat.SessionManager` | 同一 manager | HTTP `SessionStorage` | 🟡 模型共享；身份默认不同 |
| 25 | Runtime state store | 默认独立文件或内存（`backend/configs/runtime.yaml:169-181`） | 默认 `<sessions>/runtime/session_runtime.sqlite`（`sessionruntime/paths.go:200-210`） | 服务端 store | 🔴 同一 session 的运行态不一定同源 |
| 26 | 远程 TUI 控制命令 | 具备大量 HTTP 端点 | 全部本地命令 | 仅部分命令有 HTTP 适配 | 🔴 能力矩阵不对称 |---

## 五、实质分叉详述

### 5.1 Web 文本主路径不是 actor：runtime-server 内部先分叉

这是本次审查最重要的发现，优先级高于“TUI 与 server 是否共享 actor 工厂”。

#### 5.1.1 路径事实

| 入口 | 执行载体 | 关键证据 |
|---|---|---|
| Web 文本 `POST /api/agent/chat` | **请求级 Agent + ReAct 直连 loop** | `handler.go:1024`（路由）、`handler.go:2298-2378`（内联 agent 装配）、`handler.go:2586`/`:2927`（`RunReActWithSession`） |
| Web 图片 `POST /api/runtime/sessions/{id}/runtime/commands` | SessionActor | 前端注释明确“图片只在这条通道上被接受”：`frontend/src/api/runtime/session-prompt.ts:2-14`、`frontend/src/hooks/workspace/agent-chat-turn/image-prompt-turn.ts:1-14` |
| TUI 远程 `aicli chat --runtime-server` | SessionActor command API | `chat_runtime_server.go:514-569`、`:799-869`、`:1065-1100` |
| TUI 本地 | SessionActor | `chat_actor_executor.go:203-362`、`chat_actor_host.go:1598-1600` |
| scheduled | SessionActor via `Handler.Executor()` | `cmd/scheduled/executor.go:156` |

#### 5.1.2 直连路径与 actor 路径的能力差

`/api/agent/chat` 的直连路径自己做了以下事情：

- 请求级解析 profile/workspace/session（`handler.go:2067-2170`）；
- 自己构建 `agent.Config`（`handler.go:2298-2378`），而不是复用 `buildSessionAgentForContext`；
- 自己注册 `activeTurnRegistry`（`handler.go:2564`），用于 SSE 断连续传与 interrupt；
- 自己创建 `agentChatHistoryCheckpointer`（`handler.go:2527`、`agent_chat_checkpoint.go:114`）；
- 自己调用 `maybeAutoCompactSessionHistory`（`handler.go:2381`、`:4529-4605`）；
- 没有 `SessionActor` 的 durable `RuntimeState`、pending approval、pending question、plan mode 生命周期、goal driver 的 run 终态事件。

这与 actor 路径的差异不是“SSE vs 轮询”，而是**状态所有权与可挂起性不同**。

#### 5.1.3 审批语义的高风险缺口

actor 路径会在构造 actor 时安装权限引擎的交互处理器：

- `chat/actor.go:6647-6662`：`engine.AskHandler = a`（`SessionActor` 实现 `RequestApproval`）；
- `chat/actor.go:5618-5710`：审批请求落 `PendingApproval`、发 `approval_requested`、挂起等待 command API 的 `approve_tool`；
- runtime-server command API 的 `approve_tool`：`session_runtime_handlers.go:1151-1174`。

而 `/api/agent/chat` 的内联 agent 装配：

- `newAPIAgentWithRuntime`（`handler.go:4820-4918`）只构建 agent，不安装 `PermissionEngine`；
- `applyAgentExecutionPolicy`（`handler.go:5773-5862`）只设置静态 `ToolExecutionPolicy`；
- `applyAgentRuntimeServices`（`handler.go:5913-6054`）设置 broker 的 `UserInput`/`PlanMode`/`Team`/`Background`/`Schedule`，但本次静态审查**没有看到** `SetPermissionEngine` 或 `ApplySessionPermissionMode` 的调用；
- `internal/agent/loop.go:3824-3836`：如果 `GetPermissionEngine()` 为 nil，只执行静态 `ToolExecutionPolicy.AllowTool`，不会走审批询问。

**结论（本轮已部分修复，见 §11 F3）**：

- 若 `/api/agent/chat` 路径确实没有其他层补装权限引擎，则审批型工具在该路径上**不会产生 actor 的 pending approval 流程**：
  - 静态 policy 允许 → 直接执行（无审批）；
  - 静态 policy 拒绝 → 立即 deny（无挂起、无批准机会）。
- 两种结果都与 TUI 本地/TUI 远程的 actor 审批语义不同。
- 这属于**核心行为分叉**，不是 UI 差异。
- **本轮修复**：在 Web 直连 agent 上安装会话权限引擎（默认 `default`）；无 `AskHandler` 时引擎按 `approval_required` fail-closed，不再静默走静态策略执行审批型工具。
- **仍未修复**：Web 直连路径没有 durable pending approval / `approve_tool` 恢复；真正与 actor 同源仍需 P3。

> 运行期验证建议：在 Web 文本回合触发一个默认策略要求审批的写工具，观察是否出现 `approval_requested` 事件、`/runtime` 是否出现 `pending_approval`、`/runtime/commands` 的 `approve_tool` 是否能恢复执行。若没有，则本结论成立。

#### 5.1.4 健康检查语义扩大

`/api/runtime/health` 返回 `execution_core = chat.SessionActorRuntimeCore()`（`handler.go:10928`），`/healthz` 同样（`cmd/runtime-server/main.go:2073`）。这会让 `aicli` 的 `runtimeServerHealthCheck` 认为服务端具备统一 actor 执行核心（`chat_runtime_server.go:111-139`），但 Web 文本主路径并不使用它。

**本轮已修复（§11 F7）**：健康检查新增 `entrypoint_execution` 映射。建议后续继续把该字段作为契约测试的固定断言：

- `execution_core`：服务端具备的 actor 核心能力；
- `entrypoint_cores` / `default_web_execution_core`：各入口实际使用的核心（actor / direct / stateless）。

否则远程客户端只能验证“服务端有 actor”，不能验证“我即将走的入口是 actor”。

---

### 5.2 Agent / Loop / Profile / 工具策略：两套装配 + 已发生的漂移

#### 5.2.1 三份装配代码

| 路径 | 装配函数 | 位置 |
|---|---|---|
| runtime-server actor | `buildSessionAgentForContext` + `buildSessionLoopConfigFor` | `session_agent_build.go:24-233` |
| runtime-server Web 文本 | 内联 `agent.Config` + 内联 loop | `handler.go:2298-2378` |
| aicli TUI / ACP 本地 | `buildLocalChatAgent` + `buildLocalChatLoopConfig` | `chat_actor_host.go:2393-2718`、`:3234-3278` |

E3.4 设计文档已经把 D2/D3/D4 标为“宿主专属，两者都必须保留”（`docs/automation-tasks/aicli-executor-reuse-design.md:35-41`）。这在“只复用 actor 工厂”的阶段是合理的；但从**功能同源**角度，它意味着：

- 新增一个 tool policy 语义，要改 2–3 处；
- 新增一个 loop 配置项，要改 2–3 处；
- 任何一处漏改，TUI 与 server 就会对“同一个 session + 同一份配置”给出不同工具面/权限/上下文。

#### 5.2.2 已确认的漂移点

| # | 漂移点 | runtime-server | aicli TUI | 影响 |
|---|---|---|---|---|
| D1 | `ContextAccountingMode` | `buildSessionLoopConfig` 未设置（`session_runtime_support.go:4846-4872`） | `buildLocalChatLoopConfig` 从 `runtimeConfig.Context.AccountingMode` 设置（`chat_actor_host.go:3254-3258`） | **本轮已修复（§11 F5）** |
| D2 | `TurnBudgetTokens` | `RunMeta.BudgetTokens` → `cloneLoopConfigForRun`；Web direct 接收请求 `budget_tokens` | 从 `ChatSession.TurnBudgetTokens` → base loop + `RunMeta` | **本轮已修复（§11.5 F18）** |
| D3 | `allowed_roots` / `tool_base_path` / `read_only_roots` | `SessionContext` 携带 durable roots；actor/direct 均可投影到 agent options | 构建期仍同步；运行期撤回/新增由 actor admission 负责 | **本轮已修复（§11.5 F19）** |
| D4 | `active_goal_guidance` | actor 有 `OnRunPrepare` 注入（`handler_deps.go:73-82`）；direct 未见 | 构建期 + 每轮 `PrepareRun` 注入（`chat_actor_host.go:2493-2498, 2751-2755`） | **本轮已修复 direct 路径（§11 F4）** |
| D5 | base/child 判定 | 与 TUI 共用 `runtimehost.IsBaseSession`（child marker ∪ base ID 不等） | 与 server 共用 `runtimehost.IsBaseSession` | **本轮已修复（§11.6 F23）**：保守并集判定，stale child marker 在两个宿主都按 child 处理 |
| D6 | `SessionContext` 完整度 | `ExtractSessionContext` 填充 profile ref/agent/routing/observation/stream/disable_tools 等（`runtimehost/session_context.go:52-94`） | `deriveLocalSessionContext` 已直接复用 `runtimehost.ExtractSessionContext`（`chat_actor_executor_deps.go`） | **本轮已修复（§11.4 F16）** |
| D7 | agentdef 解析范围 | `ResolvePortableBinding(agentType, DiscoverOptions{ProjectRoot: workspace})`（`session_runtime_support.go:4782-4784`） | `agentdef.Resolve(agentType, agentdefDiscoverOptions(workspace, profileRoot, nil))`（`chat_actor_host.go:2030-2035`） | **本轮已补齐 ProfileRoot（§11 F6）** |
| D8 | agentdef 工具策略顺序 | 与 TUI 共用 `runtimehost.ApplyChildAgentdefToolPolicy`；在 sandbox 物化前设置 `PathAnchorRoot`（`session_runtime_support.go`） | 与 API 共用 `runtimehost.ApplyChildAgentdefToolPolicy`（`chat_actor_host.go`） | **本轮已修复（§11.4 F14）** |
| D9 | `disable_tools` | actor + direct 均发布 `llm.MetadataKeyDisableTools` 并使用空 allowlist | TUI 构建期发布同一 metadata option + 空 allowlist | **本轮已修复（§11.5 F20）** |
| D10 | 上下文 options 的字段集合 | `contextOptionsFromRuntimeConfig` 转发 `runtimehost.BuildContextOptions`（`handler.go`） | `applyLocalChatContextOptions` 转发 `runtimehost.ApplyContextOptions`（`chat_actor_host.go`） | **本轮已修复（§11.4 F15）**：共享键集，补齐仅设置 `maxImageAttachmentTokens`/artifact 时 TUI 提前返回的问题 |

> 重复度抽样（辅助证据）：
> - `applyLocalChildAgentdefToolPolicy` vs `applyAPIChildAgentdefToolPolicy`：文本相似度约 **0.61**（90 行 vs 70 行）；
> - `buildLocalChatLoopConfig` vs `buildSessionLoopConfig`：约 **0.45**（44 行 vs 27 行，TUI 多出 budget/accounting）；
> - `applyLocalChatContextOptions` vs `contextOptionsFromRuntimeConfig`：结构不同，但约 20 个 option key 语义重叠。
>
> 这些数字只说明“两套代码已经各自演化”，不证明行为等价或不等价。

#### 5.2.3 Profile / Prompt 的额外差异

- runtime-server 的 profile 解析包含 registry/`ResolveRef`/D29 prompt gate/overlay config（`profile_support.go:94-250`）；
- TUI 的 profile 投影在 `applyProfileStateToChatSession`（`chat_profile.go:74-121`），并在 `composeLocalChatSystemPrompt` 中做会话级 frozen prompt（`chat_actor_host.go:2895-2952`）；
- Runtime-server actor 的 instruction messages 走 `buildRuntimeInstructionMessages`（`instruction_messages.go:12-27`），direct 与 actor 都使用它；
- TUI 的 system prompt 走 `composeChatSystemPromptWithGuidanceForCWD` + workspace 身份段落，且对 prompt cache 做了冻结（`chat_actor_host.go:2904-2951`）。

这不是简单的“同一份 prompt 换个传输”：两边的 prompt 组成、workspace 锚定、profile overlay 应用点都不同。

**影响**：同一个 profile、同一个 workspace、同一个 session，在 TUI 与 server 上可能得到不同的 system prompt、不同的工具面、不同的模型/预算。

---

### 5.3 控制面双实现：AgentControl / Team / Goal / Background

#### 5.3.1 AgentControl：同一个接口，两套完整实现

接口只有一个：

- `toolbroker.AgentSessionController`：`internal/toolbroker/types.go:1507-1523`

实现有两套：

| 宿主 | 类型 | 文件 | 规模 |
|---|---|---|---|
| runtime-server | `sessionAgentController` | `internal/api/runtimeapi/session_runtime_support.go` | 约 5074 行，83 个方法 |
| aicli TUI | `localActorRegistry` | `cmd/aicli/commands/chat_actor_registry.go` | 约 5817 行，104 个方法 |

同名方法 **25 个**；文本相似度抽样：

| 方法 | 相似度 | 说明 |
|---|---|---|
| `enrichAgentTeamProjection` | 0.952 | 几乎复制 |
| `ApplyWorktree` | 0.936 | 几乎复制 |
| `ReadEvents` | 0.930 | 几乎复制 |
| `BudgetVerdict` | 0.919 | 几乎复制 |
| `deliverSubagentCompletionMailbox` | 0.918 | 几乎复制 |
| `Wait` | 0.853 | 账本外壳高度相似 |
| `Close` | 0.635 | 关闭语义已不同（见下） |
| `Spawn` | 0.501 | 宿主生命周期差异较大 |
| `Resume` | 0.383 | 语义差异较大 |

**已确认的语义差异示例：**

- API `Close`：`hub.Stop(closeID)` 同步停止 + `sessionManager.Close(ctx, closeID)`（`session_runtime_support.go:3190-3205`）；
- TUI `Close`：`SessionHub.StopAsync(closeID)` + `SessionStore.Update(StateClosed)`（`chat_actor_registry.go:4135-4158`）；
- 两者都做 worktree cleanup 和 registry subtree close，但写序与终态表达不同；
- `docs/plan/multi-agent-execution-optimization-plan.md:1095` 已明确记录两宿主 spawn rollback 写序不同：
  - CLI：清理隔离 → 释放预约 → 停 actor → 删会话；
  - API：清理隔离 → 停 actor → 删会话 → 释放预约。

#### 5.3.2 Team 生命周期：两套 supervisor 适配器

- API：`handlerTeamLifecycleService`（`team_lifecycle.go:15-215`，14 个函数）
- TUI：`localTeamLifecycleService`（`chat_team_lifecycle.go:16-724`，34 个函数）
- 同名方法只有 4 个：`StopLoop`、`SyncLoops`、`desiredTeamIDs`、`ownerAllowed`。
- TUI 版本包含 `RunSettled`、`Pending`、`WaitForTerminal`、`closeTerminalTeammates`、`terminalSummaryReady` 等 API 版本没有或不同的逻辑。

两者都使用 `internal/teamsupervisor` / `internal/team`，但**生命周期收敛、终态等待、teammate 清理、写序**是两套。

#### 5.3.3 Goal driver：两套驱动，默认值还不一致

- API：`session_goal_driver.go`（33 个函数），配置开关 `goal.serverAutoContinuation`，代码默认 **false**（`session_goal_driver.go:8, 582-590`；`config/manager.go:813-818`）。
- TUI：`chat_goal_driver.go`（11 个函数），默认 **true**（`chat_goal_driver.go:40-57`；`chat_setup.go:207`）。
- 两者共享 `internal/goal` 的提示词/round admit 等常量，但驱动循环、strike 记账、disarm、CLI 冷却窗各写一套。
- 现有 `docs/plan/aicli-goal-driver-unification-plan-20261018.md` 已把 Phase 2（`internal/goal/driver` 单源化）标为**未完成**（§9 实施记录，line 398、432）。

**风险**：

- 默认情况下 TUI 驱动、server 不驱动；一旦示例配置把 `serverAutoContinuation` 打开，两端都可能驱动同一会话；
- 当前互斥依赖“CLI 在场冷却窗”（`session_goal_driver.go:202-244`），不是租约，存在窗口竞争；
- 两套 strike/no-progress 判定可能对同一终态给出不同结论。

#### 5.3.4 Background 投影：两套同名语义

- API：`background_supervision.go`（6 个函数）
- TUI：`chat_actor_background_supervision.go`（6 个函数）
- 同名函数 0 个；语义都是“job 终态/巡检 → supervision inbox + wake”，但各自实现。

#### 5.3.5 Memory runtime：近乎复制

| 位置 | 代码 |
|---|---|
| TUI | `chat_memory_runtime.go:18-105` |
| API | `memory_runtime_wiring.go:137-220` |

静态比对：

- `memorystore.New(memorystore.Config{...})` 配置块相似度 **100%**；
- `memoryruntime.Config{...}` 配置块相似度约 **99.6%**；
- 差异主要在 workspace 解析、`ChatSessionSource`、`WorkerID`、host 生命周期。

这是“复制后只改四行”的典型例子，适合优先抽成共享 factory。

---

### 5.4 事件落盘与运行时状态：同一 session 不保证同一份运行态

#### 5.4.1 事件落盘默认策略分叉（本轮已修复，见 §11 F1/F2）

两宿主都有自己的“总线 → 会话事件库”桥：

- runtime-server：`Handler.attachRuntimeEventBridge`（`handler.go:4928-4968`）
- aicli TUI：`localChatRuntimeHost.bindRuntimeEventPersistence`（`chat_actor_host.go:1157-1217`）

两者都复用 `internal/events` 的声明式注册表、`SessionStoreTypeAlias`、`ProducerPersistedEvent` 和 `chat.EventPersistBuffer`，但**默认策略不同**：

| 配置/行为 | runtime-server | aicli TUI |
|---|---|---|
| 批量开关 | `Enabled: cfg.BatchingEnabled`（`handler.go:4972-4980`） | `Enabled: !cfg.Disable`（`chat_actor_host.go:1124-1136`） |
| 异步派发 | `AsyncDispatch: cfg.AsyncDispatch` | `AsyncDispatch: !cfg.Disable` |
| 默认值 | 批量默认关闭（零值） | 默认开启（`Disable` 零值=false） |
| 溢出丢弃 | 未设置 `OverflowDropTypes` | 设置 `OverflowDropTypes: IsPersistStreamDeltaEventType` |

`internal/config/manager.go:525-531` 的注释已经明确写出：

> “aicli 本地宿主自 2026-10 起默认启用（disable=false）……runtime-server 宿主仍按 batchingEnabled 逐字段生效。”

**修复后**：两个宿主共用 `runtimehost.ResolveEventPersistSettings`，默认均为批量 + AsyncDispatch；`disable: true` 是唯一同步回退开关，server 侧也补齐了 critical/stream-delta 分类器。

修复前这意味着：

- 同样的配置、同样的 session，TUI 本地默认异步批量落盘，server 默认逐条同步落盘；
- 崩溃/关闭时的持久化窗口、事件顺序可见性、stream delta 是否允许丢弃，可能不同；
- 这属于**持久化语义分叉**，不只是性能差异。

#### 5.4.2 运行时状态存储路径分叉

`internal/sessionruntime` 已经统一了路径推导函数，但默认策略仍不同：

- `ShouldUseFileDefaults`：CLI local 恒 true；server 仅在 `sessionRuntime.defaultPersistence=file` 时为 true（`sessionruntime/paths.go:200-210`）。
- `backend/configs/runtime.yaml:169-181` 明确让 server 使用独立文件 `../data/runtime/session_runtime.sqlite`，并保持 `defaultPersistence: memory`，以避免与 CLI 共享 `~/.aicli/sessions/runtime/*` 时的 SQLite 锁死。
- TUI 本地默认使用 `<sessions>/runtime/session_runtime.sqlite`（`sessionruntime/paths.go:102-106`，`chat_actor_host.go:3614-3644`）。

因此：

- **session history**（`chat.SessionManager`）可以共享；
- **session runtime state / events / approvals / tool receipts** 不一定共享；
- TUI local 与 runtime-server 即使指向同一个 session ID，也可能看到不同的 `RuntimeState`、不同的 pending approval、不同的事件 seq。

`docs/ANALYSIS-sqlite-lock-problem.md` 已经给出“不要简单把 `defaultPersistence` 改成 file 合并所有 store”的证据：CLI 长驻进程持锁会让 runtime-server 的 diagnostics 访问阻塞在 `busy_timeout`，导致健康检查挂起。

**结论**：不能把“同一个 session 文件”误认为“同一个运行态”。短期必须显式标注 runtime authority；长期要么单写者服务化，要么读侧镜像，而不是直接把多进程写同一 SQLite 当作同源。

#### 5.4.3 身份默认不同

`sessionruntime.ResolveSessionUserID` 是共享实现，但两宿主的调用参数不同：

- server fallback：`anonymous`（`sessionruntime/identity.go:11-20, 53-55`）
- CLI local fallback：OS 用户名（`identity.go:42-52`）
- 两者可通过显式 `user_id` / `AICLI_SESSION_USER` / `sessions.defaultUserId` 对齐。

这是有意的身份语义差异，但必须在“同 session 可见性”诊断中显示，否则用户会以为同一 session 在两个宿主都可见。

#### 5.4.4 租约重取与关闭清理（已修复）

**租约重取：**

- aicli 的宿主刷新钩子已从 `PrepareRun` 移到 `OnRunPrepare`（`chat_actor_executor_deps.go:165-169`）；`runtimehost.composePrepareRun` 先执行默认的 `ensureLease`，再执行宿主钩子（`runtimehost/actor_factory.go:129-145`）。
- 因此 `OnRunStalled` 释放 actor-scoped 租约后，下一轮 run 会重新获取租约；宿主钩子不能再静默替换租约路径（F10）。
- 租约已从 runtime state store 拆到独立 `session_leases.sqlite`（F43）；续租永久失败会触发 actor fence（F44），不再只记录日志。
- 仍建议补一条从“stall watchdog → lease release → 下一轮重新持锁”的端到端回归；当前已有 `composePrepareRun` 顺序单测，但没有直接驱动 stall watchdog 的完整 actor 用例。

**关闭清理：**

- runtime-server `app.close` 现在先停 schedule driver，再 `Handler.CloseSessionHub()` 停止并清空 actor hub，之后才关闭 memory、事件持久化、store（`cmd/runtime-server/main.go:1557-1629`；F38）。
- `handlerExecutor.Close` 复用同一个 `CloseSessionHub`，并阻止关闭期间 lazy 重建（`handler_executor.go:82-92`）。
- TUI `localChatRuntimeHost.Close` 在 cleanupFns 逆序关闭事件缓冲/store 之前显式 `SessionHub.StopAll()`；hub cleanup 保留为半构造兜底（`chat_actor_host.go:356-384`；F39）。
- 仍缺少“关闭时 actor 停止、事件 flush、store 关闭顺序”的独立契约测试；现有覆盖为 hub close 单测、host close/subagent operation 回归与全包测试。

---

### 5.5 TUI 远程模式：执行同源，但控制命令大面积缺位

`configureRuntimeServerChatExecutor` 会设置 `session.LocalRuntimeHost = nil`（`chat_runtime_server.go:1895-1913`）。这本身是合理的“远程执行、本地 UI”模型，但许多命令仍以 `LocalRuntimeHost` 为唯一数据源：

| 命令/能力 | TUI 远程现状 | 证据 | 风险 |
|---|---|---|---|
| `/compact` | ✅ 走 command API | `chat_runtime_server.go:638-718` | 好范例 |
| `/backtrack` | ✅ 走 HTTP turns/audit/preview/apply | `chat_runtime_server.go:738-796` | 好范例 |
| `interrupt` | ✅ 走 command API | `chat_runtime_server.go:577-597` | 好范例 |
| 回合完成状态轮询 | ❌ 请求 `/api/runtime/sessions/{id}/runtime/state`；服务端实际路由是 `/api/runtime/sessions/{id}/runtime` | `chat_runtime_server.go:1512-1516`；`handler.go:1187` | 404 被 `isRuntimeServerStateUnavailable` 当作正常降级（`chat_runtime_server.go:1796-1801`），状态完成判定可能永远依赖事件流；stream 不可用时可能长轮询不收敛 |
| `/plan` | ❌ 仍操作本地 `ChatSession`/本地 actor | `chat_plan_command.go:39-112` | 远程会话的 plan 状态与 server 不一致 |
| `/plans` | ❌ 本地 planstore | `chat_plans_command.go:24-...` | 看不到 server plan 归档 |
| `/memory` | ❌ 打开本地项目记忆库 | `chat_memory_command.go:24-35` | 命令“成功”但写的是本地文件，不是 server 会话 |
| `/schedule` | ❌ 只读 `LocalRuntimeHost.Schedule`；nil 时提示“未启用” | `chat_schedule_command.go:35-46` | 误导性错误；server 明明有 `/schedule/*` API |
| `/agents` 屏/transcript/cleanup | ❌ 依赖本地 `ActorRegistry`/`EventStore`/`SessionHub` | `chat_agent_transcript.go:483-1146`、`chat_agent_cleanup.go:95-99` | 远程会话无法查看/清理子代理 |
| `/team` | ❌ 依赖本地 `TeamStore`/`Orchestrator` | `chat_team_*` 多处 `LocalRuntimeHost == nil` 早退 | 远程团队不可管理 |
| `/supervision` | ❌ 依赖本地 `Supervision`/`wake` | `chat_supervision.go:285-298, 365-368` | 远程监督面不可用 |
| `/bots` | ✅ 显式说明“server 模式本地未挂载”并给出替代 | `chat_bots_command.go:250-257` | 可接受的降级方式 |

**核心问题不是“远程模式功能少”**——这是可以接受的产品选择；问题是：

- 有些命令**静默操作本地数据**（如 `/memory`）；
- 有些命令给出**误导性原因**（如 `/schedule` 说“未启用”，但 server 有 schedule 服务）；
- 命令目录没有声明 `local / remote / both` 能力，用户无法预判。

runtime-server 已经具备实现这些远程命令所需的 HTTP 端点：

- plan：`handler.go:1227-1255`（`/sessions/{id}/plan`、`/plans`、`/sessions/{id}/plan/reopen`）
- memory：`handler.go:1276-1302`
- schedule：`handler.go:1135-1147`
- agents：`handler.go:1209-1217`、`:1309-1322`
- teams：`handler.go:1323-1362`
- supervision：`handler.go:1365-1369`

所以这不是“没有后端能力”，而是**TUI 客户端没有接线/没有能力声明**。

---

### 5.6 Goal / Compact / Supervision 的跨路径差异

- **Goal**：见 §5.3.3。两套 driver + 默认值不一致；现有统一方案 Phase 2 未落地。
- **Compact**：
  - actor 手动 compact 已共享 `compactruntime`（`chat/actor.go:856-879, 2889-2994`）；
  - Web 直连路径自己调用 `maybeAutoCompactSessionHistory`（`handler.go:4529-4605`）；
  - TUI 本地 actor 内部自动 compact；TUI 远程手动 compact 走 command API。
  - 自动 compact 的触发点、预算来源、replace history 的写回路径仍不统一。
- **Supervision**：
  - 两宿主共享 `runtimeserver.BuildSupervisionControlPlane`、`supervision.WakeScheduler` 等控制面（`runtime-server/main.go:1393-1436`；`chat_actor_host.go:1433-1457`）；
  - 但 wake consumer、event 订阅、background 投影、goal 交互各自接线；
  - 在 runtime state store 不共享的情况下，supervision wake 可能只落在其中一个宿主的 store，另一个宿主看不到。---

## 六、目标架构与收敛方案

### 6.1 设计原则

1. **一个会话执行核**：会话 turn 的提交、租约、状态、审批、事件、恢复只允许一个实现。
2. **宿主只做适配**：宿主差异只允许出现在“数据来源、传输、UI、认证/多用户、离线能力”五类；不允许出现第二套 Agent/Loop/Policy/Control 逻辑。
3. **能力边界显式化**：远程 TUI 不支持的命令必须显式声明并拒绝，禁止静默操作本地数据或给出误导性“未启用”。
4. **状态权威显式化**：同一 session 在同一时刻只能有一个 execution authority；runtime state/event store 必须能被诊断出来。
5. **先冻结行为，再重构**：每一个收敛阶段先用 contract/golden 测试冻结现状，再替换实现；禁止“顺手改语义”。

### 6.2 目标形态

```text
┌──────────────────────────────────────────────────────────────────────┐
│                          Host Adapters                               │
│  runtime-server HTTP/SSE   aicli local TUI   aicli remote TUI   scheduled │
│  - auth/admin              - terminal UI     - HTTP client       - daemon │
│  - multi-user              - offline         - remote transport  - lease  │
└──────────────────────────────┬───────────────────────────────────────┘
                               │  HostProfile + Deps
                               ▼
┌──────────────────────────────────────────────────────────────────────┐
│                    internal/runtimehost.SessionExecutor              │
│  - SessionHub ownership                                             │
│  - SubmitTurn / SubmitAsync / Commands / Events / Close              │
│  - lease lifecycle (run-scoped / actor-scoped)                       │
│  - event persistence bridge                                          │
│  - session command service                                           │
│  - agent controller / team lifecycle / goal driver / background      │
│  - memory runtime factory                                            │
└──────────────────────────────┬───────────────────────────────────────┘
                               │
          ┌────────────────────┼────────────────────┐
          ▼                    ▼                    ▼
  runtimehost.AgentAssembler  chat.SessionActor   runtimehost.EventBridge
  runtimehost.LoopAssembler   agent.ReAct loop    runtimehost.AgentController
  profile/prompt/MCP/skills   toolbroker/policy   team/supervision/goal/background
```

### 6.3 新/扩展包建议

| 目标组件 | 建议位置 | 职责 |
|---|---|---|
| `SessionExecutor` | `internal/runtimehost/executor_impl.go` | 拥有 Hub、提交、命令、事件、关闭；替代 `handlerExecutor` 适配器与 TUI 自有 hub |
| `HostProfile` | `internal/runtimehost/host_profile.go` | `HostKind`、`LeaseMode`、事件落盘策略、命令能力、goal 策略 |
| `AgentAssembler` | `internal/runtimehost/agent_assembly.go` | 从中性 `AgentBuildInput` 构建 `agent.Agent`，固定策略顺序 |
| `LoopAssembler` | `internal/runtimehost/loop_assembly.go` | 从 `AgentBuildResult` + `SessionContext` 构建 `LoopReActConfig` |
| `SessionCommandService` | `internal/runtimehost/command_service.go` | submit/continue/interrupt/approve/answer/compact/rewind/plan/profile 的统一命令语义 |
| `EventPersistenceBridge` | `internal/runtimehost/event_bridge.go` | 总线 → session event store 的唯一桥；含 host-specific 策略解析 |
| `AgentController` | `internal/runtimehost/agent_controller.go` | 实现 `toolbroker.AgentSessionController`，宿主只注入 hooks |
| `TeamLifecycle` | `internal/runtimehost/team_lifecycle.go` | 统一 team loop reconcile/terminal settle/teammate cleanup，复用 `internal/teamsupervisor` |
| `GoalDriver` | `internal/runtimehost/goal_driver.go` + `internal/goal/driver` | 决策核心单源，宿主只提供提交/读取/租约 |
| `BackgroundProjector` | `internal/runtimehost/background_projection.go` | job 终态/巡检 → supervision inbox/wake |
| `MemoryRuntimeFactory` | `internal/runtimehost/memory_factory.go` | 统一 memorystore/memoryruntime 构造与生命周期 |
| `HostCapabilities` | `internal/runtimehost/capabilities.go` | 命令/入口能力矩阵（local/remote/both） |

> 约束：`runtimehost` 不得 import `runtimeapi`；`runtimeapi` 可以 import `runtimehost`。这保留现有依赖方向（`handler_deps.go` 已经遵循）。

### 6.4 `HostProfile` 草案

```go
type HostKind string

const (
    HostKindAICLILocal    HostKind = "aicli-local"
    HostKindRuntimeServer HostKind = "runtime-server"
    HostKindScheduled     HostKind = "scheduled"
)

type HostProfile struct {
    Kind HostKind

    // 会话租约：TUI local = actor-scoped；runtime-server/scheduled = run-scoped。
    LeaseMode LeaseMode

    // 事件落盘：解析器唯一，默认值可按 host 显式不同，但不得在宿主文件里各写 if。
    EventPersist EventPersistPolicy

    // Goal driver：是否启用、是否允许接管其他 host、租约 TTL。
    Goal GoalDriverPolicy

    // 命令能力矩阵：local / remote / both。
    Capabilities CommandCapabilitySet
}
```

关键点：

- `LeaseMode` 已存在，保留。
- `EventPersistPolicy` 不能只是把两个 `runtimeEventPersistSettings` 复制到新包；必须由**一个 resolver** 根据 `HostKind` 和 config 产出。
- `GoalDriverPolicy` 必须包含“驱动者租约”，替代 CLI 冷却窗。

### 6.5 `SessionExecutor` 草案

```go
type SessionExecutor interface {
    SubmitTurn(ctx context.Context, req TurnRequest) (TurnResult, error)
    SubmitAsync(ctx context.Context, req TurnRequest) error
    Commands() *SessionCommandService
    Events() EventSource          // 只读事件游标 / SSE 适配
    Hub() *chat.SessionHub        // 过渡期只读访问
    Close(ctx context.Context) error
}
```

- `runtime-server`：`Handler` 不再拥有 hub；`Handler.Executor()` 返回真正的 `SessionExecutor`，HTTP 路由只做协议映射。
- `aicli TUI local`：`localChatRuntimeHost` 不再自建 hub，改为持有 `SessionExecutor`（`HostProfile.Kind=aicli-local`）。
- `aicli TUI remote`：不创建 `SessionExecutor`，通过 HTTP 适配器调用服务端 command/stream。
- `scheduled`：继续使用同一 `SessionExecutor`（去掉 `handlerExecutor` 中间层或让其变成薄转发）。

### 6.6 Agent/Loop 装配统一

#### 6.6.1 中性输入

```go
type AgentBuildInput struct {
    SessionID     string
    SessionContext SessionContext
    RuntimeConfig *runtimecfg.RuntimeConfig

    Profile       ProfileView       // 中性化：prompt layers / tool policy / context / runtime overlay
    Workspace     string
    SkillRegistry *skill.Registry
    SkillLoader   *skill.Loader
    MCPManager    skill.MCPManager
    ToolSurface   skill.MCPManager
    LLMRuntime    *llm.LLMRuntime

    Hooks         []runtimehooks.HookConfig
    Knowledge     *knowledge.Activation
    Memory        *memoryruntime.Runtime
    AgentsConfig  runtimecfg.AgentsConfig
    Host          HostProfile
}

type AgentBuildResult struct {
    Agent          *agent.Agent
    SelectedConfig *runtimecfg.RuntimeConfig
    ProfileHandle  any // 宿主不透明，只供 LoopAssembler 复用
}
```

#### 6.6.2 固定策略顺序

把当前散落在两个函数里的顺序固化为 `AgentAssembler` 的单一顺序：

1. 解析 profile / prompt layers / context options；
2. 构建 `agent.Config`（provider/model/system prompt/max steps/options）；
3. 构建 agent（registry/loader/MCP/LLM/tool catalog）；
4. 安装 obligation resolvers / hooks / runtime services；
5. 应用宿主基线工具策略；
6. 应用 profile 裁剪（deny 优先）；
7. 应用宿主硬边界（read-only / shell disabled / child depth / sandbox）；
8. 安装 subagent scheduler / batch coordinator / broker；
9. 执行 parked batch resume；
10. 返回 `AgentBuildResult`。

任何“宿主专属”差异只能通过输入/回调表达，不能在共享顺序中插入 `if host == ...`。

#### 6.6.3 Loop 统一

`LoopAssembler` 统一设置：

- max steps / max tool calls / max run duration / exploration / repeated call / parallel tools；
- `TurnBudgetTokens`；
- `ContextAccountingMode`；
- reasoning effort / stream；
- main-agent routing；
- completion requirement；
- agent_session/team/job obligations；
- knowledge observation hook。

TUI 与 server actor 都从同一 `LoopBuildInput` 产出 `LoopReActConfig`；Web direct path 如果保留，也必须使用同一 assembler。

### 6.7 控制面统一

#### 6.7.1 AgentController

把 `sessionAgentController` 与 `localActorRegistry` 的公共逻辑提取为 `runtimehost.AgentController`：

```go
type AgentControllerDeps struct {
    Hub          *chat.SessionHub
    SessionStore chat.SessionStorage
    RuntimeStore chat.RuntimeStateStore
    TeamStore    team.Store
    AgentStore   agentcontrol.AgentRegistryStore
    BatchStore   subagentbatch.BatchStore
    Worktree     WorktreeOps
    RunMeta      func(ctx context.Context, sessionID string) *team.RunMeta
    CloseActor   func(ctx context.Context, sessionID string) error
    Audit        func(chat.MailboxDeliveryAudit)
    Logf         func(format string, args ...any)
}
```

实现 `toolbroker.AgentSessionController`（`toolbroker/types.go:1507-1523`）。宿主只注入：

- session/actor 的取得方式（TUI 本地 vs server hub）；
- close 的同步/异步差异（显式策略，不能隐式）；
- worktree/registry/team 的存储；
- 审计/日志。

首批优先统一相似度最高的方法：

1. `Wait` / `BudgetVerdict` / `ObserveWaitSegment`；
2. `ReadEvents` / `enrichAgentTeamProjection`；
3. `ApplyWorktree` / `DiscardWorktree`；
4. `ResolveApproval` / `SendInput`；
5. 最后再统一 `Spawn` / `Close` / `Resume` 这些宿主生命周期差异大的方法。

#### 6.7.2 TeamLifecycle

统一 `teamsupervisor` 的挂载：

- `desiredTeamIDs`、`SyncLoops`、`StopLoop`、`SupervisorSnapshot` 已共享；
- 把 `RunSettled` / `Pending` / `WaitForTerminal` / `closeTerminalTeammates` / `terminalSummaryReady` 的公共逻辑移入 `runtimehost.TeamLifecycle`；
- 宿主只提供：base session/team 判定、session closer、terminal services、mailbox 投递、日志。
- 写序必须单源冻结（尤其 spawn rollback 的 CLI/API 写序差异，见 `docs/plan/multi-agent-execution-optimization-plan.md:1095`）。

#### 6.7.3 GoalDriver

按现有 `aicli-goal-driver-unification-plan-20261018.md` 推进：

- `internal/goal/driver`：`DriverView` + `Decide` + `Loop`（纯策略 + 单 in-flight）；
- `runtimehost.GoalDriver`：host adapter（load session / submit continuation / persist / audit）；
- 用**驱动者租约**替代 CLI 冷却窗；
- 在租约落地前，保持 TUI 默认开、server 默认关，禁止两端同时开。

#### 6.7.4 Background / Memory

- `BackgroundProjector`：统一 job terminal/monitor 事件 → supervision inbox/wake；宿主只注入 event bus、store、session lookup。
- `MemoryRuntimeFactory`：统一 `memorystore.Config` + `memoryruntime.Config` 构造；宿主只提供 workspace/user/worker ID/LLM/session source。当前两端配置块 99.6% 相同，属于低风险高收益抽取。

### 6.8 Web 文本主路径的收敛策略

#### 方案 A（推荐）：Web 会话回合也走 SessionActor

- `/api/agent/chat` 改为：
  1. 解析/绑定 session；
  2. `SessionExecutor.SubmitAsync`（或 `SubmitTurn` 短等待）；
  3. 通过 `SessionExecutor.Events()` 订阅 runtime events；
  4. 用现有 SSE 帧名做**投影层**（runtime event → web chat SSE），保持前端契约不变；
  5. `resume_on_disconnect` 继续用 active turn/event cursor 机制，但 active turn 由 executor 提供。
- 优点：
  - Web 文本、Web 图片、TUI 远程、TUI 本地、scheduled 全部同一 SessionActor；
  - 审批/plan/ask/goal/compact 语义自动一致；
  - 删除直连路径的重复 agent 装配、checkpointer、auto-compact、active turn 逻辑。
- 代价：
  - SSE 投影层与流式性能需要实测；
  - 现有 `/api/agent/chat` 的 13 类 SSE 帧要逐一对齐；
  - 部分“无 session”的 stateless 请求需要单独处理。

#### 方案 B（过渡/兼容）：保留 stateless 直连，但显式声明能力

如果短期内不能切换：

- 在请求/响应/健康检查中显式标记 `execution_mode=stateless_direct`；
- 该路径必须复用 `AgentAssembler`/`LoopAssembler`，并安装与 actor 相同的审批桥（或明确 `dont_ask` 失败闭合）；
- 文档和前端必须知道该路径**不具备**跨请求审批/挂起/plan/goal 恢复；
- 不允许继续以 `execution_core=session_actor` 对外宣传。

**建议**：先做 P1（共享装配），再做 P3（Web 路径切 actor）；不要只做 P3 而不统一装配，否则只是把三套装配变成两套。

### 6.9 运行时状态与事件落盘的收敛策略

#### 6.9.1 短期：Authority 显式化

在 `/api/runtime/status`、`/health`、TUI `/status`、`/debug` 中增加：

```json
{
  "execution_authority": "runtime-server | aicli-local | scheduled",
  "runtime_store_id": "<canonical path or store id>",
  "event_store_id": "<canonical path or store id>",
  "session_lease_owner": "...",
  "event_persist": {
    "mode": "sync | batch",
    "async_dispatch": true,
    "source": "host-default | config"
  }
}
```

规则：

- TUI local 发现 session 被 server actor 持锁时，拒绝本地执行并提示用 remote 模式；
- runtime-server 发现 session 被 aicli actor 持锁时，返回 409 并提示用 remote/CLI；
- 不允许两个宿主各自打开不同 runtime store 却表现得像“同一会话状态”。

#### 6.9.2 中期：事件落盘单源

- 新建 `runtimehost.ResolveEventPersistSettings(hostKind, cfg)`，删掉两个宿主文件中的策略分支；
- 用 golden 测试固定两种 host 的入队/落盘/丢弃行为；
- 默认值是否统一为 `Disable` 语义，作为独立配置决策；在统一前，至少把差异写进状态与日志；
- 保留 `chat.EventPersistBuffer` 作为唯一实现（当前已经是共享的）。

#### 6.9.3 长期：单写者或明确分区

| 方案 | 做法 | 评价 |
|---|---|---|
| **A. 单权威（推荐先做）** | 同一 session 只允许当前 execution authority 写 runtime state；其他宿主通过 HTTP/事件游标读 | 风险最低，符合现有 lease |
| **B. 单写者服务** | 把 runtime state/event store 做成 runtime-server 内的单写者服务，所有宿主通过 API 写 | 真正共享，但工程量大 |
| **C. 多进程共享 SQLite** | 所有宿主直接打开同一 SQLite | **不推荐**：`docs/ANALYSIS-sqlite-lock-problem.md` 已证明会因 busy_timeout/锁导致健康检查挂起 |
| **D. 只读镜像** | 每个宿主写自己的 store，异步镜像事件到共享只读库 | 最终一致，复杂，易出现重复/顺序问题 |

**建议路线**：A → B（如果需要真正的跨宿主运行态共享）；C 不能作为默认方案。

### 6.10 TUI 远程命令能力矩阵

在 `chatSlashCommandCatalog` / `runtimeCommandRegistry` 的 `runtimeCommandSpec` 增加：

```go
type CommandTransport uint8
const (
    CommandTransportLocal CommandTransport = 1 << iota
    CommandTransportRemote
)
```

或复用现有 `runtimeEffectScope` 增加 `Transport` 字段。远程模式 dispatch 时：

- `Remote` 不允许 → 直接返回明确结果：
  “该命令在 runtime-server 模式不可用；请使用 Web 控制台或 `--runtime-mode local`。”
- 禁止 fallback 到 `LocalRuntimeHost` 或本地文件。
- 对已实现的远程命令（`/compact`、`/backtrack`、`interrupt`）保持现状。

首批远程适配建议：

| 命令 | 现有服务端端点 | 优先级 |
|---|---|---|
| `/plan` / `/plans` | `/sessions/{id}/plan`、`/plans*` | P1 |
| `/agents` 列表/状态/transcript | `/sessions/{id}/agents*`、`/agent-control/agents` | P1 |
| `/schedule` | `/schedule/catalog`、`/schedule/list` | P2 |
| `/memory` | `/memory/*`、`/harness/memory` | P2 |
| `/team` | `/teams*` | P2 |
| `/supervision` | `/supervision/*` | P3 |
| `/bots` | 保持显式“server 模式不支持”，或接连接器 API | P3 |

---

## 七、分阶段实施计划

> 粗估为工程人日，不含评审、并发 WIP 协调与真实终端回归排队。

### P0 —— 冻结现状 + 诊断/契约护栏（2–3 人日）

**目标**：不改变行为，先把“同源/分叉”变成可测、可见。

**交付**：

1. 新增 `internal/runtimehost/parity` 测试夹具：
   - `AgentFingerprint`：provider/model/system prompt hash/options（排序）/tool policy/allowlist/deny/readOnly/sandbox/hook names/scheduler config；
   - `LoopFingerprint`：max steps/tool calls/parallel/reasoning/budget/accounting/main routing/completion/obligations；
2. 新增 `EventPersistenceParity`：同一事件序列经两宿主 bridge → 同一类型别名/seq/顺序/丢弃规则；
3. 新增 `CommandParity`：同一 session/命令经 TUI local 与 runtimeapi command API → 同一 state/event sequence；
4. 在 `/api/runtime/status`、TUI `/status`、`/debug` 增加 authority/store/persist 字段；
5. 在命令目录增加 `local/remote/both` 能力元数据，先只用于诊断（不改变 dispatch）；
6. **阻断修复 R8**：在 `runtimehost.Deps` 增加独立的 run 级租约保证，或让 `localChatPrepareRunHook` 在 stall 后重取租约；补“stall 释放后下一轮仍持锁”的回归测试；
7. **阻断修复 R9**：把 TUI 远程 `getRuntimeServerState` 的路径改为 `/api/runtime/sessions/{id}/runtime`（或服务端补兼容路由），并补“stream 不可用时仍能靠 state 收敛”的测试。

**验收**：

- `go build ./...`、相关包 `go test` 绿；
- 新契约测试能稳定复现当前差异（允许先以“当前行为快照”形式存在）；
- 状态/诊断输出能看出 TUI local 与 server 实际使用的 store/persist 策略。
- R8/R9 的回归用例在修复前能复现、修复后绿；这两项是 P0 中唯一允许改变行为的部分。

**回滚**：新增测试/诊断字段均可独立删除。

### P1 —— 共享 Agent/Loop 装配（5–8 人日）

**目标**：消除 TUI local 与 runtime-server actor 的 Agent/Loop 双实现。

**交付**：

1. 新建 `runtimehost.AgentBuildInput` / `AgentBuildResult` / `AgentAssembler`；
2. 把 `buildLocalChatAgent` 与 `buildSessionAgentForContext` 的公共部分迁入，宿主只保留：
   - profile 解析（中性 `ProfileView`）；
   - prompt composition；
   - MCP/skill/tool surface 取得；
   - hooks/knowledge/memory 注入；
3. 新建 `LoopAssembler`，统一 `TurnBudgetTokens`、`ContextAccountingMode`、main routing、completion、obligations；
4. 修复已确认漂移：D1/D2/D3/D4/D6/D7/D8/D9；
5. TUI `aicliRuntimeDeps` 与 API `SessionDeps` 改为调用共享 assembler；
6. Web direct path 暂时保留，但也改用共享 assembler（减少第三条实现）。

**验收**：

- 同一 session metadata + runtime config + profile，TUI local 与 server actor 的 `AgentFingerprint` / `LoopFingerprint` 逐字段相等（宿主硬边界除外）；
- 已知漂移项有定向测试；
- `cmd/aicli/commands`、`internal/api/runtimeapi`、`internal/runtimehost` 全量回归绿；
- 真实 ConPTY 冒烟：本地起会话、跑一轮、工具/审批正常。

**回滚**：每个 assembler 可逐步切换；旧函数保留到对照测试通过。

### P2 —— 共享控制面（8–12 人日）

**目标**：消除 AgentControl/Team/Goal/Background/Memory 双实现。

**交付**：

1. `runtimehost.AgentController` 实现 `toolbroker.AgentSessionController`；
2. 优先迁移 `Wait/Budget/ReadEvents/Worktree/Approval/SendInput`，再迁移 `Spawn/Close/Resume`；
3. `runtimehost.TeamLifecycle`：复用 `teamsupervisor`，统一 terminal settle/cleanup/写序；
4. `runtimehost.BackgroundProjector`；
5. `runtimehost.MemoryRuntimeFactory`；
6. `internal/goal/driver` Phase 2（决策单源）+ `runtimehost.GoalDriver` 适配器；驱动者租约进入设计。

**验收**：

- 同一 registry/team/batch 数据在两宿主下 `List/Wait/Close/Resume` 返回同一状态与事件；
- team lifecycle 的 spawn rollback 写序有单一实现与契约测试；
- goal driver 同一终态只产生一个 decision；无双驱动；
- memory runtime 配置指纹一致（除 worker ID/workspace 预期差异）。

**回滚**：按 controller 方法分批迁移；旧方法保留到对应测试通过。

### P3 —— Web 文本主路径切 SessionActor（5–8 人日）

**目标**：让 `/api/agent/chat` 与 TUI remote/local、scheduled 走同一执行核。

**交付**：

1. `SessionExecutor` 拥有 hub/提交/事件/关闭；
2. `/api/agent/chat` 改为 `SubmitAsync` + runtime event → web SSE 投影；
3. 复用 active turn/event cursor，删除直连 checkpointer/auto-compact/agent 装配；
4. `resume_on_disconnect` 与 interrupt 继续工作;
5. 保留 `stateless_direct` flag 作为过渡，默认关闭；
6. 健康检查区分 `execution_core` 与 `entrypoint_execution_core`。

**验收**：

- Web 文本、Web 图片、TUI remote 对同一 session 产生同一 actor state/approval 语义；
- 审批型工具在 Web 文本回合能出现 `approval_requested` 并由 `approve_tool` 恢复；
- SSE 帧名/字段与现有前端契约兼容；
- 流式延迟/吞吐不低于基线（需性能对比）；
- `resume_on_disconnect` 刷新续传回归。

**回滚**：`/api/agent/chat` 切回 stateless flag；保留旧路径一个观察期。

### P4 —— TUI 远程命令补齐/显式拒绝（5–8 人日）

**目标**：远程模式不再静默操作本地数据，也不再给出误导性“未启用”。

**交付**：

1. 命令能力矩阵上线：`local/remote/both`；
2. 未实现远程的命令统一返回明确错误；
3. 实现 `/plan`、`/plans`、`/agents`、`/schedule` 的 HTTP adapter；
4. `/memory` 改为 server API 或显式不可用；
5. `/team`、`/supervision` 按优先级补；
6. `/bots` 保持显式降级或接连接器 API。

**验收**：

- 远程模式下任何命令不得访问 `LocalRuntimeHost`/本地 memory/本地 schedule；
- 未实现命令有可操作提示（建议 Web 控制台或 local 模式）；
- 远程 `/plan`、`/schedule`、`/agents` 与 Web 端点返回同一状态；
- 新增远程命令矩阵测试。

**回滚**：能力矩阵默认“旧行为”直到每个 adapter 验收；但不得保留静默本地 fallback。

### P5 —— Runtime state authority 收敛（5–10 人日，可与 P3/P4 并行设计）

**目标**：把“同一 session 的运行态”从“可能两份”收敛到“明确一份”。

**交付**：

1. authority/store id 进入 session 诊断与 lease 冲突文案；
2. TUI local / server 的 lease 冲突提示按 authority 给出可操作建议；
3. 评估并实现：
   - 首选：server 作为 runtime authority，TUI remote 读写均走 API；
   - 或：单写者 runtime 服务；
   - 不采用多进程直接共享 SQLite 作为默认；
4. 迁移脚本/兼容读取旧 `chat_runtime.sqlite`（如需要）；
5. 文档更新：`runtime-server-aicli-shared-session-mechanism-plan.md`、`ANALYSIS-sqlite-lock-problem.md`。

**验收**：

- 同一 session 在 TUI local 与 server 之间不会同时出现两个可写 runtime store；
- 跨模式 resume 能明确说明“运行态来自哪个 authority”；
- 现有 SQLite 健康检查不再因共享 store 挂起。

### P6 —— 清理旧实现（2–3 人日）

- 删除 `AICLI_EXECUTOR_REUSE` 逃生门与 TUI 旧 `buildSessionActor` 装配；
- 删除 `handlerExecutor` 适配器（或降为薄转发）；
- 删除重复的 `applyAPI*` / `applyLocal*` 中已迁移的公共部分；
- 删除重复 goal/team/background/memory 代码；
- 更新 stale 注释（例如 `chat_actor_executor_deps.go:86-87` 仍写“本函数没有调用方”，实际已被默认调用）。

---

## 八、测试与验收矩阵

### 8.1 契约测试（必须）

| 测试 | 输入 | 断言 |
|---|---|---|
| `TestAgentAssemblyParity` | 同一 session metadata + runtime config + profile + MCP/skill stub | `AgentFingerprint` 逐字段相等（宿主硬边界除外） |
| `TestLoopAssemblyParity` | 同一 `AgentBuildResult` + session context | `LoopFingerprint` 逐字段相等 |
| `TestToolPolicyParity` | 同一 profile + runtime sandbox + child flags | allowlist/deny/readOnly/capability/sandbox/path anchor 相等 |
| `TestEventPersistenceParity` | 同一事件序列（含 delta/critical/producer-persisted） | 类型别名、seq、顺序、丢弃规则、关闭 flush 相等 |
| `TestCommandParity` | 同一 session + submit/continue/interrupt/approve/answer/compact/rewind/plan | state/event sequence 相等 |
| `TestAgentControllerParity` | 同一 registry/team/batch/worktree fixture | Wait/List/Close/Resume 状态与事件相等 |
| `TestGoalDriverSingleOwner` | 两宿主同时启用 | 同一终态只提交一次；租约/让位可观测 |
| `TestRemoteCommandMatrix` | 远程模式逐命令 | 只调用 HTTP adapter；不触碰 `LocalRuntimeHost` |

### 8.2 E2E 场景（必须）

1. **TUI local vs TUI remote**：同一 session、同一 prompt、同一配置 → 相同 tool surface、permission mode、approval 行为、事件序列、最终 history。
2. **Web text vs TUI remote**：同一 session、审批型工具 → Web 能出现 pending approval 并恢复；TUI remote 行为一致。
3. **Web image vs Web text**：同一 session 带图/不带图 → 同一 actor、同一状态。
4. **跨进程恢复**：TUI local 崩溃后 server/TUI remote resume → 明确 authority/store，无重复执行。
5. **双驱动竞争**：goal active + supervision wake + CLI/server 同时在线 → 至多一个 driver，wake 优先。
6. **远程命令**：`/plan`、`/schedule`、`/agents`、`/memory` 在 remote 模式要么走 server，要么明确拒绝；不得读本地 store。
7. **权限**：default/accept_edits/plan/bypass/dont_ask 在 TUI local、TUI remote、Web actor 三路径的 allow/deny/ask 结果一致（宿主硬边界除外）。

### 8.3 工程验收

- `go build ./...`
- `go vet ./...`（相关包）
- `gofmt -l` 干净
- `go test ./internal/runtimehost ./internal/chat ./internal/agent ./internal/toolbroker ./internal/api/runtimeapi ./cmd/aicli/commands`
- 真实 ConPTY 冒烟：起会话、跑一轮、审批、interrupt、resume、compact、plan
- 性能对比：Web SSE 首字节/帧延迟、TUI 流式吞吐、actor 内存/goroutine 数不劣化

---

## 九、风险、开放问题与决策建议

### 9.1 风险

| # | 风险 | 缓解 |
|---|---|---|
| 1 | Web 切 actor 后 SSE 帧/时序/性能变化 | 投影层 + golden SSE 帧测试 + 性能基线 |
| 2 | Actor 在高频流式下的事件总线/落盘压力 | 复用 `EventPersistBuffer`、delta drop、异步派发；压测 |
| 3 | 多进程 SQLite 锁死 | 坚持 authority/单写者；不把合并 store 当第一步 |
| 4 | 行为漂移（工具面/权限/prompt） | 先冻结 fingerprint/golden，再逐项迁移 |
| 5 | TUI local 离线能力被破坏 | `HostProfile=aicli-local` 保留完整本地实现；remote 只是另一种 adapter |
| 6 | 与在途 WIP 冲突 | 分批、单文件/单方法迁移；每批 `git status`/测试 |
| 7 | 远程命令补齐后权限/多用户问题 | 服务端端点已有 auth/scope；TUI 只做 client，不绕过服务端策略 |
| 8 | Goal 双驱动 | 驱动者租约，先落地再考虑默认开启 server |

### 9.2 开放问题与建议

| # | 问题 | 建议 |
|---|---|---|
| Q1 | Web 文本是否必须走 actor？ | **是**，至少对 session-bound 回合；stateless 仅用于显式 one-shot。 |
| Q2 | 是否合并 runtime state store？ | **先 authority（A），再评估单写者服务（B）**；不采用多进程共享 SQLite。 |
| Q3 | Event persist 默认值是否统一？ | 代码层先统一 resolver + 诊断；默认值作为独立配置决策，Observe 一周后再切。 |
| Q4 | TUI remote 应支持哪些控制命令？ | 以“用户是否需要在终端完成该操作”为准；不支持就显式拒绝，不静默本地执行。 |
| Q5 | Goal driver 默认值？ | 先单源化 + 租约；在租约落地前保持 TUI on / server off。 |
| Q6 | Profile/Prompt 是否应完全同源？ | **是**；宿主只允许“workspace/凭据/离线能力”差异，不允许 prompt 结构差异。 |
| Q7 | `/api/agent/chat` 的 stateless 兼容期多长？ | 一个发布周期 + 观察期；期间显式标记 `stateless_direct`，不得宣传为 actor core。 |

### 9.3 不建议的做法

- **不要**直接把 TUI 的 `session_runtime.sqlite` 路径指给 runtime-server，或把 `defaultPersistence` 改成 `file` 来“统一”——已有锁死证据。
- **不要**只把 Web `/api/agent/chat` 改成调用 `SubmitAsync` 而不统一 Agent/Loop 装配；那样只是把三套装配变成两套，漂移仍在。
- **不要**为了“同源”删除 TUI 本地离线模式；正确做法是同一执行核 + 不同 `HostProfile`。
- **不要**在远程模式保留 `LocalRuntimeHost` 的隐式 fallback；那会让用户以为在操作 server 会话，实际写本地文件。
- **不要**在没有契约测试的情况下删除 `AICLI_EXECUTOR_REUSE` 旧路径；先证明默认路径在真实终端/审批/子代理场景下等价。

---

## 十、与既有文档的修订关系

| 文档 | 需要修订/补充 |
|---|---|
| `docs/automation-tasks/runtimehost-executor-extraction-plan.md` | E3.4c 仍未完成；`Executor` 仅 scheduled 使用；应补“Web `/api/agent/chat` 不走 actor”的现状 |
| `docs/automation-tasks/aicli-executor-reuse-design.md` | 明确“复用 actor 工厂 ≠ 复用 Agent/Loop 装配”；D2–D4 仍为双实现 |
| `docs/plan/runtime-server-aicli-shared-session-mechanism-plan.md` | 补当前 runtime store 默认分叉与 authority 建议；修正“server 模式走 `/api/agent/chat`”的旧描述（当前 TUI remote 走 command API） |
| `docs/plan/aicli-goal-driver-unification-plan-20261018.md` | Phase 2/3 未完成；补“server 默认关、CLI 默认开”的现状与租约方案 |
| `docs/architecture/unified-tool-surface-registry.md` | T1/T4 是“请求面标注 + profile 双落点”，不等于 Agent/Loop 装配同源；补本条边界 |
| `docs/plan/runtime-store-event-persistence-batching-and-async-plan-20260918.md` | 双宿主桥已共享 buffer，但默认策略不同；补 `Disable` vs `BatchingEnabled` 的当前差异 |
| `docs/ANALYSIS-sqlite-lock-problem.md` | 作为 P5 authority 决策的输入；补“不要用多进程共享 SQLite 作为同源方案” |

---

## 十一、本轮修复记录（2026-11-14）

本轮按 P0 + 部分 P1/P4 落地，未改变 actor/Web 主路径的结构，但消除了几处高风险语义差与静默回退。

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F1 | 事件落盘默认策略统一：两个宿主共用 `runtimehost.ResolveEventPersistSettings`，默认批量 + AsyncDispatch，`disable=true` 为唯一回退；server 侧补齐 critical/stream-delta 分类器 | ✅ | `backend/internal/runtimehost/event_persist.go`；`backend/internal/api/runtimeapi/handler.go:4991`；`backend/cmd/aicli/commands/chat_actor_host.go:1124` |
| F2 | 修复 server 事件桥在懒加载 store 时的 `sync.Once` 重入死锁：buffer 创建移出 Once，先订阅再创建，store reload 事件按非落盘控制事件处理 | ✅ | `backend/internal/api/runtimeapi/handler.go:4948-5005` |
| F3 | Web `/api/agent/chat` 安装会话权限引擎（默认 `default`），审批型工具在无跨请求审批通道时 fail-closed，而不是仅走静态策略 | ✅ | `backend/internal/api/runtimeapi/handler.go:2373-2388` |
| F4 | Web 直连回合注入 active goal guidance，与 actor `OnRunPrepare` 同口径 | ✅ | `backend/internal/api/runtimeapi/handler.go:2389-2398` |
| F5 | `buildSessionLoopConfig` 透传 `context.accountingMode`，消除 server actor 路径与 TUI 的 accounting 漂移 | ✅ | `backend/internal/api/runtimeapi/session_runtime_support.go:4866-4875` |
| F6 | API child agentdef 解析补齐 `ProfileRoot`，与 TUI `agentdefDiscoverOptions` 的 profile 级发现范围对齐 | ✅ | `backend/internal/api/runtimeapi/session_agent_build.go:139-144`；`session_runtime_support.go:4776` |
| F7 | 健康检查增加 `entrypoint_execution` 映射，显式区分 Web 文本 `stateless_direct` 与 command API `session_actor` | ✅ | `backend/internal/api/runtimeapi/handler.go:10931-10945` |
| F8 | TUI 远程模式对 `/memory`、`/plan`、`/plans`、`/agents`、`/supervision` 显式拒绝，禁止静默操作本地数据 | ✅ | `backend/cmd/aicli/commands/chat_runtime_server.go:45-80`；`chat_command_result.go:205-211`；`command.go:146-155` |
| F9 | TUI 远程 `/schedule` 改读 server `/api/runtime/schedule/catalog`，与本地 service 共用同一目录投影/排序 | ✅ | `backend/cmd/aicli/commands/chat_schedule_command.go:33-50`；`chat_runtime_server.go:1002-1023` |
| F10 | TUI 复用执行核时把 `localChatPrepareRunHook` 从 `PrepareRun` 移到 `OnRunPrepare`：默认租约获取不再被宿主 hook 替换，run stall 释放后下一轮会重新取锁 | ✅ | `backend/cmd/aicli/commands/chat_actor_executor_deps.go:173-178` |
| F11 | TUI 远程状态轮询路径从错误的 `/runtime/state` 改为服务端真实路由 `/runtime`；测试 fixture 同步修正 | ✅ | `backend/cmd/aicli/commands/chat_runtime_server.go:1570-1578`；`chat_runtime_server_test.go:566,780` |
| F12 | `FinalizeActiveCellAction` 从 Scene 快照重建 Active 时按语义投影重新推导 stable 边界，避免 finalized mutable cell 的 `Stable/Enqueued/Acked` 保持全零、使流式尾部账本校验失败 | ✅ | `backend/cmd/aicli/ui/controller_state.go:541-547` |

### 11.1 新增/更新测试

- `backend/internal/runtimehost/event_persist_test.go`：默认启用、disable 权威、调优/分类器保留。
- `backend/internal/api/runtimeapi/session_loop_config_test.go`：`ContextAccountingMode` 透传。
- `backend/internal/api/runtimeapi/runtime_event_bridge_batch_test.go`：改用 `Disable` 语义，覆盖 batch/sync 双路径。
- `backend/internal/api/runtimeapi/handler_test.go`：健康检查 `entrypoint_execution`。
- `backend/cmd/aicli/commands/chat_runtime_server_commands_test.go`：远程命令显式拒绝、远程 schedule catalog、本地 host 不被误判为 remote。
- `backend/cmd/aicli/commands/chat_runtime_events_terminal_projection_test.go`：既有流式尾部/原生历史回归用例在 F12 后通过。

### 11.2 验证结果

- ✅ `go test ./internal/runtimehost` 全量通过。
- ✅ `go test ./internal/api/runtimeapi -run 'TestBuildSessionLoopConfig|TestApplyAPIChildAgentdefToolPolicy|TestGetRuntimeHealth_ReturnsSummary|TestRuntimeServerEventBridge|TestRuntimeHealthIncludesPersistStats|TestRuntimeHealthOmitsPersistStats'` 通过。
- ✅ `go test ./internal/api/runtimeapi -run '^TestAgentChat'` 全部通过（无失败输出）。
- ✅ `go test ./cmd/aicli/commands -run 'TestRemoteUnsupportedCommands|TestRemoteScheduleReadsServerCatalog|TestLocalSessionWithHTTPExecutor|TestLocalRuntimeEventBridgeDefaults'` 通过。
- ✅ `go test ./cmd/aicli/commands -run 'TestAicliExecutorReuseContract|TestAICLIRuntimeServerChatExecutorUsesRuntimeCommands...'` 通过（租约/远程状态路径回归）。
- ✅ `go test ./cmd/aicli/commands -run '^TestStreamingAssistantFinalTailTransfersExactlyOnceToNativeHistory$'` 通过（F12）。
- ⚠️ `go test ./cmd/aicli/commands` 全量或同进程组合运行时曾出现 2 个 flaky 失败：
  - `TestAICLIChatActorExecutor_AutoStartTeamClosesNonLeadTeammateSessionAfterTerminal`
  - `TestAICLIChatActorExecutor_FailedAutoStartTeamClosesNonLeadTeammateSessionAfterTerminal`

  两者已在 §11.4 F17 用 teammate hold 门控修复时序竞态；连续 10 次运行通过。

### 11.3 尚未完成（继续按 P1–P5 推进）

- P1 的共享装配原语已完成：Agent base config、Loop config、child tool policy、context/memory/knowledge options、SessionContext 抽取均已单源化；**高层 AgentAssembler 尚未完成**，`buildSessionAgentForContext` 与 `buildLocalChatAgent` 仍是宿主薄包装（profile/prompt/tool-surface/hooks 仍属宿主边界）。
- P1 的已知 D1–D10 配置/策略漂移已全部关闭；剩余仅是高层 `AgentAssembler` / `LoopAssembler` 的结构性抽取。
- P2：AgentControl Wait/ReadEvents、Background projection、Memory runtime factory、Goal 系统态与续跑决策已单源；Spawn/Close/Resume/worktree/approval 等完整生命周期仍双实现。
- P3：Web 核心文本路径已切 durable `SessionActor`（单条 user message + `enable_react`）；多消息重放、Team、planned-subagent、route/planning、patch、`disable_tools` 仍显式走直连 fallback。
- P4：`/plan`、`/plans`、`/agents`（含 view/status/events/send/close/resume/routing）、`/memory`（status/list/search/add/show/delete/activate/consolidate/mode）、`/supervision`（status/audit/wake/list/ack/defer/resolve/control/metrics/state）已接 server HTTP adapter。
- P5：P5.0 authority/store 诊断契约已落地；独立共享 lease/authority store、租约丢失停止 writer、Team store 双重打开修复仍未落地。

### 11.4 第二轮修复记录（P1 共享装配）

本轮把“可共享的装配原语”从两个宿主中抽出，而不是只修单点默认值。目标仍是最终形成
`AgentAssembler` / `LoopAssembler`；本轮先完成其下层稳定接口和 parity 测试。

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F13 | 新增共享 Agent/Loop 基础装配：`AgentConfigSpec`、`BuildAgentConfig`、`ApplyAgentConfigLimits`、`ApplyAgentRequestOptions`、`LoopConfigSpec`、`BuildLoopReActConfig`。API actor、TUI local、Web direct 三条路径统一 runtime 限制、stream/reasoning、context accounting、turn budget 与 reasoning 优先级 | ✅ | `backend/internal/runtimehost/assembly.go` |
| F14 | child 工具策略单源化：`ApplyChildReadOnlyPolicy`、`ApplyChildShellPolicy`、`ApplyChildDepthPolicy`、`ApplyChildAgentdefToolPolicy`。API/TUI 只保留各自的 agentdef 发现（project/profile/folder-trust），应用顺序、能力收窄、deny、sandbox 前 `PathAnchorRoot` 完全一致（D8） | ✅ | `runtimehost/assembly.go`；`session_agent_build.go`；`session_runtime_support.go`；`chat_actor_host.go` |
| F15 | context/memory/knowledge/workspace/artifact options 单源化：`BuildContextOptions` / `ApplyContextOptions` / `ApplyMemoryOptions` / `ApplyKnowledgeContextOptions`。修复 TUI 在仅配置 `context.maxImageAttachmentTokens` 或 artifact 时因提前返回而漏注入的问题（D10） | ✅ | `backend/internal/runtimehost/context_options.go`；`handler.go`；`chat_actor_host.go` |
| F16 | TUI `deriveLocalSessionContext` 直接复用 `runtimehost.ExtractSessionContext`，不再维护第二套 metadata key 映射（D6）。抽取保持角色无关；TUI 在 base 会话上显式过滤 child route provider/model/reasoning，避免 stale requested model 污染 base（D5 部分修复） | ✅ | `backend/cmd/aicli/commands/chat_actor_executor_deps.go` |
| F17 | 两个 team cleanup 集成测试改为 teammate hold 门控：先断言 actor 存在，再释放 teammate 收敛。消除“快速 teammate 在 Execute 返回前已 cleanup”的调度竞态，不放宽终态清理断言 | ✅ | `backend/cmd/aicli/commands/chat_local_orchestration_integration_test.go` |

#### 11.4.1 新增/更新测试

- `backend/internal/runtimehost/assembly_test.go`：Loop 默认值/限制/accounting/budget/reasoning 优先级；Agent limits/request options；child agentdef 收窄；depth ceiling；稀疏 context/artifact options 合并。
- `backend/cmd/aicli/commands/chat_actor_executor_deps_test.go`：base/child 对同一 durable row 必须得到同一 `SessionContext`；profile/routing/observation/stream/disable_tools/permission mode 不再丢失。
- `backend/cmd/aicli/commands/chat_local_orchestration_integration_test.go`：auto-start / failed auto-start cleanup 用例增加确定性 hold；两用例 `-count=10` 通过。

#### 11.4.2 验证结果

- ✅ `go test ./internal/runtimehost`
- ✅ `go test ./internal/api/runtimeapi -run 'TestBuildSessionLoopConfig|TestApplyAPIChildAgentdefToolPolicy|TestContextOptions|TestKnowledgeAssembly|TestMemoryPolicy|TestApplyKnowledge|TestAgentChat'`
- ✅ `go test ./cmd/aicli/commands -run 'TestApplyLocalChild|TestBuildLocalChat|TestLocalChatRuntimeHostBuildSessionActor|TestKnowledgeAssembly|TestChatMemoryPolicy|TestAicliExecutorReuseContract'`
- ✅ `go test ./cmd/aicli/commands -run '^TestAICLIChatActorExecutor_(AutoStart|FailedAutoStart)TeamClosesNonLeadTeammateSessionAfterTerminal$' -count=10`
- ✅ `go test -p 1 -vet=off -timeout 600s ./cmd/aicli/commands` 全量通过（230.6s）。
- ⚠️ `go test ./internal/api/runtimeapi` 全量曾超过 10 分钟无输出，已取消并改用定向子集；全量长跑仍需单独归因，不能据此宣称全包绿。

#### 11.4.3 P1 剩余工作

1. 在现有共享原语之上抽出 `runtimehost.AgentAssembler` / `LoopAssembler`；host 只提供 profile/prompt、MCP/tool surface、hooks、runtime services 回调。
2. ✅ D2：已由 `RunMeta.BudgetTokens` + Web `budget_tokens` 完成（§11.5 F18）。
3. ✅ D3：已由 `SessionContext.AllowedRoots/ReadOnlyRoots` + `tool_base_path` 完成（§11.5 F19）。
4. ✅ D9：已统一 disable_tools 的 metadata + 空 allowlist 语义（§11.5 F20）。
5. D5：评估统一 base/child 分类函数；本轮只保证分类输入同源、TUI base route 覆盖被显式隔离。

### 11.5 第三轮修复记录（D2/D3/D9）

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F18 | per-run budget 进入 `team.RunMeta.BudgetTokens`；actor `cloneLoopConfigForRun` 用它覆盖 base loop budget；TUI `currentRunMetaForSession` 从 `ChatSession.TurnBudgetTokens` 注入；Web `/api/agent/chat` 新增 `budget_tokens` 请求字段 | ✅ | `internal/team/run_meta.go`；`internal/chat/actor.go`；`chat_actor_executor.go`；`handler.go` |
| F19 | `runtimehost.SessionContext` 增加 durable `AllowedRoots` / worktree `ReadOnlyRoots`；server actor、Web direct、TUI 统一通过 `ApplyToolBasePathOption` / `ApplySessionRootOptions` 投影到 agent options；`apiSessionReadOnlyRoots` 改为共享投影 | ✅ | `runtimehost/session_context.go`；`runtimehost/assembly.go`；`session_agent_build.go`；`handler.go`；`chat_actor_host.go` |
| F20 | disable_tools 统一：TUI 构建期发布 `llm.MetadataKeyDisableTools`；Web direct 新增 `disable_tools` 请求字段，保留完整请求目录但安装空静态 allowlist | ✅ | `runtimehost/assembly.go`；`chat_actor_host.go`；`handler.go` |
| F21 | Web direct 的流式/非流式 loop 统一走 `buildAgentChatLoopConfig`，共享 budget/accounting/reasoning/limits；非流式路径补齐 `MaxRepeatedPollCalls` | ✅ | `internal/api/runtimeapi/agent_chat_loop.go`；`handler.go` |
| F22 | 修正 team runner 测试中过期的工具提示断言（`ls` → `list_dir`），与实际 prompt 同源 | ✅ | `internal/team/teammate_runner_test.go` |

#### 11.5.1 新增/更新测试

- `internal/runtimehost/session_context_test.go`：AllowedRoots / ReadOnlyRoots、worktree repo、空 metadata 零值。
- `internal/team/run_meta_propagation_test.go`：`RunMeta.Clone` 保留 Source/BudgetTokens。
- `internal/chat/actor_test.go`：RunMeta budget 覆盖 base loop、零值保留、base nil 创建。
- `internal/api/runtimeapi/agent_chat_loop_test.go`：direct loop budget/accounting/parallel/limits。
- `internal/api/runtimeapi/handler_test.go`：direct `disable_tools` 保留请求目录；server agent options 投影 workspace/tool_base_path/roots。
- `cmd/aicli/commands/chat_actor_executor_test.go`：`currentRunMetaForSession` 携带 `TurnBudgetTokens`。
- `cmd/aicli/commands/chat_actor_host_test.go`：TUI agent 发布 disable_tools metadata option。
- `cmd/aicli/commands` 定向回归：TUI allowed roots、worktree read-only roots、session actor 构建、executor reuse 契约。

#### 11.5.2 验证结果

- ✅ `go test ./internal/runtimehost`
- ✅ `go test ./internal/team`
- ✅ `go test ./internal/chat`
- ✅ `go test ./internal/api/runtimeapi -run 'TestBuildAgentChatLoopConfig|TestAgentChat_EnableReAct_DisableToolsKeepsRequestSurface|TestBuildSessionAgentForContextProjectsDurableRootsAndToolBasePath|TestAPISessionReadOnlyRoots|...'`
- ✅ `go test ./cmd/aicli/commands -run 'TestCurrentRunMetaForSession|TestBuildLocalChatAgent_DisableTools...'`
- ✅ `go test -p 1 -vet=off -timeout 600s ./cmd/aicli/commands` 全量通过（251.0s，覆盖 D2/D3/D9 主体改动）。
- ✅ 最终 TUI session-context root 投影补丁的定向回归通过（allowed roots / worktree read_only_roots / actor 构建）。
- ⚠️ 最终重跑 commands 全包时 Windows linker 报 `Insufficient system resources`，属于本机构建资源耗尽，不是测试断言失败。

#### 11.5.3 后续

- D5 已由 §11.6 F23 统一；当前仅剩高层 assembler 的结构性抽取。
- 高层 `AgentAssembler` / `LoopAssembler` 仍可继续抽取；现阶段三条路径已共享 config/loop/policy/options/context 原语。

### 11.6 第四轮修复记录（D5）

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F23 | 新增共享 `runtimehost.IsBaseSession(sessionID, baseSessionID, SessionContext)`：child marker（agent_type/depth/read_only）与 base ID 不等取并集，任一成立即 child。server `isBaseSessionContext`、API main-agent routing、TUI `buildSessionActor`/`aicliRuntimeDeps` 全部改用同一判定 | ✅ | `runtimehost/session_context.go`；`handler_deps.go`；`session_agent_build.go`；`chat_actor_host.go`；`chat_actor_executor_deps.go` |

#### 11.6.1 测试与验证

- `internal/runtimehost/session_context_test.go`：base ID 命中、child ID、大小写、三类 child marker、unknown-host 保守并集。
- ✅ `go test ./internal/runtimehost`
- ✅ `go test ./internal/api/runtimeapi -run 'TestIsBaseSession|TestSessionGoal|TestBuildSessionAgentForContext|TestBuildSessionLoopConfig'`
- ✅ `go test ./cmd/aicli/commands -run 'TestLocalChatRuntimeHostBuildSessionActor|TestAicliExecutorReuseContract|TestDeriveLocalSessionContext|TestCurrentRunMetaForSession'`
- ✅ `go test -p 1 -vet=off -timeout 600s ./cmd/aicli/commands` 全量通过（231.3s）。

#### 11.6.2 剩余

- 仅剩高层 `AgentAssembler` / `LoopAssembler` 的结构性抽取；D1–D10 的已知行为漂移已全部关闭。

## 十二、P2 控制面进展

### 12.1 能力矩阵（当前）

| 控制面 | runtime-server | aicli TUI | 当前共享层 | 状态 |
|---|---|---|---|---|
| Goal | `session_goal_driver.go` + `/goal` HTTP | `chat_goal_driver.go` + `chat_goal_auto_continue.go` | `internal/goal` 状态模型、prompt、strike/round/budget 策略、系统态写入、`EvaluateGoalContinuation` | **部分统一**：状态字段写入与 goal-state 续跑判定已单源；驱动循环仍双实现 |
| AgentControl | `session_runtime_support.go` 控制器 | `chat_actor_registry.go` 控制器 | `AgentSessionController` + `AgentWaitFlow` + `AgentEventsFlow` | **部分统一**：Wait/ReadEvents 外壳已单源；其余方法仍双实现 |
| Team lifecycle | `team_lifecycle.go` | `chat_team_lifecycle.go` | `teamsupervisor.Supervisor` + `runtimehost.BuildTeamSupervisorConfig` | **部分统一**：reconciler 核心与配置映射共享；host hooks/event 输出仍独立 |
| Background | `background_supervision.go` | `chat_actor_background_supervision.go` | `supervision.ProjectBackgroundJob*` + event input mapper | **部分统一**：projection input/epoch/字段映射已单源；宿主投递策略仍不同 |
| Memory | `memory_runtime_wiring.go` | `chat_memory_runtime.go` | `memoryassembly.BuildFromRuntimeConfig` | **部分统一**：store/config/extractor/window summarizer 构造已单源；生命周期/event bus 接线仍属宿主 |

### 12.2 本轮切片：Goal 系统态写入

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F24 | `internal/goal` 新增系统态写入：`ApplySystemBlocked`、`ApplySystemRoundLimit`、`ApplySystemBudgetLimited`、`AddTokens`。CLI 与 server 只保留门控与持久化，blocked / round-limit / budget_limited / token 记账字段规则不再各写一份 | ✅ | `internal/goal/status_update.go`；`session_goal_driver.go`；`chat_goal_auto_continue.go` |

#### 12.2.1 测试

- `internal/goal/status_update_test.go`：system blocked 幂等、round-limit code/reason、budget_limited、token 记账与终态拒绝。
- ✅ `go test ./internal/goal`
- ✅ `go test ./internal/api/runtimeapi -run 'TestGoalDriver|TestSessionGoalDriver|TestGoal'`
- ✅ `go test ./cmd/aicli/commands -run 'TestGoal|TestMarkActiveGoal|TestAccountActiveGoal'`
- ✅ `go test -p 1 -vet=off -timeout 600s ./cmd/aicli/commands` 全量通过（279.2s）。

#### 12.2.2 后续

1. Goal 驱动循环的决策核心仍未统一：CLI 前台/事件循环与 server `session_end` 订阅的触发面、activation、supervision 让位仍需按 `aicli-goal-driver-unification-plan-20261018.md` 收敛为 `internal/goal/driver.Decide`。
2. AgentControl 的 Wait 外壳已统一（§12.3 F25）；ReadEvents / Spawn / Close / Resume / Worktree / Approval / SendInput 仍待迁移。
3. P1 高层 `AgentAssembler` / `LoopAssembler` 仍待结构性抽取，不属于 P2 行为修复。

### 12.3 AgentControl Wait 单源化

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F25 | 新增 `toolbroker.AgentWaitFlow`：统一 obligation 账本预读、等待预算 gate、等待后复读、terminal cohort 交付、terminal_delta、peek 与 budget stamping。server/TUI 的 `Wait` 只剩 ledger/waiter/terminal-reader 回调 | ✅ | `internal/toolbroker/agent_wait_flow.go`；`session_runtime_support.go`；`chat_actor_registry.go` |

#### 12.3.1 测试

- `internal/toolbroker/agent_wait_flow_test.go`：terminal cohort 不打开窗口、预算耗尽拒绝、等待后终态重置预算、interrupted 不消耗预算。
- ✅ `go test ./internal/toolbroker -run 'TestAgentWaitFlow|TestWaitBudget|TestStampAgentWaitBudget|TestBroker.*Wait'`
- ✅ `go test ./internal/api/runtimeapi -run 'TestSessionAgentControllerWait|TestWaitAgent'`
- ✅ `go test ./cmd/aicli/commands -run 'TestWaitAgent|TestLocalActorRegistryWait|TestAICLIChatActorExecutor.*Wait'`
- ✅ `go test -p 1 -vet=off -timeout 600s ./cmd/aicli/commands` 全量通过（244.0s）。

#### 12.3.2 后续

1. `ReadEvents` 的事件投影/超时结果已统一（§12.4 F26）；窗口循环仍双实现，下一步抽共享事件读取外壳。
2. Spawn/Close/Resume/Worktree/Approval/SendInput 仍由两个 controller 各自实现；优先迁移只读与预算相关方法，写生命周期后置。

### 12.4 ReadEvents 投影与超时单源化

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F26 | 新增 `toolbroker.BuildAgentEventsResult` / `BuildAgentEventsTimeoutResult`：统一 count、seq、latest_seq、payload deep-clone、`timed_out` 与 timeout next_action。server/TUI builder 改为薄包装；server `ReadEvents` 在 ID 为空时补齐 caller session fallback | ✅ | `internal/toolbroker/agent_events_result.go`；`session_runtime_support.go`；`chat_actor_registry.go` |

#### 12.4.1 测试

- `internal/toolbroker/agent_events_result_test.go`：payload deep-clone、seq/latest_seq、timeout guidance。
- ✅ `go test ./internal/toolbroker -run 'TestBuildAgentEvents|TestReadAgentEvents|TestBroker_ReadAgentEvents'`
- ✅ `go test ./internal/api/runtimeapi -run 'ReadEvents|AgentEvents|TestSessionAgentController.*Events'`
- ✅ `go test ./cmd/aicli/commands -run 'ReadEvents|AgentEvents|TestAICLIChatActorExecutor.*Events'`
- ✅ `go test -p 1 -vet=off -timeout 600s ./cmd/aicli/commands` 全量通过（242.4s）。

#### 12.4.2 后续

1. ✅ 共享 `AgentEventsFlow` 已完成（F28）。
2. ✅ server 已获得与 TUI 相同的 soft-timeout 语义。

#### 12.4.3 ReadEvents 窗口循环单源化

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F28 | 新增 `toolbroker.AgentEventsFlow`：统一窗口循环、500ms wake tick、mailbox 分支、soft-timeout、pagination 与 unread probe。server/TUI 只保留 resolve/subscribe/list/build 回调；server soft-timeout 与 TUI 对齐 | ✅ | `internal/toolbroker/agent_events_flow.go`；`session_runtime_support.go`；`chat_actor_registry.go` |

测试：

- `internal/toolbroker/agent_events_flow_test.go`：空窗口 timeout、pagination+unread、边界事件 soft-timeout、wake tick。
- ✅ `go test ./internal/toolbroker -run 'TestAgentEventsFlow|TestBuildAgentEvents|TestWaitForAgentEventsTick|...'`
- ✅ runtimeapi/cmd 的 events 定向回归。

### 12.5 P4 远程控制命令

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F27 | TUI remote 不再对 `/plan`、`/plans`、`/agents`、`/agent`、`/memory` 一刀切拒绝：已接入 server plan/plans、agent-control、memory API；未映射子命令仍显式 fail-closed，不访问本地 store | ✅ | `chat_runtime_server_control_commands.go`；`chat_runtime_server.go`；`chat_command_result.go`；`command.go` |

已接入：

- `/plan [status|enter|on|exit|approve|request_changes|quit]`
- `/plans [list|diff|reopen|<id>]`
- `/agents [list|status <id>|events <id>|view <id>|send <id> <msg>|followup <id> <msg>|close <id>|resume <id>]`
- `/agent <target>`（server event transcript 只读投影）
- `/memory [status|list|records|show|why-not <query>]`

保持显式拒绝：

- `/supervision`
- 未映射的 `/plan <sub>`、`/agents <sub>`、`/memory <sub>` 子命令

#### 12.5.1 测试

- `chat_runtime_server_commands_test.go`：httptest 验证 remote 命中 server API、无本地 fallback；未映射 `/memory add` 仍显式报错；`/supervision` 仍拒绝。

#### 12.5.2 后续

- `/memory add|search|activate|consolidate|...` 等写操作尚未接入；继续保持显式拒绝。
- `/agents defs|routing|cleanup` 尚未接入；list/status/events/view/send/followup/close/resume 已可用。
- `/supervision` 仍待映射 digest/snapshot/audit/action/wake 等控制面。

### 12.6 Background projection input 单源化

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F29 | 新增 `supervision.BackgroundJobTerminalInputFromEvent` / `BackgroundJobMonitorInputFromEvent` / `BackgroundJobPayloadString/Duration/TerminalEpoch`。server/TUI 不再各自拼装 terminal/monitor input；epoch、payload 类型宽容解析统一 | ✅ | `internal/supervision/background_projection.go`；`background_supervision.go`；`chat_actor_background_supervision.go` |

测试：

- `internal/supervision/background_projection_test.go`：terminal/monitor 字段映射、int/float64 duration、epoch。
- ✅ `go test ./internal/supervision -run 'TestBackgroundJob.*InputFromEvent|TestProjectBackgroundJob'`
- ✅ runtimeapi/cmd 的 background/supervision 定向回归。

后续：

- 宿主“投影后是否立即 wake / busy 让位”仍是各自策略；下一步可抽 `BackgroundWakeDispatcher`。

### 12.7 Memory runtime factory 单源化

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F30 | 新增 `memoryassembly.BuildFromRuntimeConfig`：统一 memorystore、`memoryruntime.Config`、policy 覆盖、LLM extractor、window summarizer、worker ID。server/TUI 只保留 workspace/UserID 与生命周期接线 | ✅ | `internal/memoryassembly/factory.go`；`memory_runtime_wiring.go`；`chat_memory_runtime.go` |

测试：

- `internal/memoryassembly/factory_test.go`：mode=off 不建库、共享 runtime 接线、缺失 host 依赖 fail-closed。
- ✅ `go test ./internal/memoryassembly`
- ✅ runtimeapi/cmd 的 memory 定向回归。

后续：

- idle sweep/session-end lifecycle 仍由宿主各自调度；下一步可抽 `memoryassembly.Lifecycle`。

### 12.8 第六轮切片：P3/P4/P5.0

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F31 | actor per-run 覆盖补齐 `RunRouteOverride.MaxSteps/Thinking`，并让 `SessionActor.nextTurnID` 尊重 `agent.WithTurnID`。Web/TUI 每轮 `max_steps`、thinking 与请求 `turn_id` 不再静默失效 | ✅ | `internal/chat/commands.go`；`internal/chat/actor.go`；`internal/chat/actor_resume_episode.go`；`internal/chat/actor_test.go`；`internal/chat/actor_resume_episode_test.go` |
| F32 | 抽取 `sessionActorClient.submitPromptResult`：AgentChat 与 command API 共享 `hub.GetOrCreate → supervision preflight → actor.SubmitPrompt`，AgentChat 保留原始 `agent.Result` 的 usage/observations/tool receipts | ✅ | `internal/api/runtimeapi/session_runtime_support.go` |
| F33 | `/api/agent/chat` 核心文本路径切 durable `SessionActor`：命名请求契约、保守 eligibility、preamble lease → actor run lease 交接、SSE worker/seed/result/done 投影、live tool bridge、非流式 envelope、usage ledger 保留；直连 fallback 对未迁移能力显式保留 | ✅（核心路径） | `internal/api/runtimeapi/agent_chat_request.go`；`agent_chat_actor.go`；`agent_chat_actor_events.go`；`handler.go` |
| F34 | P5.0 authority/store 诊断契约：共享 `sessionruntime.AuthorityInfo`/`StoreID`/owner-kind 映射；server `/runtime` 快照与 `session_persistence` 输出 execution authority、store id、durability、lease owner；TUI local/remote debug 展示同一结构；remote `/runtime` 响应缓存 authority | ✅（P5.0） | `internal/sessionruntime/authority.go`；`runtimeapi/session_authority.go`；`session_runtime_handlers.go`；`cmd/aicli/commands/chat_session_authority.go`；`chat_debug_display_http.go`；`chat_debug_document.go` |
| F35 | Team lifecycle 配置映射单源化：`runtimehost.BuildTeamSupervisorConfig` 统一 scan interval / restart backoff / max backoff；server/TUI 只保留 desired teams、run loop、event hooks | ✅（配置层） | `internal/runtimehost/team_lifecycle.go`；`runtimeapi/team_lifecycle.go`；`cmd/aicli/commands/chat_team_lifecycle.go` |
| F36 | P4 远程控制补齐：`/supervision` status/audit/wake/list/ack/defer/resolve/control/metrics/state；`/memory` add/search/show/delete/activate/consolidate/rebuild/mode；`/agents routing`；server 新增 notification resolve/control 端点，复用 `LocalControlService` 的 scope + allowed action + CAS | ✅（除显式无端点项） | `cmd/aicli/commands/chat_runtime_server_control_commands.go`；`runtimeapi/supervision_handlers.go`；`handler.go` |

#### 12.8.1 F33 行为边界

- **actor 路径**：`enable_react=true`、恰好一条 `user` 消息、无 Team/planned-subagent/route/planning/patch、未设置 `disable_tools`。
- **直连 fallback**：其余请求保持原行为；health 的 `entrypoint_execution` 同时报告 `core=session_actor` 与 `fallback_core=stateless_direct`，不能再仅凭 `execution_core` 推断全部入口同源。
- actor 自己负责 history、compact、approval suspension、goal session-end、run-scoped lease；Web adapter 只保留 HTTP/SSE/usage 投影。
- 请求 prelude 仍持短租约序列化 sessionmeta/workspace/profile 写入，然后在 actor 提交前释放，避免与 actor run lease 自冲突。

#### 12.8.2 验证

- ✅ `go test ./internal/chat -run 'TestCloneLoopConfig|TestNextTurnID'`
- ✅ `go test ./internal/api/runtimeapi -run 'TestAgentChat'`（含 actor 流式工具帧、resume/disconnect、单次历史落库）
- ✅ `go test ./internal/api/runtimeapi -run 'TestResolveSupervisionNotification'`
- ✅ `go test ./internal/sessionruntime -run 'TestAuthority|TestStoreID|TestBuildAuthority'`
- ✅ `go test ./internal/runtimehost -run 'TestBuildTeamSupervisorConfig'`
- ✅ `go test ./cmd/aicli/commands -run 'TestRemoteControlCommandsReadServerAPIs|TestRemoteSupervisionRequiresControlCaller|TestChatSessionAuthorityInfo'`

#### 12.8.3 明确保留的差异

1. **P3 fallback**：多消息重放、Team task、planned subagents、static/route planning、patch approval、`disable_tools` 尚未 actor 化；不能宣称 `/api/agent/chat` 全量同源。
2. **P5.1/P5.2 未完成**：lease 仍存于 runtime store，server/TUI 默认仍是两个 lease domain；尚无共享 authority store、租约丢失停止 writer、Team store 双重打开修复。
3. **P4 显式缺端点**：`/agents defs|cleanup` 在 runtime-server 仍无 HTTP 端点，TUI 会显式拒绝而不是读取本地 registry；execution supervisor watchdog 也只存在于本地。
4. **P2 生命周期**：AgentControl Spawn/Close/Resume/worktree/approval、Goal driver loop、Background wake/busy 策略、Memory idle sweep/session-end 仍属宿主边界；SessionHub 的关闭顺序已由 §12.9 F38 收敛。

### 12.9 第七轮切片：A 通道准入与 SessionHub 关闭

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F37 | A 通道（事件总线 → 会话事件库）的准入、`payload["seq"]` 生产者去重与 store 类型别名统一到 `runtimehost.PrepareSessionStoreEvent`。runtime-server 与 aicli 本地桥不再各自实现 session 空值判断、persisted 判定和别名映射；server 仍保留 drop 指标与同步/批量落盘差异 | ✅ | `internal/runtimehost/event_persist.go`；`internal/api/runtimeapi/handler.go`；`cmd/aicli/commands/chat_actor_host.go` |
| F38 | `Handler.CloseSessionHub` 统一执行核关闭责任：停止并清空 hub、阻止 shutdown 期间懒重建；`handlerExecutor.Close` 复用同一路径；runtime-server `app.close` 在关闭 memory/事件缓冲/store 之前先停 actor hub，避免 actor 继续向已关闭的数据面发布事件 | ✅ | `internal/api/runtimeapi/session_runtime_support.go`；`handler_executor.go`；`cmd/runtime-server/main.go` |
| F39 | TUI `localChatRuntimeHost.Close` 在 cleanupFns 逆序关闭事件缓冲/store 之前显式 `SessionHub.StopAll()`；hub 自身 cleanup 仍保留为半构造宿主兜底，避免晚到 actor 事件落入已关闭缓冲 | ✅ | `cmd/aicli/commands/chat_actor_host.go` |

#### 12.9.1 测试

- `internal/runtimehost/event_persist_test.go`：A 通道空 session、生产者已落盘、live-only、persisted+alias 四类准入与映射。
- `internal/api/runtimeapi/handler_session_hub_close_test.go`：关闭幂等、关闭后禁止 lazy 重建、`handlerExecutor.Close` 走同一生命周期。
- `cmd/aicli/commands`：host close/subagent operation、本地 A 通道桥、progress mirror session_end/close 清理回归。

#### 12.9.2 验证

- ✅ `go test ./internal/runtimehost -run 'TestPrepareSessionStoreEvent|TestResolveEventPersistSettings'`
- ✅ `go test ./internal/api/runtimeapi -run 'TestCloseSessionHub|TestHandlerExecutorClose|TestRuntimeServerEventBridge|TestRuntimeEventDelivery|TestAgentReclaim|TestRuntimeHealth'`
- ✅ `go test ./cmd/aicli/commands -run 'TestLocalChatRuntimeHostCloseWaitsForSubagentOperation|TestLocalRuntimeEventBridge|TestLocalProgressMirrorForgetsOnSessionEndAndHostClose|TestDeriveLocalSessionContext|TestBuildLocalChat'`
- ✅ `go test ./cmd/runtime-server`

#### 12.9.3 尚未收敛

- P5.1/P5.2 的独立共享 lease/authority store、租约丢失停止 writer、Team store 双重打开修复已在 §12.10 F43–F45 落地。
- 高层 `AgentAssembler` / `LoopAssembler` 仍保留宿主薄包装；本轮已把共享 service graph（scheduler/coordinator/skills/embedding/event bus/team context）抽到 `runtimehost.ApplyAgentServices`。
- P3 的 Team/Task context、`disable_tools`、非 ReAct route/planning/patch 仍保留 fallback；多消息重放、ReAct 下的 route/planning metadata、planned/patch 字段已 actor 化。

### 12.10 第八轮切片：P1/P2/P3/P4/P5 收敛

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F40 | P4 `/api/runtime/agent-definitions`：server 使用与 spawn 相同的 agentdef discovery/folder-trust；list/show/lint 文本由 `internal/agentdef.Render*` 单一实现，TUI remote `/agents defs` 直接消费 server 输出 | ✅ | `internal/api/runtimeapi/agent_definition_handlers.go`；`internal/agentdef/render.go`；`cmd/aicli/commands/chat_agent_defs.go`；`chat_runtime_server_control_commands.go` |
| F41 | P4 `/api/runtime/agent-control/cleanup`：server 复用 `SelectReclaimable`/`ReclaimAgentQuota`，支持 dry-run/idle；CLI 与 server 共用 `agentcontrol.ManualReclaimReport` 文本 | ✅ | `agent_control_cleanup_handler.go`；`internal/agentcontrol/manual_reclaim.go`；`chat_agent_cleanup.go` |
| F42 | P4 `/api/runtime/supervision/watchdog`：只读 execution supervisor 快照；TUI `/supervision watchdog` 与 `/debug supervision watchdog` 均走 remote endpoint，不读取本地看门狗 | ✅ | `execution_supervisor_handler.go`；`internal/supervision/execution_supervisor_view.go`；`chat_runtime_server_control_commands.go` |
| F43 | P5.1 独立共享 lease store：新增 `sessionRuntime.leaseStorePath/leaseStoreDSN`，默认 `<sessions.dir>/runtime/session_leases.sqlite`；runtime-server/aicli 分别持有独立 lease store，authority domain 使用 lease store ID | ✅ | `internal/config/manager.go`；`internal/sessionruntime/paths.go`；`runtimeapi/handler.go`；`cmd/aicli/commands/chat_actor_host.go` |
| F44 | P5.2 租约丢失 fence：`SessionLeaseHandle.Lost/Done/LostError`；runtimehost watcher 在续租永久失败时 `actor.MarkLeaseLost`，取消当前 run 并拒绝 session/state/event durable writes；下次成功取租约清除 fence | ✅ | `internal/chat/session_runtime_store.go`；`internal/chat/actor.go`；`internal/runtimehost/actor_factory.go`；对应测试 |
| F45 | P5.2 Team store 双重打开修复：`refreshTeamStore` 在 bootstrap 已打开同路径 store 时直接 adopt 并写入 config key，不再打开第二连接后关闭第一连接 | ✅ | `runtimeapi/handler.go`；`handler_session_persistence_test.go` |
| F46 | P2 Background 终态/巡检投影后立即尝试一次 drain（与 CLI 同口径）；busy/限流仍保持 durable | ✅ | `runtimeapi/background_supervision.go`；`background_supervision_test.go` |
| F47 | P2 Memory 生命周期：TUI 维护 sweep 的 cancel/done 并纳入 cleanup；session-end 使用 `WithoutCancel + timeout`，host Close 不再取消已接纳的 extraction | ✅ | `cmd/aicli/commands/chat_memory_runtime.go` |
| F48 | P2 AgentControl：API Close 改 `StopAsync`，避免 self-stop 自锁；Resume 复活 registry 行并重建 child completion subscription | ✅ | `runtimeapi/session_runtime_support.go` |
| F49 | P1 共享 service graph：`runtimehost.ApplyAgentServices` 统一 scheduler/coordinator/skills/embedding/event bus/team context；API actor、Web direct、TUI local 复用 | ✅ | `internal/runtimehost/assembly.go`；`runtimeapi/handler.go`；`cmd/aicli/commands/chat_actor_host.go` |
| F50 | Web direct child 安全收窄：agentdef policy、read-only floor、shell=false、depth ceiling 与 actor 路径一致 | ✅ | `runtimeapi/handler.go` |
| F51 | P3 Actor 扩展：多消息重放通过 `SubmitPromptOption.ReplaceHistory` 原子替换历史；ReAct 下 route/planning/patch/planned-subagent 字段不再被 eligibility 拒绝，route metadata 透传 actor 结果 | ✅ | `internal/chat/actor.go`；`internal/chat/commands.go`；`runtimeapi/agent_chat_request.go`；`runtimeapi/agent_chat_actor.go` |
| F52 | P3 每轮 overrides：`RunRouteOverride` 增加 context-only `TeamID/TaskID` 与 `DisableTools`；actor 在 run 前设置 agent options/空 allowlist，run 后恢复，且不改写 durable session route | ✅ | `internal/chat/commands.go`；`chat/actor.go`；`runtimeapi/agent_chat_actor.go` |
| F53 | P2 Memory maintenance 单源：`memoryassembly.StartMaintenance` 统一 ticker/startup recovery/freshness/digest/consolidation/retention；server/TUI 只保留 host 回调与日志 | ✅ | `internal/memoryassembly/maintenance.go`；`cmd/runtime-server/memory_runtime_boot.go`；`cmd/aicli/commands/chat_memory_runtime.go` |
| F54 | P2 worktree cleanup 对齐：API close 不再无条件删除 worktree；脏 worktree 保留并记录 `kept_uncommitted`，仅干净 worktree 自动移除，与 CLI 产物保全契约一致 | ✅ | `runtimeapi/session_runtime_support.go:cleanupAPISpawnIsolation` |

#### 12.10.1 验证

- ✅ `go test ./internal/chat ./internal/runtimehost ./internal/sessionruntime ./internal/supervision ./internal/toolbroker ./internal/memoryassembly ./internal/api/runtimeapi`
- ✅ `go test ./internal/config ./cmd/runtime-server ./cmd/aicli`
- ✅ `go test -count=1 ./internal/api/runtimeapi`（122.371s）
- ✅ `go test -count=1 ./cmd/aicli/commands`（224.509s）
- ✅ `TestLocalBackgroundJobRelay_ProjectsMonitorCheck -count=10` 通过；期间一次旧构建曾暴露 supervision SQLite migration 偶发错误，重跑后未复现。

#### 12.10.2 仍明确保留

1. **P1 完整高层 assembler**：`buildSessionAgentForContext` / `buildLocalChatAgent` 仍保留宿主薄包装；profile/prompt/tool-surface/hooks 仍是宿主回调。
2. **P3 fallback**：非 ReAct 的 static/route planning、planned-subagent orchestration、patch approval 仍走直连；ReAct 下的多消息、Team/Task context、`disable_tools` 已 actor 化。
3. **P2 剩余控制面**：Goal driver loop、AgentControl worktree/approval 完整生命周期、Background wake 策略差异仍属宿主边界；Memory maintenance ticker 已由 F53 单源。
4. **P5 共享 authority 的部署前提**：lease store 已独立且默认同域；若两宿主使用不同 `sessions.dir` 或显式 lease 路径，仍会形成不同 authority domain。

> 以上为第八轮时点。第九轮关闭了其中的 Goal driver 容忍/续跑生命周期、审批超时/中断/恢复投影、remote agent approve/deny 与 profile-scoped tool surface，详见 §12.11。

### 12.11 第九轮切片：Goal 与 Approval 生命周期收敛

| # | 修复 | 状态 | 关键文件 |
|---|---|---|---|
| F55 | Goal driver 容忍与续跑生命周期：`session_end.run_source` 区分 `driver:goal`；strike 只统计本驱动器续跑轮，外部成功轮重置；用户取消立即停止；`output_present` 区分成功文本与空转；pending interaction 先于 round/limit 检查；决策 ctx 与 run ctx 分离（`WithoutCancel`），避免提交后立刻取消；`ContinueAsync + goal.ContinuationMetadataKey` 使续跑提示隐藏且不落 user history；per-session decision 单飞；`goal.maxConsecutiveContinuations` 生效；terminal wrap-up 按 `goal_id|status|updated_at` 去重 | ✅ | `chat/actor.go`；`chat/commands.go`；`internal/goal/continuation.go`；`runtimeapi/session_goal_driver.go`；`runtimeapi/handler.go` |
| F56 | Round 上限语义修正：`AdmitGoalRound` 在达到上限时不再递增，`MaxRounds=N` 恰好允许 N 轮续跑；CLI 持久化失败恢复改为保存前值，避免越界回退 | ✅ | `internal/goal/continuation.go`；`cmd/aicli/commands/chat_goal_auto_continue.go` |
| F57 | Approval 超时/中断收敛：`RequestApproval` expiry 分支记录 `expired` outcome；`RetirePendingInteraction` 在 close/cancel 时关闭 approval/question、发布 resolution、记录 outcome；`ApprovalResolutionFromPayload` 保留 `run_terminated` / `run_terminal_no_resume` / `interrupted`；已过期请求不再生成 critical inbox/wake | ✅ | `chat/actor.go`；`chat/commands.go`；`internal/supervision/approval_projection.go` |
| F58 | Approval 状态投影单源：新增 `internal/agentstatus.ApplyRuntimeState`，server/TUI 共用 pending approval/question/tool 投影；server `snapshot` 在无 live actor 时回退 durable `LoadState`，重启后仍能看到 `waiting_approval` | ✅ | `internal/agentstatus/runtime_state.go`；`runtimeapi/session_runtime_support.go`；`cmd/aicli/commands/chat_actor_registry.go` |
| F59 | Approval 决策结果 fail-closed：HTTP `approve_tool`、`subagent_approve` controller、TUI local registry 统一返回 `allowed/resolved/resumed/resolution`；无 recorded outcome 时不再回显 `allowed=true`；关闭后的 session 拒绝审批 | ✅ | `runtimeapi/session_runtime_handlers.go`；`runtimeapi/session_runtime_support.go`；`cmd/aicli/commands/chat_actor_registry.go` |
| F60 | TUI remote `/agents approve|deny`：新增 `POST /sessions/{id}/agents/{agent_id}/approve`，远端通过同一 `ResolveApproval` 解析 session/path/task/batch 别名；TUI remote 不再落入“未接入” | ✅ | `runtimeapi/agent_control_approval_handler.go`；`runtimeapi/handler.go`；`cmd/aicli/commands/chat_runtime_server_control_commands.go` |
| F61 | P1 profile-scoped tool surface：actor `PrepareAgent` 改用完整 `resolveProfileRuntimeState`，profile 的 skill registry / embedding router / MCP adapter 进入真实工具面；`runtimehost.Deps.Cleanup` 在 actor stop 或构建失败时关闭 profile MCP manager | ✅ | `runtimeapi/session_agent_build.go`；`runtimeapi/handler_deps.go`；`internal/runtimehost/deps.go`；`internal/runtimehost/actor_factory.go` |
| F62 | Direct/actor 工具面与 loop 默认对齐：Web direct 使用与 actor 相同的 session MCP + goal tool wrapper；`MaxParallelToolCalls` 默认统一为共享值 4（仍由 `EnableParallelTools` 门控）；actor 首轮 `tryRouteSkill` 可由 `RunRouteOverride.DisableSkillRoute` 关闭，Web 只在 `enable_routing`/planner 模式请求时路由 | ✅ | `runtimeapi/handler.go`；`runtimeapi/agent_chat_loop.go`；`runtimeapi/agent_chat_actor.go`；`internal/chat/commands.go`；`internal/chat/actor.go` |

#### 12.11.1 验证

- ✅ `GOGC=10 GOMAXPROCS=1 go test -p 1 -vet=off -count=1 -timeout 900s ./internal/goal ./internal/chat ./internal/runtimehost ./internal/sessionruntime ./internal/supervision ./internal/agentstatus ./internal/memoryassembly ./internal/config ./cmd/runtime-server ./cmd/aicli ./internal/api/runtimeapi ./cmd/aicli/commands`
- ✅ `internal/api/runtimeapi` 全包 `145.247s`
- ✅ `cmd/aicli/commands` 全包 `257.176s`
- ✅ `gofmt -l` 无输出，`git diff --check` 无输出
- ⚠️ 一次整包运行时 `TestSessionAgentController_SpawnRollsBackReservationOnActorFailure` 偶发失败；该用例 `-count=10` 通过，随后整包重跑 `145.247s` 通过，未复现。

#### 12.11.2 仍保留的边界

1. **P3 非 ReAct 兼容路径**：`enable_react=false` 的 static/route planning、planned-subagent orchestration、patch approval 仍由 direct `Orchestrate` 执行；这是 server API 兼容面，TUI 无对应入口。ReAct 主路径、多消息、Team/Task context、`disable_tools` 已 actor 化。
2. **P1 高层包装**：profile-scoped registry/MCP/embedding 已进入 actor；`buildSessionAgentForContext` / `buildLocalChatAgent` 仍是宿主薄包装。**已知剩余**：actor profile 解析仍使用 `UsageScope{}`，租户/项目级 runtimeConfig resolver 未透传；server 未复用 aicli 的 session-frozen system prompt；TUI 项目 hooks 仍以进程 cwd 判定项目根。
3. **Goal 外层循环**：CLI 同步 for-loop 与 server `session_end` 事件驱动仍是两个 shell；策略/阈值/prompt/round/隐藏续跑 metadata 已共享。Server 仍没有 CLI 的 disarm/rearm 用户意图闸。
4. **Approval 恢复策略**：TUI/ACP 有 restored approval 投影与 yolo/expiry 自动处理；server 增加 durable 状态投影、expired 投影与 close/retire，但尚未实现等价的启动/切换会话自动 reconcile。
5. **P5 部署前提**：lease store 已独立且默认同域；若两宿主使用不同 `sessions.dir` 或显式 lease 路径，仍会形成不同 authority domain。

> 以上为第九轮时点。第十轮对提交后复审列出的 13 项逐项处理，结果见 §12.12。

### 12.12 第十轮切片：提交后复审缺口收敛

第十轮不新增“全量同源”结论，只关闭前一节列出的行为缺口，并把仍然成立的结构性边界单列。状态含义：

- ✅：行为已落地并有测试或静态证据；
- ◐：关键行为已加护栏，但宿主外壳仍是双实现或需要部署开关；
- ⚪：明确接受的产品/部署差异，不再作为待修缺口。

| # | 缺口 | 本轮处理 | 状态 | 关键证据 |
|---|---|---|---|---|
| G1 | 普通 session close/delete 不停 live actor、不 retire pending | 新增 `stopSessionForTerminalState`：先 retire approval/question，再 `StopAsync`；无 live actor 时回退 durable runtime row；close 与 batch delete 共用同一入口；清理 spawn isolation 后释放 supervision monitors/wakes | ✅ | `runtimeapi/session_lifecycle_close.go`；`runtimeapi/handler.go:3866,3915`；`runtimeapi/session_runtime_support.go:Close` |
| G2 | AgentControl Close 两端不对称、仅 durable pending 无 resolution | server controller Close 改走同一 terminal helper；TUI `localActorRegistry.Close` 新增 `chat_actor_close_lifecycle.go`，retire live actor、durable fallback、disarm monitors、resolve pending wakes 后再标记 closed | ✅ | `runtimeapi/session_runtime_support.go:Close`；`cmd/aicli/commands/chat_actor_close_lifecycle.go`；`cmd/aicli/commands/chat_actor_registry.go:Close` |
| G3 | server 每轮只刷新 goal guidance | 新增 `prepareSessionAgentForRun`，在 turn 边界刷新 frozen prompt、allowed/read-only roots、plan/permission mode、goal guidance 与迟到工具 allowlist；`OnRunPrepare` 不再要求 base session | ✅ | `runtimeapi/session_run_prepare.go`；`runtimeapi/handler_deps.go:OnRunPrepare` |
| G4 | actor profile 固定 `UsageScope{}`；switch/save-as 用轻量解析 | session 元数据持久化 tenant/project；`SessionContext` 派生 usage scope；actor 与 `resolveProfileRuntimeState` 共用完整 profile 解析；profile switch/save-as 改走完整解析并执行 cleanup | ✅ | `sessionmeta.UsageTenantID/UsageProjectID`；`runtimehost/session_context.go`；`runtimeapi/session_usage_scope.go`；`runtimeapi/profile_support.go:resolveProfileStateForSession`；`session_profile_switch.go`；`profiles_saveas_handlers.go` |
| G5 | server 未复用 session-frozen system prompt | `SessionContext.FrozenSystemPrompt` 从 `sessionmeta.SystemPromptFrozen` 派生；actor 构建优先复用；每轮首个 run 冻结当前 compose 结果；profile switch 仍清锚点 | ✅ | `session_agent_build.go`；`session_run_prepare.go`；`session_profile_switch.go:clearSessionFrozenPromptAnchor` |
| G6 | TUI project hooks/plugin/agentdef 以进程 cwd 为根 | 新增 root-aware 插件/hook/agentdef 发现：actor 构建、session-end hook、spawn hook、`/agents defs`、profile skill roots 使用 `WorkspaceAnchor` + session folder trust；旧无参函数保留为 cwd 包装 | ✅ | `cmd/aicli/commands/plugin_runtime.go`；`chat_actor_host.go`；`chat_actor_registry.go`；`chat_agent_defs.go`；`chat_profile.go` |
| G7 | Goal CLI 冷却窗内直接丢弃、busy 提交无重试；server 默认开关与 CLI 不同 | server 在 CLI 冷却窗内 arm 一次延迟接管；回调重查 CLI 在场后走正常 kick；续跑提交对 actor busy 做 5s 有界重试，窗口后由该 run 的 terminal 事件闭环；`DefaultRuntimeConfig` 将 `serverAutoContinuation` 缺省开启，部署仍可显式关闭 | ✅ | `runtimeapi/session_goal_driver.go:scheduleCLIRecheck/scheduleGoalDriverCLITakeover/goalDriverSubmitContinuationWithPrompt`；`internal/config/manager.go` |
| G8 | Approval restored reconcile 不完整；expired 仍是 500 | durable-only pending approval 在 snapshot/Resume 观察边界按 expiry 收敛并发布 `approval_resolved(expired)`；Resume 对 bypass mode 自动允许 restored approval；expired 决策返回结构化 `resolution=expired`，不再变成 HTTP/tool 500 | ✅ | `chat/pending_retire.go`；`runtimeapi/session_lifecycle_close.go`；`runtimeapi/session_runtime_support.go:snapshot/Resume/ResolveApproval`；`cmd/aicli/commands/chat_actor_registry.go:ResolveApproval` |
| G9 | TUI Resume 缺 cache lineage | 抽取 `runtimehost.StampResumeCacheLineage`，server controller 与 TUI `localActorRegistry.Resume` 共用；空历史/批子代理仍是 no-op | ✅ | `runtimehost/resume_lineage.go`；`runtimeapi/session_runtime_support.go`；`cmd/aicli/commands/chat_actor_registry.go:Resume` |
| G10 | remote `/agents answer` 缺位 | 新增 `POST /sessions/{id}/agents/{agent_id}/answer` 与 controller answer 入口；TUI remote `/agents answer` 不再落入未接入；HTTP 与 connector 共用 actor `AnswerQuestion` | ✅ | `runtimeapi/agent_control_answer_handler.go`；`runtimeapi/session_runtime_support.go:AnswerQuestion`；`cmd/aicli/commands/chat_runtime_server_control_commands.go` |
| G11 | Team owner fence 无条件 true；server shutdown 不停 team loops | 两端 `ownerAllowed` 改查 durable orchestrator lease（仍以 `RunWithWake` 的 acquire/renew 为权威 fence）；TUI 设置稳定 `OwnerID/OwnerInstance`；`CloseSessionHub` 在停 actor 前 `StopAllLoops` | ✅ | `runtimehost/team_owner.go`；`runtimeapi/team_lifecycle.go`；`cmd/aicli/commands/chat_team_lifecycle.go`；`runtimeapi/session_runtime_support.go:CloseSessionHub` |
| G12 | `enable_react=false` 规划/planned-subagent/patch 仍走 direct | 未把非 ReAct 语义强行塞进 actor；显式保留兼容分支 | ⚪ | `runtimeapi/agent_chat_request.go:agentChatUsesSessionActor` |
| G13 | 诊断文本过期、health 无入口映射 | 抽取 `ExecutionEntrypointSnapshot`，runtime status 与 `/health` 共用；注释改为“多消息/Team/Task 已 actor 化，仅非 ReAct 兼容分支直连” | ✅ | `runtimeapi/execution_entrypoints.go`；`runtimeapi/handler.go`；`cmd/runtime-server/main.go:runtimeInfoHandler` |

#### 12.12.1 本轮验证

- ✅ `go build ./...`
- ✅ `GOGC=10 GOMAXPROCS=1 go test -p 1 -vet=off -count=1 ./internal/chat ./internal/runtimehost ./internal/sessionmeta`
- ✅ `internal/api/runtimeapi` 全包（第十轮新增 close/expiry/run-prepare 测试后）
- ✅ `cmd/aicli/commands` 全包
- ✅ 新增测试：`chat.RetireDurablePendingInteraction`、`chat.ExpireDurableApproval`、`TeamOwnerAllowed`、HTTP close cleanup、restored approval sweep、run-prepare root/prompt/plan 对齐
- ⚠️ 12 包串行组合运行在本机并发 5 个 `aicli --web` 会话（约 1.1 GB）时触发 Windows paging file 不足，部分用例以 `The paging file is too small` panic；单包隔离运行均通过。该组合运行不作为代码失败证据，也不宣称组合全绿。

#### 12.12.2 仍保留的明确边界

1. **非 ReAct 兼容直连**：`enable_react=false` 的 static/route planning、planned-subagent orchestration、patch approval 继续走 direct `Orchestrate`；ReAct 主路径、多消息、Team/Task context、`disable_tools` 已 actor 化。这是兼容语义，不是同一路径的遗漏。
2. **宿主薄包装仍双实现**：`buildSessionAgentForContext` / `buildLocalChatAgent`、AgentControl 列表投影、Team lifecycle 的视图/清理方法没有合并为一个包；本轮已统一会改变状态的关键动作（close、approval、owner fence、shutdown），剩余差异属于宿主外壳。
3. **Goal 外层驱动 shell**：CLI 同步决策循环与 server `session_end` 事件驱动仍是两个 shell；策略、prompt、round、metadata、strike、延迟接管与缺省开关已共享/对齐。部署仍可用 `goal.serverAutoContinuation=false` 显式关闭服务端驱动，避免与 CLI 进程形成双驱动。
4. **TUI 通用 skill 目录发现**：插件/hook/agentdef 主路径已按会话 workspace 锚定；`skills_integration.resolveConfiguredSkillDirs` 的通用 config 目录链仍以进程 cwd 为启动锚点，需要会话级重构后再宣称完全同源。
5. **Deployment authority**：lease store 已独立且默认同域；不同 `sessions.dir` 或显式 lease 路径仍形成不同 authority domain。

---

## 附录 A：关键证据索引

| 主题 | 证据 |
|---|---|
| runtime-server Web 文本路由 | `backend/internal/api/runtimeapi/handler.go:1024` |
| Web 文本直连 loop | `handler.go:2298-2378`, `:2586`, `:2927` |
| Web 文本 active turn | `handler.go:2564` |
| actor command API | `handler.go:1206`; `session_runtime_handlers.go:948` |
| actor 工厂 | `session_runtime_support.go:4728-4737`; `runtimehost/actor_factory.go:16-122` |
| 共享 Agent/Loop 装配原语 | `runtimehost/assembly.go` |
| 共享 context/memory/knowledge options | `runtimehost/context_options.go` |
| 共享 SessionContext 抽取 | `runtimehost/session_context.go`；TUI 调用点 `chat_actor_executor_deps.go` |
| 共享 base/child 分类 | `runtimehost/session_context.go:IsBaseSession` |
| TUI actor 复用 | `cmd/aicli/commands/chat_actor_host.go:1874-1877` |
| TUI 旧 actor 路径 | `chat_actor_host.go:1879-1997` |
| TUI 自有 Hub | `chat_actor_host.go:1598-1600` |
| `Executor` 仅 scheduled 使用 | `cmd/scheduled/executor.go:156`; `internal/runtimehost/executor.go:19-29` |
| API Agent 装配 | `internal/api/runtimeapi/session_agent_build.go:24-233` |
| TUI Agent 装配 | `cmd/aicli/commands/chat_actor_host.go:2393-2718` |
| TUI Loop 配置 | `chat_actor_host.go:3234-3278` |
| API Loop 配置 | `session_agent_build.go:197-233`; `session_runtime_support.go:4846-4872` |
| TUI `ContextAccountingMode` | `chat_actor_host.go:3254-3258` |
| TUI `TurnBudgetTokens` | `chat_actor_host.go:3263-3265` |
| TUI `allowed_roots` / `read_only_roots` | `chat_actor_host.go:2448-2483`, `:2757-2768` |
| API `read_only_roots` | `handler.go:2331-2336` |
| API actor goal guidance | `handler_deps.go:73-82` |
| TUI goal guidance | `chat_actor_host.go:2493-2498`, `:2751-2755` |
| API base/child 判定 | `handler_deps.go:101-105` |
| TUI base/child 判定 | `chat_actor_host.go:1857-1861` |
| TUI `SessionContext` 派生 | `chat_actor_executor_deps.go:32-75` |
| API `SessionContext` 派生 | `runtimehost/session_context.go:52-94` |
| API child agentdef policy | `session_runtime_support.go:4774-4844` |
| TUI child agentdef policy | `chat_actor_host.go:2018-2108` |
| 工具策略 API | `handler.go:5773-5862` |
| 工具策略 TUI | `chat_actor_host.go:2990-3042` |
| 审批 actor 安装 | `chat/actor.go:6647-6662`; `:5618-5710` |
| 无权限引擎时的 loop 行为 | `agent/loop.go:3824-3836` |
| 事件落盘 API | `handler.go:4928-4968`, `:4971-4988` |
| 事件落盘 TUI | `chat_actor_host.go:1124-1142`, `:1157-1217` |
| 事件落盘配置说明 | `internal/config/manager.go:525-531` |
| Runtime store 默认策略 | `internal/sessionruntime/paths.go:200-210`; `backend/configs/runtime.yaml:169-181` |
| 身份默认 | `internal/sessionruntime/identity.go:11-56` |
| AgentController 接口 | `internal/toolbroker/types.go:1507-1523` |
| API AgentController | `session_runtime_support.go:180-...`（83 方法） |
| TUI AgentController | `chat_actor_registry.go:37-...`（104 方法） |
| API Wait | `session_runtime_support.go:2286-2365` |
| TUI Wait | `chat_actor_registry.go:3248-3321` |
| API Close | `session_runtime_support.go:3177-3228` |
| TUI Close | `chat_actor_registry.go:4122-4178` |
| Team lifecycle API | `team_lifecycle.go:15-215` |
| Team lifecycle TUI | `chat_team_lifecycle.go:16-724` |
| Team rollback 写序差异 | `docs/plan/multi-agent-execution-optimization-plan.md:1095` |
| Goal driver API | `session_goal_driver.go:1-671`, `:582-590` |
| Goal driver TUI | `chat_goal_driver.go:1-228`, `:40-57` |
| Goal 默认开关 | `chat_setup.go:207`; `config/manager.go:813-818` |
| Goal 统一计划现状 | `docs/plan/aicli-goal-driver-unification-plan-20261018.md:398, 432` |
| Background API | `background_supervision.go:1-243` |
| Background TUI | `chat_actor_background_supervision.go:1-265` |
| Memory API | `memory_runtime_wiring.go:137-220` |
| Memory TUI | `chat_memory_runtime.go:18-105` |
| TUI remote 无本地 host | `chat_runtime_server.go:1895-1913` |
| TUI remote `/compact` | `chat_runtime_server.go:638-718` |
| TUI remote `/backtrack` | `chat_runtime_server.go:738-796` |
| TUI remote `interrupt` | `chat_runtime_server.go:577-597` |
| TUI remote 状态轮询路径 | `chat_runtime_server.go:1512-1516` |
| server runtime state 路由 | `handler.go:1187` |
| TUI remote state 404 容忍 | `chat_runtime_server.go:1796-1801` |
| 租约重取缺口 | `runtimehost/actor_factory.go:75-80,129-145`；`chat_actor_executor_deps.go:173-174`；`chat_actor_host.go:2720-2724` |
| TUI host Close | `chat_actor_host.go:356-384,1601-1653` |
| runtime-server close 不含 hub StopAll | `cmd/runtime-server/main.go:1557-1629`；`handler_executor.go:82-92` |
| TUI `/plan` 本地 | `chat_plan_command.go:39-112` |
| TUI `/memory` 本地 | `chat_memory_command.go:24-35` |
| TUI `/schedule` 本地 | `chat_schedule_command.go:35-46` |
| TUI `/agents` 本地 | `chat_agent_transcript.go:483-1146`; `chat_agent_cleanup.go:95-99` |
| TUI `/supervision` 本地 | `chat_supervision.go:285-298`, `:365-368` |
| TUI `/bots` 显式远程降级 | `chat_bots_command.go:250-257` |
| server plan 端点 | `handler.go:1227-1255` |
| server memory 端点 | `handler.go:1276-1302` |
| server schedule 端点 | `handler.go:1135-1147` |
| server agents 端点 | `handler.go:1209-1217`, `:1309-1322` |
| server teams 端点 | `handler.go:1323-1362` |
| server supervision 端点 | `handler.go:1365-1369` |
| Web actor 核心路径 | `agent_chat_request.go`；`agent_chat_actor.go`；`agent_chat_actor_events.go` |
| actor per-run route/turn_id | `chat/commands.go`；`chat/actor.go:2218-2255`；`chat/actor_resume_episode.go:32-38` |
| actor submit 单源 | `session_runtime_support.go:submitPromptResult` |
| authority/store 诊断 | `internal/sessionruntime/authority.go`；`runtimeapi/session_authority.go`；`chat_session_authority.go` |
| Team supervisor 配置 | `internal/runtimehost/team_lifecycle.go` |
| remote supervision/memory | `chat_runtime_server_control_commands.go`；`supervision_handlers.go`（resolve/control） |
| 共享 A 通道准入/别名 | `internal/runtimehost/event_persist.go:PrepareSessionStoreEvent` |
| session hub 关闭 | `runtimeapi/session_runtime_support.go:CloseSessionHub`；`cmd/runtime-server/main.go:app.close` |
| P4 agentdef/cleanup/watchdog | `runtimeapi/agent_definition_handlers.go`；`agent_control_cleanup_handler.go`；`execution_supervisor_handler.go` |
| 独立共享 lease store | `sessionruntime/paths.go:SessionLeaseStorePath`；`runtimeapi/handler.go:openSessionLeaseStore`；`chat_actor_host.go:buildLocalChatLeaseStore` |
| lease loss watchdog | `chat/session_runtime_store.go:SessionLeaseHandle.Lost`；`chat/actor.go:MarkLeaseLost`；`runtimehost/actor_factory.go` |
| 共享 Agent service graph | `runtimehost/assembly.go:ApplyAgentServices` |
| 多消息重放 actor 化 | `chat/actor.go:SubmitPromptOption.ReplaceHistory`；`runtimeapi/agent_chat_actor.go`；`agent_chat_request.go` |
| Background 即时 wake | `runtimeapi/background_supervision.go:projectBackgroundJobTerminal/Monitor` |
| Memory maintenance 单源 | `internal/memoryassembly/maintenance.go:StartMaintenance` |
| Worktree close 产物保全 | `runtimeapi/session_runtime_support.go:cleanupAPISpawnIsolation`；`chat_actor_registry.go:cleanupLocalSpawnIsolation` |
| Goal driver run source / 容忍 / 单飞 | `chat/actor.go:runSourcePayloadKey/output_present`；`runtimeapi/session_goal_driver.go:goalDriverContinuationRun/beginDecision` |
| Goal round 语义 | `internal/goal/continuation.go:AdmitGoalRound`；`cmd/aicli/commands/chat_goal_auto_continue.go:admitGoalRoundForSession` |
| Approval 超时/中断/retire | `chat/actor.go:RequestApproval`；`chat/actor.go:handleRetirePendingInteraction`；`chat/commands.go:RetirePendingInteraction` |
| Approval 投影单源 | `internal/agentstatus/runtime_state.go`；`runtimeapi/session_runtime_support.go:snapshot` |
| remote agent approve | `runtimeapi/agent_control_approval_handler.go`；`cmd/aicli/commands/chat_runtime_server_control_commands.go` |
| profile-scoped actor tool surface | `runtimeapi/session_agent_build.go:resolveProfileStateForSessionContext/buildSessionAgentForContext`；`runtimehost/Deps.Cleanup` |
| direct/actor loop 默认 | `runtimeapi/agent_chat_loop.go`；`runtimeapi/agent_chat_actor.go:DisableSkillRoute` |
| healthz actor core | `handler.go:10928`; `cmd/runtime-server/main.go:2073` |
| SQLite 锁问题 | `docs/ANALYSIS-sqlite-lock-problem.md` |

---

## 附录 B：重复度抽样（辅助）

> 方法：对同名函数/配置块做文本 `difflib.SequenceMatcher`。仅用于识别“复制后漂移”的候选，不作为行为等价证明。

| 对比 | 相似度 | 说明 |
|---|---|---|
| AgentController 同名方法 25 个 | 12 个 ≥ 0.85；`Spawn` 0.50、`Wait` 0.85、`Close` 0.64、`Resume` 0.38 | 公共外壳高度复制，生命周期差异大 |
| Team lifecycle 同名方法 4 个 | `StopLoop` 0.95、`SyncLoops` 0.95、`ownerAllowed` 0.95、`desiredTeamIDs` 0.58 | 外壳复制，语义有差异 |
| `applyLocalChildAgentdefToolPolicy` vs `applyAPIChildAgentdefToolPolicy` | 0.61 | TUI 多 ProfileRoot/PathAnchor 逻辑 |
| `buildLocalChatLoopConfig` vs `buildSessionLoopConfig` | 0.45 | TUI 多 budget/accounting |
| Memory `memorystore.Config` 块 | 1.00 | 完全复制 |
| Memory `memoryruntime.Config` 块 | 0.996 | 仅 worker/workspace/source 差异 |
| Background `background.Config` 块 | 0.80 | 路径/EventHandler 差异 |

---

**结论重申**：

> 本轮后的准确表述是：**ReAct 主路径、Goal 策略/轮次、Approval 状态与超时/中断收敛、Remote AgentControl 审批、Memory maintenance、profile-scoped tool surface 已同源；非 ReAct 兼容直连、两层宿主薄包装、Goal 外层驱动 shell 与部署 authority 仍是明确边界。**
> 收敛顺序执行到：**P1 共享原语 + actor profile 装配已落地 → P2 控制面/审批/Goal 语义单源 → P3 Web ReAct 主路径已 actor 化 → P4 远程命令缺口基本关闭 → P5 lease authority 已独立**。
> 第十轮（§12.12）进一步关闭了 close/delete 生命周期、server 每轮刷新、profile usage scope、prompt freeze、restored approval/expiry、remote answer、team owner fence/shutdown 与 health 诊断缺口；非 ReAct 兼容直连与双宿主外壳仍按明确边界保留。
> P0 的契约/诊断护栏与 fail-closed 审批边界继续保持；不得把“非 ReAct 兼容路径”或“不同 sessions.dir 的部署”宣传为全量同源。