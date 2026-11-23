# G0-5 Dogfood 记忆库只读诊断（2026-11-23）

## 状态

**已完成真实 `.aicli/memory/memory.db` 的只读诊断；未执行 dry-run、修复、迁移、清理或写入。**

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