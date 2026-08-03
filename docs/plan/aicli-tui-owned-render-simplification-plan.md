# aicli TUI owned 渲染简化方案：双物理所有者 + Frame/Scrollback 单调模式

状态：**proposed（待评审）**

日期：2026-08-02

## 0. 文档定位与结论摘要

本文回答一个问题：**当前 owned viewport 渲染设计能否简化，以及怎么简化。**

结论：**能简化，而且应该简化。** 方向不是继续修补 `commitExcessHistoryToScrollbackLocked` 的时序，而是消除"同一历史行同时由三份状态负责"的结构问题：

1. `historyWindow`（应用保留文本）
2. 终端 native scrollback（不可逆字节）
3. `ScreenModel.front/back`（双缓冲帧）

简化的目标架构是 **两个物理所有者 + 一个单调提交器**：

- **committed prefix** → 只属于 native scrollback（写入一次，永不再上屏）；
- **mutable tail + bottom pane** → 只属于 `ScreenModel`（diff 渲染）；
- 所有 scrollback 修改收敛到**一个提交器**，提供两种原语：`AppendNewRows`（新内容）与 `ScrollExistingRows`（纯滚动，不重写文本）；
- 应用状态机为 **Frame mode → Scrollback mode** 的单调转移，不再需要"headroom 双保留 + frontier 账本 + CommitRange/Invalidate 猜测"的混合体。

关联文档：

- `aicli-tui-single-scroll-channel-plan.md`（本文是其"方案 A/B"之争的裁决与落地路线：**选 B 的结构化形式，同时把 A 的目标以"双物理所有者"方式吸收**，而不是继续加账本）；
- `aicli-tui-p5-owned-viewport-design.md`（owned viewport 现状设计）；
- `aicli-tui-unified-render-architecture-refactor-plan.md`（母计划）。

> 本文只给出方案与迁移顺序，不包含实现补丁。

---

## 1. 现状基线（提交 135a072 之后）

### 1.1 两个失败测试（新增自带失败，非回归）

| 测试 | 文件 | 断言 |
|---|---|---|
| `TestFixedBottomSurface_BandShrinkRestoreNeverReplaysHandedOffHistory` | `fixed_bottom_surface_band_restore_duplicate_test.go` | 每行文本在 ANSI 字节流中最多出现 1 次（`strings.Count(raw, lineText) > 1` 即失败）；band 出现→消失过程中不得把已滚入 scrollback 的行重新画上屏 |
| `TestBottomReserveShrinkRestoresHistoryWithoutBlanktingTop` | `fixed_bottom_surface_compensation_top_bug_test.go` | 20×10 屏、9 行输出 + 3 行 band；band 消失后屏幕 1..9 行必须恢复为 L1..L9；不得发出 CSI-T 下滚补偿；`ComposedFrameForTest` 与 `vt.Screen` 零差异 |

### 1.2 最新实测诊断（2026-08-02，提交 135a072）

测试 1 阶段日志：

```
阶段1后: frontier=17 window=40 visible=23   （40 行写入，17 行已进 scrollback）
阶段2后: frontier=24 window=40 visible=16   （band 6 行出现，visible 23→16，又提交 7 行）
阶段3后: frontier=36 window=52 visible=16   （band 期间写 12 行，再提交 12 行）
阶段4后: frontier=36 window=52 visible=23   （band 消失，frontier 不动，恢复）
```

重复计数：`line-17..23 ×3`、`line-24..35 ×4`、`line-36..39 ×6`、`line-40..51 ×3`。

字节流证据（`line-17` 三次出现）：

1. `@756`：band 出现时的 **commitExcess 文本滚动**（`\x1b[1;23r ... \r\nline-17...`）——此时 line-17 尚在屏上，属于"已在屏上又被滚走"；
2. `@1749`：**全帧 diff 重绘**（`\x1b[1;1H ... line-17...`）——front 未同步滚动，diff 认为需要重画；
3. `@3354`：band 消失时的 **commitExcess 窄区滚动**（`\x1b[1;16r ... \r\nline-17...`）——滚动区比需滚动行数还窄，行溢出重写。

测试 2 失败屏幕：

```
01|          ← 应为 L1
02|          ← 应为 L2
03|L4
...
09|
10|Ready
```

即 band 出现时 L1..L3 被 commitExcess 滚出屏幕，band 消失后组帧（frontier 截断）不再包含它们 → 顶部空白。

### 1.3 现状代码地图（关键引用）

| 符号 | 位置 | 职责 |
|---|---|---|
| `historyWindowMaxLines = 400` | `fixed_bottom_surface.go:1329` | 保留窗口硬上限 |
| `historyWindowHeadroom = 40` | `fixed_bottom_surface.go:4195` | shrink 恢复用双保留余量 |
| `appendHistoryWindowLocked` | `fixed_bottom_surface.go:1334` | 追加 → 内部调 `commitExcessHistoryToScrollbackLocked`（1356） |
| `writeOutput`（owned 分支） | `fixed_bottom_surface.go:825-871` | `appendHistoryWindowLocked` → `shouldAppendDirectLocked` → 直写或全帧 |
| `shouldAppendDirectLocked` | 921 | 超屏判断（逻辑行下限 + 物理行展开） |
| `appendOwnedDirectPaintLocked` | 962 | 通道 B：从 `frontier` 起画 `historyWindow[frontier:]`，`insertHistoryLinesInRegionLocked` + `stageOwnedFrameLocked` + `CommitRange(1, regionBottom)` |
| `RewriteSoftOutputTail`（owned） | 1125 / 1163-1191 | reflow 重写 → `commitExcess` → `Invalidate` → 全帧 |
| `applyOwnedViewportGeometryLocked` | 2971 | bottom 增长 → `commitExcessHistoryToScrollbackLocked`（2999）；尺寸/底部变化 → `Invalidate`（3028） |
| `renderOwnedViewportLocked` | `fixed_bottom_surface_snapshot.go:73` | `commitExcess` → `Invalidate` → `stageOwnedFrameLocked` → `Flush` |
| `stageOwnedFrameLocked` | snapshot:108 | `composedPlanLocked` 全帧 StageFrame |
| `composedPlanLocked` | snapshot:156 | `historyRowsWithCursorBlankLocked()` 全量历史组帧 |
| `commitExcessHistoryToScrollbackLocked` | `fixed_bottom_surface.go:4223` | 通道 A：`keepForRestore = visible + headroom`；`needHandedOff = len - visible`；DECSTBM 文本滚动；`AdvanceTo`；soft 失效 |
| `insertHistoryLinesLocked` / `insertHistoryLinesInRegionLocked` | 4319 / 4338 | 唯一滚动字节构造点（INV-SCROLL-01） |
| `HandoffPlan.ANSI` | `renderengine/handoff_plan.go:45` | `\x1b[s \x1b[1;N r \x1b[N;1H (\r\n row)* \x1b[r \x1b[u`——**总是重写行文本** |
| `ScreenModel` | `renderengine/screen_model.go` | front/back 双缓冲、`CommitRange`、`Invalidate`、diff `Flush` |

---
---

## 2. 复杂度根源：同一历史行被三份状态同时拥有

### 2.1 三份所有权

当前实现中，一行文本在滚动后同时存在于：

| 所有者 | 载体 | 生命周期 | 读取方 |
|---|---|---|---|
| A. 应用保留文本 | `historyWindow []string`（400 行上限，trim） | 可修改（追加/trim/replace/reflow） | `composedPlanLocked`、`commitExcess`、soft rewrite |
| B. 终端 scrollback | native 终端缓冲区（DECSTBM 滚动后的字节） | 不可逆，只增 | 无（应用不回读） |
| C. 双缓冲帧 | `ScreenModel.front/back` | 可修改（Stage/Commit/Invalidate） | diff `Flush` |

边界协调靠：`handoffFrontier`（账本）+ `historyWindowHeadroom = 40`（双保留恢复余量）。

### 2.2 补偿链：状态同步靠"事后猜测"

任何几何变化（band 出现/消失、bottom 增减、shrink）都会触发同一条补偿链：

```
commitExcessHistoryToScrollbackLocked  ① 文本滚动（DECSTBM 区域 \r\n 重写行文本）
  → HandoffFrontier.AdvanceTo          ② 账本前移
  → Invalidate                         ③ 模型失效（front 与终端实际不同步）
  → renderOwnedViewportLocked          ④ 全帧 StageFrame + diff
  → CommitRange(1, regionBottom)       ⑤ 覆盖补偿"已知的滚动"
  → Flush                              ⑥ 一次字节写入
```

这条链的每一环都是**因为上一环没有让状态自洽**而存在的：① 改变了终端，但 ③ 不知道改了什么；④ 用 diff 猜测差异；⑤ 再覆盖补偿。实测中 `line-17` 三次出现的三次来源（`@756` 文本滚动、`@1749` 全帧 diff、`@3354` 窄区 commitExcess）正是这条链的三段输出——**链越长，重复越多**。

### 2.3 为什么 headroom 双保留救不了测试 2

`commitExcessHistoryToScrollbackLocked` 的 `keepForRestore = visible + headroom` 试图在应用侧保留一份"以后可能恢复"的行。但：

- 它保留了**文本**，没有保留**屏幕位置**——band 出现时 L1..L3 已滚出屏幕，band 消失后组帧从 frontier 截断处开始，保留文本根本不会被画回；
- 保留文本与 scrollback 里的同一行构成**双写**（测试 1 的重复来源之一）；
- 40 行 headroom 是拍脑袋的余量，不是结构保证。

**结论：双保留是"用更多状态掩盖同步缺失"，方向反了。正确的简化是让滚动操作本身同时、确定性地更新模型——一次操作、一处修改，不需要补偿。**

---

## 3. Codex 对照：职责分离而不是共享所有权

只读参考：`E:\projects\ai\codex\codex-rs\tui\src\`。

### 3.1 Codex 的做法

- **`insert_history.rs`（`InsertHistoryMode::Standard`）**：scrollback 写入是**一次性操作**——`SetScrollRegion(1..area.top())` → `MoveTo(0, cursor_top)` → 逐行 `\r\n` + `write_history_line` → `ResetScrollRegion` → `MoveTo` 最后 cursor；viewport 需要被推下时用 `\x1bM`。写完即忘，不保留"可恢复副本"。
- **`custom_terminal.rs`（`viewport_area`）**：终端 buffer 尺寸 = viewport 尺寸（`set_viewport_area`），**可见区即缓冲区**，没有"全屏 + 窗口指针"的双层结构。
- **`resize_reflow.rs`**：resize 不增量修补，而是**源重建**——`clear_terminal_for_resize_replay`（`clear_visible_screen` / `clear_scrollback_and_visible_screen_ansi`）→ 从 `transcript_cells`（`HistoryCell` 语义源，write-once 不删）重放 `display_lines_for_history_insert` → `resize_reflow_max_rows` 封顶。

### 3.2 关键差异

| 维度 | Codex | aicli 现状 |
|---|---|---|
| 文本语义源 | `transcript_cells` 唯一，write-once | `historyWindow` 与屏幕帧是同一个可变数组 |
| scrollback 写入 | 一次提交，写完即忘 | 文本滚动 + 帧重绘共享同一字节流，靠账本分割 |
| 可见区 | buffer = viewport_area | 全屏双缓冲 + 窗口指针（`outputBottom`/`activeBand`） |
| resize/reflow | 显式 clear + 源重放（接受重发） | 增量修补 + 补偿（重发被当成 bug） |
| UI 区渲染 | ratatui diff buffer，与 scrollback 完全隔离 | `composedPlanLocked` 全量历史组帧 |

### 3.3 值得借鉴的三点

1. **"重发文本"本身不是 bug，没有权威源才是**。Codex resize 重建也重发相同文本，但因为源（`transcript_cells`）是权威且 write-once，重发只是"从源重新渲染"，不是状态错乱。aicli 的测试 1 用字节计数否定一切重发，把"无权威源"的病症和"从权威源重渲染"的正常行为混为一谈——**测试语义需要先澄清（§4）**。
2. **`viewport_area` 概念**：`ScreenModel` 的尺寸应等于可见区（含 bottom pane），而不是整个终端物理屏；滚动 = 模型内部平移，天然与终端同步。
3. **scrollback 提交与渲染是两个独立阶段**：提交（insert_history）负责字节正确，渲染（ratatui/模型 diff）负责屏幕正确；aicli 把它们耦合在 `commitExcess → Invalidate → 全帧重绘 → CommitRange` 的单一链条里，才产生重复与空白。

---
---

## 4. 测试语义冲突：必须先澄清"什么算重复"

### 4.1 测试 1 断言的不严谨处

`TestFixedBottomSurface_BandShrinkRestoreNeverReplaysHandedOffHistory` 用 `strings.Count(raw, lineText) > 1` 判定重复。但字节流中同一行文本出现两次，可能是三种**性质完全不同**的情况：

| 情况 | 示例 | 是否错误 |
|---|---|---|
| scrollback 重复追加 | DECSTBM 滚动两次，同一行两次进 scrollback | 是（行序列重复） |
| 原位重绘 | `CSI row;col H` + 重画 L7（位置不变） | 否（不新增 scrollback 行） |
| 权威源重建 | Codex resize 的 clear + 重放 | 否（源权威、write-once，重放是渲染行为） |

Codex 的 resize rebuild 同样会重发相同文本——如果按测试 1 的字节计数标准，Codex 自己也"失败"。**计数法无法区分上述三种情况，它度量的是实现细节而非语义。**

### 4.2 测试 1 的真实目标语义

band 出现 → 消失的过程中：

1. **不丢失**：每行文本（按语义行 ID）仍可见或仍可恢复；
2. **不重复**：任何一行进入 native scrollback 至多一次；
3. **不上屏重放**：已跨过 committed boundary（应用不再持有）的语义行不得被重新画上屏。band 出现时被物理推出屏幕、但应用仍持有的行（mutable 内）在 band 消失后恢复上屏**不算重放**——判定以应用的 handoff 语义为准，而非终端的物理 scrollback。

### 4.3 建议的断言改造（阶段 0 单独提交）

- 用 `vt.Screen` 回放 ANSI 流，**记录 DECSTBM 真正推出顶部的行序列**（scrollback 序列），断言无重复；
  > **依赖（阶段 0 前置，见 N6）**：当前 `vt.Screen`（`ui/vt/screen.go`）不保留滚出行——`index()` 滚动时直接丢弃顶部行、`reverseIndex()` 清空顶行，`fixed_bottom_surface_band_restore_duplicate_test.go:25` 注释自述 "A vt.Screen cannot see this: scrolled-out rows leave the simulated screen"。阶段 0 须先扩展 `vt.Screen`（`index()`/`reverseIndex()`/`scrollDown()` 记录推出/拉回序列；RI/SD 支持从 scrollback 拉回语义），或直接采用下一项方案；
- 或按"语义行 ID → 出现位置"记账（ID 可取行文本本身或输出序号，流式输出下两者等价）：同一 ID 跨过 committed boundary 至多一次；原位 repaint（同位置重画）不记账；
- 保留字节级断言作为**辅助诊断**，不作为通过条件。

### 4.4 测试 2 的目标语义

band 消失（shrink）后，屏幕 1..9 行必须恢复为 L1..L9，顶部不得空白。其语义是：**band 出现时的滚动是"可见区几何变化"，不是"内容提交"**——L1..L3 只是暂时被挤出可见区，仍在 mutable 数据内，恢复后应无文本重发、无空白。

> 两个测试在目标架构下都自然通过：band 出现/消失走 `ScrollExistingRows`（模型平移，零文本重写），超屏新内容走 `AppendNewRows`（一次写入）。它们并不矛盾——矛盾的是**当前实现把几何变化实现成了文本提交**。

---

## 5. 目标架构：两个物理所有者 + 单调提交器

### 5.1 所有权划分

```
┌─────────────────────────────────────────────┐
│ ScreenModel（front/back）＝唯一渲染所有者      │
│   · visibleRows（可见区，终端 1:1）           │
│   · bottomPane（band / prompt，可见区内）     │
│   · mutableTail（未提交行：组帧只消费这里）     │
│ 尺寸 = 可见区（viewport 概念），滚动 = 模型平移  │
└─────────────────────────────────────────────┘
                    │  ScrollExistingRows(count)
                    │  （纯平移，不写文本）
                    ▼
┌─────────────────────────────────────────────┐
│ native scrollback ＝唯一提交所有者            │
│   · committedRows（append-only）             │
│   文本只经 AppendNewRows(rows) 写入一次       │
│   写后应用不再持有副本、不再参与组帧            │
└─────────────────────────────────────────────┘
```

### 5.2 提交器：两个原语

- **`AppendNewRows(rows)`**：从未上屏的新内容。终端侧 `DECSTBM(1, regionBottom)` + `\r\n` 逐行写入（复用现有 `insertHistoryLinesInRegionLocked` 的字节构造）；模型侧同步滚动 + 追加。**文本的唯一进入 scrollback 的入口。**
- **`ScrollExistingRows(count)`**：可见区几何变化（band 出现/消失、bottom 增减）。终端侧只发滚动序列（`\x1bM`/DECSTBM 区域 `\r\n` 空行或直接依赖模型 diff）；模型侧 `ScrollUp/ScrollDown` 同步平移 front/back。**不重写任何行文本。**

> `HandoffPlan`/`HandoffFrontier` 保留但收缩职责：`HandoffPlan` 只由 `AppendNewRows` 使用；`HandoffFrontier` 退化为 `committedBoundary`（单调计数器，仅作调试/观测，不再参与组帧正确性）。

### 5.3 状态机：Frame mode → Scrollback mode（单调）

- **Frame mode**：`committedBoundary == 0`，全部内容在 `ScreenModel` 中，应用可恢复任意行；渲染 = 全帧 diff（现状的 `renderOwnedViewportLocked` 不变，但数据只来自模型）。
- **Scrollback mode**：一次超屏写入（或显式提交）把整个 mutable 内容整体 commit → `committedBoundary = len(rows)`，**此后不再有"未超屏历史"概念**；`mutableTail` 只容纳可见区 + bottom pane 的增量；组帧只消费 `mutableTail`。
- **转移条件**：`shouldAppendDirectLocked` 判定超屏 → 整体 commit（一次 `AppendNewRows`）→ 进入 Scrollback mode。**boundary 只进不退**，无 `TrimPrefix`/`Clamp`/headroom。

### 5.4 不变量

| ID | 内容 |
|---|---|
| INV-S1 | 每个语义行文本经 `AppendNewRows` 进入 scrollback **至多一次** |
| INV-S2 | `committedBoundary` 单调不减（无回退、无 trim 恢复） |
| INV-S3 | `ScreenModel` 与终端实际屏幕状态**始终一致**（滚动同步平移，不用 `Invalidate` 重建补偿） |
| INV-S4 | 几何变化（band/bottom 增减、shrink）**不产生文本提交**，只产生模型平移 |
| INV-S5 | 组帧只消费 `mutableTail`（boundary 之后），不消费 committed prefix |
| INV-S6 | 全帧重绘只发生在 Frame mode 或内容变更后；已同步的滚动不触发全帧 |

---
---

## 6. 具体改动清单

### 6.1 删除（简化本体）

| # | 删除项 | 位置 | 替代 |
|---|---|---|---|
| D1 | `historyWindowHeadroom` 双保留：`keepForRestore = visible + headroom` | `fixed_bottom_surface.go:4195`（`commitExcessHistoryToScrollbackLocked`） | 无——可恢复性由"模型持有全部 mutable 行"结构保证 |
| D2 | 几何变化中的文本提交：`bottomRows > previousBottomRows → commitExcessHistoryToScrollbackLocked` | `applyOwnedViewportGeometryLocked`（2971/2999） | `ScrollExistingRows`（模型平移） |
| D3 | `RewriteSoftOutputTail` owned 路径中的 `commitExcess` | 1163-1191 | 模型内重写（soft 窗口只在 mutable tail 内） |
| D4 | 直写路径独立的 frontier 计算与窄区滚动变体 | `appendOwnedDirectPaintLocked`（962）、`insertHistoryLinesInRegionLocked`（4338）的窄区分支 | 统一走 `AppendNewRows` |
| D5 | 正常路径的 `viewportBackend.Invalidate()` 重建补偿 | 滚动已知处（如 3028） | 无——INV-S3：滚动同步平移 |
| D6 | `CommitRange` 对已知滚动的覆盖补偿 | `appendOwnedDirectPaintLocked` 尾部 | `ScreenModel.ScrollUp/Down` 后无需覆盖 |
| D7 | `composedPlanLocked` 无条件全量历史组帧 | `fixed_bottom_surface_snapshot.go:156`（`historyRowsWithCursorBlankLocked`） | 只组 `mutableTail`（boundary 之后） |
| D8 | owned path 残留的 legacy reserve debt 状态 | `fixed_bottom_surface.go` 相关字段 | 删除 |
| D9 | `HandoffFrontier.TrimPrefix/Clamp` 与 `historyWindow` 400 行 trim 恢复语义 | `appendHistoryWindowLocked`（1351-1361） | `committedBoundary` 单调计数（D10） |

### 6.2 新增

| # | 新增项 | 说明 |
|---|---|---|
| N1 | `renderengine.ScreenModel.ScrollUp(count)` / `ScrollDown(count)` | front/back 同步平移，等价终端 DECSTBM 滚动；平移后 cursor/软换行状态一并维护 |
| N2 | `committedBoundary int`（或复用 `HandoffFrontier` 仅作计数） | 单调递增；组帧/观测使用，不再参与"哪些行要写"的正确性 |
| N3 | 提交器原语 `appendNewRowsLocked(rows)`（= `AppendNewRows`） | 唯一文本进入 scrollback 的入口；内部复用 `insertHistoryLinesLocked` 的字节构造，**删除窄区/恢复变体** |
| N4 | 提交器原语 `scrollExistingRowsLocked(count)`（= `ScrollExistingRows`） | 纯滚动：模型平移 + 终端滚动序列（无行文本） |
| N5 | mode 标记：`frameMode bool`（boundary==0 判定即可，可无字段） | 组帧分支：Frame mode 全量 / Scrollback mode 只组 tail |
| N6 | `vt.Screen` scrollback 记录扩展（阶段 0 测试基建） | `index()`/`reverseIndex()`/`scrollDown()` 记录推出顶部的行序列（拉回不新增）；RI/SD 从 scrollback 拉回语义——语义断言的测量设施，§4.3 前置依赖 |

### 6.3 收缩

- `historyWindow []string` → **`mutableRows`**：无 trim、无 headroom、无"历史消息"记账语义；上限 = 可见容量 + bottom pane；超限即整体 commit（转移至 Scrollback mode）。**有界性由"append 后超出可见容量即整体 commit"保证，不再依赖 400 行安全网**（`appendHistoryWindowLocked` 1359-1369 的 safety-net 随 D9 删除）；
- `HandoffPlan` 保留，但只服务 `AppendNewRows`；
- 观测层（`/debug` observability）保留 `committedBoundary`、`mutableTail` 长度、mode，删除 frontier/headroom 展示。

---

## 7. 迁移顺序（每阶段独立可提交、可回滚）

| 阶段 | 内容 | 验证 | 通过标准 |
|---|---|---|---|
| 0 | **测试语义改造**：先扩展 `vt.Screen` scrollback 记录（N6，§4.3 依赖）；测试 1 断言改为 scrollback 序列无重复 + 语义行跨 boundary 至多一次；字节计数降级为辅助诊断 | 两测试仍按现状失败（或部分转黄），但失败原因清晰化 | 断言反映 §4.2 语义 |
| 1 | **几何变化去文本提交**：band 出现/消失、bottom 增减改走 `ScrollExistingRows`（模型平移 + 终端滚动序列）；移除 D2/D6 | 测试 2 应通过；测试 1 的 `@3354` 类重复消失 | 测试 2 绿；测试 1 只剩 diff 猜测类重复 |
| 2 | **单调提交器**：`AppendNewRows` 成为唯一文本写入入口；直写路径、全帧路径、reflow 路径全部收敛；移除 Invalidate 补偿（D3/D5） | 测试 1 应通过（`@1749` 类重复消失） | 两测试全绿 |
| 3 | **删除双保留与 trim**：D1/D8/D9；`historyWindow → mutableRows` 整体 commit 语义；**同步迁移 `fixed_bottom_surface_history_window_test.go`**（cap/headroom 测试删除，改写为 committedBoundary 单调 + mutableRows 有界） | 全量 `go test ./cmd/aicli/ui/...` | 无回归 |
| 4 | **组帧收缩**：D7/N5，Frame/Scrollback 分支清晰化；`composedPlanLocked` 只消费 mutable tail | 全量测试 + `/debug` 观测 | 无回归 |
| 5 | **清理**：删除 `HandoffFrontier.TrimPrefix/Clamp`、legacy reserve debt、未用导出符号；文档同步（single-scroll-channel 方案标记为被本文取代） | `go vet` + 全量测试 | 干净 |

> 阶段 0 必须先于一切：当前测试 1 的字节计数断言会在阶段 2 的合法优化（如原位重绘消除）下误报，也会在"权威源重建"场景下误伤。语义先立，重构后行。

---
---

## 8. 验证与风险

### 8.1 验证手段

- **单元/集成**：阶段门禁中的 `go test ./cmd/aicli/ui/... -count=1`；两个专项测试按 §4.3 语义断言。
- **vt.Screen 仲裁**：所有 ANSI 流测试继续用 `vt.Screen` 回放，断言屏幕终态 + scrollback 序列无重复（`ComposedFrameForTest` 对照保留）。
- **真实终端**：Windows Terminal + ConPTY 手动验证 band 出现/消失、连续流式输出、shrink/grow、resize/reflow 场景（沿用 `backend/cmd/conpty-probe/` 观测手段）。
- **观测**：`/debug` observability 展示 `committedBoundary`、mode、mutableTail 长度，验证 INV-S2 单调性。
- **CI 覆盖现状与补强**：当前 hard gate（`release-aicli.yml` validate job）只跑 `./internal/sqlitedriver ./internal/chat ./internal/team ./internal/agentcontrol`，**不含 `cmd/aicli/ui`**；ui 包仅被 soft gate（`verify-release.ps1` 的 `go test ./... -count=1`，ubuntu + `continue-on-error: true`）覆盖。建议在 validate job 增加 `go test ./cmd/aicli/ui/... -count=1`，把渲染回归提升为 hard gate（windows-only 文件由 build tag 自动排除，ubuntu 可跑非 Windows 部分）。
- **TUI 黑盒 e2e：明确不做**（2026-08-02 决定）。仓库曾用 `backend/cmd/conpty-probe/` 验证 ConPTY 基础设施，但 ConPTY 作为自动化测试载体不可靠：读端 EOF 依赖所有写端句柄关闭（子进程退出后 `Read` 可能永久阻塞 → 测试卡住）、经 ConPTY 的 `CreateProcess` 可能返回 `0xC0000142 (STATUS_DLL_INIT_FAILED)`、行为随 Windows 版本与托管端（conhost vs Windows Terminal）漂移，且 CI（ubuntu）无法运行。因此黑盒层**不自动化**，由"设计规避（R4）+ 人工验收清单"承担；`conpty-probe` 保持手动诊断工具定位，不提升为测试辅助。
- **真实终端人工验收**：沿用 `scripts/validate-multi-agent-real-terminal.ps1` §5 清单模式，为渲染专项固化人工清单：band 出现/消失、400+ 行连续滚动、shrink/grow、resize/reflow、`/debug` 观测 `committedBoundary` 单调性（对应 `ui/README.md` 迁移计划第 5 条"在真实 PTY/ConPTY 上完成 resize、退出恢复与四档色深验收"）。

### 8.2 风险与对策

| ID | 风险 | 对策 |
|---|---|---|
| R1 | 模型平移与终端实际滚动不一致（wrapped 行逻辑/物理行换算） | `ScrollExistingRows` 的数量以**物理行**计算（复用 `expandHistoryLinesLocked` 思路）；vt.Screen 回放仲裁 |
| R2 | soft output（可重写尾部）与 committed 部分交互 | soft 窗口被限制在 mutable tail 内（INV-S5），跨 boundary 即整体 commit、放弃重写（与 Codex `clear_scrollback` 语义对齐） |
| R3 | resize/reflow 期间权威源重建的短暂清屏闪烁 | 仅在 frameMode 或明确 reflow 时重建；接受 Codex 式"源重放重发"（§3.3-1） |
| R4 | ConPTY/终端对滚动序列（`\x1bM`/RI/SD）的实际渲染差异，尤其"从 scrollback 拉回"支持度 | **设计规避（不依赖终端行为）**：`ScrollExistingRows` 不把"RI/SD 从 scrollback 拉回"作为正确性前提，统一按"模型平移 + diff 重画恢复行"落地（语义上不算重放，见 §4.2 规则 3），在任何终端/ConPTY 版本下自洽；剩余差异仅由人工验收覆盖（§8.1），不引入 ConPTY 自动化测试 |
| R5 | 大写入性能回退（逐行 flush） | `AppendNewRows` 批量提交（一次 `Flush`），复用 Presenter 批处理 |
| R6 | 迁移中途混合状态（half mode） | 阶段 1-4 每步独立提交；`frameMode` 分支在阶段 4 前允许保守双路径并存（不删除旧路径，只切换调用点） |

### 8.3 结论

简化后的设计：**数据上**从"一份数组三份所有权"变为"两处物理所有者、单一入口提交"；**流程上**从"提交→失效→猜测→补偿"四段链变为"提交或平移→模型自洽→diff"三段链；**状态上**从"账本 + headroom + trim/clamp"变为单调 boundary。删除项（D1-D9）合计超过新增项（N1-N5），且删除的都是产生重复渲染与空白 bug 的补偿机制本身。

评审通过后，按 §7 阶段 0 → 5 执行，阶段 0（测试语义改造）先行单独提交。

---

## 9. 完整性复核记录（2026-08-02）

对本文进行二次完整性审查：逐项核对 §1.3 代码地图与 §6.1 删除清单中的符号与行号（抽查全部命中：`historyWindowMaxLines`=1329、`historyWindowHeadroom`=4195、`appendHistoryWindowLocked`=1334/1356、`shouldAppendDirectLocked`=921、`appendOwnedDirectPaintLocked`=962、`RewriteSoftOutputTail`=1125/1183、`applyOwnedViewportGeometryLocked`=2971/2999/3028、`commitExcessHistoryToScrollbackLocked`=4223、`insertHistoryLinesLocked`=4319、`insertHistoryLinesInRegionLocked`=4338、`HandoffPlan.ANSI`=`handoff_plan.go:45`、`HandoffFrontier.TrimPrefix/Clamp`=`handoff_frontier.go:48/60`、`renderOwnedViewportLocked`/`stageOwnedFrameLocked`/`composedPlanLocked`=`fixed_bottom_surface_snapshot.go:73/108/156`、`ComposedFrameForTest` 与 `/debug`（PaintTrace/RowPlanDebugString）均存在；`ScreenModel` 现有 API 无 `ScrollUp/ScrollDown`，N1 为真实新增），并据此修订：

1. **§4.2 规则 3 措辞精确化**：重放判定以应用 handoff 语义（committed boundary）为准，而非终端物理 scrollback——band 恢复场景下行物理上被推出过、语义上未 handoff，恢复上屏不算重放；
2. **§4.3 补充前置依赖（N6）**：`vt.Screen` 当前不保留滚出行（`ui/vt/screen.go` 的 `index()`/`reverseIndex()` 丢弃/清空；`fixed_bottom_surface_band_restore_duplicate_test.go:25` 注释自述 "A vt.Screen cannot see this"），阶段 0 须先扩展或改用语义行 ID 记账；
3. **§6.3 mutableRows 有界性**：由"append 超可见容量即整体 commit"保证，不再依赖 400 行安全网；
4. **§7 阶段 3 补充测试迁移**：`fixed_bottom_surface_history_window_test.go` 的 6 处 cap/headroom 断言在 D1/D8/D9 落地时必须同步删除/改写（hard cap、`keep=visible+headroom`、wrapped cap 等）；
5. **§8.2 R4 措辞修正**："diff 发底行" → "diff 重画恢复行"（恢复发生在顶部），并补充 RI/SD 从 scrollback 拉回支持度风险。

复核结论：方案骨架完整（定位/基线/根源/对照/语义/架构/清单/迁移/验证/风险全齐），代码引用准确；上述修订消除了两处实质缺口（语义断言测量设施缺失、既有测试迁移遗漏）与三处措辞歧义。文档仍为 **proposed（待评审）**。

追加（2026-08-02，e2e 手段调查）：盘点确认仓库现有 e2e 手段——Playwright browser e2e（runtime-server Web 前端，与 TUI 无关）、`validate-multi-agent-real-terminal.ps1`（真实 aicli.exe 功能探针 + §5 人工清单）、`conpty-probe`（本机 ConPTY 基础设施探测）与 `vt.Screen` 回放（白盒半 e2e）；**TUI 无黑盒 e2e，且 CI hard gate 不含 `cmd/aicli/ui` 包**。据此在 §8.1 追加三项：CI 补强（validate job 增加 ui 包测试）、ConPTY 黑盒 e2e（阶段 2 后新增，复用 conpty-probe 提升为测试辅助）、渲染专项人工验收清单。

再追加（2026-08-02，评审修正）：**确认不使用 ConPTY 自动化测试**（测试会卡住）。ConPTY 作为自动化测试载体不可靠——读端 EOF 依赖所有写端句柄关闭（子进程退出后 `Read` 永久阻塞）、`CreateProcess` 经 ConPTY 可能返回 `0xC0000142 (STATUS_DLL_INIT_FAILED)`、行为随 Windows 版本/托管端漂移，CI（ubuntu）也无法运行。已按此修正：§8.1 移除 ConPTY 黑盒 e2e 条目（改为"明确不做"，`conpty-probe` 保持手动诊断工具定位）；R4 对策从"测试对照 + 退化为 diff"改为**设计规避**——`ScrollExistingRows` 不依赖"从 scrollback 拉回"，统一"模型平移 + diff 重画恢复行"，任何终端下自洽。黑盒层验证责任由人工验收清单承担。
