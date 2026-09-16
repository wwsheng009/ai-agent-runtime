# Supervision 业务化监督实施计划（巡查 / 汇报 / 收敛）

- 日期：2026-09-16
- 输入：`docs/analysis/supervision-business-supervision-gap-analysis-20260916.md`（方案 A–F）
- **实施注记（2026-09-16 落地核验）**：P0–P2 已实现并通过自动化验证（未提交）：
  - **P0-A 工具面 + 双宿主**：`supervision_descendants` 在 CLI 与 API 两宿主可调用；§2.2 `mode=children`
    过滤（`internal/supervision/snapshot.go` 新增 `ScopeModeChildren`，过滤置于 rollup 之前，summary 与
    返回行一致；HTTP `?mode=children` 同步修复）；§2.5 API 宿主装配 `Broker.Supervision`
    （`internal/api/skills/supervision_tool_controller.go` + `handler.go` 构造点）。
  - **P0-A 配套（§2.3 巡查豁免）**：doom-loop 豁免与 polling 软刹车拆分为两个 predicate
    （`supervisionInspectTool` + `pollingSoftBrakeTool`，`internal/agent/doom_loop.go` / `polling_guard.go`）：
    重复巡查不计数，含巡查的批次会重置 streak。
  - **P0-B 进度 rollup**：`internal/supervision/batch_progress.go` / `subagent_progress.go` / `progress.go`，
    父 turn 文本可见 `N/M` 进度行 + summary。
  - **P1-C 完成后收敛（方案 C）**：`agents.autoCloseCompleted`（默认 `off`，可选 `completed` /
    `batch_terminal`）；宿主钩子 `localConvergeTerminalBatchChildren`
    （`cmd/aicli/commands/chat_actor_host.go`）在 batch 终态后投影收敛行，并经 `LocalControlService.Control`
    **真正 close** 任务成功的子会话（durable action audit + resolution 回执），重放幂等。
    - 落地核验发现并修复一个缺陷：收敛行原用 `SeverityInfo`，而 `Notification.ActionRequired()` 只认
      critical/warning，导致"只投影建议、永不执行"；改为 `SeverityWarning` 后 close 才真正发生。
      由新增宿主级集成测试 `cmd/aicli/commands/supervision_autoclose_convergence_test.go` 钉住：
      成功子会话被执行一次 close、留下 `action_close_resolution` 回执、重放零副作用；off /
      未装配执行器 / `completed` 遇到部分失败时零写入。
  - **P1-E 收敛提示**：`BatchDone` 行的模型面提示指向 broker 工具 `close_agent` 并带真实 child session id
    （`localBatchConvergeHint` + 契约测试），不让模型对终态行发 control 动作。
  - **P2-D opt-in 周期巡查**：`supervision.progress_check_interval`（默认 0=关闭）复用既有 wake 通路
    （wake 预算 / 去抖语义不变），仅在"存在 running background batch 且父会话空闲"时注入轻量 progress 摘要。
  - **P2-F**：CLI `Deliver` 不再吞错。
  - 验证（2026-09-16）：`go build ./...`、`go test ./internal/...`、`go test ./cmd/...` 全绿
    （含 `./internal/supervision`、`./internal/subagentbatch`、`./internal/config` 与
    `./cmd/aicli/commands` 的 P0–P2 定向用例）。
  - 待办：§8 第 5 步手工端到端场景（spawn 3 background（快/慢/超时）→ 父 turn 调
    `supervision_descendants` + 观察 preflight progress 行）仍需在真实会话里跑一次。
    宿主级可重复版本已固化为 `cmd/aicli/commands/supervision_e2e_three_background_test.go`
    （数据面全部走生产实现，仅“模型发起 spawn”一步改为直写 registry）。
  - **2026-09-16 全量 `go test ./... -count=1` 核验记录**：除以下两处既有失败外全绿；两处均与本方案
    改动无关（`internal/supervision`、`internal/prompt`、`internal/api/skills` 隔离运行全绿）：
    1. `internal/api/skills` 偶发 panic（全量并发负载下触发，隔离运行 19.9s 全绿）：
       `SQLiteGlobalAgentRegistryStore.Close()` 持锁把 `s.db` 置 nil
       （`agentcontrol/global_agent_store.go:218`），而 `UpsertAgentControlAgent` 在 `ensure()`
       之后未持锁解引用 `s.db`（同文件 `:277`）→ 与并发 Close 的既有数据竞态（P2-9 reconciler
       的 List 闭包正好走这条路径）。修复建议：把“ensure + 取句柄”原子化（`openMu` 保护的
       `dbHandle()`），reconcile 路径各方法改用本地句柄。
    2. `cmd/aicli/commands` 的 `TestDispatchChatCommandStatusDoesNotWriteRawStdout`：捕获到裸 `"u"`
       （异步 spinner 重绘的 ESC 序列被并发写拆断），测试自带的 `stripAsyncTerminalNoise` 只覆盖
       成对/孤立 ESC 序列 → 既有 flaky，与本方案无关。
  - **当前阻断（2026-09-16 20:11）**：并行工作区在 `internal/chat/actor.go` 引入对尚未定义符号
    `sessionRunTerminalCancelSource` 的调用（`git log -S` 无历史，仅见工作区 diff），
    `cmd/aicli/commands` 因此 `[build failed]`，§8.5 的宿主级 e2e 与手工 `go run ./cmd/aicli`
    均暂不可执行；待该文件补齐定义后即可运行。
- 本计划基于对当前工作区的逐文件核对（含在制半成品与 §7 三项复核的验证结论）。

**一句话回答"业务巡检怎么落地"**：巡查原语（`supervision_descendants`）的工具面与 CLI 宿主面
已在工作区写好（未提交），先验证并补齐三件事——`mode=children` 过滤、重复调用豁免（doom-loop 与
polling 软刹车拆开）、API 宿主装配；然后把"父 turn 进度区块"（方案 B 轻量版）接到 preflight，
再做可选的"完成后收敛"（P1）与"周期巡查"（P2）。

## 0. 关键现状（与差距分析文档的偏差，先知道再动手）

1. **方案 A 的工具面 + CLI 宿主面已在工作区实现（未提交、未验证）。**
   - `backend/internal/toolbroker/supervision_tools.go`：接口（`:28-42`）、args（`:51-65`）、
     schema（`:114-146`）、parser（`:246-279`）、dispatcher（`:504-530`）、summary/next_action（`:441-467`）。
   - `broker.go`：常量 `:59-60`、`IsBrokerTool` `:163`、dispatch `:1808`、`normalizeToolName` `:3346-3347`、
     `isVolatileEmptyReplayTool` `:153`；参数审计登记 `broker_arg_audit.go:60`、`broker_arg_kinds.go:188`。
   - `internal/supervision/snapshot.go`：`SnapshotRequest` 新增 `RootScopeID` 覆盖字段（team lead 的
     "投影子树 ≠ durable root scope" 分裂，与 preflight digest 口径一致）。
   - CLI：`cmd/aicli/commands/chat_supervision_tools.go:103-129` 已实现 `SupervisionDescendants`
     （复用 `c.resolution()` 作用域 + `host.Supervision.Provider`）；测试 `chat_supervision_tools_test.go`（+84 行）、
     toolbroker fake 也已补齐（`supervision_tools_test.go:38-43`）。
   - ⇒ **P0-A 主体已落地但未提交**。2026-09-16 已核验：`go build ./...` 通过；
     `go test ./internal/toolbroker/... -run Supervision`、`go test ./cmd/aicli/commands/ -run Supervision`、
     `go test ./internal/supervision/...` 全绿。⇒ 剩余的是语义缺口（§2.2/§2.3）与提交切分
     （工作区同时有 52 个无关改动，只挑 supervision 相关文件）。

2. **CLI 侧不需要新写 descendant provider（文档 §6 方案 A 的第 2 点已过时）。**
   - CLI host 启动即通过 `runtimeserver.BuildSupervisionControlPlane(...)` 装配控制面
     （`cmd/aicli/commands/chat_actor_host.go:732-738`，传入 `AgentRegistry: globalAgentStore` +
     `TeamStore`）。
   - 该构造函数已装配 `supervisionDescendantProvider`（`internal/runtimeserver/supervision.go:110-116`），
     聚合 agentcontrol 记录 + team store + durable team edges（`:274-340`）。
   - ⇒ 在制实现已经直接使用 `host.Supervision.Provider`，此处无需再动。

3. **runtime-server：HTTP 已装、模型工具面未装**（复核项 2 已结案，详见 §7.2）。
   - HTTP：`cmd/runtime-server/main.go:1092-1095`（store/actions/wakes/provider）+ `:1101-1107`
     （真实 close 执行器 `handler.CloseAgentSessionByID`）。
   - 模型工具：`internal/api/skills/handler.go:4719/4732/4779` 只构造 `&toolbroker.Broker{}`，
     全仓没有第二处 `AgentSupervisionController` 实现 ⇒ **API 路径的 `Broker.Supervision` 恒为 nil，
     `supervision_*`/`ack_lifecycle`/`control_descendant` 只存在于 CLI 工具面**。

4. **轮询治理还有一个隐性缺口需要补**：`internal/agent/doom_loop.go:165-183` 的
   `semanticToolCallRepeatExempt` 不含 supervision 工具，而工具描述
   （`supervision_tools.go:117`）已向模型承诺 "repeat calls ... are exempt from the anti-polling advisory"。
   - 不豁免：重复相同参数的 `supervision_descendants` 会进入 doom-loop 语义重复计数
     （`doom_loop.go:99-111`：≥2 注入 advisory、=4 告警、`maxRepeated>0` 时硬停）。
   - 直接塞进现有豁免集合：`polling_guard.go:169-185` 复用同一 predicate，3 次相同调用会触发
     "waiting longer is not progress" 软刹车，与"巡查原语"定位冲突。
   - ⇒ 需要把"doom-loop 豁免"与"polling 软刹车计数"拆成两个 predicate（§2.3）。

5. **方案 B 的数据来源已在持久层，无需新表**（子代理核查结论）：
   - N/M 可由 `host.SubagentBatches`（`subagentbatch.BatchStore`；字段 `chat_actor_host.go:147`，
     装配 `:770`）的 `ListBatches` + `ListTasks`/`Counts` 直接得出。
   - `SubagentBatch.CompletedCount/RunningCount` 在 finalize 前是陈旧的（`subagent_batch_coordinator.go:1399-1417`
     只在终态/错误路径更新），**不能**直接读。
   - `SubagentTaskRecord.LastProgressAt` 有列无生产写入方（测试专用）；"最近进度 12s 前"的秒级
     真实进度目前拿不到，P0 用 task `UpdatedAt` / batch `HeartbeatAt`（60s 粒度）。
   - CLI preflight 有 `len(digest.Items)==0 → 直接返回 prompt` 的门（`chat_actor_host.go:2339-2340`），
     只加 progress 区块会被它吞掉，必须一起放宽。

## 1. 目标与验收（对齐分析文档 §7）

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| **P0** | 方案 A（`supervision_descendants` 落地两宿主）+ 方案 B 轻量版（进度区块） | 场景：spawn 3 个 background 子代理（1 快完成 / 1 慢 / 1 超时），父 agent 单次工具调用看到 3 行 + summary；父 turn 文本可见 `2/3` 进度 |
| **P1** | 方案 C（自动收敛/推荐动作）+ 方案 E（契约与提示词对齐） | batch 终态后子会话被关闭（或通知带 close 推荐并被收敛）；关闭动作有 audit 记录 |
| **P2** | 方案 D（周期巡查，opt-in）+ 方案 F（admission 语义） | 开关开启后按间隔收到 progress 摘要且不触发 polling 软刹车；关闭时行为与现状完全一致 |

## 2. P0-A：补齐 `supervision_descendants`（工具面已在，补宿主面）

### 2.1 CLI 宿主：`chat_supervision_tools.go` 新增方法

```go
func (c *localSupervisionToolController) SupervisionDescendants(
    ctx context.Context, parentSessionID string, args toolbroker.SupervisionDescendantsArgs,
) (*supervision.Snapshot, error)
```

- scope：复用 `c.resolution(ctx, parentSessionID)`（模型不可指定 root scope，沿用 `:42-61` 口径）。
- 调用 `supervision.BuildSnapshot(ctx, c.host.Supervision.Store, supervision.SnapshotRequest{...})`：
  - `Scope{RootSessionID|RootTeamID, Mode}`；
  - `AfterSeq / Health / IncludeTerminal / Limit` 直接映射；
  - `Provider: c.host.Supervision.Provider`（为 nil 时 BuildSnapshot 仍返回纯通知行，不报错）；
  - `HostCapabilities: localSupervisionHostCapabilities(c.host)`（`:2216-2223`）。
- Limit 缺省：与 snapshot 包默认（200）或新增 `supervision.snapshotMaxItems`（默认 50）二选一，
  在 §9 待决中定稿；倾向新增配置并与 `DigestMaxItems` 同量级，避免一次注入过大。

### 2.2 `mode=children` 的语义缺口（顺手修）

- `BuildSnapshot` 不消费 `Scope.Mode`；runtime-server provider（`runtimeserver/supervision.go:274-340`）
  用 `ListAgentControlAgents{RootSessionID}` 返回整棵子树，HTTP `?mode=children` 目前同样未过滤。
- 决定：在 `snapshot.go` 行构建完成后按 `Mode=="children"` 过滤 `len(ParentPath) <= 1`
  （host-neutral，两宿主/HTTP 同时修好），provider 契约不变。
- 测试：`snapshot_test.go` 增加 children / descendants 两例。
- **已实现（2026-09-16）**：`snapshot.go` 新增 `ScopeModeChildren/ScopeModeDescendants` 常量与
  行过滤（丢弃 `len(ParentPath) > 1` 的行，位于 rollup 之前 ⇒ summary 与返回行一致；无运行态的
  通知行无 path，保持可见）；新测试 `TestBuildSnapshot_ScopeModeChildrenNarrowsRows` 全绿。

### 2.3 让"反轮询豁免"承诺落到实现

- `internal/agent/doom_loop.go`：把只读巡查工具（`supervision_snapshot`、`supervision_descendants`）
  加入 `semanticToolCallRepeatExempt`；`ack_lifecycle` / `control_descendant` 是否加入按既有
  `polling_guard_test.go:202-262` 契约决定（写动作重复提交有 CAS 兜底，倾向不豁免）。
- `internal/agent/polling_guard.go`：新增 `pollingSoftBrakeTool(name)`（沿用现有 wait/read/list 集合，
  显式排除 supervision 只读巡查工具），`pollingBatchFingerprint`（`:169-185`）改用它。
  - 效果：巡查重复调用既不触发 doom-loop 告警，也不触发 polling 软刹车；`wait_agent` 等既有行为逐字不变。
- 测试：
  - `doom_loop_test.go`：重复 `supervision_descendants` → `Fingerprint` 为空；
  - `polling_guard_test.go`：既有契约全绿 + 新增"巡查工具不产生 polling 指纹"用例。
- **已实现（2026-09-16）**：`semanticToolCallRepeatExempt` 经新助手 `supervisionInspectTool` 豁免
  `supervision_snapshot`/`supervision_descendants`；`polling_guard.go` 新增
  `pollingSoftBrakeTool = 豁免集 − 巡查工具` 并替换 `pollingBatchFingerprint` 的判定。
  `ack_lifecycle`/`control_descendant` 维持不豁免（写动作重复有 CAS/幂等兜底）。
  测试：`TestDoomLoopTracker_ExemptsSupervisionInspection`、
  `TestPollingBackoffTracker_SupervisionInspectIsNotPolling` 全绿。

### 2.4 toolbroker 测试补齐

- `fakeSupervisionController`（`supervision_tools_test.go:16-48`）增加方法 + 字段
  （`descendants *supervision.Snapshot`、`descendantReq SupervisionDescendantsArgs`）。
- 用例：
  - Definitions 门控包含 `ToolSupervisionDescendants`（`:61-70` 的断言扩展）；
  - parser：`mode`/`health` 非法值拒绝、合法值归一（`:250-279` 已实现，补测试）；
  - dispatch：payload（summary 计数）+ cache-safe summary + `next_action`（`:441-467/504-530` 已实现，补断言）。

### 2.5 runtime-server（复核项 2 结论驱动）

- 结论：API 路径同样使用 `toolbroker.Broker`（`internal/api/skills/handler.go:4719/4732/4779`），
  只是从不设置 `Broker.Supervision`；而它已持有 store/actions/wakes/provider 与 close 执行器
  （`cmd/runtime-server/main.go:1092-1107`）。⇒ 补齐成本低，建议 P0 一并做。
- 做法：把 `localSupervisionToolController` 中宿主无关的部分（scope 解析 + BuildSnapshot 调用）
  抽成 `internal/supervision` 的 `LocalToolService`（或等价类型），CLI 与 API 各写一个薄适配
  （CLI 用 `ChatSession.ActiveTeam` + `TeamStore` 解析 team scope；API 用 handler 的 team store /
  会话作用域），装配点 `handler.go:4719-4780` 一带设置 `broker.Supervision`。
- 若不接受该改动：本阶段只保证 HTTP 快照不回归，并把"模型工具仅 CLI"记为已知限制（§9）。

## 3. P0-B：父 turn 进度区块（方案 B 轻量版）

### 3.1 数据与入口（已核实）

- 数据源：`host.SubagentBatches`（`subagentbatch.BatchStore`；字段 `chat_actor_host.go:147`，装配 `:770`）：
  - active batches：`ListBatches(ctx, BatchFilter{ParentSessionID: sessionID, Status: []{queued,running}, Limit: n})`；
  - 每 batch：`ListTasks(ctx, batchID)` + `subagentbatch.Counts(tasks)` → N/M、running 列表、running task 的
    `UpdatedAt`/`StartedAt`；
  - liveness：`batch.HeartbeatAt`（60s 粒度，`renewHeartbeat` `subagent_batch_coordinator.go:1062-1113`）。
- 注入点：`injectLocalSupervisionPreflight`（`chat_actor_host.go:2310-2363`）：
  - 在 `:2354` `text := digest.Text` 之后拼接 progress 区块；
  - **必须同时放宽 `:2339-2340` 的门**：`len(digest.Items)==0 && progress 为空` 才早退，否则"只有进度、
    没有通知"的 turn 会被吞掉。
- API 宿主对等：`internal/api/skills/supervision_handlers.go:315-317/330-338` 同位置用同一个渲染函数；
  若 API host 拿不到 batch store，则记录为 P1 对等项（§9）。
- 渲染契约：
  - 行数上限：新增 `supervision.progressMaxItems`（默认 5）；
  - 单行示例：`- batch <id8>: 2/3 completed, 1 running (worker-3, heartbeat 12s ago)`；
  - 仅当存在 active batch 时输出；否则输出 0 字节，保证现有行为不变（逐字回归）。

### 3.2 为什么 P0 内不做 durable progress ledger

- `SubagentTaskRecord.LastProgressAt` 有列、无生产写入方（仅测试写入）；写它需要把 live-only 的
  `subagent.progress` 事件（只在 API 进程、`session_runtime_support.go:965-985` 实例化）按
  child session → batch task 映射回填。
- P0 验收只要求"父 turn 内可见 2/3"，durable ledger 与周期巡查（P2-D）一起做，避免先加表却长期空转。

## 4. P1-C：完成后收敛（recommended close + 可选 auto-close）

### 4.1 契约与推荐动作（默认安全档）

- `localSubagentBatchLifecycleProjectorWithWakeDrain`（`chat_actor_host.go:363-444`）当前 `BatchDone`
  落到 `SeverityInfo/SupervisionTerminated/ResolutionClosed/recommended=inspect`（`:380-384`），
  `allowed_actions` 已含 `close`（`:420`）。
- 改动：`BatchDone` 时 `recommended_action=close`（无失败任务时），并在摘要行里带上
  `notification_id`（snapshot 行已有该字段）；这样模型"完成后关闭"只需把推荐动作执行一次。
- 风险：`recommended=close` 会影响既有断言（`cmd/aicli/commands/supervision_test.go` 等），需同步更新。

### 4.1.1 执行现实（复核项 1 结论，必须先读）

- 动作能否执行的**唯一判据**是 `Evaluator.evaluateAllowedActions`（`internal/supervision/evaluator.go:39-41`）：
  `ResolutionState != "" && != unresolved` ⇒ 只返回 `[inspect]`。存储在通知行里的 `allowed_actions`
  列只写不读，不参与校验。
- batch 终态投影（`chat_actor_host.go:380-384/421`）把 `BatchDone` 写成
  `ResolutionClosed` ⇒ 在这些行上 `control_descendant action=close` 会被 `ErrActionNotAllowed`
  拒绝（`action_service.go:187-190`），尽管该行 `allowed_actions` 字面含 `close`。
- 只有 **未解决** 的终态行（`BatchFailed/BatchTimedOut/BatchOrphaned`，`chat_actor_host.go:387-400`，
  resolution=unresolved）才允许 close（`evaluator.go:50-60` 的 subject-kind 默认集）。
- 成功执行 mutation 后必然产生一条新的回执通知（`action_service.go:441-511`：info/recovered/
  `allowed_actions=[inspect]`/`DecisionActioned`），并把源行置为 resolved（`:503-506`）。
  系统**没有**"终态行自动 resolve"的路径。
- ⇒ P1 的正确姿势：
  - **模型面推荐**：`BatchDone` 行推荐模型调用 `close_agent`（它本就是 broker 工具，
    `broker.go:163`，审计走工具事件），而不是在已 closed 的行上再发 control 动作；
    `supervision_descendants`/digest 行需带出 child session_id。
  - **自动收敛**：宿主侧直接调用现有 close 通道（CLI registry close / API `CloseAgentSessionByID`），
    并为每 batch 写一条新的回执通知做审计，不复用已 closed 行。
  - 备选（不推荐默认）：让 batch 终态行保持 unresolved 直到所有子会话关闭——会改变恢复语义与
    未解决计数，仅作为后续可选项。

### 4.2 自动收敛（opt-in）

- 配置：`agents.autoCloseCompleted`：`off`（默认）| `completed` | `batch_terminal`。
  - `completed`：子会话成功完成投影后（`projectLocalAgentCompletion`，`chat_actor_registry.go:1131-1158`）
    关闭该子会话；
  - `batch_terminal`：batch 终态（`chat_actor_host.go:363-444`）后关闭该 batch 下所有已终态子会话。
- 执行路径：宿主侧 close 通道（CLI: actor registry close；API: `CloseAgentSessionByID`，
  `cmd/runtime-server/main.go:1104-1106`）+ 一条新的回执通知；不要复用已 closed 的行做 control 动作
  （理由见 §4.1.1）。若坚持走 `ActionService.Control`，只能作用于 unresolved 的失败/超时/孤儿行。
- 收敛条件：开关开启 ∧ 子会话终态 ∧ 无 pending approval ∧ 每 batch 关闭次数 ≤ task 数；失败仅记录
  日志/事件，不回滚 batch 终态投影。
- 测试：配置解析；开启后 mock executor 断言 close 调用 + audit 行；关闭时零调用（现状回归）。

### 4.3 已确认缺口：run 终态不收敛 `progress_stalled`（2026-09-16 实测，归 P1）

- 实测数据（本会话 root `session_20260916183715_cU3sndSl`，
  `~/.aicli/sessions/runtime/supervision/supervision.db`）：
  - `supervision_execution_runs`：`run_20260916104153/104155/104156` 已是
    `succeeded/succeeded/failed`，`finished_at=10:48:20Z–10:49:57Z`，`progress_seq=0`，`version=2`；
  - `supervision_lifecycle_notifications`：对应 3 条 `progress_stalled`（`severity=critical`、
    `supervision_state=stalled`、`resolution=unresolved`、created `10:46:56Z`）在 run 终态之后
    **仍是 unresolved**（version 却涨到 86/163/185，`updated_at` 反复刷新），直到本轮人工
    `ack_lifecycle(resolve)` 才收敛。
- 影响：父 turn 每轮被注入陈旧的 "critical / action required"（本轮 4 条待处理里 3 条属此类），
  噪声随子代理数量线性增长；且 version 自增数百次说明还有反复更新循环。
- 规则建议：**subject 终态 ⇒ 收敛同一 subject 的未决 `progress_stalled`**（run 成功 → `recovered`，
  失败/取消 → `failed`）。这与 §4.1.1 的"终态行不自动 resolve"不冲突：这里收敛的是**同 subject 的
  过期告警行**，而不是对已 closed 的行发 control 动作。
- 实现点（实施时确认）：run 终态写入路径（`supervision_execution_runs.status/finished_at` 更新处）
  或 deadline sweeper 侧，补"同 subject 未决告警收敛"；同时给"巡检反复 bump version"加节流。
- 验收：子代理完成后 digest 不再重复出现该 run 的 stalled 行；新增回归测试覆盖"先 stalled 后 succeeded"。

## 5. P2-D：opt-in 周期巡查（`supervision.progressCheckInterval`）

- 配置：`supervision.progressCheckInterval`（duration，默认 0=关闭）。
- 实现位置：**CLI 与 runtime-server 两个宿主都已落地**（2026-09-16 补齐 API 侧，此前只有 CLI）：
  - CLI：`cmd/aicli/commands/chat_actor_progress_check.go`（单根会话，随 host lifecycleCtx 启停）；
  - API：`internal/api/skills/supervision_progress_check.go`，启动点是 `SetSupervisionConfig`
    （runtime-server 已按 `supervision.progress_check_interval` 传参）。多会话宿主的两点形态差异：
    巡查单位是"batch store 里活跃批次所属的父会话"（单轮上限 16 个父会话、按最近活动排序），
    且**不创建** session actor（只为宿主已知的会话注入，历史/孤儿 batch 行不会凭空造 actor；
    未命中时父会话的下一轮自然 turn 仍会从 preflight digest 看到进度）；
  - 仅当 interval>0 且该 host 存在 active batch 时启动 ticker（无 active batch 即停，避免"常驻扫描
    所有会话"的架构回退）；
  - 到点且父会话空闲（无 running turn、无 pending wake）时：构建 progress 摘要（复用 §3 渲染）→
    经现有 wake 提交路径注入一次汇报 turn（复用 wake 限流/admission，不新建通知类别）；
  - 关闭时完全不注册 ticker（行为与现状一致）。
- 与"无常驻轮询"原则的隔离：opt-in、按需启停、只对有 active batch 的会话生效、行数预算上限；
  runbook 记录在 `docs/plan/supervision-operator-runbook.md`。
- 验收：开→按间隔收到 progress；关→既有回归全绿且无新常驻 goroutine。
  - 证据（2026-09-16）：`go test ./internal/api/skills/ -run TestAPISupervisionProgressCheck`（API 侧
    "注入/无 active batch/父忙/待投递 wake/未知父会话/opt-in 启停"六条契约）与 CLI 侧
    `chat_actor_progress_check_test.go` 逐条对应；`go test ./internal/api/skills/`、`go build ./...` 全绿。

## 6. P2-E/F：契约与交付语义

- **E（提示词/描述对齐）**：
  - `internal/prompt/environment_context.go:247-251` 的引导语改为"巡检用 `supervision_descendants`（一次看 N 行）→
    完成后收敛并汇报"，与工具描述（`supervision_tools.go:116-118`）一致；
  - 工具描述里"repeat calls ... exempt from the anti-polling advisory"的承诺必须与 §2.3 的实现同批次落地，
    否则就是"描述承诺 > 实现"的假契约。
- **F（唤醒交付 admission 语义，复核项 3 结论）**：
  - 核心 `WakeConsumer.MaybeWakeParent` 是**同步返回 Deliver 错误**的（`wake_consumer.go:40/72-79`），
    API 宿主如实返回错误（`session_runtime_support.go:1224-1232`）；只有 CLI 的 Deliver 实现是
    fire-and-forget：`chat_actor_host.go:512-542` 里 `go func(){ ...; _ = h.submitParentWakeTurn(...) }()`
    并 `return nil` ⇒ 提交失败被吞掉。
  - 父不可运行/未知时 wake 不丢：`ErrWakeParentBusy`（`wake_scheduler.go:255-256`）不 claim 行，
    保持 unclaimed 供下次 transition 重试（`sqlite_store.go:1166-1169`）；`ErrWakeRateLimited`
    （`wake_scheduler.go:279-281`）同样保持 durable。
  - claim 之后的投递失败会 release wake 行（`wake_consumer.go:67-84`），通知本身仍 durable，
    下个自然 turn 的 preflight 会再注入。
  - ⇒ F 的最小修复：改 CLI `chat_actor_host.go:512-542`——失败时记事件/日志并
    release/重排 wake（而不是 `_ =` 丢弃），让"已投递/待重试"可观测；API 侧已对齐，无需改。

## 7. 三项复核结论

（当前工作区核对结果）

1. **info/closed 行是否允许 close**：**不允许**（`resolution=closed` 行只允许 `inspect`），
   详见 §7.1；影响 P1 收敛路径设计（§4.1.1）。
2. **runtime-server 侧 provider 装配情况**：**HTTP 已装、模型工具未装**。
   - `cmd/runtime-server/main.go:1075-1107`：`BuildSupervisionControlPlane` 装配 store/actions/wakes/provider
     并注入 `SupervisionRuntimeExecutor`（`CloseAgent: handler.CloseAgentSessionByID`）；
     `:1092-1095` 把四件套交给 HTTP handler。
   - **但 `Broker.Supervision` 在 API 路径始终为 nil**：`internal/api/skills/handler.go:4719/4732/4779`
     只构造 `&toolbroker.Broker{}`，全仓无第二处 `AgentSupervisionController` 实现（仅 CLI 一处，
     `chat_actor_host.go:1461-1465` 装配）。
   - 结论：`supervision_descendants`/`supervision_snapshot` 当前只在 CLI 模型工具面可见；runtime-server
     只有 HTTP `/supervision/*`。若要求两宿主对等，需要给 API 宿主补一个 controller（它已具备全部依赖：
     store/actions/wakes/provider + close 执行器），实现位置建议 `internal/api/skills/supervision_tool_controller.go`，
     装配点 `handler.go:4719-4780` 一带。
3. **无订阅者/Deliver 投递结果**：核心同步返回错误、wake 不丢（忙碌/限流时保持 pending），
   但 CLI Deliver 实现吞错。详见 §7.2。

### 7.1 动作校验与终态行（子代理证据）

- 唯一判据：`internal/supervision/evaluator.go:39-41`（`ResolutionState` 非空且非 unresolved ⇒
  只允许 inspect；`evaluator.go:36-38/42-49` 还覆盖 decision/deferred/auto-action 状态）。
  执行入口：`action_service.go:187-190`（`RequestAction` 拒绝 `ErrActionNotAllowed`）。
  存储的 `allowed_actions` 列（`projection.go:77`、`sqlite_store.go:562/622`）只写不读。
- 行本身可寻址（`action_service.go:376-395` 支持 `IncludeResolved`；`local_control.go:306-309`
  按 id 加载），版本校验仅在 `ExpectedVersion>0` 时比较（`action_service.go:181-183`）。
- 终态行 close 的结论如上；mutation 成功会生成新回执通知（`action_service.go:441-511`），
  并把源行 resolve（`:503-506`）；系统无终态自动 resolve。

### 7.2 唤醒投递与 admission（子代理证据）

- `WakeConsumer`（`wake_consumer.go:20-34`）同步调用 Deliver 并返回错误（`:72-79`）；
  CLI 实现异步吞错（`chat_actor_host.go:512-542`），API 实现返回错误（`session_runtime_support.go:1224-1232`）。
- 无订阅者/父不可运行：wake 保持 durable/unclaimed（`wake_scheduler.go:255-256`、
  `sqlite_store.go:1166-1169`），下个 transition 重试；被限流时同上（`wake_scheduler.go:279-281`）。
- claim 后投递失败：release wake 行（`wake_consumer.go:67-84` → `wake_scheduler.go:353-355`），
  通知不丢，由后续自然 turn 的 preflight 再注入。

## 8. 实施顺序与验证

1. ~~编译/测试确认在制改动~~ **已完成（2026-09-16 全绿，见 §0.1）**。
2. ~~P0-A 收尾：`mode=children` 过滤 + doom-loop/polling 分类拆分~~ **已完成（2026-09-16）**：
   `snapshot.go` 模式常量+过滤、`agent` 包 `supervisionInspectTool`/`pollingSoftBrakeTool` 拆分，
   新增 3 个测试；`go build ./...` + `go test ./internal/agent/... ./internal/supervision/... ./internal/toolbroker/...` 全绿。
3. ~~P0-A 对等：API 宿主 supervision controller（`internal/api/skills`）+ 门控测试~~ **已完成（2026-09-16）**：
   `internal/api/skills/supervision_tool_controller.go` + `handler.go:4763-4773` 装配 `Broker.Supervision`
   （无 durable store 时保持 nil，与 CLI 宿主一致），门控测试随包通过。
4. ~~P0-B：progress rollup（`subagentbatch` → preflight 渲染）+ 放宽空 digest 门 + CLI/API 测试~~ **已完成（2026-09-16）**：
   `internal/supervision/batch_progress.go`（`NewBatchProgressSource`，默认 10 分钟窗口 / 32 批）；
   `chat_actor_host.go:2540` 注入进度源、`:2547` 空 digest 门放宽为 `Items==0 && Progress==0`。
5. ~~手工场景：spawn 3 background（快/慢/超时）→ 父 turn 调 `supervision_descendants` + preflight progress 行~~
   **已固化为宿主级端到端回归（2026-09-16）**：`backend/cmd/aicli/commands/supervision_e2e_three_background_test.go`
   用真实 SQLite agent registry + `runtimeserver.BuildSupervisionControlPlane` + 真实 batch store 复现快/慢/超时三个子会话：
   单次 `SupervisionDescendants` 调用返回 3 行（terminated / running / timed_out，timed_out 行 `recommended=cancel`、
   允许动作含 cancel+close、summary 计数正确），preflight 同时携带通知行与 `2/3 completed, 1 running` 进度行；
   第二用例钉住"只有进度、无通知"的 turn 仍被注入（守护放宽后的空 digest 门）。
   *仍未覆盖*：真实模型交互式运行（`go run ./cmd/aicli ...` 手工跑）属人工步骤，未执行。
6. ~~P1：`BatchDone` 行推荐 close_agent / `agents.autoCloseCompleted` opt-in~~ **已完成（2026-09-16）**：
   `chat_actor_batch_converge.go` —— `localBatchConvergeHint` 指向 `close_agent` 并带真实子会话 id；
   `localConvergeTerminalBatchChildren` 按 `agents.autoCloseCompleted`（off / batch_terminal / completed）收敛，
   close 成功生成回执并 resolve 源行（重放幂等）；测试 `chat_actor_batch_converge_test.go`。
7. ~~P2：`supervision.progressCheckInterval` opt-in ticker；F：CLI Deliver 不再吞错（§6-F）~~ **已完成（2026-09-16）**：
   `chat_actor_progress_check.go`（`wireLocalSupervisionProgressSource`，默认关）；
   `submitParentWakeTurn` 错误改为记账 + 重排，事件 `EventSupervisionWakeDeliveryFailed`（`chat_actor_host.go:624-630/736`）。
8. ~~P1 补强（§4.3）：run 终态收敛同 subject 的 `progress_stalled`~~ **已完成（2026-09-16）**：
   `internal/supervision/run_alerts.go`（`ConvergeRunAlerts`：succeeded→`recovered`，其余终态→`failed`；
   CAS 重试 3 次、首解不覆写、nil store/空 subject 降级为 no-op）+ `projection_run_finalize_test.go` 回归。
9. ~~收敛提示口径统一：终态 batch 一律指向**可执行**路径~~ **已完成（2026-09-16）**：
   终态行的 done 通知是 `resolution=closed`，evaluator 对已关闭行只允许 `inspect`（§4.1.1/§7.1），
   因此 `progress.go` 的终态组提示由 `control_descendant` 改为 `close_agent`（与 P1 通知行同一口径）；
   `internal/prompt/environment_context.go` 的 supervision flow 同步改为"用 `close_agent` 收敛 / 仅未决行用
   `control_descendant close`"。两处各加断言防回归（`progress_test.go` 断言含 `close_agent` 且不含
   `control_descendant`；`environment_context_test.go` 断言新措辞），operator runbook §3.2/§4.1/§4.2 同步改写。

## 9. 风险与待决

- **在制改动与其他并行开发混在工作区**（frontend/usage ledger/SSE 等 52 个文件已修改）：P0-A 落地必须
  "只提交 supervision 相关文件"，避免裹挟无关改动。
- `mode=children` 过滤会改变现有 HTTP `?mode=children` 行为（从"整棵子树"变为"仅直接子级"）→ 记为
  缺陷修复，但需要在 `supervision_handlers.go` 测试里显式断言。
- 自动收敛（P1）触碰会话生命周期，必须有开关、预算与审计；不能默认开。
- 已定：`resolution=closed` 行不可 close（§7.1）⇒ P1 收敛一律走 `close_agent`/宿主 close + 新回执通知；
  不要再设计"对已 closed 行发 control 动作"的方案。
- 待决：`Snapshot` 的 `Limit` 缺省值（当前 CLI 方法透传 `args.Limit`，0 值时由 BuildSnapshot 决定）
  应统一为配置项，实施时定稿默认值。
- 已定（2026-09-16 落地）：API 宿主的 team scope 来源 = `handlerSupervisionToolController.leadTeamID`
  （`internal/api/skills/supervision_tool_controller.go`）：普通会话用自己的会话 id，已注册的 team lead 用团队 id
  （优先 active team），与 preflight 注入口径一致；模型无法指定 root scope，越权通知由 LocalControlService 拒绝。
- 已确认（§4.3）：run 终态后 `progress_stalled` 仍 unresolved ⇒ 父 turn 每轮被刷 critical 告警，
  已计入 P1 必修（本会话实测 3 条，人工 resolve 收敛）。
- 已确认（本会话实测，新缺陷）：对已因执行期限终止的子会话（`session_end`：`cancel_source=execution_context`、
  `status=stopped`、`success=false`）补发 `resolve_agent_approval`（即使 `allow=false`）会**复活**该会话：
  `session_start resume=true` + `session_compact_skipped(reason=resume_run)`，run 越过已触发的 deadline 继续执行，
  监督面出现 `terminated` 行与实际 `running` 状态自相矛盾。时间线证据（session_20260916191352_IJOE6as2）：
  `session_end` 11:53:18Z → `approval_resolved`(allow=false) 11:54:32Z → `session_start resume=true` 11:54:32Z。
  建议：决议路径 resume 前检查终止态（deadline 已过 / `stopped` 时只落决议、不复跑）；确需恢复则重置 deadline/预算
  并重新投影 lifecycle（标回 running + 新通知）。本会话处置：`close_agent` 停止复活 run，4 条 lifecycle 行已 resolve
  （run×2=`failed`、approval 阻塞行=`closed`、terminated 行=`failed`）。正式修复方案（含代码定位与测试设计）：
  `docs/plan/supervision-approval-resume-past-deadline-fix-plan.md`。
  **修复状态（2026-09-16 更新）：已实施并全绿**——终态守卫（P0-1/P0-2）、中止路径按取消原因分流
  （P0-4）、双宿主决议语义与共享契约（P0-3/P1-1）、持久字段 `LastRunTerminalReason`（迁移 v21）
  全部落地；灰度开关 `supervision.approval_terminal_guard`（默认开，显式 `false` 回退旧行为）已补实现，
  CLI/API 双宿主透传，并有「关掉即旧行为」钉子测试；共新增 12 个用例
  （`internal/chat/actor_approval_terminal_test.go`、`internal/toolbroker/approval_resolution_test.go`、
  `internal/supervision/config_test.go`）并完成"先红后绿"验证；
  最终一轮 5 包回归 + build/vet 全绿（GO_TEST_EXIT=0）。
  详见该方案 §11（实施映射、与方案的偏差、未实施项、验证证据、验收对照、提交切分）。
- 建议第一批提交：`internal/toolbroker/*`、`internal/supervision/snapshot.go`、
  `cmd/aicli/commands/chat_supervision_tools*.go` 等 supervision 相关文件（与工作区其余 52 个
  无关改动切分提交）。
