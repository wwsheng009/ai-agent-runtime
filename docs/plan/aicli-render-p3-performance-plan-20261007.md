# aicli 渲染 P3（性能收敛）专项计划：O(delta) 成本模型

> 依据：`docs/plan/aicli-unified-render-architecture-audit-20261005.md` §4 TOP1/2/3/6/7/9、§6 P3（:275-282）、§6.1（:290）；
> `docs/architecture/aicli-tui-renderer-architecture-design.md` §7.1 P3 行、§7.2 验收行；`docs/plan/aicli-render-remaining-defect-ledger-20261006.md` A4。
> 基线：`feat/render-p0-writer-unification` @ `8f6b32b8`（工作树干净）。
> 前置侦察：2026-10-07 三路只读（帧路径成本解剖 / 基准与验收设施 / active markdown 增量可行性），结论并入 §1，不另设侦察文档。
> 状态：**S0–S4 完成**（S4 以 profile 修正后的真实热点落地，见 §5.5；结构化源增量缓存登记为后续项）；切片记录见 §5。

## 0. 目标与验收

**目标形态**：每 delta 工作量 **O(delta) + 常量帧**；成本与全屏面积、历史总量脱钩；p95 帧时延
**< 16ms**（流式稳态）；每帧分配 / GC / 锁持有可量化。

**验收（审计 §6.1 P3 行）**：新基准报告（p50/p95 帧时延、每帧分配、GC 次数、锁持有时间）+
delta 成本 O(delta) 证明。

**不回归面**：ui / commands 全量 + `-race` 全绿；真机 e2e（history exactly-once、无 CSI 3J）
保持通过。

## 1. 现状基线（2026-10-07 侦察结论）

### 1.1 每帧成本下界（unified viewport 路径「恒做」清单）

| 项 | 位置 | 量级 |
|---|---|---|
| 整屏行数组分配 | `app_screen_layout.go:90,130-136`（`makeAppScreenRows(height)`） | O(height) 分配/帧 |
| 行组装 + 逐行 clone | `app_render_frame.go:54-77`；`terminal_session.go:68-87` | O(height) 克隆/帧 |
| **`terminalFrameCells` 全 height 行物化** | `terminal_session.go:1160-1168` → `:1842-1875`（每行 `vt.NewScreen(width,2)` + `style.RenderDocument` + Feed） | O(height×(spans+W))，每行一次分配；viewport 只消费其子集 |
| `plan.Clone()` 深拷贝 | `terminal_session.go:176-187`，调用点 `:715` | O(rows×spans) 防御性开销 |
| `ScreenModel.Clone()` front+back 深拷贝 | `terminal_session.go:874`；`renderengine/screen_model.go:71-79,580-593` | 每帧整网格复制 |
| `StageFrame` 逐行 `normalizeRow` 重分配 | `screen_model.go:141-152,564-570` | O(height×W) |
| `PrepareFlush` 全行扫描 + `copy(front,back)` | `screen_model.go:294-339`（:328-330 无条件整网格拷贝） | O(height×W) |
| **历史写入 → viewport 全量 `Invalidate` → forceRepaint** | `terminal_session.go:993-995`（`transitionBytes!="" || historyBytes!=""`）；`screen_model.go:303-314` | 任何历史插入整段重绘 viewport |

触发条件归纳：以上为**每帧恒做**（与变化量无关）；`DirtyFlags`（8 类）只存在于 `FramePump`
（`frame_pump.go:14-27`），TerminalSession flush 路径**不消费**、且无行坐标。

### 1.2 已有增量设施（可复用，勿重造）

| 设施 | 键/状态 | 证据 |
|---|---|---|
| Scene Revision / ContentVersion | 全局单调；事务提交 revision++ | `scene/scene.go:195-197,259-265,282-297,414-416`；`transaction.go:63-82` |
| per-cell Revision | cell 级 COW 变更栅栏（严格递增） | `scene.go:552-576,579-605,608-637` |
| **splitSourceLinesCache（append 前缀复用，唯一样板）** | `CellID` + `(revision, source)`；`hasPrefix` 复用 | `scene/layout_cache.go:31-99,104-125` |
| sharedCellRows / sharedHistoryPlan | 内容寻址（`cellLayoutKey`），LRU | `transcript_layout_cache.go:171-217`；`history_plan_cache.go:27-54` |
| transcriptPlanMemo | SceneID + finalized 前缀 fence + 布局/主题/epoch | `history_effect_planner.go:654-689,745-788` |
| preparedHistory 缓存 | (width, theme, commits 呈现代相等)；写后清空 | `terminal_session.go:280-290,803-823` |
| ScreenModel 双缓冲 + `diffRow` | 行级 cell 差分（未被上层利用） | `screen_model.go:294-339,455-483` |
| `sampleRingP95`（有界样本环） | 可直接复用为帧时延 p95 估计器 | `controller.go:167-211` |
| PaintTrace（emit/change/white 计数） | O(delta) 证明的工作量计数 | `renderengine/paint_trace.go` |
| `historyPrepareHits/Misses` | prepare 命中率（TOP9） | `terminal_session.go:358,808` |

### 1.3 缺口（S1–S4 的改造面）

1. **行身份缺失**：`AppScreenRow`（`app_screen_layout.go:21-35`）、`AppRenderRow`（`app_render_frame.go:16-19`）
   无 Revision/hash；`TerminalFramePlan` 只有全局 `LayoutGeneration/TerminalEpoch`（`terminal_session.go:45-63`）
   ——presenter 无法从 plan 得出脏行集合。
2. **无「变更行区间」生产者**：`LayoutAppScreen` 每帧返回全窗口行；`DirtyFlags` 无行坐标。
3. **`terminalFrameCells` 契约强制 `len(rows)==height`**（`terminal_session.go:1843`）→ 全量物化。
4. **历史写入触发全屏 forceRepaint**（见 1.1 末行）。
5. **active markdown 语义路径每帧全量重算**：`active_cell_projection.go:189`（全源 `markdown.Render`）+
   `:193`（前缀渲染）+ `:194`（全前缀 `LinesEqual`），失败再渲一次 `:141`；**无跨帧文档缓存**
   （`RenderCache` 只服务已提交 cell 与 legacy band）。关键待接线件：`suffixProjector`
   （`active_cell_projection.go:264-352`）已实现且有测试，**生产路径未使用**。
6. **测量缺口**：无逐帧时延分布 benchmark；全仓无 `ReadMemStats/NumGC` 使用；无锁持有时长埋点；
   无 O(delta) 增长断言；无确定性 N 行/s 注入器（仅有真机 e2e）。

## 2. 切片计划（S0–S4，先仪器后优化）

> 硬规则：每个切片先落**等价/回归判据**，再动实现；性能数字一律用差分口径（见 §3），
> 不以单次绝对值为准。S1/S2 可并行；S3 依赖 S0 的帧级仪器；S4 独立于 S1–S3。

### S0 基线仪器（先做；0.5–1 天）

- **帧时延分布**：新增 `ui/frame_latency_bench_test.go`——确定性 delta 注入
  （`UpdateActiveCellAction`）→ reduce → compose → flush（计数 writer）；
  批测摊薄（16 delta/样本；本机紧循环 `time.Now()` 读数量化 ~0.5ms，单 delta 计时
  不可靠，见 §5.1 口径）后 `b.ReportMetric(p50/p95/max)`。报告口径；
  硬断言（p95 < 16ms）在 S1–S4 差分验收启用。
- **每帧分配/GC**：`runtime.ReadMemStats` 前后差 / 帧数 + `testing.AllocsPerOp`；
  跨历史规模（300 / 2000 / 6719 cells）断言**分配不随历史总量增长**。
- **O(delta) 矩阵**：历史规模 × 恒定 delta 数，报 `ns/delta`、`allocs/delta`；
  断言 2× 规模斜率比 ≈ 1；用 `PaintTrace` emit/layout rows 计数交叉验证工作量 ∝ delta。
- **锁持有**：测试专用 wrapper 或对 `terminalSessionSnapshot` / `FlushTransaction` 临界区采样计时，
  报告 max/累计（不引入生产埋点）。
- **复用**：`BenchmarkDeferredOlderPageReplan` 11 个子基准（clone/plan/layout 拆项）、
  `BenchmarkLayoutAppScreenResumedSession`（自报 layout_rows）、
  `BenchmarkLayoutTranscriptTailVsFull`、`BenchmarkReplyStreamChunks`、
  `BenchmarkResumeStreamChunkWithLargeHistory`、`BenchmarkReplaceTranscriptMutableTailSnapshot`。
- **产出**：基线报告（p50/p95、分配/帧、GC、锁持有、O(delta) 斜率）+ 命令清单；
  记录本机与 CI 差分口径。

### S1 去全屏物化/编码（审计 TOP2；0.5–1 天）

- `terminalFrameCells` **viewport-only 物化**：只物化 `area.Top-1..` 的 viewport 行；
  历史行占位不编码、不 `vt.NewScreen`（把每行一次分配改为仅 viewport 行）。
- 等价判据：现有 parity 用例（`TestLayoutTranscriptTailScreenRowsMatchesFullLayout` 等）
  + 真机 e2e（exactly-once / 无 3J）。

### S2 去全屏深拷贝（审计 TOP1；~1 天）

- **plan 单所有者零拷贝**：删除 `plan.Clone()`（`terminal_session.go:715`），以契约测试钉住
  「plan 传入后不再变更」；若契约不成立则改 move 语义（构造侧交出所有权）。
- **`ScreenModel` swap 而非 copy**：`PrepareFlush` 的 `copy(front,back)`（`screen_model.go:328-330`）
  改指针交换 / 差异复用；重审 `ScreenModel.Clone` 调用点（`terminal_session.go:874`）的必要性。
- 等价判据：`-race` 全绿 + 渲染 parity + 真机 e2e；失败回退保留 Clone 路径开关。

### S3 脏行 diff（审计 TOP3/9；2–3 天）

- **行身份入帧**：`AppScreenRow` / `AppRenderRow` / `TerminalFramePlan` 携带
  `CellID + Revision`（或内容 hash）；历史 staged 行附着 `HistoryCommit.FragmentID + DisplayRange`
  （`history_commit.go:52-64` 已有身份，仅未附着到帧行）。
- **历史写入不再全屏 Invalidate**：替换 `terminal_session.go:993-995` 的整段 forceRepaint，
  只重绘变更行 + 历史插入区域（resident 尾部）。
- **`StageFrame` / `PrepareFlush` 改脏行集合**：以版本比较替换全行扫描；
  校验失败即 **fail-closed 回退全量**（保留现路径）。
- 等价判据：帧内容 parity（同输入全量 vs 增量逐行等价）+ 真机 e2e；`historyPrepareHits/Misses`
  与 PaintTrace 白重绘计数作为回归栅栏。

### S4 active markdown 增量（审计 TOP6；1–2 天）

- **A 接线 `suffixProjector`**（零接口变更，性价比最高）：`ProjectActiveCellBandWithTheme` 内
  用 `prefix()/suffix()/live()` 替换直接调用 `activeMarkdownSuffixLines`，消除 planning pass 内
  2×(N+2) 次全量重渲（`active_cell_projection.go:257-263` 注释自述）。
- **B 跨帧前缀缓存**：键 `{CellID, Revision, width, themeFingerprint}`；命中条件
  `strings.HasPrefix(source, cached.source)`（照抄 `scene/layout_cache.go:104-125` 语义）+
  Revision 前进；校验沿用渲染行前缀相等（`active_cell_projection.go:194`）。
- **C stable/holdback 拆分**：stable cut 来自 `markdown.StreamCollector`（`stream.go:151-183`）；
  stable 段走 `SharedRenderCache`（mode 区分），holdback 纯文本渲染；未闭合 fence 不高亮
  （`openMarkdownFenceStart`，`stream.go:185-222`）。
- **D chroma**：复用 highlightMemo；闭合块一次性 lex（配合分帧/预算上限，防单块 80ms 打爆帧）。
- **E 回退与护栏**：`projected=false → 全量渲染` 现成（`:134-143`）；每帧尾部 N KB 上限；
  action 开关回落当前全量路径。
- 开放问题（实施前确认）：encoder 层 assistant 流式 upsert 是否保证 Head 前缀扩展
  （决定 B 的 miss 率）；`SemanticActiveCellProjection` 生产默认启用面。

### 观察项（按 S0 报告决定是否纳入本轮）

- TOP7 每帧 ANSI 后处理逐字节扫描 + 每 CUP `strings.Split` 分配（审计 `:1048→:1391-1440` 为
  旧快照行号，需重定位）；
- TOP8 全局 `terminalWriteMu` / `transactionMu` 覆盖整批写（锁持有时长在 S0 中量化后再决策）。

## 3. 验收与门禁

- **本机口径**：差分 + p95 分布（沿用 P1-1 §1.7/§1.9 教训：`second_plan_prepend` 绝对值
  503–530ms 噪声带，不用单次绝对值卡线）。
- **硬验收**：p95 帧时延 < 16ms（稳态流式，本机口径）；O(delta) 斜率断言；
  每帧分配不随历史总量增长；ui / commands 全量 + `-race` 绿；真机 e2e 不回归。
- **门禁集成**：
  - 扩展 `scripts/test-aicli-resume-startup-perf-e2e.ps1`（P12 已打印 UI stall p95/max、
    P16 规划预算；复用为帧延迟代理）；
  - 新建 `aicli-perf-baseline.yml`（照 `frontend-perf-baseline.yml` 形态：手动触发 +
    报告 artifact + 红线脚本）；Go benchmark 不默认进 `go test`，需显式步骤；
  - `release-aicli.yml` 保持既有测试门禁（E4/parity/writer inventory），新增 perf 步骤为可选。

## 4. 风险与回滚

| 风险 | 缓解 |
|---|---|
| 深拷贝去除引入别名/竞态 | 契约测试钉住「plan 传入后不变更」；`-race` 全绿；保留 Clone 回退开关 |
| 脏行 diff 漏写（丢帧） | fail-closed：版本校验失败即全量 StageFrame + forceRepaint；parity 逐行断言 |
| markdown 增量前缀失效 | 现有 `LinesEqual` 逐帧校验 + `projected=false` 全量回退现成 |
| 性能门禁噪声 | 差分/分布阈值 + 报告 artifact；红线仅对显著回归 |
| 审计行号漂移 | 本计划已用 2026-10-07 实读行号；引用审计处标注快照 |

## 5. 记录（实施回填）

- **侦察（2026-10-07，三路只读，完成）**：帧路径成本解剖（恒做清单/克隆点/失效路径/增量设施/
  脏行缺口）；基准与验收设施（现有 bench 清单、Stage 0 基线方法与归因、可复用计数设施、
  5 项测量缺口、CI 集成点）；active markdown（每帧重复计算 10 项、缓存键表、增量可行性判定、
  最小接口草图 A–E）。以上结论已并入 §1–§2。

### 5.1 S0 基线报告（2026-10-07，本机 AMD Ryzen 7 5800H / Windows）

仪器：`backend/cmd/aicli/ui/frame_latency_bench_test.go`（挂具与生产统一路径同构：
UIController → presenter.Attach → TerminalSessionExecutor → TerminalSession → 计数 writer；
终端 100×24；delta = 3 行 ~100B；`flushed/delta = 1.000`，每 delta 一帧）。

**计时口径**：本机紧循环内 `time.Now()` 读数量化 ~0.5ms（单 delta 常读出 0，
实测：批测摊薄后稳定）。故每样本 = 16 delta 批耗时 / 16（量子误差 ≈30µs/delta）；
分布 = 批均值的 p50/p95/max。硬断言在 S1–S4 差分验收启用。

命令：

```powershell
go test ./cmd/aicli/ui/ -run '^$' -bench BenchmarkFrameLatencyStreaming -benchtime 50x
go test ./cmd/aicli/ui/ -run '^$' -bench BenchmarkFrameCostScale -benchtime 20x
go test ./cmd/aicli/ui/ -run '^$' -bench BenchmarkFrameAllocsPerDelta -benchtime 5x
```

**主基线（300 finalized cells 背景）**：p50 **2.05ms/delta**，p95 **2.47ms/delta**，
max 2.61ms/delta（mean ≈2.0ms/delta）。

**规模矩阵（per delta）**：

| 历史 cells | mean | p50 | p95 | max |
|---|---|---|---|---|
| 0 | 0.74ms | 611µs | 1.37ms | 1.53ms |
| 300 | 1.99ms | 1.82ms | 2.69ms | 3.90ms |
| 2000 | 4.01ms | 3.88ms | 4.41ms | 4.71ms |
| 6719 | 11.3ms | 11.2ms | **14.5ms** | 15.1ms |

**每 delta 分配（300 cells）**：2.64MB/delta、8,906 allocs/delta、~23 GC/64-delta 批。

**判据结论**：
1. 成本随历史总量近似线性/超线性增长（300→2000 ×2.1；2000→6719 ×2.9），
   **未与变化量脱钩**——O(delta) 目标不成立，即 S1–S4 的收敛对象。
2. 6719 cells 时 p95 14.5ms 已逼近 16ms 目标线（微挂具、无真实终端渲染），
   真机长会话风险更高；该矩阵同时是 S3/S4 验收的对照基线。
3. 每 delta 2.64MB / 8.9k allocs 是 §1.1「全屏克隆/物化」清单的直接量化。

**S0 未覆盖项（转入 S1–S4 验收）**：锁持有时长——本机时钟量子 ~0.5ms 使 µs 级
临界区计时不可靠，且「不引入生产埋点」前提下无低侵入门；改由真机 perf e2e P12
（UI stall p95/max，`test-aicli-resume-startup-perf-e2e.ps1`）与 S3 的 PaintTrace
工作计数共同约束。

> **测量修正（2026-10-08，见 §5.3）**：本报告与 §5.2 的每 delta 绝对值含挂具噪声——
> 挂具 `stepOnce` 每 delta 调 `UIController.State()`，其 `AppState.Clone()` 深拷贝整张
> `HistoryCommitLedger`（300 cells 时占挂具分配 **75%**）；生产逐帧路径
> （`terminalSessionSchedule`/`terminalSessionSnapshot`）从不克隆账本。挂具修正后的
> 真实基线为 **526KB/delta、2407 allocs/delta**；上文时延/分配绝对值按同比例缩水，
> S1 的相对 A/B 结论不变。

### 5.2 S1 实施记录（viewport-only 物化，已完成）

- 实现（`terminal_session.go`，commit `9415f683`）：新增
  `terminalFrameCellsWindow(rows, renderRows, width, height, topRow, rowCount, theme)`
  按 1-based 窗口物化；`terminalFrameCells` 退化为全帧包装（直接调用面/错误语义不变），
  `terminalViewportCells` 以 `area.Top/area.Height` 直调窗口函数，不再「全帧物化后切片」。
- **校验契约保持全帧**：行身份 / 文本 parity / 行控制检查仍覆盖每一行——viewport 之外的
  行也必须报错（`TestTerminalSessionRejectsMismatchedStructuredFrameText`、
  `TestTerminalSessionTransactionDefersPreparedHistoryWhenFramePreflightFails` 钉住）；
  仅窗口外行跳过 `style.RenderDocument` SGR 编码与 `vt.NewScreen`/`Feed`/`CellRows` 展开。
- 等价判据：新增 `TestTerminalViewportCellsWindowMatchesFullFrameMaterialization`
  （全帧物化取窗口 vs viewport-only 逐 cell `reflect.DeepEqual` + 返回行数）；
  全量 ui/commands 绿（118.4s / 186.7s）；真机 e2e 6 项 PASS
  （73 行 exactly-once / 无 3J / markdown 一次渲染）。
- 收益（同机 A/B，`-count=3` 中位数；窗口 4 行 / 跳过 20 行，100×24、300 finalized cells）：
  p50 2.41→2.18ms（**-9.5%**）、p95 3.93→2.71ms（**-31%**）、
  B/delta 2.634→2.454MB（**-6.8%**）、allocs/delta 8905→8808（-1.1%）。
  基准新增 `viewport_rows`/`skipped_rows` 报告（注：计时循环前的 `ReportMetric`
  会被测试框架丢弃，须在循环后上报）。
- 归因注：分配大头不在物化面——S1 只去掉编码/VT 展开，剩余大头在全屏深拷贝（S2）
  与全行扫描/失效（S3）。

### 5.3 S2 实施记录（去全屏深拷贝：稳态就地事务 + 延迟提交，已完成）

- 提交锚点：代码 `fa39502d`、本记录 `cb4be221`（锚点行于 `cb4be221` 后补记）。
- 测量修正（先导，pprof 实锤）：挂具改用无账本访问器（`ActiveCellState`/
  `DiagnosticState`，与生产适配器一致）。修正后真实基线 **526KB/delta、2407 allocs/delta**；
  S0 报告的 2.64MB/delta 中约 79% 是挂具账本克隆噪声（S1 的 7% 相对收益不受影响）。
- 实现：
  1. `TerminalSession.FlushTransaction` 删除 `plan.Clone()`、`CommitHistory` 删除
     `commit.Clone()` →「只读契约 + 一次构造」（compose 阶段已克隆载荷；执行器提交的
     claim 快照已分离），新增契约注记。
  2. `flushTransactionLocked` 稳态帧**就地事务化**（删除每帧 `ScreenModel.Clone()`）：
     结构变更帧（租约/几何/视口位置或尺寸变化，需 Resize/Invalidate 清空双缓冲）仍走
     事务性候选克隆——零字节失败保持已确认模型的既有契约由此保留；`Clone()` 保留为
     回退开关。
  3. `ScreenModel.PrepareFlush` **延迟提交**：不再暂存推进 front / 不再置投影 Unknown /
     不清 `forceRepaint`；`ConfirmFlush` 才提交（front←back、清 forceRepaint、Known）。
     就地路径的零字节失败因此保持已确认状态（下一帧仍可增量 diff），写失败恢复语义仍由
     `MarkWriteFailed` 保证；`TestScreenModelWriteFailureRequiresRecoveryBeforeDiff`
     断言更新为新契约（prepare 后 Known，MarkWriteFailed 后 Unknown）。
- 验收：全量 `ui/...` + `commands` 绿（ui 15.0s、renderengine 2.4s、commands 173.7s）；
  真机 e2e 6 项 PASS（73 行 exactly-once / 无 3J / markdown 一次渲染）。
- 收益（同机 A/B，`-count=3` 中位数；两侧同一修正挂具）：B/delta 524.3→399.4KB（**-23.8%**）、
  allocs 2406→2367、p50 808→595µs（**-26.4%**）、p95 1.68→0.97ms（**-42%**）。
- 剩余热点（修正后 pprof）：`vt.blankRow` 24.7%、`normalizeRow` 13.3%、
  `vt.(*Screen).CellRows` 10.3%、`ProjectActiveCellBandWithTheme` 11.7% cum →
  转入 S3（脏行）与 markdown 增量（S4 前置）。

### 5.4 S3 实施记录（脏行短路 + 行物化复用，已完成）

- 提交锚点：代码与测试 `e61b971f`。
- 侦察（S2 后稳态 pprof，alloc_space）：`vt.blankRow` 26.0% + `vt.(*Screen).CellRows`
  15.4%（`terminalFrameCellsWindow` 每个 viewport 行 `vt.NewScreen(width,2)` 的
  width×2 空白矩阵 + 逐 cell 拷贝）、`normalizeRow` 7.4%（StageFrame 每帧每行重分配）
  ——三者合计约 49% 的分配面，是 S3 的主战场。
- 实现：
  1. `terminalFrameCellsWindow`：窗口内各行共用一个 `vt.Screen`，行间 `Reset()` 复用
     已分配的行缓冲（其文档语义即「重放单行流不得每行新建 width×height 矩阵」）；
     VT 展开规则不变（仍是同一 `Screen.Feed`）。
  2. `ScreenModel.StageFrame` **脏行短路**：宽度已规范且与当前 staged 行逐 cell 相等
     的行直接复用既有 back 切片，不再 `normalizeRow` 重分配。稳态帧视口大多数行不变、
     只有流式行变化，此路径把「每帧每行一次分配」降为「仅变更行分配」；跳过是语义
     无操作（back[r] 已等于该内容）。
  3. 历史写入的「视口段全量重绘」**维持不变**（评估后不做）：视口模型自 S1 起只有
     `area.Height` 行（挂具 4 行），且历史插入可经终端滚动影响保留区（§1.1 末行），
     全量重绘是正确性要求且成本有界；pprof 未显示其为分配大头。收窄方案（令
     `terminalHistoryInsertionANSI` 报告是否发生跨行一滚动、仅 underfill 时跳过重绘）
     登记为遗留项：收益上限 = 4 行强制重绘，且触及 P2 锚定/滚动正确性，暂不启动。
- 等价判据（新增测试，全部绿）：
  - `renderengine/screen_model_dirty_rows_test.go`：短路实现 vs「逐行全量 normalize」
    参考实现——staged 网格 `DeepEqual` + `PrepareFlush` 字节相等；未变更行切片身份
    保持（无重分配）；相同行重复 StageFrame 零字节。
  - `ui/frame_dirty_rows_test.go`：稳态连续 6 帧单行变更——每帧仅变更行的 CUP 输出
    （未变更行零字节）、PaintTrace `PaintedRows==1`、`White==0`、`Missing==0`（累计同）；
    `terminalFrameCellsWindow` 复用实现 vs「每行独立 NewScreen」参考实现逐 cell 等价，
    且带样式行之后的未着色行无 SGR 泄漏。
- 收益（同机 A/B，`-count=3` 中位数；仅两生产文件差异）：B/delta 399.0→232.7KB
  （**-41.7%**）、allocs/delta 2367→2322、GC/iter 3–4→2、p50 601→545µs（-9.3%）、
  p95 1011→881µs（-12.9%）。
- 验收：全量 `ui/...` + `commands` 绿（ui 13.5s、commands 173.2s）；`-race` 两包绿
  （ui 36.5s、renderengine 3.4s）；真机 e2e 6/6 PASS（73 行 exactly-once / 无 3J）。
- 剩余热点（S4 面）：`ProjectActiveCellBandWithTheme` 9.1% cum（active markdown 每帧
  全量重算）→ S4 接线 `suffixProjector` + 跨帧前缀缓存。

### 5.5 S4 实施记录（profile 修正后的真实热点：plain band 尾部展开 + 检测去 regexp，已完成）

- 提交锚点：代码与测试 `608124dc`。
- 侦察修正（先测量再动刀）：S4 原定的「markdown 每帧全量重渲」在当前挂具上**不是**
  最大头——pprof 中 `activeMarkdownSuffixLines` 根本不出现；真实分布是
  `wrapPlainAppScreenText` 37% cum（`activeCellBandRows` 对**整段源**逐逻辑行 wrap，
  随后只保留视口尾部 maxRows 行）与 `LooksLikeMarkdown` 31.8% cum（每帧两处调用：
  投影 + `deriveActiveStableEnd`；其中 regexp 回溯 23%）。
- 实现（两生产文件）：
  1. `activeCellBandTailRows`（`active_cell_projection.go`）：从尾部反向数至多
     maxRows 个换行，只展开尾部逻辑行、返回尾部 maxRows 个可视行。wrap 按逻辑行
     独立展开（不跨行携带状态），尾部展开与「全量展开后取尾部」逐行等价；
     `ProjectActiveCellBandWithTheme` plain 分支接线。每帧成本从 O(源长度) 降为
     O(视口行)。
  2. `LooksLikeMarkdown` 去 regexp、去全源 Split：线性字节扫描 + 字节门卫
     （标题/引用必含 `#`/`>`，表格行必含 `|`，否则跳过 TrimLeft/TrimSpace 的
     逐 rune 扫描——门卫前两者占该函数成本三分之一以上）。语义与旧正则实现
     逐例等价。
- 等价判据（新增测试，全绿）：
  - `active_cell_band_tail_test.go`：13 组源（含 CRLF/控制序列/组合字符/超宽行）
    × 4 宽度 × 4 maxRows 的「尾部 vs 全量尾部」`DeepEqual`；2 万行头部 +
    小尾部的分配有界断言（≤64 allocs，防回退全源展开）；投影接线后逐行等于
    全量展开尾部。
  - `markdown/detect_test.go`：70+ 例语料 + **20,000 例固定种子随机串**与旧
    regexp 参考实现逐例等价；另有直接真值钉点。
- 收益（同机 A/B，`-count=3` 中位数；基线 = S3 `e61b971f`）：B/delta
  231.8→119.4KB（**-48.5%**）、allocs/delta 2322→610.7（**-73.7%**）、
  ns/op 8.77→2.97ms（**-66.1%**）、p50 539.7→189.3µs（**-64.9%**）、
  p95 942.3→255.9µs（**-72.8%**）；字节门卫追加后 p50 中位 169µs。
- 累计（对 S0 修正基线 526KB / 2407 allocs / p50 808µs / p95 1.68ms）：
  B/delta **-77.3%**、allocs **-74.6%**、p50 **-79.1%**、p95 **-84.9%**。
- 验收：`ui/...` + `commands` 全量绿（commands 168s）；`-race`（ui + markdown）
  绿；真机 e2e 6/6 PASS（73 行 exactly-once / 无 3J）。
- 遗留（登记不实施，需先补挂具）：结构化（markdown/reasoning）源仍每帧 1–2 次
  全源渲染（`activeMarkdownSuffixLines`/`activeReasoningSuffixLines` 的
  full+prefix 双渲），本挂具未覆盖；下一步先加 markdown 流式挂具量化，再按
  原 S4-A/B（`suffixProjector` 接线 + 跨帧前缀缓存）评估；C/D/E（stable/holdback
  拆分、chroma memo、回退护栏）随之。
