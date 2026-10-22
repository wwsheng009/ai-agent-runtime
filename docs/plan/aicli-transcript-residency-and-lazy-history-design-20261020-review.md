# 审查报告：transcript 驻留降级与懒加载设计（C 档）

> 审查对象：`docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020.md`
> 审查维度（按要求）：渲染正确性 / 性能 / 是否真正避免全量渲染驻留 / 算法与逻辑设计质量
> 审查方式：代码级证伪（不采信设计文档自述，逐条对照实现与规范文档）
> 结论：**有条件通过（Conditional Approve）**——方向正确，但有 **4 项阻断（B1–B4）** 与
> **6 项高优（H1–H6）** 必须在进入实现前闭环；本报告 §5 给出可直接替换设计文档相应章节的改进算法。

## 0. 审查结论摘要

| 维度 | 判定 | 说明 |
|---|---|---|
| 渲染正确性 | **不达标（需修订）** | B4（行账本未按 layout generation 分代）会直接破坏 resize 后的行号一致性；B1（冷区重建无确定性闭环）会让 fail-closed 退化为「永不逐出」 |
| 性能 | **基本达标（有依赖）** | 复杂度方向正确，但 H5 指出帧合成/SSE 仍全量；B2 的装载流式化不做则装载期仍全量驻留 |
| 避免全量渲染驻留 | **不达标（有遗漏）** | B3：bridge scene（`renderScene`）仍全量保留所有 cell，仅 AppState 窗口化不解决内存 |
| 算法与逻辑设计 | **需重构三处** | 双代行账本、流式交付状态机、冷区写穿物化（§5 给出设计） |

**总评**：设计文档的取舍方向（窗口化输入、复用现成判定、不恢复截断语义、fail-closed）是对的，
但当前形态不足以支撑「内存与会话长度解耦」的 KPI：真正决定内存的三个层次里，
只有 AppState 层被窗口化，scene 层与行账本代际语义尚未闭环，冷区重建的成本模型缺失。
按 §5 的改进算法修订后可以通过。

## 1. 阻断项（必须在实现前闭环）

### B1【阻断】冷区重建没有闭环的确定性来源与成本模型

**事实（代码）**：
- 现有唯一「从历史重建 Scene」的生产路径是**装载期全量重放 CLI 事件日志**：
  `replaySessionLoadLogOnce`（`commands/chat_runtime_events.go:1161-1217`）→ `replaySessionLoadEventLog`
  → `replayEventLog`；注释原话：大会话（数万行日志、数千条 canonical 消息）上
  「**这是启动关键路径最贵的一段**」，并为此增加了 compact 视图**跳过全量重放**的快路径（`:1182-1213`）。
- 事件日志是 CLI 侧 jsonl（`appendEventLog` `:3908-3948`，进程级 journal writer），
  另有 SQLite 侧 `ListEvents/ListEventsBefore(sessionID, beforeSeq, limit)`
  （`internal/chat/session_runtime_store.go:3937/4016`）可做分页事件读取，
  但**设计文档并未验证**它与 CLI jsonl 重放是否产生**逐字节一致**的 cell。
- 同时存在**第二条来源**：canonical seed（persisted history units，按消息投影）与事件重放结果
  在 `chat_history_reconcile.go`（如 `:205-213`、`:375-385`、`:500-520`）里做**匹配去重**——
  说明「同一 cell 可能由两条不同来源构造」，二者的等价性由 reconcile 逻辑保障，而非由构造确定性保障。

**风险**：设计文档 §6.2 写「复用现有事件编码 + ChangeSetMapper 重建」，但：
1. 全量重放是**整文件级**操作，没有「按 cell 范围重放」的 API → 懒加载会退化为「每次回填都重放全日志」；
2. 重建结果需要与首次创建**逐字节一致**（否则 `SourceFP/RowCount` 校验必失败 → fail-closed → **永不逐出**，
   内存目标落空）；
3. reconcile 路径的存在说明「重建 = 首次」并不天然成立（存在 alias/合并/compact 视图等后处理）。

**修订要求（二选一，建议 B）**：
- **方案 B（推荐，写穿物化）**：**逐出时序列化冷 cell 快照**到 side store
  （每 cell：`ID/Revision/Kind/Phase/Source/Presentation 摘要/RowCount(gen)/SourceFP/Boundary/ChainKey/BoundaryGroupKey`），
  读回 O(1)；不依赖事件重放，成本可控、可校验。side store 首选**复用既有 sqlite 会话库**
  （新增 `session_cells` 表或复用 artifact store），不引入新格式家族。
- 方案 A（事件范围重放）：需要新增「按事件 seq 范围重放」API + 与 jsonl 的一致性证明 + 成本实测；
  作为方案 B 的兜底/校验路径保留。
- 方案 C（从消息重新投影）：仅当 canonical seed 与事件重放被证明等价时可用；当前有 reconcile 说明需先证明。
- 无论哪种，必须补 **Spike-1**（§6）实测：单页回填 ms、字节、与首次构造的一致性率。

### B2【阻断】装载收尾仍是「全量替换 + 从 source 0 全量铸」，与窗口化冲突

**事实**：
- 装载路径（`/resume`、`/load`、启动恢复）会发布 `ReplaceTranscriptAction`
  （`chat_history_reconcile.go:482-486`、`commands/chat_ui_actor.go:1048-1053`），
  并**授权一次性 scrollback 替换**（append-only 语义下必须整段重放历史）；
- A2 第二刀后 `mintTranscriptPlan` 是「finalize 从 source 0 全量铸；skipRows 恒 0」
  （`ui/history_effect_planner.go:96-117`），且 `displayStart=0`、`firstVisible=len(rows)`；
- P16 文档记录：收尾全量替换（4225 cell）时 `screen` 相位 324–360ms、mint 86,863 行、
  全量布局锁内 1.37s→0.54s→220ms（靠装载期分块预热 + 并行渲染）。

**风险**：若按当前设计实现窗口化，装载收尾仍需要「全部 cell 的行」才能按序铸出提交
（append-only 要求较早页不得晚于新页），因此**装载期仍会全量驻留**——这正是设计文档
声称要消除的形态，只是从「稳态」挪到了「装载期」。

**修订要求**：把装载收尾改为**流式铸提交状态机**（page → 窗口布局 → mint → 入队 → 交付 →
Ack → 前进 → 释放），并证明：
1. 交付顺序 = canonical 顺序（较早页先写，append-only 不倒退）；
2. exactly-once 保持（同一来源身份不重发；跨页 token 连续）；
3. P16 门禁（≤250ms 单次锁内）不回归：单页锁内 ≤ 1 页预算 + 后台预取；
4. 装载期内存峰值 ≤ `residentCells(page × k)` 而非全量。

### B3【阻断】bridge scene 层仍全量保留所有 cell（设计遗漏）

**事实**：
- `chat_runtime_events.go:3860 applyChangeSet` 在 `sceneMu` 下持有 `b.renderScene` +
  `scene.NewChangeSetMapper`，**增量累积全部 cell**（每事件一条 changeset）；
- `ui/scene` 包内**没有任何 Trim/Compact/Evict/maxCells 逻辑**（grep 结果为空）；
- `scene.Snapshot()` 产出全量 `Cells` 指针切片，再经 `newTranscriptStateFromSnapshot`
  COW 进入 `AppState.Transcript`；`scene.New()` 只在 reconcile/装载时整体重建
  （`chat_history_reconcile.go:517-519`）。

**风险**：即便 AppState 窗口化，`renderScene` 仍持有 5,500+ cells（且随会话继续增长），
内存目标无法达成；而 `replayLoadedSessionHistory`/reconcile 后处理还依赖 scene 的完整性。

**修订要求**：把 scene 层纳入驻留策略——推荐**让 scene 成为 L2 索引的唯一持有者**
（scene 内部按窗口淘汰 + 索引记账），或引入 `SceneTrimPolicy` 与 AppState 同源；
并明确 reconcile（匹配去重、alias、compact 视图替换）在窗口化 scene 上的等价性测试。

### B4【阻断】行账本必须按 layout generation 分代（当前设计未表达）

**事实**：
- `Ack(token, frame, generation)`：generation 不匹配返回 `ErrStaleLayoutGeneration`
  （`ui/history_commit.go:517-541`）；`LayoutGeneration` 是 commit 的显式字段（`:62`）；
- resize 只 reflow **retained tail**，已进 scrollback 的行不可重排（INV-4，规范 §278）；
- 现有规划中 `displayStart=0`、`firstVisible=len(rows)` 之所以成立，是因为**整份 transcript
  在同一代内被完整铸出**；窗口化后「已交付前缀（旧代）」与「窗口（新代）」的行号不再同代。

**风险**：若行账本只维护一套行号，resize 后会出现：冷区行数（旧代）与窗口行数（新代）无法拼接、
`DisplayRange` 与 `Ack` 代际校验冲突、pager 锚点（`TranscriptPagerAnchor.LayoutGeneration`）失配。

**修订要求**：采用**双账本**（§5.1）：
`CommittedRowLedger`（已交付冷区，按代标记、只读、单调）+ `WindowRowLedger`（当前代窗口，
可重排、可重铸）；全局行号只在「同代内」有意义，跨代边界以**代边界锚**表达。
所有消费方（planner/pager/status/web）必须显式声明其使用的是哪一代的账本。

## 2. 高优先问题（H1–H6）

### H1【高】`foldTarget` 是全局扫描属性，窗口化会丢失/错位

**事实**：`layoutTranscriptScreenRowsImpl` 每帧调用 `toolFoldTargetRows(rows, cells, mutable)`
（`ui/app_screen_layout.go:241`）决定「最近一次折叠」的 Ctrl+T 提示位；
`warmTranscriptLayoutChunk` 的注释也确认「唯一的跨 cell 输入是『最近一次折叠』的提示位（foldTarget）」
（`ui/history_effect_planner.go:707-712`）。

**风险**：该属性依赖对**全量 transcript** 的扫描。窗口化后若最近一次折叠落在冷区，
窗口内计算会得出「无折叠」，提示位丢失或错误地落到更早的折叠上（缓存键含 `foldHint`，会同时污染命中）。

**修订**：索引中为每个 cell 记 `Folded bool` + `FoldHintEpoch`；`foldTarget = 索引中最后一个 Folded cell`；
窗口内渲染用该全局值判定（`cell.ID == foldTarget`），并把该值纳入 memo 指纹（与 `candidateCount` 同级）。

### H2【高】fence/memo 必须改为索引前缀指纹

**事实**：`transcriptFinalizedPrefixFence`（`ui/history_effect_planner.go:771`）是对 finalized prefix
逐 cell 折叠出的 FNV-1a 指纹，作为 memo 与失效判据（:`829-900`、`history_effect_queue.go:953`）。

**风险**：窗口化后若仍逐 cell 扫描 body 计算 fence，则每次规划仍 O(全量 cells)；
若改为「只对窗口计算」，则窗口滑动会让 fence 抖动，memo 失效风暴。

**修订**：在索引中维护**前缀折叠值**（每 entry 存 `FenceAfter`），窗口滑动只需 O(k) 追加；
fence 语义保持「覆盖 finalized prefix 的全部布局相关字段（ID/Revision/Sequence/Kind/Phase/blocked/Boundary/
source len/ChainKey/BoundaryGroupKey）」。

### H3【高】逐出判据在 epoch/跨代次语义上有二义

**事实**：`hasSettledRecordForSource`（`ui/history_effect_queue.go:232`）注释明确：
**跨代次（deliveryEpoch 不同）的 Delivered 记录视为可再铸**；代内阻断由
`entryBlocksRemintInEpoch`（`ui/history_commit.go:399`）与 `tombstoneBlocksRemint`（:413）表达，
墓碑分「代内」与「永久（Settled）」（`pruneEntry` :790-816）。

**风险**：若仅以「已 Ack」作为逐出条件，遇到新代次（会话装载换代/ledger 整体替换）时，
被逐出的冷 cell 需要**重新铸造**——而此时它的 source/行映射已不在内存，必须回读 → 若回读不可用（B1），
交付会缺行或错序。

**修订**：逐出条件收紧为**「永久墓碑（Settled，跨代不失效）存在」且「当前无打开中的 epoch 待结算」**；
其余状态（Queued/Delivered 代内/Unknown/InvalidationPending）一律 pin。此规则与 INV-10 的写失败语义一致。

### H4【高】pager 仍为 O(全量) 物化，需要虚拟列表算法

**事实**：`NewTranscriptPagerModel` 全量复制非 mutable cells（`ui/transcript_pager.go:88-112`）；
`Rows(width)` 为**所有** cell 展开行（`:164-195`）；`Reconcile`/`Scroll` 每次都调用 `model.Rows(width)`
（`:297`、`:329`）；锚点为 `CellID + cell 内行号 + LayoutGeneration`（:127-132）。
`ExpandAll` 会改变折叠 cell 的高度（:58-63、`expandCell` :196）。

**风险**：打开 pager 与每次滚动都是 O(N) 重建 + 全量分配；窗口化后如果只把「正文」懒加载而仍全量展开行，
CPU/内存都达不到目标。

**修订算法（§5.5）**：
- 高度来自**行账本**（每 cell 当前代 `RowCount`，索引已有），O(log n) 定位可视区间，只物化可见页 ±1 页；
- `ExpandAll` 打开时使用**按需校正的估算高度**（折叠态高度已知；展开态高度首次访问时计算并写回索引旁路表），
  锚点恢复仍按 `CellID + 行号`（`findTranscriptPagerAnchor` :384）语义；
- 冷页正文经 `LazyHistorySource` 拉取，未到达前渲染占位行（高度已知，不抖动）。

### H5【高】帧合成与 web SSE 仍是全量（每帧/每事件成本）

**事实**：`ComposeAppRenderFrame` 把**全部** Cells 复制成 `map[CellID]TranscriptCell`
（`ui/app_render_frame.go:45-51`）；`sceneBlockGroups` 全量 `scene.LayoutTranscript(snap.Cells)`
（`commands/chat_runtime_events.go:5217`）。

**风险**：这两处是**每帧 / 每事件**路径，全量拷贝带来持续分配（GC 压力）；
即使布局窗口化了，帧合成仍会把窗口化收益吃掉。

**修订**：帧合成只物化「视口邻域 + active + 折叠判定所需上下文」的 map（H1 的 foldTarget 由索引提供，
不再需要全量 cells）；SSE 用尾部窗口（与 `/web/api/screen` 的 `tail` 口径统一）。

### H6【高】内存口径需覆盖 canonical seed 与 session 层

**事实**：会话装载同时存在三条数据面：scene（事件重放/增量）、canonical seed units
（`chat_history_reconcile.go` 的 persisted history 导入）、`session.Messages`
（`restoreChatStateFromRuntimeSession` → `replaceRuntimeMessages`，`commands/chat_session.go:161-175`）。

**风险**：只优化 UI 层可能只覆盖一部分内存；需要先量化各面占用（否则 KPI 无法归因）。

**修订**：内存 harness（§6 Spike-4）必须按子系统输出：`scene_cells_bytes`、`appstate_cells_bytes`、
`ledger_bytes`、`seed_units_bytes`、`session_messages_bytes`、`render_cache_bytes`，
并在 `app_state.residency` 中暴露同样的分组。

## 3. 正确性矩阵（逐不变量）

| 不变量 | 本设计是否足够 | 缺口 | 补丁 |
|---|---|---|---|
| INV-1 语义源唯一 | ⚠️ 条件成立 | 冷区重建来源未证明「逐字节一致」（B1）；存在两条来源 + reconcile | B1 写穿物化；补「重建=首次」等价性测试与 `nonRebuildable` fail-closed |
| INV-2 scrollback 只写不读 | ✅ | — | 索引/账本全部来自应用侧状态 |
| INV-3 exactly-once | ⚠️ 条件成立 | 跨页 token 连续性、跨代重铸（H3） | B2 流式状态机 + token 连续性断言；H3 收紧逐出 |
| INV-4 resize 只 reflow tail | ❌ 当前不足 | 行账本未按代分账（B4） | 双账本（§5.1）+ 代边界锚；pager 显式声明代 |
| INV-5 sticky top-align | ✅ | 需回归 | 沿用 presenter 逻辑，不改写路径 |
| INV-6 提交=完整 cell 前缀 | ⚠️ 条件成立 | 装载期「全量铸」与窗口化冲突（B2） | 流式铸提交 + 顺序不变量断言 |
| INV-7 缓存不改变投影结果 | ✅ | 窗口化后 miss 模式变化 | 保留「缓存只复用、不改变输出」的等价性回归（含 foldHint 双态） |
| INV-8 memo 指纹 | ⚠️ 需扩展 | 需索引前缀 fence（H2）+ 窗口字段 | 前缀折叠值 + `windowStart/End + indexRevision + foldTargetEpoch` |
| INV-9 空行/尾随行身份 | ⚠️ 需显式 | 索引 `SourceFP` 必须覆盖 newline-backed fragment 身份 | 指纹输入含「空行/尾随空行 fragment 计数」；补单元测试 |
| INV-10 写失败语义 | ✅ | 逐出需 pin Unknown/partial | H3 收紧 + `PinUnackedRows` |

## 4. 性能审查

### 4.1 复杂度对照

| 路径 | 现状 | 设计后 | 备注 |
|---|---|---|---|
| 布局（每次规划） | O(全部 cells)（`screenTranscriptPlanWindow` 全量） | O(窗口 cells) | 窗口 512 vs 5.5k ≈ 1/10 |
| 帧合成 | O(全部 cells)（全量 map） | O(视口 + active) | H5 |
| pager 打开/滚动 | O(全部 cells) | O(可见页) | H4 |
| 装载 | O(全部) 且全量驻留 | O(页) 流式 | B2 |
| 逐出/回填 | 不存在 | O(1) 追加 / O(页) 回读 | B1 |
| 索引查询 | 不存在 | O(log n) 二分 | RowLedger |

### 4.2 参数默认值（由本次实测推导，非拍脑袋）

- 实测：5,500 cells ↔ 120,022 行 → **21.8 行/cell**；512 cells ≈ **11.2k 行**。
- 实测：`cellRows` 64 MiB（预算顶格）对应 7,509 条 → **≈8.9 KB/cell**（估算口径）。
  → 窗口 512 cells 的渲染行 ≈ **4.6 MB**；取 16 MiB 预算 = 3.5× 余量。
- 索引 64 B/cell：5.5k ≈ 0.35 MB；50k ≈ 3.2 MB。
- 回填页：256 cells ≈ 2.3 MB 渲染 + 源文，落在 2–4 MiB / 页的合理区间。

### 4.3 关键路径预算

| 动作 | 预算 | 依据 |
|---|---|---|
| 窗口滑动（memo 命中） | ≤1 ms | 只做 O(k) 索引追加 + 指纹折叠 |
| 单帧布局（窗口 miss ≤64） | ≤5 ms 串行装配 | 沿用并行门限 64（`layoutParallelRenderThreshold`） |
| 装载单次锁内 | ≤250 ms（P16 门禁） | 单页 ≤1 页；预取在后台 |
| pager 首屏 | ≤1 帧 | 可见页同步小块（≤2 MiB） |
| 冷页回填 p95 | ≤50 ms | 依赖 Spike-1 实测（B1） |
| 逐出/回填抖动 | 0 次/帧 | 滞回 1.25× + 后台投递 |

### 4.4 内存模型

```
live_heap ≈ 窗口源文与结构 + 窗口渲染行缓存 + 索引 + 其他应用状态
WS       ≈ live_heap × GC 余量(≈2×) + runtime 开销
```

以 20k cells 为目标：live ≤120 MB → heap goal ≈240 MB → WS ≈ 300 MB；
配合软上限从 512 MiB 收紧到 **384 MiB**（`AICLI_MEMORY_LIMIT`）可把 RSS 钉在 ~300–350 MB。

### 4.5 现状证据（为什么这些修正是必要的）

- `cell_rows_bytes=67,106,661` 顶格 + 12,646 次驱逐 + 99.84% 命中 → 现状是「用内存换 CPU」，
  内存完全由会话长度决定；窗口化后同一预算可覆盖 3.5 倍以上工作集。
- 装载期「全量替换 + 全量铸」被 P16 文档明确记录为最贵路径（1.37s → 220ms 靠预热摊还），
  但**摊还的是耗时，不是内存**——B2 才是把装载内存也摊还的算法。

## 5. 改进后的算法与逻辑设计（建议直接替换设计文档 §4–§6 的对应小节）

### 5.1 数据结构：双代账本 + 冷区写穿 store

```go
// —— 冷区（已交付、已逐出）：只读、按代记账，是物理事实的账本 ——
type ColdLedger struct {
    Cells    []ColdCellRef     // 按交付顺序（单调）
    GenSpans []GenerationSpan  // 按代的已交付行数（拼接全局锚用）
    DeliveredRows int64        // 已交付物理行总数（跨代累计）
}

type ColdCellRef struct {
    ID, Revision, Kind, Phase     // 身份（与 ledger 墓碑/身份判定同源）
    SourceFP, SourceLen           // 内容指纹（含空行/尾随行的 fragment 身份）
    RowsAtGen   int32; Gen uint64 // 交付代与该代行数（resize 不重排）
    DeliveredAt int64             // 交付序（单调，用于顺序断言）
    TombstonePermanent bool       // 永久结算（唯一允许逐出的终态）
    Folded      bool              // H1：最近折叠判定
    GroupKeyFP  uint64            // ChainKey/BoundaryGroupKey 折叠指纹（分组边界判定）
}

// —— 窗口（当前代、可变、可重排、可重铸）——
type WindowLedger struct {
    StartSeq, EndSeq int          // 在 ledger 全序中的下标
    Cells            []scene.TranscriptCell
    LocalRows        []int32       // 每 cell 当前代行数（含 gap 归属）
    TotalRows        int32
    Generation       uint64        // layout generation
}

// —— 冷区物化（B1 方案 B：写穿）——
type CellBodyStore interface {
    Put(cell scene.TranscriptCell, ref ColdCellRef) error  // 逐出时写穿；失败即放弃逐出
    Get(id scene.CellID, rev uint64) (scene.TranscriptCell, bool)
}

// —— 驻留状态机（每 cell 一个状态）——
type Residency uint8
const (
    Hot          Residency = iota // 完整 body 在窗口内
    Cold                          // 仅 ColdCellRef
    Loading                       // 回填中（pin，不参与逐出）
    Unrebuildable                  // 回填校验失败：永久 pin + diagnostics
)
```

### 5.2 状态机与守卫

```
Hot → Cold   ⟺ finalized(cell) ∧ ackedAll(cell) ∧ ref.TombstonePermanent
             ∧ !epochOpen ∧ !pinned(cell) ∧ windowAdvancedPast(cell)
             ∧ CellBodyStore.Put(cell, ref) == nil          // 写穿成功才允许逐出
Cold → Loading ⟸ pager/anchor/回滚触达（含 prefetch 窗口）
Loading → Hot  ⟸ fp(body) == ref.SourceFP ∧ rows 校验通过
Loading → Unrebuildable ⟸ 任一校验失败（fail-closed，永不再次尝试逐出）
任何 → Hot    ⟸ 装载重放 / ledger 换代 / 会话切换（走 §5.6 流式装载，不逐出）
```

**守卫的可判定性**：全部输入来自现有结构（`cellIsFinalizedForHistory`、
`HistoryCommitLedger` 的 `AckFrame`/`tombstone`/`entryBlocksRemintInEpoch`、
`hasSettledRecordForSource`、`retainedQueuedCommitForSource`），不新增语义，只做组合。

### 5.3 窗口滑动算法（O(k)，滞回防抖）

```
onReduce(state):
    mustKeep = firstIndexWithUnsettledOrUnacked(state)        # 必须驻留的最早 cell
    desired  = min(mustKeep,
                   pagerStart   if pagerOpen,
                   viewportStart - margin)
    if window.TotalRows > maxRows * 1.25:                     # 高水位：强制回收
        target = max(desired, window.EndSeq - maxCells)
    elif window.StartSeq < desired - hysteresis:               # 低水位：只在有必要时滑
        target = desired
    else:
        return
    slidePrefix(target)                                       # 逐 cell：写穿 → 转 Cold → 更新账本
```

- 一次滑动只处理**整 cell 前缀**（INV-6）；单次上限 `k = maxCells/4`（避免长临界区）；
- `slidePrefix` 失败（写穿失败/守卫不满足）→ **停在半途之前**（原子性：先全部写穿成功再提交状态切换）；
- 滑动后 `indexRevision++`，memo 只需失效「窗口相关」输入（H2 前缀 fence 不变则冷区不重算）。

### 5.4 冷区回填（读路径，off-lock）

```
ensureResident(range):
    for c in range where state(c) == Cold: state(c) = Loading; enqueue(fetch c)
fetch(c):  body, ok = CellBodyStore.Get(c.ID, c.Revision)
           if !ok || fp(body) != ref.SourceFP: state(c) = Unrebuildable; return
           rows = layout(body)            # 当前代；缓存 miss 走 worker 池
           install Hot(body, rows); indexRevision++
```

- 预取：pager 打开时 `lookbehind=2 页`、前向 `1 页`；主界面回滚只回填可见页；
- 回填期间渲染占位行：高度先按 `ref` 元数据（Kind/Folded/SourceLen）估算，布局到达后校正
  （虚拟列表标准做法，避免抖动）；
- 回填属于内部 action（参考 `WarmTranscriptLayoutAction`，`ui/controller_state.go:480-489`），
  不产生用户意图副作用。

### 5.5 虚拟化 pager（O(可见页)）

```
viewport → O(log n) 定位（ColdLedger 高度 / WindowLedger 高度）
for cell in visibleRange: ensureResident + rows(cell)        # 只物化可见页
ExpandAll: 展开高度按需计算并写回旁路表；未加载页使用估算高度，加载后校正
anchor:    (CellID, rowInCell, LayoutGeneration)             # 沿用现有语义（:127-132）
```

- 冷页正文按 §5.4 回填；「未加载」状态以占位行呈现（高度已知，滚动条不跳）；
- `ExpandAll` 的行数变化不影响交付账本（交付账本只关心**交付时**的行数）。

### 5.6 流式装载铸提交（B2 的算法）

```
loadSession(session):
    pages = canonicalPagesDescending(session)     # 尾部页优先（用户最关心）
    install(pages.latest) → window（Hot）
    for page in pages.remaining:
        prefetch(page);                           # 后台，off-lock
        mint(page)    # 只为该页中未结算的 cell 铸提交；保持「页序 = canonical 序」
        deliver(page) # 同页内按 cell 顺序；跨页按 canonical 顺序（append-only）
        awaitAck(bounded, timeout)                # 有界等待；Unknown/partial → pin，不逐出
        evictDelivered(page) → Cold（写穿）
    closeEpoch(); publishScrollbackReplace()      # 沿用现有一次性替换授权路径
```

- 不变量：`deliverOrder == canonicalOrder`、跨页 token 单调连续、每 cell 只交付一次；
- 峰值内存 ≈ 1 页窗口 + 预取 1 页（≤2 页），而非全量 transcript；
- 与 P16 门禁兼容：单次锁内只处理 1 页（≤250ms），其余在后台。

### 5.7 复杂度与正确性要点

| 关注点 | 结论 | 依据 |
|---|---|---|
| 布局 | O(窗口) | 冷区不布局；行号由账本拼接 |
| 帧 | O(viewport) | H5 修订后 |
| pager | O(可见页) | §5.5 |
| 装载 | O(页) 时间与内存 | §5.6 |
| 逐出/回填 | O(1)/O(页) | §5.2/5.4 |
| 有界性 | 驻留 = 窗口 ∪ pin，窗口与 pin 均有上限 | §5.3 高水位 + pin 集合有限（active 1、未结算 ≤1 页、pager 页、viewport） |

正确性证明要点（实现期作为测试断言）：
1. **exactly-once**：身份（ColdCellRef 与 ledger 墓碑同源）+ token 单调 + 顺序单调；
2. **resize 安全**：双账本隔离代际，冷区只读，窗口重排不改写已交付行；
3. **渲染等价**：窗口布局 = 全量布局的**子序列**（同一布局函数与同一缓存键；`foldTarget` 由索引提供唯一全局值）；
4. **内存有界**：见上表「有界性」。

## 6. Spike 与验收门禁（进入实现前先跑）

| Spike | 内容 | 通过标准 |
|---|---|---|
| **Spike-1 冷区物化** | 抽样 1k cells：写穿序列化 → 读回 → 与首次构造比对 | 一致率 100%；读回 p95 ≤50ms；页字节 ≤2 MiB（256 cells） |
| **Spike-2 流式装载** | 5.5k cells 会话按 §5.6 装载 | exactly-once marker 全绿；P16 ≤250ms；装载峰值内存 ≤2 页 |
| **Spike-3 虚拟 pager** | 5.5k / 20k cells 打开与滚动 | 首屏 ≤1 帧；滚动帧时 p95 ≤16ms；锚点恢复 100% |
| **Spike-4 内存基线** | 20k cells 合成会话，按 H6 分区口径 | live heap ≤120MB；WS ≤384MB；各子系统占用可归因 |

任一 Spike 不达标 → 退化为保守形态（只做窗口化布局 + 缓存预算收紧，不逐出），仍可拿回部分收益。

## 7. 对设计文档的修订清单（按优先级）

| # | 设计文档章节 | 修订内容 | 优先级 |
|---|---|---|---|
| 1 | §4 / §6 | 换成 §5.1 双代账本 + 写穿 store；逐出守卫收紧（永久 tombstone + epoch 关闭） | P0（B1/H3） |
| 2 | §5.1/§5.3 | 增补 §5.6 流式装载铸提交（页序、锁内预算、跨页 token 连续性断言） | P0（B2） |
| 3 | §5.4 | 消费者清单补 scene/bridge（B3）、帧合成、SSE（H5） | P0 |
| 4 | §7.1/§7.2 | 失效与校验补 generation 维度、双账本边界、fence 前缀折叠、foldTarget 唯一性 | P0（B4） |
| 5 | §6.2 | 再物化改为「写穿优先 + 事件重放兜底」，附三方案决策矩阵与一致率校验 | P0（B1） |
| 6 | §4.3/§5.2 | 窗口选取补 `foldTarget` 来源（H1）与 `mustKeep` 语义 | P1 |
| 7 | §4.6/§9.1 | 默认值更新：cellRows 16 MiB / plan 8 MiB / fold 8 MiB；页 256 cells / 2 MiB；滞回 1.25×；软上限建议 384 MiB | P1 |
| 8 | §11 | 采纳 Spike-1..4 与 H6 分区内存口径 | P1 |
| 9 | §13.2 | 开放问题收敛：`ChainKey/BoundaryGroupKey` 只存指纹；`ExpandAll` 采用估算+校正；SSE 尾部窗口化 | P1 |

## 8. 最终结论

- **方向**：认可。窗口化输入 + 索引记账 + fail-closed 是解决「内存 ∝ 会话长度」的正确路线。
- **当前形态**：不足以落地——缺 B1（物化来源）、B2（装载流式化）、B3（scene 层）、B4（行账本代际）
  这四块地基；H1–H6 是随之而来的高风险点。
- **改进后**：§5 的双代账本 + 写穿物化 + 流式装载 + 虚拟 pager 构成一个自洽、可证明、有界的算法；
  四个 Spike 达标后即可按 P0→P1 顺序实施，回退开关保持「只窗口化不逐出」的保守档。
- **审查判定**：修订 §7 清单后可进入 Spike 阶段；Spike 通过前不得进入 Stage 3（逐出）实现。