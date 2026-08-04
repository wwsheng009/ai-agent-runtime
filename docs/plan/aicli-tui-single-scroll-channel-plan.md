# aicli TUI 单一滚动通道与屏幕/历史解耦专项方案

状态：**proposed（待评审；方案 B 可在已修复的重复渲染 bug 之上直接实施，方案 A 作为后续独立重构）**

日期：2026-08-02

## 0. 文档定位

本文针对 aicli TUI 交互终端（`backend/cmd/aicli/ui`，核心文件 `fixed_bottom_surface.go`）的**重复渲染**与**未超屏内容也进入历史消息**两个现象，给出根因分析与两层架构优化方案。

关联文档：

- 长期架构母计划：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`（本文的 handoff 不变量与"history window 多职责"结论沿用其定义，本文是其在"滚动通道与屏幕/历史边界"方向的专项细化）；
- owned viewport 当前实施真相：`docs/plan/aicli-tui-p5-owned-viewport-design.md`；
- ActiveBand/scroll compensation 专项历史：`docs/plan/aicli-activeband-scrollback-compensation-blank-lines-fix-plan.md`；
- 渲染引擎模块设计：`docs/plan/aicli-tui-render-engine-module-design.md`。
- primary/alternate RendererMode、transcript pager 与 history handoff 实施：`docs/plan/aicli-tui-transcript-overlay-renderer-mode-plan.md`。

> 本文不否定已有 P5 实施与已修复的重复 bug（`appendOwnedDirectPaintLocked` 尊重 `handoffFrontier` 的补丁）。方案 B 是对该补丁的**正规化固化**——把"直写必须尊重账本"的隐式约定变成"只有一个滚动通道"的结构保证；方案 A 则是消除账本本身。

---

## 1. 背景与问题现象

### 1.1 现象

1. **重复渲染**：单次大写入（超过一屏可见区）后继续流式追加、随后 `ShowPrompt` 收缩可见区，屏幕底部会再次出现此前已经滚动过的行（诊断中 line-38..41 在 line-60 之后重复出现）。已修复并被测试锁定（`fixed_bottom_surface_overflow_render_test.go`）。
2. **未超屏内容也进入历史**：写入内容哪怕只有几行、远未超过屏幕，也会被追加进 `historyWindow` 并参与"历史消息"的记账与绘制；屏幕可见区与历史消息由同一份数据、同一条写入管线驱动，表现为"当前屏幕与历史消息同步绘制"。
3. **窄终端 wrapped 直写错位风险**：直写路径的滚动计数按逻辑行计算，而终端实际滚动按物理行（wrap 展开后），存在内容正确但位置偏移的风险（尚未根治）。

### 1.2 为什么"未超屏也进历史"是问题

`historyWindow` 不是"仅当超过一屏才累积的历史"，而是**每次写入的必经之地**：写入 → `appendHistoryWindowLocked` → （超屏）直写 / （未超屏）全帧重绘。可见区 = `historyWindow` 的尾部（`composedPlanLocked` 直接取 `historyRowsWithCursorBlankLocked()` 组帧），历史 = 同一数组的前段。

后果：

- 屏幕帧的构建依赖历史数组的**当前内容**（而非"可见区快照"），任何历史操作（trim、soft rewrite、reflow、handoff）都会同时影响屏幕；
- "历史消息"与"屏幕"无法独立演进，逻辑上应该属于两层的数据被耦合在一个可变数组 + 一个边界指针里。

---
## 2. 根因分析

### 2.1 直接原因：两条独立滚动通道

终端滚动（DECSTBM 区域内的 `\r\n`）由两个互不知晓的通道发出：

- **通道 A（历史滚动）**：`commitExcessHistoryToScrollbackLocked → insertHistoryLinesLocked`——把 `historyWindow` 中超出可见区的行滚入 native scrollback，并推进 `handoffFrontier`。调用点：`appendHistoryWindowLocked`（超行时）、`renderOwnedViewportLocked`（几何收缩时）、reflow 前置检查、geometry 变化（`applyLayoutLocked` 等）。
- **通道 B（直写滚动）**：`appendOwnedDirectPaintLocked`——大写入时把剩余行直接写到视口底部，写入行为本身触发终端 `\r\n` 滚动。

两条通道切的行来自同一个 `historyWindow`，靠 `handoffFrontier` 手工账本分割职责。**一旦某条路径记账滞后、顺序颠倒或绕开账本，同一行就被两个通道各滚一次 → 重复渲染。**

本次已修复的 bug 正是通道 B 在 `frontier` 推进之后仍重写全部行（未只画 frontier 之后的可见尾部），修复方式是让通道 B 尊重通道 A 的账本（`appendOwnedDirectPaintLocked` 从 `handoffFrontier.Value()` 处开始画）。

### 2.2 深层原因：架构性缺陷，而非偶发 bug

**（a）单一数组承担三个职责**

`historyWindow []string` 同时是：

1. **屏幕帧数据源**——`composedPlanLocked → historyRowsWithCursorBlankLocked` 用它的尾部组帧；
2. **scrollback 数据源**——`commitExcess` 从它的前段切行滚入终端；
3. **soft rewrite 数据源**——`replaceOwnedHistorySuffixLocked` / `noteSoftOutputLocked` 在它的尾部做可重写窗口。

三个消费者共享一个可变数组。任何消费者对数组的修改（trim、替换、reflow）都会影响其余两个。屏幕与历史因此**天然同步绘制**——不是设计目标，而是单一数据源的必然结果。

**（b）手工账本式状态机**

`handoffFrontier`（`renderengine.HandoffFrontier`）维护"已滚入 native scrollback 的行数"边界，其维护点散落：

- `AdvanceTo`（通道 A 滚动后）；
- `TrimPrefix(drop, len)`（`historyWindowMaxLines` 硬上限 trim 与 suffix 替换时，且必须 clamp）；
- `Clamp` / `Reset`（adopt、reset、suffix 替换后）。

幂等性依赖"只滚 frontier 之后的行"这一**隐式约定**。账本与数组不同步（trim 忘带 clamp、suffix 替换忘更新）即产生重复或丢失。

**（c）双通道协调靠调用顺序**

`WriteOutput` 的路径为：先 `appendHistoryWindowLocked`（可能触发通道 A 滚动）→ 再 `appendOwnedDirectPaintLocked`（通道 B 滚动）。顺序是正确性前提，但没有任何机制强制——新增调用点或重构时顺序一变即回归。

**（d）逻辑行/物理行手工换算**

数据模型是逻辑行，终端滚动要求 1:1 物理行。wrapped 场景需要 `expandHistoryLinesLocked`（用 `vt.Screen` 模拟展开）把逻辑行展开为物理行。直写路径的滚动计数按逻辑行计算，与终端实际物理行滚动存在错位风险（现象 1.3）。

**（e）最核心的矛盾：终端模拟器存在，但生产路径不用它**

`vt.Screen`（完整终端模拟：wrap、DECSTBM、宽字符、SGR、滚动）目前只用于：测试断言（`vt.Screen` 回放）、快照展开（`expandHistoryLinesLocked`）、`ComposedFrameForTest`。**生产渲染路径不把它作为事实源**，而是手工维护 `historyWindow` + 手拼 ANSI 序列（DECSTBM + `\r\n`）。

即：**代码库里已有一个"正确模拟终端行为"的组件，生产代码却在旁边手写另一套对终端行为的记账模拟**。两套体系并存是复杂度的总根源——重复、错位、同步绘制都是这套手工体系下的必然风险。

### 2.3 证据代码地图（fixed_bottom_surface.go）

| 符号 | 行号（2026-08-02 基线） | 职责 |
|---|---|---|
| `historyWindow []string` | 140 | 唯一文本事实源（屏幕尾部 + 历史前段） |
| `handoffFrontier *renderengine.HandoffFrontier` | 147 | 手工账本：已滚入 native scrollback 的行数 |
| `softOutput renderengine.SoftOutputState` | 151 | 可重写尾部窗口（硬上限 64 行） |
| `WriteOutput` 主路径 | ~820-900 | 写入 → appendHistoryWindow → 超屏直写 / 未超屏全帧重绘 |
| `appendHistoryWindowLocked` | 1317 | 归一化、追加、`commitExcess`（1339）、硬上限 trim + `TrimPrefix`（1351-1361） |
| `appendOwnedDirectPaintLocked` | 962 | 通道 B：大写入直写；现从 `frontier` 处开始画（977-981） |
| `commitExcessHistoryToScrollbackLocked` | 4206 | 通道 A：DECSTBM 滚动超出行入 scrollback |
| `renderOwnedViewportLocked` | 73 | 全帧 stage + diff Flush；几何收缩时先 commitExcess |
| `stageOwnedFrameLocked` / `composedPlanLocked` | 108 / 156 | 屏幕帧 = historyWindow 尾部组帧 |
| `replaceOwnedHistorySuffixLocked` | 1373 | soft rewrite：替换尾部 + `TrimPrefix` + `Clamp` |
| `insertHistoryLinesLocked` | — | 通道 A 的实际 ANSI 滚动输出 |
| `expandHistoryLinesLocked` | — | 逻辑行 → 物理行（vt.Screen 展开） |

---
## 3. 目标与不变量

### 3.1 目标

1. **结构上消除重复渲染**：同一行最多被滚入 native scrollback 一次（不依赖调用顺序或账本精确性）。
2. **未超屏内容不进入历史**：屏幕可见区与历史消息在数据上解耦；不超过可见区的写入不产生任何滚动与历史记账。
3. **滚动计数物理行正确**：wrapped 场景下滚动计数与终端实际物理行一致。
4. **可验证**：每次改动有真实字节流级测试锁定（沿用 `fixed_bottom_surface_overflow_render_test.go` 的方法：真实写入 → vt.Screen 回放 → 屏幕断言）。

### 3.2 沿用母计划不变量（不新增冲突）

- **INV-HANDOFF-01**：handoff frontier 单调前进，不能回退到已交给 native scrollback 的 range；
- **INV-HANDOFF-02**：同一 `CellID + Revision + DisplayRange + LayoutGeneration` 最多 handoff 一次；
- **INV-HANDOFF-03**：只有 committed、不可再 mutation 的内容可以 handoff；
- **INV-HANDOFF-04**：handoff 不产生新 cell、不改变 gap，不能再次走普通 append 路径；
- **INV-HANDOFF-05**：resize 只 reflow retained tail；已进入 native scrollback 的物理行视为不可重排。

### 3.3 本次新增的强不变量（方案 B 落实）

- **INV-SCROLL-01**：**终端滚动只有唯一发起点**——`commitExcess → insertHistoryLinesLocked`。直写路径（通道 B）不再自建 DECSTBM 滚动。
- **INV-SCROLL-02**：直写路径只把"frontier 之后、可见区之内"的行写到视口底部；滚动行为完全由写入"触碰屏幕底部"自然产生（即退化为纯文本追加，滚动是终端对追加的响应，不再由应用侧模拟）。
- **INV-SCROLL-03**：未超屏写入（追加后 `len(historyWindow) <= visible`）不调用 `commitExcess`，不推进 frontier，不产生任何滚动字节。

---

## 4. 方案 B（近期，低成本止血）：单一滚动通道

### 4.1 思路

本次修复的本质是"让通道 B 尊重通道 A 的账本"。方案 B 把它固化：**通道 B 不再滚动**。

- 大写入路径（当前 `appendOwnedDirectPaintLocked`）：先 `appendHistoryWindowLocked`（通道 A 负责把超出行滚入 scrollback），然后直写**仅剩余可见尾部**（frontier 之后的行）到视口底部。这些行写在可见区底部，`\r\n` 落在 DECSTBM 区域内时终端自然滚动——但**应用侧不再为直写路径单独构造滚动序列**，滚动只可能由通道 A 发出（INV-SCROLL-01）。
- 未超屏路径：`appendHistoryWindowLocked` 后 `commitExcess` 判定 `needHandedOff == 0` 即不滚动（已是现状），再走全帧重绘或轻量直写；**不推进 frontier、不产生滚动字节**（INV-SCROLL-03）。

### 4.2 具体改动点（范围预估）

1. **`appendOwnedDirectPaintLocked`（962）**：删除其内部的滚动序列构造（DECSTBM 区域设置 + 行级滚动拼接），改为"从 `frontier` 处取行 + 纯文本追加 + `CommitRange` 静默提交已滚行"。若当前实现已无自建滚动（修复后），则此步退化为**删除冗余的滚动分支 + 加注释固化语义**。
2. **`commitExcessHistoryToScrollbackLocked`（4206）**：成为滚动唯一入口。确认所有需要滚动的调用点（1339、89、1166、2982）都在 `appendHistoryWindowLocked` / 几何变化之后调用，顺序由函数契约固定：**"先记账后滚动"在函数内完成**（把 `appendHistoryWindowLocked` 内部的 `commitExcess` 调用保留，外部新增调用点必须经过同一入口）。
3. **`handoffFrontier` 维护点收敛**：`AdvanceTo` 只在 `commitExcess` 内推进；`TrimPrefix`/`Clamp` 只在窗口 trim/suffix 替换处；删除散落的手工推进。
4. **wrapped 直写对齐（1.3 现象）**：直写前用 `expandHistoryLinesLocked` 预展开待写物理行，滚动计数/写入行数以物理行为准（最小改动：直写段以物理行写入，frontier 记账仍按逻辑行但滚动由通道 A 负责，二者在 commitExcess 内统一换算）。

   **验收标准（硬性）**：取消 `fixed_bottom_surface_overflow_render_test.go` 中 `TestFixedBottomSurface_WrappedOverflowScreenContentMatchesModel` 的 `t.Skip`（当前注释明确标注 "KNOWN BUG (architecture-level)"：直写路径按逻辑行计数滚动、终端按物理行滚动，wrapped 行占 2 物理行导致每行直写视口漂移 1 行），并使其通过。

   **如果方案 B 修不了（预判风险）**：frontier 按逻辑行记账、终端按物理行滚动，二者换算本质上无法在"手工 ANSI + 账本"体系内稳定对齐——则该测试保持 skip，并在 §4.4 与方案 A 验收中显式标注"wrapped 直写错位由方案 A 消除"，方案 B 不承诺修复此问题，避免把残留风险误记为已解决。

### 4.3 方案 B 的验证

- 新增测试（`fixed_bottom_surface_overflow_render_test.go` 扩展）：
  - **未超屏写入零滚动**（INV-SCROLL-03 的专属断言）：写入 N 行（N < 可见区），断言输出字节流中**无 DECSTBM / 无 `\r\n` 滚动序列**、`handoffFrontier` 不推进。注意：此断言只能写在 ui 层（e2e 层字节流混入 prompt/status/交互 ANSI，无法隔离滚动通道）。
  - **wrapped 直写对齐**：取消 `WrappedOverflowScreenContentMatchesModel` 的 `t.Skip` 并转绿（验收标准见 4.2 改动点 4）。
- **既有测试全量矩阵（方案 B 后必须全部转绿）**：
  - overflow 专项：`OverOneScreenOutputNeverDuplicatesHistory`、`OverOneScreenWrappedOutputNeverDuplicatesHistory`、`WrappedOverflowScreenContentMatchesModel`（取消 skip 后）、`DirectWriteBandToggleScreenMatchesModel`（直写 × ActiveBand 全帧重绘交替，最棘手的交错场景）；
  - resize 专项：`TestOwnedViewport_ReconcileConvergesAfterResize`、`TestFixedBottomSurface_TerminalSizeChangeDropsAbsorbedRowDebt`、`TestFixedBottomSurface_SyncTerminalGeometryProbesSizeOnce`（覆盖改动点 2 中 `applyLayoutLocked`（2982）的 commitExcess 调用路径）；
  - soft tail 系列：`TestFixedBottomSurface_WriteOutputInvalidatesSoftTail`、`TestFixedBottomSurface_SoftTailTrimsAndBlocksReflowMapping` 等（改动点 3 收敛 frontier 时不得破坏 soft 窗口与 frontier 的交叉联动）；
  - 门禁：`go test ./cmd/aicli/ui/...` + `go test ./cmd/aicli/commands`（含 P0 门禁 `TestChatInteractiveDirectWriterInventory`、真实 TTY 主循环测试 `chat_tty_live_loop_test.go`）全绿。

### 4.4 方案 B 的边界（不解决什么）

- 逻辑行/物理行双数据模型仍存在（只是换算点收敛到 commitExcess）；
- 可见区动态推导（ActiveBand/popup/prompt）与历史的耦合仍在；
- 双保留 headroom（visible+40）仍在；
- **wrapped 直写错位可能仍存在**：若 4.2 改动点 4 的验收未达成（`WrappedOverflowScreenContentMatchesModel` 保持 skip），则该问题明确记为方案 A 的消除项，方案 B 不得宣称已解决（见 4.2 预判风险）。

---
## 5. 方案 A（远期，根治）：终端模型唯一事实源

### 5.1 思路

把 `vt.Screen`（已存在、已被测试与快照展开使用）**升级为生产路径的唯一写入入口**，让终端行为（wrap、DECSTBM 滚动、宽字符、光标）由模型内部完成，应用侧不再手工模拟：

```
写入        →  model.Write(text)           ← 唯一通道；滚动/wrap/位置由模型内部完成
屏幕        →  model.Viewport() 快照        ← 与历史完全独立的视图
历史        →  model.Scrollback() 环形缓冲  ← 行只写入一次，物理上不可能重复
渲染        →  diff(prevViewport, Viewport) → 复用现有 viewport backend 双缓冲
soft rewrite → model.RewriteTail(k, rows)    ← 可见区内的回滚重写，不触碰 scrollback
reflow      →  model.RewrapTail()           ← 仅尾部可重排，scrollback 物理行不可变
```

### 5.2 删除清单（方案 A 完成后）

- `historyWindow` 双段模型（屏幕尾部 + 历史前段）→ 由 `model.Viewport()` / `model.Scrollback()` 取代；
- `handoffFrontier` 手工账本 → 由模型的滚动缓冲内部位置取代（handoff 变成"查询模型已滚出多少物理行"）；
- `commitExcessHistoryToScrollbackLocked` / `insertHistoryLinesLocked` / `expandHistoryLinesLocked` / `appendOwnedDirectPaintLocked` 的滚动分支 → 全部由模型内部滚动取代；
- 逻辑行/物理行手工换算 → 模型天然按物理行滚动（wrapped 错位从机制上消失）；
- 双保留 headroom 与 shrink 恢复 → 可见区收缩时模型内滚出 + 视口重取，无"预留行"概念。

### 5.3 需要给 vt.Screen 补充的能力（缺口清单）

1. **Scrollback 缓冲**：有界环形缓冲（容量可配，默认对齐现有 `historyWindowMaxLines` 语义）+ 查询 API（按物理行索引）；
2. **Viewport 快照**：返回可见区 `[][]vt.Cell` + 光标位置 + 一个单调递增的 frame generation（供 diff 缓存判断）；
3. **RewriteTail**：可见区内回滚最后 k 物理行并重写（soft rewrite 语义：仅尾部可重写，scrollback 部分不可变，对应 INV-HANDOFF-05）；
4. **物理行滚动计数**：`Write` 后报告本次滚出的物理行数，供上层（如 /debug 统计）与双缓冲 Invalidate 决策使用；
5. **Resize + rewrap**：当前 `vt.Screen` **没有 Resize 能力**（方法清单只有 `NewScreen/Feed/index/reverseIndex/scrollUp/scrollDown/insertLines/deleteLines` 等），且 `index()`（`vt/screen.go` 114 行）在区域底部滚动时**直接丢弃顶部行**——scrollback 不是"加一个字段"，而是**滚动路径本身（index/scrollUp/insertLines/deleteLines 全部滚动出口）都要改为把丢弃行推入环形缓冲**。Resize 后需按新宽度 rewrap 保留尾部（仅 retained tail，已入 scrollback 的物理行不可重排，对应 INV-HANDOFF-05）——这是 A1 工作量的主体，不是边角；
6. **handoff 接口**：`CommittedPhysicalRows()` / `AdvanceCommitted(n)`，把"已滚出 + 已冻结"与"可见尾部可重写"边界显式化。

   **与母计划 ScrollbackHandoff 的分工（评审补充）**：母计划（`aicli-tui-unified-render-architecture-refactor-plan.md` §3.5/§5）定义 `ScrollbackHandoff` 组件为"选择 eligible committed range、记录 exactly-once frontier"，其输入是 Scene 数据层的 cell/revision。本方案的 vt.Screen 模型是**物理终端行为层**（滚动/wrap/光标），两者层次不同：模型负责"物理上每行只滚出一次并进入环形缓冲"，`ScrollbackHandoff` 负责"哪些 committed cell 可以被 handoff"的业务判定。A1 直接在 vt.Screen 上实现物理滚动缓冲（不越过 Scene 层）；业务层 handoff 判定仍按母计划组件化，二者通过第 6 项接口衔接。

### 5.4 里程碑（每个里程碑独立可验证、可回滚）

| 里程碑 | 内容 | 验证 |
|---|---|---|
| A1 | vt.Screen 增加 scrollback 缓冲（改造所有滚动出口）+ Viewport 快照 + Resize/rewrap + 查询 API（纯新增，不动生产路径） | 新增单元测试：滚动入缓冲、wrap、宽字符、Resize/rewrap、物理行滚动计数（先红后绿） |
| A2 | 生产路径接入模型：`WriteOutput` → `model.Write` + `Viewport` diff 渲染（替代当前直写/全帧重绘双路径） | ui 层强断言全绿（`WrappedOverflowScreenContentMatchesModel` 取消 skip 并转绿、overflow 4 个、resize 3 个、soft tail 系列）+ commands 包全绿 + e2e TTY 冒烟全绿。**e2e 全绿不是充分验收**（见 §7 分层矩阵：e2e 抓不住非相邻重复/零滚动/wrapped 对齐，必须靠 ui 层字节流断言） |
| A3 | soft rewrite / reflow 迁移到 `RewriteTail` | `soft_output` 相关测试全绿 |
| A4 | 删除旧路径：`historyWindow` 双段、frontier 账本、commitExcess、insertHistoryLines、expandHistoryLines、直写滚动分支 | `grep` 确认无残留符号；全量回归 + P0 门禁 |
| A5 | /debug display 与 trace 适配模型（行号、white 计数以模型物理行为准） | /debug 手工验证 + 快照测试 |

### 5.5 风险

- 每帧 `Viewport` 快照拷贝成本 → 只在 frame generation 变化时快照，diff 复用现有双缓冲（A2 内先做基准对比）。**性能量化门槛（评审补充）**：在 A2 实施时对 `WriteOutput` 热路径（大写入 + 流式追加 + shrink 序列）做基准，单次追加的端到端渲染开销（字节产出 + 模型更新）相对方案 B 基线**不超过 +30%**，且不引入每帧全量 `[][]vt.Cell` 拷贝（快照只做增量/懒拷贝）；超出门槛则回退 A2，先做快照复用优化再上；
- 与母计划（Scene/事务/flush）的边界：模型是"物理终端行为"的真相源，母计划的 Scene 是"内容/布局"层，二者层次不同不冲突——模型承接母计划 `ScrollbackHandoff` + presenter 底层职责；
- 迁移期间双体系并存（同 2.2e 现状），A2 前不得删除任何旧路径。

---

## 6. 方案对比与推荐路线

| 维度 | 方案 B（单一滚动通道） | 方案 A（终端模型唯一事实源） |
|---|---|---|
| 消除重复 | ✅ 机制上消除（唯一滚动发起点） | ✅ 机制上消除（行只写一次） |
| 未超屏进历史 | ✅ 不推进 frontier、零滚动字节 | ✅ 模型内不滚动即不入 scrollback |
| wrapped 错位 | ⚠️ 收敛换算点，仍手工 | ✅ 模型天然物理行 |
| 可见区/历史解耦 | ⚠️ 仍共享 historyWindow | ✅ Viewport/Scrollback 独立 |
| soft rewrite 复杂度 | 不变 | ✅ RewriteTail 语义清晰 |
| 改动量/风险 | 小（1 文件族 + 测试） | 大（vt.Screen 扩展 + 生产路径切换 + 删除迁移） |
| 与母计划衔接 | 不冲突，是母计划 handoff 约束的强化 | 承接母计划 ScrollbackHandoff/presenter 底层 |

**推荐：先 B 后 A。** 方案 B 是本次已修复 bug 的正规化（几小时内可完成，风险低），立刻把"双通道账本"降为"单一通道 + 收敛记账"；方案 A 作为独立重构排期，彻底删除手工终端模拟。

---

## 7. 验证策略（通用）

### 7.1 验证能力分层矩阵（评审重写）

不同层级的测试能力不同，**必须按层声明能验证什么、抓不住什么**。方案 B/A 的核心不变量只存在于 ui 层，e2e 层无法替代。

| 层级 | 载体 | 能验证 | 抓不住（必须由下一层补） |
|---|---|---|---|
| **L1：ui 层字节流测试** | `ui/fixed_bottom_surface_overflow_render_test.go` 等方法：捕获 `WriteOutput` 输出字节 → `vt.Screen` 回放 → 逐行 content-match | **非相邻重复**（全局唯一性断言，如 `WrappedOverflowScreenContentMatchesModel` 的逐行模型对照）；**零滚动**（INV-SCROLL-03：无 DECSTBM/滚动序列 + frontier 不推进）；**wrapped 对齐**（窄终端 + 超宽行，屏幕行 == 模型物理行）；shrink/grow 精确序列 | 真实主循环、真实 stdin 注入、prompt/status/popup 与 slash 命令的端到端集成 |
| **L2：e2e TTY 冒烟** | `commands/chat_tty_live_loop_test.go`（真实主循环 + stdin 注入 + `vt.Screen(80,24)` 回放） | 滚动真实发生（头部滚出/尾部可见）；**相邻**重复哨兵；行宽健康（`OverflowRows`）；内容可达性；多轮交互 | **非相邻重复**（哨兵只查相邻两行）；**流式+shrink 序列**（现为一次性写入）；**零滚动**（字节流混入交互 ANSI 无法隔离）；**wrapped 错位**（固定 80 列且行限 40 列刻意避开 wrap）；**resize/reflow**（无 resize 注入） |
| **L3：真实 provider 人工 smoke** | `commands/chat_tty_live_loop_opencode_live_test.go`（`AICLI_LIVE_OPENCODE_TTY_TEST=1`） | opencode.ai deepseek-v4-flash max 真实链路的端到端冒烟（屏幕 dump + 相邻重复哨兵） | 断言语义弱（相邻重复）；默认 skip，不进 CI 门禁；不可作为方案 B/A 的验收依据 |

### 7.2 门禁规则

1. **方案 B/A 的每个改动必须先在 L1 有对应字节流测试（先红后绿）**，再谈 L2/L3。
2. **零滚动断言只在 L1 写**（L2 字节流含 prompt/status/交互 ANSI，无法隔离滚动通道）。
3. **e2e 全绿不是充分验收**：L2 通过仅证明"冒烟健康"，核心不变量以 L1 为准（§5.4 A2 已验证此条）。
4. **双向验证**：对每个修复，临时恢复 bug 行为确认测试能捕获（先红），再恢复修复（转绿），防"测试永远通过"。
5. **门禁命令**：`go test ./cmd/aicli/ui/...`、`go test ./cmd/aicli/commands`（含 `TestChatInteractiveDirectWriterInventory` P0 门禁、`chat_tty_live_loop_test.go`）全绿；L3 不作为门禁。

### 7.3 e2e 增强（可选，评审建议）

`TestTTY_LiveLoop_LongResponseScrollsBeyondOneScreen` 的重复哨兵从"相邻"升级为"**raw 字节流中每行至多出现一次**"（全局唯一性）。注意：需排除合法重绘路径的干扰——soft rewrite/全帧重绘会合法地重复写出同一行文本，因此判定应基于"同一行文本在**最终屏幕**出现 >1 次"或"滚出后又以相同文本重现"，而非朴素地数字节流出现次数；该升级需先确认不误伤合法重绘路径。

---

## 8. 风险与回滚

- **方案 B**：改动集中在 `appendOwnedDirectPaintLocked` 与 `commitExcessHistoryToScrollbackLocked`；回滚 = 恢复 4.2 改动前的函数体（已有 git diff 基线）。
- **方案 A**：A2 为生产路径切换点，风险最高；回滚 = 保留旧路径开关（环境变量 `AICLI_RENDER_LEGACY=1`），A4 删除旧路径前默认关闭开关。
- **顺序回归风险**：B 实施后任何新增写入/几何路径都必须经过 `appendHistoryWindowLocked → commitExcess` 单一入口；在代码评审中强制检查"是否有新调用点绕过 commitExcess 直接滚动"。

---

## 9. 待办清单

- [ ] 方案 B：`appendOwnedDirectPaintLocked` 删除自建滚动分支（或确认已无后固化注释）
- [ ] 方案 B：`commitExcess` 作为滚动唯一入口，收敛 `AdvanceTo/TrimPrefix/Clamp` 维护点
- [ ] 方案 B：未超屏零滚动测试（断言无 DECSTBM、frontier 不推进；仅 L1）
- [ ] 方案 B：wrapped 直写对齐——取消 `WrappedOverflowScreenContentMatchesModel` 的 t.Skip 并转绿（4.2 验收标准；修不了则保持 skip 并记入方案 A）
- [ ] 方案 B：既有测试全量矩阵回归（overflow 4 个 + resize 3 个 + soft tail 系列 + P0 门禁）
- [ ] 方案 A：vt.Screen scrollback/Viewport/RewriteTail API 设计评审
- [ ] 方案 A：vt.Screen Resize/rewrap 设计（A1 主体工作量；滚动出口全部改造为入环形缓冲）
- [ ] 方案 A：A1–A5 里程碑排期（建议独立任务卡）
- [ ] 方案 A：A2 性能基准门槛（相对方案 B 基线 ≤ +30%，无每帧全量快照拷贝）
- [ ] 可选：e2e `LongResponseScrollsBeyondOneScreen` 升级全局唯一性哨兵（先确认不误伤合法重绘路径）
