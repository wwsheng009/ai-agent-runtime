# aicli transcript 驻留降级 —— 实施任务清单与验证计划

> 来源设计：`docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020.md`（v2，权威规范 §14–§18）
> 审查依据：`docs/plan/aicli-transcript-residency-and-lazy-history-design-20261020-review.md`
> 验证基线：`docs/plan/aicli-terminal-e2e-methodology.md`（L1/L2/L3/L3-ConPTY 分层与断言清单）
> 状态：可执行任务清单（2026-10-20）。**硬性要求：涉及渲染的每个里程碑都必须有实机
> ConPTY / 真实 Windows Terminal 的通过证据（manifest.json + UIA dump），不得只靠进程内测试。**

## 0. 总览

### 0.1 里程碑

| 里程碑 | 内容 | 出口门禁（全部必须通过） |
|---|---|---|
| **M0 基线与护栏** | 观测字段、开关、内存基线、测试资产核对 | L3 全绿 + 内存基线报告（现状数据固化） |
| **M1 窗口化（不逐出）** | 双代账本 + 窗口化布局/帧/SSE + 缓存预算收紧；`evictPrefix=false` | L2 fixture E2E 全绿 + L3-ConPTY 全绿 + P16 ≤250ms |
| **M2 写穿与回填** | `CellBodyStore` 写穿、冷区回填、虚拟 pager；仍不逐出（影子校验） | 上述 + Spike-1 一致率 100% + 虚拟 pager 性能门禁 |
| **M3 逐出灰度** | `evictPrefix=true`（灰度）；流式装载 | **L1 真实 provider E2E 全绿 + 新增 Residency 矩阵 R1–R8 全绿** |
| **M4 默认开启** | 默认值切换（§15.1）+ 运行手册 | 连续 2 轮 L1 全绿 + 24h 观测无回归 |

### 0.2 依赖关系（文本图）

```
T0 基线/护栏 ─┬─► T1 Spike-1 写穿 ─┬─► T4 守卫与逐出 ─┬─► T5 回填/虚拟 pager ─┐
              ├─► T2 Spike-2 流式装载（并行）          │                       ├─► T9 验证总集 ─► T10 灰度/默认开启
              ├─► T3 双代账本/索引 ────────────────────┘                       │
              ├─► T6 scene 层窗口化 ──────────────────────────────────────────┘
              └─► T7 帧/SSE/调试端点窗口化 ───────────────────────────────────┘
T8 配置与观测贯穿全程（每个 T 交付即可观测）
```

### 0.3 完成定义（DoD，对每个任务生效）

1. 代码：`go build ./...`、`go vet ./cmd/aicli/...`、`go test ./cmd/aicli/... -count=1` 全绿；
   涉及并发路径的包必须跑 `-race`。
2. 行为：新增能力默认关闭（开关见 §15.1），关闭时与原实现**逐字节等价**（回归基线）。
3. 观测：新状态字段可在 `/debug/chat/status` 读到，计数器可归因（§15.2/§15.3）。
4. 证据：给出可复现命令 + 产物路径；涉及渲染的，必须附实机 E2E 证据（见 §2）。

## 1. 任务清单

### T0 基线与护栏（M0）

| 项 | 内容 |
|---|---|
| 目标 | 固化现状基线、建立开关与观测面，为后续 A/B 提供对照 |
| 交付物 | ① `ui.residency.*` 开关骨架（全部默认 off，行为与原实现一致）；② `app_state.residency` 状态字段骨架（§15.2）；③ 内存基线脚本（§15.3 分区口径，采集 live heap/WS/分区占用）；④ 现状对照数据（5.5k cells：活堆 210.6 MB / UI 99.1 MB / cell_rows 64 MiB 顶格） |
| 涉及文件 | `cmd/aicli/ui/*`（开关注入）、`internal/config/manager.go`、`cmd/aicli/commands/chat_debug_display_http.go`（状态投影）、`scripts/`（基线脚本） |
| 依赖 | — |
| 验收 | L3 全绿；基线脚本可在当前实例上复现 5.5k cells 数据（误差 <5%）；开关 off 时 pprof 对照无差异 |

> 状态：✅ 已完成（2026-10-20）。证据见 §7 登记表 M0 行。

### T1 Spike-1：写穿物化（M2 前置，可与 T2 并行）

| 项 | 内容 |
|---|---|
| 目标 | 证明「逐出时序列化 cell → 读回 → 与首次构造逐字节一致」且成本可接受 |
| 交付物 | ① `CellBodyStore` 接口 + sqlite 实现（表结构：`session_cells(session_id, cell_id, revision, source_fp, rows_at_gen, gen, payload)`）；② 序列化/反序列化（含 Source/Presentation/边界键/折叠态）；③ 校验器（FP/row 数）；④ 抽样 1k cells 的实测报告（一致率、p95 读回耗时、页字节） |
| 涉及文件 | 新增 `cmd/aicli/ui/transcript_lazy_source.go`、`internal/chat/session_cell_store.go`（或复用 artifact store） |
| 依赖 | T0 |
| 验收 | 一致率 **100%**；读回 p95 ≤50ms；单页（256 cells）≤2 MiB；`put_errors=0`（1k 抽样） |

> 状态：✅ 已完成（2026-10-20）。证据见 §7 Spike 证据登记。

### T2 Spike-2：流式装载（M3 前置，可与 T1 并行）

| 项 | 内容 |
|---|---|
| 目标 | 证明「按页 mint → deliver → ack → evict」在长会话上保持 append-only 顺序与 exactly-once，且 P16 不回归 |
| 交付物 | ① 页化装载状态机（§14.6）；② 顺序断言（`deliverOrder == canonicalOrder`、跨页 token 连续）；③ 装载峰值内存测量（≤2 页窗口）；④ 与 `DeferHistoryDelivery`/一次性替换授权的对接测试 |
| 涉及文件 | `cmd/aicli/ui/history_effect_planner.go`（页化入口）、`cmd/aicli/commands/chat_history*.go`、`chat_ui_actor.go` |
| 依赖 | T0；与 T3 共用账本接口 |
| 验收 | L2 fixture E2E 全绿（72 行 exactly-once）；P16 ≤250ms；装载峰值 ≤2 页；顺序断言 0 违例（合成 5.5k cells 会话） |

> 状态：🟡 原型通过（2026-10-20）：合成 5,632 cells 的顺序 / exactly-once / token 连续 /
> 窗口上界 / 写穿回读全部达标（证据见 §7 Spike 登记）。生产接线项（L2 fixture 全绿、
> P16 ≤250ms 实测、真实会话装载峰值）在 M3（T6/T8 接线后）复核，原型阶段不作门禁。

### T3 双代账本与索引（M1 核心）

| 项 | 内容 |
|---|---|
| 目标 | 落地 §14.2 数据结构与 §14.7 全局属性（generation、PrefixFence、foldTarget、GroupKeyFP、gap/尾行身份） |
| 交付物 | ① `ColdLedger`/`WindowLedger`/`GenerationSpan`；② `PrefixFence` 增量折叠；③ `lastFoldedCellID()`；④ `GroupKeyFP`；⑤ `RowLedger` 分代定位（二分 + 前缀和）；⑥ 属性测试（单调/不重叠/随机对拍） |
| 涉及文件 | 新增 `cmd/aicli/ui/transcript_residency.go`；改 `ui/app_state.go`（`TranscriptState` 旁挂）、`ui/history_effect_planner.go`（输入改窗口）、`ui/scene/*`（gap/尾行口径核对） |
| 依赖 | T0 |
| 验收 | 单测+属性测试全绿；`layout(window) == filter(layout(all), window)` 等价性测试全绿；fence 与现状逐 cell 折叠结果一致（对拍 5.5k cells） |

> 状态：✅ 已完成（2026-10-20）：`ColdLedger`/`ColdRowLedger`（5,000 引用属性测试 + 二分定位对拍 +
> 篡改检测）、`WindowLedger`（前缀和/定位/Detach/foldTarget）、`GlobalFoldTarget`（窗口优先、冷区回退）、
> `GroupKeyFP`；**等价性对拍**（30 cell 合成语料：`layout(window) == filter(layout(all), window)`，
> 差异恰好等于账本记录的 `LeadingGapRows`）与 **fence 对拍**（5,000 cells：账本前缀链 ==
> 生产 `transcriptFinalizedPrefixFence`）全绿。为可对拍，生产 fence 的逐 cell 折叠被提取为共享原语
> `transcriptFenceFoldCell`（行为逐字保持）并统一链初始状态常量 `transcriptFenceOffsetBasis`
> （对拍曾捕获两侧初始状态不一致的真实缺陷）。

### T4 窗口滑动与逐出守卫（M2/M3）

| 项 | 内容 |
|---|---|
| 目标 | 落地 §14.3 状态机 + §14.4 滑动算法（含滞回、原子写穿、pin 集合） |
| 交付物 | ① `ResidencyPolicy` + 状态机（Hot/Cold/Loading/Unrebuildable）；② `slidePrefix`（整 cell 前缀、单次 ≤maxCells/4、先全部 Put 再提交）；③ pin 集合实现（active/frontier/未 Ack/DeferHistoryDelivery/pager/viewport/Loading）；④ 决策矩阵单测（穷举守卫组合） |
| 涉及文件 | `ui/transcript_residency.go`、`ui/controller_state.go`（内部 action：Slide/Evict/PageLoaded）、`ui/history_effect_planner.go` |
| 依赖 | T1、T3 |
| 验收 | 决策矩阵全绿；`-race` 绿；关闭 evictPrefix 时驻留集合恒等于窗口（无逐出）；开启后 `put_errors>0` 的场景必须保持 Hot（失败不逐出） |

> 状态：✅ 已完成（2026-10-20）：`EvaluateEvictionGuards`（32 组合穷举 + 单点失败原因码）、
> `ResidencyPolicy.PlanSlide`（滞回 128 / 高水位 1.25× / 步长上限 maxCells÷4）、`DesiredStart`
> （mustKeep ∕ pager ∕ viewport-margin 取最小）、`TranscriptResidency.SlidePrefix`（**两阶段原子**：
> 先全部写穿成功、再提交账本/窗口/状态；守卫截断前缀；状态轴由驻留状态机权威决定）、四态状态机
> （Hot/Cold/Loading/Unrebuildable）；`WindowLedger` 增加 `LeadingGaps` 并在 DetachPrefix 中同步迁移。
> 实测：写穿失败 → `Aborted` 且窗口/冷区/状态全不变（Put 错误计数 +1）；`evictPrefix=false` 时窗口恒等于驻留集合；
> `-race`（ui 整包 31.2s）与 commands 整包（182.1s）全绿。

### T5 冷区回填与虚拟 pager（M2/M3）

| 项 | 内容 |
|---|---|
| 目标 | 落地 §14.5 回填/预取与 §14.9 虚拟化 pager |
| 交付物 | ① `ensureResident` + 预取（lookbehind 2 / forward 1）+ in-flight 去重/取消；② 占位高度与校正；③ pager 虚拟列表（高度来自账本、锚点保持、`ExpandAll` 估算+校正）；④ 性能采样接入 |
| 涉及文件 | `ui/transcript_pager.go`、`commands/chat_transcript_pager.go`、`ui/transcript_lazy_source.go` |
| 依赖 | T1、T4 |
| 验收 | 5.5k/20k cells：首屏 ≤1 帧、滚动 p95 ≤16ms、锚点恢复 100%；冷页回填 p95 ≤50ms；未加载页占位高度不抖动（滚动条位置稳定） |

> 状态：✅ 已完成（2026-10-20）：`TranscriptPageCache`（FIFO 有界）、`TranscriptBackfiller`
> （`EnsureResident` 去重 + `BackfillOnce` + `PrefetchVisible`（lookbehind 2 / forward 1，经
> `SeqRangesForPrefetch` 规划）+ 失败 3 次后 `Unrebuildable` 永久 pin + `Reset` 取消）、
> `TranscriptVirtualPager`（高度来自账本 O(1)、冷区二分/窗口定位、锚点 `(CellID,Row,Gen)`
> 往返、`ExpandAll` 估算→校正旁路表）；`TranscriptResidency` 增加 `SeqOf/CellIDAtSeq/ColdRefOf/
> GlobalRowStart/SeqAtGlobalRow` 与自失效索引缓存。
> Spike-3 实测（5,632 cells 全冷区、sqlite 写穿）：10 页 × 128 cells 回填 **p95 9.02ms**（门禁 50ms）、
> 1 万次定位探针 **p95 <1µs**（门禁 100µs）、锚点往返 **100%**、缓存 1,280 cells / 513 KB；
> 证据 `output/residency-baseline/spike3/report.json`；ui 整包（14.2s）+ `-race`（31.7s）+ commands 整包（159.7s）全绿。

### T6 scene 层窗口化（M1 关键路径）

| 项 | 内容 |
|---|---|
| 目标 | 消除 `renderScene` 全量累积（B3），并保证 reconcile / alias / compact 视图替换等价 |
| 交付物 | ① scene 裁剪入口（与 L1 同源滑动）或索引上移；② reconcile 在窗口化 scene 上的等价性测试；③ compact 视图替换、`/load`、resume picker 回归 |
| 涉及文件 | `commands/chat_runtime_events.go`（`applyChangeSet` 持有链）、`commands/chat_history_reconcile.go`、`ui/scene/*` |
| 依赖 | T3 |
| 验收 | 窗口化 scene 下：72/40 marker exactly-once 全绿（L2/L1）；reconcile 去重无重复 cell；scene 内存不随会话增长（分区口径） |

> 状态：✅ **已完成（2026-10-20）**，实机门禁已在 T8b-6 通过（真实 ConPTY 冷前缀 + L2 fixture）：
> `scene.TrimPrefix/RestorePrefix`（可逆：顺序/byID/byChain 往返一致、mutable 前缀拒绝、幂等恢复、
> `nextID/nextSeq` 单调不回退、mapper 追加连续性）；bridge 侧 `bridgeSceneTrimPolicy`
> （默认 nil = 空操作；开启且 **写穿成功才裁剪**，失败 fail-closed 恢复场景；`TrimBatch` 为单次上限）；
> 单测：scene 3 个（往返/拒绝/连续性）+ commands 3 个（默认关闭不变、写穿后场景有界 100→20 且
> 80 个被裁 cell 全部可读回校验、写穿失败场景不变）。
> 回归：`ui/...` 全子树、`-race`（ui+scene）、commands 整包全绿。
> 复核结论（T8b-6）：真实 ConPTY 下 72 marker exactly-once + 48/48 冷区读回 + resident=24；
> 真实 Windows Terminal fixture 8/8 PASS（默认关闭路径无回归）。reconcile 在窗口化 scene 下的
> 等价性由 T9 的 R1/R4 fixture 模式（`AICLI-RENDER-FIXTURE-COLD-PREFIX=1`）继续加固。

### T7 帧合成 / SSE / 调试端点窗口化（M1）

| 项 | 内容 |
|---|---|
| 目标 | 落地 §14.10：`ComposeAppRenderFrame` 只物化视口邻域；SSE 尾部窗口；debug 默认窗口 + `full=1` |
| 交付物 | ① 帧合成窗口化（含 foldTarget 取自索引）；② `sceneBlockGroups` 尾部窗口；③ `/debug/chat/status|screen` 默认窗口；④ 每帧分配量与耗时采样 |
| 涉及文件 | `ui/app_render_frame.go`、`commands/chat_runtime_events.go`、`commands/chat_debug_*.go` |
| 依赖 | T3 |
| 验收 | 单帧分配量较基线下降 ≥60%（pprof alloc_space 对照）；`/web/api/screen?tail=N` 输出与现状一致（同 tail 下逐字节比对）；L2/L1 全绿 |

> 状态：✅ **已完成（2026-10-20，端到端 ≥60% 门禁已达成，见 T8b-3）**：
> ① 帧合成 transcript 索引改为**帧内按需**（旧实现每帧复制全量 transcript map：
> 5,000 cells × 20 帧 63.1MB → 40.9MB，端到端 **-35.1%**；隔离度量 map 分量
> 55.4MB → 187KB，**-99.7%**），帧输出与全量 map 参考实现**逐字节等价**；
> 当时未达 60% 的原因（每帧大头是全量布局）已由 **T8b-3 窗口化布局输入**消除：
> 对照（5,000 cells × 20 帧，old = 全量布局 + 全量 map）63,069,976B → **381,176B
> （-99.4%）**，详见 T8 状态块第 3 项；
> ② `tailWindowSnapshot`（调试/SSE 尾巴窗口原语）已就绪：顺序/规模正确、未超限零拷贝，
> 与全量 `sceneBlockGroups` 分组逐组等价（首组 leading gap 差异按 §14.7 显式豁免），
> 默认路径不变；`/debug/*?window=N`、SSE 尾部窗口与 residency-enabled 默认窗口在 T8 接线。

### T8 配置与观测（贯穿，M0 起）

| 项 | 内容 |
|---|---|
| 目标 | 落地 §15.1/§15.2/§15.3：开关、状态字段、分区内存口径 |
| 交付物 | ① 配置项（含 hot-reload 策略：仅启动期生效的标注）；② `app_state.residency` 全字段；③ 分区内存指标（scene/appstate/ledger/seed/session/cache）；④ 运行手册增补 |
| 涉及文件 | `internal/config/manager.go`、`commands/chat_debug_display_http.go`、`docs/`（手册） |
| 依赖 | —（骨架随 T0） |
| 验收 | 所有字段在真实实例可读；分区口径与 pprof 对照误差 <10%；开关关闭时无行为变化 |

> 状态：🟡 进行中（2026-10-20）——**T8a 已交付**：
> `ui.LayoutCacheBudgets` + `ApplyLayoutCacheBudgets/Reset/Snapshot`（锁内 `setBudget` 就地调整并立即逐出，
> 无指针替换/无数据竞争）；三类缓存新增 `setBudgetBytes`；配置 `ui.layoutCache.*MiB` 接线（仅 >0 时覆盖，
> 0 = 保持现状）；`app_state.residency.layout_cache_budgets` 暴露生效覆盖。
> 单测：默认 64/64/64 MiB 不逐出 → 下调 1MiB 立即收敛并回收字节 → 负数=不覆盖 → Reset 恢复默认；
> `ui` 整包（14.0s）全绿。
>
> **T8b 待办（明确清单）**：
> 1. ✅ **已完成（2026-10-20）** residency 运行时接线：`transcriptResidencyRuntime`（进程级，含 policy/store/
>    路径与 store 错误）、store 打开（`<sessions>/session_cells.sqlite`）、bridge `SceneTrimPolicy` 自动安装
>    （`ensureSceneTrimPolicy`：启用且 store 可用且 sessionID 可解析时一次安装，幂等；`bodyStore=off`/目录
>    不可用时 fail-closed 不安装）；配置映射签名更新为 `applyTranscriptResidencyConfig(cfg, sessionDir)`；
> 2. ✅ **已完成（2026-10-20）** residency 启用且预算未显式配置时套用 §15.1 默认（缓存 16/8/8 MiB；
>    页/滞回/水位默认由 ui 夹紧层提供）；显式配置优先；
>    （单测：默认零副作用 / 启用开 store + 默认预算 / `bodyStore=off` fail-closed / 显式预算覆盖 /
>    bridge 幂等安装；commands 整包 173.9s 绿）
> 3. ✅ **已完成（2026-10-20）** 窗口化布局输入：`LayoutAppScreen` 不再物化全量语义行 + 全量 cells map，
>    改为尾部 cell 窗口按需布局——`scene.TailLayoutWindowStart`（与 LayoutTranscript 同一计数口径）+
>    前驱上下文 cell 复现窗口首个 cell 的 leading gap + 全局 `foldTarget` 覆盖
>    （`transcriptFoldTarget{ID,Known}` / `transcriptGlobalFoldTarget`；§14.7 禁止窗口内局部重算）；
>    `LayoutAppState` 拆出 `layoutAppStateParts`/`appLayoutFromParts` 供窗口与全量两条路径共用；
>    新文件 `ui/app_screen_layout_window.go`、`ui/scene/layout_window.go`。
>    等价性：`TestLayoutAppScreenWindowedMatchesFullLayout`（5 fixture × 3 宽度 × 5 高度）+
>    `TestLayoutAppScreenWindowedTrimBoundariesSweep`（高度 1..64 逐一，覆盖裁剪边界）+ 5000 cells
>    对拍全量参考实现**逐行一致**；scene 侧 `TestTailLayoutWindowStartMatchesLayoutRows` /
>    `TestLayoutWindowRowHelpersMatchLayoutTranscript` 以 LayoutTranscript 实际行数为地面真值钉住计数口径
>    （并修掉一处窗口前缀 off-by-one：上下文 cell 在窗口输入里没有前驱，其 leading gap row 不属于前缀）。
>    分配量：帧合成 5,000 cells × 20 帧 **63,069,976B → 381,176B（-99.4%）**（门禁 ≥60%）；
>    `LayoutAppScreen` 单帧 200 cells=235,496B vs 5000 cells=241,992B（比值 1.028，与历史长度无关）。
>    回归：`ui/...` 18 包、`-race`（ui+scene）、commands 整包全绿（T7 端到端门禁就此达成）；
> 4. ✅ **已完成（2026-10-20）** 调试/SSE 窗口默认值接入（§14.10）：
>    `?window=N`（cells，上限 20000）/ `?full=1|all|true|yes|on`（或 `window=0`）= 显式全量；
>    缺省**仅**在 residency 开启时套用 `chatDebugDefaultWindowCells=2000`，关闭时保持历史全量——
>    默认路径不新增任何响应字段（`window` 标注只在确实裁剪时出现）；
>    `/debug/chat/status`（含 `/web/api/status` 与 `?format=text`）的布局摘要
>    （`LayoutTranscript`/`RenderText`）改为对 `tailWindowSnapshot` 窗口计算并标注窗口；
>    `/debug/chat/screen`（含 `/web/api/screen?view=tui`）的 fallback 通道（uiActor 语义 cells、
>    bridge Scene 快照）按窗口物化（geometry 通道本就是 T8b-3 的窗口化帧）；
>    新测试 `commands/chat_debug_window_param_test.go`（5 用例：参数矩阵 12 组 × residency 开关、
>    缺省随开关、screen 窗口 = 全量尾部逐行一致（首个窗口 cell leading gap 按 §14.7 归零）、
>    请求入口 `?window`/`?full`/JSON+文本注解、status 布局行数与全量尾部一致）；
>    回归：debug 专项、`ui/...` 18 包、`-race`（ui+scene）、commands 整包 195.6s 全绿。
> 5. ✅ **已完成（2026-10-20）** 状态字段扩展为真实计数 + §15.3 分区内存口径（§15.2）：
>    `ui.TranscriptResidencyCounters` 增加 `rematerialize_bytes/max_ms/inflight`；新增导出记录入口
>    （`NoteTranscriptResidencyEviction/WriteThrough/Rematerialize(+Start/Done)`），bridge 写穿裁剪路径
>    真实记账（`TrimBatches/TrimmedBytes/TrimmedRows/RestoredCells/LastReason/GenerationMin..Max`，
>    逐出时固化本批真实行数）并回写 ui 计数器；`ui.TranscriptResidencyStats` 新增 §15.2 分区
>    （`resident/cold/index/evictions/rematerialize/body_store` + `sources/unavailable`）与
>    `TranscriptResidencyMemoryStats`（§15.3 六分区，-1 = 不可归因）；端点侧 `chatResidencyStatsForSession`
>    现场补充 bridge/store/session 口径与字节估算（`EstimateTranscriptCellBytes` 等）；
>    `index`（L2 账本）未接线 → 显式登记 unavailable（不输出零值伪装）。`/debug/chat/status` 真机采样：
>    4,096 cells → keep 512、逐出 3,584 cells / 10,738 rows / 1,272,583 B、store 自报 puts=3,584、
>    resident 512 cells / 1,535 rows / 70,656 B、14 批量裁剪 1,211 ms。
>    测试：commands 4 个（bridge+store 真实计数、fail-closed 恢复登记、默认关闭的形状、§15.2/§15.3
>    JSON 字段名 + index 不得零值输出）+ ui 3 个（分区派生、inflight 钳制、字节估算单调）；
>    证据 `output/residency-baseline/t8b5/status-sample.json`（`AICLI_T8B5_OUT` 门控）；
>    回归：ui 18 包、`-race`（ui+scene）、commands 整包全绿。
> 6. ✅ **已完成（2026-10-20）** T6/T7 的实机门禁（**真实 ConPTY + 真实 Windows Terminal**）：
>    ① **L3-ConPTY residency 冷前缀门禁**（新增
>    `TestChatTTY_LiveLoop_ConPTY_ResidencyColdPrefixExactlyOnce`）：测试二进制经真实
>    `CreatePseudoConsole` 运行真实 chat 主循环（`IsInteractiveTerminal()=true` 有 isatty 证据），
>    72 个历史 cell 由**真实 runtime 事件**经 bridge 装载、经生产 unified presenter 写入伪控制台；
>    随后按**灰度顺序**（先装载交付、再启用 residency）经 `applyTranscriptResidencyConfig` →
>    `ensureSceneTrimPolicy` 自动装策略（sqlite store），逐轮写穿裁剪到 keep=24。
>    断言全绿：72/72 marker 在最终终端投影（scrollback+可见屏，vt.Screen 重建）**各恰好一次且严格递增**、
>    `CSI 3J = 0`、无相邻重复行、**48/48 冷区 cell 读回并指纹校验通过**、resident=24、
>    逐出后仍能正常完成一轮用户对话；`-count=2` 连续两轮稳定（18.2s）。
>    证据 `output/residency-baseline/t8b6/conpty-residency.json`。
>    ② **L2 fixture 真机**（`scripts/test-aicli-windows-terminal-e2e.ps1`，真实 Windows Terminal +
>    UIA TextPattern）：**8/8 PASS，exit 0**（73 行 history exactly-once、最旧行进入 scrollback、
>    装载/追加无 3J、prompt/status 各一次、markdown 一次渲染无源语法、流式采样与终态各一次）。
>    ③ 发现的灰度口径（记入 T10）：residency 若在装载前启用，bridge 每轮 apply 都会裁剪（TrimBatch=keep/2
>    需多轮收敛），装载结束时即只剩窗口；安全灰度顺序 = **先装载交付、后启用**（写穿先于逐出仍保证不丢数据）。
>    回归：`go build`、`ui/...` 18 包、`-race`（ui+scene）、commands 整包 200.2s 全绿。

### T9 测试与验证总集（贯穿；详见 §2）

| 项 | 内容 |
|---|---|
| 目标 | 建立「单测 → 集成 → L3/ConPTY → L2 fixture → L1 真实 provider」的完整门禁链并跑通 |
| 交付物 | ① 单测/属性/race 清单；② 新增 Residency E2E 用例 R1–R8（§2.2）；③ fixture 扩展（冷前缀模式，L2 可脱 API 验证逐出/回填）；④ 内存 harness；⑤ 每轮运行的 manifest 归档 |
| 依赖 | T1–T7 接口稳定 |
| 验收 | §2.3 的硬门禁逐级通过 |

> 状态：🟢 **完成（2026-10-21）**——脱 API 门禁 + **L1 真实 provider（magpie / qoder/dfmodel, effort=max）
> 连续 2 轮全绿**；过程中抓到并修复 1 个 critical 缺陷（未交付即逐出）。

> **L1 真机（补跑，2026-10-21）**：
> provider `magpie`（`http://127.0.0.1:3425`）+ model `qoder/dfmodel` + effort `max`，真实 Windows Terminal + UIA。
> - 第 1 轮：**PASS**（40/40 marker exactly-once、marker01 进 scrollback、严格递增、无原始协议标签、
>   异常空行 0、exit 0）→ `opencode-wt-0769647dec29…/manifest.json`
> - 第 2 轮：**PASS**（同断言集）→ `opencode-wt-5505a887d34a…/manifest.json`
>   （M3→M4 的"连续 2 轮 L1"满足）
> - 打开 residency（`-ResidencyKeepCells 1`，真实触发逐出）：**PASS** →
>   `opencode-wt-f19566b6ea4f…/manifest.json`
>
> **⚠️ 该轮抓到 critical 缺陷（已修复）**：keepLast=1 时 40 个答案 marker **全部从终端消失**
> （store 里只有 6 行）。根因：bridge 裁剪只判断 `total > keepLast`，从未校验 §14.3 的"已交付"
> 守卫，于是把**尚未交付到 scrollback** 的 cell 写穿并逐出了。
> 修复：新增 `deliveryFacts`（三态 Known/Delivered）+ 连续已交付前缀守卫
> （遇到首个未交付 cell 即停，`last_reason=unacked_rows`）；无 UI 账本的装配形态
> （无 uiActor / 直连 bridge 的测试）保持 fail-open 不干预，既有行为不变。
> 回归护栏：`TestSceneTrimRefusesUndeliveredPrefix`（新增）+ 既有 3 个裁剪用例全绿。
>
> 同轮还修掉一个**配置层陷阱**：`--config` 只喂 agent 配置层，顶层 `ui:` 属于 RuntimeConfig
> （由 aiclipaths 独立解析）——只写在 `--config` 里完全不生效（静默 no-op）。live E2E 改为
> 生成 `runtime.yaml` 并用顶层的 `skills_runtime.config_file` 指过去。
>
> **R5（partial/zero-write 与逐出互锁）已补（2026-10-21）**：新增
> `HistoryCommitLedger.DeliveryBlockers()` / `UIController.DeliveryBlockers()`，把"存在
> Queued（未交付）、`MayHavePartiallyWritten`、`UnresolvedDelivery`、`InvalidationPending`"
> 作为**整轮禁止逐出**的条件（原因码 `unacked_rows` / `projection_unknown`），并接入
> bridge 裁剪；同时把交付事实读取换成**零 payload 克隆**的 `DeliveredCellIDs()`
> （`Entries()` 会逐条复制 `Commit.Lines`，交付期账本可达 8 万条，每次裁剪克隆一遍会把
> UI 锁按秒级占用）。进程内门禁：`TestDeliveredCellIDsReflectsLedgerState`、
> `TestDeliveryBlockersStopsEviction`（queued / partial-write / invalidation-pending 三态）。
>
> **仍未做（T10 前置）**：R3 多页流式装载页序需更长会话的实机轮次；R8 30–60min 长跑；
> 24h 观测 + 回退演练。
>
> **T10-1b（方案 A，2026-10-21）：lazy history 接入生产 pager** ✅
> 接线审计发现 `TranscriptResidency`/`TranscriptBackfiller`/`TranscriptVirtualPager` **只在测试里构造**——
> 开启逐出后 Ctrl+T 只能看到驻留窗口（§14.9 从未落地），R1 无法通过。已补齐：
> ① `bridgeSceneTrimPolicy` 与裁剪策略同源安装 `ui.TranscriptResidency`；
> ② 写穿成功后把同一批 cell 记入 `ColdLedger`（逐 cell 行数 = 内容 + leading gap，gap 归后继 cell）；
> ③ `chatTranscriptPagerView` 打开时从 store **逐 cell 读回并指纹校验**，前置到窗口内容之前；
> ④ 读回不足时 `PagerTruncated=true` → pager 标题显式标注
> `(early history unavailable: store read failed)`，绝不静默少显示；
> ⑤ status 的 `index`（prefix_fence/fold_target/revision）与 `cold`（cells/bytes/delivered_rows/generations）
> 改为真实账本值。
> 门禁：`TestPagerRestoresEvictedCellsFromStore`（40→keep 8，32 逐出全部读回且顺序正确）、
> `TestPagerColdRestoreDegradesExplicitly`、`TestPagerColdRestoreNoLedgerKeepsLegacy`、
> ui 两个渲染标注用例；**真实 ConPTY** 用例已扩展为同时断言"账本 48 + pager 读回 48 且正文顺序一致"
> （PASS 3.3s）；L1 真 provider（keep=1）与 L2 冷前缀 fixture 均 PASS；
> 回归：`ui/...` 18 包、commands 整包 225.0s 全绿。证据 `output/residency-baseline/t10b/report.json`。
>
> **T10-1c（R1 真机翻页证据，2026-10-21）** ✅
> 真实 ConPTY 用例已扩展到"逐出后用 pager 的**生产渲染函数**产出帧并断言"：
> 账本 48 == 逐出数 48、`restoreColdCellsHeader` 读回 48 个正文且顺序递增、
> 帧内含 pager 标题 `Transcript`、**最早冷区 marker 落在帧顶部（index<200）**；
> PASS 3.4s，`-count=2` 18.6s 稳定。
> 两条实测结论（已写入代码注释与报告）：
> ① 行模式（`AICLI_CHAT_INPUT_LINE_MODE=1`）下 Ctrl+T 不被逐键编辑器认领，无法用 0x14 注入触发——
>    因此本门禁证明的是**内容链路**（账本→store→pager model→帧），键位绑定由既有 ui 用例覆盖；
>    已为将来补 `RawBytes` 注入能力。
> ② pager 是副屏：把帧写进主 PTY 流会把 marker 数翻倍并破坏 exactly-once（实测 HISTORY-042 变 2 次），
>    因此帧改为落证据 JSON 独立断言。
> 证据 `output/residency-baseline/t10c/conpty-residency.json`（含实际渲染帧）。
>
> **T10-2（R3 多页装载页序）已完成** ✅（2026-10-21）：
> `TestResidencyMultiPageLoadKeepsOrderAndEvictsDeliveredPrefix`——250 条历史（每页 100 →
> 3 页）走**真实 SQLite 分页**（`loadNewestResumeHistoryPage` + `deferResumeHistoryCompletion`
> + `startDeferredResumeHistoryLoad`），断言：装满后 **250/250 严格升序、无重复**；
> 随后开启 residency（keep=16）做**真实写穿逐出**，断言冷区 234 == 历史-keep，
> 且**冷区读回顺序与装载顺序完全一致**（逐 cell 比对 marker）。
>
> **T10-3（回退演练）已完成** ✅（2026-10-21）：`TestResidencyRollbackReturnsToLegacyShape`
> 覆盖"改配置 → 重启"的进程内等价路径。**过程中修掉两个真实回退缺陷**：
> ① `enabled=false` 时 §15.1 的 16/8/8 缓存预算**没有被复位**（回退只停了逐出，仍保留收紧预算）；
>    已在 `applyTranscriptResidencyConfig` 的 else 分支补 `ui.ResetLayoutCacheBudgets()`；
> ② 运行时替换/回退时**只清指针不关 store**（sqlite 句柄泄漏，实测 TempDir 清理报 file in use）；
>    已在 `installTranscriptResidencyRuntime` 的两条路径上都先 Close 旧 store。
> 回退后断言：运行时为 nil、`status.enabled=false`、预算为 `{}`、新会话**不裁剪/无冷区账本**、
> pager 不注入冷区、status 不输出 index 分区。
>
> **T10-2（R8 30 分钟长跑）已完成** ✅（2026-10-21，真实运行 30m）：
> harness `TestResidencySoak`（env 门控）+ 真实 bridge/sqlite/逐出守卫，每 500ms 追加 20 cells。
> 结果：**累计 72,000 cells、采样 180 次**；
> - **驻留恒定 512**（总量 400 → 72,000，180× 增长，驻留不涨）✅
> - 冷区 0 → **71,488**、`trimmed_bytes` 0 → **29.3 MB**（逐出真实发生）✅
> - **Go heap 30 MB**（门禁 ≤120 MB；设计口径是 20k cells，本轮跑到 72k）✅
> - **进程 WS 峰值 64.2 MB**（门禁 ≤384 MB）✅
> - `rematerialize_failures = 0` ✅
> - 写穿吞吐 ≈1.5k puts/s 持续 30 分钟，`put_errors = 0`
> 证据：`output/residency-baseline/r8/soak-report.json`（180 个采样点）+ `output/residency-baseline/r8/report.json`。
>
> **T10-4（默认值切换）已完成** ✅（2026-10-21，按用户指示直接切换，24h 观测未做）：
> **分层默认**：`ui.residency.enabled = true`（窗口化布局/帧 + §15.1 缓存预算 16/8/8 MiB +
> 调试端点缺省窗口），**`evictPrefix` 仍为 false**（逐出是独立灰度开关，需显式开启）。
>
> 切换要点：
> ① `DefaultTranscriptResidencyOptions()` 的 `Enabled` 改为 `true`；新增
>    `DisabledTranscriptResidencyOptions()` 作为**显式回退档**（测试清理与回退路径都用它，
>    避免"默认开启后清理反而把开关打开"）；
> ② `config.ResidencyConfig.Enabled` / `EvictPrefix` 改为 `*bool`：T10-4 后若仍是 bool，
>    就无法区分"未配置"（走默认 true）与"显式 false"（回退），**回退开关会变成静默 no-op**
>    （与 T10-3 修过的是同一类缺陷）；
> ③ **写穿 store 改为惰性打开**：`evictPrefix=false` 时完全不创建 `session_cells.sqlite`。
>    默认开启后实测：旧行为会让每个纯聊天会话都落一个库且**没有关闭路径**，
>    导致大量 actor 测试在 TempDir 清理时报 `file in use`（一次跑出 9 个失败）。
> ④ 状态端点区分"降级"与"正常未启用写穿"：后者记在 `sources`（`body_store.lazy_off`），
>    不再登记为 `unavailable`。
>
> 验证：`TestResidencyDefaultOnMatrix`（4 组：未配置=开+不逐出+不建库 / 显式 false=全关 /
> enabled+evict=建库逐出 / 只开 evictPrefix=默认开+逐出）、`TestResidencyDefaultEvictionStaysOff`
> （默认下 100 cells 不裁剪且无策略）、`TestResidencyRuntimeDisabledByDefault`（默认开 + 显式关）；
> `ui/...` 18 包、ConPTY `-count=2`（16.7s）、commands 整包 **238.4s** 全绿。
>
> **已通过**：
> ① **L0/L3**：`go test ./cmd/aicli/... -count=1` exit 0（全包）；
> ② **L3-ConPTY**：`-run TestChatTTY_LiveLoop -count=2` → ok（18.8s），含 residency 冷前缀用例；
> ③ **L2 fixture 默认路径**：Windows Terminal **8/8 PASS**；
> ④ **L2 fixture 冷前缀（R1/R4）**：新增 `-ColdPrefix` 模式
> （fixture `AICLI_RENDER_FIXTURE_COLD_PREFIX=1`：对**当前已交付内容**写穿 → 裁剪到 keep=24 → 逐 cell 读回校验；
> 后续阶段经 `fixtureTrimStart` 保持窗口形态）→ **8/8 PASS**：逐出 51 cells 后 73 个 history marker
> 在 UIA 文档里**仍各恰好一次**、`readback == persisted`、`resident=24`、`CSI 3J=0`、
> prompt/status 各一次、markdown/streaming 各一次；
> ⑤ **R6（foldTarget 在冷区）**：`TestWindowedLayoutFoldTargetColdRegion`——目标在窗口外时窗口内 0 处
> Ctrl+T 提示；全量布局（高视口）恰 1 处且落在目标 cell；目标移入窗口后恰 1 处；
> ⑥ **R7（web/SSE 窗口化）**：`TestHandleChatWebAPI|TestChatWeb|TestSceneBlockGroups|TestTailWindowSnapshot|TestChatDebug`
> → ok（27.8s）；
> ⑦ **R8 进程内分量**：`TestWindowedFrameAllocationsFlatUnderLongHistory`——500 cells=191,896B vs
> 20,000 cells=191,944B（**ratio 1.000**，40× 历史增长下单帧分配不变）。
>
> **未执行（阻塞/需长窗口）**：
> - **R2/R3/R5 + L1 真实 provider**：本机凭据全部不可用（deepseek 返回 **402 Payment Required**；
>   commandcode 系端点 **401**；本地代理 `127.0.0.1:18902` 拒绝连接）。资产已就绪
>   （`scripts/test-aicli-opencode-windows-terminal-e2e.ps1` 40 markers + reasoning sentinel），
>   必须在 T10 打开默认值**之前**用有效凭据补跑。
> - **R8 长跑（30–60min）**：需真实桌面时段；harness 为 `scripts/measure-aicli-residency.ps1`
>   （WS/private/CPU + `app_state.residency` 采样），本轮以 T8b-5 的分区实测 + 上述分配量门禁替代。
>
> 证据：`output/residency-baseline/t9/report.json`；`output/aicli-terminal-e2e/`（UIA dump）。
> 过程中修复的两处 fixture 缺陷（均已记入报告）：冷前缀阶段必须放在**最后**（否则后续阶段重新发布
> 全量快照把前缀"接回来"）；raw 证据打印会被随后的内联视口重绘擦掉，须在打印后再重绘一次底部区。

### T10 灰度与默认开启（M3/M4）

| 项 | 内容 |
|---|---|
| 目标 | 灰度放量与默认值切换；回退演练 |
| 交付物 | ① 灰度策略（`evictPrefix` 分实例/分会话开启）；② 回退开关演练记录（含一次真实回退执行）；③ 默认值切换 PR（`ui.residency.enabled=true`、缓存预算按 §15.1、软上限 384 MiB）；④ 观测看板字段说明 |
| 依赖 | T9 |
| 验收 | 连续 2 轮 L1 全绿 + 24h 观测无回归；回退演练后内存回到旧形态（可复现） |

### T10-5 生产内存归因（2026-10-21，400MB 会话复盘）

**现象**：用户报告另一会话（`session_20261018183040_rPueGv9v`，pid 24792）占用 445–516 MB。

**机制生效证据**（`GET /debug/chat/status`）：`enabled=true`、预算 16/8/8 MiB 已套用、
`scene_cells_bytes=1.0 MB`、`render_cache_bytes=5.0 MB`、驻留 895 cells / 10,886 rows。
**§15.3 可归因合计仅 67 MB，未归因约 378 MB（85%）**。

**真实归因**（`go tool pprof -top -inuse_space`，`?gc=1` 强制 GC 后仍 inuse）：

| 项 | 大小 | 说明 |
|---|---|---|
| `encoding/json/v2.makeString` | 130.99 MB | JSON 解码出的字符串 |
| `reflect.mapassign_faststr0` | 42.54 MB | `map[string]interface{}` 插入 |
| `loadCanonicalMessagesTx`（cum） | **157.23 MB** | 占堆 62%，调用方 `updateSessionTx` |
| `loadPromptMessages`（cum） | 20.88 MB | 正常 Load 路径 |
| **Go 堆合计** | **250.78 MB** | 进程 WS 516 MB 的其余部分是 runtime/栈/文件映射 |

**根因（与最初推断不同，以实测为准）**：
1. **不是** §5.4 #6「装载路径一次性装入全量」——`Load()` 本应只读 718 行的 prompt 投影。
2. 而是 **`updateSessionTx` 的 canonical 全量回退分支**：当调用方递来的历史是
   “未到达最新行”的过期窗口时，它用**整段 canonical** 重建投影，并把解码结果经
   `applyStoredPromptProjection` 挂到 `session.History` 上，于是整条转录被钉在内存里。
3. 实测复现（本机只读探针）：该会话投影 **724 行 → 18,528 行**，且 **compaction
   摘要行被冲掉**（canonical 侧无 compaction 行，投影侧有 → 压缩摘要只存在于投影）。
4. **磁盘数据特征**（`session_history.sqlite`，只读统计）：canonical 18,683 条 /
   payload 86.6 MB / **metadata JSON 43.9 MB**（`tool_metadata` 16.6 MB、
   `reasoning_details` 12.8 MB）/ content 仅 23.8 MB。`map[string]interface{}` 的
   Go 表示约为 JSON 字节数的 3~4 倍 → 43.9 MB × 3.5 ≈ 154 MB，与 pprof 的 157 MB 吻合。

**关键结论**：
- **开启 `evictPrefix` 不能改善此问题**：逐出只作用于 scene（当时仅 1 MB），
  157 MB 在 `session.History` 里。
- **这是可回收膨胀，不是泄漏**：该会话随后发生一次压缩，投影从 18,528 行缩到 48 行，
  堆从 **251 MB → 56.8 MB**，进程 WS **516 MB → 244 MB**。证明内存随投影规模走。

**已交付修复（本清单代码改动）**：

1. `cmd/aicli/commands/chat_residency_status.go`：`estimateRuntimeMessagesBytes` 补齐
   `Metadata` 归因（含嵌套 map/slice 递归与 `map[string]interface{}` 3× 表示倍数）。
   修前该会话被估成 60.4 MB（真实 157 MB），归因口径漏掉 metadata 就无法解释 85% 的堆。

2. **`internal/chat/sqlite_storage.go`：投影重建不再整段覆盖压缩边界**（核心修复）

   缺陷：`updateSessionTx` 的 default 分支在「调用方历史未到达最新行」时用**整段
   canonical** 重建 `session_prompt_messages`。canonical 里没有压缩摘要行（摘要只
   存在于投影），因此任何整段重建都会把摘要删掉，同时把投影从 724 行撑到 18,528 行，
   再经 `applyStoredPromptProjection` 挂回 `session.History`，形成 157 MB 常驻堆。

   另一条更隐蔽的入口：`proven=true`（identity 快速路证明「到达最新行」）时直接采用
   调用方历史。但展示用全量历史（`ResumeHistory`，18k 条）同样"到达最新行"，却**不是
   模型视图**、没有压缩边界 —— 这条路径同样会冲掉摘要。

   修复：新增 `rebuildProjectionFromStoredBaseTx`，以**表里已存的投影**为基线（而非
   `promptRows`，后者首条不匹配就整体返回空），只在基线末尾追加 canonical 中更新的
   消息；无法安全对齐时保留原投影不动并计入 `canonicalBoundedFallbacks`。仅当投影
   **携带 `context_stage=compaction` 边界**时才接管，避免改变普通增长型投影的既有行为。

   新增观测：`CanonicalRebuildStats.BoundedRebuilds` / `BoundedFallbacks`。

   修复前后（`TestCompactionSummarySurvivesFullWindowSync`，200 条 canonical）：

   | 指标 | 修复前 | 修复后 |
   |---|---|---|
   | 投影行数 | 21 → **200** | 21 → **21** |
   | 压缩摘要 | ❌ 丢失 | ✅ 保留 |
   | 回写 `session.History` | 200 条 | **21 条** |
   | canonical | 200（未改） | 200（未改） |

   生产会话实测（`session_20261018183040_rPueGv9v`，canonical 18,924 条）：
   `Load` 返回 **224 行**（含 1 条 compaction 摘要），修复前为 **18,677 条**。

3. 新增测试：
   - `cmd/aicli/commands/chat_residency_memory_estimate_test.go`（2 用例：metadata 必须
     显著大于纯文本；体积 100× 增长时估算须近线性）
   - `internal/chat/sqlite_storage_compaction_survive_test.go`（3 用例：全量窗口同步、
     过期窗口同步、压缩后继续追加新轮次）

4. **展示历史裁剪到固定上限（T10-7，方案 2）**

   背景：屏幕上真正绘制的只有 `FixedBottomSurface.historyWindow`，硬上限
   `historyWindowMaxLines = 400`（+ `historyWindowHeadroom = 40`）。而
   `ChatSession.ResumeHistory` 却整份驻留 canonical 转录——**两者数量级完全脱钩**。
   该会话 18,677 条展示历史 ≈ **108.7 MB**，与屏幕 400 行无关。

   交付：
   - `resumeHistoryRetainedAfterDelivery = 512`（与 §15.1 `MaxResidentCells` 同量级）；
   - `trimResumeHistoryAfterDelivery`：装载交付完成后裁剪，**三条安全前提**——
     ① 无待补齐游标且交付挂起已释放（否则会丢掉未重放的页）；② 只裁剪展示快照、
     绝不碰 `session.Messages`；③ 记 `resumeHistoryTrimCount/TrimmedOff`（降级不得静默）；
     裁剪起点对齐到完整轮次（不落在 tool 结果中间）；
   - **方案 2 的核心**：`refreshResumeHistoryForReplay` 在 `/history` 等「需要完整历史」
     的重放入口从存储**重新分页读回**（复用 `GetHistoryPage` 链路），因此裁剪
     **不改变任何用户可见内容**；读取失败时在标题上显式标注
     「仅显示最近保留的历史（完整历史读取失败）」，绝不伪装成完整历史。
   - §15.3 新增分区 `resume_history_bytes` / `resume_history_messages` /
     `resume_history_trimmed`——该数据面此前从未被计量。

   真实会话实测（`session_20261018183040_rPueGv9v`，19,000 条可装载消息）：

   | 指标 | 实测 |
   |---|---|
   | 分页装载规模 | 191 页 / 19,000 条 |
   | 平均每条 | 5.9 KB |
   | 全量驻留估算 | **108.7 MB** |
   | 裁剪后（512 条） | **2.9 MB** |
   | 降幅 | **约 106 MB（97%）** |

   新增测试 `chat_residency_trim_test.go`（6 用例）：固定上限裁剪、补齐在飞时跳过、
   小窗不误伤、裁剪边界对齐轮次、`/history` 完整恢复、无分页后端时 fail-closed。

   **L3-ConPTY 实机验收（已完成）**：
   - `TestChatTTY_LiveLoop_ConPTY_ResumeHistoryTrimAfterDelivery` — PASS 3.59s
     断言链：`interactive_terminal=true` → `loaded_after_first=100`（首帧只装一页）→
     `loaded_full=900`（9 页补齐）→ `delivered_rows=900`（真实交付发生）→
     `after_trim=512`（交付后裁剪）→ `trim_count=1/trimmed_off=388`（留痕）→
     `restore_ok=true/after_restore=900`（`/history` 完整恢复）。
   - `TestChatTTY_LiveLoop_ConPTY_HistoryReplayUsesRestoredTranscript` — PASS 6.04s
     真机渲染流中出现 `T10-7-MSG-0699`，证明裁剪后 `/history` 确实走了
     `refreshResumeHistoryForReplay` 分支并重放完整历史。
   - 稳定性：两项 `-count=2` → `ok 19.523s`。
   - 回归：既有 3 个 ConPTY 用例全部 PASS（RendersRealScreen / UnknownCommand /
     ResidencyColdPrefixExactlyOnce）。
   - 证据：`output/residency-baseline/t10-7/conpty-trim.json` + `report.json`。

   为何必须上真机：裁剪的触发条件是「装载交付已完成」，而交付只有在**真实终端
   写端**存在时才会发生；进程内测试的 surface 不会真的把行交给终端，因此只有
   ConPTY 能证明裁剪点确实落在交付之后。

**明确未做（如实登记）**：
- **24h 真实实例观测仍未做**（用户明确跳过，直接切默认值）。
- `evictPrefix` 默认仍为 `false`；逐出档的长期真实使用观察未积累。
- `TranscriptBackfiller` 的预取/页缓存仍未接线（同步按需读回）。

## 2. 测试与验证专项（重点：ConPTY / 实机 E2E 必须通过）

### 2.1 分层与现有资产映射

| 层 | 现成资产 | 命令 | 是否实机 | 用途 |
|---|---|---|---|---|
| L0 单测/属性 | `go test`（新：`transcript_residency_test.go` 等） | `go test ./cmd/aicli/... -count=1` | 否 | 账本/守卫/指纹/等价性 |
| L1 集成 | `go test ./cmd/aicli/...`（production-chain） | 同上 + `-race` | 否 | reducer/planner/ledger 交互 |
| L2 fixture E2E | `scripts/test-aicli-windows-terminal-e2e.ps1`（72 行 history marker） | 见 §2.5 | **是**（真实 Windows Terminal + UIA，不耗 API） | scrollback 语义回归 |
| L3 进程内 | `backend/cmd/aicli/commands/chat_tty_live_loop_test.go`（os.Pipe + `ui/vt.Screen`） | `go test ./cmd/aicli/...` | 否 | 渲染逻辑正确性（CI 友好） |
| L3-ConPTY | `chat_tty_conpty_live_test.go`（真伪控制台，`pty.Supported()` 门禁） | `go test ./cmd/aicli/commands/ -run TestChatTTY_LiveLoop_ConPTY -count=2` | 半实机（无头可跑，真 ConPTY 语义） | 逐键/按行输入、真实终端属性 |
| L4 真实 provider | `scripts/test-aicli-opencode-windows-terminal-e2e.ps1`（40 markers + reasoning sentinel） | 见 §2.5 | **是**（真实 WT + UIA + 真实 API） | 最终验收门禁 |
| 旁路观测 | `scripts/aicli-e2e-harness.ps1`（timeline / 诊断包 / 稳态判据 / endpoint 覆盖 / UIA 取文） | dot-source 复用 | — | 失败诊断与证据采集 |
| ConPTY 专项 | `backend/scripts/*conpty-test.py`（startup picker / settled wake / live slot quota / profile anchor）、`scripts/run-conpty-goal-e2e.ps1`、`cmd/goal-conpty-driver`、`cmd/conpty-probe` | 见 §2.5 | **是** | 启动/唤醒/配额/锚点的实机回归 |
| 聚合门禁 | `scripts/test-aicli-e2e-all.ps1`（断言基线 + E2E-DEBUG-01/02） | 见 §2.5 | 部分实机 | 一键回归 |

### 2.2 Residency 专用 E2E 用例矩阵（必须实机执行并通过）

| ID | 场景 | 资产（复用 / 新增） | 关键断言 | 归属里程碑 |
|---|---|---|---|---|
| **R1** | **冷前缀已逐出后的回滚/翻页** | 新增 fixture 模式：L2 fixture 扩展 `AICLI-RENDER-FIXTURE-COLD-PREFIX=1`（渲染 N 行后强制触发逐出，再滚动回看） | `DocumentRange` 中全部 history marker 各一次、严格递增、无缺行；pager（Ctrl+T）滚动到冷区内容完整；无重复行 | M2 |
| **R2** | **逐出后 exactly-once 保持** | L1（40 markers）复用 | marker01 `document=true visible=false`、marker40 visible、顺序递增；manifest 中 `residency.evicted_total>0` 且 `duplicate_count==0` | M3 |
| **R3** | **流式装载页序** | L1/L2 复用 + 长会话 fixture（>2 页） | 装载期间 marker 顺序连续、无重复、无乱序；manifest `load_pages ≥2`、`page_order_ok=true` | M3 |
| **R4** | **逐出后 resize / reflow** | L2 fixture 增加 resize 步骤（窗口几何变化） | sticky top-align 保持；冷区行不重排；窗口重排后内容与 source 一致；异常空行 = 0 | M2 |
| **R5** | **partial/zero-write 与逐出互锁** | 既有注入路径（`MayHavePartiallyWritten`/零字节失败） | Unknown/partial 期间 `evictions=0`（pin 生效）；恢复后同 token 重试一次；无重复/缺行 | M3 |
| **R6** | **折叠提示全局唯一（foldTarget 在冷区）** | 新增 fixture 场景：多个折叠 cell，最近折叠位于冷区 | Ctrl+T 提示仅出现在最近折叠；逐出后仍正确；pager 内 `e` 展开行为正确 | M2 |
| **R7** | **web/SSE 窗口化正确性** | `scripts/test-aicli-e2e-all.ps1` 的 E2E-DEBUG 族 + `/web/api/screen?tail=N` | 同 tail 下与基线逐字节一致；冷区存在时不返回全量；SSE `mesh=1` 分组语义正确 | M1 |
| **R8** | **实机长跑内存基线** | 新增内存 harness（ConPTY 内跑 30–60min） | `resident_cells` 不随总 cell 数增长；live heap ≤120 MB（20k cells）；WS ≤384 MB；`rematerialize_failures=0` | M3 |

> 用例放置：R1/R4/R6 归入 L2 fixture 脚本（脱 API，快回归）；R2/R3/R5/R8 走 L1 或 ConPTY 长跑；
> R7 走 HTTP 控制面族。每个用例失败时必须落诊断包（`Save-AicliDiagnostics`）。

### 2.3 硬门禁（不得降级、不得放宽）

| 阶段转换 | 必须通过 |
|---|---|
| M0 → M1 | L3 全绿 + L3-ConPTY 全绿 + L2 fixture 全绿（现状基线）+ 基线内存报告 |
| M1 → M2 | 上述 + T7 单帧分配门禁（对照降 ≥60%）+ P16 ≤250ms |
| M2 → M3 | 上述 + **L1 真实 provider 全绿** + R1/R4/R6/R7 全绿 + Spike-1 一致率 100% |
| M3 → M4 | **连续 2 轮 L1 全绿** + R2/R3/R5/R8 全绿 + 24h 观测无回归 + 回退演练通过 |

规则：
1. 任一断言失败 → 不推进阶段；保留 `manifest.json` 与 UIA dump，回到对应任务修复；
2. L1/L2 必须在**交互式 Windows 桌面会话**执行（`docs/plan/aicli-terminal-e2e-methodology.md` §1），
   无头环境只能跑 L3/L3-ConPTY/L0；
3. 证据以 `manifest.json` 为准；报告引用路径与 `document_sha256/visible_sha256`；
4. 逐出相关回归不允许以「重跑一遍恰好过了」收尾：必须解释失败机理并补单测。

### 2.4 证据与产物要求

- 目录：`output/aicli-terminal-e2e/residency-wt-<runID>/`（L1/L2），包含 `manifest.json`、
  `uia-document-full.txt`、`uia-visible-ranges.txt`、`sessions/`、`chat-logs/`、`aicli.log`；
- `manifest.json` **必须包含**：断言结果（复用方法论 §5.2/§5.3 清单）、UIA 三个代表快照的 SHA256、
  marker exactly-once 统计、`residency` 片段（`resident/evicted/rematerialize/put_errors/body_store`）、
  `runner_exit_code`、`forced_cleanup=0`；
- 凭证与敏感串按现行 `[REDACTED]` 清洗；`credential.bridge` 用后清理；
- ConPTY 专项与 L3-ConPTY 的记录按 `docs/e2e/debug-guide-evidence.md` 的表格格式追加。

### 2.5 命令清单（可直接复制）

```powershell
# —— 快速层（无需实机桌面）——
cd backend
go test ./cmd/aicli/... -count=1
go test -race ./cmd/aicli/ui ./cmd/aicli/commands -count=1
go test ./cmd/aicli/commands/ -run TestChatTTY_LiveLoop_ConPTY -count=2

# —— 实机层（交互式桌面；L2 不耗 API，L1 耗配额）——
cd E:\projects\ai\ai-agent-runtime
pwsh -NoProfile -File scripts/test-aicli-windows-terminal-e2e.ps1 -TimeoutSeconds 45        # L2 fixture
pwsh -NoProfile -File scripts/test-aicli-opencode-windows-terminal-e2e.ps1 -TimeoutSeconds 300  # L1 live
pwsh -NoProfile -File scripts/test-aicli-e2e-all.ps1                                        # 聚合门禁

# —— ConPTY 专项 ——
python backend/scripts/conpty-startup-picker-test.py
python backend/scripts/settled-wake-conpty-test.py
python backend/scripts/live-slot-quota-conpty-test.py
python backend/scripts/profile-workspace-anchor-conpty-test.py
pwsh -NoProfile -File scripts/run-conpty-goal-e2e.ps1

# —— 内存基线（T0 交付脚本后固定名称）——
pwsh -NoProfile -File scripts/measure-aicli-residency.ps1 -Scenario 20k-cells -DurationMinutes 60
```

## 3. 里程碑 DoD（逐项可核验）

| 里程碑 | 验收人可核验项（缺一不可） |
|---|---|
| **M0** | ① `ui.residency.*` 开关骨架合入且默认 off；② `/debug/chat/status` 出现 `app_state.residency`（全字段可读）；③ 分区块内存指标与 pprof 对照误差 <10%；④ 基线报告（5.5k cells 现状：活堆 ≈210.6 MB / UI ≈99.1 MB / cell_rows 64 MiB 顶格）；⑤ L3 全绿 |
| **M1** | ① L2 fixture E2E 全绿（72 marker exactly-once）；② L3-ConPTY 全绿；③ 帧分配量对照降 ≥60%；④ P16 ≤250ms；⑤ `evictPrefix=false` 下驻留集合 = 窗口（无逐出、行为与基线逐字节一致）；⑥ R7 通过 |
| **M2** | ① Spike-1 报告（一致率 100%、p95 ≤50ms、页 ≤2 MiB）；② 虚拟 pager 性能门禁（首屏 ≤1 帧、滚动 p95 ≤16ms、锚点 100%）；③ R1/R4/R6 通过；④ `body_store.put_errors=0`（正常路径）；⑤ 回填失败路径 fail-closed 单测绿 |
| **M3** | ① **L1 真实 provider E2E 全绿**且 `residency.evicted_total>0`；② R2/R3/R5/R8 通过；③ 顺序断言与 token 连续性单测/集成全绿；④ 24h 观测无回归（内存/`plan_ms`/心跳） |
| **M4** | ① 默认值切换（§15.1）合入；② 连续 2 轮 L1 全绿；③ 回退演练记录（关闭开关后回到基线形态）；④ 运行手册/验收文档更新 |

## 4. 风险与回退矩阵（开关粒度）

| 风险 | 触发信号 | 回退动作（无需改代码） |
|---|---|---|
| 写穿不一致/失败 | `body_store.put_errors>0` 或 Spike-1 一致率 <100% | `ui.residency.bodyStore=off`（只窗口化，不逐出） |
| 流式装载顺序违例 | `page_order_ok=false` / marker 乱序 | 关闭流式装载，回退一次性全量装载（保留窗口化布局） |
| memo 失效风暴 | `plan_ms` 上升 >2×、memo 命中率下降 | `ui.residency.enabled=false`（回到全量驻留） |
| pager 抖动/锚点丢失 | 锚点恢复 <100% 或占位高度跳变 | `ui.residency.prefetchPages` 加倍；仍失败则关闭 pager 懒加载 |
| scene 窗口化回归 | reconcile 重复 cell / 装载错序 | 恢复 scene 全量累积（保留 AppState 窗口化） |
| resize 行号不一致 | `ColdLedger` 自校验失败 | `evictPrefix=false`（冷区不逐出，全部留在窗口） |
| 内存不达标 | Spike-4 未到 KPI | 保守档：只窗口化 + 缓存预算收紧（预期收益 ≥40% 活堆） |
| 实机 E2E 失败 | L1/L2/R* 任一红 | 按 §2.3 不推进阶段；保留 artifact 定位修复 |

## 5. 文件映射（新增 / 修改）

**新增**
- `backend/cmd/aicli/ui/transcript_residency.go`（`ColdLedger`/`WindowLedger`/`ResidencyPolicy`/状态机）
- `backend/cmd/aicli/ui/transcript_lazy_source.go`（`CellBodyStore` 适配 + 回填校验）
- `backend/internal/chat/session_cell_store.go`（写穿表与读写，或复用 artifact store）
- `backend/cmd/aicli/ui/transcript_residency_test.go`、`transcript_lazy_source_test.go`（属性/对拍）
- `scripts/measure-aicli-residency.ps1`（内存 harness，分区口径）
- L2 fixture 冷前缀模式：`backend/cmd/aicli-render-fixture`（新增开关与场景）

**修改**
- `backend/cmd/aicli/ui/`：`app_state.go`（`TranscriptState` 旁挂索引/窗口）、`history_effect_planner.go`（窗口输入 + memo 指纹扩展）、`controller_state.go`（内部 action：Slide/Evict/PageLoaded）、`app_render_frame.go`（窗口化物化）、`transcript_pager.go`（虚拟列表）、`history_plan_cache.go`/`transcript_layout_cache.go`/`tool_fold_omission_cache.go`（预算可配置 + 新默认）、`scene/*`（裁剪入口与 gap/尾行口径核对）
- `backend/cmd/aicli/commands/`：`chat_runtime_events.go`（`renderScene` 裁剪 + SSE 尾部窗口）、`chat_history_reconcile.go`（窗口化等价性）、`chat_transcript_pager.go`、`chat_debug_display_http.go`（状态/默认窗口）、`chat_ui_actor.go`、`chat_actor_executor.go`（装载流式化接线）
- `backend/internal/config/manager.go`（新增配置项）
- `backend/internal/chat/session_storage_factory.go`（页接口复用；如需按 cell 锚读取再评估）
- `scripts/test-aicli-windows-terminal-e2e.ps1`（冷前缀场景 + R1/R4/R6 断言）

## 6. 执行顺序与工时估算（参考，单人）

| 批次 | 任务 | 估算 | 并行性 |
|---|---|---|---|
| 第 1 批 | T0 →（T1 ∥ T2） | 1–2 d + 各 2–3 d | T1/T2 可并行（不同代码面） |
| 第 2 批 | T3 → T6 ∥ T7 | 3–4 d + 各 2–3 d | T6/T7 依赖 T3 接口 |
| 第 3 批 | T4 → T5 | 2–3 d + 2–3 d | T5 依赖 T4 的 pin 接口 |
| 第 4 批 | T9（门禁链）→ T10（灰度） | 3–5 d + 1–2 d | 每轮实机 E2E 需预留桌面时段 |
| 合计 | — | **约 3–4 周**（含 2 轮 L1、R1–R8 实机与 24h 观测） | — |

## 7. 验收证据登记表（每里程碑填写，归档到验收记录）

| 里程碑 | 日期 | L3 | L3-ConPTY | L2 fixture | L1 live | R 用例 | 内存 KPI | manifest 路径 | 备注 |
|---|---|---|---|---|---|---|---|---|---|
| M0 | 2026-10-20 | ✅ 全绿（`./cmd/aicli/...` exit 0） | ✅ 全绿（`-count=2`） | ✅ 全绿（73 rows exactly once，8 项 PASS） | — | — | 基线已录（WS 511.4/Private 601.2 MB；cell_rows 64 MiB 顶格 / evictions 15,132） | `output/residency-baseline/run-20261020-234341/` + `…/t0-verify/status-full.json` | 新构建交互实例：`app_state.residency` 可读、默认关闭、`/exit` 干净退出（forced cleanup 0） |
| M1 | | | | | — | R7 | | | |
| M2 | | | | | — | R1/R4/R6 | | | |
| M3 | | | | | ✅ 必需 | R2/R3/R5/R8 | | | |
| M4 | | | | | ✅ 2 轮 | 全部 | | | |

> 说明：`—` 表示该里程碑不要求；`✅ 必需` 表示缺失即视为未达里程碑。所有引用的
> `manifest.json` 必须真实存在于 `output/aicli-terminal-e2e/` 且为干净运行（无 forced cleanup）。

### Spike 证据登记

| Spike | 日期 | 结果 | 证据 |
|---|---|---|---|
| **Spike-1 写穿物化** | 2026-10-20 | ✅ 一致率 100%（1000/1000）；put p95 1.00ms、读回 p95 <1ms；页字节 224,256 B（256 cells，预算 2 MiB）；store errors 0 | `output/residency-baseline/spike1/report.json` |
| **Spike-2 流式装载** | 2026-10-20 | 🟡 原型通过：5,632 cells / 44 页；order=0、duplicate=0、token gap=0；稳态窗口 2 页、瞬时 3 页（append→trim）；逐出 5,632、回读校验 5,632 全过、failures 0、put errors 0；单页 mint <0.01ms（合成口径）；整轮 wall 1.40s（含 5,632 次写穿 + 5,632 次回读） | `output/residency-baseline/spike2/report.json` |
| **Spike-3 冷区回填 + 虚拟 pager** | 2026-10-20 | ✅ 通过：5,632 cells 全冷区；10 页 × 128 cells 回填 p95 **9.02ms**（门禁 50ms）、失败 0、不可重建 0；1 万次锚点/定位探针 p95 **<1µs**（门禁 100µs）、锚点往返 **100%**；页面缓存 1,280 cells / 513,356 B | `output/residency-baseline/spike3/report.json` |
| Spike-4 内存基线 | — | 待执行 | — |

> Spike-1 语料为合成 1,000 cells（覆盖 plain CJK / 尾随空行、markdown Document、
> diff supplement、长 reasoning、tool-chain 分组），真实 5.5k 会话抽样留在 T9/R8。
> 真实会话单 cell payload 可能显著大于合成均值（876 B）：T4 需落地 `MaxPayloadBytes`
> 裁剪/分片策略并复测页预算（否则 page_bytes 上限不再成立）。
>
> Spike-2 为**算法原型**（`ui.TranscriptLoadPager`，与生产装载路径解耦）：页序/exactly-once/
> token 连续/窗口上界/写穿 fail-closed 均由单测钉住；与 `DeferHistoryDelivery`、一次性
> scrollback 替换授权的真实对接在 M3（T6/T8）完成。
