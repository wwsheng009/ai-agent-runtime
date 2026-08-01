# aicli TUI 渲染引擎（RenderEngine）模块设计

状态：**in progress（A-D 已有增量实现；阶段 E 与 Scene 终局仍未完成）**

更新时间：**2026-08-01**

### 实施审计注记（2026-08-01）

- **阶段 A：基础设施已收口。** coordinator 的四类渲染意图现在经由
  `renderengine.Engine`/单一 `FramePump` 调度；`Presenter` 会先在内存中聚合
  ANSI，再对目标 writer 执行一次帧级写入。当前 `FramePump` 已消除 per-key
  `time.AfterFunc`，并已记录 dirty 位、替换/触发统计和可配置帧间隔；它仍是
  按最近 deadline 唤醒的调度器，尚未实现完整的 scene snapshot backpressure。
  `ActiveStreamController` 的默认拉取式帧预算也已迁为
  `renderengine.FrameGate`/`FrameClock`；旧 `render.FrameScheduler` 只保留为
  兼容的可注入实现，不再是生产默认路径。
- **阶段 B-D：生产路径已接入但边界仍是过渡态。** owned viewport、row plan、
  shared `RenderCache` 均在生产代码中使用；`FixedBottomSurface` 已采用
  coordinator 共享的 `Engine`；`renderengine.ScreenModel`/`Composer` facade
  已建立，Engine 现在显式持有 shared `RenderCache` 并注入
  `ActiveStreamController`；ScreenModel、RowPlan 与 Composer 算法已物理迁入
  `renderengine`，`ui/viewport` 现为兼容转发层；SceneState 与 legacy surface
  的终局迁移仍未完成。
- **阶段 E：已开始但未完成。** `renderengine.HandoffFrontier` 已接管
  `historyHandedOff` 的单调推进、trim 重基和替换 clamp；
  `scrollCompensatedRows`、`pendingScrollDownRows`、`outputScrollDebtRows`、
  `outputCursorOnBlankRow`、soft-output 状态机和 legacy `FixedBottomSurface`
  渲染入口仍存在；因此本文不能标记为完成，终局目标仍需继续按阶段 E 清单迁移和删除。

适用范围：`backend/cmd/aicli/ui`、`backend/cmd/aicli/commands` 中所有与屏幕渲染、输出、历史、ActiveBand、viewport、status、popup、prompt 相关的代码。

关联文档（按职责分层）：

- 架构契约/终局 Scene 设计（Scene、RenderEvent、BoundaryPolicy、handoff frontier、fullscreen lease、P0–P9 阶段）：`docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`；
- owned viewport 双缓冲现状（P5.1 SHADOW MODE）：`docs/plan/aicli-tui-p5-owned-viewport-design.md`；
- 渲染面/数据面迁移母计划：`docs/plan/aicli-tui-render-data-plane-codex-migration-plan.md`；
- 内容渲染 IR、主题、Markdown/Diff 与终端能力：`docs/plan/aicli-ui-ux-rendering-codex-reference-plan.md`；
- ActiveBand/scroll 补偿专项历史（本文宣布其"补丁式演进"阶段终结）：`docs/plan/aicli-activeband-scrollback-compensation-blank-lines-fix-plan.md`；
- 内容渲染实施审查与 Phase 0 清点：`docs/plan/aicli-ui-rendering-implementation-review.md`、`docs/plan/aicli-ui-rendering-phase0-inventory.md`。

> 本文与 unified plan 的关系：unified plan 定义**做什么**（Scene 语义、事件事务、不变量、删除清单）；本文定义**模块怎么建**（RenderEngine 包的内部组件、帧调度、屏幕模型、对账机制、缓存、行所有权仲裁），并把 unified plan 的 `ui/scene`、`ui/layout`、`ui/viewport`、`ui/presenter`、`ui/screenlease` 包边界收敛为一个可运行的权威模块，避免继续在 `FixedBottomSurface` 上叠加补偿状态机。

---

## 0. 执行摘要

当前 TUI 渲染是**补丁式演进**的终态，而不是设计的结果：

- `FixedBottomSurface`（`ui/fixed_bottom_surface.go`，129KB）承载全部职责：布局、历史窗口、ActiveBand、prompt、popup、status、soft output、handoff、补偿状态机、lease、owned viewport 切换——约 150 个 public 方法；
- 渲染由 `chatInteractionCoordinator` 的 **4 个独立 `time.AfterFunc` 循环**驱动（dynamicStatus :638、stableCommit :4350、activeFrame :4891、promptRedraw :5396），没有统一帧调度；
- 屏幕状态靠**手工补偿**保持同步：`scrollCompensatedRows` / `pendingScrollDownRows` / `outputScrollDebtRows` / `outputCursorOnBlankRow`（`applyLayoutWithSizeLocked` :2762 附近），每个新 bug 一个布尔量；
- 双缓冲基础设施（`ui/viewport.Backend`，front/back diff）**已经写好但仍是 SHADOW MODE**，没有生产路径接入；
- markdown 存在**双渲染调用点**（live band `markdown.Render(ActiveBandBodyOptions)` vs 历史 `Formatter.Format(AssistantBodyOptions)`），靠 parity 测试对齐而不是同一代码路径；
- 流式绘制在**业务互斥锁内**执行（`ActiveStreamController.paintLinesLocked`），渲染期间输入与输出全部排队；
- 行级直写终端（`insertHistoryLinesLocked` 硬编码 `io.WriteString(os.Stdout)`），无帧级批量 flush，多 timer 输出可交错。

用户可观察到的三类症状——**卡顿、markdown 内容被吞、viewport/历史/ActiveBand 互相覆盖**——不是三个独立 bug，而是同一个根因：**没有一个拥有"屏幕模型 + 帧调度 + 全量对账 + 行所有权"的权威渲染模块**。

本文定义 `RenderEngine`：一个包、一个帧泵、一个屏幕模型、一套对账机制、一套行所有权仲裁。它是 unified plan 各阶段的**模块落地层**；在它落地前，禁止继续往 `FixedBottomSurface` 加补偿字段（见 §10 冻结清单）。

---

## 1. 现状盘点（问题证据）

### 1.1 入口爆炸：约 150 个 public 方法、多个写终端出口

`FixedBottomSurface` 同时是布局器、状态容器、输出器、补偿器。`grep '^func (s \*FixedBottomSurface)'` 约 150 个方法，调用方（coordinator / slash command / runtime 事件）按需挑选，绕过统一路径的机会极多：

- 输出类：`WriteOutput` / `WriteSoftTrackedOutput` / `AdoptSoftOutputTail` / `RewriteSoftOutputTail` / `insertHistoryLinesLocked`；
- 状态类：`SetActiveBand(Stlyed)` / `SetStatusModels` / `SetDynamicStatusModel` / `SetPromptInputState` / `ShowPopup*` / `BeginPopupInputForOwner`；
- 生命周期类：`Enable` / `Disable` / `BeginOutput` / `SettleOutputDebt` / `SyncTerminalGeometry`。

此外 P0 审计（unified plan §1.2）固化 **155 组 / 552 个 raw terminal call sites** 直接写 `fmt.Print*`/`os.Stdout`，绕过 surface 所有权。**入口越多，对账越不可能**——这是架构问题，不是纪律问题。

### 1.2 多 timer 驱动：没有帧预算

`chat_interaction.go` 中：

- `dynamicStatusTimer`（:638）：状态行 repaint，motion spinner 按动画 cadence 触发；
- `stableCommitTimer`（:4350）：`runActiveStableCommitTick` 稳定前缀滚动提交；
- `activeFrameTimer`（:4891）：`paintScheduledActiveStreamFrame` 流式帧（30FPS 合帧）；
- `promptRedrawTimer`（:5396）：prompt 延迟重绘。

四个 timer **互不感知**，没有共享的帧预算、没有统一的批量 flush、没有全局 dirty 位。同一时刻可能 status repaint 与 active frame paint 与 stable commit 同时写终端，每帧多次 syscall，Windows ConPTY 上放大为肉眼可见卡顿。

### 1.3 补偿状态机：每个 bug 一个布尔量

`applyLayoutWithSizeLocked`（:2762）维护：

- `scrollCompensatedRows`：reserve 行数增长/收缩后的滚动补偿；
- `pendingScrollDownRows`：延迟下滚（先清 stale 行再滚，避免擦掉已移动内容）；
- `outputScrollDebtRows`：被吸收的空行产生的"欠账"；
- `outputCursorOnBlankRow`：cursor 是否停在输出区底部空行。

这些布尔量**彼此耦合**（同尺寸分支里三者互相抵消/叠加），任何新组合（band 增长 + popup 打开 + status 出现 + resize）都会产生新的未覆盖分支。补丁式修复（`compensation_top_bug_test`、`scrollback-compensation-blank-lines-fix-plan`）只覆盖了已发现的组合。

### 1.4 双缓冲 shadow mode：基础设施写好了没人用

`ui/viewport.Backend`（`backend.go`）已经是完整的 front/back 双缓冲 + diff Flush 实现，但文件头注释明确写着：**"P5.1 status: SHADOW MODE. No production render path constructs a Backend yet"**。双缓冲 diff 的正确性只在测试中验证过，生产路径仍在用"手工算行号 + 行级直写"。

### 1.5 双渲染调用点：markdown 直播与历史各渲染一次

- 直播 band：`ActiveStreamController.activeDocumentLocked`（`active_stream.go` :458）→ `markdown.Render(ActiveBandBodyOptions)`（自带 Highlighter、`HideHighlightFallback=true`）；
- 历史/scrollback：`Formatter.Format` → `AssistantBodyOptions`（`formatter/markdown.go` :174-176）。

两者共享 `ApplyAssistantBodyContract` 并有 parity 测试对齐，但**不是同一代码路径**：同一份 markdown 源码在 band 与历史里各渲染一遍，缓存互不共享，样式/空白差异只能靠测试兜底。

### 1.6 锁内渲染：渲染期间输入与输出全部排队

`ActiveStreamController.paintLinesLocked`（`active_stream.go` :379）在 `c.mu` 内执行 `markdown.Render`（goldmark + Chroma，O(源大小)）+ `Buffer.Render` + `Diff`。`chatInteractionCoordinator` 的各 timer 回调再套 `c.mu`/`s.mu` 嵌套锁。流式输出越快、markdown 越长，锁持有越久——**打字、滚动、resize 全部被渲染阻塞**，这是卡顿的直接来源。

### 1.7 行级直写：无帧级批量输出

`insertHistoryLinesLocked`（`fixed_bottom_surface.go` :4328）硬编码 `io.WriteString(os.Stdout)`，按行写；`Terminal` 方法用 `fmt.Print` 直写。对比 codex 的做法：ratatui 每帧 cell diff 后**一次**批量 flush，且由 synchronized update 包裹，杜绝交错。

---

## 2. 设计目标与非目标

### 2.1 目标

1. **单一权威渲染模块**：所有屏幕输出（transcript、ActiveBand、prompt、popup、status、fullscreen）必须经过 `RenderEngine` 的唯一入口；模块对外暴露**意图级 API**（`Update(scene)` / `Invalidate(reason)`），不暴露行号与 ANSI。
2. **帧级对账收敛**：每一帧从权威 Scene 重新合成屏幕模型（内存前端缓冲），与上一帧 diff 后输出；任何增量路径算错行数，**下一帧自动纠正**——不再依赖手工补偿状态机。
3. **可预测性能**：单一帧泵按 FPS 预算合并请求；渲染缓存（markdown Document、行、样式）命中时跳过重渲染；渲染不在业务锁内执行。
4. **组件解耦**：状态生产者（coordinator、stream controller、slash command）不再持有屏幕坐标；坐标只存在于 Composer 与 ScreenModel 中。
5. **行所有权显式化**：屏幕每一行都有 owner（transcript / band / prompt / popup / status / gap），行分配由布局求解器一次性算出，任何组件不能自行"借用"行。

### 2.2 非目标

1. **不做完整立即模式**：不引入 ratatui 式"每事件全树重绘"，保留"脏区 + 合帧 + diff"的增量模型——本项目是 Go 直写终端，目标不是复刻 Rust 生态，而是获得 codex 的**收敛性**（每帧对账）与**批量输出**（帧级 flush）。
2. **不一次性重写**：`FixedBottomSurface` 先作为 facade 保留，内部职责按 §7 逐步委托给 RenderEngine 组件；迁移期间业务代码改动最小。
3. **不改变交互语义**：prompt 编辑、popup 交互、fullscreen lease 的用户可见行为不变，只换渲染管道。
4. **不做 Scene 语义迁移**：cell identity、BoundaryPolicy、semantic cell 属于 unified plan P4–P6 范围；本文只要求 ScreenModel 的行级对账，不要求 Transcript 先迁移到 cell 模型。

### 2.3 成功定义（可测量）

- 卡顿：流式期间输入事件到屏幕反馈延迟 < 50ms（现状在长 markdown 下可达数百 ms）；每帧终端写调用次数从"多次小写"降到"一次批量 flush"。
- 内容被吞：对同一随机事件序列（streaming + resize + status + band grow/shrink + popup），重放后屏幕与内存 ScreenModel **逐格一致**（reconcile 不变量测试）。
- 协调：任何时刻屏幕行 owner 集合构成完整覆盖（无未声明行），且与 Scene 派生布局一致（布局不变量测试）。
- 打补丁终止：§10 冻结清单生效后，`FixedBottomSurface` 新增方法数 / 新增补偿字段数为 0，新增渲染逻辑必须进入 RenderEngine。

---

## 3. 模块边界与数据流

### 3.1 总体数据流

```text
状态生产者（coordinator / ActiveStreamController / slash command / runtime 事件）
        │  意图级 API：Update(ScenePatch) / Invalidate(reason)
        ▼
┌───────────────────────── RenderEngine（唯一渲染权威）─────────────────────────┐
│  Engine 门面（单入口，内部持有下列组件）                                          │
│    SceneState ──► FramePump（帧调度/合并/背压）                                   │
│                      │ 帧到期                                                      │
│                      ▼                                                           │
│    Composer（布局求解：Scene → ScreenModel + 行所有权表）                          │
│                      │                                                           │
│                      ▼                                                           │
│    ScreenModel（内存终端 front 网格；reconcile 目标）                              │
│                      │                                                           │
│                      ▼                                                           │
│    Presenter（front→back diff；批量 ANSI；同步更新；handoff 原语）                 │
│                      │                                                           │
│                      ▼                                                           │
│    RenderCache（DocumentCache / 行缓存 / 样式缓存，被 Composer 查询）              │
└──────────────────────────────────────────────────────────────────────────────────┘
        │ 一次批量 flush（synchronized update 包裹，可含 scrollback handoff 序列）
        ▼
   物理终端（主屏或 lease 的 alternate screen）
```

### 3.2 模块位置与包边界

建议新建 `ui/renderengine` 包（也可按 unified plan §4.4 拆为 `ui/scene`、`ui/layout`、`ui/viewport`、`ui/presenter`，本文按单包设计，内部目录级分层，避免多包互依赖的工程成本）：

```text
cmd/aicli/ui/renderengine/
  engine.go          Engine 门面：Update/Invalidate/Flush/Resize/Lease 接入
  frame_pump.go      FramePump：单一 ticker、dirty 位、FPS 预算、合并、背压
  scene.go           SceneState：当前权威 UI 状态（与 unified plan TuiScene 对齐的骨架）
  screen.go          RowPlan/Compose：行所有权表、布局求解与 ui/vt 互转
  screen_model.go    ScreenModel：内存终端网格与 front/back diff
  composer.go        Composer：布局求解（行分配、wrap、gap、owner 标注）
  presenter.go       Presenter：diff、批量 ANSI 输出、synchronized update、handoff
  cache.go           RenderCache：markdown Document 缓存、行缓存、样式缓存
  handoff_frontier.go HandoffFrontier：scrollback 交接边界（单调）
  ownership.go       行所有权类型与仲裁规则
```

约束：

- `renderengine` **不导入** `commands` 包；`commands` 只通过 Engine 的意图级 API 交互。
- 包内所有组件**不直接写终端**，终端写入只发生在 `Presenter` 的 `Flush` 中；handoff 字节也由 `Presenter` 统一输出（消灭 `insertHistoryLinesLocked` 的 os.Stdout 硬编码）。
- `ui/viewport.Backend` 的 front/back diff 逻辑**并入** `screen_model.go`（复用其测试资产），不再维持 shadow mode。

### 3.3 与现有代码的边界

| 现有组件 | 迁移后角色 |
| --- | --- |
| `FixedBottomSurface` | facade：`Enable/Disable/Lease` 委托 Engine；渲染方法逐步删除 |
| `chatInteractionCoordinator` | 纯状态生产者：只调 `Engine.Update(...)` / `Engine.Invalidate(...)` |
| `ActiveStreamController` | 纯内容源：产出 stable/tail 内容，不再持有 Buffer/调度（FrameScheduler 并入 FramePump） |
| `ui/viewport.Backend` | 被 `screen.go` + `presenter.go` 吸收（diff 逻辑复用） |
| `ui/render.FrameScheduler` | 被 `FramePump` 吸收（单一调度） |
| `ui/screen_lease.go` | 保留，作为 Engine 的 lease 后端（P3 已完成，直接接入） |
| `ui/markdown`、`ui/syntax`、`ui/style`、`ui/vt` | 纯库，被 Composer/Cache 调用，职责不变 |

---

## 4. 组件设计

### 4.1 Engine 门面（唯一 API）

```go
// Engine 是渲染唯一入口。所有 UI 状态变更通过意图级 API 提交，
// 不暴露行号、ANSI 或终端句柄。
type Engine struct {
    pump      *FramePump
    scene     *SceneState
    composer  *Composer
    screen    *ScreenModel
    presenter *Presenter
    cache     *RenderCache
    handoff   *HandoffFrontier
}

// Update 应用一次状态补丁并标记 dirty（低开销；不渲染）。
func (e *Engine) Update(p Patch)          // 合并：同帧多次 Update 只触发一次渲染
// Invalidate 强制整帧重绘（resize、lease release、reconcile 定时兜底）。
func (e *Engine) Invalidate(reason string)
// Flush 立即产出当前帧（测试与退出路径用；正常路径由 FramePump 驱动）。
func (e *Engine) Flush(now time.Time) error
// Resize 更新几何并把需要重排的区域标 dirty。
func (e *Engine) Resize(width, height int)
// AcquireLease/ReleaseLease 委托 screen_lease；release 时 Invalidate(full repaint)。
func (e *Engine) AcquireLease(owner string) error
func (e *Engine) ReleaseLease(owner string) error
// Snapshot 返回当前 ScreenModel 的文本/网格视图（/debug display、测试、属性验证用）。
func (e *Engine) Snapshot() ScreenSnapshot
```

不变量：

- 同一时刻只有一个 Engine 实例处于 active 状态（配合 lease）；
- `Update` 与 `Flush` 之间可以合并任意多次变更；
- `Flush` 输出必须是**一次**批量序列（见 4.5），不允许部分输出泄漏到下一帧。

### 4.2 FramePump（帧泵）

```go
type FramePump struct {
    ticker   *time.Ticker   // 唯一时钟源（默认 30FPS，motion 需要时提升到动画 cadence）
    dirty    uint32         // dirty 位图（content / band / status / prompt / popup / geometry / full）
    lastAt   time.Time
    maxFPS   int
    // backpressure: 帧在渲染中时，新的 Update 只置位，不并发渲染
    rendering atomic.Bool
}
```

规则：

1. **单一 ticker**：取代 4 个 `time.AfterFunc`。所有请求（Update/Invalidate/动画）只置 dirty 位；
2. **帧预算**：每 tick 检查 dirty，到期渲染一帧；motion spinner 请求将 next 到期时间提前到动画 cadence（保留现有 `NextFrameDelay` 语义，但由 pump 统一仲裁）；
3. **背压**：渲染中到达的 Update 只置位，下一帧合并——**渲染绝不在业务 goroutine 的锁内执行**（pump 在自己的 goroutine 或调用方指定的渲染 goroutine 中跑）；
4. **不变量**：两帧之间至少间隔 `1/maxFPS`；一次 Flush 的输出字节由 Presenter 收集后一次性写出。

### 4.3 ScreenModel（屏幕模型 + 行所有权表）

```go
type ScreenModel struct {
    width, height int
    rows          []Row        // 与终端 1:1 的物理行
    owners        []RowOwner   // 每行归属：Transcript/Band/Prompt/Popup/Status/Gap
    front         [][]vt.Cell  // 上一帧（diff 基线）
}

type RowOwner uint8 // owner + 可选的 owner 序（如 band row 3）
```

关键点：

- ScreenModel 是**内存中的终端**：`ui/vt.Screen` 已经能做 ANSI 解释与 wrap，ScreenModel 以 vt.Cell 网格存储，`Presenter` diff 后输出；
- **行所有权表**与网格并行维护：Composer 每次布局**全量重算** owner 映射，任何组件不能单独写某行——从机制上消灭"band 增长后历史区空行"、"popup 关闭后残留行"这类所有权漂移；
- `reconcile`（§5.2）以 ScreenModel 为唯一对账目标。

### 4.4 Composer（布局求解器）

```go
type Composer struct {
    cache *RenderCache
}

// Compose 从权威 Scene 派生完整屏幕模型：行分配 + wrap + gap + owner 标注。
// 结果必须是确定性的：相同 Scene + 相同几何 → 相同 ScreenModel。
func (c *Composer) Compose(scene *SceneState, w, h int) *ScreenModel
```

职责：

1. **布局求解**：自底向上分配——status（0/1 行）、popup（可变）、prompt（可变）、ActiveBand（可变）、transcript（剩余）；所有行号由求解器一次性算出，输出 `RowPlan`（每个区域的行区间）；
2. **wrap 与 gap**：行展开用统一原语（复用 `expandHistoryLinesLocked`/`historyCellsToPlainText` 的单一展开路径，见 unified plan §7 gap policy 的行级版本）；
3. **owner 标注**：每个物理行标记归属，供调试、测试与增量优化使用；
4. **缓存查询**：markdown 区域先查 RenderCache，命中则跳过重渲染（§4.6）。

Composer 是**纯函数**（Scene+几何 → ScreenModel），这是它可测试与可对账的前提。

### 4.5 Presenter（输出器）

```go
type Presenter struct {
    out io.Writer      // 唯一终端写入点（测试可注入）
    front [][]vt.Cell  // 上一帧基线（与 ScreenModel 同步）
}

// Flush 将 screen 与 front 的 diff 编译为一条 ANSI 序列并一次性写出。
// 序列整体用 synchronized update（ESC[?2026h … ESC[?2026l）包裹；
// 需要 scrollback handoff 时，handoff 序列作为同一帧的尾部（或头部）输出。
func (p *Presenter) Flush(screen *ScreenModel, handoff *HandoffPlan) error
```

规则：

1. **一帧一次写**：diff 编译成单条 `[]byte`，一次 `Write`；消灭行级直写；
2. **同步更新包裹**：所有帧输出包在 synchronized update 中（不支持时降级为无包裹，但帧内顺序仍原子）；
3. **handoff 原语**：scrollback 交接（DECSTBM + `\r\n` 滚动）由 Presenter 基于 `HandoffPlan`（行数、样式源）生成；**交接计数与 diff 计数同源**，杜绝 `commitExcessHistoryToScrollbackLocked` 与 `repaintActiveBandDiffLocked` 行数不一致；
4. **失败处理**：Write 失败 → front 标记 invalid，下一帧强制 full repaint（与 unified plan §16.2 "partial terminal write" 测试矩阵一致）。

### 4.6 RenderCache（渲染缓存）

```go
type RenderCache struct {
    docs   map[DocKey]*render.Document   // markdown Document 缓存
    rows   map[RowKey][]render.Line      // 已展开行缓存（band/prompt 常用）
    styles atomic.Pointer[StyleSet]      // 主题/语法主题版本
}

type DocKey struct {
    Source string // 完整 markdown 源码（或内容 hash）
    Width  int
    Theme  string
    Mode   string // ActiveBandBodyOptions / AssistantBodyOptions（收敛后同一路径）
}
```

现状与问题：

- `ActiveStreamController` 已有 `markdownDocSource/Width/Theme` 三级缓存（`active_stream.go` :458-468），但它是 **per-stream 私有缓存**：每次新流都重建，且只缓存当前整份文档，不缓存历史 replay 的文档；
- 双渲染调用点（§1.5）各持有自己的渲染结果，缓存互不共享。

目标：

1. **全局缓存**：`RenderCache` 挂在 Engine 上，band 与历史共用；key 含 mode，收敛到单一渲染路径后 mode 消失；
2. **内容寻址**：key 用源码内容 hash + width + theme + mode，避免长字符串比较；`active_stream.go` 的私有缓存删除；
3. **失效规则**：theme 切换 → 全清；width 变化 → 按 width 分片失效；内存上限（如 64MB / 256 个文档）→ LRU 淘汰；
4. **指标**：命中率、重建次数、平均渲染耗时（§9）。

### 4.7 Ownership（行所有权仲裁）

```go
type RowOwner uint8

const (
    OwnerTranscript RowOwner = iota
    OwnerBand
    OwnerPrompt
    OwnerPopup
    OwnerStatus
    OwnerGap
)
```

仲裁规则（Composer 唯一裁决者）：

1. **从底向上分配**：status（底部固定）→ popup（status 之上）→ prompt（popup 之上）→ band（prompt 之上）→ transcript（剩余全部）；与现有 `effectiveBottomRowsLocked` 语义一致，但由求解器一次性计算；
2. **owner 不可叠加**：任何物理行只有一个 owner；gap 也是显式 owner，不允许"未声明行"存在；
3. **增量优化许可**：diff 渲染允许只重绘 owner 变化或内容变化的行，但**对账**（reconcile）必须全量校验 owner 表与 Scene 派生一致；
4. **调试输出**：`/debug display` 可渲染 owner 表（每行前缀），直接暴露"某行是谁的、为什么在这里"。

---

## 5. 关键机制

### 5.1 帧合并与背压

- 所有变更请求（内容 delta、状态行、band、prompt、popup、motion）只置 dirty 位；FramePump 每 tick 检查；
- 渲染在 pump 的渲染 goroutine 中执行，**不持有业务锁**（`chatInteractionCoordinator.mu`、`ActiveStreamController.mu` 只保护状态读写，不保护渲染）；
- 背压：渲染中到达的 Update 合并到下一帧；单帧渲染超时（如 > 100ms）记录指标并允许下一帧打断（增量 diff 天然支持断点续传——因为 front 基线总是上一帧完整状态）。

### 5.2 reconcile（全量对账兜底）

这是本设计**对抗"被吞/漂移"的核心机制**，直接对应 codex 每帧全量 diff 的收敛性：

- **触发时机**：resize 后、lease release 后、stream finalize 后、popup 关闭后、以及**周期性兜底**（如 1s 一次，仅 dirty 时）；`/debug display` 可强制触发；
- **执行方式**：Composer 从 Scene 全量重算 ScreenModel，与当前 front 逐格 diff——**不是"假设之前的增量是对的"**，而是"以 Scene 为真源重建"；
- **成本控制**：reconcile 只发生在 dirty 且需要收敛的时刻；正常 streaming 时仍是增量 diff（Composer 可复用 RowPlan 缓存，只对变化区域重算）；
- **验收**：属性测试"重放任意事件序列后 reconcile 必然使 front == Compose(Scene)"，等价于 codex 的 `Front valid => VT(screen) == presenter front`。

### 5.3 resize / reflow

统一策略（对齐 codex `transcript_reflow.rs` 与 unified plan §12）：

- **内存源是真源**：ScreenModel/Scene 始终持有完整行内容（source-backed）；resize 后 Composer 按新宽度重新展开，Presenter 对 visible 区域整帧重发（diff 后输出）；
- **已 handoff 部分**：交给终端按新宽度自动重排（codex 明确"终端拥有已写出行"）；内存中只重算**未 handoff** 窗口与所有 owned 区域（band/prompt/popup/status）；
- **流式期间的 resize**：标记 `defer-refresh`，流 finalize 后再做一次 source-backed reconcile（unified plan §12.3 的"流式期重排延迟到 source-backed 后"）；
- **删除**：`scrollCompensatedRows` / `pendingScrollDownRows` / `outputScrollDebtRows` / `outputCursorOnBlankRow` 全部删除——它们存在的理由（"终端已滚动但内存不知道"）被"resize/结构变化后整帧 reconcile"取代；
- 保留一个关键兼容点：**soft output tail 的原地重写**（`RewriteSoftOutputTail`）在过渡期仍可用，但语义改为"改写 ScreenModel 中对应 owner=Transcript 的行并整帧 diff"，不再单独维护"软尾巴"状态。

### 5.4 handoff 原语化

- `HandoffFrontier`（单调边界）从 `historyHandedOff` 提升为 Engine 级组件，与 ScreenModel 行数同源；
- handoff 由 Presenter 在帧尾部执行：**先 diff 输出可见区，再在同一同步更新块内滚动 handoff 行**（顺序：写可见内容 → 滚动 → 更新 frontier），杜绝"写一半、滚一半"；
- wrap 段展开（`expandHistorySegmentToPhysicalTextLocked`）进入 Composer 的共享展开路径，handoff 计数与可见区 wrap 计数必然一致（消灭 §1.7 的行数错位）；
- 测试：现有 `TestHistoryWindow*` 全部保留并增加"handoff 后立即 reconcile 屏幕一致"断言。

### 5.5 markdown 流式与缓存

- **单一路径**：live band 与历史 replay 统一走 `markdown.Render(Formatter 契约)` + RenderCache；`ActiveBandBodyOptions` 与 `AssistantBodyOptions` 合并为 `Formatter.FormatDocument` 的差异化参数（highlighter、holdback），收敛 unified plan §15.6 的双渲染调用点；
- **stable/tail 提交**：`CommitStablePrefix` 语义保留（偏移量），但改由 Engine 处理：stable 前缀内容从 band 区域**移交**到 transcript 区域并触发 reconcile，不再由 coordinator 手工算 rows delta；
- **holdback**：table holdback 状态仍由 StreamCollector 判定，但渲染侧只做"该区域保持 band owner、不 handoff"，不再散落多个判定点。

### 5.6 锁策略

| 现状 | 目标 |
| --- | --- |
| 渲染在 `ActiveStreamController.mu` / `FixedBottomSurface.mu` 内执行 | 业务锁只保护状态读写；渲染在 pump goroutine 内 |
| 状态读取与渲染共用同一把锁 | Scene 快照（immutable snapshot）交付给 Composer；pump 持有 snapshot 期间业务可继续 Update |
| 多 timer 并发写终端 | 唯一 flush 路径（Presenter），天然串行 |

- Scene 更新采用 **copy-on-write snapshot**：`Update` 在业务锁内快速生成新 snapshot（数据引用共享），Composer 在无锁下消费 snapshot；
- `FixedBottomSurface` 的 `mu` 逐步缩小为 facade 的薄锁，最终随 facade 一起删除。

---

## 6. 现有代码迁移映射

| # | 现有职责（文件/方法） | 迁往（组件） | 迁移要点 |
| --- | --- | --- | --- |
| 1 | 布局与滚动区（`applyLayoutWithSizeLocked`、`appendApplyLayoutSequenceWithSizeLocked`、`refreshTerminalDimensionsLocked`） | Composer + Presenter | 布局成为纯函数；滚动区序列由 Presenter 生成 |
| 2 | 补偿状态机（`scrollCompensatedRows`/`pendingScrollDownRows`/`outputScrollDebtRows`/`outputCursorOnBlankRow`） | **删除** | 由 §5.3 reconcile 取代 |
| 3 | 历史窗口（`appendHistoryWindowLocked`、`replaceOwnedHistorySuffixLocked`、`ownedHistorySuffixStartLocked`、`canRewriteOwnedHistorySuffixLocked`） | SceneState（transcript 区） + Composer | `historyWindow []string` 过渡期保留，但只作为 Scene 数据，不再参与行号计算 |
| 4 | handoff（`commitExcessHistoryToScrollbackLocked`、`historySegmentIsSinglePhysicalRowsLocked`、`insertHistoryLinesLocked`） | HandoffFrontier + Presenter | os.Stdout 直写删除；计数与 diff 同源 |
| 5 | ActiveBand（`SetActiveBand`/`SetActiveBandStyled`/`repaintActiveBandDiffLocked`/`RefreshActiveBand`/`ClearActiveBand`/`renderActiveBandRowLocked`） | SceneState（band 区） + Composer + RenderCache | diff 渲染由 ScreenModel 驱动；`repaintActiveBandDiffLocked` 的 prev 对比逻辑删除 |
| 6 | prompt（`SetPromptRows`/`TrackPromptInputState`/`reflowPromptViewportLocked`/`renderPromptRowsLocked`/`MoveToPromptCursor`） | SceneState（prompt 区） + Presenter | cursor 意图进入 Scene；Presenter 帧尾统一放置 cursor |
| 7 | popup（`ShowPopup*`/`BeginPopupInputForOwner*`/`UpdatePopupInputForHandle`/`clearPopupStateLocked` 等约 20 个方法） | SceneState（popup 栈） + Composer | popup 只声明"占用 N 行"，行分配由求解器完成 |
| 8 | status（`SetStatusModels`/`SetDynamicStatusModel`/`repaintStatusUpdateLocked`/`renderStatusLocked`） | SceneState（status 区） + Composer + FramePump | 动画 cadence 由 pump 统一；`repaintStatusUpdateLocked` 的直写删除 |
| 9 | soft output（`WriteSoftTrackedOutput`/`AdoptSoftOutputTail`/`RewriteSoftOutputTail`/`invalidateSoftOutputLocked`） | SceneState + Presenter | 语义改为 ScreenModel 行改写 + 整帧 diff；soft 状态机删除 |
| 10 | 双缓冲 shadow（`ui/viewport.Backend`） | `screen.go` + `presenter.go` | diff 逻辑复用；SHADOW MODE 注释删除 |
| 11 | 调度（`ui/render.FrameScheduler`、coordinator 4 个 timer） | `frame_pump.go` | 4 个 AfterFunc 删除 |
| 12 | 流式渲染（`ActiveStreamController.paintLinesLocked`、`activeDocumentLocked`、`markdownDoc*` 缓存） | Composer + RenderCache + SceneState | controller 变为内容源；锁内渲染删除；私有缓存删除 |
| 13 | lease（`ui/screen_lease.go`） | Engine.AcquireLease/ReleaseLease | 语义不变；release 触发 reconcile |
| 14 | `renderOwnedViewportLocked` 合成帧（historyWindow + band 一帧合成） | Composer.Compose | 合成逻辑整体搬入 Composer |

---

## 7. 分阶段实施计划

与 unified plan 的 P0–P9 并行但不重复：本文阶段是**模块内部里程碑**，每个阶段结束都有可运行的验证目标；阶段之间独立可回退。

### 阶段 A：帧泵 + 批量输出（先解决卡顿，风险最低）

**状态：已实施（2026-08-01）**，见 `cmd/aicli/ui/renderengine/` 与 `chat_interaction.go`。

- 新建 `renderengine` 包骨架（`frame_pump.go`、`engine.go`）；
- 把 coordinator 的 4 个 timer 收敛为单一 FramePump：timer 回调改为 `Engine.Invalidate(reason)` / dirty 置位，渲染回调集中执行现有各 `paint*`/`repaint*` 方法（**行为不变，只是调度收敛**）；
- FramePump 维护 `DirtyFlags` 联集、调度替换/触发统计和可配置 `MaxFPS` 帧预算；旧 `Schedule` 调用默认归入 `DirtyExternal`，不破坏兼容路径；
- Presenter 第一版：收集一帧内所有输出字节，synchronized update 包裹，一次 Write；
- **验收**：流式场景 syscall 数显著下降；`FramePump`/`FrameClock` 的合并与帧预算测试通过；现有 4 组 timer 测试改走 pump 后全绿。旧 `FrameScheduler` 测试仍保留，用于验证兼容注入实现。

### 阶段 B：ScreenModel + Composer 接入 owned 区域（解决被吞/漂移）

**状态：已实施（2026-08-01）**，`ui/renderengine`（ScreenModel/RowPlan/Composer）与 `renderOwnedViewportLocked` 生产接入完成；`ui/viewport` 保留兼容 API。

- `screen.go`/`screen_model.go`/`composer.go` 落地；`renderOwnedViewportLocked` 的合成逻辑搬入 Composer（historyWindow + band 一帧合成改为 ScreenModel 全量合成 + diff）；
- owned surface 已仅使用 `renderengine.ScreenModel`、`PlanRow`、`RowOwner` 和
  `PlanCells`；双缓冲 diff 与行所有权算法现由 `renderengine` 直接实现，旧
  `ui/viewport` API 仅作兼容转发；
- Presenter 保持帧级批量输出职责，ScreenModel 负责 front/back diff；shadow
  mode 已删除；
- owned viewport 的完整帧、direct-scroll 后续 diff 和 reconcile 输出统一经由
  Presenter；即使测试/过渡装配未预先注入 Presenter，`flushHoldingLock` 也会
  创建兜底实例，不再从 owned 路径退回 `fmt.Print`；
- reconcile 接入：resize、lease release、finalize、popup 关闭后强制整帧对账；
- 终端写锁与 DEC 2026 状态下沉到 `renderengine`（`terminal_lock.go`），ui 包转发，避免 ui→renderengine→ui 循环依赖；`renderOwnedViewportLocked` 输出改走 Engine 的 `FlushHoldingLock` 门面；
- popup 清理方法（`ClearPopup*` 族）在 owned 模式下走 reconcile 整帧路径，不再走 legacy 清空分支；
- 新增公有 `Reconcile()`（`fixed_bottom_surface_snapshot.go`），供 reconciliation 时机调用；
- **验收**：属性测试"任意事件序列后 reconcile 收敛"通过（`fixed_bottom_surface_reconcile_test.go`：内容事件序列收敛、resize 后收敛、重复 reconcile 幂等）；`TestHistoryWindow*`、`TestFixedBottomSurface*` 全部保持绿；resize 场景的补偿状态机分支测试改为"reconcile 后一致"断言。

### 阶段 C：行所有权仲裁（解决 viewport/历史/ActiveBand 冲突）

**状态：已实施（2026-08-01）**，`ui/viewport/rowplan.go`（RowOwner/PlanRow/ComposePlan）与 owned 帧生产路径接入完成。

- `rowplan.go` 落地：`RowOwner` 枚举（Gap/Transcript/Band/Prompt/Popup/Status，零值 Gap）、`PlanRow{Owner,Cells}`、`ComposePlan`（全屏 plan 带每行 owner 标注）；`Compose` 收敛为 `ComposePlan` 的兼容包装（`compose.go` 保留 P5.2 布局注释与语义：历史 bottom-aligned、reserve 占最后 N 行、其余 Gap）；
- 所有 paint 计划输出 owner：band/notice/dynamic/prompt 行（`promptPaintPlanLocked`）、popup/composer 行（`popupPaintPlanLocked`）、status 行；`bottomRowsWithOwnersLocked` + `bottomOwnerMapLocked` 将 bottom reserve 每个物理行映射到组件 owner，margin/gap 行显式标 Gap——「无未声明行」；
- `renderOwnedViewportLocked` 改走 `composedPlanLocked`（历史行标 Transcript），刷新 `lastRowOwners`；`/debug display` 增加 Row Ownership (stage C) 表（`RowPlanDebugString`）；`RowOwnersForTest` 供测试取 owner 表；
- band/prompt/popup/status 全部改为"声明占用行数"，行分配由求解器完成；
- **验收**：布局不变量测试通过（`fixed_bottom_surface_ownership_test.go`：owner 全覆盖且每行均为已声明枚举、内容行必有组件 owner（band/popup/notice/prompt/dynamic/status 各归其主）、band gap 行声明为 Gap、owner 表渲染行数=屏高）；既有 band grow/shrink、popup 开关、status 出现/消失的组合测试全部保持绿（ui 包全量回归通过）。

### 阶段 D：缓存与单一路径（解决性能与双渲染调用点）

**状态：已实施（2026-08-01）**，`ui/renderengine/cache.go`（RenderCache）与共享缓存接入完成。

- `RenderCache` 落地（`ui/renderengine/cache.go`）：DocKey = 内容 hash（fnv64a）+ width + theme 指纹 + mode；LRU 条目上限（默认 256）和 64 MiB 确定性字节预算（源码/IR 文本/结构估算）+ 源码二次校验防碰撞；命中/未命中/驱逐指标与 `HitRate`；`SharedRenderCache()` 进程级单例；超过总预算按 LRU 驱逐，单个超限文档直接跳过缓存而不清空热条目；
- `Engine` 显式持有 `SharedRenderCache()`，coordinator 创建 `ActiveStreamController` 时通过 `SetRenderCache` 注入，独立 controller 仍保留 nil fallback 兼容语义；
- `RenderCache.Render` 的 Markdown/goldmark 慢路径已移出 cache mutex，只在查找、发布和 LRU 更新阶段持锁，避免长文档渲染阻塞其他 stream 的命中读取；
- 同一 `(DocKey, source)` 的并发 miss 通过 in-flight 合并：只有首个
  请求执行 `markdown.Render`，其余请求等待发布后按命中重试；`Reset` 使用
  generation 阻止 reset 前的慢渲染把过期文档回写到新缓存；
- `ActiveStreamController` 私有 markdown 缓存（`markdownDoc*` 四字段）删除：`activeDocumentLocked` 改走 `bandFormatter`（`Formatter.FormatDocumentCached`），缓存未命中等价旧 bodyChanged 语义；frame 组合缓存（`markdownFrameDoc/Hold/Title`）保留；
- `Formatter.FormatDocument`/`FormatDocumentCached` 成为唯一 markdown 渲染路径：生产代码 `markdown.Render` 调用点收敛到 `renderengine/cache.go` 一处（其余为测试/基准）；band 差异化只剩 highlighter 注入、`HideHighlightFallback`、`TrustMarkdown` 三个 formatter 选项，mode="band" 与 scrollback 的 "assistant"/"plain" 分组共享同一缓存实例；
- **验收**：`TestActiveStreamControllerRebuildsMarkdownCacheForSyntaxTheme` 改为断言共享缓存命中/未命中（初始 miss、稳定帧 hit、theme 切换 miss、resize miss），markdown parity 测试不变且全绿；性能量化：`BenchmarkRenderCacheHit` 876ns/op vs `BenchmarkMarkdownRenderRaw` 707µs/op（≈800x），`BenchmarkActiveMarkdownRepaint100KiB` 281µs/op / 60 allocs（初始渲染含一次 goldmark 为 493µs / 2120 allocs，重绘路径不再重复解析）；`ui`/`formatter`/`renderengine` 全量回归通过。

### 阶段 E：删除补偿状态机与旧入口（终结补丁模式）

- **状态：进行中（2026-08-01）。** `Engine.HandoffFrontier()` 已落地并由
  owned surface 共享，handoff 边界不再是裸 `historyHandedOff` 整数；其余 legacy
  状态机仍需在 capability fallback 收敛后删除。
- 删除 §6 表中标注"删除"的全部方法/字段：补偿状态机、`insertHistoryLinesLocked` 直写、`repaintActiveBandDiffLocked` 的 prev 逻辑、soft output 状态机、4 个 timer、`FixedBottomSurface` 的渲染方法（facade 只剩 Enable/Disable/Lease 委托）；
- P0 审计基线（155 组/552 call site）随迁移逐项从基线删除，最终 `tui_unowned_terminal_write_total` 归零；
- **验收**：`FixedBottomSurface` 体积从 129KB 降到薄 facade；全文搜索 `fmt.Print`/`os.Stdout` 在 owned 路径为 0（plain/json renderer 除外）。

---

## 8. 冻结清单（从现在生效）

在 RenderEngine 落地前，以下行为**禁止**，防止继续打补丁：

1. **禁止向 `FixedBottomSurface` 新增补偿类字段**（`*Compensated*`/`*PendingScroll*`/`*Debt*`/`*BlankRow*` 模式）；新布局逻辑必须进入 Composer；
2. **禁止新增 `time.AfterFunc` 渲染循环**；新调度需求必须进入 FramePump；
3. **禁止新增直写终端的渲染方法**（`fmt.Print*`/`io.WriteString(os.Stdout)` 的渲染用途）；新输出必须走 Presenter；
4. **禁止新增私有 markdown 渲染缓存**（per-stream `markdownDoc*` 模式）；统一使用 RenderCache；
5. **禁止为单一症状新增独立修复测试而不挂接对账机制**——新测试必须包含"reconcile 后一致"或"RowPlan owner 一致"断言（沿用并扩展 unified plan §16 测试矩阵）。

---

## 9. 测试与验收

### 9.1 新增测试资产

| 层 | 测试 | 断言 |
| --- | --- | --- |
| 单元 | `FramePump` 合并/背压/预算 | 同帧多 Update 只渲染一次；渲染中 Update 不并发 |
| 单元 | `Composer` 确定性 | 同 Scene+几何 → 逐格相同 ScreenModel；owner 全覆盖 |
| 单元 | `Presenter` diff | 执行 diff 序列后 vt.Screen 重建 == ScreenModel（现有 viewport 测试迁移） |
| 单元 | `RenderCache` | key 命中/失效/LRU；theme 切换全清 |
| 属性 | reconcile 收敛 | 随机事件序列（streaming/resize/band/popup/status/lease）后 front == Compose(Scene) |
| 属性 | handoff 单调 | 任意序列下 frontier 单调、无重复交接 |
| 集成 | 既有 4 组 timer 行为 | 改走 FramePump 后行为等价（golden 不变） |
| 集成 | markdown parity | 双渲染调用点收敛后 parity 测试仍然通过且共享缓存 |
| 性能 | 帧延迟/syscall 计数 | 流式长 markdown 下单帧写次数 ≤ 1；帧延迟 < 50ms |

### 9.2 性能验收指标（目标值）

- 帧输出：**每帧 1 次 Write**（现状：多次行级写）；
- 流式渲染：缓存命中路径下 markdown 渲染耗时 < 1ms（现状：每次 Paint 全量 Render）；
- 交互反馈：流式期间输入 → 屏幕更新 < 50ms（现状：长文档可达数百 ms）；
- 内存：RenderCache 上限 64MB，LRU 淘汰。

### 9.3 回归约束

- 所有现有 `fixed_bottom_surface*`、`*history_window*`、`active_stream*`、`screen_lease*`、`info_test`、selection 类测试在每阶段结束时全绿；
- `commands` 全量测试在阶段 A/B 结束时至少各跑 2 次（既有顺序依赖 flaky 另计，单独隔离跟进）。

---

## 10. 风险与回退

| 风险 | 缓解 |
| --- | --- |
| 阶段 A 行为等价性破坏 | timer 回调原样搬运，只换调度壳；golden/行为测试双跑（新旧调度并存开关） |
| reconcile 全量对账在长会话成本高 | reconcile 只在 dirty+收敛时机触发；RowPlan 缓存复用；diff 仍是增量 |
| 双渲染路径收敛改变历史外观 | parity 测试先行（golden 冻结）；收敛按"先共享缓存、后合并路径"两步走 |
| 迁移期间 facade 与 Engine 双写 | 阶段 A/B 期间只允许 Engine 输出；facade 渲染方法逐一删除而非并行新增 |
| 流式 finalize 与 reconcile 竞态 | finalize 与 reconcile 都走 pump 队列，单渲染 goroutine 天然串行 |

**回退策略**：每个阶段独立提交；阶段 A/B 可整体 revert（Engine 作为旁路存在时业务路径不变）；阶段 C 之后以 RowPlan 测试为回归门槛，不满足则回退到 B 并修复后再上。

---

## 附：与 codex 架构的对照（为什么这个设计能解决观察到的症状）

| 症状 | codex 的做法（本设计借鉴） | 本项目现状（问题所在） |
| --- | --- | --- |
| 卡顿 | 单事件循环 + 每帧一次批量 flush（ratatui diff） | 4 timer 各自 repaint + 行级直写 + 锁内渲染 |
| 内容被吞 | 每帧全量对账（buffer diff 自愈） | 增量局部 repaint，算错行号后无兜底 |
| 组件冲突 | 同一 widget 树每帧重新派生布局，无持久行所有权 | viewport/历史/band/prompt/popup/status 共享物理行，靠补偿状态机同步 |
| 缓存 | 无（全量重渲染足够快 + diff 输出） | 有缓存但 per-stream 私有、双路径不共享 |
| resize | 内存 cell 为源，清空重发（`transcript_reflow.rs`） | 已 handoff 部分与内存行计数脱节，后续布局基于失效计数 |

RenderEngine 不是复刻 ratatui，而是把 codex 的**收敛性、批量输出、无持久行所有权**三个核心性质，以 Go 增量模型实现到本项目里。
