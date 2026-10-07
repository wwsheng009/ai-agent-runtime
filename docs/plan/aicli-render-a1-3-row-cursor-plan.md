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
| **3c** | 压缩改造：`compactResolvedIfLarge` 更新 frontier/例外集；删除 `compactedTerminalSources` | 无 | 压缩等价测试 + 长会话载荷/内存核算 |
| **3d** | 删除面落地（按 §3 结论）：Quarantine 子类折叠评估、`bySource` 降级、`byRange` 收敛、ackBatch 形态复核 | 视评估 | 逐项迁移记录 + fail-closed 语义不回退 |
| **3e** | 验收：宽回归（ui + commands + `-race`）+ 真机 e2e（exactly-once 72 行 + resume 页序） | — | 全绿 + 台账/设计文档同步 |

## 3. 删除面评估（前置结论，3d 实施时复核）

| 对象 | 判定 | 依据 / 前提 |
|---|---|---|
| `compactedTerminalSources`（tombstone） | **可删**（3c 后） | 「已铸造」由 frontier 承担；「可重铸」由 `remintable` 承担；唯一消费者是 `Enqueue` 去重与 `retainedQueuedCommitForSource`，均已切换 |
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
