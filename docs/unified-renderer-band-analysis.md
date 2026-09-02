# aicli chat 统一渲染器：Active Band 丢失问题分析

> 目标：梳理「统一界面渲染器」的完整处理逻辑，定位「active band 内容有时不进入统一渲染器、重启后恢复」这一间歇性 bug 的根因候选，并对照最近变更提交。
> 范围：`backend/cmd/aicli`（`chat_ui_actor.go` / `chat_runtime_events.go` / `chat_interaction.go` / `ui/controller*.go` / `ui/app_layout.go` / `ui/app_screen_layout.go` / `ui/controller_state.go`）。

---

## 1. 统一渲染器的整体链路（梳理）

### 1.1 数据流总览

```
Bridge worker（chat_core.go 流回调 / chat_runtime_events.go）
  │  1) RenderAssistantDelta(delta)   ← 实时 facade 旁路（bridge worker goroutine，非 reducer）
  │  2) RuntimeEvent 投递（带 epoch 的桥接 action）
  ▼
UIController mailbox（单 reducer goroutine Run() 消费，durable/causal/coalescable 三类）
  │  applyRuntimeEventActionWithContext
  ▼
Scene（语义 transcript 数据平面）
  │  encodeRenderModelEvent → applyChangeSet（创建/追加 mutable cell）
  │  sceneSnapshot() → 不可变快照
  ▼
AppState（reducer 内的不可变状态）
  │  ReplaceTranscriptAction   → 全量/active-only 重建 transcript + reconcileTranscriptActiveCell
  │  UpdateActiveCellAction    → 增量更新 state.Active.Source（band 内容来源）
  │  SetSemanticActiveCellProjectionAction → 启用「语义 active cell 投影」开关
  ▼
LayoutAppScreen / LayoutAppState（纯函数，无 I/O）
  │  activeBand = ProjectActiveCellBandWithTheme(state.Active, ...)   ← unified 模式 band 唯一来源
  ▼
FlushEffect → TerminalSessionPresenter → RenderOutputGateway
  ▼
PhysicalSink（console primary） + FileSink mirror（--render-output-file, 仅 committed 字节）
```

### 1.2 Active band 的两条候选来源（Layout 决定）

`app_layout.go` 中 band 投影逻辑：

```go
activeBand := ProjectActiveCellBandWithTheme(state.Active, state.Geometry, state.Theme)
legacyBand := !state.SemanticActiveCellProjection && hasLegacyActiveBand(state.Bottom)
```

- **语义路径（unified）**：band 内容 100% 来自 `state.Active`（Scene 驱动的语义 active cell）。
- **legacy 路径**：只有 `SemanticActiveCellProjection == false` 时才可能使用 `state.Bottom.ActiveBandLines`（facade 写入的 legacy band）。

结论：**只要 unified 渲染器生效（投影开关为 true），active band 的内容只可能来自 `state.Active`。** 因此「band 内容不进入统一渲染器」等价于「Scene → AppState 的 active cell 镜像没有挂载/更新成功」。

### 1.3 `state.Active` 的生命周期

| 阶段 | 触发 | 机制 |
|---|---|---|
| 挂载 | 首个流 chunk 的 RuntimeEvent | reducer 内 `bridge.handleEvent` → Scene 出现新 mutable cell → `runtimeEventNeedsTranscriptReplace` 判 true（当前 `CellID==0`）→ `postCausalUIActionWithContext(ReplaceTranscriptAction)` → reducer `NewTranscriptState(snapshot)`（含 live mutable cell）→ `reconcileTranscriptActiveCell` 挂载 |
| 更新 | 后续 chunk 的 `RenderAssistantDelta`（**bridge worker 直接调用**） | `unifiedSceneActiveCellActionLocked()`：Scene 快照 vs `actor.ActiveCellState()`，CellID/Kind 匹配且 Source 有变化 → `UpdateActiveCellAction` → `postCausalUIAction`（非 reducer goroutine：`PostFollowup` 必 false → 回退 `actor.Post`，返回值 `_ =` 丢弃） |
| 提交 | 消息边界 commit | `commitHistoryCellLocked` → `postTranscriptSnapshotFromBridge` → transcript 同步 |

### 1.4 关键防线 / 竞态窗口

1. **mount-once gate**（`unifiedSceneActiveCellActionLocked`）：`current.CellID == 0 || CellID/Kind 不匹配` → 返回 nil，把挂载留给 ReplaceTranscriptAction。任何 CellID 为 0 期间到达的 Update 都会被 gate 掉。
2. **`transcriptSnapshotAlreadyInstalled`**（`controller_state.go:762`）：`SceneID + Revision + ContentVersion` 三者全等 → 直接 `break` 跳过 ReplaceTranscript（防重复全量重建）。注意它**只比版本号，不比快照里的 active cell 内容**。
3. **`PostFollowup` 语义**（`controller.go:373`）：仅 reducer goroutine 返回 true；外部 goroutine 收到 false，必须改用 `Post`。`postCausalUIAction` 的失败路径静默回退。
4. **初始化时序**（`enableUnifiedRendererWithPort`）：
   - `c.unifiedRenderer = true`（先置位）
   - `surface.SetPhysicalWritesEnabled(false)`（fence legacy surface）
   - `ensureUIActor()` → `go uiActor.Run()`
   - `Post(SetThemeContextAction)` + `Post(SetSemanticActiveCellProjectionAction{Enabled:true})`（任一失败 → unifiedRenderer 回退 false）
   - `actor.WaitIdle()`（保证上述 action 已消费）
   - `SetPrimaryPresenter(presenter)`
5. **初始化与 `RenderAssistantDelta` 的并发**：`RenderAssistantDelta` 是 coordinator 的公开方法，由 bridge worker（chat_core.go 流回调）直接调用，与 reducer 并发。它会读 `c.unifiedRenderer` 并走 `unifiedSceneActiveCellActionLocked()`。

---

## 2. 最近变更提交检查

```
9d9e115 fix(commands): enable SyncEveryWrite for --render-output-file mirror
72cd1a0 feat(commands): wire FileSink mirror via --render-output-file for interactive chat
47b6df2 feat(ui): implement FileSink for terminal output to text file
7a34f61 fix(ui): keep plain viewport flushes alive during success-mode recovery backoff
8ae68e9 feat(ui): derived loop-health diagnosis + window counters + executor pprof
9cc1b7a fix(ui): break terminal session scrollback recovery loop + executor diag pprof
2b48261 refactor(ui): remove render*Locked legacy bodies and migrate 33 contract tests to owned mode
237e234 fix(ui): fence all legacy TerminalOutput paths in FixedBottomSurface
035d40a fix(aicli): route command output through CommandTextWriter ...
d3beec5 feat(aicli): wire render output gateway in production chat setup
a71c18e feat(aicli): observability event publishing and mirror drop finality
79d8d07 / f1a4569 / d6c34a5：abandoned render gateway / reconfigure watchdog 生命周期
```

- **FileSink 镜像（47b6df2 / 72cd1a0 / 9d9e115）**：`--render-output-file` 新增 committed-only mirror，`MirrorCommittedOnly` + `SyncEveryWrite`。与 band 内容无直接耦合（只镜像 committed wire 字节），但若 mirror 阻塞（3s Timeout）不应影响 primary；需确认镜像 Apply 失败不会级联回 primary 提交路径。
- **legacy fence（237e234 / 2b48261）**：把 legacy `TerminalOutput` 路径在 `FixedBottomSurface` 全部 fence。这与 band 直接相关——unified 模式下 legacy band 被故意禁用，若某条内容仍走 legacy 通道（而不是 Scene），就会「不进统一渲染器」。**这是最可疑的变更区域之一。**
- **d3beec5（gateway 上生产）+ 网关生命周期（79d8d07 / f1a4569）**：渲染网关的 abandoned/reconfigure 逻辑与 presenter 切换相关；`chat_runtime_events_presenter_switch_test.go` 覆盖。
- **9cc1b7a / 7a34f61（scrollback 恢复 / 成功态 backoff）**：viewport flush 与恢复路径，可能影响「重启后恢复」的观感，但与 band 数据源无直接关系。

---

## 3. 根因假设（按可能性排序）

### H1（最可能）：Scene→AppState 挂载在初始化竞态窗口内失败，且 `state.Active` 停留在 `CellID==0`
统一渲染器下 band 唯一来源是 `state.Active`。若首个 chunk 的 `ReplaceTranscriptAction` 未生效：
- `state.Active.CellID == 0`；
- 之后所有 `UpdateActiveCellAction` 被 **mount-once gate** 拒绝（`current.CellID == 0 → nil`）；
- 若 `runtimeEventNeedsTranscriptReplace` 的补救 ReplaceTranscript 同样未投递（如 causal followup 丢失 / 快照被 `transcriptSnapshotAlreadyInstalled` 跳过），则整段会话 band 持续缺失；
- 重启后 Scene/transcript 从干净状态重建 → 恢复。**与「重启恢复」症状吻合。**

触发窗口：
- `c.unifiedRenderer = true` 在 `SetSemanticActiveCellProjectionAction` 投递/生效之前就置位；
- bridge worker 的 `RenderAssistantDelta`（实时 facade 旁路）在 `WaitIdle()` 完成前触发时，会在投影 flag 未生效时走 `unifiedSceneActiveCellActionLocked()`，基于当时的 `actor.ActiveCellState()` 做出错误的挂载判断；
- `postCausalUIAction` 的返回值在 `RenderAssistantDelta` 的 defer 中被 `_ =` 丢弃，失败无观测。

### H2：`transcriptSnapshotAlreadyInstalled` 误判跳过 + Update 被 gate
`transcriptSnapshotAlreadyInstalled` 只比较 `SceneID/Revision/ContentVersion` 三个版本号，不比较快照中的 active cell。若 Scene 复用同一版本号（如 resume/backtrack 后 revision 未推进）而内容实际变化，ReplaceTranscript 被跳过、`state.Active` 停留在旧 cell/空 cell，后续 Update 又因 CellID 不匹配被 gate。代码注释已声明用 SceneID 防 coincident revision，但 **unified + active-only 快照路径下仍缺一道「active cell 内容一致性」校验**。

### H3：causal followup / 非 reducer goroutine 投递静默丢失
`RenderAssistantDelta` 在 bridge worker goroutine 调 `postCausalUIAction` → `PostFollowup` 必然返回 false（controller 只信任 reducer goroutine）→ 回退 `actor.Post`。若 actor 恰好 closed/为 nil，`_ =` 丢弃且无打点。流式高峰期 mailbox 满时 `Post` 也可能合并/丢弃 coalescable 的 `UpdateActiveCellAction`，一旦与 ReplaceTranscript 的顺序竞态叠加，band 更新即丢失。

### H4：legacy fence 后的内容仍走 legacy 通道
`237e234 / 2b48261` 全面 fence legacy `TerminalOutput` 后，若某类内容（tool 事件、渲染旁路等）仍写入 `state.Bottom.ActiveBandLines` 或 legacy facade，而 `SemanticActiveCellProjection` 已为 true，则 Layout 只认 `state.Active`，legacy 内容被「静默丢弃」。需核对 `renderToolChainEvent`、`SetActiveBandAction`、FramePump 回调是否全部改走 Scene/`UpdateActiveCellAction`。

---

## 4. 建议的验证与修复方向

1. **加观测（最小改动、立刻可复现定位）**：在以下位置打点（计数 + 首错日志）：
   - `runtimeEventNeedsTranscriptReplace` 的返回分支；
   - `transcriptSnapshotAlreadyInstalled` 命中跳过的分支（记录 snapshot 与 current 版本号、active cell 的 CellID/Source 前缀）；
   - `unifiedSceneActiveCellActionLocked` 的 mount-once gate 拒绝分支；
   - `postCausalUIAction` / `postCausalUIActionWithContext` 的失败/回退分支（不再 `_ =` 丢弃）。
2. **修复 H1/H2**：`transcriptSnapshotAlreadyInstalled` 在 `SemanticActiveCellProjection` 下增加「快照 active cell 与已挂载 active cell 一致性」校验（CellID + Source 前缀），避免版本号误判导致挂载/更新被跳过。
3. **修复 H3**：`postCausalUIAction` 失败不再静默；非 reducer goroutine 的 Update 投递失败时记录并重试为直接 `Post`，避免 band 更新丢失。
4. **修复 H4**：审计 legacy fence 后仍写入 `Bottom.ActiveBandLines` / legacy facade 的所有路径（tool 事件、FramePump、`SetActiveBandAction`），确保 unified 下全部改走 Scene。
5. **初始化加固**：将 `c.unifiedRenderer = true` 的置位推迟到 `SetSemanticActiveCellProjectionAction` 生效之后（或在 `WaitIdle()` 完成前禁止任何 `RenderAssistantDelta` 进入 `unifiedSceneActiveCellActionLocked`）。
