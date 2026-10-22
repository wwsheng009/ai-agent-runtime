# aicli transcript 驻留降级与懒加载设计（C 档）

> 状态：**设计草案 v2**（2026-10-20，依审查报告修订；权威规范见 §14–§18）。范围：交互式 TUI 渲染路径的 transcript 驻留模型，涉及
> `backend/cmd/aicli/ui`、`backend/cmd/aicli/commands`、`backend/internal/chat` 的装载/存储接口。
> 目标：把「内存 ∝ 会话长度」改造为「内存 ∝ 可见窗口 + 未交付增量」，同时不改变
> native scrollback exactly-once 语义、pager 语义与 resize 语义。
>
> 关联文档：
> - `docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`（唯一规范源；§217 明确「不要求应用无限期保留所有已交给 native scrollback 的 display rows」）
> - `docs/plan/aicli-resume-p16-plan-freeze-attribution-20261008.md`（§4「流式/增量 plan」；P16 门禁 250ms）
> - `docs/plan/aicli-render-p1-1-step4-planning-incremental-plan.md`（§115 无预算单遍的由来与截断语义教训）
> - `docs/architecture/aicli-tui-renderer-architecture-design.md`（§20 native scrollback append-only 单向交付）
> - `docs/debug/cpu-hotspots-ui-render-and-session-persist-optimization-20260929.md`（长会话全量规划热点）
>
> **v2 修订（2026-10-20，依审查报告）**：本文已按 `docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020-review.md`
> 的审查结论修订。**权威规范在 §14–§17**；下列小节已被覆盖，阅读时以 v2 为准：
> §3.1 三层模型 → §14.1（四层驻留 + scene 层）；§4.1–§4.7 → §14.1–§14.4（双代账本 + 写穿 store）；
> §5.2/§5.3 → §14.3/§14.6（mustKeep 语义 + 流式装载）；§6 → §14.2/§14.4（逐出守卫收紧 + 写穿物化）；
> §9.1 → §15.1（新默认值）；§11 → §16（Spike 门禁）；§13.2 → §17（开放问题收敛）。
> 覆盖原因：审查发现的 B1–B4（阻断）与 H1–H6（高优），详见修订记录 §18。

## 1. 问题与目标

### 1.1 实测基线（2026-10-20，PID 11704，session_20261018183040_rPueGv9v）

| 维度 | 实测值 | 出处 |
|---|---|---|
| 会话规模 | canonical 13,855 消息；prompt 258 消息；Compact Gen #26；运行 8.3h | `/debug/chat/status#session` |
| UI 语义规模 | `cells=5,500`；`layout_rows=120,022`（=`text_rows`）；geometry 156×44 | `/debug/chat/status#scene`、`#app_state.plan` |
| 活堆（强制 GC 后） | **210.6 MB**，其中 UI 路径 99.1 MB（47.1%） | `go tool pprof -top -inuse_space .../heap?gc=1` |
| UI 缓存占用 | `cell_rows_bytes=67,106,661`（**64 MiB 预算顶格**，7,509/8,192 条，命中 99.84%，累计逐出 12,646） | `/debug/chat/status#app_state.layout_cache` |
| 单点最大申请 | `ui.cloneAppRenderLine` 44.06 MB flat | pprof flat 榜首 |
| 非 UI 全部 | 111.5 MB（长尾，最大单项 8.6 MB） | pprof `-ignore=cmd/aicli/ui` |
| 存储 | `session_history.sqlite` 2,379 MB + WAL 123 MB；SQLite/WASM 侧仅占 4.5 MB | 目录 + pprof focus |
| 进程口径 | WS ≈ 490 MB，Private ≈ 580 MB；PeakWS 1,312 MB（pprof 文本导出风暴，诊断诱发，非稳态） | `Get-Process` |
| 运行时上限 | 启动默认 `debug.SetMemoryLimit(512 MiB)`（`cmd/aicli/memory_limit.go:18`）；GOGC 默认 100 | 代码 |

### 1.2 为什么内存 ∝ 会话长度（结构性原因）

1. **语义层全量驻留**：`TranscriptState` 以 cells 形式承载**整个会话**（`ui/app_state.go:74-79`；
   `NewTranscriptState` 对快照逐 cell 克隆，`app_state.go:83-100`），设计上禁止从
   ScreenModel / VT / native scrollback 反推内容。
2. **布局层全量投影**：`screenTranscriptPlanWindow` 名为 window、实为全量——注释原文
   「纯布局，**输出即全量布局（无预算单遍）**」（`ui/history_effect_planner.go:69-73`）；
   `layoutTranscriptScreenRowsImpl` 注释「帧必须完整，截断会画错屏，因此这里不存在部分结果；
   规划侧不再有预算/游标/窗口参数」（`ui/app_screen_layout.go:195-204`）。
   `mintTranscriptPlan` 中 `displayStart := 0; firstVisible := len(rows)`（`history_effect_planner.go:114-117`）。
3. **缓存按工作集设计**：`cellRowsCacheMax=8192`、`cellRowsCacheMaxBytes=64 MiB`
   （`ui/transcript_layout_cache.go:183-195`），注释明确「容量必须覆盖工作集：工作集是 transcript 的 cell 数」。
   即：为省 CPU 而缓存**所有 cell 的渲染行**，渲染缓存成为内存主项。
4. **pager 亦为全量物化**：`NewTranscriptPagerModel` 克隆全部非 mutable cells（`ui/transcript_pager.go:88-112`），
   `Rows(width)` 为所有 cell 重建行（`:164-195`）。
5. **缺卸载通道**：已进 native scrollback 的行与 cell 全量常驻；仅渲染缓存有硬预算与驱逐，
   语义层没有等价降级。规范文档只把它写成「不要求无限期保留」（unified plan §217），未实现。

### 1.3 目标（可度量）

| KPI | 现状 | 目标 |
|---|---|---|
| 活堆（GC 后）随会话增长 | ~210 MB @ 5.5k cells（缓存 64 MiB 顶格） | ≤ 120 MB @ 20k cells；缓存字节与条目数不再随总 cell 数增长 |
| WS（稳态） | ~490 MB | ≤ 384 MB（配合 A 档 512→384MiB 软上限）；极端长会话不劣化 |
| 冷启动首轮锁内耗时 | P16 门禁 220ms/250ms（已达标） | 不回归（≤250ms），窗口化不得引入新的长临界区 |
| pager 首屏延迟 | 单帧内 | ≤ 1 帧；重物化按块摊还（可观测） |
| exactly-once / 连续性 | 已验收 | 全部回归不回归（真实宿主 E2E） |

### 1.4 非目标

- 不改变 native scrollback 语义（append-only、不可回读、sticky top-align）。
- 不改变 pager 交互（Ctrl+T / `/history` 仍是完整 transcript 阅读器）。
- **不引入截断式布局**（历史教训：P1-1 曾因截断/续跑游标引发正确性问题，见
  `aicli-render-p1-1-step4-planning-incremental-plan.md:115-121`）；本设计采用
  「索引 + 窗口 + 按需重物化」，而不是「把布局截断成半截画错屏」。
- 不新增磁盘格式/外部数据库；重物化源仍是既有 sqlite 会话库与既有分页 API。

## 2. 现状模型与不变量

### 2.1 数据流（现状）

```
canonical store (sqlite, 分页 API)
   │  装载（分页/整体；DeferHistoryDelivery 窗口化装载进行中）
   ▼
scene.Snapshot.Cells ──► AppState.Transcript.Cells（全量语义，COW 复用）
   │                          │
   │                          ├─► 布局规划（全量单遍）──► HistoryCommit ──► HistoryCommitLedger
   │                          │                                            │
   │                          │                                            └─► TerminalSession 写 native scrollback（exactly-once）
   │                          ├─► 帧合成（compose；bottom inline viewport）
   │                          ├─► pager（全量克隆 + 全量物化行）
   │                          └─► /debug/chat/status、/web/api/screen（尾部读取）
   ▼
cellRowsCache / historyPlanCache / sharedFoldOmissions（内容寻址 LRU，64 MiB / 64 MiB / 3.4 MB）
   ▲
warmTranscriptLayoutChunk（装载期按块预热，单块 ≤512 cells，只填缓存不铸提交）
```

### 2.2 必须保持的不变量（设计输入）

| 编号 | 不变量 | 出处 |
|---|---|---|
| INV-1 | cells/语义源是唯一真相；不得从 ScreenModel、VT snapshot、native scrollback 反推/reconstruct | `architecture/aicli-chat-unified-renderer-architecture.md:185`；`plan/aicli-tui-p5-owned-viewport-design.md:24` |
| INV-2 | native scrollback append-only，写出的字节永不回读、永不改写 | `architecture/aicli-tui-renderer-architecture-design.md:20` |
| INV-3 | handoff frontier 单调前进；同一 cell/revision/行范围最多交接一次（exactly-once） | `plan/aicli-tui-unified-render-architecture-refactor-plan.md:274` |
| INV-4 | resize 只 reflow retained tail；已进 scrollback 的物理行不可重排 | 同上 `:278` |
| INV-5 | 首次真实溢出后 resident tail sticky top-aligned，禁止非语义空白断层 | 同上 `:41`；`plan/aicli-tui-owned-render-simplification-plan.md:23` |
| INV-6 | 提交 = 完整 cell 前缀（增量必须保持该前缀不变量） | `plan/aicli-render-p1-1-step4-planning-incremental-plan.md:83-84` |
| INV-7 | 布局缓存只做复用，不改变投影结果（内容寻址键：source+kind+presentation+width+theme+foldHint） | `ui/history_effect_planner.go:707-712`；`ui/transcript_layout_cache.go:171-217` |
| INV-8 | 计划 memo 输入指纹 = SceneID + finalized fence + finalized 计数 + layoutGen + width/height + projection + themeKey + terminalEpoch；且候选必须仍在 ledger 中 | `ui/history_effect_planner.go:829-900` |
| INV-9 | plain source 的内部空行/尾随空行必须有非空、可比较的 source/fragment identity | `plan/aicli-tui-owned-render-simplification-plan.md:21` |
| INV-10 | partial write 保持 Unknown 且不得盲重试；零字节失败重试同 token | `plan/aicli-tui-owned-render-simplification-implementation-guide.md:19` |

### 2.3 现有降级机制盘点（改造前）

| 机制 | 作用域 | 上限/阈值 | 是否覆盖语义层 |
|---|---|---|---|
| `cellRowsCache` LRU | 渲染行 | 8,192 条 / 64 MiB，逐出 LRU 端 | ❌（只缓存行） |
| `historyPlanCache` LRU | 物理行映射 | 8,192 条 / 64 MiB | ❌ |
| `sharedFoldOmissions` | 折叠判定 | 小（3.4 MB 实测） | ❌ |
| LLM 上下文压缩 | 送模型的 prompt 历史 | Compact Gen #26；258 条 | ❌（canonical 不动） |
| `HistoryCommitLedger` 压缩 | 交付账本 | `compactResolvedIfLarge` / `pruneEntry` / `pruneCompactedSourcesNotInTranscript` | ❌（cells 不动） |
| 窗口化装载 | 装载期交付顺序 | `DeferHistoryDelivery` + `warmTranscriptLayoutChunk`（≤512 cells/块） | ⚠️ 仅装载期，收尾仍全量替换 |
| 帧合成 viewport-only | 绘制 | `ScreenModel` 只拥有底部 viewport | ❌（不减少 cells/行） |

## 3. 设计总览

> ⚠️ **本节 §3.1 的三层模型已被 §14.1 的四层模型覆盖（v2）**；§3.2 数据流图请按 §14.1 的“scene 层 + 冷区 store”补读；§3.3/§3.4 的取舍结论仍然有效。

### 3.1 三层驻留模型

把「全部 cells + 全部行」拆成三层，按**可逆性**分级驻留：

| 层 | 内容 | 驻留形态 | 上限（默认） |
|---|---|---|---|
| **L1 Resident Window（热）** | 未交付/未结算增量 + 可见窗口邻域 + 活动 cell | 完整 `scene.TranscriptCell`（source、presentation、render 行缓存命中） | `residentCells ≤ 512`、`residentRows ≤ 4,096`、字节 ≤ 32 MiB（含 `cellRowsCache` 配额） |
| **L2 Committed Index（温）** | 已 Ack 进 native scrollback 的历史 cell 的**紧凑索引**：全局行账本 + 交付账本 + 内容指纹 | 定长结构体 + 共享哈希/摘要，O(1) 定位、O(1) 判重 | `≤ 64 B/cell`（20k cells ≈ 1.3 MB） |
| **L3 Cold Store（冷）** | canonical 消息、来源文本/工件（sqlite + artifact 目录） | 按需读取；分页 API 已有（`HistoryPageMessages=100`、`HistoryPageBytes=4 MiB`） | 不在堆上常驻 |

**关键转变**：`AppState.Transcript.Cells` 从「整个会话」变为「L1 窗口 + L2 索引的全序视图」；
布局、规划、pager、状态端点全部改为通过**索引 + 窗口**访问，而不是直接遍历全量 cells。

### 3.2 新数据流

```
                     ┌────────────────────────────── AppState.Transcript ─────────────────────────────┐
canonical store ──►  │  CommittedCellIndex（L2：行账本 + 交付账本 + 指纹）                            │
  （分页/按游标）    │        ▲                                     │                                 │
                     │        │ 逐出（已 Ack/Settled 且离开窗口）    │ 回填（pager/回滚触达冷区）       │
                     │  ResidentWindow（L1：连续 cell 段 + 未交付增量）◄─ LazyHistorySource.LoadCells  │
                     └──────────────────────────────────────────────────────────────────────────────┘
                              │                            │                        │
                     布局/规划（仅窗口 + 行账本）      pager（窗口 + 索引按需回填）   状态/Web（尾部 + 索引摘要）
                              │
                              ▼
                     HistoryCommit ──► 交付（exactly-once）──► native scrollback
```

### 3.3 为什么这样做仍然满足既有不变量

| 不变量 | 本设计的满足方式 |
|---|---|
| INV-1（语义源唯一） | 冷区内容从 canonical store（唯一真相）按确定性规则重物化；索引只存**派生指纹**，不承担内容真相 |
| INV-2（scrollback 不可回读） | 索引与账本全部来自应用侧自身状态（cells/revision/显示行区间），不读取终端 scrollback |
| INV-3（exactly-once） | 交付身份（CellID/Revision/SourceRange/FragmentID + token）在 L2 索引中**完整保留**，逐出不影响重发判定；逐出条件必须晚于「该 cell 全部行已 Ack/Settled」 |
| INV-4/INV-5（resize/对齐） | 已交付行不重排；窗口化只影响「尚未交付」的尾部与 pager 视图；sticky top-align 逻辑不变 |
| INV-6（提交=完整 cell 前缀） | 逐出单元 = 完整 cell；窗口边界只落在 cell 边界；增量计划按「页/块」推进但仍保持完整前缀语义 |
| INV-7（缓存不改变结果） | 行缓存键不变；窗口化后缓存预算不再需要覆盖全工作集（见 §4.6） |
| INV-8（memo 指纹） | 指纹输入增加 `residentWindowStart/End`、`indexRevision`；窗口变化只让**窗口内**的重算失效，冷区不参与布局 |
| INV-9（空行身份） | 索引保留 source 摘要与 fragment 计数的**可比较身份**（含空行/尾随空行） |
| INV-10（写失败语义） | 只有 `Acked`/已结算 cell 才可逐出；`Unknown/partial` 状态进入 pin 集合，禁止逐出 |

### 3.4 与「无预算单遍」的关系（重要取舍）

本设计**不恢复**旧的「截断布局 + 续跑游标」：窗口不是为了让一次布局「算一半」，
而是让**布局的输入集合变小说**——冷区不再参与布局，因为冷区行的物理事实已经固化在
native scrollback，应用侧只需要它的**行账本**（全局行号区间），而不是可再次布局的 cells。

因此：
- 布局仍是「单遍、完整、无截断」，只是 input = `ResidentWindow`（而不是整个 transcript）；
- 「提交 = 完整 cell 前缀」由 `CommittedCellIndex` 的行账本保证：铸提交时全局行号从账本推导，
  不依赖冷区 body；
- 历史教训（截断语义引发的漏交/错屏）不会被重新引入，因为**没有任何 cell 处于「算了一半」状态**。

## 4. 数据结构（Go 草案）

> ⚠️ **本节已被 §14.1–§14.4 覆盖（v2）**：`CommittedCellIndex`/`RowLedger`/`ResidentWindow` 草案被
> `ColdLedger` + `WindowLedger` 双代账本与 `CellBodyStore`（写穿）取代。保留本节仅作演进对照。

### 4.1 L2 `CommittedCellIndex`：紧凑全序索引

```go
// CommittedCellIndex 是已交付前缀的紧凑账本，替代"把整个会话的 cell 留在堆上"。
// 单一全序：按 transcript 顺序排列；行区间单调、不重叠（INV-3/INV-6 的结构化表达）。
type CommittedCellIndex struct {
    Rows      RowLedger          // 全局显示行账本（cell 粒度，含 gap 归属）
    Cells     []CommittedCellRef // 按序紧凑引用（目标 ≤64B/cell）
    Revision  uint64             // 索引自身版本；任何结构性变更 +1（供 memo/失效使用）
}

type CommittedCellRef struct {
    ID        scene.CellID      // 全局单调 cell id（scene 侧分配，跨 replay/resize 稳定）
    Revision  uint64            // cell revision（内容寻址协作）
    Kind      scene.CellKind    // user/assistant/reasoning/tool/...
    Phase     scene.CellPhase   // 终态 phase（Committed/Failed/...），不可再变
    SourceLen int               // 源文长度（快速一致性校验）
    SourceFP  uint64            // 64-bit 内容指纹（覆盖空行/尾随空行的身份，见 INV-9）
    LayoutFP  uint64            // 布局输入指纹：presentation 类型 + width + theme + foldHint
    RowStart  int64             // 全局首行（含 leading gap 的归属规则，与现状一致）
    RowCount  int32             // 显示行数（含 gap）
    TokenSpan uint32            // 该 cell 已铸 token 的连续段长度（连续性由 mint 顺序保证，
                                // 非连续时走 side-table 兜底）
    TokenBase uint64            // 首 token
    Acked     bool              // 全部行已 Ack（逐出的必要条件之一）
    RowAckBase uint64           // 已 Ack 到该 cell 内的第几行（用于 partial 场景 pin）
}
```

设计要点：
- **只存派生事实**：指纹/区间/账本都是可重算的，不承担内容真相（INV-1）。
- **判重与重发不需要 body**：`ID/Revision/SourceFP/RowStart/RowCount/TokenBase` 足以支撑
  `historyCommitSourceIdentity` / `holdsPlan` / `settled` 等现有判定（详见 §8 接线）。
- **指纹必须覆盖空行身份**：`SourceFP` 的输入包含「源文 + 内部空行/尾随空行的
  newline-backed fragment 计数」，与现网 `SourceRange/FragmentID` 口径一致。

### 4.2 `RowLedger`：全局行定位（O(log n)）

```go
type RowLedger struct {
    spans []rowSpan // {cellSeq int32; start int64; count int32}，单调不重叠
    total int64
}

func (l *RowLedger) Locate(row int64) (cellSeq int32, offset int32) // 二分
func (l *RowLedger) RowStart(cellSeq int32) int64
func (l *RowLedger) RowEnd(cellSeq int32) int64
func (l *RowLedger) Total() int64
```

用途：
- 规划时把「窗口内行下标」换算为**全局 DisplayRange**（现状 `displayStart+start` 的替代）；
- pager 定位（锚点 = cell + cell 内行号，已有 `TranscriptPagerAnchor` 语义）；
- resize/reflow 只作用于**未交付**区间（已交付区间的行账本不变，满足 INV-4）。

### 4.3 L1 `ResidentWindow`：连续段 + 未交付增量

```go
// ResidentWindow 是唯一持有完整 TranscriptCell 的区域；永远是"连续 cell 段"。
type ResidentWindow struct {
    FirstSeq int                     // 在 CommittedCellIndex.Cells 中的首个下标
    Cells    []scene.TranscriptCell  // 完整语义 cell（含 source/presentation）
    Pinned   map[scene.CellID]PinReason
}

type PinReason uint8
const (
    PinActiveCell PinReason = iota // mutable/finalizing，frontier barrier
    PinUnackedRows                 // 存在未 Ack/Unknown 行
    PinQueuedTokens                // 队列中 token 未结算（holdsPlan 相关）
    PinPagerTarget                 // pager 锚点/可见窗口所在 cell
    PinViewportNeighbor            // 主界面 viewport ± margin
    PinRecentlyRead                // /debug、/web 最近读取窗口（TTL）
)
```

不变式：
- **窗口边界只落在 cell 边界**；窗口内 cell 顺序与索引一致；
- 窗口必须完整包含 `[frontier barrier, tail]`（不能把未交付内容挤出窗口）；
- 逐出/回填都以「整 cell 段」为单位，避免出现“半个 cell”的状态（这正是 P1-1 截断语义的教训）。

### 4.4 L3 `LazyHistorySource`：冷区回填接口（草案）

```go
type LazyHistorySource interface {
    // LoadCells 依据稳定游标读取一段 canonical 记录并重建 cell 段。
    LoadCells(ctx context.Context, req CellPageRequest) (CellPage, error)
}

type CellPageRequest struct {
    BeforeCellID scene.CellID // 0 = 从最新尾部向前
    LimitCells   int
    LimitBytes   int
    AnchorSeq    int64        // 索引中记录的 canonical 序号锚（可选一致性校验）
}

type CellPage struct {
    Cells      []scene.TranscriptCell // 已重建；ID/Revision/Source 必须与首次一致
    FirstSeq   int64
    NextBefore scene.CellID
    Complete   bool
}
```

硬性要求（缺一不可）：
1. **确定性重物化**：同一 canonical 记录两次重建必须得到**逐字节一致**的
   source/ID/revision/presentation；实现路径 = 复用现有 scene 构建函数（装载期与重建期同一实现）。
2. **序号锚**：索引保存每段起点对应的 canonical 序号/消息身份，避免只按 cell 序号重建造成漂移。
3. **有界预算**：单次回填 ≤ `LimitCells` / `LimitBytes`；分块摊还到多帧（pager 首屏 ≤1 帧内先出
   可视块，其余块异步补齐）。
4. **不写路径**：回填只读；不触发任何 history 重铸（索引中的 Token/Ack 事实优先）。

### 4.5 `ResidencyPolicy`：逐出与固定

```go
type ResidencyPolicy struct {
    MaxResidentCells  int // 默认 512
    MaxResidentRows   int // 默认 4096
    MaxResidentBytes  int // 默认 32 MiB（含行缓存配额）
    ViewportMargin    int // 默认 2 屏行数
    PagerLookbehind   int // pager 一次回填的 cell 页大小（默认 3× 可视 cells）
}
```

逐出判定（全部为**必要条件**）：

```
evictable(cell) :=
      cellIsFinalizedForHistory(cell)               // 终态、不可能再变
   && index.AckedAll(cell.ID)                       // 该 cell 全部显示行已 Ack
   && !index.HasUnresolvedToken(cell.ID)            // 无 queued/Unknown/partial
   && !pinned(cell, window, pager, viewport)        // §4.3 的 Pin 集合
   && windowStartAfter(cell)                        // 只裁前缀（窗口保持连续段）
```

禁止逐出的场景：`DeferHistoryDelivery` 装载窗口进行中（交付顺序未定）、
`MayHavePartiallyWritten`/Unknown、frontier barrier、pager 当前页与相邻页、
`/debug` 与 `/web` 正在读取的尾部窗口（带 TTL）。

### 4.6 缓存预算重定标

窗口化之后，`cellRowsCache` / `historyPlanCache` 的「工作集」从**全部 cell** 降为**窗口内 cell**：

| 常量 | 现状 | 建议值 | 依据 |
|---|---|---|---|
| `cellRowsCacheMax` | 8,192 条 | 1,024 条 | 窗口 512 cells × 2（foldHint 双态）= 1,024 |
| `cellRowsCacheMaxBytes` | 64 MiB | 16–24 MiB | 与 `MaxResidentBytes` 同源；实测 64 MiB 顶格正是因为工作集=全量 |
| `historyPlanCacheMax` | 8,192 条 | 1,024 条 | 同上 |
| `historyPlanCacheMaxBytes` | 64 MiB | 16 MiB | 同上 |

并把这些常量改为**可配置**（配置项 + 环境变量），默认值取保守值，便于灰度与回退（§10）。

### 4.7 内存量级估算

| 会话规模 | 现状（缓存顶格 + 语义全量） | 设计后（窗口 512 + 索引） |
|---|---|---|
| 5.5k cells（本机实测） | 活堆 ~210 MB（UI 99 MB） | ~60–80 MB（窗口 ~30 MB + 索引 ~0.4 MB + 其他） |
| 20k cells | ~210 MB+（增长点=缓存与语义） | ~80–100 MB（与 5.5k 同量级） |
| 50k cells | 线性增长（未实测） | ~100–120 MB（索引 3–4 MB） |

KPI 按 §1.3 执行；量级估算仅用于设计取舍，不作为验收证据（验收以 §11 的基准 harness 为准）。

## 5. 窗口化布局与增量计划

> ⚠️ **§5.2 的窗口选取需按 §14.3 增补 `mustKeep`/`foldTarget` 语义；§5.3 的一次性“按页摊还”
> 被 §14.6 的“流式装载铸提交状态机”取代（v2）。§5.1/§5.4 仍然有效。**

### 5.1 规划输入的重定义

现状：`planEligibleHistoryCommitsWithinTimed`（`ui/history_effect_planner.go:169`）以
`transcriptPlanSnapshotFor`（:62）构造 `byID`（全量 map）+ `LayoutRows`（全量行），
`screenTranscriptPlanWindow`（:71）对**全部** cell 布局，`mintTranscriptPlan`（:98）逐 cell 扫描。

改造后（同一函数骨架，输入集合变化）：

```go
type transcriptPlanSnapshot struct {
    // 新增：窗口与索引取代“全量 cells + 全量行”
    Window      ResidentWindow          // L1：可布局的连续 cell 段（末尾段）
    Index       *CommittedCellIndex     // L2：冷区行账本（提供全局行号）
    // 其余（width/theme/generation/mutable/fence）语义不变
}
```

- `screenTranscriptPlanWindow` 只对 `Window.Cells` 物化行；冷区不再进入布局。
- 冷区的**行区间**由 `Index.Rows` 提供，用于把窗口内局部行号换算为全局 `DisplayRange`
  （替代现状 `displayStart+start` 的 `displayStart=0`）。
- `mintTranscriptPlan` 的候选集 = 窗口内「已 finalized 且尚未结算」的 cell；
  冷区早已结算（否则不可逐出，见 §6.1），不需要再铸提交。

### 5.2 窗口选取（尾锚定，只裁前缀）

```
windowEnd   = transcript 尾部（含 active barrier cell）
windowStart = max(
    index.firstUnackedRow 所在 cell        // 未交付/未结算的最早 cell（必须驻留）
    , pagerWindowStart                      // pager 打开时的可见页 + lookbehind
    , viewportStart - ViewportMargin         // 主界面视口邻域
    , tailStart - MaxResidentCells           // 数量上限
)
```

三条硬约束：
1. **不得裁掉 frontier barrier 及其后的任何 cell**（`canonicalHistoryCommitFrontier`
   `ui/history_effect_planner.go:198`：第一个 mutable cell 是屏障，后续 finalized 不得跨越）。
2. **不得裁掉存在 Queued/Unknown/partial 记录的 cell**（`MayRemint`/
   `MayHavePartiallyWritten`/`InvalidationPending`，见 `ui/history_commit.go:110-161`）。
3. **窗口边界只落在 cell 边界**（否则会出现“半个 cell”的截断语义，
   历史教训见 §1.4/§3.4）。

### 5.3 增量计划（P16 §4.1 的落地方式）

保留「提交 = 完整 cell 前缀」不变量（INV-6），把「一次铸全量」改为**按窗口页摊还**：

- 每次规划只对 `Window` 内新增 finalized cell 铸提交；
- 冷区已结算，不参与；窗口向后滑动（新的 finalized 进入窗口）时，增量只处理新进入的部分；
- memo 指纹扩展为 `{原字段} + windowStartIndex + windowEndIndex + indexRevision`
  （现有字段见 `ui/history_effect_planner.go:829-900` 与 `history_effect_queue.go:953 invalidateTranscriptPlanMemo`），
  窗口滑动只让「窗口内」重算失效，冷区不触发重算；
- 与 `DeferHistoryDelivery`（装载窗口化）保持同一套顺序语义：窗口滑动不得让较早页在新页之后交付
  （append-only 顺序约束，`ui/history_effect_planner.go:634-645`）。

### 5.4 消费者同步窗口化（必须一起改，否则设计失效）

| 消费者 | 现状（file:line） | 目标形态 |
|---|---|---|
| 布局规划 | `planEligibleHistoryCommitsWithinTimed`（`history_effect_planner.go:169`） | 窗口 + 行账本 |
| 帧合成 | `ComposeAppRenderFrame` 把全部 cells 复制成 `map[CellID]TranscriptCell`（`app_render_frame.go:45-51`） | 只复制视口邻域 + active + 必要 gap 上下文 |
| pager | `NewTranscriptPagerModel` 全量复制；`Rows` 全量展开（`transcript_pager.go:88-112,164`） | 范围读取：可见页 + lookbehind，冷页经 `LazyHistorySource` 拉取（锚点语义不变，`TranscriptPagerAnchor` :127-132） |
| web SSE 编码 | `sceneBlockGroups` 全量 `scene.LayoutTranscript(snap.Cells)`（`chat_runtime_events.go:5217`） | 尾部窗口（与 `/web/api/screen` 的 `tail` 语义一致） |
| `/debug/chat/status` | 全量 `DiagnoseHistoryPlan` + `LayoutTranscript`（`chat_debug_display_http.go:686-701`、`history_plan_diagnosis.go:84-91`） | 默认窗口；需要全量时显式 `full=1`（诊断口径，不影响生产路径） |
| `/web/api/screen` | 全量 fallback + 窗口读混合（`chat_debug_screen_http.go:178,418-423,467-470`） | 统一走窗口 + 索引 |
| 装载恢复 | `restoreChatStateFromRuntimeSession` → `replaceRuntimeMessages` **完整**历史装进 `session.Messages`（`commands/chat_session.go:161-175`；`chat_history.go:78`） | 分页装载：仅装载尾部窗口；更早历史按 `GetMessagePage` 懒加载 |

> 注意：pager 需要每个已提交 cell 的完整 `Source`、`Kind`、`ChainKey`、`BoundaryGroupKey`、
> `Boundary`（`transcript_pager.go:94-98,173-189,228`，`commands/chat_transcript_pager.go:83-100`）。
> 因此 **L2 索引必须能按需提供这些字段**（冷区用 `LazyHistorySource` 重建；索引至少保存
> 其指纹与分组键的稳定哈希，用于分组边界判定，正文按需拉取）。

## 6. 逐出与再物化（Lazy）

> ⚠️ **本节已被 §14.2（逐出守卫）与 §14.4（写穿物化）覆盖（v2）**：逐出条件收紧为
> 「永久墓碑 + 无打开 epoch + 写穿成功」，再物化改为“写穿优先、事件重放兜底”。

### 6.1 可逐出判定（复用现成判据，不新增语义）

逐出（cell 级降级为索引）必须同时满足：

| 条件 | 现成判定 |
|---|---|
| cell 已 finalize | `cellIsFinalizedForHistory`（Phase ∈ {Committed, PartiallyHandedOff, HandedOff}，`history_effect_planner.go:612`） |
| 处于 finalized 前缀内且 `Source != ""` | `canonicalHistoryCommitFrontier`（:198-208）+ :131 |
| 该 cell 全部显示行已 Ack | `HistoryCommit.Ack` 成功即 `Lines=nil` + `acksSinceCompact++`（`history_commit.go:517-541`）；逐行 `AckFrame` 证明 |
| 源级无未结算 | `hasSettledRecordForSource`（`history_effect_queue.go:232`，`mintedThrough` 且无 Queued；逐 token 检查） |
| 无跨代次可再铸窗口 | `entryBlocksRemintInEpoch`（`history_commit.go:399`）/ `tombstoneBlocksRemint`（:413）；注意 `DeliveryEpoch` 语义 |
| 不是队列保留段 | `retainedQueuedCommitForSource` 返回 safe 语义（`history_effect_queue.go:280-310`） |
| 不在 pin 集合 | §4.5 |

**代价说明**：现有压缩粒度是 token/source-key（ledger 墓碑），**不存在 cell 级压缩判据**
（child B 结论）。本设计新增的只是「cell 级驻留判定」，其输入全部复用上述现成函数，不改变
它们的语义；新增部分是 `ResidencyPolicy` 与 `CommittedCellIndex` 的簿记。

### 6.2 再物化（懒加载）路径

```
pager / resize / 回到冷区
   └─► CellPageRequest{BeforeCellID, LimitCells, LimitBytes}
        └─► LazyHistorySource.LoadCells
             ├─ SessionStorageHistoryPager.GetMessagePage(ctx, id, beforeSeq, limit)
             │    （beforeSeq==0 取最新页；返回 {Messages, FirstSeq, LastSeq, NextBeforeSeq, HasMore}，
             │      exclusive-before 游标、序号 1-based 稳定；`internal/chat/session_storage_factory.go:127-142`、
             │     `sqlite_storage.go:2394-2460`，默认页 100 条 / 4 MiB）
             ├─ 复用现有事件编码 + ChangeSetMapper 重建 cell（`ui/render/encoding/encoder.go:223`、
             │    `scene/from_changeset.go:180`）
             └─ 一致性校验：重建结果的 CellID/Revision/Source 指纹与 L2 索引比对
                  ├─ 一致 → 装入窗口、按需重算布局（行缓存 miss 走 worker 池）
                  └─ 不一致 → fail-closed：保持该 cell 常驻（不逐出），并记录 diagnostics
```

要点：
- **CellID 稳定性是可行性的前提**：`CellIDFromItemID`（`scene/from_changeset.go:59`）把
  `item-{n}` 解析为 CellID，注释明确「重放同一 Item 恒等映射」（INV-SCENE-02），
  即**同一事件序列重建 → 相同 ID**。设计据此要求「重建必须走同一编码路径」。
- 若某类 cell 无法确定性重建（例如 presentation 依赖运行期瞬时状态），
  则其 `LayoutFP` 标记为 `nonRebuildable`，该 cell 永不进入逐出候选（安全兜底）。
- 回填预算分摊到多帧：pager 首屏先同步出「可见页」，其余按块异步补齐；
  单块 ≤ `PagerLookbehind`。

### 6.3 逐出动作（只裁前缀）

- 逐出单位 = 从 `Window.FirstSeq` 开始的**连续前缀**（窗口永远保持连续段）；
- 逐出时把该 cell 的 `CommittedCellRef` 补齐到 `Index`（含 `RowStart/RowCount/TokenBase/Acked`）；
- **不触碰** `HistoryCommitLedger` 的墓碑与队列状态（它们继续独立裁剪，阈值
  `historyLedgerCompactHighWater=4096`/`Target=2048`，`history_commit.go:721-750`）；
- resize/reflow 只作用于窗口内；冷区行账本不变（INV-4）。

## 7. 失效与正确性

### 7.1 失效触发清单

| 触发 | 影响 | 处理 |
|---|---|---|
| transcript 整体替换 / replay（SceneID 变化） | L2 索引与 L1 窗口全部失效 | 以新快照重建索引（复用 `ReplaceTranscriptAction` 分支，`ui/controller_state.go:388-479`）；沿用 `pruneCompactedSourcesNotInTranscript`（`history_commit.go:907`，调用点 :470） |
| `DeferHistoryDelivery` 翻转（装载窗口化开始/结束） | 交付顺序语义切换 | 冻结逐出（装载期禁止降级裁剪），沿用现有失效点（`controller_state.go:406-409`） |
| resize / theme 切换 | `LayoutFP`（width/theme）失效 | 只重算**窗口内**布局；冷区行账本不重排（INV-4） |
| ledger 整体替换 / 重证明归零 | memo 指纹中的 `holdsPlan` 语义失效 | 沿用 `invalidateTranscriptPlanMemo`（`history_effect_queue.go:953`，调用点 `controller_state.go:408,423,438`） |
| 窗口滑动 / 逐出 / 回填 | `indexRevision` +1 | memo 指纹扩展项变化 → 仅窗口内重算；冷区不参与 |
| `DeliveryEpoch` 打开（会话装载换代） | 跨代次 Delivered 可能可再铸 | 逐出判定加入 epoch 维度（`entryBlocksRemintInEpoch`/`tombstoneBlocksRemint`）；不确定者保持驻留 |
| active cell 变更 / finalize | fence 变化 | 沿用 `transcriptFinalizedPrefixFence`（`history_effect_planner.go:771`）；窗口尾部随 frontier 前移 |

### 7.2 一致性校验（fail-closed）

1. **重建校验**：重建 cell 的 `CellID`、`Revision`、`SourceFP`、`RowCount` 必须与 L2 索引一致；
   不一致 → 该 cell 标记 `nonRebuildable` 并保持驻留，记录 `rematerialize_failures` 计数。
2. **行账本自校验**（每次窗口滑动/索引变更）：
   `Σ RowCount == 已交付行总数` 且 `RowStart` 严格单调、无重叠；校验失败 → 放弃逐出（回退为全量驻留）。
3. **账本对账**：`Index` 中 `Acked/TokenBase` 与 `HistoryCommitLedger` 的终态记录按
   `historyCommitSourceIdentity` 抽样比对（debug 路径，非热路径）。

### 7.3 并发与调度

- 逐出、回填**只在 actor reduce 内改状态**（复用现有单线程 reducer 约束）。
- 冷区读取（sqlite IO）在后台 goroutine 完成，结果以内部 action 投递回 actor
  （参考现有 `WarmTranscriptLayoutAction` 模式，`ui/controller_state.go:480-489`）；
  in-flight 请求按 `(BeforeCellID, limit)` 去重、可取消、超时退避。
- 防抖动：窗口在**阈值滞回**后滑出（`evictHighWater = MaxResidentCells × 1.25`），避免边界抖动。

### 7.4 明确禁止

- 不得从 native scrollback / `ScreenModel` / VT snapshot 反读内容（INV-1/INV-2）。
- 不得对未 Ack、Unknown、partial 的行/ cell 做逐出（INV-3/INV-10）。
- 不得把布局截断为「半截窗口」参与规划（INV-6）；窗口化的语义是「输入变少」，不是「结果变少」。

## 8. 接口与接线改动

| 目标 | 文件/函数 | 改动 |
|---|---|---|
| 三层驻留核心 | 新增 `ui/transcript_residency.go` | `CommittedCellIndex` / `RowLedger` / `ResidentWindow` / `ResidencyPolicy` 与单元测试 |
| 冷区读取 | 新增 `ui/transcript_lazy_source.go` | `LazyHistorySource` 实现：`GetMessagePage` → 事件编码 → ChangeSetMapper → 重建校验 |
| 状态容器 | `ui/app_state.go`（`TranscriptState` :74-79、`NewTranscriptState` :83-100、`newTranscriptStateFromSnapshot` :119） | 增加 `Index`/`Window`（或并列 `ResidencyState`）；安装/增量复用路径不变 |
| 规划 | `ui/history_effect_planner.go`（`transcriptPlanSnapshotFor` :62、`screenTranscriptPlanWindow` :71、`mintTranscriptPlan` :98、`warmTranscriptLayoutChunk` :713、memo :829-900） | 输入改窗口；全局行号由 `Index.Rows` 提供；memo 指纹扩展 `windowStart/End + indexRevision` |
| 帧合成 | `ui/app_render_frame.go`（`ComposeAppRenderFrame` :45-51） | 只物化视口邻域 + active 的 cell map |
| pager | `ui/transcript_pager.go`（`NewTranscriptPagerModel` :88、`Rows` :164、`Scroll/Reconcile` :297/:329）、`commands/chat_transcript_pager.go:83-100` | 增加范围读取 + 冷页懒加载；锚点（`TranscriptPagerAnchor` :127）语义不变 |
| web SSE | `commands/chat_runtime_events.go`（`sceneBlockGroups` :5217） | 尾部窗口化（与 `/web/api/screen tail` 一致） |
| 调试端点 | `commands/chat_debug_display_http.go:686-701`、`history_plan_diagnosis.go:84-91`、`chat_debug_screen_http.go` | 默认窗口；`full=1` 才全量（诊断专用） |
| 装载恢复 | `commands/chat_actor_executor.go:684`、`commands/chat_session.go:161-175`（`replaceRuntimeMessages`） | 改为分页装载：先装尾部窗口，更早历史懒加载 |
| 存储 | `internal/chat/session_storage_factory.go:127-142`、`sqlite_storage.go:2394-2460` | 复用 `GetMessagePage`（exclusive-before 游标 + 1-based 稳定序号）；不新增磁盘格式 |
| 缓存预算 | `ui/transcript_layout_cache.go:189-190`、`ui/history_plan_cache.go:40-45`、`ui/tool_fold_omission_cache.go:17-27` | 常量改为可配置 + 默认值下调（§4.6） |

新增 action（内部、非用户意图）：`TranscriptWindowSlideAction`、`TranscriptPageLoadedAction`、
`TranscriptEvictPrefixAction`；均复用现有 reducer/ledger 失效机制，不引入新的物理写路径。

## 9. 配置与可观测性

> ⚠️ **§9.1 默认值已被 §15.1 覆盖（v2）**；§9.2 状态字段按 §15.2 增补驻留/物化/分区内存计数。

### 9.1 配置项（默认保守，逐阶段放开）

| 键 | 默认 | 说明 |
|---|---|---|
| `ui.residency.enabled` | `false` | 总开关（灰度） |
| `ui.residency.evictPrefix` | `false` | 允许逐出（Stage 3 前保持 false） |
| `ui.residency.maxResidentCells` | `512` | L1 上限 |
| `ui.residency.maxResidentRows` | `4096` | L1 行上限 |
| `ui.residency.maxResidentMiB` | `32` | L1 字节上限 |
| `ui.residency.viewportMarginRows` | `2 × 视口高度` | 视口邻域 |
| `ui.residency.rematerializePageCells` | `256` | 单次回填 cell 上限 |
| `ui.residency.rematerializePageMiB` | `2` | 单次回填字节上限 |
| `ui.layoutCache.cellRowsMiB` | `24` | 行缓存预算（窗口化后可降） |
| `ui.layoutCache.historyPlanMiB` | `16` | 计划缓存预算 |
| `ui.layoutCache.foldOmissionMiB` | `16` | 折叠判定缓存预算 |

### 9.2 状态字段（`/debug/chat/status#app_state.residency`）

```json
{
  "enabled": true,
  "resident": {"cells": 512, "rows": 3902, "bytes": 31457280,
               "window_start_seq": 4988, "window_end_seq": 5500},
  "index":    {"cells": 5500, "bytes": 352000, "revision": 17, "total_rows": 120022},
  "evictions": {"total": 4988, "last_reason": "acked", "pinned": 12},
  "rematerialize": {"total": 37, "max_ms": 41, "failures": 0, "bytes_total": 10240000},
  "pin_reasons": {"active": 1, "unacked": 3, "pager": 12, "viewport": 8}
}
```

**观测三件套**（沿用 P16 口径）：`app_state.residency` + `app_state.layout_cache` +
`/debug/pprof/executor`，用于回答「窗口是否按预期滑动、缓存是否不再顶格、回填是否抖动」。

## 10. 分阶段落地

| 阶段 | 内容 | 开关 | 验收 |
|---|---|---|---|
| **S0 观测** | 影子 `CommittedCellIndex` 记账（只读、不参与决策）+ `app_state.residency` 状态 + 内存基准 harness | 无（始终开） | 索引与现状行账本逐 cell 对账 100% 一致；harness 采集到 5.5k/20k cells 两档基线 |
| **S1 窗口化规划输入** | planner 输入从「全量」改为「窗口 + 行账本」；窗口=全量时与现状等价（等价性回归）；**不逐出** | `ui.residency.enabled` 默认 off | 全量窗口 vs 现状输出逐行等价；P16 ≤250ms 不回归；memo 命中率不下降 |
| **S2 窗口滑动** | 尾部锚定窗口 + 视口/pager 邻域；memo 指纹扩展；帧合成/web SSE 窗口化 | 同上 | 长会话连续运行 30min：窗口稳定滑动、无未交付行被裁、sticky top-align/连续性回归绿 |
| **S3 逐出 + 懒加载** | `evictPrefix=true`；pager 冷页懒加载；重建校验 fail-closed；装载恢复分页化 | `ui.residency.evictPrefix` | 20k cells 会话 live heap ≤120MB；exactly-once/resize/pager 锚点全绿；`rematerialize_failures=0` |
| **S4 默认开启与收紧** | 默认打开；`layoutCache` 预算降到 §4.6 新默认；文档与运行手册更新 | — | 生产观测两周无回归；WS ≤384MB（配合软上限）|

回退：任一阶段发现问题，关闭 `ui.residency.*` 即回到「全量驻留」旧行为（索引影子记账可保留）。

## 11. 验证与验收门禁

> ⚠️ **本节门禁已被 §16 覆盖并扩充（v2）：新增 Spike-1..4、分区内存口径（H6）与回归清单。**

1. **单元/属性测试**
   - `RowLedger`：单调、不重叠、`Locate`/`RowStart` 二分正确性（属性测试 + 随机对拍）。
   - `ResidencyPolicy`：决策矩阵穷举（finalized/ack/epoch/pin/frontier）→ 期望动作。
   - 重建一致性：同输入两次重建 `CellID/Revision/SourceFP/RowCount` 全等；故意注入差异 → 必须 fail-closed。
   - memo 失效矩阵：窗口滑动、resize、theme、ledger 替换、epoch 打开各自命中/失效期望。
2. **集成回归（既有资产）**
   - exactly-once marker（真实 Windows Terminal / ConPTY E2E：`docs/plan/aicli-terminal-e2e-methodology.md` 口径）。
   - sticky top-align / resident tail 连续性、resize shrink/grow、alternate-screen 往返。
   - pager：锚点恢复、`ExpandAll`、长会话滚动（新增冷页懒加载用例）。
   - `DeferHistoryDelivery` 装载顺序（较早页不得晚于新页交付）。
   - partial/zero-write：逐出禁止、token 重试同身份（INV-10）。
3. **内存基准 harness（新增）**
   - 合成 5.5k / 20k / 50k cells 会话，稳态 60s；断言：
     `resident_cells ≤ maxResidentCells(+ε)`、`cell_rows_bytes ≤ 预算`、
     `live heap ≤ 120MB @20k`、`rematerialize_ms ≤ 50ms/块`。
   - 对照组 = 当前实现（记录 5.5k cells ≈ 210MB 活堆 / 99MB UI）。
4. **性能门禁**：P16 ≤250ms；pager 打开首屏 ≤1 帧；窗口滑动单次 ≤5ms（memo 命中路径）。

## 12. 风险与回退

| 风险 | 影响 | 缓解 | 回退 |
|---|---|---|---|
| 冷区重建不确定（presentation 依赖瞬时状态） | 重建出的 cell 与首次不一致 | `nonRebuildable` 标记 + fail-closed；只对可重建类放手 | 关闭 evictPrefix |
| sqlite 读抖动（2.4GB 库、与写池竞争） | pager 首屏变慢 | 复用 exclusive-before 分页 + 读池 + 后台预取 + 退避 | 提高 page 预算/关闭懒加载 |
| epoch/跨代次「Delivered 可再铸」误判 | exactly-once 被破坏 | 不确定性即 pin；只逐出永久 tombstone 结算的 cell | 关闭 evictPrefix |
| memo 失效风暴 | 冷区/窗口频繁重算 | 指纹扩展最小化；窗口滑动只失效窗口 | 关闭窗口化（S1 等价模式） |
| 会话切换 / 多实例并发 | 索引与实际 transcript 漂移 | 索引按 SceneID + indexRevision 管理；切换即重建；装载期冻结逐出 | 会话切换重建索引 |
| pager/ExpandAll 依赖全量正文 | 功能退化 | 懒加载按页满足；仅对当前打开页 + lookbehind 常驻 | 提高 pager lookbehind |
| 回归面大（pager/web/debug/executor） | 隐性副作用 | 分阶段 + 开关 + 影子对账 + 既有 E2E 门禁 | 逐阶段关闭 |

## 13. 附录

### 13.1 关键事实索引（供实现期核对）

| 主题 | 引用 |
|---|---|
| Cell 来源与身份 | `ui/render/encoding/encoder.go:223`（Encode）、`:1043 nextID`（`item-{n}` 单调）；`ui/scene/from_changeset.go:59 CellIDFromItemID`（INV-SCENE-02）、`:180 Apply` |
| Scene 版本 | `ui/scene/scene.go:26/195-196/207/320`（SceneID/Revision/ContentVersion/Snapshot） |
| 快照安装与 COW | `ui/app_state.go:74-100/119`；`ui/controller_state.go:449/930` |
| 整体替换路径 | `commands/chat_ui_actor.go:1048-1053`；`ui/controller_state.go:388-479`（`:465-470` 墓碑剪枝、`:478 sync`） |
| 规划与 memo | `ui/history_effect_planner.go:62/71/98/169/612/713/771/829-900`；`ui/history_effect_queue.go:953` |
| 交付账本 | `ui/history_commit.go:52-64/110-161/312/399/413/517/580/608/706/729/790/869/891/907` |
| frontier | `ui/history_effect_planner.go:198-208`（首个 mutable 为屏障） |
| pager | `ui/transcript_pager.go:88-112/127-132/164/297/329/384`；`commands/chat_transcript_pager.go:83-100` |
| 帧合成 / web SSE | `ui/app_render_frame.go:45-51`；`commands/chat_runtime_events.go:5217` |
| 存储分页 | `internal/chat/session_storage_factory.go:127-142`（`SessionHistoryPage`/`GetMessagePage`，默认 100 条 / 4 MiB）；`internal/chat/sqlite_storage.go:2394-2460` |
| 恢复路径（全量装载） | `commands/chat_actor_executor.go:684`；`commands/chat_session.go:161-175`；`chat_history.go:78` |
| 缓存预算 | `ui/transcript_layout_cache.go:189-190`；`ui/history_plan_cache.go:40-45`；`ui/tool_fold_omission_cache.go:17-27` |
| 软内存上限 | `cmd/aicli/memory_limit.go:18`（512 MiB）、`cmd/aicli/main.go:40` |
| 诊断出口 | `ui/transcript_layout_cache.go:263-295`（`TranscriptLayoutCacheStatsSnapshot`）；`ui/app_state.go`（`layout_cache`）；`commands/chat_debug_display_http.go:349-355/844-850` |

### 13.2 开放问题（设计评审需裁决）

> ⚠️ **本节已被 §17 收敛（v2）**：原 6 条开放问题已给出裁决与待验证项。


1. `ChainKey` / `BoundaryGroupKey` 是否允许只存哈希（pager 分组边界判定是否够用）？
2. 冷区重建的 presentation 覆盖面：diff / 语法高亮 / 图片附件 / 折叠态是否全部可确定性重放？
3. `ExpandAll` 模式下是否需要「按需全量」（影响逐出窗口策略）？
4. `sceneBlockGroups`（web SSE）是否必须全量 `kindByID`，还是可尾部窗口化？
5. `restoreChatStateFromRuntimeSession` 的 `session.Messages` 是否也纳入分页（LLM 侧已有 258 条 prompt 历史，但 canonical 全量装载目前在堆上）？
6. 逐出阈值滞回（1.25×）与 pager lookbehind 的取值需要 harness 标定。

### 13.3 参考

- `docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md`（§217 允许卸载已交付行；§274/278 handoff 不变量）
- `docs/plan/aicli-resume-p16-plan-freeze-attribution-20261008.md`（§4 增量 plan 取舍）
- `docs/plan/aicli-render-p1-1-step4-planning-incremental-plan.md`（§115-121 无预算单遍与截断语义教训）
- `docs/architecture/aicli-tui-renderer-architecture-design.md`（§20 append-only 单向交付）
- `docs/debug/cpu-hotspots-ui-render-and-session-persist-optimization-20260929.md`（长会话全量规划热点）

## 14. v2 规范：四层驻留 / 双代账本 / 写穿物化 / 流式装载（覆盖 §4–§6 对应小节）

> 本节是审查后唯一权威规范。命名与现有代码对齐；未提及处沿用本文原章节。

### 14.1 四层驻留模型（补 scene 层与冷区 store）

| 层 | 内容 | 驻留形态 | 上限（默认） | 退出条件 |
|---|---|---|---|---|
| **L0 Bridge Scene** | `commands/chat_runtime_events.go` 的 `renderScene` + `ChangeSetMapper` | 与 L1 同源的**窗口 + 索引**（推荐：scene 成为索引唯一持有者，AppState 从索引取窗口） | 同 L1 | 与 L1 同步滑动 |
| **L1 Window（热）** | 未交付增量 + 视口邻域 + pager 可见页 + active | 完整 `scene.TranscriptCell`（含 source/presentation） | `residentCells ≤ 512`、`residentRows ≤ 4096`、`≤ 24 MiB` | §14.3 守卫 |
| **L2 Cold Ledger（温）** | 已交付且已逐出的 cell 紧凑账本 + 分代行账本 | 定长 `ColdCellRef` + 前缀指纹（≤64 B/cell） | 20k cells ≈ 1.3 MB | — |
| **L3 Cold Body Store（写穿）** | 逐出时序列化的 cell body 快照（source/presentation 摘要） | `CellBodyStore`（首选复用 sqlite 会话库表；失败即放弃逐出） | 磁盘侧，不在堆上 | — |
| **L4 Canonical Store** | 消息 + 事件日志（jsonl / runtime store） | 既有分页 API（`GetMessagePage`、`ListEventsBefore`） | 不在堆上 | — |

**新增要求（B3）**：`renderScene` 不得无限累积；`scene` 包需新增与 L1 同源的裁剪入口
（或把索引上移到 scene 内部），并保证 reconcile / alias / compact 视图替换在窗口化 scene 上等价
（用 §16 的回归矩阵验证）。

### 14.2 数据结构（Go v2）

```go
// —— L2 冷区账本：已交付物理事实，只读、按代标记 ——
type ColdLedger struct {
    Cells         []ColdCellRef    // 交付顺序单调
    GenSpans      []GenerationSpan // 按代聚合的已交付行数（跨代拼接用）
    DeliveredRows int64            // 已交付物理行总数
}

type ColdCellRef struct {
    ID        scene.CellID
    Revision  uint64
    Kind      scene.CellKind
    Phase     scene.CellPhase
    SourceFP  uint64   // 覆盖空行/尾随行的 fragment 身份（INV-9）
    SourceLen int
    RowsAtGen int32    // 交付代的行数（含 gap 归属）
    Gen       uint64   // layout generation（与 Ack/LayoutGeneration 对齐）
    DeliveredAt int64  // 交付序（顺序断言）
    TombstonePermanent bool // 唯一允许逐出的终态（H3）
    Folded    bool     // H1：foldTarget 判定
    GroupKeyFP uint64  // ChainKey/BoundaryGroupKey 折叠指纹（分组边界）
    PrefixFence uint64 // H2：该 cell 之前的 finalized 前缀折叠值（增量维护）
}

// —— L1 窗口：当前代、可变、可重排、可重铸 ——
type WindowLedger struct {
    StartSeq, EndSeq int
    Cells            []scene.TranscriptCell
    LocalRows        []int32 // 每 cell 当前代行数（gap 归属规则同现状）
    TotalRows        int32
    Generation       uint64  // 当前 layout generation
}

// —— L3 写穿 body store（B1 方案 B）——
type CellBodyStore interface {
    Put(cell scene.TranscriptCell, ref ColdCellRef) error // 逐出前调用；失败即放弃逐出
    Get(id scene.CellID, rev uint64) (scene.TranscriptCell, bool)
}

type Residency uint8
const (
    Hot          Residency = iota // body 在窗口
    Cold                          // 仅 ColdCellRef
    Loading                       // 回填中（pin）
    Unrebuildable                 // 校验失败：永久 pin + diagnostics
)
```

### 14.3 守卫与状态机（覆盖 §4.5/§6.1）

```
Hot → Cold  ⟺ cellIsFinalizedForHistory(cell)
           ∧ 所有显示行已 Ack（AckFrame 证明；无 Unknown/partial/InvalidationPending）
           ∧ ref.TombstonePermanent                  # 永久墓碑，跨代不失效（H3）
           ∧ !epochOpen(state.HistoryEffects)        # 无打开中的 DeliveryEpoch
           ∧ !pinned(cell)                           # §14.4 pin 集合
           ∧ CellBodyStore.Put(cell, ref) == nil     # 写穿成功（B1；先写后切）
Cold → Loading ⟸ pager/锚点/回滚触达（含预取窗口）
Loading → Hot  ⟸ fp(body)==ref.SourceFP ∧ rows 校验通过
Loading → Unrebuildable ⟸ 任一校验失败（永久 pin；计数 rematerialize_failures）
```

说明：
- 守卫输入全部来自现成函数（`cellIsFinalizedForHistory`、`HistoryCommitLedger.Ack`、
  `entryBlocksRemintInEpoch`/`tombstoneBlocksRemint`、`hasSettledRecordForSource`、
  `retainedQueuedCommitForSource`），不新增语义；
- **原子性**：先对整段前缀全部完成 `Put`，再提交状态切换；任一条失败则整段放弃（不留半裁前缀）。

### 14.4 窗口滑动算法（覆盖 §5.2，O(k) + 滞回）

```
onReduce(state):
    mustKeep = firstIndexWith(未结算 ∨ 未 Ack ∨ Unknown/partial ∨ frontier barrier)
    foldTarget = ledger.lastFoldedCellID()          # H1：全局唯一值，来自 L2 索引
    desired  = min(mustKeep,
                   pagerVisibleStart  if pagerOpen,
                   viewportStart - margin)
    if window.TotalRows > maxRows * 1.25:           # 高水位：强制回收
        target = max(desired, window.EndSeq - maxCells)
    elif window.StartSeq < desired - hysteresis:
        target = desired
    else:
        return                                       # 不动
    slidePrefix(target)                              # 整 cell 前缀；单次 ≤ maxCells/4
```

- `slidePrefix` 只允许裁掉满足 §14.3 守卫的**连续前缀**（INV-6）；
- pin 集合：`active/finalizing/frontier barrier`、未 Ack 行、`DeferHistoryDelivery` 期、
  pager 可见页 ± lookbehind、可视区 ± margin、`Loading` 中的 cell；
- 滑动后 `indexRevision++`；`foldTarget` 不变则 memo 只失效窗口相关输入（H2）。

### 14.5 冷区回填与预取（off-lock，覆盖 §6.2 读路径）

```
ensureResident(range):                    # 调用方：pager / 回滚 / 锚点恢复
    for c in range where residency(c) == Cold:
        residency(c) = Loading            # pin，禁止并发逐出与重复请求
        enqueue(CellPageRequest{BeforeCellID: c.ID, LimitCells, LimitBytes})

fetch(page):                              # 后台 goroutine（不持有 actor 锁）
    cells, err = CellBodyStore.Get(page)   # 写穿 store 优先
    if err or miss: cells, err = eventReplayFallback(page)   # 兜底（B1 方案 A/C）
    for c in cells:
        if fp(c) != ref.SourceFP or rows(c) != ref.RowsAtGen: residency(c) = Unrebuildable; continue
        install Hot(c); layout(c)             # 当前代布局；miss 走 worker 池
    post(TranscriptPageLoadedAction{page})    # 内部 action，回 actor 原子应用
```

- 预取：pager 打开 → `lookbehind = 2 页`、前向 `1 页`；主界面回滚 → 只回填可见页；
- 占位高度：未到达前用 `ref.Kind/Folded/SourceLen` 估算，布局到达后校正（滚动条不跳）；
- 去重/取消：按 `(BeforeCellID, limit)` 去重；会话切换（SceneID 变化）时取消全部 in-flight；
- 失败退避：IO 错误指数退避（上限 3 次），期间视为 `Unrebuildable` 的临时态（仍 pin）。

### 14.6 流式装载铸提交状态机（覆盖 §5.3，B2）

```
loadSession(session):
    pages = canonicalPagesDescending(session)     # GetMessagePage 的 exclusive-before 游标；默认 256 cells / 2 MiB
    install(pages[0])                             # 尾部页先落地（用户最关心）
    for i, page in pages[1:]:
        prefetch(pages[i+1])                      # 后台预取，off-lock
        with actor lock (≤ 1 page budget, ≤250ms):
            layout(page)                          # 仅本页；miss 走 worker 池
            mint(page)                            # 只为未结算 cell 铸提交（提交=完整 cell 前缀）
        deliver(page)                             # 按 canonical 顺序；同页内按 cell 序
        awaitAck(page, bounded)                   # Unknown/partial → pin，不逐出
        evictDelivered(page)                      # §14.3 守卫 + 写穿；失败则保持 Hot
    closeEpoch()                                  # 结束 DeferHistoryDelivery
    publishScrollbackReplaceAuthorization()       # 沿用现有一次性替换授权路径
```

不变量（必须由测试断言）：
1. `deliverOrder == canonicalOrder`（较早页不得晚于较新页交付；append-only 不倒退；
   现有约束见 `history_effect_planner.go:634-645` 的装载窗口注释）；
2. 跨页 token 单调连续、每 cell 恰好交付一次（INV-3）；
3. 单次锁内 ≤ 1 页（P16 门禁 ≤250ms 不回归）；
4. **装载峰值内存 ≤ 2 页窗口**（当前页 + 预取页），而非全量 transcript。

与现有机制的对接：
- `DeferHistoryDelivery` 期不铸提交/不记 memo 的语义保持不变（本状态机在其内部推进）；
- compact 视图快路径（跳过全量事件日志重放）继续有效：本状态机从 canonical 页取源，
  不依赖事件日志全量重放；
- 一次性 scrollback 替换授权仍由装载路径显式发布（`chat_history_reconcile.go:482-486` 口径）。

### 14.7 全局属性与身份规则（H1/H2/H3/B4/INV-9）

| 属性 | 规则 |
|---|---|
| `foldTarget` | 全局唯一（“最近一次折叠”）；由 L2 索引 `lastFoldedCellID()` 提供；窗口渲染判定 `cell.ID == foldTarget`；纳入 memo 指纹。禁止在窗口内局部重算（否则提示位错位/丢失） |
| `fence` | `PrefixFence` 在索引内增量折叠（O(k)/次）；语义仍覆盖 ID/Revision/Sequence/Kind/Phase/blocked/Boundary/source len/ChainKey/BoundaryGroupKey；失效点沿用 `invalidateTranscriptPlanMemo`（`history_effect_queue.go:953`） |
| `generation` | 双账本：`ColdLedger.GenSpans`（只读）+ `WindowLedger.Generation`（当前）；跨代行号不混用；`Ack` 代校验（`ErrStaleLayoutGeneration`）；pager/status/web 必须声明所用代 |
| gap / 尾行 | gap row 归属**后继 cell**（与 `warmTranscriptLayoutChunk` 口径一致）；plain source 的尾随空行用 fragment 身份参与 `SourceFP`；`RowsAtGen` 含 gap、不含下一 cell 的 gap |
| `GroupKey` | 索引只存 `GroupKeyFP`（ChainKey/BoundaryGroupKey 折叠指纹）；pager 分组边界用指纹比对；需要原文时经 §14.5 回读 body |

### 14.8 正确性证明要点（实现期作为断言/测试挂点）

1. **exactly-once**：`ColdCellRef` 与 `HistoryCommitLedger` 墓碑同源（同一 `historyCommitSourceIdentity` 口径）
   + token 单调 + `DeliveredAt` 单调 → 同一来源身份不会被二次交付。
   断言：`deliveredSet ∩ reMintSet == ∅`（按身份比对，含 Unknown 恢复路径）。
2. **resize 安全**：冷区 `RowsAtGen` 只读、不参与重排；窗口重排产生新代但**不改写**已交付行；
   `Ack` 只接受当前代或明确重铸代。断言：resize 前后 `ColdLedger` 字节级不变。
3. **渲染等价**：窗口布局 = 全量布局的**子序列**（同一布局函数、同一缓存键、同一 `foldTarget` 全局值）。
   断言：随机会话下 `layout(window) == filter(layout(all), window)`（逐行比对，含 gap/空行）。
4. **内存有界**：驻留集合 = 窗口 ∪ pin，且 `|window| ≤ maxResidentCells`、pin 集合有界
   （active 1、未结算 ≤1 页、pager 页、可视区）。
   断言：20k cells 长跑 60min 后 `resident_cells` 与 `cache_bytes` 不随总 cell 数增长。

### 14.9 虚拟化 pager（H4）

```
open/scroll(pager):
    heights = ledger.heights(generation)            # O(1) 前缀和（ColdLedger.GenSpans + WindowLedger.LocalRows）
    visible = indexRangeAt(anchor, viewportRows)     # O(log n)
    for cell in visible ± lookbehind: ensureResident(cell)   # §14.5
    rows = concat(rowsFor(cell))                     # 只物化可见页
```

- `ExpandAll`：折叠态高度已知；展开态高度**按需计算并写回旁路表**（`expandedHeights[cellID]`），
  未加载页使用估算高度、加载后校正——不要求全量展开；
- 锚点保持现有语义：`TranscriptPagerAnchor{CellID, Row, LayoutGeneration, Offset}`（`transcript_pager.go:127-132`），
  跨代时优先按 `CellID` 命中，失败再退回 `Offset`（与现状一致）；
- 交付账本与 pager 解耦：pager 只读语义窗口，**不参与交付顺序**；`ExpandAll` 不改变交付行账本。

### 14.10 帧合成 / SSE 窗口化（H5）

- `ComposeAppRenderFrame`：只物化「可视区 ± margin + active + gap 上下文」的
  `map[CellID]TranscriptCell`；`foldTarget` 由 L2 索引给出（§14.7），不再需要全量 cells；
- `sceneBlockGroups`（web SSE）：改为尾部窗口（与 `/web/api/screen?tail=N` 同一口径）；
- `/debug/chat/status`、`/debug/chat/screen`：默认窗口；`full=1` 显式请求全量（诊断用，不进生产路径）。

## 15. 配置与观测（v2，覆盖 §9.1/§9.2）

### 15.1 配置项与默认值（v2）

| 键 | 默认 | 说明 |
|---|---|---|
| `ui.residency.enabled` | `false` | 总开关（灰度） |
| `ui.residency.evictPrefix` | `false` | 逐出开关；Spike 通过前保持 false |
| `ui.residency.maxResidentCells` | `512` | L1 cell 上限 |
| `ui.residency.maxResidentRows` | `4096` | L1 行上限 |
| `ui.residency.maxResidentMiB` | `24` | L1 字节上限 |
| `ui.residency.viewportMarginRows` | `2 × 视口高` | 可视区邻域 |
| `ui.residency.hysteresisCells` | `128` | 滑动滞回（防抖） |
| `ui.residency.evictHighWater` | `1.25` | 高水位倍数（`§14.4`） |
| `ui.residency.pageCells` | `256` | 装载/回填页大小 |
| `ui.residency.pageMiB` | `2` | 装载/回填页字节上限 |
| `ui.residency.prefetchPages` | `2`（pager）/`1`（forward） | 预取页数 |
| `ui.residency.bodyStore` | `sqlite` | 写穿目标；`off` = 只窗口化不逐出（保守档） |
| `ui.layoutCache.cellRowsMiB` | `16` | 行缓存预算（窗口化后） |
| `ui.layoutCache.historyPlanMiB` | `8` | 计划缓存预算 |
| `ui.layoutCache.foldOmissionMiB` | `8` | 折叠判定缓存预算 |
| `ui.memory.softLimitMiB` | `384` | 软上限（`AICLI_MEMORY_LIMIT`/`GOMEMLIMIT` 优先） |

### 15.2 状态字段（`/debug/chat/status#app_state.residency`，v2）

```json
{
  "enabled": true, "evict_prefix": true,
  "resident":    {"cells": 512, "rows": 3902, "bytes": 25165824, "window_start_seq": 4988, "generation": 42},
  "cold":        {"cells": 4988, "bytes": 319232, "delivered_rows": 116120, "generations": [41, 42]},
  "index":       {"prefix_fence": 123456789, "fold_target": 7311, "revision": 17},
  "evictions":   {"total": 4988, "last_reason": "acked+permanent", "pinned_by_reason": {"active": 1, "unacked": 3, "pager": 8, "viewport": 4, "loading": 2}},
  "rematerialize": {"total": 37, "max_ms": 41, "failures": 0, "bytes_total": 10240000, "inflight": 1},
  "body_store":  {"puts": 4988, "gets": 37, "put_errors": 0, "get_misses": 0}
}
```

### 15.3 分区内存口径（H6）

内存 harness 与 status 必须按子系统输出：

`scene_cells_bytes`（L0）、`appstate_cells_bytes`（L1）、`ledger_bytes`（L2+交付账本）、
`seed_units_bytes`（canonical seed）、`session_messages_bytes`、`render_cache_bytes`（三类缓存）。

验收判定以**分区可归因**为前提：任一子系统随会话线性增长而未说明者，视为回归。

## 16. 验证与门禁（v2，覆盖 §11）

### 16.1 Spike（进入 Stage 3 前必须全绿）

| Spike | 内容 | 通过标准 |
|---|---|---|
| **Spike-1 写穿物化** | 抽样 1k cells：`Put` → `Get` → 与首次构造逐字节比对 | 一致率 100%；读回 p95 ≤50ms；页字节 ≤2 MiB |
| **Spike-2 流式装载** | 5.5k cells 会话按 §14.6 装载 | exactly-once marker 全绿；P16 ≤250ms；装载峰值 ≤2 页 |
| **Spike-3 虚拟 pager** | 5.5k / 20k cells 打开与滚动 | 首屏 ≤1 帧；滚动帧时 p95 ≤16ms；锚点恢复 100% |
| **Spike-4 内存基线** | 20k cells 合成会话 60min 稳态，按 §15.3 分区 | live heap ≤120 MB；WS ≤384 MB；`resident_cells` 不随总量增长 |

### 16.2 回归门禁（沿用既有资产）

exactly-once（真实 Windows Terminal / ConPTY）、sticky top-align / resident tail 连续性、
resize shrink/grow 与 alternate-screen 往返、`DeferHistoryDelivery` 装载顺序、
partial/zero-write（逐出禁止、同 token 重试）、pager 锚点与 `ExpandAll`、
reconcile / alias / compact 视图替换（B3 窗口化 scene 等价性）。

### 16.3 性能门禁

窗口滑动 ≤1ms（memo 命中）；单帧布局 ≤5ms（miss ≤64）；装载单次锁内 ≤250ms；
pager 首屏 ≤1 帧；冷页回填 p95 ≤50ms；`rematerialize_failures == 0`。

## 17. 开放问题（v2 收敛，覆盖 §13.2）

| 原开放问题 | 裁决 | 待验证 |
|---|---|---|
| `ChainKey`/`BoundaryGroupKey` 是否只存哈希 | **是**：只存 `GroupKeyFP`；原文按需回读 | pager 分组边界在指纹碰撞下的行为（加长哈希 + 兜底比对） |
| 冷区重建的 presentation 覆盖 | **写穿快照覆盖全部类型**（source + presentation 摘要）；事件重放仅兜底；不可重建者 `Unrebuildable` 永久 pin | Spike-1 覆盖 diff/高亮/折叠/图片附件的序列化 |
| `ExpandAll` 是否要求全量 | **否**：估算高度 + 按需校正（§14.9） | 展开态滚动精度与锚点稳定性（Spike-3） |
| web SSE 是否必须全量 `kindByID` | **否**：尾部窗口（§14.10） | 前端分组语义在窗口边界的行为 |
| `session.Messages`/canonical seed 是否分页 | **是**：§14.6 装载与 §15.3 口径同时收敛 | 分页后 LLM 侧 token 统计与 compact 视图一致性 |
| 阈值/参数标定 | 默认值见 §15.1 | Spike-4 后按数据固化并写入运行手册 |

## 18. 修订记录（审查 → 文档变更）

| 审查项 | 本文章节变更 |
|---|---|
| B1 冷区重建闭环 | §14.1（L3 写穿 store）、§14.2 `CellBodyStore`、§14.3 写穿守卫、§14.5 回填 |
| B2 装载全量铸提交 | §14.6 流式装载状态机（页序/token/锁内/峰值不变量） |
| B3 scene 层全量 | §14.1 L0 + 「新增要求」、§14.10、§16.2 等价性回归 |
| B4 行账本代际 | §14.2 双账本、§14.7 generation 规则 |
| H1 foldTarget | §14.7、§14.4 |
| H2 fence 前缀指纹 | §14.2 `PrefixFence`、§14.7 |
| H3 epoch 逐出二义 | §14.3 守卫（永久墓碑 + epoch 关闭） |
| H4 pager 虚拟化 | §14.9 |
| H5 帧合成/SSE | §14.10 |
| H6 内存口径 | §15.3 |
| P1 默认值/门禁/开放问题 | §15.1、§16、§17 |

> 审查报告：`docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020-review.md`。
> 实施顺序：Spike-1..4 → P0 项落码（§14.1–§14.6）→ Stage 3 灰度（`evictPrefix=true`）→ Stage 4 默认开启。
>
> **实施任务清单与验证计划**：`docs/plan/aicli-transcript-residency-implementation-tasks-20261020.md`
> （T0–T10 任务分解、R1–R8 实机 ConPTY 用例矩阵、里程碑硬门禁与证据登记表）。