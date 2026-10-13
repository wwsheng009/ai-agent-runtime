# 抽取 `internal/runtimehost.Executor`：分析与重构方案

> 目标：把「会话执行核」（session hub + actor 工厂 + provider/工具/事件/会话存储的装配）从
> `runtimeapi.Handler` 中抽出来，独立成包 `internal/runtimehost`；让 `cmd/runtime-server`、
> `cmd/aicli`、`cmd/scheduled` **共用同一执行核心**。`cmd/scheduled` 借此启用 `embedded`
> 模式（[`embedded-execution-design.md`](./embedded-execution-design.md) §3 路径 B / §10 E3）。
>
> 定位：**独立重构批次**，不是功能批次。验收标准是「行为零变化 + 三宿主同源」，不是新能力。
>
> 结论：先落 **接口 + 适配器**（行为零变化），再把 actor 构建期对 `Handler` 字段的依赖
> 逐步收敛为显式 `runtimehost.Deps`，最后让三宿主共用同一 `Executor`。分 **5 个阶段**，
> 每阶段可独立编译、独立回滚。

---

## 1. 背景与动机

`embedded-execution-design.md` 给出两条内嵌执行路径：

| 路径 | 做法 | 现状 |
|---|---|---|
| **A** | daemon 按 runtime-server 的方式构造 `bootstrap.Manager` + `runtimeapi.Handler`，不启 HTTP | 机制接缝已就位，未落地 |
| **B** | 抽取 `internal/runtimehost.Executor`，三宿主共用 | **本方案**（E3） |

当前 `cmd/scheduled` 传入 `Executor: nil`（`cmd/scheduled/main.go`），因此 `embedded` 模式
**名存实亡**：`schedule.Runtime.tryEmbedded` 因 `Executor == nil` 恒返回 false，只剩
`delegate` + 持久 mailbox 两条通道（见 [`implementation-status.md`](./implementation-status.md) §5）。

路径 A 能最快让 daemon 跑起来，但代价是 **daemon 链接整个 API 层**，并且执行栈的所有权
仍锁在 `runtimeapi.Handler` 这个巨型对象里——daemon 要「当作库用」一个 HTTP handler，
依赖面与心智负担都很大。路径 B 从根上解决：

1. **执行核与 HTTP/API 解耦**：谁执行会话，与「谁提供 HTTP 路由」无关。
2. **消除重复实现**：`cmd/aicli` 的 `localChatRuntimeHost`（`chat_actor_host.go:159`）已经是
   执行核的**第二份实现**（自带 hub/actor 工厂/事件落盘）。三宿主共用后只保留一份。
3. **让 `embedded` 成为一等公民**：daemon 依赖一个窄接口，而不是整个 `runtimeapi`。
4. **收敛测试**：执行核的单测不再需要拉起 HTTP handler。

> 与本方案正交：`schedule.SessionExecutor` / `schedule.LeaseArbiter` 接缝（`internal/schedule/executor.go`）
> 已经定义好了 daemon 侧的消费面；本方案只负责**提供**一个能接上它的执行核。

---

## 2. 现状核验（真实符号）

### 2.1 执行栈当前由 `runtimeapi.Handler` 拥有

| 位置 | 事实 |
|---|---|
| `internal/api/runtimeapi/session_runtime_support.go:4487` | `Handler.getSessionHub()` 惰性创建 `chat.NewBoundedSessionHub(factory)`，**hub 的所有权在 Handler 上** |
| `internal/api/runtimeapi/session_runtime_support.go:4509` | `Handler.buildSessionActor(sessionID)` 是 actor 工厂：解析 profile/workspace/路由 → 建 `agent.Config` → `newAPIAgentWithRuntime` → 施加工具策略 → 取 runtime/event store → 取 session 租约 → 组装 `chat.SessionActorConfig` → `chat.NewSessionActor` |
| `internal/api/runtimeapi/session_runtime_support.go:5061 / :5093` | `getSessionRuntimeStore()` / `getSessionEventStore()`：执行期状态与事件落盘的装配点 |
| `internal/api/runtimeapi/handler.go:111` | `Handler` 结构体（200+ 字段）：**执行核字段**与 **HTTP/API 专属字段混在一起** |
| `internal/api/runtimeapi/handler.go:4590` | `newAPIAgentWithRuntime(cfg, components)`：按 registry / embeddingRouter / mcpManager / llmRuntime 组装 agent |
| `internal/api/runtimeapi/handler.go:5533` | `applyAgentExecutionPolicy(...)`：profile/workspace 决定工具面 |
| `internal/api/runtimeapi/profile_support.go:198` | `resolveProfileSessionState(...)`：会话 profile 解析 |
| `internal/chat` | `SessionHub` / `SessionActor` / `SessionActorConfig` / `NewBoundedSessionHub` / `NewSessionActor` |

### 2.2 两处宿主的装配（对比）

**runtime-server**（`cmd/runtime-server/main.go`）：

```go
bootstrapManager, _ := runtimebootstrap.NewManager(&Options{Config: runtimeConfig, SkillDir: ...})
_ = bootstrapManager.Validate()
handler := runtimeapi.NewHandler(bootstrapManager.Registry(), bootstrapManager.Loader(), mcpAdapter)
handler.SetWorkspaceMCPSupport(...)
bootstrapManager.ApplyToSkillsHandler(handler)   // → SetLLMRuntime / SetSessionManager / SetHotReload / SetEmbeddingRouter
handler.SetRuntimeConfig(runtimeConfig, path)
handler.SetProfileSupport(...)
// …约 40 个 Set*（鉴权/用量账本/监督面/配置文档/知识层/MCP 管理…）
runtimeRouter := handler.RegisterRoutes(router)  // ← HTTP 专属
```

**aicli**（`cmd/aicli/commands/chat_actor_host.go`）：

```go
type localChatRuntimeHost struct {          // :159
    Bootstrap *runtimebootstrap.Manager
    RuntimeConfig *runtimecfg.RuntimeConfig
    SessionHub *runtimechat.SessionHub       // :1507 NewBoundedSessionHub(host.buildSessionActor)
    RuntimeStore / EventStore / ReceiptStore
    SessionStore runtimechat.SessionStorage
    Schedule *schedule.Service               // :1404 newLocalScheduleService(...)
    // …team / agentcontrol / supervision / background…
}
actor, _ := runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{ // :1843
    Agent: apiAgent, LLMRuntime: h.Bootstrap.LLMRuntime(), SessionStore: sessionStore,
    StateStore: h.RuntimeStore, EventStore: h.EventStore, ...
})
```

**含义**：两宿主各有一份 actor 工厂，语义近似但参数与装配顺序不同（`buildSessionActor(sessionID)`
vs `buildSessionActor(sessionID, session, sessionStore, runtimeConfig, workspaceRoot)`）。
这就是「执行核」重复的证据。

### 2.3 `Handler` 字段的可分性

把 `Handler` 的字段按归属分三类（用于第 5 阶段迁移）：

| 分类 | 代表字段 | 归属 |
|---|---|---|
| **执行核** | `sessionManager`、`llmRuntime`、`sessionHub`、`skillRegistry`、`skillLoader`、`mcpManager`、`embeddingRouter`、`hotReload`、`runtimeConfig`、`runtimeConfigResolver`、session/event/runtime store、工具策略与 profile 解析、knowledge shadow/recorder、supervision 义务解析 | → `runtimehost.Executor` |
| **HTTP/API 专属** | `adminToken`、`authPolicyPersister`、`usageLedgerStore`、`searchTelemetry`、`mutationPolicy`、`scopeResolverConfig`、`configDocumentService`、`siteAccountService`、`fileTransferService`、路由/中间件 | 留在 `runtimeapi.Handler` |
| **跨界** | `runtimeEventBus`、`observeService`、`usageTracker`、`activeTurns` | 执行核**使用**、API 层**暴露**；以接口/回调注入，两边都持有 |

> 关键观察：**跨界字段是本次重构的主要难点**，不是 `buildSessionActor` 本身。
> `buildSessionActor` 的依赖几乎都能通过 `Deps` 显式传入；难的是它运行期回调到
> Handler 的少量方法（如 `applyAPISessionToolObservation`、`agentSessionObligationResolver`）。

### 2.4 已有接缝（不必新造）

- `internal/schedule/executor.go:12` `SessionExecutor{ ExecuteTurn(ctx, sessionID, prompt) error }`
  ——daemon 侧的执行消费面，已定义。
- `internal/schedule/executor.go:22` `LeaseArbiter{ AcquireSession(ctx, sessionID) (release, ok) }`
  ——会话排他仲裁，已定义；`cmd/scheduled/arbiter_*.go` 已实现，但用的是
  `internal/mesh` 文件租约（`purpose=session`）——**与宿主的执行租约不是同一把锁**（见 §2.5）。
- `internal/api/runtimeapi/connector_host.go:70` `SubmitConnectorPrompt` 与
  `internal/api/runtimeapi/schedule_host.go`（本批次刚加）的进程内 deliverer，是
  「宿主内投递一条 prompt 进会话」的既有范式——`Executor` 应把它吸收为通用能力。

### 2.5 两个阻塞缺陷（核验发现，E3.3 前必须修）——**已修复（本批次）**

#### (a) 会话执行租约域不一致 ——「绝不抢占」当前不成立

| 角色 | 使用的锁 | 位置 |
|---|---|---|
| daemon 仲裁 | `internal/mesh` **文件**租约，`purpose=session`，`key=session-<sid>` | `cmd/scheduled/arbiter_windows.go` / `arbiter_other.go` |
| runtime-server 执行 | `chat.SessionLeaseStore`（`session_runtime.sqlite`，**DB** 租约） | `internal/chat/session_runtime_store.go:59`；`session_runtime_support.go:4742` |
| aicli 执行 | 同上（`chat.SessionLeaseStore`） | `cmd/aicli/commands/chat_actor_host.go:2080` |

两者是**独立的锁域**（文件 vs 数据库），且 `cmd/runtime-server` **完全不使用 mesh**
（`cmd/runtime-server` 下 `mesh.` 无匹配）。因此 daemon 的 `LeaseArbiter` 查不到宿主是否
正在跑该会话的 turn——一旦 E3.3 注入 Executor，daemon 会在宿主执行同一会话时
**误判为空闲**并并发执行。

> 结论：`runtimehost.Deps` 必须暴露**宿主的** `chat.SessionLeaseStore`（共享 runtime state
> DB），daemon 的 arbiter 改为使用同一把锁；`internal/mesh` 的 session 租约不应再用于执行互斥
> （它属于 mesh 控制面：presence / bindings / stop 能力）。

**修复（已落地）**：删除 `cmd/scheduled/arbiter_windows.go` / `arbiter_other.go`（mesh 版），
新增 `cmd/scheduled/arbiter.go`——`daemonSessionArbiter` 改为 `chat.SessionLeaseStore`；
新增 `cmd/scheduled/sessionlease.go`：从 `sessionRuntime.storePath/storeDSN` 打开共享存储，
**仅在 `mode` 允许内嵌时打开**；未配置/打不开 → **fail-closed**（store=nil，永不内嵌）。
测试：`cmd/scheduled/arbiter_test.go`（被占用时拒绝、释放后可再取、nil store fail-closed）。

#### (b) `schedule.execution.mode` 未接线

- `schedule.RuntimeOptions`（`internal/schedule/runtime.go`）**没有 Mode 字段**；
  `Runtime.tryEmbedded` 只判断 `Executor == nil || Arbiter == nil`，**不读 mode**。
- `cmd/scheduled/main.go` 恒传 `Executor: nil`；`mode` 只被 status 端点回显
  （`statusServer.mode`），不参与任何决策。
- 含义：`delegate | embedded | auto` 目前**语义相同**（都等于 delegate → 失败落 mailbox）。
  E3.3 必须把 mode 接进 `RuntimeOptions`（或由调用方按 mode 决定是否注入 Executor/Arbiter），
  否则「默认 delegate、embedded 可选」的设计意图无法保证。

**修复（已落地）**：`schedule.RuntimeOptions` 新增 `Mode`；新增
`schedule.ModeAllowsEmbedded(mode)`（`delegate`/未知值 → false，fail-closed）；
`Runtime.tryEmbedded` 先过 mode 门控（delegate 下即使注入 Executor/Arbiter 也不内嵌）；
`cmd/scheduled/main.go` 传入 `cfg.Execution.Mode`。
测试：`internal/schedule/embedded_test.go` 的 `TestTryEmbeddedGatedByDelegateMode`。

---

## 3. 问题陈述（耦合点清单）

按「必须解掉 / 可以后置」分级：

### 3.1 必须解掉（否则抽不干净）

1. **hub 所有权在 Handler**：`getSessionHub()` 与 `sessionHub` 字段私有；`cmd/scheduled`
   无法只拿执行核不拿 API。
2. **actor 工厂读 200+ Handler 字段**：`buildSessionActor` 直接读 `h.sessionManager`、
   `h.llmRuntime`、`h.knowledgeShadow`、`h.runtimeConfig`、`h.getSessionRuntimeStore()` 等；
   不显式化依赖就无法移到别的包。
3. **会话租约取用散落在 Handler**：`acquireSessionLease` / `acquireSessionLeaseNoWait`
   （`session_runtime_support.go`）是执行核的关键互斥，但实现挂在 Handler。
4. **aicli 第二份实现**：`localChatRuntimeHost.buildSessionActor` 与 API 版语义漂移
   （参数不同、事件落盘路径不同），需要合并为一份。

### 3.2 可以后置（接口先留缝）

5. **事件落盘缓冲**（`runtimeEventPersistBuffer`、`CloseRuntimeEventPersistence`）：先随执行核
   搬走，API 层通过接口读。
6. **supervision 义务解析**（`agentSessionObligationResolver` / `teamObligationResolver`）：
   执行核调用、supervision 控制面提供；以回调注入，暂不搬 supervision。
7. **main agent 路由 / completion requirement / 知识层 observation hook**：
   都是「执行期策略」，随 actor 工厂一起搬，但依赖通过 `Deps` 传入。
8. **`activeTurns` 注册表**：与 HTTP 断连续传强相关，第一阶段留在 API 层。

### 3.3 明确不做（本批次范围外）

- 不改 `runtimeapi` 的 HTTP 契约、路由、鉴权。
- 不搬 `supervision` / `team` / `agentcontrol` 控制面本身。
- 不引入新配置项（`schedule.execution.mode` 等已存在）。
- 不改变 `delegate` 默认行为。
- 不重写 `chat.SessionActor`（它已经是干净的下层）。

### 3.4 必须解掉（否则 embedded 不安全，非抽取本身）——**已完成（本批次）**

9. **会话租约域不一致**（§2.5a）：daemon 用 `internal/mesh` 文件租约，宿主用
   `chat.SessionLeaseStore`（DB）——两把锁。**已修**：arbiter 改用 `chat.SessionLeaseStore`
   （`cmd/scheduled/arbiter.go` + `sessionlease.go`，fail-closed）。
10. **`mode` 未接线**（§2.5b）：`RuntimeOptions` 无 Mode、`tryEmbedded` 不读配置。
    **已修**：新增 `Mode` + `ModeAllowsEmbedded`，`tryEmbedded` 门控，daemon 传入配置值。

---

## 4. 目标架构

### 4.1 包与层次

```
                 cmd/runtime-server        cmd/aicli            cmd/scheduled
                        │                     │                      │
                        ▼                     ▼                      ▼
                 runtimeapi.Handler      (aicli 本地宿主)       schedule.Runtime
                        │                     │                      │
                        └──────────┬──────────┴──────────────────────┘
                                   ▼
                        internal/runtimehost.Executor      ← 唯一执行核
                                   │
              ┌────────────┬───────┴────────┬──────────────┬─────────────┐
              ▼            ▼                ▼              ▼             ▼
        chat.SessionHub  bootstrap.Manager  internal/mesh  internal/events  agent/toolbroker
        + SessionActor   (LLMRuntime/       (session lease) (事件持久化)    (工具面/策略)
                          SessionManager/
                          Registry/Loader)
```

- **`runtimehost` 不 import `runtimeapi`**（依赖单向：API → runtimehost）。
- **`runtimehost` 不 import HTTP 相关包**（`net/http`、`webui`、`gateway`、路由）。
- `runtimeapi.Handler` 从「hub 拥有者」降级为「`Executor` 的消费者 + HTTP 外壳」。

### 4.2 职责边界

| 归属 `runtimehost.Executor` | 归属 `runtimeapi.Handler`（或各宿主） |
|---|---|
| 会话 hub 生命周期（建/驱逐/StopAll） | HTTP 路由、鉴权、CORS、限流 |
| actor 工厂（profile/workspace/路由/策略） | 用量账本、审计、mutation policy 持久化 |
| 会话租约（取/续/放） | 配置文档、站点账户、文件传输、MCP 管理 UI |
| prompt 提交（同步 / 异步）与 turn 终态 | 搜索、知识层**管理**面（执行期 hook 除外） |
| 事件落盘与缓冲 | supervision 控制面（执行期回调除外） |
| 执行期策略钩子（工具面/权限/审批降级） | 前端/网关/console |

---

## 5. 分阶段方案

> 每阶段结束都要满足：`go build ./...` + `go vet` + 相关包测试绿 + **行为零变化**
> （E3.0–E3.2 尤其如此）。每阶段单独可回滚。

### E3.0 —— 接口 + 适配器（行为零变化）——**已完成（本批次）**

**目标**：定义 `runtimehost.Executor` 接口，由 `runtimeapi.Handler` 适配实现；
没有任何调用方切换。

**已交付**：

- `internal/runtimehost/executor.go`：`Executor` 接口 + `TurnRequest` / `TurnResult`
  （`TurnResult` 含 `Completed`/`State`，如实表达"已接受未完成"中间态）。
  `Deps` 留到 E3.1（依赖收敛阶段）再引入。
- `internal/api/runtimeapi/handler_executor.go`：`Handler.Executor()` 访问器 + 无状态
  `handlerExecutor` 适配器——内部仍走 `getSessionHub` / `buildSessionActor` /
  `submitSessionPrompt`，与 HTTP 路径**同源**；`Close` 停止 hub（E3.0 无调用方）。
- `TurnRequest.Profile` 在 E3.0 **刻意忽略**（工具面收窄属 E3.1+）。
- `cmd/scheduled` **仍不注入**（`Executor: nil`），行为不变。
- 测试：`internal/api/runtimeapi/handler_executor_test.go`（编译期接口断言 + 契约 +
  未配置 fail-closed + 空 session 拒绝）。

**未改**：Handler 结构体（适配器无状态，未新增字段）、任何 HTTP 契约、任何既有执行路径。

**回滚点**：删掉新包与适配器文件，零影响。

### E3.1 —— 把 actor 构建依赖收敛为 `runtimehost.Deps`

**目标**：让 actor 工厂可脱离 `*Handler` 编译。

分两步走（先抽纯逻辑，再动 `Deps` 与工厂主体），每步都可独立验收/回滚。

#### E3.1a —— 抽取会话上下文派生（纯逻辑）——**已完成（本批次）**

- `internal/runtimehost/session_context.go`：`SessionContext` + `ExtractSessionContext(session)`
  ——把「读会话 metadata → workspace / profile / 路由 / 子代理标记 / 模型覆盖 / 执行偏好」
  这段与 Handler 无关的逻辑收敛为**纯函数**（只依赖 `chat` / `sessionmeta` / `toolbroker` /
  `agentconfig`，不读进程级状态）。
- `buildSessionActor` 改为调用它（`sessionCtx := runtimehost.ExtractSessionContext(session)`），
  其余变量由该结构派生；profile 解析（`h.resolveProfileSessionState`）仍在 Handler，
  输入改为 `sessionCtx.*`——**行为零变化**（语义逐条对齐，含 worktree 覆盖、model 回落、
  Bool/Int 缺失不覆盖）。
- 测试：`internal/runtimehost/session_context_test.go`（nil / 全字段 / worktree 覆盖 /
  model 回落 / 缺失标志保持零值）。
- 验证：`go build ./...` ✅、`go vet` ✅、**`go test ./internal/api/runtimeapi/...` 全量 ✅**
  （40s，证明重构无行为回归）。

#### E3.1b-1 —— 定义 `Deps` 并让工厂经它取依赖——**已完成（本批次）**

- `internal/runtimehost/deps.go`：`Deps`（基础设施子集：`SessionContext` / `SessionStorage`
  / `RuntimeStore` / `EventStore` / `EventBus` / `Logf`）——**全部公开类型**，
  不含 `*runtimeapi.Handler` 或任何 HTTP 类型（编译期不反向依赖）。
- `internal/api/runtimeapi/handler_deps.go`：`Handler.SessionDeps(sessionID)` 组装 `Deps`
  （带截止时间加载会话 + `ExtractSessionContext` + 既有存储/事件 getter）。
- `buildSessionActor` 改为 `deps := h.SessionDeps(sessionID)`，并用
  `deps.SessionContext` / `deps.SessionStorage` / `deps.RuntimeStore` / `deps.EventStore`
  / `deps.EventBus`——**行为零变化**（含 lazy store 初始化语义不变）。
- 测试：`handler_deps_test.go`（存储/事件/上下文装配、未知会话零上下文、nil handler 零值）。
- 验证：`go build ./...` ✅、`go vet` ✅、**`go test ./internal/api/runtimeapi/...` 全量 ✅**（40.8s）。

#### E3.1b-2a —— 收敛会话租约与 LLM runtime——**已完成（本批次）**

- `Deps` 增加 `LLMRuntime`、`AcquireSessionLeaseNoWait`、`AcquireSessionLease`
  （均为公开类型）。
- `Handler.SessionDeps` 绑定 owner kind/scope（`sessionActorLeaseOwnerKind` + sessionID）。
- `buildSessionActor` 改为 `deps.AcquireSessionLeaseNoWait(...)` / `deps.AcquireSessionLease(...)`
  / `LLMRuntime: deps.LLMRuntime`——**行为零变化**。
- 验证：`go build ./...` ✅、`go vet` ✅、**`go test ./internal/api/runtimeapi/...` 全量 ✅**（42s）。

#### E3.1b-2b-i —— 抽出 agent 装配命名接缝——**已完成（本批次）**

- 新增 `internal/api/runtimeapi/session_agent_build.go`：`Handler.buildSessionAgentForContext`
  ——把 `buildSessionActor` 中「profile 解析结果 → `agent.Config` → `apiAgent` → 工具策略
  （disable_tools / 子代理 def / 只读 / 深度）→ hooks/runtime services」约 110 行装配抽成
  **命名方法**（方法体逐条照搬，**行为零变化**）。
- `buildSessionActor` 改为
  `apiAgent, selectedConfig := h.buildSessionAgentForContext(sessionID, sessionCtx, profileState, runtimeConfig)`；
  随之删除无用局部变量与 `llm` import。
- 验证：`go build ./...` ✅、`go vet` ✅、**`go test ./internal/api/runtimeapi/...` 全量 ✅**（38.7s）。

#### E3.1b-2b-ii —— 回调接缝（agent + loop）——**已完成（本批次）**

**已交付**：

- `Deps` 增加 `PrepareAgent` 与 `BuildLoopConfig` 两个回调（公开类型；profile 状态经
  **不透明 handle `any`** 在两次回调间传递，**不二次解析**）。
- `Handler.SessionDeps` 提供两个回调：`PrepareAgent` 内部解析 profile + 调
  `buildSessionAgentForContext`；`BuildLoopConfig` 复用 handle + 调 `buildSessionLoopConfigFor`。
- 新增 `Handler.resolveProfileStateForSessionContext`、`Handler.buildSessionLoopConfigFor`
  （loop 接线命名方法，体逐条照搬）。
- `buildSessionActor` 改为 `deps.PrepareAgent(...)` → 取租约 → `deps.BuildLoopConfig(...)`，
  **保持原「agent → 租约 → loop」顺序**；删除全部 profile/sessionCtx 派生局部变量。
- 验证：`go build ./...` ✅、`go vet` ✅、**`go test ./internal/api/runtimeapi/...` 全量 ✅**（44.8s）。

**E3.1b-2b-iii —— 工厂搬迁——已完成（本批次）**：

- `Deps` 补齐最后触点：`OnProgress` / `OnBudgetProgress` / `ApprovalTerminalGuard` /
  `TriggerTurnDrain` / `TriggerTurnRunMeta` / `SuspendedTurnAbandon` / `PersistHook`。
- 新增 `internal/runtimehost/actor_factory.go`：`BuildSessionActor(sessionID, deps)` ——
  工厂主体（租约获取/作用域/释放 + `chat.NewSessionActor` 装配）**整体移入 runtimehost**，
  不再引用任何 Handler 符号。
- `Handler.buildSessionActor` 退化为
  `return runtimehost.BuildSessionActor(sessionID, h.SessionDeps(sessionID))`（约 110 行 → 3 行）。
- 测试：`internal/runtimehost/actor_factory_test.go` —— **用纯内存 fake Deps 在 runtimehost 内
  单测工厂**（不依赖 runtimeapi），并覆盖缺依赖 fail-closed。
- 验证：`go build ./...` ✅、`go vet` ✅、**`go test ./internal/api/runtimeapi/...` 全量 ✅**（39.7s）、
  `go test ./internal/runtimehost/...` ✅。

---

#### 附：为什么不能直接"搬迁 profile 子系统"（设计修正记录）

> **重要修正**：原计划"导出 `profileRuntimeState` 再搬迁"**不可行**——
> `profileRuntimeState` 是 profile 子系统的中枢类型，被 `profile_support.go`、
> `handler.go`、`instruction_messages.go`、`session_runtime_support.go`、
> `session_profile_switch.go`、`profiles_saveas_handlers.go`、`prompt_layout_debug.go`、
> `usage_ledger_group.go` 等约 10 个文件共享（`resolveAgentProvider/Model`、
> `profileStateToolPolicy`、`buildProfileContextPack`、`buildRuntimeInstructionMessages`、
> `applyAPISessionCompletionRequirement`、`ledgerProfileName` …）。把它搬进 `runtimehost`
> 等价于搬迁整个 profile 子系统，超出"依赖收敛"的范围与风险预算。

**修正后的做法**：`Deps` 用**公开类型的回调**封装 profile/agent 相关逻辑，profile 状态
保持 runtimeapi 私有；`runtimehost` 只做编排，不感知 profile 内部结构。

> **实现约束（核验后采用方案 (a)）**：`profileState` **同时**被 agent 装配与 loop 接线
> （`applyAPISessionCompletionRequirement` / main-agent routing / knowledge observation）使用。
> 只加单个 `BuildAgent` 回调会导致 profile **二次解析**或丢失状态。本批采用 **(a)**：
> `PrepareAgent` 返回不透明 `profileHandle any`，由 `BuildLoopConfig` 回传复用；
> **执行顺序保持**「agent → 租约 → loop」不变（未采用 (b) 的合并/重排，避免语义变化）。

- `PrepareAgent func(ctx, sessionID string, sessionCtx SessionContext) (*agent.Agent, *runtimecfg.RuntimeConfig, any)`
  ——内部完成 profile 解析 + `agent.Config` 装配 + 工具策略（含子代理深度/只读/路由）。
- `BuildLoopConfig func(sessionID string, sessionCtx SessionContext, selectedConfig *runtimecfg.RuntimeConfig, profileHandle any) *agent.LoopReActConfig`
  ——内部完成义务解析器 / main-agent routing / completion requirement / knowledge observation。
- ✅ 已完成（E3.1b-2b-iii）：工厂主体已移入 `internal/runtimehost/actor_factory.go`；
  `Handler.buildSessionActor` 已退化为转发。

**验收（E3.1b-2b-iii）——✅ 达成**：`runtimehost` 包内可单测 actor 工厂（内存 session store +
fake `PrepareAgent`/`BuildLoopConfig` 回调，见 `actor_factory_test.go`），不依赖 `runtimeapi`；
profile 子系统仍留在 runtimeapi（未搬迁，符合设计修正）。

**回滚点**：每步独立；工厂函数搬回 Handler。

### E3.2 —— `runtimeapi.Handler` 改为消费 `Executor`

**目标**：Handler 不再自己持有 hub；所有会话执行路径经 `Executor`。

- `Handler.getSessionHub()` 改为 `h.executor.Hub()`（或删除，改调 `Executor` 方法）。
- `SubmitSessionRuntimeCommand` 的 `submit_prompt`/`continue` 路径改为经 `Executor`。
- `connector_host.go` / `schedule_host.go` 的进程内投递改为经 `Executor.SubmitAsync`。
- 事件落盘缓冲随执行核搬到 `runtimehost`，Handler 保留读接口。

**验收**：runtimeapi 全量测试绿；`/sessions/{id}/runtime/commands` 行为与重构前一致
（契约测试固定请求/响应）。

**回滚点**：Handler 重新自建 hub（旧代码仍在 git 历史）。

### E3.3 —— `cmd/scheduled` 注入 `Executor`，启用 `embedded`

**目标**：兑现 `embedded-execution-design.md` 的 E0/E1。

**前置（阻塞项）——已完成（本批次）**：

- **统一会话租约域**（§2.5a）：daemon 的 `LeaseArbiter` 改为使用宿主的
  `chat.SessionLeaseStore`（共享 `session_runtime.sqlite`），废弃 `internal/mesh` 的
  session 执行仲裁。验收：宿主正跑 turn 时，daemon 的 `AcquireSession` 返回 `ok=false`。
  ✅ 已落地（`cmd/scheduled/arbiter.go` / `sessionlease.go`）。
- **接通 `mode`**（§2.5b）：给 `schedule.RuntimeOptions` 增加 `Mode`，`tryEmbedded`
  按 `embedded`/`auto` 决定是否尝试内嵌；`delegate` 模式即使注入 Executor 也不内嵌。
  ✅ 已落地（`ModeAllowsEmbedded` + `tryEmbedded` 门控 + daemon 传配置值）。

**主体——已完成（本批次，路径 A）**：

- `cmd/scheduled/executor.go`：`scheduleExecutorAdapter` 实现 `schedule.SessionExecutor`
  （`ExecuteTurn` → `runtimehost.SubmitTurn`；`RunMeta.PermissionMode=dont_ask`（fail-closed
  无人值守）；携带 `execution.profile`）。
- `buildHeadlessExecutor`：`bootstrap.Manager` + `runtimeapi.NewHandler`（**不启 HTTP**）+
  `ApplyToSkillsHandler` + `SetRuntimeConfig` → `Handler.Executor()`。
- `cmd/scheduled/main.go`：仅 `embedded`/`auto` 构建执行核并注入 `RuntimeOptions.Executor`；
  任一步失败 → **fail-closed**（不内嵌，退回 delegate/mailbox）。`delegate` 零装配、零行为变化。
- `schedule.Runtime.tryEmbedded` 走通：先经 `LeaseArbiter` 取 session 租约（已与宿主同源），
  拿到才执行；执行完释放；`commit` 推进任务。
- 测试：`cmd/scheduled/executor_test.go`（映射一次 turn + `dont_ask` + profile；nil 执行核 fail-closed）。

> **路径说明**：本批走**路径 A**（daemon 复用 `runtimeapi.Handler` 作库）。虽然 E3.1 已把工厂
> 搬进 `runtimehost`，但 `PrepareAgent`/`BuildLoopConfig` 回调仍在 runtimeapi（profile/agent
> 装配未搬迁，见 E3.1b-2b 修正），故 daemon 仍须链接 API 层。代价 = 二进制体积；收益 =
> embedded 立即可用。彻底消除该依赖需后续把 profile 子系统也抽出（独立批次）。

**验收**（对应 `embedded-execution-design.md` §10 E0/E1）：
- ✅ daemon 对**空闲**会话执行一次提醒 turn、推进任务、不落 pending ——
  `cmd/scheduled/embedded_integration_test.go::TestEmbeddedExecutionEndToEnd`
  （真实 `schedule.Runtime` + `daemonSessionArbiter` + `scheduleExecutorAdapter`）。
- ✅ 会话被宿主以**同一把** chat 租约持有时，daemon 不抢占、退回 mailbox ——
  同文件 `TestEmbeddedSkippedWhenSessionHeld`（钉住 §2.5a 的锁域统一）。
- ✅ `mode=delegate` 默认行为不变（`doctor` 冒烟 + 单测确认零装配）。
- ✅ **全栈冒烟**：`cmd/scheduled/executor_integration_test.go::TestBuildHeadlessExecutorAssemblesAndRuns`
  ——最小 runtime 配置（落盘路径指向临时目录）下 `buildHeadlessExecutor` 真实装配出
  `bootstrap.Manager` + `Handler` + 执行核；适配器调用真实执行核对未知会话返回
  `[SESSION_NOT_FOUND]`（不 panic、不静默成功）。

> **冒烟发现（需知悉）**：`Handler` 懒加载的 `session_runtime.sqlite` 在进程内**没有关闭路径**
> （`buildHeadlessExecutor` 只 `manager.Stop()`）。长驻 daemon 场景可接受（随进程退出释放），
> 但意味着 daemon 持有的 store 生命周期不受显式管理。若后续要支持优雅重启/多实例切换，
> 需给执行核补 `Close()`（关闭 runtime/event store）。

**回滚点**：配置回 `mode=delegate` / `Executor: nil`。

### E3.4 —— aicli 复用（收敛第二份实现）——**前置已完成，采纳待做**

> **详细设计见 [`aicli-executor-reuse-design.md`](./aicli-executor-reuse-design.md)**
> （差异核验 / `Deps` 映射 / E3.4a–c 分阶段 / 契约测试 / 开放问题 O1–O3）。
>
> **进展**：**E3.4-0 已完成**——`runtimehost.Deps.LeaseMode`（`runScoped` 零值 / `actorScoped`）
> 落地，`actorScoped` 抑制构造后与 `OnRunFinished` 两处释放（`runtimeapi`/daemon 零值 → 行为不变，
> 全量回归 ✅）。**E3.4a–c 待做**，且受 `cmd/aicli/commands` 在途 WIP 影响（见设计 §12.5）。

**目标**：`localChatRuntimeHost` 的 actor 工厂改为使用 `runtimehost.BuildSessionActor`。

**核验发现（本批次）**：aicli 的 `localChatRuntimeHost.buildSessionActor`
（`cmd/aicli/commands/chat_actor_host.go:1755`）与 runtimeapi 版**输入与钩子面存在实质差异**：

| 差异 | aicli | runtimeapi |
|---|---|---|
| 会话上下文来源 | `sessionStore.Load` 的 **runtime session**（`GetContext` / `agentcontrol.ContextString`） | `chat.Session.Metadata.Context`（`sessionmeta`） |
| agent 装配 | `buildLocalChatAgent(...)` | `buildSessionAgentForContext(...)` |
| loop 接线 | `buildLocalChatLoopConfig` / `applyLocalChat*` | `buildSessionLoopConfigFor` / `applyAPISession*` |
| 租约 | `acquireLocalChatSessionLease`（单次） | NoWait + 阻塞两态 |
| 额外不变量 | `ensureSessionDurableBeforeActor` / `ensureSessionRowLoadable`（基础会话持久化） | — |
| 额外 config | `PrepareRun`（本地 hook）/ `EnsureSession` / `CheckpointInterval` / `RunStallTimeout` | 无（默认 ensureLease） |

**结论**：不能简单"删掉 aicli 的工厂"——必须把 aicli 的差异**显式化为 `Deps`**，且其
`PrepareRun` 会替换默认的"取租约"语义（aicli 自行保证租约）。因此 E3.4 是**独立的、
触及 TUI 核心的高风险批次**，需单独设计（会话上下文来源统一 / 租约生命周期对齐）。

**前置（已完成，本批次）**：`runtimehost.Deps` 补齐 aicli 所需的 config 面——
`PrepareRun`（默认取租约，可覆盖）、`EnsureSession`、`CheckpointInterval`、`RunStallTimeout`。
runtimeapi / daemon 均传零值 → **行为零变化**（`go test ./internal/api/runtimeapi/...` 全量 ✅）。
测试：`internal/runtimehost/actor_factory_test.go::TestBuildSessionActorHonorsHostOverrides`。

**待做（E3.4 采纳）**：
- aicli 侧构造 aicli 专属 `Deps`（含本地 `PrepareRun`/`EnsureSession` 与租约生命周期），
  调用 `runtimehost.BuildSessionActor`；`ensureSessionDurableBeforeActor` 等前置不变量在调用前执行。
- 用共享契约测试钉住两宿主 actor 配置的关键不变量（租约 run-scoped、RecoverStale、无人值守 dont_ask）。

**验收**：aicli 本地会话执行行为不变；两份 actor 工厂归一。
**回滚点**：aicli 恢复自带工厂。

---

## 6. 接口草案

> 以下为**方向性**草案，最终以 E3.0 实现为准；字段名/方法名在评审时定稿。
> 包名沿用代码库既有别名（`runtimebootstrap`=internal/bootstrap、`runtimechat`=internal/chat、
> `runtimecfg`=internal/config 的 runtime 配置、`runtimeevents`=internal/events 等）。

### 6.1 `Executor`

```go
package runtimehost

// Executor 是唯一的会话执行核心：拥有 hub、actor 工厂、会话租约与事件落盘。
// 它不感知 HTTP；调用方（runtimeapi / aicli / scheduled）负责传输与控制面。
type Executor interface {
    // SubmitTurn 同步执行一次 turn，返回终态；受 ctx / maxTurnDuration 约束。
    SubmitTurn(ctx context.Context, req TurnRequest) (TurnResult, error)
    // SubmitAsync 提交后立即返回（"被 actor 接受"语义），用于投递回执。
    SubmitAsync(ctx context.Context, req TurnRequest) error
    // Hub 暴露底层 hub 供既有 API 读取（过渡期；E3.2 后应收敛为方法）。
    Hub() *chat.SessionHub
    // Close 停止 hub、flush 事件缓冲、释放执行核资源。
    Close(ctx context.Context) error
}

type TurnRequest struct {
    SessionID string
    Prompt    string
    RunMeta   *team.RunMeta      // source / permission / profile 等
    Images    []string
    Profile   string             // 可选：限定工具面（scheduled 用）
}

type TurnResult struct {
    SessionID string
    Output    string
    Success   bool
    Error     string
    State     *chat.RuntimeState // 可空
}
```

### 6.2 `Deps`（显式依赖，替代 Handler 字段）

```go
type Deps struct {
    // 上层组件
    Bootstrap     *bootstrap.Manager        // LLMRuntime / Registry / Loader / SessionManager / TeamStore
    RuntimeConfig *runtimecfg.RuntimeConfig
    ConfigResolver func(UsageScope) *runtimecfg.RuntimeConfig

    // 存储
    SessionStore chat.SessionStorage
    RuntimeStore chat.RuntimeStateStore
    EventStore   chat.EventStore
    ReceiptStore chat.ToolReceiptStore
    TeamStore    team.Store

    // 会话租约（执行核互斥）
    LeaseStore SessionLeaseStore // 宿主的 chat.SessionLeaseStore（DB 租约；**非** internal/mesh，见 §2.5a）

    // 执行期策略与钩子（跨界字段，以回调注入）
    EventBus           *runtimeevents.Bus
    ObservationHook    func(sessionID string, subagent, readOnly bool, taskID string) // 知识层
    SessionObligations func() subagentbatch.AgentSessionObligationResolver
    TeamObligations    func() subagentbatch.TeamObligationResolver
    MainAgentRouting   func(sessionRoutingOverride, workspace string) MainAgentRouting
    ApplyToolPolicy    func(agent *agent.Agent, workspace string, cfg *runtimecfg.RuntimeConfig, profilePolicy *runtimepolicy.ToolExecutionPolicy)
    // ResolveProfile 目前返回 Handler 内未导出的 profileRuntimeState；迁移时需导出/重命名。
    ResolveProfile func(profileRef, agentID, workspace string) (*ProfileState, error)
    Logger         func(format string, args ...any)
}
```

**规则**：`Deps` 只允许出现**下层包类型**（chat/bootstrap/mesh/events/agent/toolbroker）。
不允许出现 `*runtimeapi.Handler` 或任何 HTTP 类型——这是「不反向依赖」的编译期保证。

### 6.3 与 `schedule` 的对接

```go
// cmd/scheduled 侧适配器（放 cmd/scheduled，不污染 runtimehost）
type scheduleExecutorAdapter struct{ exec runtimehost.Executor }

func (a scheduleExecutorAdapter) ExecuteTurn(ctx context.Context, sessionID, prompt string) error {
    _, err := a.exec.SubmitTurn(ctx, runtimehost.TurnRequest{
        SessionID: sessionID, Prompt: prompt,
        RunMeta: &team.RunMeta{Source: "schedule", PermissionMode: "dont_ask"},
        Profile: "scheduled",
    })
    return err
}
```

`schedule.LeaseArbiter`（`cmd/scheduled/arbiter_*.go`）与 `runtimehost.Deps.LeaseStore`
必须**同源**——但**不是** `internal/mesh`，而是宿主的 `chat.SessionLeaseStore`
（§2.5a：当前实现用 mesh，是错误的锁域）。建议 `runtimehost` 暴露
`SessionLeaseStore()`（或让 `Deps` 直接持有宿主的 store），daemon 的 arbiter 复用它；
`internal/mesh` 的 session 租约保留给 mesh 控制面（presence/bindings/stop），不用于执行互斥。

---

## 7. 风险与缓解

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| 1 | **行为漂移**：搬动 actor 工厂改变执行语义 | 高 | E3.0/E3.1 只搬不改；用契约测试（固定输入→固定 actor 配置/工具面）钉住 |
| 2 | **双执行者竞态**：daemon embedded 与交互宿主并发跑同一会话 | 高 | 双重互斥：调度租约（daemon 单实例）+ session 租约；**前置**：session 租约必须与宿主**同域**（`chat.SessionLeaseStore`，见 §2.5a）——否则缓解无效。集成测试覆盖「用户在线时 daemon 不抢」 |
| 3 | **反向依赖**：`runtimehost` 需要 Handler 的能力，被迫 import `runtimeapi` | 高 | 编译期约束：`Deps` 不出现 API 类型；CI 加 import 边界检查 |
| 4 | **跨界字段泄漏**：`runtimeEventBus`/`observeService` 双向使用 | 中 | 以接口注入；执行核持有**只写/只读**视图，不持有 API 对象 |
| 5 | **二进制膨胀**（daemon 链接 API 层） | 中 | 路径 B 正是为了消除它；E3.3 后 daemon 不再依赖 `runtimeapi` |
| 6 | **无人值守审批挂起** | 中 | `scheduled` profile 预置拒绝/降级；`dont_ask`；超时中断 |
| 7 | **事件落盘顺序/缓冲语义变化** | 中 | 缓冲随执行核整体搬迁，不拆分；保留 flush-on-close |
| 8 | **aicli 语义差异**（本地 workspace/凭据） | 中 | E3.4 用 `Deps` 显式表达差异；共享契约测试比对两宿主 |
| 9 | **迁移期双份 hub** | 低 | E3.0–E3.2 期间只允许一份活跃 hub；适配器只转发，不自建 |

---

## 8. 测试与验收

### 8.1 分层测试

| 层 | 内容 |
|---|---|
| **契约测试** | `handlerExecutor` 满足 `runtimehost.Executor`；固定 `TurnRequest` → 固定 actor 配置/工具面/回执 |
| **执行核单测** | `runtimehost` 内用内存 session/runtime/event store + fake provider，跑 actor 工厂与一次 turn |
| **租约仲裁** | 空闲会话 → 授予并执行；被持有 → 拒绝并落 mailbox（复用 `internal/schedule/embedded_test.go` 范式） |
| **API 回归** | `SubmitSessionRuntimeCommand`（submit_prompt/continue）、connector、schedule drain 的既有测试全绿 |
| **跨宿主同源** | 同一 `TurnRequest` 在 runtime-server 与 aicli 两条路径产出等价的 actor 配置（工具面/权限/profile） |
| **集成（daemon）** | 起 daemon（embedded）+ 一个冷会话 → 到点执行一次 turn；用户在线时只排队 |

### 8.2 每阶段验收门槛

- `go build ./...`、`go vet ./...`（相关包）、`gofmt -l` 干净。
- 相关包测试绿；E3.2 起 **runtimeapi 全量**测试绿。
- 行为零变化的阶段（E3.0–E3.2）不得修改任何 HTTP 响应契约。

---

## 9. 与既有文档的关系（需同步修订）

| 文档 | 修订点 |
|---|---|
| [`embedded-execution-design.md`](./embedded-execution-design.md) §3 | 路径 A/B 的取舍 → 明确「B 已进入独立批次，A 不再作为过渡落地」 |
| 同上 §10 E3 | 展开为本方案的 E3.0–E3.4 |
| 同上 §11 风险 1/6 | 二进制膨胀与「两路径行为同源」→ 由本方案消除/收敛 |
| [`implementation-status.md`](./implementation-status.md) §5 | 「P5 余项（E0 完整执行栈）」→ 指向本方案 |
| [`scheduler-daemon-design.md`](./scheduler-daemon-design.md) §3.1 | 补 `doctor` 已实现（本批次） |
| 同上 §2 三平面 | 执行面「可选 delegate/embedded」的落地依赖本方案 |

---

## 10. 开放问题

1. **`UsageScope` / 多配置解析**：daemon 无请求上下文，`runtimeConfigResolver` 的 scope 如何定？
   倾向：daemon 用**全局配置**，不参与 per-scope rollout。
2. **凭据与服务账户**：daemon 以服务账户运行，provider 凭据/会话库的读权限需在部署文档明确
   （对应 `embedded-execution-design.md` §7）。
3. **`activeTurns` 归属**：与 HTTP 断连续传绑定，暂留 API 层；若 daemon 也需要「在途回合」
   视图，再决定是否下沉。
4. **`Executor.Hub()` 是否长期保留**：E3.2 后 API 层若只剩少量读取，可改为窄方法，最终删掉
   `Hub()` 暴露。
5. **配置热重载**：runtime-server 有 `RuntimeConfigHotReloader`；daemon 是否需要热重载，
   还是重启生效？倾向后者（服务化场景）。
