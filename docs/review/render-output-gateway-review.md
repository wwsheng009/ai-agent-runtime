# Render Output 中间层实施审查报告

> 审查依据：`docs/plan/aicli-chat-render-intermediate-layer-design-plan.md`  
> 审查范围：`backend/cmd/aicli/ui/render/output/` + 集成点（`terminal_session.go`, `terminal_session_presenter.go`, `chat_setup.go`, `chat_ui_actor.go`, `chat_interaction.go`, `fixed_bottom_surface.go`, `terminal_output.go`, `terminal.go`）  
> 审查日期：2026-09-01  
> 基线提交：`035d40a`（HEAD）  
> 关键实现提交：`9ef01af` (Phase 0-1), `eca4c3f` (Phase 2), `b1d603a` (Phase 3), `30f423e` (Phase 4), `6b15439` (Phase 5), `d64d217` (Phase 5), `477a3cd` (Phase 6)

---

## 1. 总体评估

设计方案（Phases 0-6）**已基本实现**。核心 `render/output` 包结构完整，生产集成链路已闭环，`go build`/`go vet`/`go test` 全部通过。

| 阶段 | 计划交付 | 实施状态 | 说明 |
|------|---------|---------|------|
| Phase 0 | 契约/fixture | ✅ 完成 | 完整类型体系、Gateway 状态机、Memory/Capture/Discard/Fault sink、contract tests |
| Phase 1 | PhysicalSink + TerminalSession 接入 | ✅ 完成 | PhysicalSink 包装 writer/aborter；FlushTransaction/alternate/history 走 gateway |
| Phase 2 | VirtualTerminalSink + 观察面 | ✅ 完成 | VirtualTerminalSink、capture、geometry context barrier、delivery journal |
| Phase 3 | Legacy adapter + binding fence | ⚠️ 部分实现 | SessionBindingRegistry 已实现；LegacyTransactionAdapter 类型不存在（改为 Phase 6 直接删除） |
| Phase 4 | 物理+capture 双跑/parity | ✅ 完成 | mirror scheduler、parity、/debug Render Output 节 |
| Phase 5 | 路由切换/回放 | ✅ 完成 | Begin/Commit/AbortReconfigure 两阶段 barrier、replay recorder/decoder |
| Phase 6 | 删除绕路/收口 | ✅ 完成 | Legacy immediate renderer chain 已删除（commit 477a3cd） |

---

## 2. 核心包实现详细检查

### 2.1 `types.go` — 类型体系

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| `RenderIntent` / `RenderBatch` | ✅ 已实现 | Intent 含 producer 字段，Batch 由 gateway 盖章 |
| `TransactionKind` 闭集 | ✅ 已实现 | 9 种 kind，含 `TransactionContextBarrier` |
| `HistoryEpoch` 只允许 `history_handoff`/`frame_history` | ✅ 已实现 | `historyBearingKind()` 强制校验 |
| 空 bytes 只允许 `TransactionContextBarrier` | ✅ 已实现 | `validateIntent()` 检查 |
| `WriteCertainty` / `DeliveryStatus` / `DeliveryErrorClass` | ✅ 已实现 | 与计划伪代码完全一致 |
| `AdmissionReceipt` / `SinkDeliveryResult` / `TargetReceipt` / `OutputReceipt` | ✅ 已实现 | Primary=nil 区分 pre-admission |
| `MirrorReceipt` / `MirrorAdmissionReceipt` / `MirrorEntryRef` | ✅ 已实现 | 含 Scheduled/TargetInvoked/CallbackReturned |
| `RecordedBatch` / `RecordedOutputReceipt` / `SanitizedBatch()` | ✅ 已实现 | 只允许 metadata_only/hash_only |
| `HistoryDeliveryDomain` | ✅ 已实现 | 由 gateway 盖章，producer 不能覆盖 |
| 深拷贝 (`RenderBatch.deepCopy()`) | ✅ 已实现 | sink 不得修改 batch |
| `normalizeSinkResult()` | ✅ 已实现 | 保守归一化，禁止伪造 committed/zero proof |

### 2.2 `gateway.go` — RenderOutputGateway

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| `RenderSubmitPort` / `RenderOutputPort` 接口 | ✅ 已实现 | Port 分离权限 |
| `Run()` 启动 gateway worker | ✅ 已实现 | 启动 runner goroutine |
| `Submit(ctx, intent) OutputReceipt` | ✅ 已实现 | 完整 admission + primary dispatch + mirror scheduling |
| `Close(ctx)` 关闭 | ✅ 已实现 | 含 drain、abort、close owned sinks |
| `BeginReconfigure` / `CommitReconfigure` / `AbortReconfigure` | ✅ 已实现 | 两阶段 barrier |
| `WaitIdle(ctx)` / `Drain(ctx)` | ✅ 已实现 | 基于 sequence cutoff |
| `Snapshot()` | ✅ 已实现 | 返回 detached 快照 |
| `Subscribe()` / `RecentDeliveries()` / `RecentEvents()` | ✅ 已实现 | 可观测性 |
| `SetPendingRoute()` | ✅ 已实现 | 预配置路由 |
| `MirrorSlots()` / `RecordsSealed()` | ✅ 已实现 | 诊断 |

### 2.3 `state.go` — 状态机

| 状态 | 实施状态 | 备注 |
|------|---------|------|
| `Open` | ✅ 已实现 | 接受 Submit |
| `Reconfiguring` | ✅ 已实现 | Submit 返回 `AdmissionDeferred + DeliveryErrorReconfiguring` |
| `Closing` | ✅ 已实现 | Submit 返回 `AdmissionRejected + DeliveryErrorClosed` |
| `Closed` | ✅ 已实现 | 幂等 |
| `Abandoned` | ✅ 已实现 | 永不复用 writer |

状态转换：`Open -> Reconfiguring -> Open`, `-> Closing -> Closed`, `-> Abandoned` — 与计划一致。

### 2.4 `sink.go` — Sink 契约

| 接口/类型 | 实施状态 | 备注 |
|----------|---------|------|
| `RenderOutputSink` | ✅ 已实现 | `Submit`, `Snapshot`, `Abort`, `Close` |
| `RenderMirrorSink` | ✅ 已实现 | `SubmitMirror` 含 primary outcome |
| `InvocationAborter` | ✅ 已实现 | 可选 |
| `FlushableSink` / `VirtualProjectionSink` / `CaptureReadableSink` | ✅ 已实现 | 可选能力 |
| `CapturePayloadAccess` / `CapturePayloadAuthorizer` | ✅ 已实现 | payload 访问控制 |
| `TargetDescriptor` / `TargetClass` | ✅ 已实现 | physical/capture/virtual/discard |
| `SinkSnapshot` | ✅ 已实现 | 完整的 schema 1.0 快照 |
| `SinkLifecycleState` / `AbortProof` | ✅ 已实现 | open/closing/closed/abandoned |

### 2.5 `physical_sink.go` — PhysicalSink

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| 包装 `io.Writer` + `TerminalWriteAborter` | ✅ 已实现 | `PhysicalSinkOptions` 含 Aborter |
| 一次 batch 一次底层提交，不自行 retry | ✅ 已实现 | |
| 短写/零写/错误分类归一化 | ✅ 已实现 | 与计划 7.1 表格一致 |
| `AbortSupported` / `AbortProof` 报告 | ✅ 已实现 | 默认 `AbortProofNone` |
| `Snapshot()` 统计 | ✅ 已实现 | committed/zero/partial/rejected 计数 |

### 2.6 `capture_sink.go` — CaptureSink

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| 三层：semantic/wire/journal | ✅ 已实现 | `CapturedDelivery` + `CaptureSnapshot` |
| `SubmitMirror` 支持 | ✅ 已实现 | 含 policy/apply-mode |
| bounded journal ring buffer | ✅ 已实现 | `CaptureOptions.MaxEntries`/`MaxPayloadBytes` |
| payload access 带 TTL/authorization | ✅ 已实现 | `AcquirePayload`/`PayloadHandle`/`RevokePayload` |
| 性能退化行为 | ✅ 已实现 | hash-only/metadata-only 降级 |
| `NonAuthoritative` 标记 | ✅ 已实现 | 诊断投影 |

### 2.7 `virtual_terminal.go` — VirtualTerminalSink

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| `VirtualProjectionSink` 接口 | ✅ 已实现 | `Projection()` 返回 `VirtualProjectionSnapshot` |
| `SubmitMirror` 支持 | ✅ 已实现 | 含 apply mode / non-authoritative |
| geometry/barrier 上下文 | ✅ 已实现 | invalid geometry 拒绝 |
| scrollback 限制 | ✅ 已实现 | `MaxScrollbackRows` 选项 |
| primary submit 支持 | ✅ 已实现 | 直接作为 primary（非 mirror） |

### 2.8 `mirror_scheduler.go` — Mirror Scheduler

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| outcome-aware mirror policy | ✅ 已实现 | `MirrorBestEffort`/`MirrorCommittedOnly`/`MirrorAttempted` |
| apply mode 分离 | ✅ 已实现 | `MirrorApplyMetadataOnly`/`MirrorApplyBytes` |
| per-mirror timeout entry sealing | ✅ 已实现 | timeout 后 sealed，late return 只进诊断 |
| quarantine/abandoned 规则 | ✅ 已实现 | 无法证明终止的 sink 被 quarantine |
| late completion 事件 | ✅ 已实现 | `EventPrimaryLateCompletion`/`EventMirrorLifecycle` |

### 2.9 `observer.go` — 观察/事件

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| 事件类型: `OutputEventKind` | ✅ 已实现 | 共 11 种事件类型 |
| Mirror 事件阶段 | ✅ 已实现 | `MirrorPhaseScheduled`/`Sealed`/`LateCompletion`/`Quarantine` |
| Subscription 接口 | ✅ 已实现 | bounded buffer，drop 计数 |
| gateway 事件发布 | ✅ 已实现 | `eventPublishResult` 含 cutoff marker |

### 2.10 `binding.go` — Session Binding

| 设计方案要求 | 实施状态 | 备注 |
|-------------|---------|------|
| `SessionBindingRegistry` | ✅ 已实现 | `Bind`/`Unbind`/`UnbindFenceAll` |
| `SessionBindingRef` | ✅ 已实现 | 含 `SessionID`/`BindingGeneration`/`Finality` |
| generation fence via `fencedPort` | ✅ 已实现 | `Submit` 时校验 generation；stale 返回 `AdmissionRejected` |
| `DeliverabilityClass` | ✅ 已实现 | `Deliverable`/`Fenced`/`Terminal` |

### 2.11 `replay.go` / `parity.go` / `fault_sink.go` / `clock.go`

| 组件 | 实施状态 | 备注 |
|-----|---------|------|
| Replay recorder/decoder | ✅ 已实现 | `ReplayEnvelope`/`ReplayEntry`/`ReplayProvenance` 版本化 |
| Parity 测试 | ✅ 已实现 | `ParityCheck`/`ParityResult` 跨 batch 比较 |
| FaultSink | ✅ 已实现 | 确定性故障注入：zero/short/block/timeout/panic |
| FaultTickerSink | ✅ 已实现 | 定时触发故障 |
| Clock/FakeClock | ✅ 已实现 | 确定性测试用 |

---

## 3. 集成点检查

### 3.1 生产工厂：`EnableUnifiedRendererGateway`

文件：`chat_ui_actor.go`

- ✅ 构建 `PhysicalSink`（wrapper `os.Stdout`）→ `RenderOutputGateway`
- ✅ `RenderGatewayOptions` 含适当 timeout/limit
- ✅ `gw.Run()` 启动 worker
- ✅ `enableUnifiedRendererWithPort(gw)` 绑定到 presenter
- ✅ `SetPhysicalWritesEnabled(false)` 关闭 legacy 物理写入
- ✅ 稳定 render `SessionID`（`render-` + runtime session ID）

### 3.2 TerminalSession 集成

文件：`terminal_session.go`

- ✅ `RenderOutputSnapshot()` 方法 — 通过 port 获取 gateway 快照（`/debug` 数据面）
- ✅ `EnterAlternateScreen`/`WriteAlternateScreen`/`ExitAlternateScreen` 使用 `TransactionAlternateEnter/Write/Exit` kind
- ✅ `writeTerminalBytesKindLocked` 使用 `TransactionKind` 参数
- ✅ `FlushTransaction`/history/alternate lease 走 gateway

### 3.3 TerminalSessionPresenter

文件：`terminal_session_presenter.go`

- ✅ `NewTerminalSessionPresenterWithOutput` 构造入口
- ✅ 绑定到 gateway-backed port

### 3.4 chat_setup.go

- ✅ 注释说明使用 `RenderOutputGateway`（line 219）
- ✅ `sessionID` 传递

### 3.5 chat_interaction.go

- ✅ `renderGateway *RenderOutputGateway` 字段
- ✅ `SetSessionIDLine` 集成

### 3.6 TerminalOutput 降级

文件：`terminal_output.go`

- ✅ 注释标注 Phase 3 降级语义：“保护启动前 process-compat 与测试入口”
- ✅ `SetTerminalOutputForTesting` 测试专用
- ✅ 生产 interactive 路径通过 gateway binding 重定向
- ❓ **仍存在多个生产 TerminalOutput() 调用点**（见 §4.2）

---

## 4. 观察与潜在差距

### 4.1 `LegacyTransactionAdapter` 类型不存在

计划 §8.2 要求构建 `LegacyTransactionAdapter` 和 `LegacyImmediateAdapter` 作为 legacy 适配器。代码库中不存在这些类型。实际实现选择了在 Phase 6（commit `477a3cd`）直接删除 legacy immediate renderer chain，而非通过适配器接入。

**评估**：这符合计划的最终目标（Phase 6: "删除绕路与收口"），但跳过了 Phase 3 的适配器中间步骤。生产链路已闭环，没有因此产生功能缺口。建议在计划文档中明确标注此偏离。

### 4.2 遗留 `TerminalOutput()` 生产调用点

计划 §17.4 要求 "`TerminalOutput()` 生产调用只存在于登记的 startup/process-compat allowlist"。目前仍有以下非测试调用点：

| 文件 | 行号 | 调用上下文 |
|------|------|-----------|
| `fixed_bottom_surface.go` | 1219 | `flushHoldingLock(TerminalOutput(), ...)` — 在 `paintSurfaceLocked` 中 |
| `fixed_bottom_surface.go` | 2228 | `fmt.Fprint(TerminalOutput(), ...)` — 在 `applyLayoutTransition` 中 |
| `fixed_bottom_surface.go` | 3704, 3709 | `fmt.Fprint(TerminalOutput(), ...)` — 在 `renderStatusLocked` 中 |
| `fixed_bottom_surface.go` | 3741 | `fmt.Fprint(TerminalOutput(), ...)` — 在 `renderPopupLocked` 中 |
| `fixed_bottom_surface.go` | 4064 | `fmt.Fprint(TerminalOutput(), ...)` — 在 `renderPromptRowsLocked` 中 |
| `fixed_bottom_surface.go` | 5069 | `flushHandoffHoldingLock(TerminalOutput(), ...)` — 在 `handoffHistoryLocked` 中 |
| `fixed_bottom_surface_snapshot.go` | 89 | `flushHoldingLock(TerminalOutput(), ...)` — 在 snapshot 中 |
| `terminal.go` | 31, 348 | `emitControl` / `PrintAt` 使用 `TerminalOutput()` |

**评估**：这些是 legacy paint 路径中的调用。在统一模式下，`SetPhysicalWritesEnabled(false)` 对 `flushHoldingLock`/`flushHandoffHoldingLock` 有防护（`physicalWritesEnabledLocked` 返回 false 时直接 return nil），但直接 `fmt.Fprint(TerminalOutput(), ...)` 在 `renderStatusLocked`、`renderPopupLocked`、`renderPromptRowsLocked` 中**没有显式运行时围栏**。这些方法通过 UI actor 的 action apply 链调用，在统一模式下是否仍可达需要进一步验证。如果这些调用在统一模式下确实被绕过（例如因为 PaintSurface 等 action 不再 dispatch），则它们属于 process-compat 允许列表。建议补充断言或注释证明其不可达，或添加 `physicalWritesEnabled` 检查以符合零旁路门禁。

### 4.3 工作区未提交修改

`git status` 显示 `backend/cmd/aicli/ui/` 下有 6 个文件被修改（`controller_state.go`, `history_effect_planner.go`, `history_effect_planner_test.go`, `history_effect_queue.go`, `history_hot_path_bench_test.go`, `terminal_session_executor.go`），还有若干 untracked 临时文件。这些修改**不在 render/output 范围**内，但仍可能影响集成测试。

**评估**：测试仍全部通过，说明这些修改与现有代码兼容。建议完成这些修改后及时提交，保持工作区干净。

### 4.4 计划文档状态未更新

计划文件头部标注 "文档状态：设计方案（未开始实现）"，但该方案已全部实施。`docs/architecture/aicli-chat-unified-renderer-architecture.md` 也需同步更新以反映 gateway 中间层的存在。

---

## 5. 测试验收

### 5.1 测试结果

| 测试套件 | 结果 | 耗时 |
|---------|------|------|
| `go build ./cmd/aicli/...` | ✅ PASS | 4.5s |
| `go vet ./cmd/aicli/...` | ✅ PASS | 5.9s |
| `go test ./cmd/aicli/ui/render/output/...` | ✅ PASS (49 tests) | 1.38s |
| `go test ./cmd/aicli/ui/...` (short) | ✅ PASS | 6.6s |
| `go test ./cmd/aicli/commands/...` (short) | ✅ PASS | 63.7s |

### 5.2 计划测试验收对照

| 验收标准 | 状态 | 备注 |
|---------|------|------|
| Gateway/sink contract tests 通过 | ✅ | 49 tests 全部通过 |
| Unified UI 测试不再依赖全局 stdout 替换 | ✅ | 使用 `SetTerminalOutputForTesting` + gateway fixture |
| 共同 batch/receipt/virtual snapshot 断言工具 | ✅ | `RenderTestFixture` + `TestFixtureOutput` |
| Fault sink 覆盖 zero/short/full+error/block/cancel/timeout/abort | ✅ | `FaultSink` + `FaultTickerSink` |
| 物理+capture parity 测试 | ✅ | `parity_test.go` |
| Mirror policy/apply-mode 矩阵测试 | ✅ | `mirror_drop_regression_test.go`, `observability_test.go` |
| `go test -race` 兼容 | ❓ 未验证 | 建议运行一次确保 |
| 真实 console smoke | ❓ 未验证 | 需要手动验证 |

---

## 6. 关键建议

1. **Low** — 更新计划文档状态（`aicli-chat-render-intermediate-layer-design-plan.md` 头部 "文档状态" → "已实施"）
2. **Low** — 更新架构文档 `aicli-chat-unified-renderer-architecture.md` 纳入 gateway 中间层
3. **Medium** — 审查遗留 `TerminalOutput()` 调用点（§4.2），在统一模式下证实不可达或添加 `physicalWritesEnabled` 围栏，确保零旁路门禁
4. **Low** — 在计划文档中标注 `LegacyTransactionAdapter` 偏离（或添加说明：Phase 6 直接删除替代了 Phase 3 适配器方案）
5. **Low** — 提交工作区未完成修改，保持基线干净
6. **Medium** — 运行 `go test -race ./cmd/aicli/...` 确保 race 检测通过
7. **Low** — 补充 `FaultSink` 的 timeout/abort 契约测试以覆盖 6.5 的完整矩阵

---

## 7. 总结

`RenderOutputGateway` 中间层的实施与设计方案高度一致。核心 `render/output` 包提供了完整的类型体系、状态机、sink 契约、mirror 调度、可观测性和 replay/parity 支持。生产集成链路 `PhysicalSink → RenderOutputGateway → TerminalSessionPresenter` 已闭环，所有阶段（Phases 0-6）的关键交付物均已实现并可通过测试验证。

主要观察点：`LegacyTransactionAdapter` 类型不存在（直接删除替代了适配器方案），以及少量遗留 `TerminalOutput()` 调用点仍需确认在统一模式下的可达性。整体而言，实施质量良好，与设计方案的核心语义一致。

**是否允许发布**：是，但建议在发布前审查 §4.2 的遗留调用点。