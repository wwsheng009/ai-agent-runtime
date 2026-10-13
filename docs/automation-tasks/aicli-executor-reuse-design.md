# aicli 复用执行核（E3.4）详细设计

> 目标：把 `cmd/aicli` 的 `localChatRuntimeHost.buildSessionActor` 改为复用
> `internal/runtimehost.BuildSessionActor`，收敛「第二份 actor 工厂」，让 Web 与 TUI
> 的执行语义同源。
>
> 定位：[`runtimehost-executor-extraction-plan.md`](./runtimehost-executor-extraction-plan.md) 的 **E3.4**。
> E3.0/E3.1/E3.3 已完成（执行核已在 `internal/runtimehost`，daemon 已注入）。
>
> 结论：**可行，但必须"显式化差异"而非"删代码"**——aicli 与 runtimeapi 的 actor 工厂在
> **输入来源、agent 装配、loop 接线、租约生命周期、前置不变量**五处存在实质差异。
> 本设计给出差异映射、Deps 缺口、分阶段实施与验收；**实施前需先确认 2 个开放问题**（见 §7）。

---

## 1. 背景

`runtimehost-executor-extraction-plan.md` §E3.4 原计划「删除重复的 `buildSessionActor`」。
核验后该表述过于乐观：aicli 的工厂不是 runtimeapi 工厂的"复制"，而是**同构但不同参**的两套装配。

现状（两处 actor 工厂）：

| 位置 | 函数 |
|---|---|
| `internal/api/runtimeapi/session_runtime_support.go:4509` | `Handler.buildSessionActor(sessionID)` → 已退化为 `runtimehost.BuildSessionActor(sessionID, h.SessionDeps(sessionID))` |
| `cmd/aicli/commands/chat_actor_host.go:1755` | `localChatRuntimeHost.buildSessionActor(sessionID, session, sessionStore, runtimeConfig, workspaceRoot)` |

---

## 2. 差异核验（真实符号）

| # | 维度 | aicli | runtimeapi | 影响 |
|---|---|---|---|---|
| D1 | **会话上下文来源** | `sessionStore.Load` 的 **runtime session**（`GetContext` / `agentcontrol.ContextString`） | `chat.Session.Metadata.Context`（`sessionmeta`，经 `ExtractSessionContext`） | `SessionContext` 的**填充方式不同** |
| D2 | **agent 装配** | `buildLocalChatAgent(session, h, runtimeConfig, workspaceRoot, childAgentType, requestedModel, requestedProvider, requestedReasoningEffort)` | `Handler.buildSessionAgentForContext(...)` | 两者都必须保留（宿主专属） |
| D3 | **工具策略** | `applyLocalChildAgentdefToolPolicy` / `applyLocalChildReadOnlyPolicy` / `applyLocalChildDepthPolicy` | `applyAPIChildAgentdefToolPolicy` / 只读/深度（在 `buildSessionAgentForContext` 内） | 宿主专属，随 D2 走 |
| D4 | **loop 接线** | `buildLocalChatLoopConfig` / `applyLocalChatMainAgentRouting` / `applyLocalChatToolObservation` / `applyLocalChatCompletionRequirement` | `buildSessionLoopConfigFor` / `applyAPISession*` | 宿主专属 |
| D5 | **会话租约** | `acquireLocalChatSessionLease(ctx, h.RuntimeStore, sessionID)`（**单次**获取；`OnRunStalled`/`OnStop` 释放） | `AcquireSessionLeaseNoWait`（构造期）+ `AcquireSessionLease`（run 期，`PrepareRun` 取、`OnRunFinished`/`OnRunStalled`/`OnStop` 放） | **生命周期语义不同**（见 §7 开放问题 O1） |
| D6 | **前置不变量** | `ensureSessionDurableBeforeActor(session)` + `ensureSessionRowLoadable(ctx, store, sessionID, session)`（仅基础会话） | 无 | 必须在调用前执行 |
| D7 | **额外 config** | `EnsureSession` / `CheckpointInterval` / `RunStallTimeout` / `PrepareRun`（本地 hook）/ `TriggerTurnRunMeta`（`toolbroker.SpawnAgentRunMetaFromContext`） | 无（默认 `PrepareRun`=取租约） | `Deps` 需覆盖 |
| D8 | **额外身份判断** | `isBaseSession`（`baseSessionID` 比较）影响 workspace 覆盖与策略 | 无 | aicli 侧在填充 `SessionContext` 前处理 |

> `Deps` 现状（E3.1b-2a/b-iii 已建）：`SessionContext` / `SessionStorage` / `RuntimeStore` /
> `EventStore` / `LLMRuntime` / `AcquireSessionLeaseNoWait` / `AcquireSessionLease` /
> `PrepareAgent` / `BuildLoopConfig` / `OnProgress` / `OnBudgetProgress` /
> `ApprovalTerminalGuard` / `TriggerTurnDrain` / `TriggerTurnRunMeta` / `SuspendedTurnAbandon` /
> `PersistHook` / `PrepareRun` / `EnsureSession` / `CheckpointInterval` / `RunStallTimeout` /
> `EventBus` / `Logf`。

---

## 3. 目标形态

```
localChatRuntimeHost.buildSessionActor(sessionID, session, sessionStore, runtimeConfig, workspaceRoot)
  ├─ 1. 前置不变量（D6）：ensureSessionDurableBeforeActor / ensureSessionRowLoadable
  ├─ 2. 由 runtime session 填充 runtimehost.SessionContext（D1/D8，宿主专属）
  └─ 3. runtimehost.BuildSessionActor(sessionID, aicliDeps(session, ...))
         └─ 内部：PrepareAgent → 租约 → BuildLoopConfig → chat.NewSessionActor（统一装配）
```

**原则**：`runtimehost` 只做**编排**（租约作用域、`SessionActorConfig` 装配、错误路径释放），
所有宿主差异经 `Deps` 注入；`runtimehost` **不感知** aicli 的 session/workspace/profile 语义。

---

## 4. 差异映射设计

| 差异 | 在 `Deps` 中的表达 |
|---|---|
| D1/D8 会话上下文 | aicli **不走** `ExtractSessionContext`；自行由 runtime session 派生 `SessionContext` 各字段（含 `isBaseSession` 的 workspace 覆盖） |
| D2/D3 agent 装配 | `Deps.PrepareAgent` = aicli 闭包：解析 profile → `buildLocalChatAgent` → 本地工具策略 → 返回 `(*agent.Agent, *runtimecfg.RuntimeConfig, any)` |
| D4 loop 接线 | `Deps.BuildLoopConfig` = aicli 闭包：`buildLocalChatLoopConfig` + `applyLocalChat*`（复用 `PrepareAgent` 的 handle） |
| D5 租约 | `Deps.AcquireSessionLeaseNoWait` / `AcquireSessionLease` = aicli 的 `acquireLocalChatSessionLease`（两者都映射到同一实现）；**租约模式必须可配置**——aicli 是 actor 生命周期持锁（O1），而 `runtimehost` 在**构造后**（`actor_factory.go:117`）与 **`OnRunFinished`**（`:103`）两处释放 → 需 `Deps.LeaseMode`（E3.4-0） |
| D6 前置不变量 | 在调用 `BuildSessionActor` **之前**执行（不进 Deps） |
| D7 EnsureSession | `Deps.EnsureSession` = `localChatActorEnsureSession(session, isBaseSession)` |
| D7 Checkpoint/RunStall | `Deps.CheckpointInterval` / `Deps.RunStallTimeout` = aicli 的 env 派生值 |
| D7 PrepareRun | `Deps.PrepareRun` = `localChatPrepareRunHook(...)`（**注意**：当前 `BuildSessionActor` 的默认 `PrepareRun` 是"取租约"；注入后 aicli 必须自行保证租约，见 O1） |
| D7 TriggerTurnRunMeta | `Deps.TriggerTurnRunMeta` = `toolbroker.SpawnAgentRunMetaFromContext` |
| 其余 | `OnProgress`/`OnBudgetProgress`/`ApprovalTerminalGuard`/`TriggerTurnDrain`/`SuspendedTurnAbandon`/`PersistHook`/`EventBus`/`LLMRuntime`/存储 均按 aicli 现值传入 |

> `runtimehost.BuildSessionActor` 已有 `Deps.PrepareRun` 覆盖点（nil → 默认取租约），
> 因此 D7 的 `PrepareRun` 差异**无需再改 runtimehost**。

---

## 5. 分阶段实施

| 阶段 | 交付 | 验收 | 回滚 |
|---|---|---|---|
| **E3.4-0** ✅**已完成** | `runtimehost` 增加**租约模式**：`Deps.LeaseMode`（`LeaseModeRunScoped`（零值/现状）\| `LeaseModeActorScoped`）。`actorScoped` **同时**抑制两处释放——① 构造后释放（`actor_factory.go`）② `OnRunFinished` 释放，只保留 stalled/stop（`ReleasesAfterConstruction` / `ReleasesOnRunFinished`）。**仅加"RunFinished 开关"不够**（§11 G1） | ✅ 验证：`TestLeaseModePolicy`（策略）、`TestActorScopedKeepsLeaseAfterConstruction`（**G1 证明**：actor-scoped 构造后仍持锁）、`TestRunScopedReleasesLeaseAfterConstruction`（零值行为不变）；`runtimeapi` 全量回归 ✅（44.9s） | 删字段 |
| **E3.4a-1** ✅**已完成** | 把 aicli 的「runtime session → `SessionContext`」派生抽成命名函数 `deriveLocalSessionContext(sessionStore, sessionID, isBaseSession, workspaceRoot) (SessionContext, string)`（`chat_actor_executor_deps.go`）；`buildSessionActor` 改为调用它（引用改 `sessionCtx.*`）。**行为零变化** | ✅ 验证：`cmd/aicli/commands` **全量回归** ✅（197.7s）；`TestDeriveLocalSessionContext`（子会话读全量 / 基础会话只读 agent_type·completion / 存储缺失零值） | 还原内联块 |
| **E3.4a-2** | aicli 侧新增 `aicliRuntimeDeps(baseSession, sessionStore, runtimeConfig, workspaceRoot, sessionID) runtimehost.Deps`——**按 sessionID 派生**（hub 工厂以基础会话闭包 + 变化的 sessionID 调用，见 §11 G2）；**只装配、不切换**；补契约测试断言各回调非空 | 编译 + 单测；行为零变化（未切换调用点） | 删函数 |
| **E3.4b** | `buildSessionActor` 改为：前置不变量 → 构造 Deps → `runtimehost.BuildSessionActor` | aicli 全量测试绿；**关键**：用契约测试钉住 actor 配置（见 §6） | 恢复旧工厂（git） |
| **E3.4c** | 删除 aicli 侧不再使用的装配代码；文档记录 | 无死代码；行为不变 | git |

**建议**：E3.4b 与 E3.4c 之间**观察一个发布周期**（TUI 回归面大），确认无行为差异再删旧码。

---

## 6. 测试策略

| 层 | 内容 |
|---|---|
| **契约测试（核心）** | 同一 `(session, sessionStore, runtimeConfig, workspaceRoot)` 下，比较"新路径（`runtimehost.BuildSessionActor`）"与"旧路径（保留的旧实现，测试内)"产出的 `chat.SessionActorConfig` **关键不变量**：`Agent`（provider/model/tool policy 摘要）、`LoopConfig`（obligations/routing/completion）、`StateStore`/`EventStore`、`RecoverStale`、`EnsureSession` 存在性、`CheckpointInterval`/`RunStallTimeout` 值 |
| **租约不变量** | 构造期取租约 → 构造后释放；run 期取、run 末/stalled/stop 释放（按 O1 结论钉住） |
| **前置不变量** | `ensureSessionDurableBeforeActor` 失败时**不构造 actor**（错误原样返回） |
| **回归** | `cmd/aicli/commands` 全量测试；TUI 冒烟（chat 起会话、跑一轮、interrupt、resume） |
| **共享** | 与 runtimeapi 侧同一组不变量断言（避免"两套测试各测各的"） |

---

## 7. 开放问题（O1 已定论；O2/O3 实施前确认）

**O1 — aicli 的会话租约生命周期（✅ 已核验：actor 生命周期持锁）**

证据（`cmd/aicli/commands/chat_actor_host.go`）：
- `:1827` 构造期 `acquireLocalChatSessionLease(ctx, h.RuntimeStore, sessionID)`；
- 仅在 `OnRunStalled`(`:1895`) / `OnStop`(`:1900`) 释放，构造失败时 `:1906` 释放；**没有 `OnRunFinished`**；
- `localChatPrepareRunHook`(`:2570`) **不触碰租约**（仅做 run 前的 workspace/agent 准备；
  `!isBaseSession` 时返回 `nil`）。

⇒ aicli 的租约是 **actor 生命周期**，与 runtimeapi 的 **run-scoped** 语义不同。
`runtimehost.BuildSessionActor` 默认在 `OnRunFinished` 释放 → **直接切换会让 aicli 在首个 run 结束后丢锁**。

**结论**：先做 **E3.4-0**（`Deps.ReleaseLeaseOnRunFinished`，aicli 传 `false`），再进 E3.4b。

**O2 — `isBaseSession` 的 workspace 覆盖时机**
aicli 在读取 runtime session 时按 `!isBaseSession` 决定是否用 worktree/workspace 覆盖 `workspaceRoot`。
该逻辑必须在**填充 `SessionContext` 之前**完成，否则 `PrepareAgent` 拿到的 workspace 会错。
→ 实现时把该覆盖放进"填充 SessionContext"步骤，并加单测。

**O3 — `TriggerTurnRunMeta` 的 session 参数**
`Deps.TriggerTurnRunMeta` 签名 `func(ctx, *chat.Session) *team.RunMeta`；aicli 用
`toolbroker.SpawnAgentRunMetaFromContext(session)`，runtimeapi 用 `h.apiSessionRunMeta(ctx, sessionID)`。
→ 确认 `session` 参数在 drain 路径上是"子会话"还是"基础会话"，保持 aicli 现有语义。

---

## 8. 风险与缓解

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| 1 | **TUI 回归**（交互会话、interrupt、resume、子代理） | 高 | 分阶段 + 契约测试 + 观察一个发布周期后再删旧码 |
| 2 | **租约语义漂移**（O1） | 高 | 先确认；必要时给 `runtimehost` 加释放策略回调 |
| 3 | **workspace 覆盖顺序**（O2） | 中 | 单测钉住 `isBaseSession` 分支 |
| 4 | **agent 装配差异被抹平** | 高 | `PrepareAgent` 保留 aicli 全部本地逻辑（不共用 runtimeapi 的） |
| 5 | 事件落盘/持久化钩子差异 | 中 | `PersistHook`/`EventBus` 显式传入并断言 |
| 6 | 测试只覆盖新路径 | 中 | §6 的"新旧路径对比"契约测试 |

---

## 9. 验收标准

0. **E3.4-0**：`runtimehost.Deps.ReleaseLeaseOnRunFinished` 落地，runtimeapi/daemon 零值 → 行为不变，
   runtimehost 单测覆盖两种释放策略；
1. `localChatRuntimeHost.buildSessionActor` 调用 `runtimehost.BuildSessionActor`；
2. §6 契约测试全绿（新旧路径关键不变量一致）；
3. `cmd/aicli/commands` 全量测试绿；TUI 冒烟（起会话/一轮/interrupt/resume）通过；
4. 行为零变化（无用户可见差异）；
5. 文档更新：E3.4 标完成，记录 O1–O3 结论。

---

## 10. 与既有文档的关系

| 文档 | 关系 |
|---|---|
| [`runtimehost-executor-extraction-plan.md`](./runtimehost-executor-extraction-plan.md) §E3.4 | 本设计是其展开（差异核验 + 映射 + 阶段） |
| [`embedded-execution-design.md`](./embedded-execution-design.md) | 执行核来源；E3.4 与 daemon embedded 无直接耦合 |
| [`automation-job-generality-design.md`](./automation-job-generality-design.md) | 无耦合（作业通用化走 schedule 线） |

---

## 11. 完整性审查（本批补充，含对原设计的修正）

| # | 级别 | 问题（已用代码核验） | 结论 / 修正 |
|---|---|---|---|
| **G1** | **阻断** | E3.4-0 原设计只加"`OnRunFinished` 释放开关"，但 `runtimehost.BuildSessionActor` **还**在构造后**无条件**释放（`internal/runtimehost/actor_factory.go:117`）。aicli 的 actor 生命周期持锁会被**立即破坏**（首个 run 前就丢锁） | ✅ **已修复（E3.4-0 已落地）**：改为 `Deps.LeaseMode{LeaseModeRunScoped(零值/现状), LeaseModeActorScoped}`；`actorScoped` **同时**抑制构造后释放与 `OnRunFinished` 释放，仅保留 stalled/stop。测试 `TestActorScopedKeepsLeaseAfterConstruction` 钉住 |
| **G2** | **重要** | aicli 的 hub 工厂是 `func(sessionID){ buildSessionActor(sessionID, session /*基础会话闭包*/, sessionStore, runtimeConfig, workspaceRoot) }`（`cmd/aicli/commands/chat_actor_host.go:1515`）——`sessionID` 会**变**（子会话），而 `session` 是**基础会话** | `Deps` 必须**按 sessionID 派生**：`aicliRuntimeDeps(baseSession, sessionStore, runtimeConfig, workspaceRoot, sessionID)`；`isBaseSession` 判定与 workspace 覆盖在该派生内完成（呼应 O2） |
| **G3** | 重要 | 抽取方案 §E3.4 原措辞"hub/actor 工厂改为使用 `runtimehost.Executor`"**不准确**（已在抽取方案中修正为 `BuildSessionActor`） | E3.4 **只复用 actor 工厂**（`runtimehost.BuildSessionActor`）；aicli **不采用** `Executor`（它自有 hub 与提交路径）。§3 已澄清 |
| **G4** | 中 | `SessionContext.RoutingOverrideRaw` 是 runtimeapi 专属（`agentconfig.SessionRoutingOverrideContextKey`）；aicli 的主 Agent 路由读 `session`（`applyLocalChatMainAgentRouting(loopConfig, session, isBaseSession)`） | aicli 派生时该字段留空；路由逻辑在 `BuildLoopConfig` 内用 `session` |
| **G5** | 中 | §6 的"新旧路径对比测试"在 E3.4c 删除旧实现后**无法运行** | 对比测试必须**内嵌冻结的期望值**（不引用旧函数），E3.4c 后仍可运行 |
| **G6** | 低 | 无灰度开关，回滚仅靠 git | 建议 E3.4b 加 env 开关（如 `AICLI_EXECUTOR_REUSE=1`）以便灰度；默认走旧路径 |
| **G7** | 低 | 原设计未列影响面 | 见 §12 |

**审查结论**：原设计**不完备**——**G1 为阻断级**（原 E3.4-0 不足以支撑切换），**G2 为结构级**（Deps 派生维度写错）。
两处已在 §4/§5 修正。**G1/G2 未解决前不得进入 E3.4b。**

---

## 12. 架构变更影响面

### 12.1 代码面

| 模块 | 影响 | 性质 |
|---|---|---|
| `internal/runtimehost`（`actor_factory.go` / `deps.go`） | 新增 `LeaseMode`；释放逻辑分支 | 增量（零值不变） |
| `cmd/aicli/commands/chat_actor_host.go` | 工厂改写 + 新增 `aicliRuntimeDeps` | **行为敏感（TUI 核心）** |
| `internal/api/runtimeapi`（`handler_deps.go`） | 显式传 `LeaseMode: runScoped`（或留零值） | 无行为变化 |
| `cmd/scheduled`（`executor.go`） | 经 `Handler.Executor()` 间接使用 `BuildSessionActor` | 无行为变化（零值） |
| `internal/chat` | **不改** | — |

### 12.2 行为面（TUI）

| 行为 | 风险 |
|---|---|
| 会话租约持有期（actor 生命周期） | **高**：改错 → 丢锁 / 双执行者 |
| interrupt / resume / 挂起 turn 放弃 | 高：`SuspendedTurnAbandon` / `OnRunStalled` 语义 |
| 子会话（`spawn_agent` / `team_*`）actor 构建 | 高：G2 的 sessionID 维度 |
| workspace / worktree 覆盖 | 中 |
| goal 持久化（`PersistHook`）/ 事件落盘（`EventBus`） | 中 |
| `trigger_turn` drain / supervision 进度回调 | 中 |
| 基础会话持久化自愈（`EnsureSession`） | 中 |

### 12.3 测试面

| 套件 | 影响 |
|---|---|
| `cmd/aicli/commands/*`（大量 chat/host 测试） | 必须全绿 |
| `internal/runtimehost` | 新增 `LeaseMode` 两模式测试（构造后 / run 末 / stalled / stop 四态） |
| `internal/api/runtimeapi` | 全量回归（Deps 零值） |
| `cmd/scheduled` | 回归（embedded 路径） |
| TUI 冒烟（人工或脚本） | 起会话 → 一轮 → interrupt → resume → 子代理 |

### 12.4 非代码面

- **文档**：本设计 + 抽取方案 §E3.4 + README 索引；
- **发布**：建议灰度开关（G6）——先内部启用，再默认。

### 12.5 并发与协调（⚠️ 实施前必查）

核验当前工作区：`cmd/aicli/commands` 有 **14 个文件处于未提交改动**（并发 WIP），
**其中就包含 E3.4 必须改写的 `chat_actor_host.go`**：

```
M cmd/aicli/commands/chat_actor_host.go
M cmd/aicli/commands/chat.go
M cmd/aicli/commands/chat_busy_*.go（5 个）
M cmd/aicli/commands/chat_picker_common.go
M cmd/aicli/commands/chat_runtime_command_*.go（3 个）
M cmd/aicli/commands/connector_host*.go（2 个）
```

⇒ **E3.4 与在途 aicli 工作存在直接冲突面**。实施前必须：
1. 确认这批 WIP 的归属与合并时间（避免与 E3.4 互相覆盖）；
2. 在 WIP 落地后再基于**最新** `chat_actor_host.go` 制定/校对差异（本设计的行号基于当前工作区快照）；
3. 若必须并行，则约定：E3.4 只改 `buildSessionActor` 及其新增 `aicliRuntimeDeps`，
   不触碰 busy/picker/runtime-command 路径。
