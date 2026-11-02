# 11. Fact Ledger 收敛决策记录

> 状态：**决策已执行，本层停止继续投入（dormant-but-correct）**
> 日期：2026-10-28
> 范围：`backend/internal/contextmgr`、`backend/internal/factledger`、`backend/internal/artifact`、`backend/internal/compactruntime`、`backend/internal/agent`
> 证据：本机生产库 `artifacts.sqlite` / `session_history.sqlite` 实测，非推演

## 1. 一句话结论

fact ledger 的原始定位（“压缩之后仍然活着的已验证事实”）是合理的，但实现把它做成了
**每轮重复注入的工具执行日志**。三轮改造后它已收窄为“压缩边界的声明式残留层”：正确、
廉价、默认静默。**基于实测产出量，继续投入（失效通道、TTL 修剪、重复动作率度量）期望价值为负，停止。**

## 2. 问题是怎么被发现的

系统提示词里会被压入一段自述权威的事实块：

```text
Verified fact ledger (authoritative over compacted prose):
- [execution] view succeeded {"output":"1: package foldertrust 2: 3: import ( ...
  [evidence=event:trace_0ce3027c-ee32-4cd9-ad92-a01449d92107:tool:view:1]
```

对照两份参考实现后确认方向性错误（证据见第 6 节）：

| | 过程产物（工具输出） | 结论 | 跨轮持久注入 |
| --- | --- | --- | --- |
| codex | 压缩时整段剥离，`build_compacted_history` 新历史只含 初始上下文 + 预算内用户消息 + 摘要；仅 `client_authored` 且无匹配 call 的输出可保留，且整条取舍 | LLM 摘要（4 类：进度与决策 / 约束与偏好 / 剩余工作 / 关键引用） | **世界状态增量**（`render_diff`、RFC 7386 `merge_patch_from`），19 个 State 全部是声明式，无一条记录工具结果 |
| deepseek-harness | `compaction-tool-result-pruner` 就地修剪（默认 >8192 码点 → head 4096 + “middle pruned” + tail 1024），仅在压力确认后运行、不发起模型调用；原文留会话日志可回读 | LLM 摘要（`summarizer.ts`：decisions+rationale / constraints / preferences / data needed to continue） | 声明式指令分层（AGENTS.md，64KiB 预算）；**全仓无 fact ledger 机制**（`ledger` 只命中 UI 槽位注册） |
| **AAR（改造前）** | 每轮抄一份塞进 prompt | 摘要 **+ 第二套账本** | 账本 + observation + recall + ledger 四套投影同一份数据 |

仓库自己的文档其实早已给出正确答案，只是没被执行：
`docs/agent-memory/02-current-runtime-gap-analysis.md:98`
“定位：**已验证事实**。Agent Memory v2 应引用 fact ID / artifact ID，**而不是把同样的内容复制成新记忆**。”

## 3. 已实施的三轮收敛

### 3.1 降噪（第一轮，后被更彻底的方案取代其部分手段）

去 JSON 外壳、正文移除不可解引用的 `[evidence=...]`、recency 主序、失败恢复失效接线、
语义 kind 分类。实测最大 6 会话 −69%。

### 3.2 过程产物/结论切分（第二轮，决定性）

判据：**模型能从别处免费拿到的，就不该发。**

- 写侧：删除 `RecordObservations` 与 `loop.go` 调用点，工具观测不再落库；`failure` 保留为声明式 pitfall 类。
- 读侧：`injectableFactKinds` 白名单（`constraint` / `decision` / `remaining_work` / `open_question` / `failure` / `goal` / `completed_action`）；
  `execution`/`test_result`/`state_change`/`runtime`/`workspace` **在读取侧过滤，无需数据迁移**。
- 门控：`factLedgerSuppressed` 引入 `compactionPressure || transcriptHasCompactionBoundary(scopedHistory)`。
  此前它完全不引用 `compactionPressure`，而 `docs/compact/README.md:125` 声明生产请求路径默认关闭
  prompt-view compaction —— 即**账本在它自己声称成立的理由从未成立的路径上每轮都发**。
- 增量：`announcedFactIDs(input.History)` 跳过 transcript 已宣告的 fact id，对齐 codex 的 diff 语义。
- 唯一写入点：`Manager.PersistCompactionFacts`，由 `compactruntime` 的 local（整段被替换历史）与
  remote（`droppedCompactionInput` 按消息身份做差集）两条压缩边界调用。
- 标题撤下 blanket authority，改为 `Session facts carried across compaction (constraints, decisions, open work; newest first):`；
  CLI / Web 隐藏列表升级为识别**全部三代标题**（`contextmgr.LegacyFactLedgerHeaders()` ↔ `history-artifacts.ts`）。

### 3.3 可达性与自我污染（第三轮，由真实库暴露）

- **kind 过滤下推到 SQL**（`artifact.Store.LoadMemoryEntriesByContentKind`，`json_extract(content_json,'$.kind')`）。
  此前读窗口按“行数”计，`ListActive` 末尾还会再截断到 `LedgerLoadLimit`，导致长期会话里稀疏的
  声明式事实**永远读不到**（实测 rank 387 / 361）。
- **只采集用户断言**（`ledgerHarvestableMessage`）。agent 自己的叙述本就是压缩摘要被要求承载的内容，
  再存一遍等于把它的话换个标签还给它——真实库里确实出现过 `- [rule] assistant requires: Let me do final due-diligence...`。
- **读侧三道兜底**，用于拦掉旧 writer 已经写进去的行：
  `narratedFactSubject`（Subject 是发言角色）、`selfIngestedFactValue`（内容本身是账本/检查点渲染——
  账本在吃自己）、对 `failure` 行复用的 `reportsACleanResult`（“848 passed / 0 failed” 被词汇启发式判成 pitfall）。

## 4. 实测证据

### 4.1 改造前

| 指标 | 值 |
| --- | --- |
| 单个账本块（真实存储的 prompt 行） | 中位 **3,695** 字符，最大 **31,851**；行数中位 12，最大 252 |
| 带不可解引用 `[evidence=…]` 的行 | **67%**（3,528 / 5,301） |
| 存量 `structured_fact` | **≈181,500** 行 / 669 会话 |
| kind 分布 | `execution` 178,838 · `workspace` 2,547 · `test_result` 49 · `failure` 27 · `constraint` 3 · `open_question` 2 |
| 首轮审计（改造前快照）的语义层产出 | **0 条**：当时 kind 只有 `execution`/`workspace`/`test_result`，即全部来自工具观测 |

说明上表两行如何并存：kind 分布取自本次会话结束时的快照，比首轮多了 199 条语义行（只分布在 3 个会话，
`created_at` 全部为 2026-10-28）。这 199 条由**legacy `contextmgr` 压缩路径**
（`saveLedgerCheckpointSegment` → `persistStructuredFacts`，agent loop 以 `EnablePromptCompaction: true` 走到）
在本会话期间写入；而 chat actor 实际使用的 `compactruntime` 路径当时**完全不调用** `factledger`，
这正是 §3.2 把写入点接到压缩边界的原因。

最大 6 个会话的注入体积：改造前 **17,436** 字符 → 三轮改造后 **0** 字符。

### 4.2 可达性（第三轮的关键发现）

读窗口按行数计时，声明式事实排在过程产物之后即不可达：

| 会话 | fact 行数 | 最新声明式行 rank | 截断窗口 | 下推前 | 下推后可达行数 |
| --- | --- | --- | --- | --- | --- |
| `session_20261026231753_mxou6C26` | 4,075 | **387** | 12 | 不可达 | 3 行 / 669 字符（随后被 §4.3 的读侧兜底拦下） |
| `session_20261027193811_MpnW9KBW` | 1,378 | **361** | 12 | 不可达 | 7 行 / 1,584 字符（同上） |

复测方式：对生产库做**一致性只读副本**（`sqlite3.backup`，412.6 MB），用真实读路径
`contextmgr.Manager.Build()` 渲染，而不是复刻规则。原库未以写方式打开。

### 4.3 可达之后暴露的真相（读侧漏斗实测）

对全库 199 条声明式 kind 行施加实现中的读侧过滤（顺序与 `selectRenderableFacts` 一致）：

| 阶段 | 拦下 | 余量 | 被拦下的内容 |
| --- | --- | --- | --- |
| 声明式 kind | — | 199（分布在 3 个会话） | — |
| 空值 / 账本自引 / 宿主控制面提示 | 46 | 153 | 账本引用自己、`[子任务与后台作业进展]`、`[supervision]` 文本 |
| `narratedFactSubject` | 153 | **0** | agent/developer 自己的叙述被当作规则、待办、pitfall |
| `reportsACleanResult` | 0 | **0** | 已无剩余行 |
| **最终可注入** | | **0** | |

修复前该漏斗的终态是 2 行，且这 2 行同样不是事实：

```text
[failure] subject=user | [子任务与后台作业进展] 有 20 项待处理。 - 子任务会话 session_2026…：已结束。child session failed
[failure] subject=user | [子任务与后台作业进展] 有 12 项待处理，另有 5 项已结束。 - 后台作业 job_55d0…：已结束。
```

它们是**宿主提交的控制面提醒**（`supervision` digest）以普通 user 轮次进入 transcript，
因此当时通过了“仅用户断言”这一关，再被词表判成 pitfall。该泄漏现已在写侧与读侧同时封堵（§8 末条）。

即：**全库 669 个会话、≈181,500 行中，真正属于用户的不可再生断言为 0 条。**
这不是“机制失效”，而是**这套机制从未产生过它声称要产生的东西**——旧 writer 只记录工具调用与宿主提醒。

### 4.4 稳态产出量（停止投入的依据）

| 指标 | 值 | 来源 |
| --- | --- | --- |
| 历史上发生过压缩边界的会话 | **115**（820 次 checkpoint） | `checkpoints` 表 `reason=history_window_summary_segment` |
| 真实用户消息能通过采集判据的比例 | **9.28%**（250 / 2,694，跨 204 会话；**上界**：未要求落在被压缩段内、未排除 staged 行） | `session_messages` 扫描 |
| 全库经完整读侧过滤后真正可注入的行 | **2**（且两条均为控制面提醒误判 → 真实值 **0**） | §4.3 漏斗 |
| 推算稳态产出 | 目前证据下接近 **0 条 / 压缩会话**；上界 ≤1 条 | 上行与 115 会话相除 |

即：即使一切按设计工作，这一层今后的产出也是“每个发生过压缩的会话 ≤1 行、约 120 字符”量级，
而目前已观测到的部分全为误报。

## 5. 停止投入的理由

| 待办 | 期望价值 | 代价 | 判定 |
| --- | --- | --- | --- |
| 用户约束失效通道 | 基数实测为 **0 条真实用户断言**（上界 ≤1 条/压缩会话），最坏后果是一行陈旧提示；而它“优先于 prose”的权限**已被取消**，风险主要部分已修 | 给一个近乎空的存储建生命周期 + 双写一致性 | **负** |
| 重复动作率度量 | 干预强度 ~120 字符 / ~1 行，低于任何重做率指标的噪声 | 跨 loop / telemetry / storage 的插桩 | **负** |
| 存量 18.1 万行 TTL 修剪 | 读侧已完全不消费，只占磁盘 | 触碰仓库既有 SQLite 在线维护护栏测试 | **纯风险** |

## 6. 参考实现证据（本节结论的来源，勿重复调研）

- codex：`codex-rs/prompts/templates/compact/prompt.md:3-7`（只要求 4 类结论）；
  `prompts/templates/compact/summary_prefix.md:1`（“avoid duplicating work” 靠 tool **state** 而非结果账本）；
  `core/src/compact.rs:664`（“we **stripped** it from the pre-compaction history”）、`compact.rs:677-703`
  （新历史 = 初始上下文 + 预算内用户消息 + 摘要）；`core/src/compact_retained_tool_outputs.rs:1-19`；
  `core/src/context/world_state/mod.rs`（19 个声明式 State；`render_diff:163`、`merge_patch_from:413-414`、
  `render_full:485` vs `render_history_diff:490`、`WorldStateHash:360`）。
- deepseek-harness：`packages/compaction/{compaction-tool-result-pruner,compaction-image-offload}/`
  （pruner 默认 `thresholdChars=8192 → head 4096 + tail 1024`，仅压力后运行、零模型调用，原文留日志可回读）；
  `compaction-basic/src/summarizer.ts`；`packages/` 全域 `ledger` 检索只命中 `client/ui-slots` 的槽位注册账本。
- AAR 内已存在的正确机制（本层不该重复造的轮子）：
  `internal/output/tool_result_content.go:18-26`（`Full raw output artifact_id:` 指针）
  + `internal/toolkit/tools/artifact_read.go`（按需分页回读）= spill/preview；
  `internal/toolkit/tools/read_ledger.go:160/275/341`（按内容哈希在工具层拦重复读，文件变更即失效）。

## 7. 重启条件（满足任一才继续，且按此顺序）

1. **接入新的不可再生类别**：跨会话项目记忆（L2）若落地，fact ledger 会是它的进程内投影，
   失效通道从“可选”变“必需”。
2. **约束密度上升**：若“每压缩会话声明式事实数”从接近 0 升到 >5，token 与陈旧性同时开始有意义。
3. **出现可复现反例**：模型重做了压缩前已完成的工作，且根因指向缺失声明式残留。

### 复测配方（不要重新推导）

```python
# 1) 每压缩会话的声明式事实数（重启条件 2）
#    artifacts.sqlite 只读打开：file:...?mode=ro
SELECT COUNT(*) FROM memory_entries
 WHERE kind='structured_fact'
   AND json_extract(content_json,'$.kind') IN
       ('constraint','decision','remaining_work','open_question','failure','goal','completed_action');
# 分母：SELECT COUNT(DISTINCT session_id) FROM checkpoints
#        WHERE reason='history_window_summary_segment';

# 2) 用户消息判据通过率（上界，无需新代码）
#    session_history.sqlite: session_messages WHERE role='user'，
#    对 content 施加 contextmgr/compact.go 的 looksLike* + reportsACleanResult 同义谓词。

# 3) 任何 prompt 侧对比必须走真实读路径：
#    contextmgr.NewManager(DefaultBudget(), store).Build(ctx, BuildInput{...})
#    取 context_stage=fact_ledger 的消息；且对生产库先做 sqlite3.backup 一致性副本，原库只读。
```

## 8. 明确未做（非遗漏，是决定）

- 存量 18.1 万行过程产物行的物理清理（无 TTL / 无 prune）。
- 声明式事实的失效与 supersede：`factledger.Invalidate` 已实现并单测，生产调用点为 0
  （`fact_invalidation` 表行数全库为 0）。
- shared-chat 路径 `compactruntime.New(llmRuntime, nil)`（`chat_core.go:519/706`）：
  `summaryCheckpointStore(nil)` 返回 nil，该路径改造前就不落 checkpoint、改造后也不写事实，行为未变（既有缺口）。
- 端到端用例（真跑会话→压缩→断言 prompt 内容）仅有单元级覆盖。
- 已知泄漏（§4.3）——宿主控制面提醒以普通 user 轮次进入 transcript 并被 harvest 成"用户断言"——
  **已修**：`types.IsControlPlanePromptText` 以 `types.SupervisionDigestPromptHeader` /
  `types.SupervisionPromptTag` 为单一事实源，`ledgerHarvestableMessage` 与
  `types.MetadataKeyTransientPrompt` 一并拒绝；`supervision` 的 digest 渲染直接引用该常量
  （漂移由编译器拦截），`AutoWakePrompt` / `ResumePrompt` 的前缀由
  `TestHostPromptsStillCarryTheSharedControlPlaneTag` 拦截。修完后全库最终可注入行数 = **0**。
- `reportsACleanResult` 与 `looksLike*` 仍是词汇启发式；收窄到“仅用户断言”后误判影响面已大幅缩小，但未根除。

## 9. 代码与文档索引

| 主题 | 位置 |
| --- | --- |
| 账本写入 | `contextmgr/facts.go`：`PersistCompactionFacts`、`ledgerHarvestableMessage` |
| 账本读取与选择 | `contextmgr/facts.go`：`buildFactLedgerMessage`、`selectRenderableFacts`、`announcedFactIDs`、`injectableFactKinds`、`declarativeFactKinds` |
| 读侧兜底 | `contextmgr/facts.go`：`narratedFactSubject`、`selfIngestedFactValue`；`contextmgr/compact.go`：`reportsACleanResult`、`looksLikeFailure`、`transcriptHasCompactionBoundary` |
| 宿主控制面提示识别（单一事实源） | `types/context_scope.go`：`SupervisionDigestPromptHeader`、`SupervisionPromptTag`、`IsControlPlanePromptText`；生产方 `supervision/digest.go`、`supervision/wake_consumer.go`、`supervision/resume_context.go`；消费方 `contextmgr/facts.go:ledgerHarvestableMessage` 与 `selectRenderableFacts` |
| 回归锁 | `contextmgr/facts_test.go`、`contextmgr/facts_quality_test.go`、`compactruntime/remote_facts_test.go`、`types/context_scope_control_plane_test.go`、`supervision/control_plane_prompt_test.go` |
| 标题与历史标题兼容 | `contextmgr/fact_ledger_header.go` ↔ `frontend/src/lib/thread-state/history-artifacts.ts`、`cmd/aicli/commands/chat_history.go` |
| kind 下推 | `artifact/store.go`：`LoadMemoryEntriesByContentKind`、`LoadMemoryEntriesRecent`；`factledger/query.go`：`loadFacts`、`recencyAwareStore`、`factKindAwareStore`、`matchesQuery` |
| 边界调用点 | `compactruntime/local.go:saveSummaryCheckpoint`、`compactruntime/remote.go:persistDeclarativeResidue`+`droppedCompactionInput` |
| 相关规范 | `docs/compact/README.md`「Fact ledger 的边界」、`backend/docs/architecture/prompt-cache-layout.md`「Fact ledger policy」 |
