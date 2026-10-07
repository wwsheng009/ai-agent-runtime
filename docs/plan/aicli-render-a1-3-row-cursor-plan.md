# A1-3 行序交付游标实施子计划（设计冻结）

> 性质：A1-3 切片的设计冻结（先设计后编码；每步 tests-first、可独立回退）。
> 依据：`docs/plan/aicli-render-a1-write-proof-recon-20261007.md` §4（A1-3 定义）、
> `docs/architecture/aicli-tui-renderer-architecture-design.md` §3.4/§3.6（目标形态）、
> `docs/plan/aicli-render-a1-2-write-proof-record-plan.md`（A1-2 已落地基线）。
> 基线：`feat/render-p0-writer-unification` @ `f78f75dc`（A1-2c 收尾后）。

## 1. 语义定义（本轮冻结）

### 1.1 顺序空间判定（关键结论）

现有事实（代码证据）：

- token 按入队顺序单调分配（`NextToken++`），队列 claim 只允许头指针位置
  （`queueHeadToken` = 最小 Queued token），ack 要求 `WriteCursor==token`；
  因此**物理交付序 = token 序 = mint 序（首次资格序）**。
- 计划序（transcript 序）与交付序**不是同一个序**：deferred 较早页 prepend 后，
  新铸的较早页 token 追加在存量 token 之后（`history_prepend_replan_bench_test.go:197-200`：
  「ledger 会保留上一轮已经铸好的 token…把新铸的较早页追加在后面。交付顺序由计划顺序
  决定，而不是此刻的 ledger 顺序」——计划序断言只约束**这一轮新铸集合**与呈现簿记；
  物理 append 序仍是 token 序）。
- 因此：**游标只能建立在 mint 序上**；把游标建立在 transcript 序或"行位置"上都会在
  prepend/中部插入下失稳（recon 风险 #2 的答案：粒度 = `(cell, revision, source range,
  fragment)` 身份，序 = mint 序）。

### 1.2 mint 序的稳定化（allocation order 定理）

**定理**：在「最新页先同步装载并完成首帧规划，较早页由 deferred 路径后插」
（`chat.go:904-907` 装载契约）前提下，mint 序 = **allocation 序**，即
`(cellID, revision, sourceStart, fragmentID)` 字典序。

- 常规流式：cell 按流序分配 ID 并 finalize，首次资格序 = ID 序。
- prepend/中部插入：新 cell 的 ID 更晚分配、也更晚进入规划 → mint 序仍是 ID 序。
- **唯一反例**：两页在首次规划前同装（walk 序 = transcript 序会把高 ID 的较早页先铸）。
  生产路径不存在（deferred 装载在首帧之后才启动）；本切片将其钉为**不可达契约**
  （断言 + 守护用例），若未来需要支持，则铸造必须改为按 allocation 序而非 walk 序。

### 1.3 游标模型（三个有界集合）

| 名称 | 含义 | 规模 |
|---|---|---|
| `mintedFrontier` | 已铸造前缀上界（allocation 序，单调）；候选「已铸造」⇔ `sourceKey ≤ frontier` | O(1) |
| `live` | 现存 `byToken` 条目（queued/claimed/未压缩终态） | 有界（队列 + 未压缩窗口） |
| `remintable` | ≤ frontier 但**允许重铸**的来源（仅 invalidated-clean 零写一类） | 有界例外 |

- claim 身份 = frontier 之后的下一 slot（token + `(cell, range, fragment)` 身份）；
  仍至多一个在途（单飞写证明）。
- 「cell 可跳过物化」⇔ 该 cell 全部 source ≤ frontier 且无 live 条目
  （替代 `hasSettledRecordForSource` 的逐条扫描）。

### 1.4 不变量

- **I1 无重复铸造**：`sourceKey ≤ frontier` 的来源不得再次铸造（`remintable` 例外）。
- **I2 无丢行**：每个 finalized source 恰好进入一次交付或一次退役（delivered / retired），
  覆盖 = 全量（既有覆盖度测试口径不变）。
- **I3 顺序**：token 序 = allocation 序 = 物理 append 序；claim 单飞；ack 覆盖集为 token 前缀。
- **I4 压缩安全**：压缩只剪 `live` 之外的终态；frontier + `remintable` 保持判定等价。
- **I5 回滚面**：每步独立可回退（字段/分支先并存，双跑断言后再删旧路径）。

## 2. 迁移步骤（tests-first，逐步提交）

| 步 | 内容 | 行为变化 | 验收 |
|---|---|---|---|
| **3a** | ledger 增 `mintedFrontier` + `remintable` 集，在 `Enqueue`/`pruneEntry` 处维护；纯镜像 | 无 | 等价性测试：任意 fixture 序列下 `sourceKey ≤ frontier`（+例外）≡ 旧 `hasTerminalRecordForSource` / `compactedTerminalSources` 并集判定 |
| **3b** | 规划 skip 切换：`enqueueHistoryCandidatesRetained`、`hasSettledRecordForSource`、`retainedQueuedCommitForSource` 改走 frontier + live 检查；旧路径留作测试双跑对照 | 无（双跑断言） | 双跑一致 + prepend/retention/生成漂移族绿 |
| **3c** | 压缩改造：装载边界墓碑剪枝（R5 修复版） | **已实施（见 §6）**：墓碑保留为精确阻断真相；`ReplaceTranscriptAction` 整体替换（armed 装载/较早页插入，或 cell 集合收缩）时剪除 `cellID ∉ 新 transcript` 的压缩来源。frontier 退回纯加速器 | 剪枝/保留双向用例 + 装载/前缀生产流绿 + 宽回归绿 |
| **3d** | 删除面落地（按 §3 结论）：Quarantine 子类折叠评估、`bySource` 降级、`byRange` 收敛、ackBatch 形态复核 | 视评估 | 逐项迁移记录 + fail-closed 语义不回退 |
| **3e** | 验收：宽回归（ui + commands + `-race`）+ 真机 e2e（exactly-once 72 行 + resume 页序） | — | 全绿 + 台账/设计文档同步 |

## 3. 删除面评估（前置结论，3d 实施时复核）

| 对象 | 判定 | 依据 / 前提 |
|---|---|---|
| `compactedTerminalSources`（tombstone） | **保留（精确阻断真相），装载边界按当前 transcript 剪枝** | frontier 只作加速器（3b）；「已铸造」的精确真相是墓碑本身（R5：装载可引入低于旧 frontier 的新来源）。装载边界剪除被移除 cell 的来源后，规模 ≤ 当前会话来源数 |
| `bySource` | **降级为 live-only 索引**（3d） | 终态压缩后无需保留：已铸造判定走 frontier；live 检查只需现存条目 |
| `byRange` | **保留但收敛为 live-only** | 同代重复区间入队去重仍需要（防同一 pass 内重复候选）；终态压缩后由 frontier 判定，无需保留 |
| Quarantine 子类（Failed/Invalidated/Settled） | **评估折叠**为 `retired` + `unresolved bool` + `remintable bool` | 行为差异只剩三项：unresolved 计数、可压缩性、可重铸性；折叠前须有逐行为等价测试 |
| `ackBatch`（covered 集） | **保留** | 它是「一笔事务写了哪些 token」的物理事实（proof），不是特例；3d 只复核是否可收敛为 claim 批次前缀的简化形态 |
| `unresolvedCount` / `hasUnresolvedTerminalDelivery` | **保留** | A1-2c 已定稿为 proof 谓词 |

## 4. 测试与验收矩阵

- **不变量**：无重复铸造（含 prepend、压缩后重规划、生成漂移）、无丢行（覆盖度
  `history_resume_full_coverage_test.go` / `history_planning_budget_test.go` 口径）、
  顺序（token=allocation=append）、压缩等价、重铸例外（invalidated-clean）。
- **宽回归**：`cmd/aicli/ui` 全量、`cmd/aicli/commands` 全量、`-race`、专项 `-count`。
- **真机 e2e**：Windows Terminal exactly-once 72 行 + resume 页序（较早页进入 scrollback、
  无 3J、无丢行）。
- 每步：提交锚点 + 侦察/台账/设计文档同步。

## 5. 风险与开放项

1. **R1 两页同装前首规划的不可达性**：依赖装载契约（最新页先、首帧先规划）。
   3a 加守护用例与断言；若未来需支持，改「铸造按 allocation 序」而非 walk 序。
2. **R2 `remintable` 例外集的无界性**：只保留 invalidated-clean；需实测发生率与压缩策略
   （有界窗口），若出现增长则在 3c 一并给聚合表示。
3. **R3 顺序断言面**：现有测试中把「计划序」当「交付序」断言的条目，3b 时逐条核对改写
   （不改语义，只改断言对象）。
4. **R4 `sourceEnd` 在游标比较中的角色**：fragment 划分下同 `sourceStart` 不同 `sourceEnd`
   的键必须有序且无歧义；比较器定为
   `(cellID, revision, sourceStart, fragmentID, sourceEnd)`，由等价性测试钉住。
5. **R5 会话装载是 allocation 序定理的反例（2026-10-07 发现，阻塞 3c 删除面）**：
   `ReplaceTranscriptAction` 整体替换 transcript 时 ledger 与 frontier 均保留
   （append-only 语义要求），但新 transcript 的 cell 是**新来源**，其 key 可以落在
   旧 frontier 之下。此时 `mintedThrough(key)=true` 而该来源从未铸造：
   - 规划 skip 会把新 cell 误判为「已压缩终态」→ 不物化 → 内容永不交付；
   - `Enqueue` 守卫会拒绝其铸造；
   - 反向（装载同一会话）又确实需要旧 key 的阻断（防重复追加）。
   实测：`TestTerminalSessionExecutorLoadKeepsScrollbackAppendOnly`（装载未送达）与
   `TestSyncHistoryEffectCandidatesPrefixKeepsPendingTail`（截断前缀不铸前缀）在 3c
   判定下失败。**结论**：frontier 只能当加速器，不能当精确「已铸造」真相；删除
   `compactedTerminalSources` 必须先给出跨装载精确的覆盖表示，否则 3c 保持阻塞、
   墓碑保留。

   **R5 解法方向（已定稿，3c-redesign 按此实施）**：精确性不能来自 frontier；
   墓碑保留为「已铸造」真相，改为**按当前 transcript 有界化**——
   `ReplaceTranscriptAction`（非 active-only）时，把 `cellID ∉ nextTranscript`
   的压缩来源从墓碑集合中剪除（O(cells) 的稀有事件扫描）：
   - 装载不同会话：旧来源不在新 transcript → 墓碑释放，新来源可铸（修 R5 丢行）；
   - 重装同一会话：来源仍在 transcript → 墓碑保留（防重复追加，与旧行为一致）；
   - 上界：墓碑 ≤ 当前 transcript 的来源数（会话自身即有界），不再是「历史交付
     行数」的无界增长；frontier 退回纯加速器（3b 语义，双跑已证等价）。

   **R5 状态：已解（3c-redesign，见 §6）** —— `HistoryCommitLedger.
   pruneCompactedSourcesNotInTranscript` 在 `ReplaceTranscriptAction` 整体替换
   （门控：armed 装载/较早页插入，或 cell 集合收缩）时剪除被移除 cell 的来源；
   墓碑保留为精确阻断。装载不同会话 → 释放（新来源可铸）；重装同一会话 → 保留
   （防重复追加）。

## 6. 实施记录

- **3a（`1f375719`，2026-10-07）**：`HistoryCommitLedger` 增 `mintedFrontier` +
  `mintedFrontierValid` 纯镜像（`Enqueue` 单调推进、`Clone` 保真、`mintedThrough` 判定）；
  `historyCommitSourceKeyLess` 定义 allocation 序（cellID → revision → sourceStart →
  fragmentID → sourceEnd）。等价性用例
  `TestMintedFrontierMirrorsMintedSourcesAcrossResumePrepend`（resume 先装 + deferred
  prepend + 交付后重规划：候选 `mintedThrough ≡ bySource/压缩墓碑判定`，且较早页确实被铸造）
  与 `TestMintedFrontierCloneAndMonotonicity` 绿。全量回归：ui 127.8s、commands 186.3s 绿。
  **结论**：allocation 序定理在关键流程上经验成立；R4（`sourceEnd` 序）由用例钉住；
  3b 可按计划将规划 skip 切换到 `mintedThrough` + live 检查（双跑对照后删旧路径）。

- **3b（`b66d3367`，2026-10-07）**：三个来源判定切换 mint 游标：
  - `hasTerminalRecordForSource`：加 `mintedThrough` 早出（等价）；
  - `hasSettledRecordForSource`：加游标早出 + **已压缩终态来源 → settled=true**
    （旧实现先白物化 payload、随后在入队处以墓碑跳过；分歧仅发生在 bySource 为空、
    无 live 条目可被 reconcile 时，投递语义不变）；
  - `retainedQueuedCommitForSource`：加游标早出（等价）。
  等价性双跑：`TestSourceJudgmentsMatchLegacyAcrossResumePrepend`（生产顺序 + 交付 +
  压缩，全 key 三判定对照，且断言压缩分歧分支确实被触发）、
  `TestCompactedTerminalSourceSkipsMaterializationAndMinting`、
  `TestPrunedRemintableSourceStillRequiresMaterialization`（零写作废来源保持
  unsettled，重规划仍可再铸，不丢行）绿。全量回归：专项族 119.8s、ui 135.9s、
  commands 189.9s 绿。**结论**：3c 可删 `compactedTerminalSources` 的消费面
  （`hasSettled`/`hasTerminal` 已由游标承担主判定），并将 bySource 降级为 live-only。

- **3c 尝试（2026-10-07，未落地，代码已回退到 `d23bc10a`）**：按 §2 实现「删
  `compactedTerminalSources` → 由 frontier + remintable 例外承担阻断/结算判定 +
  `Enqueue` 守卫改走同一判据」，并补 3c 等价/内存用例（交付型压缩 5000 条后例外集
  为空、`bySource` live-only、压缩阻断仍生效）。专项族与部分回归绿，但**全量回归
  暴露 §5 R5 反例**：会话装载与截断前缀两个生产流用例失败，证明 frontier 覆盖不跨
  transcript 代际。已整体回退（`git checkout HEAD --` 7 个文件），复跑装载/前缀/
  3b 等价族绿。**下一步**：先解 R5（epoch 域身份或有界精确集），再重启 3c；期间
  tombstone 保留（成本 = 交付来源数 × 小键，由 P2-1 窗口压缩的 live 上界之外独立
  增长，A3 台账继续跟踪）。
- **3c-redesign（`17c10e53`，2026-10-07）**：按 R5 定稿落地——`HistoryCommitLedger.
  pruneCompactedSourcesNotInTranscript`（装载边界按新 transcript 的 cell 集合剪除被
  移除来源的压缩墓碑）+ `ReplaceTranscriptAction` 门控调用（非 active-only 且
  armed 装载/较早页插入，或 cell 集合收缩；纯追加不扫，空墓碑零成本）。新增
  `history_ledger_transcript_prune_test.go` 三用例：装载不同会话释放墓碑且低于旧
  frontier 的新来源可铸（R5 回归点）、重装同一会话保留阻断（Enqueue 仍拒绝重复
  铸造）、reducer 装载路径按 cell 集合双向剪枝/保留。生产流
  `TestTerminalSessionExecutorLoadKeepsScrollbackAppendOnly` 与
  `TestSyncHistoryEffectCandidatesPrefixKeepsPendingTail` 保持绿；宽回归 ui（110s）
  + commands（196s）exit 0。frontier 维持 3b 纯加速器语义，不再承担跨代际
  「已铸造」真相；墓碑上界 = 当前 transcript 来源数。
