# G0-5 Dogfood 记忆库只读诊断（2026-11-23）

## 状态

**已完成真实 `.aicli/memory/memory.db` 的只读诊断；未执行 dry-run、修复、迁移、清理或写入。**

> 2026-11-24 更新：真实库仍未写入；最新一致快照、v23→v32 副本迁移、digest/job
> operator 分类、deadline repair 和策略模拟见 §8。

- 数据库：`.aicli/memory/memory.db`
- 读取方式：`sqlite3 -readonly`（Python sqlite3 在当前宿主不可用）
- 观测时间：2026-11-23 22:32 CST
- schema：**v23**（`memory_v23_core_usage`）；尚未应用 v24 scope 与 v25 候选查询索引
- DB 主文件约 2.0 MB，存在 WAL 文件；读取期间库仍可能被运行中的 host 写入
- 没有打开 runtime-server/aicli memory runtime，避免初始化过程自动执行 migration

---

## 1. 关键计数

| 对象 | 数量 |
|---|---:|
| `memory_entries` | 75 |
| `memory_jobs` | 119 |
| `memory_turn_digests` | 111 |
| `memory_versions` | 75 |
| `memory_core` | 0 |
| `memory_core_usage` | 0 |
| `memory_summaries` | 1 |
| `memory_scope_grants` | 表不存在（v23） |

`memory_entries` 状态：

| status | 数量 |
|---|---:|
| active | 4 |
| candidate | 71 |

全部 entries 的 `source_kind` 为 `session_extract`；没有 manual 记录。

`memory_turn_digests`：

| status | 数量 |
|---|---:|
| pending | 101 |
| done | 10 |

pending digest 覆盖 **31 个 session**，最早 `created_at = 2026-11-18T00:23:56Z`，最晚 `2026-11-23T14:04:35Z`。

---

## 2. Job backlog 分类

| kind / status | 数量 |
|---|---:|
| `phase1_extract/v2` failed | 76 |
| `phase2_consolidate` succeeded | 13 |
| `phase1_extract` failed | 10 |
| `phase1_extract/v2` succeeded | 10 |
| `phase1_extract/v2` pending | 8 |
| `phase1_extract` cancelled | 1 |
| `phase1_extract` succeeded | 1 |

pending job：

- 8 条全部为 `phase1_extract/v2`，`attempt=0`；
- 8 条全部缺少 `deadline_at`（历史行），在 v25 的 claim 保护下不会再被盲目启动，但需要显式 repair/dry-run；
- 最早 `available_at = 2026-11-22T10:19:59Z`。

failed `phase1_extract/v2` 错误分类（按 `last_error` 子串，未输出原始错误）：

| 分类 | 数量 |
|---|---:|
| deadline | 54 |
| invalid | 14 |
| other | 8 |

---

## 3. 结论

1. G0-5 仍不能关闭：真实库存在 **101 条 pending digest**、**8 条缺 deadline 的 pending job**、**76 条 failed v2 job**；
2. `memory_entries` 已有 75 条自动抽取记录，但只有 4 条 active，71 条停留在 candidate；consolidation/审核链路仍未完成闭环；
3. 真实库 schema v23；当前代码已到 v25。**不能直接用新 runtime 打开该库**，否则会在首次打开时自动应用 v24/v25 migration；
4. 需要对真实库先备份，再在受控副本上执行 dry-run、deadline repair、digest replay/恢复和 migration 演练；
5. 本轮没有写入、清理、reset 或迁移真实库。

---

## 4. 可复现命令

```text
cd <repo>
sqlite3 -readonly .aicli/memory/memory.db \
  "SELECT version,name FROM schema_migrations ORDER BY version DESC LIMIT 3;"

sqlite3 -readonly .aicli/memory/memory.db \
  "SELECT status,COUNT(*) FROM memory_entries GROUP BY status;"

sqlite3 -readonly .aicli/memory/memory.db \
  "SELECT status,COUNT(*) FROM memory_turn_digests GROUP BY status;"

sqlite3 -readonly .aicli/memory/memory.db \
  "SELECT kind,status,COUNT(*) FROM memory_jobs GROUP BY kind,status ORDER BY COUNT(*) DESC;"

sqlite3 -readonly .aicli/memory/memory.db \
  "SELECT COUNT(*) FROM memory_jobs WHERE status='pending' AND (deadline_at IS NULL OR deadline_at='');"
```

---

## 5. 下一步（G0-5）

1. 备份 `.aicli/memory/memory.db` 及 `-wal/-shm`；
2. 在副本上按当前代码执行 migration v24/v25，并运行 `jobDeadlineRepair` dry-run；
3. 对 101 条 pending digest 做 `replay-preview` 分类，不自动 replay；
4. 对 8 条缺 deadline 的 pending job 生成受控修复计划；
5. 对 54 条 deadline failure 与 14 条 invalid failure 做抽样根因分类（不记录原始 provider 错误）；
6. 受控恢复后，再验证 `memory_entries` active 数、v2 succeeded job 与新会话注入。

---

## 6. 2026-11-23 副本 dry-run 证据

为避免新 runtime 首次打开真实库时自动 migration，本轮只复制：

```text
.aicli/memory/memory.db
.aicli/memory/memory.db-wal
.aicli/memory/memory.db-shm
  -> .tmp/g0-5-copy/
```

然后在副本上以当前代码打开，执行 migration 和只读/dry-run 查询；真实 `.aicli/memory/memory.db` 未迁移、未修复、未清理。

副本观测：

```text
schema=29（v23 副本已成功应用当前 migration）
memory_entries total=88
memory_jobs: cancelled=1 failed=97 pending=10 succeeded=26
memory_turn_digests: done=12 pending=114
job diagnostics=134
deadline plan: dry_run=true scanned=9 eligible=9 skipped=0
digest replay candidates=114
```

结论：

1. v23 → v29 migration 在副本上成功，未观察到 schema 阻断；
2. 当前真实库 backlog 已增长到 114 条 pending digest、10 条 pending job，其中 9 条缺 deadline；
3. `PlanMissingJobDeadlines` 可识别 9 条可修复 job，但本轮未 apply；
4. 114 条 digest replay candidate 仍未分类/恢复；需要 canonical capture 与受控 replay；
5. 副本复制时宿主仍可能写入 WAL，因此 counts 是快照，不是对真实库的持续一致读；
6. 下一步仍应在副本上做 replay-preview 分类、deadline repair apply（需 lease + 二次确认）和 consolidation 增量验证，再决定是否迁移真实库。

---

## 7. 2026-11-23 副本 replay 分类与 deadline repair

在重新复制的最新数据库副本上执行：

- `ListDigestReplayCandidateIDs`；
- `ReplayDigestDryRun`（按 digest 自身的 capture identity 构造，不写 replay）；
- `PlanMissingJobDeadlines`；
- `RepairMissingJobDeadlines`（维护 lease，仅作用于副本）。

结果：

```text
digest replay candidates=116
  already_queued=9
  missing_receipt=17
  not_orphaned=90
  eligible=0
  errors=0

deadline plan: dry_run=true scanned=8 eligible=8 skipped=0
deadline repair: applied=8 updated=8 skipped=0

post-repair jobs:
  failed=100
  pending=9
  succeeded=31
  cancelled=1
```

结论：

1. 当前副本没有可自动 replay 的 orphaned digest；90 条是 not orphaned（有 job/状态需走 operator retry），9 条已 queued，17 条缺 receipt；
2. 8 条缺 deadline 的 job 已能在副本上受控修复，但真实库未 apply；
3. 仍需对 90 条 not_orphaned 和 100 条 failed job 做 operator 分类；不能把“没有 eligible”理解成 backlog 已清空；
4. 真实库仍未 migrate/repair，副本只在临时目录中操作。

---

## 8. 2026-11-24 最新真实库快照、分类与副本恢复演练

### 8.1 一致快照

观测时间：2026-11-24 12:17 CST。

真实库仍为 schema v23，仍有 WAL；使用 SQLite backup API 创建一致副本：

```text
.aicli/memory/memory.db
  -> .tmp/g0-5-20261124/memory.db

PRAGMA integrity_check = ok
```

副本随后由当前代码从 v23 迁移到 **v32**（当前工作区并发改动已推进到
`memory_v32_dream_source_registry`）。真实库未迁移、未修复、未清理。

### 8.2 最新计数

```text
entries=151
  active=115
  candidate=36

jobs=151
  phase1_extract cancelled=1
  phase1_extract failed=10
  phase1_extract succeeded=1
  phase1_extract/v2 failed=97
  phase1_extract/v2 pending=9
  phase1_extract/v2 succeeded=19
  phase2_consolidate succeeded=14

digests=142
  pending=123
  done=19

windows=0
versions=262
summaries=2
```

与 2026-11-23 的副本相比，真实库在继续增长：failed v2 job 97、pending digest
123、active entry 115。

快照后真实库仍在被 host 写入：2026-11-24 12:44 CST 只读复核为 jobs=153、
done digest=21、succeeded v2 job=21；failed=97、pending job=9、pending digest=123
未变化。后续真实操作必须基于停止写入后的新快照，不能直接使用 §8.2 的历史总数。

### 8.3 精确 replay 分类

在已迁移副本上使用当前代码的 `ListDigestReplayCandidateIDs` + 逐 digest
`ReplayDigestDryRun`：

```text
digest_candidates=123
  already_queued=9
  missing_receipt=17
  not_orphaned=97
  eligible=0
  classification_errors=0
```

补充只读核对：

- 106 条 pending digest 有 receipt，全部有 capture generation binding；
- 106 条均有 `process_job_id`：97 条对应 failed job，9 条对应 pending job；
- 17 条无 receipt 的 digest 全部有 user/assistant 文本，来自 9 个 session；
- 17 条无 receipt 的 session 在 canonical `session_events` 中均有历史事件；
- pending digest 中没有 output receipt，也没有匹配的 purge fence；
- 因没有 matching job，17 条缺 receipt 行被 replay classifier 归类
  `missing_receipt`，但它们是 **legacy extraction 候选**，不是可丢弃的无效行。

### 8.4 Failed job operator 分类

```text
failed total=107
  v2 deadline, attempt<max_attempts   = 52
  v2 deadline, attempt>=max_attempts  = 5
  v2 invalid/json parse, attempt>=max = 32
  v2 provider/model, attempt>=max     = 7
  v2 other, attempt>=max             = 1
  legacy failed without digest        = 10
```

失败原因分类：

```text
v2 deadline=57
v2 invalid/json_parse=32
v2 provider/model=7
v2 other=1
legacy invalid=10
```

97 个 v2 failed job 全部有 pending digest；10 个 legacy failed job 没有
`process_job_id` 关联，属于旧路径残留。

### 8.5 Deadline repair

```text
deadline plan: dry_run=true scanned=8 eligible=8 skipped=0
deadline repair: dry_run=false scanned=8 eligible=8 updated=8 skipped=0
```

仅作用于副本。修复后 9 条 pending job 均有 deadline。

### 8.6 现有 operator 动作验证

在第二个副本上验证：

| 动作 | 结果 |
|---|---|
| `RequeueJob`（failed deadline，attempt<max） | `pending`，刷新未来 deadline |
| `RequeueJob` + claim（failed deadline，attempt=max） | claim 后 `attempt=4/3`，可执行一次 operator override；若再失败回到 `failed` |
| `DeadLetterJob`（invalid/json，attempt=max） | job 变 `dead_letter`，digest 仍为 `pending` |
| `DeadLetterJob`（legacy failed，无 digest） | job 变 `dead_letter` |

发现并修复一个 G0-5 阻断点：**dead-letter receipt-backed job 后没有受支持方式
关闭 digest**。新增窄范围恢复操作：

```text
Store.CloseDeadLetterDigest(ctx, digestID, reason)
```

约束：

- 仅处理有 capture receipt + process job 的 digest；
- process job 必须已经是 `dead_letter`；
- 不得已有 output receipt；
- 将 digest 置为 `invalidated`、rollup `skipped`、写 processed_at/reason；
- 写 `memory_job_operator_audit` 的 `close_digest` row；
- 幂等；跨 tenant fail closed。

对应测试：

```text
TestCloseDeadLetterDigestIsAuditedAndTenantScoped
go test ./internal/memorystore -run 'TestCloseDeadLetterDigestIsAuditedAndTenantScoped' -count=1
ok  internal/memorystore  0.907s
```

收尾回归：

```text
go test ./internal/memorystore -count=1
ok  internal/memorystore  101.760s

go test ./internal/api/runtimeapi -run 'TestMemory' -count=1
ok  internal/api/runtimeapi  34.097s

go test ./cmd/aicli/commands \
  -run 'Test(StructuredMemory|ChatMemory|HandleMemoryCommand|MemoryCrossSurface)' -count=1
ok  cmd/aicli/commands  17.832s

go test ./internal/toolkit/tools -run 'TestMemory' -count=1
ok  internal/toolkit/tools  9.560s
```

### 8.7 分类执行计划与副本模拟

建议的真实库动作（分阶段、非自动）：

1. **32 条 invalid/json parse**：`DeadLetterJob` + `CloseDeadLetterDigest`；
2. **52 条 deadline attempt<max**：`RequeueJob`，刷新 deadline；
3. **5 条 deadline attempt=max**：operator 一次性 override retry；
4. **7 条 provider/model attempt=max**：operator 一次性 retry；
5. **1 条 other attempt=max**：保留 failed，人工复核后再决定；
6. **10 条 legacy failed（无 digest）**：`DeadLetterJob` 标记历史终态；
7. **17 条 missing receipt**：交给 legacy digest extraction 路径，不 replay；
8. **9 条 already queued**：deadline repair 后交给 worker。

在第三个副本上按该策略模拟执行，结果：

```text
planned_jobs:
  phase1_extract cancelled=1
  phase1_extract dead_letter=10
  phase1_extract succeeded=1
  phase1_extract/v2 dead_letter=32
  phase1_extract/v2 failed=1
  phase1_extract/v2 pending=73
  phase1_extract/v2 succeeded=19
  phase2_consolidate succeeded=14

planned_digests:
  done/pending=19
  invalidated/skipped=32
  pending/pending=91
```

91 条剩余 pending 的构成：

```text
17 missing_receipt legacy candidates
 9 already queued
57 deadline requeued (52 + 5)
 7 provider requeued
 1 other failed waiting for manual review
```

本轮只模拟到“动作已分类/状态已转移”，没有证明 64 条 requeued job 能在真实
canonical transcript/provider 上重新抽取成功；这需要 P3 的受控 worker 运行与
新会话注入验证。

### 8.8 当前结论

1. G0-5 的 operator 分类已经完成，不再是未知 backlog；
2. deadline repair 和各 operator 动作已在副本上验证；
3. dead-letter → digest close 的缺口已用受支持、可审计的恢复操作补齐；
4. 真实库仍未迁移、未 dead-letter、未 requeue、未关闭任何 digest；
5. 真实库执行前仍需完整 raw db/wal/shm 备份、固定提交和二次确认；
6. P3 仍需验证 17 条 legacy 提取、64 条 requeue 后的真实执行结果、SQLite
   busy/lock/corruption 以及新会话注入。