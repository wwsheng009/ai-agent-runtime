# Usage 分析页查询性能优化：实施与验证记录

- 日期：2026-09-17
- 方案：`docs/plan/usage-analytics-query-performance-optimization-plan-20260917.md`
- 状态：Phase 1–4 已实施并通过验证
- 运行库实测基线：`backend/data/runtime/usage_analytics.sqlite`（35,996 请求 / 386 会话）

## 1. 实施内容

### Phase 1（P0，核心）：预聚合统计列 + 增量维护 + 回填 + 读路径切换

| 项 | 落地位置 | 说明 |
|---|---|---|
| v3 迁移（15 个计数列 + turn 去重表） | `internal/usageanalytics/store_stats.go` | `user_version=2→3`，单事务；列按 `PRAGMA table_info` 判重后 `ALTER TABLE ADD COLUMN`；失败回滚保持 v2 可读 |
| 历史回填/对账 SQL | `store_stats.go:rebuildSessionStatsTx` | 从 `usage_requests` 一次聚合重建全部计数列 + `usage_session_turn_keys`；迁移与 `rebuild-stats` 复用同一实现 |
| 写入路径事务化 + delta | `ingest_stats.go` | 单事务：读旧行 → UPSERT 请求 → UPSERT 会话元数据 → 会话计数增量 → turn 去重键维护；重复终态事件 delta=0，不重复计数 |
| turn 键变更清理 | `ingest_stats.go:removeStaleTurnKeyTx` | 同请求 turn 键变化时，旧键无其他请求引用则移除并回退计数（消除方案 §5.2 记录的"偏高 1"已知偏差） |
| 读路径切换 + 回退 | `query.go:sessionQuerySource/sessionStatsSelect` | `ListSessions/Summarize/Dimensions/sessionRollup` 单表读 `usage_sessions`；`statsReady()=false` 时自动回退旧 `sessionSelect` |
| 逃生开关 | `store_stats.go:EnvDisableStats` | `AICLI_USAGE_ANALYTICS_DISABLE_STATS=1` 强制读路径回退（只影响读；写入侧继续维护计数） |
| 对账/修复 | `store_maintenance.go`、`cmd/aicli/commands/usage_analytics_command.go` | `Store.RebuildSessionStats/RebuildAllSessionStats`；`aicli usage-analytics rebuild-stats --session <id> \| --all` |
| 健康漂移抽样 | `query_v2_drift.go`、`query_v2_health.go` | `/api/runtime/status` → `usage_analytics.stats_ready` + `stats_drift`（随机抽样 3 会话，15 字段逐项对比） |
| 写事务 DSN | `store.go:writableDSN` | `_txlock=immediate`：避免 deferred 事务在"先读后写"升级路径上遇并发写锁立即 `SQLITE_BUSY` |

### Phase 2（P0）：首屏端点合并

- 后端 `/api/runtime/analytics/overview` 由 `GetAnalyticsSummary` 别名改为一次性引导端点（`analytics_handlers.go:GetAnalyticsOverview`）：返回 `sessions + summary + dimensions + matched`，顶层 `matched` 保持兼容。
- 前端 `overview.tsx` 首屏 3 个主数据请求合并为 1 个 `getAnalyticsOverview`；`getAnalyticsSummary` 改回 `/analytics/summary`（provider/model 分布延迟加载继续使用）。

### Phase 3（P1）：Dimensions 单查询 + 服务端缓存

- `Dimensions` 由 5 次 DISTINCT 扫描合并为 1 次 `UNION ALL`（每段独立 `ORDER BY + LIMIT 1000`，Go 侧排序保证输出稳定）。
- `service_cache.go`：`ListSessions/Summarize/Dimensions` 5s TTL 进程内缓存（键 = op + Query 全字段），写入代数（`Store.statsGen`）变化立即失效；不缓存 `SessionUsage`/错误模式等明细路径。

### Phase 4（P2）：首屏瘦身 + 索引 + 保留期

- `overview.tsx`：`subagents/errors/health` 移出首屏 `Promise.all`，首屏数据就位后延迟 300ms 加载（卸载/切换自动清理定时器）。
- 错误模式部分索引：`idx_usage_requests_error_category ... WHERE error_category <> ''`（旧库缺列时自动跳过）。
- 保留期清理：`Store.PruneBefore` + `Store.Vacuum`；`aicli usage-analytics prune --before <date> [--vacuum]`（删旧请求明细 + 重建统计，会话元数据保留）。

## 2. 实测结果（真实库快照，35,996 请求 / 386 会话）

方法：复制运行库（含 WAL）→ 新代码可写打开（含 v3 迁移 + 回填）→ 同一进程内对同一库分别测量新路径与旧路径（`AICLI_USAGE_ANALYTICS_DISABLE_STATS=1`），每项预热 1 次后取多次最优/均值。

| 查询 | 改造前（旧 CTE 聚合） | 改造后（预聚合列） | 提升 |
|---|---|---|---|
| `ListSessions(limit=50)` | 603.7 ms | **5.2 ms** | ≈117× |
| `Summarize(group_by=day)` | 454.5 ms | **2.6 ms** | ≈175× |
| `Summarize(group_by=provider)` | ≈470 ms | **2.0 ms** | ≈230× |
| `Dimensions`（合并前 5 次扫描） | 1244.0 ms | **2.1 ms** | ≈600× |
| v3 迁移 + 全量回填（一次性，Open 内） | — | **327 ms** | — |
| 漂移抽样（3 会话 × 15 字段） | — | 0 drifted | — |

- 首屏后端串行成本：改造前 6+2 请求 ≈3.2–7.9s（与方案 §2.8 基线一致）；改造后 ≈14ms（单次 overview ≈6ms），满足 "<500ms" 目标。
- 迁移在 `Open()` 内完成（未对外提供查询），实测 0.33s；超大库（>100 万请求）时回填成本线性增长，届时按方案 §10 改为后台分批。
- ingest 写入吞吐（§11.3 基准，1500 次/轮 ×3，取中位数）：
  - 改造前基线（原 DSN + 两条独立 UPSERT）：≈1.01 ms/op
  - 改造后（事务 + delta + turn 维护）：≈1.00 ms/op
  - 比值 ≈1.0，满足"不低于基线 −10%"；单事务替代两次独立提交抵消了新增语句成本。

## 3. 验证清单

| 验证 | 位置 | 结果 |
|---|---|---|
| 增量计数（重复终态不重复计数、token 覆盖、turn 失败→成功回退、duration=0 样本） | `store_stats_incremental_test.go` | PASS |
| golden：旧/新路径列表 + 汇总（6 种 group_by）+ 维度逐字段一致 | `store_stats_golden_test.go` | PASS |
| v2 旧库迁移 v3 + 回填正确 + 重复 Open 幂等 | `store_migrate_v3_test.go` | PASS |
| 只读 v2 库不迁移且回退旧路径、结果正确 | `store_migrate_v3_test.go` | PASS |
| `EXPLAIN QUERY PLAN`：新路径不扫 `usage_requests`（旧路径对照组必须扫） | `query_stats_plan_test.go` | PASS |
| 缓存 TTL/代数失效、写入后读到新值 | `service_cache_test.go` | PASS |
| 漂移抽样发现人为漂移、rebuild 后归零 | `store_stats_incremental_test.go` | PASS |
| CLI rebuild/prune/参数校验/全库重建 | `usage_analytics_command_test.go` | PASS |
| 存量回归：`go test ./internal/usageanalytics/ ./internal/api/skills/ ./internal/cacheanalytics/ ./internal/runtimeserver/ -run ...` | — | PASS |
| 前端：`npx tsc -b`、`npx vitest run`（298 文件 / 2420 用例） | — | PASS |
| `/analytics/overview` 引导载荷（sessions/summary/dimensions/matched） | `analytics_handlers_test.go` | PASS |

基准脚本：`scripts/usage-analytics-endpoint-bench.ps1`（Phase 0.1 固化，端点级 P50/P95，可对改造前后留档对比）。

## 4. 运维与回退

- **上线**：需重启 runtime-server / aicli（迁移在 `Open()` 执行）；停机窗口内旧进程不受影响（`user_version` 门控 + 新列向后兼容）。
- **回退读路径**：`AICLI_USAGE_ANALYTICS_DISABLE_STATS=1`（无需回滚版本）；恢复后如担心漂移，执行 `aicli usage-analytics rebuild-stats --all`。
- **漂移处置**：`/api/runtime/status` → `usage_analytics.stats_drift`（`checked/drifted/samples`）；`drifted>0` 时用 rebuild 修复。
- **保留期**：`aicli usage-analytics prune --before 2026-01-01 [--vacuum]`；建议按季度执行并配合 `VACUUM`。
- **只读工具兼容**：只读打开不迁移；旧库（v2 / 部分 schema）自动回退旧读路径，不报错。

## 5. 遗留与决策

1. **`average_response_time_ms` 语义（方案 §15 待拍板）**：本期按 golden 要求逐字段兼容，仍为"各会话均值的和"（`SUM(c_avg_duration_ms)`），未改为"总时长/总请求数"。前端未消费该字段做关键展示时建议单独提改进项；若确认是缺陷，应同时改后端与前端口径。
2. **部分 schema 旧库**：`usage_requests` 缺列（v1 裁剪库）时不启用 v3，保持 v2 与旧读路径（`requestsStatsColumnsComplete` 门控）。
3. **`usage_turns` 采集不全**：turn 统计由 `usage_session_turn_keys` 维护，与旧 `COUNT(DISTINCT)` 口径一致（见方案 §5.2 验证）。
4. **前端骨架屏**：首屏后端已降至毫秒级，未新增分区骨架屏；保留现有加载态与错误提示。
5. **无关的存量失败**：`cmd/aicli/commands` 中 `TestLocalHostPipelinePersistsToolObservability`、`TestHandleChatWebAPIAnalysis_ToolsSubagentsErrors` 失败与本次改动无关（工具百分位/错误 Top-N 延迟到 `ToolStatsDetail` 的既有实现未同步测试，见方案 §2.7）。
