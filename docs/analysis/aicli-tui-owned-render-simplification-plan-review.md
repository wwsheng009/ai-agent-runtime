# aicli TUI 渲染方案完整性评审 + 当前渲染机制缺陷分析

> 评审对象：`docs/plan/aicli-tui-owned-render-simplification-plan.md`（proposed，2026-08-02）
> 对照基线：`E:\projects\ai\codex\codex-rs\tui\src\`（只读参考）
> 评审日期：2026-08-03
> 结论先行：**方案本身是完整的，且对 handoff/scrollback 层的根因判断正确；但它只解决了"重复渲染"的一半——下半层（scrollback 三权分裂），没有触及上半层（coordinator 双路径、五份文本副本、四个调度器、无统一 Scene），因此按方案落地后，用户观察到的"历史消息重复渲染"与"逻辑过于复杂、无统一状态管理"问题仍会残留。**

---

## 1. 方案完整性检查

### 1.1 逐节核对

| 方案章节 | 内容 | 完整性判定 |
|---|---|---|
| §0 结论摘要 | 双物理所有者 + 单调提交器 | ✅ 清晰 |
| §1 现状基线 | 两个失败测试 + 字节级诊断（`line-17 ×3`、`line-24..35 ×4`）+ 代码地图 | ✅ 实测证据扎实，行号抽查命中 |
| §2 复杂度根源 | 同一历史行三份所有权（historyWindow / native scrollback / ScreenModel front-back）+ 补偿链 + headroom 批判 | ✅ 根因判断正确，与代码实读一致 |
| §3 Codex 对照 | insert_history / viewport_area / resize_reflow 三点 | ✅ 方向正确，但对照偏窄（只对照了 scrollback 层，未对照事件流、调度、状态组织，见 §3.4） |
| §4 测试语义 | 字节计数 vs 语义行 ID 记账；阶段 0 先行 | ✅ 这是方案最有价值的部分，切中要害 |
| §5 目标架构 | 两物理所有者 + AppendNewRows/ScrollExistingRows + Frame→Scrollback 单调状态机 + 不变量 S1-S6 | ✅ 逻辑自洽 |
| §6 改动清单 | D1-D9 删除、N1-N6 新增、收缩项 | ✅ 与代码符号对应 |
| §7 迁移顺序 | 阶段 0-5，每阶段独立提交 | ✅ 可行 |
| §8 验证与风险 | CI 补强、ConPTY 明确不做、R1-R6 | ✅ 务实（ConPTY 结论有实测依据） |
| §9 复核记录 | 两次自我复核 + 三处实质修订 | ✅ 体现了审慎，但**没有外部视角**（见 §3.5） |

### 1.2 方案"完整"但"范围不够"——四层问题只覆盖了一层

方案完整地回答了它自己提出的问题（"owned viewport 能否简化"），但它把问题域限定在了 **scrollback handoff 层**。用代码实测把整个渲染栈展开后，实际有四层问题：

```
L4 事件层     input / SSE output / render intent / lease 事件 —— 无统一事件流
L3 协调层     chatInteractionCoordinator：streamBuffer、stableCommitQueue、transcript —— 五份文本副本
L2 渲染层     FixedBottomSurface：3 条渲染路径 + 3 份行所有权 + 补偿链   ← 方案只覆盖这里
L1 数据层     scene.TuiScene 已存在但未接线；historyWindow 仍是 []string
```

方案对 L2 的诊断和处方是准确的；对 L1（Scene 接线）、L3（coordinator 双路径）、L4（事件流统一）**只字未提**。而这正是用户报告"历史消息重复渲染"和"没有统一状态管理"的发生层。

---

## 2. 当前渲染机制全貌（代码实测）

### 2.1 一条助手消息的五份文本副本

以一次 assistant 流式输出为例，同一段文本同时存在于：

| # | 载体 | 位置 | 生命周期 |
|---|---|---|---|
| 1 | `coordinator.streamBuffer` | `chat_interaction.go` | 整个 turn |
| 2 | `ActiveStreamController` 内部 BufferBackend | `ui/active_stream.go`（30 FPS 合并绘制） | turn 期间 |
| 3 | `FixedBottomSurface.historyWindow []string` | `fixed_bottom_surface.go:1334` | 400 行上限 + trim |
| 4 | `assistantTurnTranscript.Blocks` | `chat_interaction_transcript.go` | 持久化/replay |
| 5 | 终端 native scrollback | `insertHistoryLinesLocked` 写入 | 不可逆 |

五份副本由 **三套账本** 对齐：`handoffFrontier`（行级）、`streamEnqueuedPrefixLen`/`streamRenderedPrefixLen`（字符级）、`softOutput`（行级可重写尾）。任何一页账本漂移，就产生重复或丢失——这正是"历史消息重复渲染"的机制性来源，且**不在方案 D1-D9 的删除清单里**。

### 2.2 三条渲染路径并存

| 路径 | 入口 | 机制 | 问题 |
|---|---|---|---|
| A. 全帧 diff | `renderOwnedViewportLocked`（snapshot.go:73） | commitExcess → Invalidate → stageOwnedFrame → Flush | 每帧把**全部 historyWindow** 物化为 cell（`composedPlanLocked` → `historyRowsWithCursorBlankLocked`），流式输出下每 token 一次 O(visible) 全量组帧 |
| B. 直写滚动 | `appendOwnedDirectPaintLocked`（:962） | `historyWindow[frontier:]` → `insertHistoryLinesInRegionLocked` → **stageOwnedFrameLocked 后又 CommitRange(1, regionBottom)** 静默提交已滚行 → Flush 只发底部带 delta | 先全量组帧再"假装已画"，diff 与终端状态靠 CommitRange 掩盖；`line-17 ×3` 的第二个来源（@1749 全帧 diff 重绘）就是 front 未同步滚动导致的 |
| C. 补偿滚动 | `commitExcessHistoryToScrollbackLocked`（:4223） | `keepForRestore = visible + 40` 双保留 → DECSTBM `\r\n` **重写行文本** → `AdvanceTo` → 调用方 Invalidate | 几何变化被实现成文本提交；`line-17 ×3` 的一、三来源（@756/@3354）都在这里 |

三条路径共享同一个 `insertHistoryLinesInRegionLocked` 字节构造点（INV-SCROLL-01 成立），但**调用点语义不同**（A 是"重建"，B 是"追加+掩盖"，C 是"补偿"），导致同一行在字节流里出现 3-6 次。

### 2.3 状态爆炸：一个 129.5 KB 的巨物

`fixed_bottom_surface.go`（129.5 KB，不含测试）承担了：历史窗口、handoff 账本、soft output、legacy 补偿状态机（`legacyReserve.CursorOnBlankRow` 等）、popup、prompt（7 个字段）、status、ActiveBand、ScreenLease（DEC 1049）、终端 write lock 转发。结构字段约 40+ 个，其中**互相咬合的补偿/账本状态**至少 9 组：

- `historyWindow` / `historyPartial` / `handoffFrontier`（TrimPrefix/Clamp/AdvanceTo）
- `softOutput`（可重写尾）+ `historyWindowHeadroom=40`
- `legacyReserve`（CursorOnBlankRow 等遗留）
- `streamEnqueuedPrefixLen` / `streamRenderedPrefixLen`（coordinator 侧字符账本）
- `stableCommitQueue`（catch-up 队列）
- `ownedFrameFlushCount` / `lastGeometryProbeAt`（观测/探测）

方案 §2 说"三份所有权"只统计了 L2 层；加上 L3 层实际是**六份所有权**（五份副本 + 账本）。

### 2.4 调度器分裂：四个渲染时钟

| 调度器 | 位置 | 触发 |
|---|---|---|
| `FramePump`（FrameKeyActiveFrame/DynamicStatus/Prompt/StableCommit 四个 key） | renderengine | 活跃帧、动态状态、提示符、稳定提交各自独立调度 |
| `ActiveStreamController` 30 FPS 合并 | active_stream.go | 动画/流式自身节奏 |
| `stableCommitQueue` drain + catch-up | chat_interaction.go | 稳定前缀落 scrollback 的追赶队列 |
| 几何探测 `maybeRefreshStreamGeometryLocked` | chat_interaction.go | 挂在 paint 路径里，节流探测 + 软 reflow |

四个时钟各自决定"何时画什么"，没有单一帧权威。Codex 只有一个 `FrameRequester` + 一个 `TuiEventStream`（input 与 draw 事件合并成一条流），帧请求与输入在**同一事件循环**里仲裁。

### 2.5 屏幕管理与写入

已做对的部分（值得肯定）：

- **单一物理 writer**：`renderengine.WithTerminalWriteLock`（terminal_lock.go）全局串行化 + Presenter 批量 flush——这是全局门禁，方向正确；
- **ScreenLease**：DEC 1049 进入/退出与主屏挂起/恢复在同一所有权事务内（screen_lease.go），是母计划 §11 的正确落地；
- **DEC 2026 synchronized framing** 已有开关。

未做对的部分：

- `FixedBottomSurface` 同时是"模型 + 布局 + 屏幕管理 + writer 门禁 + 全屏 lease"，职责未分离；
- **光标状态没有模型化**：Codex `custom_terminal.rs` 有 `last_known_cursor_pos` 随每次 flush 更新；aicli 每次 `writeOutput` 都做 `cursorHide → move → 写文本 → cursorShow`（fixed_bottom_surface.go:784-788），并用 `storedPromptCursor` 反复保存/恢复——光标是"操作序列"而不是"状态"，任何绕过路径都会漂移。

### 2.6 "重复渲染探测器"本身就是病症证据

`fixed_bottom_surface_snapshot.go` 的 `/debug` 逐行标签机制（`[3f9a #05 w0]`：内容指纹 + 白重绘计数 + 最近帧星标，`annotateDebugRowsLocked`）——为"发现重复渲染"专门建了一套内容寻址的观测仪器（`WhiteEmitsByHash`、`PaintTrace`）。这是一个强信号：**团队已经承认重复渲染是常态，只能靠探测器去数，而不是从结构上消除**。方案 D5/D6（去 Invalidate、去 CommitRange）方向正确，但只去掉 L2 层的补偿。

---

## 3. Codex 对照（实测，非转述）

### 3.1 Codex 的分层

| 层 | Codex 实现 | aicli 现状 |
|---|---|---|
| 语义源 | `transcript_cells`/`HistoryCell`（write-once，resize 重放的唯一源） | `historyWindow []string`（可变、trim、无身份）→ scene.TuiScene 存在但未接线 |
| 可见区 | `custom_terminal.rs: viewport_area`，**终端 buffer 尺寸 = viewport 尺寸**；ratatui `Buffer` diff 渲染（diff_render.rs） | 全屏双缓冲 + 窗口指针（outputBottom/activeBand），尺寸与 viewport 脱钩 |
| scrollback 写入 | `insert_history.rs`：`SetScrollRegion → MoveTo → \r\n + 行 → ResetScrollRegion → MoveTo`，**写完即忘，应用不再持有副本** | 文本滚动 + 帧重绘共享字节流，靠 `HandoffFrontier` 账本分割 + headroom 双保留 |
| resize | `tui.rs: draw_with_resize_reflow` + `clear_for_viewport_change`：**显式 clear + 从源重放**，行数 cap 按终端配置（resize_reflow_cap.rs：VS Code 1000 / Windows Terminal 9001 / WezTerm 3500 / Alacritty 10000） | 增量修补 + 补偿 + soft reflow；"重发文本"被当成 bug |
| 事件 | `TuiEventStream`：**crossterm input 与 draw 事件合并为一条流**，`FrameRequester` 唯一帧请求源 | 4 个 FrameKey + 30FPS + status tick + stable queue + 几何探测 |
| 状态组织 | `app.rs`（57KB）+ `chatwidget.rs`（80.7KB）+ `exec_cell/` `history_cell/` 按职责拆包，cell 有稳定身份 | `chat_interaction.go` 181.9KB + `fixed_bottom_surface.go` 129.5KB 两个巨物，行无身份 |

### 3.2 Codex 的"重复"观

Codex 的 resize 重建同样会重发相同文本——方案 §3.3/§4.1 对此的判断完全正确：**"重发"不是 bug，没有权威源才是**。Codex 之所以不重复，不是因为它不做 diff，而是因为：

1. **源唯一且 write-once**（transcript_cells 不删不改）；
2. **scrollback 与 viewport 是两个物理通道**（insert_history 的字节 vs ratatui diff 的字节互不相交）；
3. **resize 是全量重建，不做增量补偿**——重建的代价是已知的、可封顶的（cap），补偿的代价是未知的、组合爆炸的。

### 3.3 aicli 已经借鉴到的东西

- `insertHistoryLinesLocked` 的 DECSTBM 字节构造已与 Codex insert_history 对齐（注释自述 "Codex-aligned DECSTBM path"）；
- `vt.Screen` 是 "byte stream → rows" 重建器，与 Codex 的测试侧一致；
- 方案 §3.2 表格里的差距判断（buffer=viewport、write-once、显式 clear+replay）全部成立。

### 3.4 方案 §3 对照的盲区

方案只对照了 scrollback/visible-area 机制，**没有对照**：

1. **事件流组织**（TuiEventStream 单一流 vs aicli 四时钟）；
2. **状态组织**（Codex 的 cell 身份 + 按职责分包 vs aicli 双巨物 + 行无身份）；
3. **光标管理**（last_known_cursor_pos vs 每次 hide/show + restore）；
4. **调度仲裁**（FrameRequester + draw 事件 vs 四个独立 key）。

这四点是用户提问"组件协调性、事件流管理、设计范式"的核心答案所在，方案缺席。

---

## 4. 方案本身的缺口清单（在 §1.2 基础上细化）

| # | 缺口 | 影响 | 建议 |
|---|---|---|---|
| G1 | **Scene 接线悬空**：目标架构的 `mutableRows` 仍是 `[]string`，与母计划 P4-P9 的 `scene.TuiScene`（已实现、有事务/不变量/测试）没有建立关系。若按方案落地，P4-P9 终局还要把 mutableRows 换成 cell——二次返工 | 方案与母计划脱节，可能重复投资 | 阶段 0 增加"Scene 接线决策"：明确 mutableRows 是临时中间态还是终局（建议：`RenderText(cells)` 已经能投影出行，可直接作为 Scrollback mode 的提交源，见 §6） |
| G2 | **coordinator 双路径未涉及**：`commitActiveStableScrollbackLocked`（live band 稳定前缀 → WriteSoftTrackedOutput）+ 历史 replay（`Formatter.Format`）双渲染调用点，`streamEnqueuedPrefixLen` 账本——assistant 文本的 L3 层重复源不在方案内 | 用户报告"历史消息重复渲染"的**主要**来源之一 | 方案增加 §L3：以 `Formatter.FormatDocument` 为唯一结构化真源，band 只做差异化；删除字符级账本，改由 Scene cell 的 Revision 对齐 |
| G3 | **四时钟调度未统一** | 组件协调性问题（用户明确点名） | 收敛到单一 FramePump + 统一事件队列（见 §6） |
| G4 | **光标无模型** | 每次写 hide/show + restore，绕过路径漂移 | ScreenModel 增加 cursor 状态；flush 末尾更新，同 Codex |
| G5 | **legacy 路径删除无日程**：`ownedViewport=false` 分支（applyLayoutLocked + scrollCompensatedRows 等）仍在；方案 D8 只删"owned path 残留"，未安排 legacy 删除 | 双路径并存 = 双倍回归面 | 母计划 P8-P9 已列，方案应显式声明依赖 |
| G6 | **性能上限未量化**：`composedPlanLocked` 每帧全量物化 + `stageOwnedFrameLocked` 每 token 一次；方案 R5 只谈 flush 批量 | 流式 30FPS 下 CPU/GC 压力 | 增量组帧（只重画 dirty cell）或至少 benchmark 门禁 |
| G7 | **测试语义改造只覆盖 scrollback 层**：阶段 0 只改两个失败测试；`chat_interaction_*` 系列（live vs replay blank、midstream blank 等 25KB+ 测试）仍以字节断言为主，未按 §4.2 语义重建 | 上层的"语义行不重复"无测量设施 | 阶段 0 一并引入"语义行 ID 记账"通用断言工具，供 L3 层测试使用 |
| G8 | **观测层缺权威指标**：`/debug` 已展示 frontier/headroom/白重绘计数，方案 §6.3 只改展示字段，未加"进程级重复行计数"（每行文本进入 scrollback 的次数）作为 CI 可断言指标 | 无法自动回归 | 把 INV-S1 做成计数器暴露，vt.Screen 回放断言 |

---

## 5. 设计范式建议

### 5.1 范式选择：retained-mode 语义源 + 单调提交 + diff 呈现（即 Codex 范式）

不建议走 ncurses 式 immediate-mode 全重绘，也不建议纯 terminal-native 无缓冲。对"底部固定带 + 上方原生 scrollback"形态，正确范式是：

```
              ┌──────────────┐   Event（input / SSE / timer / lease）
              │  App/Coord   │───────────────┐
              └──────────────┘               ▼
   ┌────────────┐  Transaction   ┌──────────────────────┐
   │ Scene(cells)│◄──────────────│  SceneController      │
   │ write-once  │               │  Validate→Commit→snap │
   └─────┬───────┘               └──────────────────────┘
         │ snapshot（不可变）
         ▼
   ┌──────────────────┐   width/theme   ┌──────────────────┐
   │ Layout/Compose   │───────────────►│ ScreenModel       │
   │（纯函数，可重放）  │                │ front/back（唯一   │
   └──────────────────┘                │ 渲染所有者）       │
                                       └────────┬─────────┘
                                                │ diff
        ┌───────────────────────────────────────▼──────────┐
        │ Presenter（唯一 writer：terminal lock + batch）    │
        │  · 可见区变化 → diff flush                         │
        │  · 超屏提交 → AppendNewRows（唯一 scrollback 入口） │
        │  · 几何变化 → ScrollExistingRows（纯平移）          │
        └──────────────────────────────────────────────────┘
```

关键设计决策（每条都有 Codex 实测背书）：

1. **一个真相源**：Scene cell（已实现）是唯一可变状态；行文本、间距、handoff 边界全部由它派生。`[]string` 只存在于投影结果中，不参与状态。
2. **两个物理通道，互不相交**：scrollback 字节（insert_history 一次提交）与 viewport diff 字节（ScreenModel）永不相交——重复渲染在结构上不可能发生，而不是靠探测器数出来。
3. **一个屏幕所有者**：ScreenModel 尺寸 = 可见区（含 bottom pane），滚动 = 模型内平移；`ScrollRegionUp/Down` 已实现（screen_model.go:122-147，方案 N1 已存在！——方案说 "ScreenModel 现有 API 无 ScrollUp/ScrollDown，N1 为真实新增" 不准确，scroll 方法 2026-08 已实现，只有 `ApplyRegionAppend` 之后的语义对齐需要核对）。
4. **一个事件循环**：input + draw 请求 + SSE 输出 + lease 事件合并为一条流（仿 `TuiEventStream`），单一 reducer → 事务 → 快照 → 调度帧。四个 FrameKey 只是 dirty 分类，不再是四个独立时钟。
5. **resize = clear + replay（可封顶）**：接受重发文本，用 cap 控制代价；不做增量补偿。
6. **光标是状态**：flush 后记录 `lastCursorPos`，任何写路径先对齐光标再写。

### 5.2 不建议的做法（避免踩坑）

- ❌ 继续给 `FixedBottomSurface` 加字段/账本（headroom、frontier、reserve debt 都是这条路线的产物）；
- ❌ 用字节计数断言"无重复"作为通过条件（方案 §4 已论证，同意）；
- ❌ 在 L3 层再做一套"稳定前缀落盘账本"（`streamEnqueuedPrefixLen` 已经是第三套账本了）；
- ❌ ConPTY 自动化（方案 §8.1 结论有实测依据，同意不做）。

---

## 6. 建议的收敛路径（对方案 §7 的修订）

| 阶段 | 方案原计划 | 建议修订 |
|---|---|---|
| 0 | 测试语义改造（vt.Screen scrollback 记录 + 语义行 ID） | 保留，并把"语义行 ID 记账"工具扩展到 coordinator 层测试；**新增 G8 计数器** |
| 1 | 几何变化去文本提交（ScrollExistingRows） | 保留（这是对的） |
| 2 | 单调提交器 AppendNewRows | 保留；**同时把 Scene 的 `RenderText(cells)` 作为提交源**（G1：避免 mutableRows 二次返工）——若时间不够，则显式声明"mutableRows 是临时态，P4-P9 前替换" |
| 3 | 删双保留与 trim | 保留 |
| 4 | 状态机收敛 | 保留 |
| 5 | 旧路径删除 | 保留；**新增 L3 收敛**：删除 `streamEnqueuedPrefixLen/streamRenderedPrefixLen` 字符账本，改由 Scene cell Revision 对齐；删除双渲染调用点（band 与 scrollback 共用 `FormatDocument`）；**新增 L4 收敛**：FrameKey 收敛为单一调度权威 |

若认为 L3/L4 超出"渲染简化"范围，至少应在方案 §0 增加"范围声明"，明确这两层由母计划 P4-P9 承接并给出承接点——现状是两层都不在任何一份文档的近期阶段里，会继续烂下去。

---

## 7. 一句话总结

方案对"scrollback 三权分裂导致重复渲染"的诊断和处方是**正确的、可执行的、测试语义改造尤其出色**；但它是"补丁级简化"，不是"架构级简化"——真正的复杂度在 L1（Scene 未接线）、L3（coordinator 五副本 + 三套账本 + 双渲染调用点）、L4（四时钟 + 无统一事件流）。**建议按 §6 修订后批准，并同步在母计划中把 L3/L4 收敛排入近期阶段**，否则用户可见的"历史消息重复渲染"和"组件协调失控"不会消失。
