# aicli 渲染/移交剩余缺陷分类台账（2026-10-06）

> 口径：**可确定性修复**（本轮清理）与**依赖架构件**（不得再用概率性局部补丁伪装成修复）两类登记。
> 基线：`feat/render-p0-writer-unification` @ `b93cc1e4`（Stage 0 后）。
> 上游：`aicli-unified-render-architecture-audit-20261005.md` §5/§6、`aicli-ui-handoff-inflight-strand-hardening-plan-20261005.md` §5、
> `aicli-render-p1-state-convergence-plan.md`、`aicli-render-p1-1-step4-planning-incremental-plan.md`。

## 1. 依赖架构件（先落地前提，再谈根治）

| # | 缺陷 | 证据 | 根治前提 | 现状 |
|---|---|---|---|---|
| A1 | **写证明缺口**：ledger 无法区分「已落盘 / 未落盘」（no-replay 语义） | 硬化计划 §525-540：WIP 修复「作废→丢行、不作废→双写」被拒，原文「ledger/规划层无法决定是否发射」 | 写事务化 + 提交证明：写成功→提交记录落账；失败/中止→source-backed 恢复重绘 + 幂等。行级所有权状态机（移交/写互斥由状态迁移保证，而非在途时序门槛） | 未开始；resident-tail 双写已以帧/移交门槛止血（`1cace024`），属时序条件，非状态机保证 |
| A2 | **P2 特例族**：active 溢出归档 / sticky top-align / scrollback replay / reset backoff / settle-unresolved | 审计 §5 根因 2/3、§6 P2；当日两次现场缺陷（resident-tail 双写、中部插入）均落此族 | 历史线性化：finalized-only 进 history；可见窗口 W 行 + 溢出按行序 append；mutable 内容不提前入 scrollback（等 finalized 一次性写）；resize 只重画窗口 | 未开始（验收矩阵 = 审计 §6 P2 八项场景 + 真机 e2e） |
| A3 | **P2-1b 载荷聚合**：Acked+Active 前缀证明的渲染行载荷随会话单调增长；tombstone 每 range 一个 key | 硬化计划 §520；`activeAckedRenderedPrefixRows`（history_effect_planner.go:331-367）依赖结构化载荷做行等价匹配（显式拒绝文本哈希） | 证明载荷的可聚合表示——本质是 A1 写证明的一部分（行等价证明的新数据结构） | 未实施（设计依赖 A1） |
| A4 | **P3 全量渲染基线**：每帧全屏克隆/物化/强制重绘，成本与变化量脱钩 | 审计 §4 TOP1/2/3/7 | 增量编码（viewport 外占位不编码）+ 脏行 diff + 去全屏深拷贝 | 未开始（可先做 Stage 1 计划增量） |
| A5 | **P1-1 第 4 步**：规划续跑组删除（≈305 refs） | P1 计划 §1；子计划 §4 依赖 | 规划单线程化：子计划 Stage 1「段级增量装配」（Stage 0 已完成 `b93cc1e4`，热点归因见其 §1.5） | 子计划已就绪，待 Stage 1 |

**建议顺序**：A5（Stage 1，解锁第 4 步并顺带压掉 resume 慢用例）→ A2（含 A1 写证明件）→ A4；A3 随 A1 一并设计。

## 2. 本轮已清理（确定性修复）

| # | 缺陷 | 修复 | 证据 |
|---|---|---|---|
| B1 | `TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation` 在 `-race` 下被 60s 看门狗误杀（语义无失败，纯 wall-clock） | race 构建标签（`race_enabled_test.go`/`race_disabled_test.go`）+ `raceScaledDeadline`（`test_deadline_test.go`，×6）；语义断言不变 | 红：race 下 154.85s `history projection never converged`（`E:\tmp\cl-armed-race.txt`）；绿：`-race` ok 297.6s、常规 ok 81.1s（`E:\tmp\cl-armed-race2.txt` / `cl-armed-normal2.txt`） |
| B2 | 既有 flake：`TestPrintVisibleChatHistory_UnifiedHandoffsOverflowedCanonicalHistory` 等 4 处 actor idle 后即时断言 | `awaitHistoryCommitDelivered`（P1-3 顺带，前轮） | P1 计划 §5.1 |
| B3 | resident-tail 双写（约 1/4 概率） | 帧/移交边界在途门槛（`1cace024`）；本轮 `-count=40` 复核绿（`E:\tmp\cl-resident40.txt`） | 硬化计划 §545-559 |

## 3. 明确保留项（已决策，非缺陷）

- `historyTailCells`（撤回删除；ledger 需新增「投影重置后已交付 cell」索引方可替换）——P1 计划 §2.1；
- `historyPrepareHits/Misses`（暂缓；仅测试观测，待 /debug 导出或测试改用 `preparedHistory` 身份断言）；
- `historyTailRows` / `preparedHistory`（writer 私有，保留）；`PaintTrace`（/debug 专用）。
