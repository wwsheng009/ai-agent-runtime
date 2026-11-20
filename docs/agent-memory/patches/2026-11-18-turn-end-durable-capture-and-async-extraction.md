# Turn-End 持久采集与异步记忆抽取方案

> 状态：**方案提案（待评审，未实施）**<br>
> 日期：2026-11-18<br>
> 范围：Agent Memory v2 写入触发、Turn Digest Outbox、Session 生命周期、维护任务所有权、恢复与验收<br>
> 关系：本文件是 `03/04/05/06/13/14` 的变更提案。评审通过后，必须把结论回写到基线文档；不直接替代它们。
>
> 增量实施记录（不改变上述提案状态）：当前代码已补入 receipt-backed
> turn capture、digest/job 同事务物化、generation-aware purge fence、
> 原子 extraction output receipt、generation binding 缺失拒绝、通用 candidate
> writer purge fence 和本轮新增的 `RecordTurnDigest` 写入 fence。当前 purge 对 generation-qualified
> session/project 数据按精确代数清理；无法证明 incarnation 的 generationless
> writer/job 在 fence 后 fail closed，不能据此宣称 legacy payload 已安全
> 删除；window 写入/状态转换尚无 generation-qualified fence，generationless
> 或混合代数 window、未版本化 recall trace/session mode 也保留待后续迁移。
> 已完成代数的 purge 重试不会重新清理新 incarnation 的数据。
> 相关定向单测已覆盖；hard-kill、跨 SQLite 崩溃恢复、全量性能/并发门禁和
> shadow 运营验收仍未完成。

## 1. 结论

当前设计把“Turn 结束后的原始采集”和“记忆抽取的异步消费”拆成了两个相互独立、没有可靠交接保证的阶段：

```text
turn 结束
  → 写 Turn Digest（被动 outbox）
  → 等待 session-end / idle / startup
  → 才调用 LLM 抽取
```

问题不在于“Turn 没有采集”，而在于 **Turn 结束没有建立可恢复的 durable handoff，也没有立即物化可执行任务的补偿协议**。因此，只要会话保持 `active`、进程提前退出、后台 ticker 尚未运行，或者 canonical transcript 先被 session TTL 清理，digest 就可能永久停在 `pending`，且没有可靠 source 可供恢复。

目标设计应改成以下可靠性边界。这里的 durable handoff 是**可恢复的 capture receipt**，并不意味着跨数据库的 digest 与 job 在 turn 返回前已经同时可见：

```text
turn 终态
  → canonical transcript 与 capture receipt 在 canonical store 的同一事务提交
  → turn 返回（不调用 LLM）

receipt materializer（至少一次、幂等）
  → 在 memory store 的单一事务内写 digest + phase1 job
  → 提交后回写 canonical receipt 为 materialized

phase1 worker
  → claim job
  → 读取并校验 receipt 指定的 turn range
  → 异步执行 LLM 抽取
  → 在一个 memory store 事务内写 candidates + 成功 receipt + job/digest 终态

session-end
  → 仅做 flush / drain

idle / startup
  → materialize 未确认的 receipt，重试、失联恢复和 dead-letter 巡检
```

核心原则：

- **turn 返回前必须有 durable handoff receipt；receipt 与 canonical turn commit 原子提交**；
- **digest/job 物化是跨库的至少一次交接，不宣称分布式原子事务；依赖唯一键和 receipt 补偿**；
- **LLM 抽取与 Consolidation 保持异步**；
- **session-end/idle/startup 不再是正常运行时的唯一消费者**；
- **Turn range 是持久化幂等单位，不再以整段 session transcript hash 作为抽取单位**；
- **如果 canonical transcript 与 receipt 无法在同一个可靠事务边界提交，则不得宣称满足此方案的 durable handoff 保证；该能力必须先由存储层提供，或将 turn 标记为 capture-degraded 并由可扫描的 canonical turn journal 补偿**。

### 1.1 本轮审查后的强制修订

本轮审查将以下约束提升为实现前必须冻结的协议，而不是实现阶段的可选细节：

- `session_generation` 表示可复用 session ID 的 canonical incarnation，必须在 session 创建时分配，并跨删除/重建单调递增；它不能与一次 purge 操作的 `purge_generation` 混用；
- receipt、turn-range digest、session-scoped job、candidate provenance 和 per-turn output receipt 必须携带同一个 `session_generation`；memory tombstone 同时记录被 fence 的 incarnation 和 purge 操作代数；
- purge 的线性化点是 memory fence 提交。memory fence 之前已提交的派生记录必须由 purge 清理或标记失效；之后迟到的 worker commit 一律拒绝。`drain` 只表示等待/取消 in-flight 工作，不表示允许旧 worker 在 fence 后提交候选；
- digest 与 job 的正常物化必须是同一 memory SQLite 事务；“digest 已写、job 未写”或相反只能作为损坏/迁移修复状态，不能成为可 claim 的正常状态；
- session purge 的删除契约必须分别定义 memory 内容删除、summary 重建和最小审计保留，不能只删除 digest/window 就宣称 session memory 已清除；
- canonical revision、legacy consumer、job key 编码、lease 时钟和 `capture_degraded` 的用户可见语义必须在实现前固定并纳入验收。

## 2. 当前问题总结

### 2.1 现有写入链路

现有流程是：

```text
SessionActor terminal boundary
  → recordTurnDigest()
  → memory_turn_digests(status=pending)
  → session-end / idle ticker / startup recovery
  → RunSessionEnd / RunPendingTurnDigests
  → Phase 1 candidate
  → Phase 2 active summary
  → contextmgr 注入
```

其中 `recordTurnDigest` 只写 digest，不创建可执行 job；真正进入抽取还依赖后续触发。

### 2.2 失效链

实际 TUI 使用场景通常是：

```text
用户持续对话
  → session 长期保持 active
  → 用户关闭终端或进程退出
  → session 仍然是 active
  → 下一次启动
  → startup recovery 只接受 terminal session
  → active session 的 pending digest 被跳过
```

同时：

- `idle` 首次 ticker 默认等待 30 分钟；
- active session 还需要满足 pending 数阈值或 6 小时空闲；
- short-lived CLI 进程通常活不到下一次 ticker；
- `runtime-server` 不是必然长期运行，且当前只绑定一个 workspace；
- session TTL 到期后会被独立清理，memory digest 仍可能保持 pending。

### 2.3 当前运行证据

本次检查时的运行快照（仅表示检查时所选 workspace/database 的观测值，不是全局或长期统计）：

| 项目 | 当前值 |
|---|---:|
| `memory_entries` | 0 |
| `memory_summaries` | 0 |
| `memory_turn_digests` | 5，全部 `pending` |
| `memory_recall_traces` | 150，全部 `candidate_count=0`、`injected=0` |
| 相关 session 状态 | 全部为 `active` |

其中 `session_20261111194152_F9dt5thc` 在检查时仍有一条 pending digest，但其 `expires_at` 已到达；如果 session cleanup 先于 memory drain 执行，该 digest 将失去 canonical source。

这不是单纯配置错误，而是 **Turn 写入与 Turn 消费之间缺少 durable handoff**。该快照用于说明问题背景；实现验收必须使用可重复的测试数据与指标，不以此处计数作为基线或成功标准。

### 2.4 缺口清单

| 优先级 | 缺口 | 结果 |
|---|---|---|
| P0 | Turn 结束只写被动 digest，没有立即创建抽取 job | 数据只能等后续偶发触发 |
| P0 | active session 没有定义可靠的 liveness/orphaned 状态 | 进程崩溃后永远无法恢复 |
| P0 | startup recovery 只处理 terminal session | 最常见的崩溃状态被排除 |
| P0 | 没有维护者所有权、heartbeat 或 leader lease | 无进程时无人消费，多进程时无明确责任 |
| P0 | Session TTL 与 pending digest 未协调 | canonical 删除后 digest 变成死信 |
| P1 | 抽取单位使用整段 session/hash，不是 turn range | 会话增长会使旧 job 失效，并可能漏掉早期 turn |
| P1 | `trigger` 同时表达正常触发与恢复路径 | 配置语义不完整，`session_end` 组合可能看似合法但不可靠 |
| P1 | `minIdleHours` 被写成 Phase 1 的全局筛选条件 | 正常 session-end 也可能因为 idle 不足而被拒绝，session-end 实际退化为 idle |
| P1 | Digest 缺少完整状态机和 deferred 复活路径 | `processing/deferred/failed` 可能永久停留，无法重试、清理或终止 |
| P1 | pending 按最老 digest 排序，坏 session 会占据扫描队首 | 缺失 session/transcript 的毒化行会饿死后面的健康会话 |
| P1 | 验收测试在 hard-kill 前预先把 session 设为 `closed` | 测试没有覆盖真实 crash 的 `active` 状态 |
| P1 | 缺少手动 drain/extract 的 CLI 路径 | 用户无法在 TUI 内补救积压 |
| P1 | 维护循环为单 goroutine，且所有维护特性关闭时被整体 no-op | startup recovery 可能被跳过；单个阶段卡住会冻结整个维护面 |
| P2 | L0/L1/L2 在不同文档中表示三套层级 | Memory horizon、digest rollup、tiered brief 语义互相冲突 |
| P2 | 配置依赖没有交叉校验 | 可能出现“已开启但永远不注入”的组合 |

其中 L0/L1/L2 目前至少有三套含义：

1. `09 §2.1`：L0=turn working set，L1=session short-term，L2=project durable；
2. `04/patch`：L0=turn digest，L1=window summary，L2=project summary；
3. `03/06` 的 tiered brief：L0/L1/L2 表示 256B/500B/正文的渐进披露层级。

这会让 `L0 retention`、`L1 purge`、`L2 summary` 和 `tiered brief budget` 指向不同对象。建议把 digest rollup 改名为 `D0/D1/D2` 或直接使用 `digest/window/project-summary`，把 L0-L4 只保留给 memory horizon。

## 3. 目标

### 3.1 必须保证

1. 一个被标记为 `capture_committed` 的 turn 返回前，canonical transcript 与 durable capture receipt 已在同一可靠事务边界提交；**不要求跨库的 digest/job 已在返回前物化**；
2. turn 后立刻崩溃，receipt 仍可由后续 materializer 幂等地生成 digest/job；若 receipt 事务本身失败，不能将该 turn 标记为 `capture_committed`，必须按 §3.1 明确返回/展示 `capture_failed` 或具备 durable journal 时的 `capture_degraded`；
3. 会话仍为 active 时，也可以安全抽取已经完成的 turn range；
4. session TTL 删除之前，必须先建立 deletion fence、禁止新提交/claim、完成有界 drain；未能 drain 时按 purge 协议处理，而不是先删除 canonical 再尝试读取；
5. 同一 `(project_key, session_id, session_generation, turn_id, source_revision, range_hash)` 的重复投递不会重复提交 candidate；
6. 抽取不阻塞 turn 请求，不在 turn 路径同步调用 LLM；
7. turn 路径只允许有界的本地持久化 handoff；数据库持久化失败不能被伪装为成功 capture；
8. 所有失败都可观测、可重试、可终止，不允许无限 pending；
9. 现有读取/注入、prompt cache 和权限边界不发生回归。

这里的“成功 turn”需同时区分对话输出与持久化提交：memory store 暂时不可用但 canonical receipt 已提交时，turn 仍是 `capture_committed`，状态显示 `materialization_pending`，由后续补偿恢复。若 canonical transcript+receipt 事务失败，则不得报告为已提交；只有实现了另一条与 canonical terminal marker 原子提交、可扫描且保留同样 range/hash 的 turn journal，才允许返回对话但标记 `capture_degraded`。既没有 receipt 也没有 journal 时不得宣称满足本节保证。

对话输出与持久化结果必须分开向调用方报告：canonical commit 成功则为 `turn_completed + capture_committed`；receipt/journal 均未能持久化时不能回滚或伪称对话模型未执行，但必须报告 `turn_completed + capture_failed`，明确本轮未保证可恢复，并提供同一 `turn_id` 的持久化重试/人工恢复入口。重试只重试 canonical terminal persistence，不得因此再次调用模型或创建新的 turn 身份。只有存在可扫描 durable journal 时才使用 `capture_degraded`。

### 3.2 非目标

- 不在 turn 路径同步执行 LLM；
- 不要求每个 turn 都立即产生 active summary；
- 不改变现有 memory read/injection 的 stable/recall 结构；
- 不把完整 transcript 复制为第二事实源；
- 不引入 embedding、云端同步或自动权限提升。

## 4. 目标架构

### 4.1 主链路

```text
[Turn Terminal]
  canonical transcript + capture receipt committed atomically
        │
        ▼
  turn returns (no LLM; receipt is the durable handoff)
        │
        ▼
[receipt materializer]
  memory.db transaction
  ├─ memory_turn_digests (queued, exact range, revision/hash)
  └─ memory_jobs (phase1_extract/v2, available_at=now)
        │
        ▼
[memory worker]
  claim job by lease
        │
        ├─ read exact turn range from canonical
        ├─ LLM extract candidates
        ├─ validate/redact/source
        ├─ insert candidates + sources + events
        └─ mark job succeeded + digest done
        │
        ▼
[phase2 consolidation]
  promote candidate / summary revision (异步)
        │
        ▼
[contextmgr next turn]
  inject stable + recall memory
```

### 4.2 触发分层

建议将“正常触发”和“恢复触发”从单一枚举中拆开。

| 类型 | 触发 | 作用 |
|---|---|---|
| 主触发 | `turn_end` | 每个完成 turn 都采集并创建 durable receipt；由 materializer 至少一次物化为 job |
| 会话边界 | `session_end` | 只做 flush、drain 和最终确认 |
| 后台恢复 | `idle` | 周期 retry、处理积压和长期 pending |
| 启动恢复 | `startup` | 处理 orphaned/terminal/queued 任务 |
| 手动 | `manual` | 用户或管理端显式补抽取 |

推荐配置语义：

```yaml
memory:
  capture:
    enabled: true            # 只控制 turn-end receipt
    maxHandoffDelay: 2s

  enqueue:
    enabled: true            # receipt → digest/job materialization
    maxMaterializeDelay: 30s

  generation:
    enabled: true            # 控制 worker 是否调用 LLM
    trigger: turn_end        # 不改变 receipt 的独立语义

  recovery:
    enabled: true
    onStartup: true
    onIdle: true
    onSessionEnd: true
    orphanedGrace: 10m
    retryPolicy: classified
```

如果为了兼容暂不增加 `turn_end` 枚举，至少必须做到：

- `capture.enabled=true` 时，turn-end receipt 不依赖 session-end；
- `enqueue.enabled=true` 时，receipt 由 materializer 至少一次物化为 digest/job；
- `trigger=session_end` 只影响自动 worker/legacy session 抽取，不允许改变已提交 receipt 的恢复语义；
- 配置加载时对“短生命周期 CLI + session_end only”给出显式健康警告。

各触发器的 eligibility 必须分别定义，不能复用同一套 idle 过滤：

| 触发 | capture/materialize/extract eligibility |
|---|---|
| `turn_end` | `capture.enabled` 且 session mode 允许时写 receipt；`enqueue.enabled` 决定是否物化；只有 generation policy 允许时 worker 才 claim |
| `session_end` | bounded flush/drain；可消费已提交且可读的 legacy/v2 range，不受 `minIdleHours` 阻止；新配置默认不以它作为唯一入口 |
| `idle` | 仅 legacy idle 策略受 `minIdleHours` 限制；v2 jobs 也受显式 trigger policy 约束，不由 `available_at` 擅自覆盖用户配置 |
| `startup` | materialize durable receipt、恢复过期 lease；terminal/orphaned 可 catch-up，但只 claim policy-eligible 工作；live session 只处理已提交且归属明确的 range |
| `manual` | 用户/管理端显式指定当前 project 内已提交 session/turn/range；必须授权，默认不绕过 `mode=off`/session disable |

同时，`MaintenanceEnabled` 必须把 startup recovery 和 digest recovery 视为独立功能。即使 `consolidation/retention/freshness` 全部关闭，只要存在 pending/queued recovery 工作，维护循环也不能整体 no-op。

### 4.3 轻量采集与成本控制

“turn-end 采集”不等于“每个 turn 同步调用一次 LLM”。

建议：

- 每个 turn 写一条 receipt；materializer 在 memory DB 单一事务内创建 digest 与 `available_at=now` 的 job；
- worker 可在 10～30 秒内做 micro-batch，合并同一会话的相邻 jobs；
- 每个 job 有最大等待时间，避免再次退化成无限延迟；
- `maxTurnsPerBatch`、`maxBytesPerBatch`、`maxRecordsPerSession` 仍保留；
- 如果用户紧接着继续对话，worker 只读取已提交的 turn range，不越界读取当前未完成 turn。

这样既保证数据持久化，又避免每轮强制一次大模型调用。

## 5. 数据模型调整

### 5.0 Durable capture receipt 与跨 SQLite 交接

`capture_receipt` 是 turn-end 的可靠交接凭证，不能用 memory DB 中的 digest 反向充当 receipt。它必须与 canonical transcript 的 turn terminal commit 位于同一个可靠事务边界；如果 canonical store 和 memory store 是两个 SQLite，不能假设两者支持分布式事务。

建议 receipt 至少包含：

```text
capture_receipt_id
project_key
session_id
session_generation
turn_id
message_seq_start
message_seq_end
canonical_revision
range_hash
created_at
state
materialized_at
last_error
materializer_owner_id
materializer_lease_until
materializer_fencing_token
attempt_count
retry_at
```

#### Generation 命名与跨库传递

本方案固定区分两个 generation：

| 名称 | 所属 | 语义 | 是否可复用 |
|---|---|---|---|
| `session_generation` | canonical session / receipt / memory provenance | 同一 `(project_key, session_id)` 的 session incarnation；session 删除后重建必须分配更大的值 | 不可复用、不可回退 |
| `purge_generation` | canonical purge journal / memory tombstone | 一次 deletion fence 操作的单调代数；用于幂等重放和旧 purge 拒绝 | 不可覆盖更大的值 |

`sessions.delete_generation` 可以作为 canonical 的持久化 purge-generation 分配器，但不能直接代替 `session_generation`。新 session 创建必须在 canonical 事务中从保留的 purge journal/identity allocator 取得大于历史 tombstone 的 `session_generation`；不能因为 `sessions` 行被删除就把 generation 重置为 0。已有 `session_generation=0` 的 legacy receipt/job 只允许在没有对应 memory fence 时走兼容读取，不能参与严格 v2 claim 或 commit。

canonical `BeginSessionPurge` 必须同时固化：

```text
project_key
session_id
fenced_session_generation
purge_generation
```

canonical purge journal 以 `(project_key, session_id, purge_generation)` 唯一标识，并按 `draining → memory_fenced → canonical_deleted → purged` 单向推进；已进入 `draining` 的 incarnation 不得自动恢复为 active。memory tombstone 以 `(project_key, session_id)` 为作用域，保存 `fenced_session_generation`、最高已接受的 `purge_generation`、`fenced/purged` 状态和脱敏原因。memory fence 事务提交后 tombstone 为 `fenced`，在 canonical 删除和 purge finalize 完成后再幂等更新为 `purged`；该 tombstone 是持久 fence，不因完成 purge 而删除。

任何 session-scoped materialize/claim/complete/candidate commit 必须在其 memory SQLite 事务中先检查 tombstone 状态；当状态为 `fenced` 时一律拒绝。仅当状态为 `purged` 时，新的 incarnation 才可按下列 generation 条件继续：

```text
work.session_generation > tombstone.fenced_session_generation
```

若没有 tombstone，当前 incarnation 可继续；tombstone 为 `fenced` 时，所有该 session 的新 work 都拒绝；tombstone 为 `purged` 时，只有严格大于 `fenced_session_generation` 的新 incarnation 可继续，generation 相同或更旧必须拒绝。canonical fence 阻止创建新 incarnation，直到 purge journal 到达 `purged`；新 incarnation 之后必须取得更大的 `session_generation` 才能重新物化。相同 `purge_generation` 的 purge 重放必须幂等，较旧 purge 不得覆盖较新的 journal 或 tombstone。

receipt 的状态只描述交接/物化，不与 job 的执行状态混用：

```text
captured
  → materializing
  → materialized        # digest + job 已在 memory store 同一事务提交

captured/materializing
  → deferred             # 暂时不可写/不可读，带 deferred_until
  → failed               # receipt 格式/参数永久非法
  → source_missing       # canonical range 已确认不存在
  → invalidated          # canonical revision 已变化
  → dead_letter          # 达到分类后的终止条件
```

job 的 `queued/leased/retry_wait/deferred/succeeded/dead_letter` 与 digest 的 extraction status 独立推进；receipt 到 `materialized` 后不因 job 开始抽取而改成 `processing`。

`materialized` 不是 LLM 成功；它只证明 digest 与 job 已建立。job 和 digest 必须共同引用 `capture_receipt_id`，并且使用以下唯一键防止重复物化：

```text
(project_key, session_id, session_generation, turn_id, canonical_revision, range_hash)
```

物化协议必须是至少一次、可重入的：

1. materializer 领取 receipt，并在 memory store 单一事务中 `INSERT ... ON CONFLICT` digest 与 job；两者必须同生共死；
2. memory 事务成功后，回写 receipt 为 `materialized`；
3. 若 memory 事务成功、第 2 步 receipt ack 失败，下一次扫描通过唯一键识别已存在的 digest/job，补写关联和 receipt，不重复 enqueue；
4. 若 memory 事务失败，事务整体回滚，receipt 保持 `materializing/deferred`，下次从完整事务重试；
5. 若迁移修复发现 digest-only 或 job-only 残留，必须把它标记为不可 claim 的 `repair_required/invalidated`，由修复器补齐或隔离；它不是正常 materialization 状态；
6. receipt 缺失、range 字段不完整、generation 不匹配或 workspace/project 不匹配时，禁止生成可执行 job，并记录可诊断错误。

因此，“turn 返回前已入队”不再作为跨 SQLite 的硬保证。可保证的边界是“turn 返回前 receipt 已持久化”；`digest_visible`、`job_visible` 和 `capture_degraded` 必须分别观测。若产品必须要求 turn 返回前 digest/job 都可见，则 canonical 与 memory 必须纳入同一事务边界；否则该要求不可执行。

### 5.1 扩展 `memory_turn_digests`

现有表已有 `digest_seq`、`message_seq_end`、`status`、`rollup_status`，建议补充/明确：

| 字段 | 作用 |
|---|---|
| `message_seq_start` | 该 turn 覆盖的 canonical 起始 seq |
| `message_seq_end` | 该 turn 覆盖的 canonical 结束 seq |
| `capture_receipt_id` | 关联 canonical capture receipt |
| `turn_id` | turn 的稳定 ID |
| `session_generation` | canonical session incarnation；与 receipt/job/output receipt 必须一致 |
| `status` / `extraction_status` | `queued / leased / retry_wait / deferred / succeeded / skipped / failed / source_missing / dead_letter / invalidated / cancelled_by_purge`；legacy rows 保持独立 schema/status 解释 |
| `enqueued_at` | 成功创建抽取 job 的时间 |
| `available_at` | worker 最早可领取时间 |
| `range_hash` | 该 turn range 的 source hash |
| `source_revision` | canonical revision；不是由当前 transcript 事后推导 |
| `deferred_until` | deferred 的下次尝试时间 |
| `attempt_count` | 该 receipt/job 的逻辑尝试次数 |
| `dead_letter_at` | 进入死信的时间 |
| `dead_letter_reason` | 分类后的终止原因 |

约束：

- `message_seq_start <= message_seq_end`；
- 同一 `(project_key, session_id, session_generation, turn_id, source_revision, range_hash)` 只能有一条 digest；
- 成功 enqueue 后 `enqueued_at` 不允许被重复写入改变，除非 job 被显式重置；
- digest 的文本仍只是诊断/恢复辅助，抽取优先读取 canonical range；
- tool message 是否计入 range 必须由 canonical schema 固定，不能由 worker 猜测；
- 只有确认 canonical range 在指定 revision 下不存在，且没有合法 fallback 时，才允许进入 `source_missing`/dead letter；
- backtrack、compaction 或 canonical revision 变化会使未完成的旧 range `invalidated`，不能继续写入当前 revision 的 candidate。

### 5.2 抽取 job 的幂等键

当前 `phase1_extract` 使用整段 transcript hash 作为 key，建议改为带 schema/version 的 turn range：

```text
phase1_extract/v2 key =
  project:<project_key>:session:<session_id>:generation:<session_generation>:
  turn:<turn_id>:revision:<source_revision>:range:<start_seq>-<end_seq>:<range_hash>
```

同一 incarnation 内同一 turn 的重复事件必须命中同一个 job；session ID 重建后的新 incarnation 必须与旧 job 隔离。job 完成后重复触发只返回已完成结果，不重复写入 candidate。`job_key` 不是唯一的安全边界：candidate、source、event 也必须以 `job_id`/`capture_receipt_id`/`session_generation`/`source_revision` 组成唯一幂等键，提交时再次校验 generation 和 revision。

建议 `memory_jobs` 增加或复用以下字段：

| 字段 | 作用 |
|---|---|
| `source_start_seq` / `source_end_seq` | 对应 canonical range |
| `source_turn_id` | 对应 turn |
| `session_id` / `session_generation` | 显式 session identity/incarnation；所有 v2 session job 必填，不能只从 `job_key` 反解析 |
| `available_at` | 可领取时间 |
| `deadline_at` | 最晚完成时间 |
| `lease_until` | worker 租约 |
| `heartbeat_at` | worker 心跳（可选，用于长任务） |
| `capture_receipt_id` | durable handoff 关联 |
| `job_kind` / `schema_version` | 区分 legacy recovery 与 turn-range worker |
| `retry_at` / `deferred_until` | 重试或 deferred 时间 |
| `dead_letter_at` | 死信时间 |
| `dead_letter_reason` | 死信原因 |

job 还必须记录 `project_key`，并使用 `(project_key, job_key)` 唯一约束。客户端提交的 workspace path 不能成为 job 的身份来源。

v2 的唯一键必须明确落到实际 SQLite 约束，推荐 `(kind, scope, project_key, job_key)`；job key 应使用版本化、无歧义的确定性编码，不允许直接拼接可能包含分隔符的原始字段。generation-qualified key 可防止同一 session ID 重建后旧终态 job 阻止新 job，但 SQL fence 仍以显式 `project_key + session_id + session_generation` 为准。digest 的唯一键至少是 `(project_key, session_id, session_generation, turn_id, source_revision, range_hash)`；`memory_turn_output_receipts`、候选 provenance/source 和任何成功 watermark 也必须能追溯到同一 session generation。

### 5.3 维护者 lease

新增一张项目级维护者表，例如：

```sql
CREATE TABLE memory_maintenance_leases (
    project_key   TEXT PRIMARY KEY,
    owner_id      TEXT NOT NULL,
    owner_kind    TEXT NOT NULL,       -- aicli / runtime-server
    pid           INTEGER,
    hostname      TEXT,
    acquired_at   TEXT NOT NULL,
    heartbeat_at  TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    fencing_token INTEGER NOT NULL
);
```

语义：

- 一个 project 同时只允许一个 active maintenance owner；
- `owner_id` 是每次进程启动生成的随机 incarnation ID；PID/hostname 仅作诊断；
- acquire、renew、takeover 必须通过 SQLite 写事务串行化，按 `expires_at <= db_now` 或当前 owner/token 条件更新；抢占时每次递增 `fencing_token`，更新影响行数不是 1 就视为未取得 lease；
- `db_now` 使用统一 UTC epoch/time representation；不能用各进程本地 wall clock 比较 lease；lease 行/token generation 不得在 session 清理或 owner 释放时删除重置；
- owner 负责 receipt materialization、`turn_end` job 的 claim、idle retry、startup catch-up 和 dead-letter 检查；
- owner 每隔固定周期更新 heartbeat；renew 使用 `WHERE project_key=? AND owner_id=? AND fencing_token=? AND expires_at>db_now`，所有 job claim/ack、candidate commit 也必须以当前 fencing token 做条件写入；
- 旧 owner 即使进程尚未退出，只要 token 已过期并被新 owner 递增替换，其后续写入必须因 token mismatch 被拒绝；
- 进程退出、lease 过期或宿主不可达时，其他进程可在 `orphanedGrace` 后接管；
- 接管后立即执行一次 catch-up，不等待新的 30 分钟 tick；
- 多进程同时存在时，非 owner 不处理正常主路径，只保留本地 enqueue 能力。
- maintenance loop 不能因为 consolidation/retention/freshness 关闭而整体 no-op；startup/digest recovery 必须有独立入口；
- 每个阶段必须有 bounded timeout 和 panic isolation；一个阶段卡住或 panic 不能冻结其他维护工作；
- stop/cancel 不能无限等待；必须记录 draining 状态和最后 heartbeat。

`db_now` 必须来自 memory SQLite 的统一时间表达（或在同一写事务中计算），不能把不同进程的本地 wall clock 直接互相比较。时间戳只用于判断 lease 是否到期，owner/token 的条件更新才是最终 fencing 依据；读到过期 lease 后也必须通过递增 token 的写事务完成 takeover。实现必须固定支持的部署边界：共享 memory DB 的进程必须使用同一可串行化 SQLite 文件/连接配置，数据库文件复制、恢复或跨主机共享不应被当作 lease 仍然有效。

PID/hostname 只能用于诊断和加速判断，不能作为唯一 fencing。若不引入新表，也可以复用现有 `session_actor_leases`，但需要新增 project 维度的维护职责和 fencing token，不能把 session 执行 lease 和 memory maintenance lease 混为一谈。

### 5.4 Session liveness 投影

不要把 UI 的 `SessionState` 直接当作 memory 是否可抽取的依据。建议引入独立的只读投影：

| 状态 | 判定 | memory 行为 |
|---|---|---|
| `live` | session lease 有效，进程存活，存在运行中 turn | 不处理当前未完成 turn |
| `idle_live` | session lease 有效，无运行中 turn | 可处理已完成 turn range |
| `orphaned` | lease 过期/PID 退出/宿主不可达，超过 grace | 立即 catch-up |
| `terminal` | `closed / archived` | 立即 drain |
| `expired` | `expires_at < now` | 先 drain；无法完成则 dead-letter |

判定输入：

- `session_actor_leases.heartbeat_at/expires_at`；
- PID/hostname 检查（仅诊断/辅助，不作为唯一存活证明）；
- `session_runtime_state.status`；
- session storage 的 `state`；
- `sessions.expires_at`；
- 最后活动时间。

禁止把“active”直接等同于“存活”，也禁止把“active”直接等同于“可无限抽取”。`active` 只是存储状态，不是 liveness 证明。

锁顺序必须固定为：

```text
project maintenance lease
  → session deletion/liveness fence
  → session extraction lease
  → individual job lease
```

任何需要反向获取的路径必须改写为先释放再重试，禁止交叉持锁。shutdown 时 owner 进入 `draining`，停止新 claim，等待有界时间；超时 job 由 lease recovery 接管，不能无限阻塞进程退出。

### 5.5 状态转换

```text
canonical turn commit + receipt
  → receipt=captured
  → receipt materializer (at-least-once)
  → memory transaction: digest + job created
  → receipt=materialized
  → job=queued
  → job=leased (with job lease + maintenance fencing token)
  → read exact range + verify revision/hash
  → LLM output validated
  → one memory transaction: candidates/sources/events + per-turn output receipt
  → job=succeeded, digest=done

transient failure
  → job=retry_wait (retry_at, classified backoff)
  → retryable queued

configuration/capability unavailable
  → job=deferred (deferred_until; no retry-budget burn unless policy says otherwise)

permanent invalid input / confirmed source deletion / retry exhaustion
  → job=dead_letter or source_missing (reason required; never ordinary queue)

source revision changed
  → old receipt/job=invalidated
  → enqueue a new receipt only for the new canonical revision

owner lease expires
  → fencing token increments on takeover
  → old owner writes are rejected
  → expired job lease becomes retryable

active session owner disappears
  → liveness=orphaned after grace
  → maintenance materializes and claims only committed turn receipts
```

### 5.6 Receipt / digest / job 状态机

三者承担不同职责，不能共享一个模糊的 `status`：receipt 表示跨库交接是否物化；digest 表示 turn capture/extraction 的可见状态；job 表示可执行工作与 lease。更新必须带预期状态、row version 或 lease/fencing token，旧 owner 的迟到 ack 不得覆盖新状态。

| 对象/当前状态 | 触发 | 下一状态 | 约束 |
|---|---|---|---|
| receipt `captured` | materializer claim | `materializing` | 有 receipt lease/token；允许重复投递 |
| receipt `materializing` | memory transaction 提交 digest+job | `materialized` | 先提交 memory DB，再 ack canonical receipt |
| receipt `materializing` | transient store/source 错误 | `deferred` | 设置 `deferred_until` 和错误类别 |
| receipt 任一非终态 | 确认 source revision/range 已删除 | `source_missing` | 只有有证据时可用 |
| job `queued`/到期 `retry_wait` | 原子 claim 成功 | `leased` | 校验 project fence、session fence、维护 fencing token |
| job `leased` | 校验通过且候选事务提交 | `succeeded` | candidates、sources、events、输出 receipt、digest done 同一 memory DB 事务 |
| job `leased` | transient 错误且未耗尽预算 | `retry_wait` | 错误分类 + backoff/Retry-After |
| job `leased` | 配置/能力暂不可用 | `deferred` | 不按普通 transient retry 轮询 |
| job `leased` | lease 超时 | `retry_wait` | takeover token 递增；旧 worker 的 ack 拒绝 |
| job 任一非终态 | source revision 改变 | `invalidated` | 不能复用旧输出；新 revision 使用新幂等键 |
| job `retry_wait` | retry 次数耗尽或永久错误 | `dead_letter` | 必须记录原因和可操作性 |
| job 任一未完成状态 | deletion fence drain 超时 | `cancelled_by_purge` | 不可执行；保留 purge generation 和审计 |
| job/digest `dead_letter` | 授权人工 retry | `queued`/`pending` | 写审计事件并按新 attempt 执行，不清除旧历史 |

约束：

- digest 自身不是 worker lease；job lease 是执行所有权的唯一依据；
- `deferred` 必须有 `deferred_until` 和配置恢复/人工恢复入口；超过最大 deferred age 时先按类别评估，不允许仅因年龄自动把所有错误死信化；
- `failed` 仅用于可恢复的本次 attempt，终态归类为 `dead_letter`、`source_missing` 或 `invalidated`；
- `skipped` 仅用于明确业务规则（如无成功 turn、无 eligible content），不能表示 transient reader 错误；
- 成功确认必须使用显式接口语义，例如 `MarkTurnDigestDone(jobID, digestID, sourceRevision, rangeHash, outputReceipt)`；每个 turn 有独立 output receipt：`(job_id, digest_id, source_revision, range_hash, output_hash)`；不能通过 `session_id + maxSeq` 批量把 digest 标为 done；
- 若需要 session watermark，只能是**严格 contiguous watermark**：从前一确认点逐个连续验证成功 receipt；遇到 failed/deferred/invalidated gap 即停止推进，后续 turn 的成功不能越过 gap；
- Phase 2 的 summary/rollup 只消费显式成功 receipt 覆盖的范围，不能把 `queued`/`deferred` 当作已抽取；
- `storeText=false` 与 `windowSummaryEnabled=true` 必须显式拒绝或自动关闭 window summary，不能让 window summary 永远无法构建。

### 5.7 Session memory mode 优先级

需要明确以下优先级，避免“没有 mode 行”和“读取失败”被混为一谈：

配置按职责独立求值：

```text
capture eligibility = memory.mode + session mode + capture.enabled
materialization eligibility = enqueue.enabled + recovery/owner availability
LLM eligibility = generation.enabled + session mode + provider availability
```

- `memory_session_modes` 没有该 session 行：按配置默认值处理（默认值必须显式配置并在 status 显示）；
- 读取 session mode 失败：fail closed，记录错误并显示，不静默当作 enabled；
- `disabled`：禁止新 receipt 和自动 LLM generation；已存在 receipt/job 保留并进入 `deferred`，除非显式 purge；
- `polluted`：禁止自动 generation，不删除已有记忆；是否继续 capture receipt 由 `capture.enabled` 决定，但不得自动调用 LLM；
- `memory.mode=off`：默认禁止新 capture、materialization 和 generation；此前已持久化的 receipt 仅执行受控恢复/清理，不因关闭配置而被静默删除；
- manual 操作默认不绕过 mode/session disable；若产品需要 privileged override，必须作为独立授权动作并审计。

## 6. 并发、幂等和一致性

### 6.1 Turn 终态提交、handoff 与跨库补偿

canonical turn commit 的事务边界必须包含：

```text
1. 提交本 turn 所有 canonical messages / terminal marker
2. 为该 turn 固化 turn_id、message_seq_start/end、canonical_revision、range_hash
3. 写 capture_receipt(state=captured)
4. 一次提交以上变更，再允许 turn 返回为 capture_committed
```

`message_seq_start/end` 指 canonical store 中该 turn 的闭区间；包括归属该 turn 的 user、assistant、tool 等已持久化 canonical messages，不包括临时 UI 事件、未提交的 assistant retry 输出或后台 metadata。continuation/retry 必须沿用同一 `turn_id` 并只在最终 terminal commit 冻结 range；被放弃的 attempt 不进入 receipt。range/hash 的序列化必须版本化、确定性，且哈希只覆盖闭区间的 canonical 内容。

`canonical_revision` 的规范语义是 session canonical history 的不可混淆快照版本；它不是“当前 transcript 行数”，也不是每次 append 都必须使所有旧 receipt 失效的全局水印。本文在 digest/job 等 memory-side 记录中使用字段名 `source_revision`，其值必须等于 receipt 固化的 `canonical_revision`，不得由当前 transcript 事后推导。每个 receipt 固化 `(session_generation, canonical_revision, message_seq_start, message_seq_end, range_hash)`：

- append 新 turn 可以产生更大的 revision，但不得改变旧 revision 下已经提交的闭区间；
- backtrack、compaction 或 rewrite 若触及某个已记录 range，必须产生新的 revision，并在 canonical invalidation journal 中使该 receipt/job/candidate 失效；
- worker 必须按 receipt 指定的 revision 读取完整闭区间并重新计算 hash；缺消息、revision 不匹配或 hash 不匹配不得标记成功；
- 若底层存储不能读取历史 revision，必须提供等价的 immutable range snapshot，或把受影响 receipt 标记为 `invalidated`，不能用当前 transcript 猜测旧内容。

实现前还必须固定 append、continuation、retry、tool message、compaction 和 backtrack 的 revision 变化表；不能由 worker 根据 `seq` 或当前 session state 推断。

若 canonical store 与 memory store 是两套 SQLite：

- 不使用分布式事务，也不声称 turn 返回前 digest/job 已创建；turn 返回前的强保证只覆盖 canonical commit + receipt；
- materializer 从 canonical receipt 至少一次投递，在 memory DB 同一事务中写 digest 与 job，使用唯一键 `(project_key, session_id, session_generation, turn_id, source_revision, range_hash)`；
- memory transaction 成功后才 ack canonical receipt。若 ack 丢失，重放必须命中相同 digest/job；若 memory transaction 失败，receipt 保持可重试；
- job 在 digest/source receipt 不完整时不可 claim；worker 读取前再次验证 canonical receipt、range、revision、hash、workspace/project；
- canonical source 暂时不可用是 `deferred`，只有确认 session/range 已永久删除才是 `source_missing`；
- 若 canonical DB 无法在提交 turn 时同步写 receipt，则先提供同一可靠边界的 journal/outbox 与扫描恢复能力；做不到时必须显式降级，不能把“best effort”描述成 durable handoff。

### 6.2 并发规则与 session 串行化

- 同一 session 同一时刻只有一个 extraction lease；不同 session 可以并行；
- claim 必须在 memory DB 短事务内同时校验 project maintenance fencing token、job 所属 `project_key/session_id/session_generation`、memory tombstone（`fenced_session_generation + purge_generation`）、session extraction lease 和单个 job lease；
- lease 由带过期条件的原子更新获取，包含独立递增的 fencing token；所有 heartbeat、candidate commit、ack 都以 owner ID + token + lease version 做条件更新；
- `lease_until` 到期后可重领，但旧 worker 的 LLM 返回不能提交候选；候选提交、job complete/fail/requeue、output receipt 和 digest 状态变更都必须在同一写事务中再次校验 token、job identity、`session_generation` 和 session tombstone；
- Phase 2 Consolidation 仍按 project 单写者，并与 Phase 1 通过已提交的 per-turn receipt 协调；
- 读取/注入路径不等待 extraction job。

session deletion fence 不是 project maintenance lease，也不是 session extraction lease；三者必须分开校验。任何一个预检查都不能替代 memory SQLite 的最终 commit-time generation fence。

### 6.3 Micro-batch

job 的持久化单位仍是一 turn/range；micro-batch 只是 worker 一次模型调用的调度优化，不改变幂等单位：

```text
receipt materialized → 每个 turn 独立 queued job
  → worker 在同一 session lease 下等待相邻 jobs，最长 30s 或达到 batch 上限
  → 一次调用处理 1..N 个明确 range
  → 对每个 range 单独验证并生成 output receipt
```

batch 成员必须逐一记录 job ID、range、revision/hash。若输出可按 turn 独立校验，则成功成员可各自提交；若调用结果无法可靠拆分，则整个 batch 原子失败并重试，不能只把 batch 最后一个 seq 当成所有 turn 成功。最大等待时间到达、batch 达到 `maxTurnsPerBatch`/`maxBytesPerBatch` 或 deadline 临近时必须执行。batch 不得读取未完成的当前 turn，也不得越过 range gap。

### 6.4 Revision 变化与重复提交

- backtrack/compaction/rewrite 必须在 canonical source 和 memory store 维护 invalidation journal；旧 revision 未开始的 job 取消，运行中的 job 在提交时因 revision/fence mismatch 被拒绝；
- 已生成的 candidate 保留审计记录但标记 `stale/invalidated`，不得继续参与 Phase 2；summary watermark 回退到受影响范围之前，或按已定义机制重建；
- 新 revision 使用新的 job 幂等键和 output receipt；不得原地改写旧 job 的 source hash；
- candidate/source/event 的插入与 job 成功、digest done、output receipt 必须同一 memory DB 事务完成，防止 LLM 重试重复写入或出现“job 成功但候选未提交”。

## 7. Session TTL 与死信

### 7.1 删除前 drain

显式删除和 TTL 删除必须共用同一个 deletion fence 协议，不能一个走 purge、另一个直接删 canonical：

```text
active
  → draining (canonical fence；递增 purge_generation，拒绝新的 canonical turn/receipt)
  → memory_fenced (memory fence 与派生 memory 清理在同一事务提交)
  → bounded wait/cancel（只等待或取消 in-flight 工作，不承诺其结果可提交）
  → canonical_deleted (删除 canonical source)
  → purged (finalize journal；永久保留最小审计/fence tombstone)

draining 超时/明确不可完成
  → 仍保持 deletion fence
  → memory fence 提交并清理派生 memory；未完成 job 标记 cancelled_by_purge
  → 确认 memory fence 后删除 canonical source，再完成 finalize
```

具体采用跨库两阶段 fencing，因为 memory DB 与 canonical store 不能共享原子事务：

1. **Canonical fence**：在 canonical store 一个事务内将 session 标为 `draining`、递增 `purge_generation`、固化被 fence 的 `session_generation`，拒绝新的 turn terminal commit/receipt，并持久化 purge journal。此步骤后崩溃时由 purge recovery 继续，不能自动回到 active。
2. **Memory fence**：在 memory DB 一个事务内写入 `fenced_session_generation + purge_generation`，停止该 incarnation 的 receipt materialize/new job claim，使已有 session extraction lease 失效；所有既有可执行 job/digest 先幂等终结为不可 claim 的 `cancelled_by_purge`/`invalidated`，再删除其 source payload、output receipts、windows 和 provenance，或只保留去正文的审计行。引用这些 memory 的 summary 在同一事务内标 stale、排队重建，并使其 artifact 立即不再可作为 fresh 内容读取；如果 artifact 存在于事务外的存储，freshness gate 必须由该事务提交的 revision/state 控制，物理删除可异步完成。候选提交事务必须检查此 memory fence，因此旧 worker 即使完成 LLM 也不能提交有效 candidate。这个事务提交是 memory 侧 purge 线性化点；提交后由 recovery 在 canonical store 的独立事务中把 purge journal 推进到 `memory_fenced`，不能把两次提交描述为原子操作。
3. **Drain/cancel**：仅等待已在 memory fence 前启动的 worker 返回或到 `drainTimeout`；这里的“drain 成功”只表示没有仍可提交的旧 lease，不能表示 LLM 结果允许写入。超时后，对仍保留的执行行幂等写入 `cancelled_by_purge`，已转为审计行的只更新脱敏审计状态。若 memory fence 无法确认，不能删除 canonical source，只能让 purge journal 保持进行中并重试；`source_missing` 仅用于另有证据证明 canonical range 永久不存在的情形，不能用来代替 purge 取消状态。
4. **Canonical delete**：在确认 memory fence 已持久化且新 turn 已被 canonical fence 阻止后，删除 canonical messages/source，并更新 purge journal 阶段。整个流程不得持有一个数据库事务等待另一个数据库的提交。
5. **Finalize**：确认 memory 侧 tombstone、清理结果和 summary invalidation 已提交；在 memory store 中将 tombstone 幂等推进到 `purged`，遗留 job 终结为不可执行状态，仅保留最小、脱敏的审计元数据；随后完成 canonical purge journal。跨步骤任意 hard kill 后，recovery 按 journal 幂等继续。新 incarnation 只能在此流程完成后创建，并取得更大的 `session_generation`。

worker claim 与 purge 即使并发，memory fence 的提交也会使后续 claim/候选 commit 失败。canonical fence 之后、memory fence 之前的 source recheck 只能尽量避免不必要的 LLM 调用；由于两库不共享原子锁，它不能作为正确性保证，检查后仍可能有一个在途调用。该调用的结果必须被 memory fence 拒绝。memory fence 事务本身原子清理此前已提交的 candidate/source/event；fence 后的旧 worker 输出不得写成有效 memory。协议覆盖 pending、queued、leased、retry_wait、deferred 和 processing，不能只检查 digest。显式删除和 TTL cleanup 共用该协议；TTL 使用绝对时间或滑动时间由 session 层明确，但仍有有效 session lease 的 active 会话不得被静默删除。

当前实现检查记录显示，session TTL 是创建时写入的绝对时间，不随活动滑动；cleanup 曾只看 `expires_at < now`，不检查 `state=active` 或 lease。该观察是本提案的风险依据，不代表提案已实现新的删除协议。若实现继续采用绝对 TTL，必须遵守上面的 deletion fence；仍有有效 session lease 的 active 会话不得被静默删除。

删除完成后必须清理或隔离残留 job，且残留记录中的 workspace/project/session/generation 归属不能丢失。

#### 7.1.1 派生 memory 删除契约

本方案将“session purge”定义为删除该 session incarnation 产生的可回溯内容，而不只是删除 outbox。memory fence 提交后，purge transaction 至少必须处理：

- `memory_turn_digests`、`memory_turn_windows`、`memory_turn_output_receipts`、session-scoped jobs；
- `source_session_id` 指向该 session incarnation 的 `memory_entries`、`memory_sources`、`memory_events` 和 `memory_usage`；删除谓词必须同时限制 `project_key` 与 generation，不能跨项目或跨 incarnation；
- `memory_summary_items`、active summary freshness 状态和 summary artifacts 也必须处理；受影响 summary 必须在 memory fence 事务内标记 `stale` 并排队重建，读取路径必须 fail closed 不返回 stale 内容，并清除或按 revision 隔离旧 artifact。重建本身不在 purge 事务内执行；若重建 job 物化失败，summary 继续保持 stale 且不可读，recovery 必须可再次排队；
- session-scoped digest/job/output receipt 等执行记录必须在 fence 时变为不可 claim，随后删除正文和 source payload；finalize 后不得残留可执行行。允许保留的仅是脱敏的 `(project_key, session_id, session_generation, purge_generation, receipt/job ID, reason, state, timestamps)` 审计元数据；
- legacy job 若没有显式 session/generation，只能按已验证的旧 key 做 best-effort quarantine，并且不能被当作严格 fence 已完成的证据。

可保留的只应是最小脱敏审计元数据（project/session/generation、job/receipt ID、purge reason、时间和状态），不得通过审计行、dead-letter 或错误日志保留原始 transcript、候选正文或 secret。若产品选择保留某条长期事实，必须先脱离被删除 session 的 source provenance，并经过独立的数据保留/隐私规则；否则按上述硬删除处理。

本节的 purge 规则优先于 §6.4 对 transcript rewrite/backtrack 的普通 invalidation 规则：普通 invalidation 可按审计策略保留 stale candidate 正文；显式/TTL session purge 不得仅把正文标 stale 后继续保留。

### 7.2 `source_missing`

当 canonical 已不存在且 digest 无法作为抽取 source 时：

- 标记 digest/job 为 `source_missing`；
- 写 `memory_events` 或维护审计；
- 记录计数和告警；
- 不再无限 retry；
- 不把该 digest 伪装成成功。

`source_missing` 不是普通 transient retry：只有在 deletion fence 已提交、canonical range 在指定 revision 下确认不可读、且没有合法持久 fallback 时才能使用。若只是 SQLite busy、provider 不可用、maintenance owner 切换或 source reader 暂时失败，应分别进入 `retry_wait` 或 `deferred`。

### 7.3 不在 retention 中静默删除 pending

当前 `PruneTurnDigests` 只删除 terminal 且 rollup 完成的行。这是正确的，但必须补 dead-letter/巡检，否则 orphaned pending 会长期占用 backlog。

建议：

- pending 超过 `maxPendingAge` 不能无条件进入 `dead_letter`；必须结合错误类别、source 是否仍存在和是否有 active deletion fence 决定；
- `deferred` 必须有 `deferred_until`；到期后回到 retry/materialization 队列，是否终止取决于错误类别，不只看年龄；
- dead letter 不参与正常 worker 扫描；
- 提供管理端查询、重试和人工恢复；
- 清理策略与 `source_missing` 分开配置。

错误分类与默认动作：

| 错误类别 | 默认动作 | 是否消耗 job retry budget |
|---|---|---|
| SQLite busy、短暂 I/O、内部 transient | 指数退避 + jitter，保留 job | 是，受较高上限保护；超限可进入可重试 dead-letter |
| provider rate limit | 遵循 `Retry-After`，无值时使用配置 backoff | 仅按实际 provider attempt 计数 |
| worker/maintenance lease 过期 | 使旧 token 失效，job 到期后重排 | 否；属于 ownership recovery |
| source revision/hash 改变 | `invalidated`，为新 revision 建新 job | 否 |
| canonical source 暂时不可读 | `deferred`，安排检查 | 否或单独计数 |
| 配置关闭、provider 未配置、权限暂不可用 | `deferred`，配置恢复后复活 | 否 |
| source 确认永久不存在 / purge 完成 | `source_missing` 或 purge terminal | 否 |
| 参数非法、range 不可能、schema 不兼容 | permanent failure / `dead_letter` | 不重试相同输入 |
| transient/provider 重试预算耗尽 | `dead_letter`，保留人工 retry 入口 | 是 |

`deadLetterAfter` 只可作为最长滞留的巡检阈值或告警阈值，不能独立触发所有错误的 dead-letter；对仍可恢复的错误必须允许配置化延期/重试。

### 7.4 防止队首毒化

当前 pending session 按最老 digest 排序，并有扫描数量上限。一个 session 若永久无法找到（缺失 canonical、空 transcript、持续错误），可能占满队首窗口，饿死后续健康 session。

必须保证：

- 单个 session 的连续失败超过上限后进入 `dead_letter` 或独立 quarantine；
- 每轮扫描不仅按 `MIN(digest_seq)` 取前 N 个 session，还要有 cursor/轮转机制；
- 缺失 session 不应每轮重复消耗完整扫描配额；
- dead-letter 行保留诊断，但不能阻塞普通 worker；
- 提供队列健康视图，显示队首 session、最老年龄和 quarantine 数量。

## 8. 读写边界

### 8.1 读取路径保持不变

本次变更只调整写入/恢复触发，不改变：

- stable `project_memory` + recall `project_memory_recall` 的布局；
- summary gate；
- pinned budget；
- citation/usage；
- 读取无 LLM 的约束。

### 8.2 注入时点

Turn-end extraction 产生的 candidate 仍默认是 candidate。是否立即可注入取决于 Phase 2：

- 生产策略可继续采用异步 Consolidation；
- 但用户应能在状态面看到 pending/candidate/active 的区别；
- 不允许把“candidate 已生成”误报为“memory 已生效”。

如果希望 turn-end 后尽快生效，可以增加“小批量即时候选晋升”策略，但必须保持 confidence、suppression、source 和 correction 规则不变。

## 9. 可观测性与健康判定

### 9.1 必需指标

除现有 job/digest 计数外，新增：

| 指标 | 含义 |
|---|---|
| `capture_receipt_committed_total` | 与 canonical turn commit 原子提交的 receipt 数 |
| `capture_commit_failed_total` | canonical turn+receipt 事务失败次数；不得计为 `capture_committed` |
| `capture_degraded_total` | 仅当有独立 durable canonical journal 可恢复时，turn 降级返回的次数；无 journal 时禁止降级成功 |
| `receipt_materialization_failed_total` | materializer 尝试失败数（按错误类别分维度） |
| `receipt_to_digest_job_visible_ms` | receipt commit 到 memory DB 中 digest+job 同事务可见的延迟 |
| `job_claim_latency_ms` | job 到 `available_at` 至首次成功 claim 的延迟 |
| `turn_to_phase1_success_ms` | canonical turn commit 至 per-turn 成功 output receipt 的端到端延迟 |
| `pending_digest_age` | 尚未 done 的 digest 年龄，按 queued/deferred/failed 状态拆分 |
| `maintenance_last_heartbeat_at` | 维护者最后心跳 |
| `maintenance_last_catchup_at` | 最后 catch-up 时间 |
| `maintenance_owner` / `fencing_token` | 当前 owner 及 fencing token |
| `dead_letter_total` | 死信数，按 reason/job kind 分类 |
| `source_missing_total` | canonical range 经确认缺失数 |
| `extract_retry_exhausted_total` | 按错误类别耗尽重试预算的次数 |

所有延迟指标必须记录 start/end event 和单位，不能仅上报含义模糊的 `turn_end_to_job`：

- receipt commit 可见性与 canonical commit 是同一事务断言，目标为 100% 一致；
- `receipt_to_digest_job_visible_ms` 起点是 receipt transaction commit，终点是 memory DB digest 与 job 同一事务可读；
- `job_claim_latency_ms` 起点是 job 到达 `available_at` 且 worker/owner 健康，终点是 claim transaction commit；micro-batch 等待计入此延迟；
- `turn_to_phase1_success_ms` 起点为 canonical terminal commit，终点为 per-turn output receipt commit；须同时记录 provider、模型和 job kind，不能将 provider 延迟混同为 capture 延迟。

### 9.2 健康状态

`/memory status` 和 `/api/runtime/memory/status` 应返回明确状态：

```text
healthy
  - owner alive
  - oldest pending < target
  - dead_letter = 0

degraded
  - owner alive but backlog exceeds threshold
  - trigger=session_end with short-lived hosts
  - no successful extraction for N minutes

unavailable
  - no maintenance owner
  - memory runtime not initialized
  - DB unavailable

failed
  - repeated enqueue failures
  - dead-letter present
  - source_missing present
```

状态面至少展示：

- `runtime`
- `capture`
- `generation_trigger`
- `recovery_trigger`
- `last_enqueue_at`
- `last_extract_at`
- `last_catchup_at`
- `maintenance_owner`
- `oldest_pending_age`
- `dead_letter_count`
- `last_error`

### 9.3 告警条件

- heartbeat 连续 2 个 `leaseTTL` 未刷新，或 owner fencing token 不可验证；
- capture receipt 事务失败（立即告警；无可恢复 durable journal 时报告 `capture_failed`，只有 journal 已原子持久化时才报告 `capture_degraded`）；
- materialization/job backlog 超过配置的延迟 SLO；
- 同一 session 连续多次 lease 过期或被 fencing；
- `source_missing_total > 0`；
- deletion fence 进入超时/失败状态，或 purge 完成后仍有可执行 job；
- legacy `trigger=session_end` 迁移中出现新格式 turn 未被 turn worker 处理。

## 10. CLI / API 变更

### 10.1 CLI

补充：

```text
/memory status
/memory extract <session-id> [--turn <turn-id> | --range <start>-<end>]
/memory drain [--session <id> | --all]
/memory queue [--limit N]
/memory dead-letter [--limit N]
/memory retry <job-id>
```

其中 `/memory extract` 是显式的管理/用户操作，不得绕过 session mode、project 权限、source revision 校验或配置禁用策略；当前 CLI 没有该命令，需要实现后才可对外承诺。`--range` 只能选择当前 project 内已有、完整且已提交的 canonical turn range，不能接受任意 path。

### 10.2 HTTP API

保留并扩展：

```text
POST /api/runtime/memory/extract
POST /api/runtime/memory/consolidate
POST /api/runtime/memory/drain
GET  /api/runtime/memory/jobs
GET  /api/runtime/memory/turn-digests
GET  /api/runtime/memory/maintenance
```

安全契约：

- project/workspace scope 必须从已认证的 server request context 解析，不信任客户端传入的 `workspace_path`、`project_key` 或 job 中间字段；
- 每个 session/job/digest 操作都校验其归属当前 project；job ID 采用 project-scoped lookup，跨 workspace 的 ID 返回 indistinguishable not-found，不泄漏存在性；
- `/retry`、`/drain --all`、dead-letter 修改、purge/fence 等管理操作要求管理员或等价本机授权；读取队列状态也需符合 workspace 权限；
- API 成功响应应返回操作 ID、作用范围和状态，不回显完整 digest/transcript；dead-letter diagnostics 默认脱敏，原始 source 只在显式授权后按最小范围访问；
- 多 workspace maintenance owner 的 lease key 必须包含规范化的可信 project identity，不能跨 workspace 共用 owner/fencing 状态；
- 手动操作必须写入 actor、时间、project scope、目标 receipt/job 和结果的审计记录；不允许把 manual trigger 当作权限绕过。

## 11. 配置变更

### 11.1 新配置草案

```yaml
memory:
  mode: on
  capture:
    enabled: true              # 只控制 canonical turn receipt 是否生成
    maxHandoffDelay: 2s        # 仅限本地 durable receipt；不含 LLM

  enqueue:
    enabled: true              # 控制 receipt 是否物化为 digest/job
    maxMaterializeDelay: 30s

  generation:
    enabled: true                # 控制 phase1 worker 是否执行 LLM
    trigger: turn_end            # 仅表达自动 enqueue/worker 策略，不重定义 capture
    turnDigest:
      enabled: true
      storeText: true
      maxTurnsPerBatch: 5
      maxBytesPerBatch: 131072
      maxDigestsPerRun: 200
      extractEveryNTurns: 1      # 不再需要等 50 turns
      idleExtractMinutes: 30
      recoveryOnStartup: true

  recovery:
    enabled: true
    onStartup: true             # receipt materialization + orphan/job recovery
    onIdle: true                # retry/deferred/dead-letter巡检
    onSessionEnd: true          # bounded drain，不是唯一 consumer
    orphanedGrace: 10m
    maxDeferredAge: 24h
    maxPendingAge: 7d
    retryPolicy: classified

  maintenance:
    heartbeatInterval: 30s
    leaseTTL: 2m
    drainTimeout: 30s
    catchupOnStartup: true
```

具体字段名可调整，但语义必须是：

- `capture.enabled`、`enqueue.enabled`、`generation.enabled`、`recovery.enabled` 是四个独立开关；
- `turn_end` 是默认主策略，但不自动覆盖显式关闭的 generation/enqueue；
- `session_end/idle/startup` 是恢复与 drain；
- maintenance owner 有明确 heartbeat/lease；
- 每个配置组合都能在 `/memory status` 中显示为 healthy/degraded/unavailable。

### 11.2 兼容策略

- 旧 `trigger=session_end`：保留原有 session-end 自动抽取时机；turn-end receipt/capture 可按新 `capture.enabled` 启用，但 v2 worker 仍按旧 trigger policy 等待 session-end，不得因迁移悄悄改成每 turn 调用 LLM；显示 migration warning 和队列状态；
- 旧 `trigger=both`：保留原有 session-end/idle eligibility；可生成 v2 turn receipts/jobs，但未显式迁移前不将 generation 时机扩展为每 turn；
- 旧 `trigger=manual`：保留 manual-only 语义，默认 `enqueue.enabled=false`、自动 generation 关闭；已有 manual API/CLI 仍显式处理有权限的 source。用户 opt-in 新配置后再启用 turn-end job worker；
- 没有 turn-end digest 的旧 session：继续使用 legacy bounded transcript fallback；
- 已存在的 pending digest：由 legacy recovery consumer 处理；只有具备 `job_kind=phase1_extract/v2` 和完整 receipt 的对象才交给新 turn worker；
- 新旧路径共存期间，job kind/schema version 是 claim 前置条件；同一 digest 不能同时具有 legacy 与 v2 可执行 job；
- 迁移完成后先停止生成 legacy job，再等待 legacy backlog 清空/隔离，最后关闭 legacy consumer；startup catch-up 必须按 kind 分流，不能重复抽取。

迁移期间 legacy 不是严格 fencing 的豁免路径：没有 `session_generation` 的 legacy receipt/job 必须在 purge fence 存在时 fail closed，不能继续 claim、complete、fail 后重排或提交 candidate。实现可以选择先通过已验证的 canonical identity 回填 generation，也可以将该行 quarantine/cancel；在未完成其中之一前，不得把 legacy cleanup 宣称为 purge 完成。`CompleteJobForWorker`、`FailJobForWorker` 及 recovery requeue 也必须重复检查 project/session/generation fence，不能只在初次 claim 检查。

### 11.3 必须阻止或警告的配置组合

| 组合 | 处理 |
|---|---|
| `capture.enabled=true` + `enqueue.enabled=false` | 允许；receipt 保留，`/memory status` 显示 deferred/not materialized，不得宣称有可执行 job |
| `enqueue.enabled=true` + `capture.enabled=false` | 拒绝启动或自动关闭 enqueue；没有可靠 source receipt |
| `generation.enabled=true` + `enqueue.enabled=false` | warning/degraded；只积累 receipt/digest，不执行 LLM |
| 旧 `trigger=session_end` + 短生命周期 CLI | warning；保留旧 generation eligibility，不静默升级为每 turn LLM；durable receipt 可先行物化并展示 backlog |
| `generation.enabled=true` + `turnDigest.enabled=false` + legacy recovery 也关闭 | 拒绝启动；没有消费路径 |
| `generation.enabled=true` + `consolidation.enabled=false` | warning；candidate 不会进入 active |
| `injection.enabled=true` + `mode=on` + 无 active/summary 生产路径 | warning |
| `turnDigest.storeText=false` + `windowSummaryEnabled=true` | 拒绝或自动关闭 window summary |
| `generation.enabled=false` + pending digest/job 存在 | recovery 仍可 materialize、lease recovery 和 drain；新 LLM job 进入 deferred，不能静默遗留 |
| `mode=off` + manual extract | 默认拒绝；任何特权例外必须是单独授权、显式 opt-in 且写审计 |
| `scope=user` | 当前 v1 不支持，继续拒绝 |

配置校验不能只检查枚举和负数；至少要为上述组合提供 `warning` 或 `error`，并在 `/memory status` 中显示。

## 12. 迁移与实施顺序

### Phase 0：契约与文档

1. 确定 `turn_end` 主触发与 recovery 触发的语义；
2. 定义 session liveness 投影；
3. 定义 digest/job/dead-letter 状态；
4. 明确 session TTL 与 memory drain 的先后关系；
5. 回写 `03/04/06/13`，并修订 `14` 的未关闭项。

### Phase 1：存储与状态机

1. migration 增加 capture receipt、turn range、revision/hash、`session_generation`、`purge_generation`/delete fence、materialization、available、dead-letter、fencing 字段/表，并建立跨 session ID 重建仍单调的 generation allocator；
2. 保持旧 schema 可读，并为 legacy digest/job 固定 `job_kind=legacy`、schema version；
3. 增加 `phase1_extract/v2` job key 与 `(project, job_key)` 唯一约束；
4. 增加 receipt/digest/job 状态转换和 fencing token 校验；digest+job 的正常物化必须是同一 memory SQLite 事务，repair-only 残留不可 claim；
5. 增加 per-turn output receipt、候选幂等键和严格 contiguous watermark（如仍需要）的数据约束；
6. 明确 canonical receipt/journal 的事务边界、purge tombstone 线性化点和派生 memory 删除契约，并先用故障注入证明跨 SQLite 补偿。

### Phase 2：Turn-end 采集/入队

1. 在 canonical transcript 提交事务中固化 turn range、revision/hash 与 capture receipt；
2. materializer 在 memory DB 单一事务中写 digest + phase1 job；
3. 覆盖 digest+job 原子事务整体回滚、事务成功但 receipt ack 丢失，以及修复发现历史 digest-only/job-only 残留时的隔离/补齐；
4. turn 路径只做有界 durable handoff，不做 LLM；无法提交 receipt 时按 §3.1 显式返回/展示 `capture_failed`，只有另有可扫描 durable journal 时才返回 `capture_degraded`。

### Phase 3：异步 worker 与恢复

1. legacy consumer 继续只处理 legacy digest/job；新 worker 只 claim `phase1_extract/v2`；
2. worker claim turn-range job，建立 project maintenance lease、session extraction lease、job lease 和 fencing；
3. 加入 micro-batch、分类 retry/deferred/backoff、per-turn output receipt；
4. 启动时按 job kind 分流并立即 catch-up；加入 orphaned liveness 检测；
5. 实现 session deletion fence、bounded wait/cancel（不允许 fence 后旧 worker commit）、purge 后 source_missing/cancelled 状态和 summary invalidation/rebuild；
6. 完成 shadow/双读对比后停止生成 legacy job，再清空并关闭 legacy consumer。

### Phase 4：运维面

1. `/memory extract/drain/queue/dead-letter`；
2. status 健康判定；
3. metrics/告警；
4. Web 面板展示 maintenance owner 和 backlog。

### Phase 5：灰度与默认启用

1. 先在 shadow 模式验证；
2. 对比 legacy 与 v2 的重复率、覆盖率、成本、materialization 延迟和 Phase 1 成功延迟；
3. 通过 M5 门禁后再默认启用 v2 turn worker；
4. 保留 legacy 只读/恢复路径，直到 backlog、source_missing 和 invalidation 迁移验收完成；
5. rollback 只停止新 job claim/生成并保留 receipt/job 诊断，不删除已提交 receipt，也不让两个 consumer 同时 claim 同一对象。

## 13. 验收标准

### 13.1 功能与一致性验收

- [ ] 10,000 个成功提交的 turn 中，100% 在 canonical terminal commit 事务中有完整且唯一的 capture receipt；事务失败不得计为 `capture_committed`。若产品采用 `capture_degraded`，必须同时证明独立 journal 已原子持久化且可恢复。
- [ ] 在 canonical commit、receipt materialize、digest/job memory transaction、receipt ack、candidate commit 各持久化边界注入 hard kill，各 100 次；重启后无静默丢失，重复物化/处理不重复创建 job 或 candidate；receipt 已提交但 memory DB 不可用时展示 `materialization_pending`，不误报 `capture_degraded`。
- [ ] 强制制造 digest+job 事务中途失败、memory commit 成功但 canonical receipt ack 丢失，以及历史 repair-only 不完整行；检查事务回滚、重放幂等、不可 claim 隔离与最终收敛。
- [ ] 每个 job 只读取 receipt 指定的闭区间；测试 user/assistant/tool messages、continuation、retry、被放弃 attempt；range/hash/revision 不匹配时 100% 不得进入 `done`。
- [ ] session 保持 `active`，杀死原 owner 后，超过 `orphanedGrace` 的 receipt 能由新 owner catch-up；任何未完成 turn 不得被读取。
- [ ] 对 backtrack、compaction 和 rewrite，旧 job/candidate 100% 被 invalidated/stale；旧 owner 的迟到 candidate commit 被 fencing 拒绝。
- [ ] generation 1 purge 后，同 generation 的 materialize/claim/complete/candidate commit 全部被拒绝；相同 purge generation 重放幂等；session 重建拿到更大的 session generation 后可创建新 job；旧 worker 无论何时返回都不能跨 incarnation 提交。
- [ ] explicit delete 与 TTL purge 均先建立 canonical fence，再提交 memory tombstone/派生数据清理事务；claim 与 purge 并发测试 1,000 次，canonical 删除后无可执行 job、旧 session 内容不可被 fresh summary/artifact 读取。
- [ ] purge hard-kill 矩阵覆盖 canonical fence 后、memory fence 前、memory fence 提交后、canonical delete 后及 finalize 前；每阶段 recovery 幂等，memory fence 未提交时不得删除 canonical source。
- [ ] dead-letter 记录保留 reason、attempt history、project scope 和审计信息；普通 retention 不静默删除；人工 retry 必须授权且可审计。
- [ ] 所有配置关闭、能力恢复、legacy job、new v2 job 组合都有状态断言；manual-only 不自动调用 LLM。
- [ ] 维护特性全部关闭时，显式启用的 recovery 仍可运行；一个阶段 panic/超时不冻结其他阶段；session mode 缺失与读取失败按不同规则处理。

### 13.2 并发、fencing 与迁移验收

- [ ] 20 个并发 worker 对同一批 10,000 jobs 扫描，任一 job 同一时刻只有一个有效 fencing token owner；无重复有效候选。
- [ ] 同一 session 同时最多一个有效 extraction lease；不同 session 可并行。
- [ ] maintenance owner lease 过期后新 owner 递增 token；暂停旧 owner、完成 takeover、再恢复旧 owner，旧 owner 的 heartbeat/claim/ack/candidate commit 全部被拒绝。
- [ ] job lease 到期、maintenance owner crash、PID reuse/hostname 不可达场景均依赖数据库 fencing，而不是仅靠进程检测。
- [ ] 旧 recovery consumer 只处理 `legacy` kind/schema；新 worker 只处理 `phase1_extract/v2`；测试期间两个 consumer 同时运行也不能 claim 同一 digest/source receipt。
- [ ] Phase 2 与 Phase 1 并发时，summary gate 只接受成功 receipt 覆盖范围，仍 fail closed。
- [ ] deferred、retry_wait、invalidated、dead_letter 并发恢复不会跳过 watermark gap、重复推进或遗失审计历史。

### 13.3 性能与成本门禁

所有采样使用 monotonic timer；排除 LLM 的 turn capture/handoff 延迟与包含 provider 的端到端延迟必须分开报告。

- [ ] 在 1,000 个本地 turn、至少 10 个并发 session 的基准中，记录 turn terminal commit 到 handler return 的新增开销：P95 ≤ 50ms、P99 ≤ 100ms；receipt 已与 commit 同事务可见，LLM 不在该路径。
- [ ] healthy materializer 运行且没有人为 DB 故障时，receipt commit 到 digest+job 同事务可见 P95 ≤ 5s、P99 ≤ 30s；若 worker/owner 未运行，报表必须显示延迟原因，不冒充成功。
- [ ] worker 健康、队列无资源饱和时，`available_at` 到 claim 的 P95 ≤ 35s、P99 ≤ 40s（包含最多 30s micro-batch window）；有界性测试确保单个 job 不会因 debounce 无限等待。
- [ ] 用固定延迟的 deterministic provider、1,000 个 turn-range jobs 验证 micro-batch 实际减少 provider call 数；输出 coverage 必须 100%，每个 range 各有 output receipt。真实 provider 的 P50/P95/P99 独立报告，不与本地 capture SLO 混用。
- [ ] 在持续写入与 SQLite busy 注入期间，turn capture 仍按 receipt 事务语义成功或显式降级；health/status 查询 P95 ≤ 200ms，不能无限等待 worker/LLM。
- [ ] 门禁至少运行 3 次独立冷启动和 3 次热启动；报告样本数、失败数、P50/P95/P99、机器/DB 配置及 provider 类型。任何 receipt 丢失、跨 workspace 访问或旧 fencing token 成功提交均为零容忍。

### 13.4 真实故障验收

必须覆盖并保存可复现日志/数据库断言：

1. turn receipt commit 后立刻 `TerminateProcess`，session 仍为 `active`，下一次 worker 启动能 materialize 并处理；
2. canonical store 与 memory store 分开时，在每个跨库交接步骤 hard kill，并验证 at-least-once 补偿；
3. runtime-server 不运行、短生命周期 aicli 连续重启，单次存活时间小于 idle tick；receipt 在下一次可用 owner 启动后仍恢复；
4. lease 过期、owner takeover、PID reuse、旧 owner 恢复写入；fencing 必须拒绝旧写入；
5. TTL purge 与 queued/leased/LLM-running job 竞态；检查删除 fence、bounded wait/cancel、purge 线性化点、派生 memory/summary 清理、candidate commit 和残留 job；
6. canonical range 删除、hash mismatch、revision changed、backtrack/compaction；区分 deferred、source_missing、invalidated；
7. 两个 aicli 进程和 runtime-server 共享同一 workspace memory DB，同时有两个 workspace，验证 owner/fence/job ID 不跨项目；
8. rate limit、SQLite busy、provider outage、配置禁用、非法 range 各触发一次，逐一断言 retry/deferred/dead-letter 类别；
9. 重复 enqueue、重复 worker claim、批次部分失败、候选写入前后 hard kill；
10. legacy 与 v2 consumer 共存及 rollback，证明没有双重 claim 和重复抽取。

现有 `e2e_sqlite_test.go` 的 hard-kill 测试在终止前把 session 设置为 `closed`，这不能证明真实 crash 恢复。新验收必须保留 `active` 状态；同时应验证 canonical receipt 与 terminal commit 原子性，而非只验证 memory DB 中已有 digest。

## 14. 风险与权衡

### 14.1 成本上升

每个 turn 入队不等于每个 turn 同步抽取 LLM。通过 micro-batch、debounce、maxDelay、maxTurnsPerBatch 控制成本。

### 14.2 重复抽取

必须使用 turn range job key + source hash + lease。重复触发只允许复用已完成结果，不能重新生成 candidate。

### 14.3 Canonical 仍在变化

Turn range 必须在 canonical transcript 提交后才创建；worker 读取时校验 range、hash 和 revision。若 session 已 backtrack/compaction，按现有 invalidation 逻辑处理。

### 14.4 安全边界

- digest 文本仍按 secret 规则脱敏；
- worker 不读取跨 workspace/跨 session 数据；
- maintenance API 需要授权；
- 不把 digest 内容当作指令；
- dead-letter 查询不能泄漏原始 secret。

### 14.5 与现有读路径兼容

本方案不改变 stable/recall 注入格式、summary gate、pinned 预算和 citation 规则，只改变写入触发和恢复路径。

## 15. 代码落点建议

| 位置 | 变更 |
|---|---|
| `backend/internal/chat` turn terminal hook | 在 canonical terminal commit 同一事务边界固化 turn range/revision/hash/capture receipt；不直接假设 memory DB job 已存在 |
| `backend/internal/memoryruntime/turn_digest_sink.go` | 实现 receipt materialization：在 memory DB 单一事务内幂等写 digest + v2 job，并补偿跨 SQLite 失败 |
| `backend/internal/memoryruntime/turn_digest.go` | 支持 turn-range consumer、startup catch-up、active turn range |
| `backend/internal/memoryruntime/runtime.go` | liveness-aware eligibility，不再只看 SessionState |
| `backend/internal/memorystore/v2_turn_digest.go` | 增加 receipt/range/revision、per-turn output receipt、显式状态转换、contiguous watermark（如保留）和 dead-letter 状态 |
| `backend/internal/memorystore/v2_schema.go` | migration：capture receipt、turn range/revision/hash、job kind/version、lease/fencing、delete fence、retry/deferred/dead-letter |
| `backend/internal/memoryassembly/maintenance.go` | 首 tick 立即 catch-up；owner/lease；idle retry |
| `backend/cmd/aicli/commands/chat_memory_runtime.go` | 启动 catch-up、手动 drain/extract、host liveness |
| `backend/cmd/runtime-server/memory_runtime_boot.go` | per-workspace maintenance owner |
| `backend/internal/api/runtimeapi/memory_handlers.go` | status health、queue/dead-letter/maintenance API |
| `backend/internal/sessionpurge` / session cleanup | 统一 explicit/TTL deletion fence、禁止新 claim/materialize、bounded drain、残留 job 隔离和 source_missing/cancelled 审计 |

## 16. 实施前必须冻结的参数

以下不是可以留到实现后再猜测的架构问题；实现前必须写入配置/协议版本，并由验收测试覆盖：

1. `capture`、`enqueue`、`generation`、`recovery` 四个开关及旧 `trigger` 的明确映射；
2. materializer 的最大延迟、worker micro-batch 窗口（建议 10～30 秒）、batch 上限和每个 job deadline；
3. `session_generation` 分配/复用规则、`purge_generation` 与它的区分、`canonical_revision` 的生成规则、turn range 是否包含 tool messages、continuation/retry 的归属规则，以及 range hash 的确定性编码；
4. source 暂时不可读与永久 `source_missing` 的判定和合法 fallback；digest 文本默认不能替代 canonical source；
5. dead-letter 保留期、按错误类别的 retry/deferred 上限、人工重试权限与审计字段；
6. maintenance owner 的 project/workspace scope、lease TTL、orphaned grace、fencing token 和 shutdown drain timeout；
7. explicit delete/TTL purge 共用的两库 fence、memory fence 线性化语义、派生 memory/summary 删除规则、残留诊断数据保留期和跨库清理顺序；
8. legacy consumer 与 v2 worker 的 schema/job kind 分界、迁移完成门禁和 rollback 行为；
9. candidate 是否即时展示；无论展示与否，candidate 不能被误报为 active memory。

## 17. 最终建议

这项变更应作为 **P0 写路径设计修正**，而不是简单把 `generation.trigger` 改成 `both`。

优先级顺序：

```text
1. Canonical turn commit + durable capture receipt
2. Cross-SQLite materialization/compensation + v2 turn-range idempotency
3. Session deletion fence and canonical range/revision contract
4. Maintenance owner, session/job lease and fencing/orphaned recovery
5. Per-turn output receipt, retry/deferred/dead-letter and strict watermark
6. Legacy/v2 migration, status/health/manual recovery
7. Shadow rollout and fault-injected acceptance
```

只有前五项落地后，“记忆系统在会话结束后生效”才不再依赖用户是否正常 `/exit`、进程是否长期存活，以及 digest 是否刚好被某个 ticker 扫到。当前文档只修订设计契约，未声称这些代码能力已经存在。


## 18. 本轮增量实施记录（2026-11-18）

> 本记录只补充代码进展，不改变本文“**方案提案（待评审，未实施）**”状态，也不表示生产验收完成。

- turn capture receipt 的 materialize 与 session-end recovery 现在以正 `session_generation` 作为 incarnation 边界；generationless receipt 不进入物化，缺少 generation-scoped receipt 查询能力时 fail closed。session-end pending digest drain 及 capture materialization 按当前 session generation 查询。
- materialized receipt recovery 使用 `(materialized_at, created_at, id)` keyset 顺序；SQLite 查询、in-memory 排序和 recovery cursor 使用相同排序键。generation-scoped recovery 的分页继续条件也按对应分页接口能力判定。
- compaction/backtrack invalidation event 携带 session generation；缺少有效 generation 的 invalidation 不再退化为 session-ID 级清理。当前 generation 不匹配的旧 receipt 会跳过，不读取复用 ID 的新 incarnation transcript。
- 本轮针对性测试通过：`go test ./internal/memoryruntime -count=1`、`go test ./internal/chat -count=1`、`go test ./internal/memorystore -count=1`。测试数据 fixture 已补充明确的 generation；未修改真实 SQLite 实例数据或 suspension 状态。
- 仍未完成生产验收：hard-kill 故障注入、跨 SQLite 崩溃恢复、性能/并发门禁及 shadow 运营验证。generationless/混代窗口和其它未版本化派生状态仍按前述风险待后续处理；本记录不宣称 Turn Window generation 缺口已整体关闭。
