# P1-1 第 4 步前置子计划：规划单线程增量（无预算截断、无异步 screening）

- 定位：`docs/plan/aicli-render-p1-state-convergence-plan.md` §1.3 第 4 步（删除规划续跑组）的前置子计划。
  本子计划验收达成前，第 4 步不得启动（删除面约 305 处引用，见 §1.4）。
- 性能背景：`docs/plan/resume-large-session-optimization-plan-20260924.md` §4.3/§4.4/§4.5/§4.6/§4.7/§4.8。
- 侦察日期：2026-10-06（三路只读侦察：预算截断 / 异步栅栏 / 删除面；行号以当日工作区为准）。

## 0. 终态定义与总验收

终态四要件：

1. **单线程**：transcript 规划只在 reducer（actor 线程、持 `c.mu`）内执行；无 worker、无跨线程结果 action。
2. **增量**：单次规划代价 ∝ 输入增量（变更 cell 数 / 新增页），与历史总规模解耦。
3. **无预算截断**：不存在"被时间预算切断的半成品计划"；`PlanIncomplete / PlanStalled / planResume* / planRequestSeq / planRequestInFlight / planInputsEpoch` 全部成为可删状态。
4. **语义不回退**：prepend（较早页在前）、armed resume、在途批次延迟、membership 踢除、全覆盖与顺序不变。

总验收（缺一不可）：

- `BenchmarkDeferredOlderPageReplan/second_plan_prepend` ≤ 基线 1/3（基线 602ms，§1.3）。
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

## 2. 设计决策

### 2.1 候选

- **A（推荐首做）计划层增量 screening（L2.6）**：缓存已 screening 屏幕行（键含 cell 身份/revision/布局输入），单次 pass 只 screen 变更 cell 并重组计划；重复 pass 代价 ∝ 增量。只改 screening 复用，不触碰投递语义，风险最低，且验收口径已存在。
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

### Stage 0 基线与门禁（0.5 天）

- 固化基线：`go test ./cmd/aicli/ui/ -run '^$' -bench 'BenchmarkDeferredOlderPageReplan|BenchmarkLayoutTranscriptResumeScale|BenchmarkPlanEligibleHistoryCommitsPlainTranscript' -benchtime 3x`，记录 `second_plan_prepend` 当前值。
- 锁内耗时打点已存在（P16 `recordTranscriptPlanTiming` queue `:300`；plan-last/max 诊断）。

### Stage 1 计划层增量 screening（2–4 天）

- 1a 新缓存：per-cell screened rows（键含 cell 身份/revision/width/theme/generation 相关项；容量自适应；append-only 前缀复用与 L2.2 同型）。
- 1b 重组：全量 rows = 缓存段拼接 + 变更 cell 增量 screen；commit 铸走现有 `mintTranscriptPlanWindow`；cell 边界对齐契约（`TestLayoutTranscriptScreenRowsFromResumesAtCellBoundaries` 语义）。
- 1c 结构断言：screening 次数/缓存命中计数可观测（诊断）；等价测试：增量结果 == 无预算全量（身份多重集 + 顺序），prepend 场景覆盖与顺序。
- 验收：`second_plan_prepend` ≤1/3 基线；宽回归组（History/Transcript/Plan/Sync/Executor/NativeScrollback/两个 E2E）绿。

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
