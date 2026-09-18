# Runtime Store 加固未实施项方案索引（P1.5 / P1.6 / P1.7 / P2.11 / P2.13）

- 版本：v1.0（2026-09-18 初稿）
- 状态：待评审；评审通过后各分册 §决策记录 即为默认执行基线（未被否决的决策按基线执行）；实施依据见 §0
- 日期：2026-09-18
- 背景：`runtime store` 死锁事故的 P0（池重入、操作超时、重入守卫）与 P1/P2 可观测性部分已落地，见：
  - `backend/internal/chat/session_runtime_store.go`（`operationContext`、`recoverSQLiteMailboxDuplicate` 改用已持有连接、`notifyDrops`）
  - `backend/internal/chat/runtime_store_hardening.go`（`poolReentryGuard`、`NotifyDropStats`、`ReentryDetections`）
  - `backend/internal/chat/runtime_store_hardening_test.go`（7 个回归用例）
- 本索引覆盖仍未实施的 5 项，各自分册：

| 编号 | 主题 | 分册 | 优先级 | 依赖 |
| --- | --- | --- | --- | --- |
| P1.5 | 事件持久化批量化 | `docs/plan/runtime-store-event-persistence-batching-and-async-plan-20260918.md` | P1 | 无（可与 P1.6 并行） |
| P2.11 | 发布链异步化（持久化旁路） | 同上（同分册 Phase 2） | P2 | P1.5 的 buffer 组件 |
| P1.6 | `AppendEvent` 热路径：`RETURNING` + prune/vacuum 外移 | `docs/plan/runtime-store-append-hotpath-optimization-plan-20260918.md` | P1 | 无 |
| P1.7 | 读写双连接池拆分 | `docs/plan/runtime-store-read-write-pool-split-plan-20260918.md` | P2 | 建议在 P1.6 之后（减少写事务时长，收益更大） |
| P2.13 | `SnapshotSession` 审计与加固 | `docs/plan/sqlite-session-snapshot-hardening-plan-20260918.md` | P1（独立于 runtime store） | 无 |

> 审查报告：`docs/plan/runtime-store-hardening-plan-review-20260918.md`。其中 **R1 是现有代码的阻断级缺陷**（deferred 读→写升级 → `SQLITE_BUSY_SNAPSHOT(517)`；runtime-server 桥接静默忽略错误 → 事件丢失），建议 **P0.5 先行**：写事务 `LevelSerializable`（BEGIN IMMEDIATE）+ 517/BUSY 分类重试 + 桥接错误可见性。R2–R6 已回写到各分册（P1.5 §3.6、P1.6 §3.1/§3.2、P1.7 §3.7、P2.13 §2.8）。

## 0. 实施依据与冲突裁决（每个工作项的唯一契约）

| 工作项 | 实施依据（唯一契约文档） | 强制约束来源 | 前置 |
| --- | --- | --- | --- |
| P0.5（审查 R1 修复） | `runtime-store-hardening-plan-review-20260918.md` §1.5（R1 + 修复清单；暂无独立分册） | P1.5 分册 §3.6（同一约束的分册表述） | 无，建议立即 |
| P2.13 | `sqlite-session-snapshot-hardening-plan-20260918.md`（§0.3 发现 F1–F8、§2 加固设计、§6 决策、§4 DoD） | 审查报告 §2.7 | 无 |
| P1.6 | `runtime-store-append-hotpath-optimization-plan-20260918.md`（§3 设计、§7 决策、§5 DoD） | 审查报告 §2.4；P0.5 完成 | P0.5 |
| P1.5 / P2.11 | `runtime-store-event-persistence-batching-and-async-plan-20260918.md`（§3 设计含 §3.6 锁约束、§9 决策、§7 DoD） | 审查报告 §2.1–2.3；P0.5 完成 | P0.5；建议 P1.6 |
| P1.7 | `runtime-store-read-write-pool-split-plan-20260918.md`（§3 设计含 §3.7、§7 决策、§5 DoD） | 审查报告 §2.5–2.6 | 建议 P1.6 |

裁决规则（冲突时）：

1. 每个工作项**只以对应分册**为需求 / 设计 / DoD / 里程碑的契约；本索引仅作导航，审查报告仅作约束与证据，不单独新增需求。
2. 分册正文与其「决策记录（评审基线）」冲突时，以决策记录为准；评审否决的决策在决策表原地更新并保留历史行。
3. 分册与审查报告冲突时：报告中的 R1–R10 是强制约束；若分册未覆盖对应结论，**先补分册再实施**（R1–R10 已于本轮回写完成）。
4. 行号引用一律以函数 / 符号名定位为准（工作区存在并发改动）。
5. 每项实施前先完成该分册的 M0（度量 / 基线）；验收以分册「测试与验收（DoD）」门槛为准。

## 0.1 共同现状（各分册共享的事实基线）

- 单连接池：`backend/internal/chat/session_runtime_store.go:1744-1745`（`SetMaxOpenConns(1)`/`SetMaxIdleConns(1)`），打开重试路径同样设置于 `:4204-4205`。
- per-connection 语义：`PRAGMA busy_timeout` 等逐连接生效（`init`，`:4232-4247`）；全局邮箱靠 `ATTACH DATABASE` 绑定在某一连接上（`backend/internal/agentcontrol/mailbox.go:154-157`，需要 `*sql.Conn`）。
- 双宿主事件桥均同步落盘（在 bus handler 内直接 `AppendEvent`）：
  - aicli：`backend/cmd/aicli/commands/chat_actor_host.go:871-897`
  - runtime-server：`backend/internal/api/skills/handler.go:4056-4081`
- `Bus.Publish` 是同步派发（`backend/internal/events/bus.go:307-330`，handler 在发布者 goroutine 内执行）。
- 生产者已先落盘再发布并回填 `payload["seq"]`（`backend/internal/chat/actor.go:5264-5275`），桥接层据此跳过去重。
- 读 API 清单（`ensureForReadCtx` 调用点，2026-09-18 行号）：`GetLease:1965`、`LoadState:1998`、`GetToolReceipt:2303`、`ListToolReceipts:2356`、`ListEvents:3460`、`ListEventsBefore:3536`、`ListMailbox:3608`、`ListAgentControlMailbox:3684`、`ListAgentControlMailboxRecords:3761`、`LastEventSeq:3960`、`LastMailboxSeq:3982`、`LastAgentControlMailboxSeq:4004`、`LastAgentControlMailboxRecordSeq:4027`。
- 驱动事实：`github.com/ncruces/go-sqlite3 v0.32.0` 内置 SQLite **3.51.3**（证据：模块缓存 `embed/sqlite3.wasm` 内版本串），支持 URI `_pragma=name(value)` 逐连接注入（模块 `conn.go:60-138`）。

> 行号均为 2026-09-18 工作区快照；工作区存在其他会话的并发改动，评审/实施时请以函数名定位为准。

## 1. 实施顺序建议与理由

0. **P0.5（新增，审查 R1，前置）— 已于 2026-09-18 实施并回归通过**：写事务 IMMEDIATE + 517/BUSY 重试与计数 + runtime-server 桥接错误可见性。改动清单与验证证据见审查报告 §7。
   **P0.5b（P0.5 收尾）— 已于 2026-09-18 完成**：全仓逐 store 收敛写事务模式（先按连接池 DSN 是否已带 `_txlock=immediate` 分类，只改真正 deferred 的池）；`agentcontrol` 全局 agent 注册表、`artifact`、`background`（aicli/runtime-server 共享库的 `MAX(seq)+1`）、`migrate` 共 6 处改 `sqliteutil.WriteTxOptions`；新增全仓门禁 `TestWriteTransactionsUseImmediateOptions`（白名单自校验 + 已用探针验证"会红"）；明确**不**在 `sqliteutil.OpenFileCtx` 统一注入 `_txlock`（会波及只读连接）。详见审查报告 §7.4–7.5。
1. **P2.13 快照加固 — 已于 2026-09-18 实施并回归通过**：专用快照池、超时/取消、O_EXCL 归属权、容量预检、一致性校验、归档降级与超龄目录清扫。改动清单与测试对照见分册 §8；**M5 数值压测已达成**（门控压测 4 轮：写 P99 增量 0.96–5.23ms ≤10ms，快照 180–207ms/次、零失败）；Q3 已答复（frontend 无 manifest 消费点），Q2 维持不做（理由在册）。
2. **P1.6 热路径 — 已于 2026-09-18 实施并回归通过**：RETURNING 单语句取号 + 能力探测回退、prune/vacuum 拆分与后台单飞维护、append 统计。语句数 2→1、写事务内 vacuum=0 已证；**单事件时延目标已随 P1.5 批量复测关闭：单位事件写锁成本 456µs/op → 34–36µs/事件（≈13×）**。详见分册 §9.3/§9.5。
3. **P1.5 批量落盘 — 已于 2026-09-18 完成（默认关闭，灰度期开启）**：store `AppendEvents` + `eventPersistBuffer` + 双宿主接线 + 配置 + D3 关键类型注册表 + 健康端点 `persist` 段；M5 压测 1k/s×60s：**零丢失、批均值 25.0、flush P95 1.57ms、发布链零磁盘等待（max 1.0ms）、写事务压缩 25×**；批参数扫描已补齐"≥32×"结论：`flushInterval=32ms→31.9×`（贴线）、**`40ms→39.9×`（达标，flush P95 2.1ms）**，部署侧调 `sessionRuntime.eventPersist.flushInterval=40ms` 即得 ≥32×。aicli 可视化已接线（`/debug display` + `/web/api/status` 的"存储与持久化:"区块 / `storage.persist` 段）。详见分册 §11–§13。
4. **P2.11 异步派发 — 已于 2026-09-18 实施完成（默认关闭）**：`AsyncDispatch` 语义（队列满→有界等待 worker 腾挪→超时才同步兜底+计数）+ `dispatch_*` 延迟遥测；M5 复测 1k/s×60s：**零丢失、dispatch P95 <1µs（max 646µs）、flush P95 2.33ms**，慢盘故障注入下发布链 max <10ms（与磁盘解耦）。详见分册 §14。
5. **P1.7 读写双池 — 已于 2026-09-18 实施完成（`DisableReadPool=true` / `sessionRuntime.readPool.disable=true` 可逐字节回滚）**：写池单连接/ATTACH/锁序零改动；读池惰性打开、逐连接 `_pragma`（`query_only`/`busy_timeout`/`cache_size`）并读回校验，13 个读 API 全部路由 + 清单门禁，Close 先关读池再 TRUNCATE checkpoint，失败降级写池可观测；DoD 1–12 用例齐（含重开作废、长读 WAL 预算 60s 门控）。压测（2k 写 + 4×200 读）：**读 P95 4.87ms→1.85ms（−62%）**、写 P95 不劣化；长读 60s：写零失败、WAL 与提交数成比例（~19KB/事件）且释放后 checkpoints 回收 <1MiB，**批量写把放大降 16×（~1.2KB/事件）**。健康快照新增 `store_pools`，双宿主配置已接线，aicli `/debug` 观测同步接线（`storage.pool`）。详见分册 §9。

## 2. 统一验收原则（各分册 DoD 均须满足）

- 默认值安全：所有新行为默认关闭或保持现状逐字节兼容；开关关闭时既有测试全绿。
- 不静默丢数据：任何降级/丢事件/超时都必须有计数 + 限频日志。
- 有界：任何新增等待都有 deadline（复用 `operationContext` 模式）；任何新增队列都有容量与溢出策略。
- 双宿主对等：aicli 与 runtime-server 共享同一实现，不允许两份逻辑。
- 可回滚：每个分册给出关闭开关与回滚步骤，且不涉及不可逆磁盘格式变更（必要时用 `user_version` 迁移与向后兼容读取）。
