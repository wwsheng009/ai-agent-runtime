# 存储引用与写入点审计（2026-11-06）

> 目标：回答“这些数据有没有引用/写入点”，区分产品消费、维护性读写、纯写入与死数据。
> 范围：`backend/*.go` 生产代码（排除 `*_test.go`），覆盖 9 个库、32 张表、15 个关键字段。
> 口径：**写入** = `INSERT` / `UPDATE` / `DELETE`；**引用** = `SELECT` / `JOIN`。
> 引用计数包含 purge、retention、compaction 等维护性读取，因此第 2 节另列“产品消费方”。

## 1. 结论

- 27 张表有产品读取方；1 张表是纯记录（只有维护性读取）；4 张表 + 1 个列是死数据。
- `usage_tool_calls.record_json`（653 MB）：除手动 `usage-analytics compact-records` 外**没有任何读取方**。
- `agent_control_agent_wake_events`（498 MB）：所有读取都是 retention 的计数/删除；每行约 200 B 身份字段只有 `session_id` 被 purge 使用。
- `session_messages.preview_json`（703 MB 冗余）：有 3 个读取点，全部在 `sqlite_storage.go`，属于历史列表/回溯快路径。
- `analytics_legacy_*`（约 25 MB）：生产代码 0 写 0 读，只有 purge 删除与脚本引用。
- `agent_control_agent_wake_events.agent_row_id`：写入 959k 行，代码中无任何 `SELECT` → 死列。

## 2. 表级审计

| 表 | 写点 | 读点 | 判定 | 产品消费方 |
|---|---:|---:|---|---|
| `history.sessions` | 5 | 20 | 引用型 | 会话列表、恢复、purge |
| `history.session_messages` | 7 | 21 | 引用型 | 回放、`identity_hash` 回溯、dedupe |
| `history.session_prompt_messages` | 4 | 8 | 引用型（派生） | prompt 重放、前缀校验 |
| `cells.session_cells` | 1 | 2 | 引用型 | transcript body |
| `runtime.session_events` | 7 | 16 | 记录型 + 续传 | resume 回放、trajectory API |
| `runtime.session_tool_receipts` | 4 | 7 | 引用型 | 工具结果配对回放 |
| `runtime.cache_requests` | 4 | 7 | 引用型 | 缓存分析回放 |
| `runtime.runtime_tool_surface_content` | 2 | 1 | 引用型 | 工具面内容寻址 |
| `analytics.usage_requests` | 6 | 29 | 记录型 + 详情 | 用量详情、缓存回放 |
| `analytics.usage_tool_calls` | 3 | 15 | 记录型 | 仅聚合查询；`record_json` 无消费方 |
| `analytics.usage_sessions` | 9 | 14 | 记录型 | 分析列表、purge |
| `analytics.usage_turns` | 2 | 7 | 记录型 | 分析聚合 |
| `analytics.usage_subagents` | 1 | 12 | 记录型 | 子代理分析 |
| `analytics.usage_routes` | 1 | 9 | 记录型 | 路由分析 |
| `analytics.usage_lsp_requests` | 1 | 4 | 记录型 | LSP 分析 |
| `analytics.analytics_legacy_*` | 0 | 0 | 死数据 | 仅 purge 删除 |
| `artifacts.artifacts` | 1 | 7 | 引用型 | 工具产物读取 |
| `artifacts.memory_entries` | 1 | 4 | 引用型 | 记忆召回 |
| `artifacts.checkpoints` | 3 | 11 | 引用型 | checkpoint 恢复 |
| `agent_control.agent_control_agents` | 5 | 8 | 引用型 | agent 注册表 |
| `agent_control.agent_control_agent_wake_events` | 5 | 12 | **纯记录** | 仅 retention 维护 |
| `agent_control.agent_control_global_mailbox_records` | 2 | 5 | 引用型 | 全局邮箱 |
| `background.background_jobs` | 6 | 10 | 引用型 | 后台任务查询 |
| `background.background_job_events` | 2 | 3 | 记录型 | job 详情/日志尾部 |
| `background.runtime_instances` | 2 | 1 | 死表 | `ListRuntimeInstances` 无调用方 |
| `team.team_events` | 2 | 5 | 引用型 | 团队事件 |
| `team.teammates` | 6 | 8 | 引用型 | 团队调度 |
| `team.teams` | 5 | 10 | 引用型 | 团队状态 |
| `schedule.schedule_tasks` | 8 | 12 | 引用型 | 定时任务 |
| `schedule.schedule_pending_messages` | 3 | 2 | 引用型 | 投递队列 |

## 3. 引用型：写入点与引用点

### history

- `session_messages`
  - 写：`backend/internal/chat/sqlite_storage.go:2422`（INSERT）、`backend/internal/chat/sqlite_storage_identity.go:209`（UPDATE identity_hash）、`sqlite_storage.go:1068/1188/3216`（DELETE）。
  - 读：`sqlite_storage.go:1121/2763/2908`、`sqlite_storage_identity.go:76/108/133/158`、`sqlite_storage_snapshot.go:219`。
- `session_prompt_messages`
  - 写：`sqlite_storage.go:2358/2374`（INSERT）、`2370/3219`（DELETE）。
  - 读：`sqlite_storage.go:1908/2081/2275/2316`（prompt 重放）、`sqlite_storage_snapshot.go:220`。
- `sessions`
  - 写：`sqlite_storage.go:1718`（UPSERT）、`1727`、`3223`。
  - 读：`sqlite_storage.go:1839/2728/3093` 等 20 处。

### runtime

- `session_tool_receipts`
  - 写：`backend/internal/chat/session_runtime_store.go:2711`（INSERT）、`2653/2780`（DELETE）、`4905`（UPDATE）。
  - 读：`session_runtime_store.go:2746/2802`（工具回放）、`runtime_retention.go:195`（保留策略）。
- `session_events`
  - 写：`session_runtime_store.go:2952/2983/3478/3560`、`session_runtime_store_batch.go:137`。
  - 读：`session_runtime_store.go:4013/4092`（回放）、`internal/api/runtimeapi/trajectory_events.go`、`runtime_retention.go:295/306`。
- `cache_requests`
  - 写：`session_runtime_store_cache.go:61`（INSERT OR REPLACE）、`121`（DELETE）。
  - 读：`session_runtime_store_cache.go:91`（按会话回放缓存记录）、`runtime_retention.go:261`。
- `runtime_tool_surface_content`
  - 写：`session_runtime_store.go`、`runtime_retention.go`；读：`session_runtime_store.go`（按 fingerprint）。

### analytics

- `usage_requests`
  - 写：`backend/internal/usageanalytics/ingest.go:676`、`store_cache_ratio.go:61`、`store_stats.go:359/439/443`。
  - 读：`request_query.go:171`、`cachesource.go:414/458/592/624`、`query.go:983`、`query_v2.go` 聚合。
- `usage_tool_calls`
  - 写：`ingest_v2.go:172/248`；读：仅 `tool_record_compaction.go:89`（维护）。
- `usage_sessions / usage_turns / usage_subagents / usage_routes / usage_lsp_requests`
  - 写：`ingest_stats.go`、`ingest_v2.go`、`ingest_routes.go`、`ingest_lsp.go`。
  - 读：`query_v2.go`、`query_lsp.go`、`sessionpurge/candidates.go`（按龄保护/删除）。

### artifacts

- `artifacts`：写 `backend/internal/artifact/store.go:276`；读 `store.go:318/419/461`。
- `memory_entries`：写 `store.go:535`；读 `store.go:632`。
- `checkpoints`：写 `store.go:705/747`；读 `store.go:771/822/875`、`checkpoint_files.go:236/309`。
- `blobs / checkpoint_files`：当前 0 行，但代码路径完整（内容寻址），`checkpoint_files.go:55/121` 读取。

### agent_control / background / team / schedule

- `agent_control_agents`：写 `global_agent_store.go:424/824/959/1006`；读 `588/702/1341`。
- `agent_control_global_mailbox_records`：写/读均在 `global_mailbox_store.go`。
- `background_jobs`：写 `backend/internal/background/store.go:324/474/490/546`；读 `704` 等。
- `background_job_events`：写 `store.go:942`；读 `store.go:975`（job 详情/日志尾部）。
- `teams / teammates / team_events`：写读均在 `backend/internal/team/`。
- `schedule_tasks / schedule_pending_messages`：写读均在 `backend/internal/schedule/`。

## 4. 纯记录型：有写入、无产品读取

| 表/字段 | 写入点 | 读取点 | 说明 |
|---|---|---|---|
| `agent_control_agent_wake_events` | `global_agent_store.go:1454` | `1076/1165/1178/1489`（取 id 删除）、`1202`（计数）、`1427`（MAX id） | 全部是 retention 维护 |
| `usage_tool_calls.record_json` | `ingest_v2.go:172/248`、`tool_record_compaction.go:116` | `tool_record_compaction.go:89` | 仅手动 compact 命令读取 |
| `runtime_instances` | `background/store.go` | `store.go:1268`（实例列表） | 仅运维展示 |
| `analytics_legacy_llm_requests / turns / sessions` | 无 | 无 | 历史归档残留 |

## 5. 死数据与死列

- **死表**：`analytics_legacy_llm_requests`（35,129 行）、`analytics_legacy_turns`（1,891）、`analytics_legacy_sessions`（4,131）。生产代码 0 写 0 读；只有 `backend/internal/sessionpurge/stores.go:378-380` 删除和 `scripts/usage-analytics-*.ps1` 引用。
- **死表**：`background.runtime_instances`（1,529 行）。写入在 `background/store.go`，唯一读取方法 `ListRuntimeInstances`（`store.go:1255`）在全仓无调用方。
- **死列**：`agent_control_agent_wake_events.agent_row_id`。写入在 `global_agent_store.go:1455`，DDL 在 `1715`，全仓无 `SELECT`。

## 6. 关键字段审计

| 字段 | 写入 | 读取 | 判定 |
|---|---|---|---|
| `session_messages.payload_json` | `sqlite_storage.go:2422` | 回放/回溯/快照多处 | 引用型，权威内容 |
| `session_messages.preview_json` | `sqlite_storage.go:2423` | `1120/2763/2908` | 有引用，但仅历史快路径；等于 payload 时冗余 |
| `session_prompt_messages.payload_json` | `2358/2374` | `1908/2081/2275/2316` | 引用型（派生） |
| `session_tool_receipts.message_json` | `session_runtime_store.go:2711` | `2746/2802`、`runtime_retention.go:194` | 引用型，工具回放权威 |
| `session_events.payload_json` | `session_runtime_store*.go` | `4013/4092`、trajectory API | 引用型 |
| `cache_requests.record_json` | `session_runtime_store_cache.go:61` | `91` | 引用型（缓存分析） |
| `usage_requests.record_json` | `ingest.go:676` | `cachesource.go:414/458/592/624`、`request_query.go:171`、`query.go:983` | 引用型 |
| `usage_tool_calls.record_json` | `ingest_v2.go:172/248` | 仅 `tool_record_compaction.go:89` | **无产品消费方** |
| `checkpoints.ledger_json` | `artifact/store.go:706/748` | `771/822/875` | 引用型 |
| `memory_entries.source_refs_json` | `artifact/store.go:536` | `632` | 引用型 |
| `memory_entries.source_hash` | `536` | `538`（ON CONFLICT）、`632` | 引用型（去重键） |
| `background_jobs.log_path` | `background/store.go:324/474/490/546` | `704`（job SELECT） | 引用型 |
| `sessions.head_offset` | `sqlite_storage.go:1727`、`session_runtime_store.go:2601` | `sqlite_storage.go:1840/2729/3094` | 引用型 |
| `session_messages.identity_hash` | `sqlite_storage_identity.go:209` | `108/158` | 引用型（回溯/去重） |
| `agent_control_agent_wake_events.agent_row_id` | `global_agent_store.go:1455` | 无 | 死列 |

## 7. 对优化方案的影响

- 上一版报告建议“analytics 去正文、只存指标”成立：`usage_tool_calls.record_json` 确实没有查询方，删除不会破坏任何 API。
- `wake_events` 可以直接改为“状态表 + 跃迁日志”：现有读取全是 retention 维护，不涉及业务语义。
- `preview_json` 不能直接整列删除，必须先保留 3 个读取点的回退逻辑；建议“相同即写 NULL”，而不是删列。
- `analytics_legacy_*` 可立即 drop；`agent_row_id` 可在下次 schema 迁移中删除。
- `runtime_instances` 可直接下线：`ListRuntimeInstances` 没有调用方，不存在 UI 依赖。

## 8. 附录：扫描命令

```bash
# 表级写入/引用点
rg -n --glob '*.go' --glob '!*_test.go' 'INSERT\s+(OR\s+\w+\s+)?INTO\s+<table>\b|UPDATE\s+<table>\b|DELETE\s+FROM\s+<table>\b' backend
rg -n --glob '*.go' --glob '!*_test.go' 'FROM\s+<table>\b|JOIN\s+<table>\b' backend

# 字段级
rg -n --glob '*.go' --glob '!*_test.go' '\b<column>\b' backend
```
