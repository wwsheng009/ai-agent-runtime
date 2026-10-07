# P1-1 第 4 步前置子计划：规划单线程增量（无预算截断、无异步 screening）

- 定位：`docs/plan/aicli-render-p1-state-convergence-plan.md` §1.3 第 4 步（删除规划续跑组）的前置子计划。
  本子计划验收达成前，第 4 步不得启动（删除面约 305 处引用，见 §1.4）。
- 性能背景：`docs/plan/resume-large-session-optimization-plan-20260924.md` §4.3/§4.4/§4.5/§4.6/§4.7/§4.8。
- 侦察日期：2026-10-06（三路只读侦察：预算截断 / 异步栅栏 / 删除面；行号以当日工作区为准）。
- 关联：残余差距收敛（G1–G12）入口：`docs/plan/aicli-render-gap-closure-plan-20261006.md`。

## 0. 终态定义与总验收

终态四要件：

1. **单线程**：transcript 规划只在 reducer（actor 线程、持 `c.mu`）内执行；无 worker、无跨线程结果 action。
2. **增量**：单次规划代价 ∝ 输入增量（变更 cell 数 / 新增页），与历史总规模解耦。
3. **无预算截断**：不存在"被时间预算切断的半成品计划"；`PlanIncomplete / PlanStalled / planResume* / planRequestSeq / planRequestInFlight / planInputsEpoch` 全部成为可删状态。
4. **语义不回退**：prepend（较早页在前）、armed resume、在途批次延迟、membership 踢除、全覆盖与顺序不变。

总验收（缺一不可）：

- `BenchmarkDeferredOlderPageReplan/second_plan_prepend` ≤ 基线 1/3（本机基线 522.4ms → ≤174ms；Stage 0 记录见 §1.5）。
- 大会话（6,720 cells / ~161k 行）重复 pass 代价 ∝ 增量（4,001 vs 6,720 cells 两档对照不随规模增长）；冷启动首轮 ≤ 单窗口预算（≤2s，harness P12 口径）。
- 语义组保持绿：`TestDeferredOlderPagePrependReplansAndCoversOlderCells`、`TestPlanEligibleHistoryCommitsResumeUnionMatchesFullPlan`（改写后）、`TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation`（改写后）。
- `go test ./cmd/aicli/ui/` 与 `./cmd/aicli/commands/` 全量（含 -race 抽验）绿。

## 1. 现状机制测绘（证据）

### 1.1 预算截断与续跑组

- 预算：`historyCommitPlanningBudget = 250ms`（`history_effect_planner.go:24`；var 仅为测试压 0）。
- 截断点：`app_screen_layout.go:258-265`（按 `layoutBudgetCheckRows=4096` 采样；先走完当前 cell，绝不返回空前缀 `:247-253`）。
- 截断落账：`applyTranscriptPlanWindow`（`history_effect_planner.go:1037-1070`）→ `PlanIncomplete=true` + `storeTranscriptPlanResume`（`history_effect_queue.go:312-320`）。
- 续跑：ack 处理器（`controller_state.go:319/338/379/410`）+ executor kick（`ContinueHistoryPlanAction`，`terminal_session_executor.go:1107-1121`）→ `continueTruncatedHistoryPlan`（planner `:1168-1221`，drain gate `:1194`）；`PlanStalled` 防旋（`:1218`）；末尾零预算全量 membership（`finishResumedTranscriptPlan` `:1125-1145`）。
- 唤醒链：`planContinuationPending`（`:1228-1236`）→ `historyCommitWakeNeeded`（`controller.go:641-656`）、`terminal_session_snapshot.go:55/220`、executor kick（同上）。
- 存在理由（原文）：planner `:14-18`（病态 markdown 单候选数百 ms）、`:1206-1210`（旧无预算单遍 = 数秒持锁）、queue `:43-57`（idle resume 无 transcript 迁移，前缀排空即终点）。

### 1.2 异步 screening 与三重栅栏

- screening = 纯布局相位（`screenTranscriptPlanWindow` planner `:87-89` → `layoutTranscriptScreenRowsFrom` `app_screen_layout.go:229`）。
- 生产 wiring：`commands/chat_ui_actor.go:67-75`（`AsyncTranscriptPlan: true`）；worker `controller_plan_worker.go:84-110`；结果 action `HistoryPlanWindowReady`（`action.go:571-589`）。
- 栅栏：seq（dispatch `:176` / handle `:1081`）、`planInputsEpoch`（`invalidateTranscriptPlanMemo` queue `:800` / handle `:1095`）、输入指纹（`:160/:1094`）；stale 立即重派发（`:1101-1111`）；在飞抑制 kick（queue `:85-86`）。
- 理由：把大会话 resume 的 O(entire history) 布局前置移出 actor 锁（`commands/chat_ui_actor.go:67-69`）。

### 1.3 成本归因（resume-large-session §4.6，实测）

- L2.2 后布局已非瓶颈：全 transcript 热态 2.5ms、较早页 1.7ms。
- 大头在规划器 screening + 铸 commit（104ms @4,001 cells）与计划集成（~180ms）；6,720 cells 第二次规划 **602ms**（超线性）。
- 既定替代方向 §4.7 **L2.6**：把布局层"前缀复用"上移到计划层——按 cell 身份/revision 缓存已 screening 屏幕行，增量重组计划顺序。

### 1.4 删除面（第 4 步本体）

- 去重后总引用 ≈305（生产 ≈207 / 测试 ≈113），8 个测试文件 + executor 1 处。
- 跨包：commands debug HTTP/文档 8 处读 4 个诊断字段（schema 稳定性需决策，§2.3）。
- 5 个不可直接删耦合：memo 早退（planner `:1353`）、在途批次挂起（`:984-988/:1089-1092/:1126-1130`）、`planContinuationPending` 4 类消费方、共享同步 helper（`screenTranscriptPlanWindow`/`planEligibleHistoryCommitsWithinFrom`）、benchmark 假阳性（`prependReplanSink` 与 planSink 无关）。

### 1.5 Stage 0 实测记录（2026-10-06，本机，`-benchtime 3x`）

基线（`BenchmarkDeferredOlderPageReplan`，ui 包）：

| 子基准 | 耗时 | 分配 |
|---|---|---|
| `second_plan_prepend`（6,720 cells） | **522.4ms** | 346.6MB / 837k allocs |
| `first_plan`（4,001 cells） | 229.5ms | 246.6MB / 678k allocs |
| `plan_only`（4,001 cells） | 135.5ms | 124.9MB / 176k allocs |
| `older_page_layout_only`（2,719 cells） | 1.9ms | 3.6MB |
| `layout_only` / scene 热布局 | 2.8ms / 4.9ms | — |
| `state_clone` / `clone_history_effects_only` / `clone_transcript_only` | 148.8ms / 136.6ms / 0.24ms | — |
| `install_only` | 0.43ms | — |
| `history_effects_diagnostics_projection` | 6.8ms / 0 allocs | — |

分解读法（基准自带口径 `:241-247`）：`second(522) ≫ first(229) + older_layout(1.9)`
⇒ 第二次规划**不是**增量形态；渲染/布局已非瓶颈（per-cell `sharedCellRows` 缓存已存在，热布局 2.8–4.9ms）。

CPU profile（`plan_only` + `second_plan_prepend`，9,630ms 样本）热点：

| 函数 | cum | 说明 |
|---|---|---|
| `syncHistoryEffectCandidates` | 3,340ms（34.7%） | `enqueueHistoryCandidates` 1,850ms；全量 `byToken` 对账 980ms；`valid` map 构建 430ms |
| `assemblePlainHistoryCommits` | 900ms（9.4%） | 逐行 `settled`（`hasTerminalRecordForSource`）查询 610ms |
| GC/分配内部 | ~25% | 单次规划 125–346MB 分配 |
| map 操作 | matchH2 12.6% flat | 大量小 map 查找（源身份键） |

**结论（Stage 1 范围修正）**：L2.6 的落点不是 screening 复用（已有 per-cell 行缓存），
而是**计划层段级增量装配与集成**——per-cell 提交段（commits + source 身份）memo、
未变段复用（跳过逐行 settled，改提交级过滤）、集成按段 diff（免全量 `byToken` 扫描与全量 enqueue）、
prepend 时复用段按常量偏移重定基 DisplayRange（与 `RebasePending` 同语义）。

## 2. 设计决策

### 2.1 候选

- **A（推荐首做）计划层段级增量装配（L2.6 的实测修正版）**：per-cell 计划段 memo（commits + source 身份）＋提交级 settled 过滤＋段 diff 集成＋prepend DisplayRange 重定基；重复 pass 代价 ∝ 变更段。复用段仅跳过重算，不触碰投递语义，风险最低；Stage 0 实测确认渲染缓存已存在、热点在装配/集成（§1.5）。
- **B（备选）L2.4 计算移出锁 + L2.7 ledger 克隆地板**：单线程顺序执行但计算在锁外；无预算、无 worker。当前整状态 `Clone()` 被 ledger 深拷贝钉在 157–180ms（resume §4.8），需 L2.7 中期项（已 ack entry 降级为不含 `Lines`）达标后才可行。
- **C（暂不采用）需求驱动增量铸 commit**：以结构性批量边界替代时间预算；需改 membership/逐出语义，风险最大。

### 2.2 推荐路径

Stage 1（A）→ 重测 → Stage 2（无预算同步化）→ Stage 3（去异步）→ Stage 4（= 第 4 步删除本体）。

理由：A 是既定方向且验收已存在；冷启动首轮是唯一未被 A 覆盖的敞口（§4 风险 1），故以 Stage 2 实测门控而非预判——超预算则 Stage 3 阻塞并转 B（先落 L2.7 步骤 1–3）。

### 2.3 关键耦合处置（先定契约再动刀）

- **memo 早退**：删除 `PlanIncomplete` 早退（planner `:1353-1355`）后，memo 语义回归"完整规划已落"；增量 screening 不得在部分结果上落 memo（保证：单次 pass 要么完整重组、要么不落）。
- **在途批次挂起**：`hasClaimedActiveOriginDelivery` 分支不再有 `PlanIncomplete` 可用；替代 = ack/settle 处理器无条件重跑规划（新规划便宜）+ memo 由 `finalizedActiveAckPlanVersion` 失效（已存在，planner `:1315-1317/:1366`）。
- **wake/kick**：`planContinuationPending` 删除；空队列唤醒分支删除；executor kick 删除；`terminal_session_snapshot` 两处读点删除。前提：规划不再有"欠账"状态。
- **诊断 schema**：`HistoryEffectDiagnostics` 四字段保留为 deprecated 零值（跨包 JSON 稳定性优先），commands 注释标注；不破坏 debug API。
- **active-cell 预算**（planner `:526`）：属"单 chunk 软早退"，非计划截断，保留并注明。

## 3. 分阶段实施（每阶段独立提交，失败即回滚）

### Stage 0 基线与门禁（已完成，2026-10-06；实测记录见 §1.5）

- 固化基线：`go test ./cmd/aicli/ui/ -run '^$' -bench 'BenchmarkDeferredOlderPageReplan|BenchmarkLayoutTranscriptResumeScale|BenchmarkPlanEligibleHistoryCommitsPlainTranscript' -benchtime 3x`，记录 `second_plan_prepend` 当前值。
- CPU profile 归因已完成（`plan_only` + `second_plan_prepend`）：热点 = 集成 34.7% / 装配 9.4% / GC ~25%，见 §1.5。
- 锁内耗时打点已存在（P16 `recordTranscriptPlanTiming` queue `:300`；plan-last/max 诊断）。

### Stage 1 计划层段级增量装配（2–4 天；范围按 Stage 0 实测修正，§1.5）

- 1a 段索引：per-cell 计划段（`[]HistoryCommit` + source 身份集合 + 段行数），键 = cell 身份/revision/呈现输入
  （width/theme/generation/skipRows 相关项）；容量自适应，与 `sharedCellRows` 同型但缓存的是**提交段**而非屏幕行。
- 1b 复用与重定基：未变段直接复用；prepend 场景按前插行数对 DisplayRange 做常量偏移
  （复用段不重跑 assemble/逐行 settled）；变更段走现有 assemble+mint。
- 1c 提交级 settled 过滤：复用段按提交（非逐行）做 `hasTerminalRecordForSource` 过滤后交付集成。
- 1d diff 集成：`syncHistoryEffectCandidates` 只对「新增/变更/移除」段做入队与逐出；
  未变段跳过 enqueue 与全量 `byToken` 对账（Stage 0 热点 3,340ms 的消除点）。
- 1e 结构断言与等价：段命中/复用计数可观测（诊断）；增量结果 == 无预算全量（身份多重集 + 顺序）；
  prepend 覆盖与顺序（`TestDeferredOlderPagePrependReplansAndCoversOlderCells` 保持绿）。
- 验收：`second_plan_prepend` ≤174ms（1/3 × 522ms）；宽回归组（History/Transcript/Plan/Sync/Executor/NativeScrollback/两个 E2E）绿。

### Stage 2 无预算同步化（1–2 天）

- 2a：transcript 路径切无预算单遍；删 deadline 形参/截断分支/`startRow/screenRowsBefore/nextRow/screenRows` 续跑参数；`planEligibleHistoryCommitsWithinFrom` 收敛为单一实现。
- 2b：在途批次挂起替代（§2.3）。
- 2c：memo 契约改写（删 `PlanIncomplete` 早退）。
- 验收：冷/热单次规划锁内耗时实测（harness 口径）；截断类测试删除或改写；全覆盖组绿。

### Stage 3 去异步（1 天）

- 删 worker/sink/请求/`HistoryPlanWindowReady`/三重栅栏字段/wiring/`WaitIdle` 例外/`AsyncTranscriptPlan` 配置；`TestProductionUIActorEnablesAsyncTranscriptPlan` 删除。
- 验收：装载/覆盖度断言在同步语义下绿；`-race` 抽验绿。

### Stage 4 = P1-1 第 4 步本体（1–2 天）

- 删续跑组与全部消费方（§1.4 清单）；wake/kick/snapshot 收口。
- 验收：主计划 §1.3 第 4 步验收 + 全包门禁。

### Stage 5 收尾（0.5 天）

- 诊断注释、主计划台账回填、文档登记。

## 4. 风险

1. **冷启动首轮锁内耗时（最大敞口）**：A 只覆盖重复 pass；首轮全量若超单窗口预算，Stage 3 阻塞转 B。门控 = Stage 2 实测。
2. **prepend/重排语义**：较早页插入导致顺序重组；测试先行（`TestDeferredOlderPagePrependReplansAndCoversOlderCells`）。
3. **在途批次**：新挂起契约错则丢计划（§1.4 耦合 2）；`TestFinalizeDefersTranscriptPlanWhileActiveBatchInFlight` 改写后必须等价。
4. **memo 误命中**：部分计划落 memo 会漏交付；结构断言 + 等价测试双保险。
5. **回滚粒度**：每阶段独立提交；Stage 2/3 失败回滚到 Stage 1 态（同步+预算仍可用）。

### 1.6 Stage 1 设计细化（2026-10-06 编码前复核，工作区实读）

**已存在的缓存（先复用，不重造）**：

- `sharedHistoryPlan`（history_plan_cache.go:27-54）：per-cell `[]planPhysicalRow` LRU（8192 条 / 64MB）；
  `planPlainCellHistoryCommits` 命中路径（planner:724-741）跳过 wrap/物化，仅做对齐校验 + `assemblePlainHistoryCommits`。
- 结论：Stage 1 不需要新的物理行缓存；缺的是**提交级**复用与**集成 diff**。

**提交身份模型（决定 diff 可行域）**：

- `historyCommitSourceKey`（history_commit.go:210-217）：origin/cellID/revision/sourceStart/sourceEnd/fragmentID
  ——**不含 DisplayRange**；`bySource` + `compactedTerminalSources` 是其索引，`BlocksRemint`（:187-196）
  对 **Queued 与 Delivered 均为 true** ⇒ `hasTerminalRecordForSource` 覆盖未交付条目，
  `enqueueHistoryCandidates` 对已存在来源的候选直接跳过。
- `historyCommitRangeKey`（:236-248）：source key + displayStart/displayEnd/layoutGeneration，用于 `byRange`
  （Enqueue 的精确重复检测 / RebasePending 冲突检测）。
- `historyCommitPresentationEqual`（planner:1563-1578）：finalized 提交**比较 DisplayRange**（Active 已跳过）。
- prepend 场景：全部 finalized Queued 条目 DisplayRange 常量平移 → presentationEqual=false →
  `RebasePending` 全量重定基（reconcile 980ms 与 byRange 双 map 操作的主体）。

**DisplayRange 生产消费方（实读，非测试，仅 3 处）**：`Valid()` 校验（:82）、`historyCommitKey`（byRange）、
`historyCommitPresentationEqual`。**writer/presenter/scrollback 均不消费**（字节权威 = SourceRange + Lines）。

**Stage 1d 两个候选方向（编码前二选一或组合）**：

- **D1（保守）**：段 diff 只跳过「source 身份集合与 Lines 均未变**且** DisplayRange 未变」的段；
  prepend 仍全量重定基 ⇒ 只消 enqueue 侧，消不掉 reconcile。
- **D2（激进，收益大）**：把 DisplayRange 从「呈现等价」与 `byRange` 键中降级为纯簿记
  （finalized 亦跳过比较；`historyCommitRangeKey` 收敛为 source key + layoutGeneration）——
  prepend/中部插入不再触发全量 RebasePending，未变段可直接跳过。
  待证不变式：①`byRange` 去 display 后无重复铸造路径（bySource 阻断已覆盖 Queued/Delivered）；
  ②跨 generation（reflow）不碰撞由 layoutGeneration 键隔离；③**中部插入反例已识别（2026-10-06）**：
  新来源会复用被平移条目的旧 display 区间；若只跳过重定基而保留 display 键（D2-lite），
  `Enqueue` 命中 `byRange[旧键]` 且 `BlocksRemint`=true 会把新来源提交当重复拒绝（**丢行**）——
  因此 D2 必须**同时**把 display 从 `byRange` 键移除，而不是"保留键、只跳重定基"。
  ④续跑轮键稳定性：display 不进键 ⇒ 天然稳定（旧注释的"局部下标"问题一并消失）。
  风险：`byRange` 语义收窄后，直接 `Enqueue`（测试/手写状态）的重复检测覆盖面变化需测试钉死。

**落点排序（修订）**：D2 身份论证（测试先行）→ 1c 提交级 settled 过滤 → 1d 段 diff 集成
→ 1b 复用段常量偏移重定基 → 1a 段索引与命中计数（诊断）。

### 1.7 D2 实施记录（2026-10-06，已完成）

- 变更：`historyCommitRangeKey` 移除 displayStart/displayEnd（`history_commit.go`）；
  `historyCommitPresentationEqual` 对 finalized 亦不再比较 DisplayRange（planner 注释同步重写）。
- 回归测试：`TestHistoryCommitLedger_DisplayRangeReuseDoesNotBlockNewSource`（中部插入复用旧 display
  区间不丢行；同来源+同 generation 仍拒绝）、`TestHistoryCommitPresentationEqual_DisplayOnlyShiftIsEqual`
  （display-only 平移判等；Lines 变化判不等）。
- 门禁：ui 全量 ok 114.2s、commands 全量 ok 173.8s、prepend 覆盖结构断言绿。
- 基准（本机 `-benchtime 3x`，含侦察并发噪声）：`second_plan_prepend` 503–530ms（基线 522.4ms，噪声内）；
  分配 **837k → 641k allocs/op（-23%）**、346.6 → 325.0 MB/op。
- 结论：D2 的时间收益在噪声内（其消除的重定基只是 522ms 的一小部分）；价值 = 身份收敛（中部插入
  丢行隐患从"依赖全量重定基"变为"结构上不可能"）+ 分配下降 + 为 1d 段级 diff 解除 display 障碍。
- **验收修订（重要）**：基线 522ms 的构成 = `state.Clone()` 136–148ms（L2.4 固定开销，属 L2.7 clone 地板）
  + 2,719 新 cell 的真实增量（mint+enqueue）+ 存量 4,001 cell 的重算。**≤174ms 在 L2.7 落地前不可达**。
  Stage 1 独立验收改为：`second_plan_prepend − clone − older_page 增量` 的**存量重算部分下降 ≥50%**
  （用 plan_only / first_plan / state_clone 三个子基准差分读）。

### 1.8 Stage 1c/1d 编码设计（2026-10-07，工作区实读补充；编码前冻结）

**落点**：planner 装配产出「候选 + 保留分类」，integration 消费保留分类跳过全量对账。

**保留分类模型**（`mintTranscriptPlanWindow` 扩展输出）：

- 段 memo 命中（`sharedHistoryPlan` 行缓存 + 提交段 memo，键 = `cellLayoutKey` + generation + skipRows + 完整覆盖）且 cell 复用：
  - 段内提交逐条 `hasTerminalRecordForSource` 分拣：
    - 全部已有终态记录 → `retainedCells`（cell 级；integration 跳过该 cell 的全部 ledger 条目）；
    - 部分终态/部分新增（混合 cell）→ 新增提交入 `candidates`；已有终态 source 入 `retainedSources`（仅混合 cell 需要，规模 ≤ 在飞窗口）；
    - 全部无终态 → 复用 memo 提交直接入 `candidates`（免重建 Lines）。
- memo miss（新增/变更 cell）→ 现有 assemble 路径；产出同一分类结构。
- 截断窗口（部分覆盖）不落段 memo，也不进入保留分类（维持现有前缀语义）。

**integration（`syncHistoryEffectCandidates`）**：

- `valid` map 仅由 `candidates` 构建（规模 = 新增/变更；prepend 场景从 161k 降至 ~65k）。
- `byToken` 对账：entry 的 cell ∈ `retainedCells` 或 source ∈ `retainedSources` → 跳过；
  其余照旧（absent → invalidate；presentation 漂移 → rebase/invalidate）。
- `enqueueHistoryCandidates(candidates)` 只入队新增。
- 截断前缀路径（`syncHistoryEffectCandidatesPrefix`）不变（只入队）。

**安全条件（反例，必须同时满足）**：

1. 不可仅按「已有终态记录」跳过：presentation 漂移（reflow/width/generation 变化）时新提交必须入
   `candidates` 触发 rebase/invalidate —— 跳过仅允许在段复用（Lines 身份由 memo 键保证相同）前提下。
2. 混合 cell 的被跳过条目不得落入「absent → invalidate」：必须显式 `retainedSources`。
3. ledger/会话替换：memo 键含 cell 身份；若 cell 身份可复用（序列重置），须在 ledger 重置点清段 memo
   （实现时加 epoch 或显式清点）。
4. 复用提交的 DisplayRange 按 displayStart 常量偏移重写（D2 后不参与等价/键，仅簿记准确）。

**管线改动面**：`mintTranscriptPlanWindow`（返回扩展）→ `applyTranscriptPlanWindow` →
`HistoryPlanWindowReady` 载荷（action.go）→ `handleHistoryPlanWindowReady` →
`syncHistoryEffectCandidates`（新增 retained 参数，nil = 旧语义）；`rebasePendingHistoryEffects` 同构消费。

**诊断（1e）**：段命中/复用/保留计数入 `recordTranscriptPlanTiming` 旁路，/debug 可读。

**验收**：§1.7 差分口径（存量重算 ≥50% 下降）+ 宽回归组绿 + 既有等价/覆盖断言
（`TestPlanEligibleHistoryCommitsResumeUnionMatchesFullPlan`、`TestDeferredOlderPagePrependReplansAndCoversOlderCells`）保持绿。

**回滚**：单提交；retained 参数缺省 nil 时行为与当前逐字一致（可分步落地）。
