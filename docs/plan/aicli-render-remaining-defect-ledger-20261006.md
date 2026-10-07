# aicli 渲染/移交剩余缺陷分类台账（2026-10-06）

> 口径：**可确定性修复**（本轮清理）与**依赖架构件**（不得再用概率性局部补丁伪装成修复）两类登记。
> 基线：`feat/render-p0-writer-unification` @ `b93cc1e4`（Stage 0 后）。
> 上游：`aicli-unified-render-architecture-audit-20261005.md` §5/§6、`aicli-ui-handoff-inflight-strand-hardening-plan-20261005.md` §5、
> `aicli-render-p1-state-convergence-plan.md`、`aicli-render-p1-1-step4-planning-incremental-plan.md`。
> 关联：G1–G12 差距收敛实施方案 `docs/plan/aicli-render-gap-closure-plan-20261006.md`
>（P0/P1-3/P2 尾项的执行入口；A1–A5 主线仍以本台账为准）。

## 1. 依赖架构件（先落地前提，再谈根治）

| # | 缺陷 | 证据 | 根治前提 | 现状 |
|---|---|---|---|---|
| A1 | **写证明缺口**：ledger 无法区分「已落盘 / 未落盘」（no-replay 语义） | 硬化计划 §525-540：WIP 修复「作废→丢行、不作废→双写」被拒，原文「ledger/规划层无法决定是否发射」 | 写事务化 + 提交证明：写成功→提交记录落账；失败/中止→source-backed 恢复重绘 + 幂等。行级所有权状态机（移交/写互斥由状态迁移保证，而非在途时序门槛） | **侦察完成（2026-10-07）**：`docs/plan/aicli-render-a1-write-proof-recon-20261007.md`；基线校正——`1cace024` 门槛已随 A2 第二刀（`e3236de9`）整体删除，resident-tail 保护现为 planner 不 rebase 游标 token + generation→Deferred + 会话侧 tail 去重（仍属时序条件）；`TerminalEpoch` 无生产推进点（恢复恒 settle）。A1-1 矩阵完成（`1e223029`/`7637cd56`）；A1-2a 写事务证明贯通（`c29865fd`，abort 派发二义纠偏）；A1-2b 失效证明化（`b99c6bd1`，Q2/Q4 + 写前门控）；Q3 覆盖集逐 token 解析（`31cf5f72`，整批回退删除）；A1-2c settle 终态定稿 + 六门槛删除面评估（设计文档 §3.4/§3.6 对齐，专项用例绿）；**A1-3 3a–3e 完成（2026-10-07）**：mint 游标加速器（`1f375719`/`b66d3367`）、装载边界墓碑剪枝（R5，`17c10e53`）、Quarantine 两轴折叠 + 索引/ackBatch 复核（`43499641`）；`-race` 双包绿 + 真机 e2e exactly-once/无 3J（记录：`docs/plan/aicli-render-a1-3-row-cursor-plan.md` §6） |
| A2 | **P2 特例族**：active 溢出归档 / sticky top-align / scrollback replay / reset backoff / settle-unresolved | 审计 §5 根因 2/3、§6 P2；当日两次现场缺陷（resident-tail 双写、中部插入）均落此族 | 历史线性化：finalized-only 进 history；可见窗口 W 行 + 溢出按行序 append；mutable 内容不提前入 scrollback（等 finalized 一次性写）；resize 只重画窗口 | **已完成（2026-10-07）**：archive 停铸（Slice 1 `130cc7f5`/`7471d34a`/`80738513`/`e3236de9`）；replay S1–S5（`59ac603d`/`9a205572`/`9e7383c1`/`7ade9732`/`473da400`）；锚定统一（`90d3342d`，+76/−192）；backoff 收敛（§9.5）；真机 e2e 73 行 exactly-once + 无 3J |
| A3 | **P2-1b 载荷聚合**：Acked+Active 前缀证明的渲染行载荷随会话单调增长；tombstone 每 range 一个 key | 硬化计划 §520；~~`activeAckedRenderedPrefixRows`~~（**已随 A2 第二刀删除**，依据 stale） | 证明载荷的可聚合表示——本质是 A1 写证明的一部分（行等价证明的新数据结构） | **重估（2026-10-07）**：Ack 即 `Lines=nil`（`history_commit.go:388`），已交付载荷增长已止血；剩余增长面 = tombstone 集合（每 range 一个 key、无聚合）+ Queued 载荷。A1-3 3c-redesign 已收敛：墓碑保留为精确阻断真相并**按当前 transcript 有界**（装载边界剪枝，`17c10e53`），不再随历史交付行数增长；聚合/删除问题关闭（设计/实施记录：`docs/plan/aicli-render-a1-3-row-cursor-plan.md` §3/§6） |
| A4 | **P3 全量渲染基线**：每帧全屏克隆/物化/强制重绘，成本与变化量脱钩 | 审计 §4 TOP1/2/3/7 | 增量编码（viewport 外占位不编码）+ 脏行 diff + 去全屏深拷贝 | 未开始（可先做 Stage 1 计划增量） |
| A5 | **P1-1 第 4 步**：规划续跑组删除 | P1 计划 §1；子计划 §4 依赖 | 规划单线程化：子计划 Stage 1–5 | **已完成（2026-10-07）**：Stage 1 段级增量（`9d82756c` 等）→ Stage 2 无预算单遍（`72f7f7f4`）→ Stage 3 去异步（`c2745fb5`）→ Stage 4 续跑组删除（`9651f07b`，+60/−162）；全量 ui/commands + `-race` 点检绿 |

**建议顺序**：A5 已完成 → A2 已完成 → A1（写证明件：写事务化 + 提交证明 + 行级所有权状态机）→ A4；A3 随 A1 一并设计。

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
