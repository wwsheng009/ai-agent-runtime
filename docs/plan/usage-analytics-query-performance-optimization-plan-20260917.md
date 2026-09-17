# Usage 分析页查询性能优化实施方案（预聚合统计表）

- 日期：2026-09-17
- 状态：待评审
- 关联文档：`docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md`（原始设计，§6.3 曾规划 `analytics_daily_rollups`）、`docs/plan/session-analytics-runbook.md`

## 1. 背景

`http://localhost:5193/usage` 首屏加载超过 10 秒。用户核心诉求：

1. 统计数据与会话列表是否分开加载？
2. 是否有独立的统计表，在请求/会话结束时增量更新？
3. 统计应从统计表直接读取，而不是每次从原始表重新聚合。

审查结论：**没有独立统计表**；所有统计在查询时从原始表（`usage_requests` 等）实时聚合，存在严重的读放大与串行化问题。

## 2. 现状审查（代码证据）

### 2.1 首屏请求清单

前端 `frontend/src/pages/usage-analytics/overview.tsx`：

- L104–119：首屏 `Promise.all` 并发 6 个请求：
  `listAnalyticsSessions`、`getAnalyticsSummary(group_by)`、`getAnalyticsDimensions`、`getAnalyticsSubagents`、`listAnalyticsErrors`、`getUsageAnalyticsHealth`
- L132–139：首屏渲染后再延迟加载 2 个请求：`getAnalyticsSummary(provider)`、`getAnalyticsSummary(model)`

### 2.2 读放大根因：`sessionSelect` 的 `req` CTE 无谓词下推

`backend/internal/usageanalytics/query.go:113`：

```sql
WITH req AS (
  SELECT session_id, COUNT(*), SUM(...), COUNT(DISTINCT ...)   -- 全表聚合，无法下推
  FROM usage_requests GROUP BY session_id
)
SELECT ... FROM usage_sessions s LEFT JOIN req ON req.session_id = s.session_id
WHERE <过滤器>          -- 只作用于 usage_sessions；req 已扫完全表
```

`req` CTE **每次都把整张 `usage_requests` 表按 session 分组聚合**，时间窗 / provider / model 等过滤条件无法下推进 CTE。`COUNT(DISTINCT ...)`（`turn_count` / `failed_turns`）是最贵部分。

### 2.3 每次页面加载对 `usage_requests` 的全量聚合次数

| 请求 | 查询实现 | 全表聚合次数 |
|---|---|---|
| `listAnalyticsSessions` | `ListSessions`：COUNT + 分页（`query.go:355`） | 2 |
| `getAnalyticsSummary(day)` | `Summarize`：`aggregateTotals`（`query.go:421`）+ GROUP BY | 2 |
| `getAnalyticsDimensions` | `Dimensions`：5 个 `distinctValues`（`query.go:629/664`） | 5 |
| `listAnalyticsErrors` | 3 源扫描（`query_v2.go:331/353/375`） | 1（usage_requests）+ 2 小表 |
| `getAnalyticsSummary(provider)` | 延迟加载 | 2 |
| `getAnalyticsSummary(model)` | 延迟加载 | 2 |
| **合计** | | **≈13 次（首屏约 9 次）** |

### 2.4 串行化

`backend/internal/usageanalytics/store.go:134`：`db.SetMaxOpenConns(1)`（PRAGMA 连接级设置所必需）。所有查询在 DB 层严格串行，6 个"并行"HTTP 请求实际排队累加延迟。

### 2.5 现有表结构：无统计表

`store.go:265–387` 共 5 张原始事件表：

```
usage_requests / usage_sessions / usage_tool_calls / usage_subagents / usage_turns
```

`usage_sessions` 仅含元数据（title、provider、project、status、时间戳），**没有任何计数列**。

### 2.6 已具备的改造条件

写入侧已有天然钩子，每次请求终态与会话终态都会调用 upsert：

- `ingest.go:261–265`：`llm.request.finished` → `upsertRequest` + `upsertSession`
- `ingest.go:325–334`：`session_end` → 悬挂请求兜底 `upsertRequest` + `upsertSession`
- `upsertRequest` 已是幂等 `ON CONFLICT(llm_request_id) DO UPDATE`（`ingest.go:367–393`）

即：**只需在同一写入事务中顺带维护计数，无需新增采集链路**。

### 2.7 已完成的缓解措施（本次会话）

- 前端：provider/model summary 延迟加载；Dimensions 缓存 60s
- 后端：`summary` 合并调用（`/analytics/overview` 路由已存在，当前仅是 `GetAnalyticsSummary` 别名，`handler.go:770`）
- 后端：复合索引 `idx_usage_sessions_time_provider_model_status`（`store.go:312`）
- 后端：工具百分位 / 错误 Top-N 延迟到 `ToolStatsDetail`（`query_v2.go:181`）

上述措施只减少请求数，**未消除读放大**，因此 10s 问题依旧。

### 2.8 实测基线（2026-09-17，runtime-server 8101 直连）

服务端 `/api/runtime/status` 回报的真实运行库：

```
db_path        = E:\projects\ai\ai-agent-runtime\backend\data\runtime\usage_analytics.sqlite
attached       = true, degraded = false
ingested_total = 36571
table_counts   = { requests: 35796, sessions: 385, tool_calls: 382, turns: 8, subagents: 0 }
```

关键比例：**385 会话承载 35,796 请求（≈93 请求/会话）**，即每次全表聚合要处理约 3.6 万行；会话级统计只需 385 行。

各端点实测（curl，同一台机器，与首屏 6 请求一一对应）：

| 端点 | 耗时 | 对应全表聚合次数 | 单次扫描成本（估） |
|---|---|---|---|
| `/analytics/summary?group_by=day` | **2.04s** | 2（totals + GROUP BY，含 COUNT(DISTINCT)） | ≈1.0s |
| `/analytics/sessions?limit=50` | 0.66s | 2（COUNT + 分页） | ≈0.33s |
| `/analytics/dimensions` | 1.09s | 5 | ≈0.22s |
| `/analytics/errors?top=10` | 0.08s | 1（小结果集） | – |
| `/analytics/subagents?limit=200` | 0.002s | 1（空表） | – |

外推首屏总成本：6 个首屏请求 ≈ 3.9s，**加 2 个延迟 provider/model summary（各 ≈2s）≈ 7.9s 后端串行耗时**；再叠加 Vite dev server（5193 为 `vite.js` 进程，代理到 8101）的开发态模块转换开销，即用户观测到的 >10s。

> 结论：**性能问题 = 全表聚合次数 × 单次扫描成本**，与 2.2/2.3 的静态分析一致；Phase 1 落地后每次查询只扫 385 行，单次成本降至毫秒级。
> 注意：`~/.aicli/sessions/runtime/usage_analytics.sqlite`（32MB、v1 表为空、`analytics_legacy_*` 为旧库归档）是 **aicli CLI 侧的另一份库**，不是本页面的数据源，复盘时勿混淆。

## 3. 目标与非目标

### 3.1 目标

| 指标 | 现状 | 目标 |
|---|---|---|
| 首屏对 `usage_requests` 的全表聚合次数 | ≈9 | **0** |
| 首屏 API 总耗时（P95，本机 3.6 万行实测基线 7.9s） | 7.9s | **<500ms** |
| 统计查询扫描行数 | 请求行数 | 会话行数（≈1/10~1/50） |
| 新增/变更请求终态写入成本 | 1 UPSERT | 1 事务内 UPSERT + 1 增量 UPDATE |
| 采集写入吞吐（ingest 回归） | 基线值 | 不低于基线 −10% |

### 3.2 非目标

- 不改动事件采集协议（EventBus 载荷不变）
- 不改动 `/api/runtime/analytics/*` 响应字段语义（前端零改或最小改）
- 不引入外部依赖（继续单文件 SQLite）

## 4. 总体方案

分四个阶段，P0 完成后即可根治问题：

| 阶段 | 内容 | 优先级 |
|---|---|---|
| Phase 1 | 预聚合计数列 + 写入增量维护 + 一次性回填 + 读路径切换 | P0 |
| Phase 2 | 首屏端点合并（`/analytics/overview` 扩展为真正的一次性引导端点） | P0 |
| Phase 3 | Dimensions 单查询合并 + 服务端短 TTL 缓存 | P1 |
| Phase 4 | 首屏瘦身（subagents/errors/health 延迟）+ 错误模式索引 | P2 |


## 5. 详细设计（Phase 1）

### 5.1 DDL：`usage_sessions` 增加预聚合计数列

迁移版本 `user_version = 2 → 3`。所有列 `NOT NULL DEFAULT 0`，对既有行安全：

```sql
ALTER TABLE usage_sessions ADD COLUMN c_total_requests        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_llm_successes         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_llm_errors            INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_requests_with_usage   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_total_tokens          INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_prompt_tokens         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_completion_tokens     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_cached_tokens         INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_reasoning_tokens      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_total_duration_ms     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_duration_samples      INTEGER NOT NULL DEFAULT 0;  -- duration_ms<>0 的行数
ALTER TABLE usage_sessions ADD COLUMN c_turn_count            INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_failed_turns          INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_sessions ADD COLUMN c_first_started_at      INTEGER NOT NULL DEFAULT 0;  -- 替代 req.first_started
ALTER TABLE usage_sessions ADD COLUMN c_last_started_at       INTEGER NOT NULL DEFAULT 0;  -- 替代 req.last_started
```

语义对齐（必须与旧 `sessionSelect` 完全一致，`query.go:113–161`）：

| 旧表达式 | 新增列 / 计算 |
|---|---|
| `COUNT(*)` | `c_total_requests` |
| `SUM(CASE WHEN success=1 ...)` | `c_llm_successes` |
| `SUM(CASE WHEN success=0 ...)` | `c_llm_errors` |
| `SUM(CASE WHEN usage_available=1 ...)` | `c_requests_with_usage` |
| `SUM(total_tokens)` 等 5 列 | `c_total_tokens` / `c_prompt_tokens` / `c_completion_tokens` / `c_cached_tokens` / `c_reasoning_tokens` |
| `SUM(duration_ms)` | `c_total_duration_ms` |
| `AVG(NULLIF(duration_ms,0))` | `c_total_duration_ms / NULLIF(c_duration_samples,0)`（Go 侧同式） |
| `MIN(NULLIF(started_at,0))` | `c_first_started_at`（0 视作缺失，取最小非 0） |
| `MAX(started_at)` | `c_last_started_at` |
| `COUNT(DISTINCT COALESCE(NULLIF(trace_id,''),NULLIF(turn_id,''),llm_request_id))` | `c_turn_count`（维护见 5.2） |
| 失败 turn 去重计数 | `c_failed_turns`（维护见 5.2） |
| `sessionStartExpr`（`query.go:57`） | `COALESCE(NULLIF(s.started_at_unix_nano,0), NULLIF(s.c_first_started_at,0), 0)` |
| `sessionLastExpr`（`query.go:60`） | `MAX(COALESCE(s.updated_at,0), COALESCE(s.ended_at,0), COALESCE(s.c_last_started_at,0))` |

### 5.2 turn 去重表（解决 `COUNT(DISTINCT)` 不可增量问题）

```sql
CREATE TABLE IF NOT EXISTS usage_session_turn_keys (
  session_id TEXT NOT NULL,
  turn_key   TEXT NOT NULL,          -- COALESCE(NULLIF(trace_id,''),NULLIF(turn_id,''),llm_request_id)
  failed     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (session_id, turn_key)
) WITHOUT ROWID;
```

维护规则（与请求终态写入同事务）：

1. `INSERT OR IGNORE INTO usage_session_turn_keys(session_id,turn_key,failed) VALUES(?,?,?)`
2. 若 `changes() = 1`（新 turn）：
   `UPDATE usage_sessions SET c_turn_count = c_turn_count + 1 [, c_failed_turns = c_failed_turns + 1] WHERE session_id = ?`
3. 若已存在且本次为失败：`UPDATE usage_session_turn_keys SET failed = 1 WHERE session_id=? AND turn_key=? AND failed=0`；
   若 `changes() = 1`（失败状态首次出现）：`c_failed_turns = c_failed_turns + 1`

已知偏差场景：同一 `llm_request_id` 在重试中 `trace_id/turn_id` 由空变为非空时，可能残留一条回退键（`llm_request_id`）行，导致 `c_turn_count` 偏高 1。由 §6 对账命令重建修复。

> 设计验证：实测量得 `usage_turns` 仅 8 行，而同一时刻接口口径的 turns 为 878（`usage_turns` 只在 `session_end` 落库，多数会话尚未终结或未触发）。**因此不能把 turn 计数改为从 `usage_turns` 派生**，§5.2 的去重表方案是必要的（而不是可选优化）。

### 5.3 写入路径改造（幂等 delta）

`upsertRequest`（`ingest.go:338`）的 `ON CONFLICT DO UPDATE` 会覆盖计数列，**不能简单累加**，否则重复终态事件（`TestIngestRequestTerminalIsIdempotent` 已覆盖此场景）会重复计数。改为**同事务 delta**：

```
BEGIN IMMEDIATE
  1) SELECT success, usage_available, total_tokens, prompt_tokens, completion_tokens,
            cache_read_tokens, reasoning_tokens, duration_ms, started_at_unix_nano
     FROM usage_requests WHERE llm_request_id = ?
     -- 不存在则旧值全 0
  2) 计算 delta = new - old（逐列）
  3) UPSERT usage_requests（保持现有 ON CONFLICT 语义不变）
  4) UPSERT usage_sessions 元数据（现有 upsertSession 逻辑）
  5) UPDATE usage_sessions SET c_* = c_* + delta, 
       c_duration_samples = c_duration_samples + (new.duration_ms<>0) - (old.duration_ms<>0),
       c_first_started_at = CASE WHEN c_first_started_at = 0 THEN new.started ELSE MIN(c_first_started_at, new.started) END,
       c_last_started_at  = MAX(c_last_started_at, new.started)
     WHERE session_id = ?
  6) turn 去重维护（§5.2）
COMMIT
```

要点：

- 需要新增 `Store.beginTxWithLockRetry()` 帮助函数（复用现有 `execWithLockRetry` 的重试策略，`store.go` 中已有实现）
- `started_at` 的 CASE 保持旧值语义（`ingest.go:384`）与 delta 无关；但 `c_first_started_at/c_last_started_at` 需用「生效后」的值参与计算
- 会话行不存在时先 `INSERT OR IGNORE INTO usage_sessions(session_id, started_at_unix_nano)` 再 UPDATE
- `onSessionTerminal` 的兜底 `upsertRequest`（`ingest.go:325`）自动走同一逻辑，无需单独改造

### 5.4 读路径改造

新增 `sessionStatsSelect`（单表）：

```sql
SELECT s.session_id, s.title, s.project_path, s.working_directory, s.provider, s.model,
       s.protocol, s.status, s.started_at_unix_nano, s.ended_at_unix_nano, s.updated_at_unix_nano,
       s.c_total_requests, s.c_llm_successes, s.c_llm_errors, s.c_requests_with_usage,
       s.c_total_tokens, s.c_prompt_tokens, s.c_completion_tokens, s.c_cached_tokens, s.c_reasoning_tokens,
       s.c_total_duration_ms,
       CASE WHEN s.c_duration_samples > 0 THEN s.c_total_duration_ms / s.c_duration_samples ELSE 0 END,
       s.c_turn_count, s.c_failed_turns,
       COALESCE(NULLIF(s.started_at_unix_nano,0), NULLIF(s.c_first_started_at,0), 0),
       MAX(COALESCE(s.updated_at_unix_nano,0), COALESCE(s.ended_at_unix_nano,0), COALESCE(s.c_last_started_at,0))
FROM usage_sessions s
WHERE <过滤器>     -- 全部落在 usage_sessions 单表列上
```

作用范围：

| 方法 | 改造 |
|---|---|
| `ListSessions`（`query.go:355`） | 直接换用 `sessionStatsSelect`；可选：用 `COUNT(*) OVER()` 把 2 次扫描并为 1 次 |
| `Summarize`（`query.go:517`） | 换用 `sessionStatsSelect`；totals 与 GROUP BY 各 1 次单表扫描（表已缩小一个数量级） |
| `Dimensions`（`query.go:629`） | 换用 `sessionStatsSelect`；5 次 DISTINCT 变单表扫描（Phase 3 再合并为 1 次） |
| `SessionUsage`（`query.go:695`） | 不变（单会话明细仍需 `usage_requests`） |

**兼容回退**：读路径根据 `store.statsReady()`（`user_version >= 3` 且列存在）二选一；只读打开（`openReadOnly`，不跑迁移）或旧库自动回退到旧 `sessionSelect`，保证离线工具与旧库文件不失效。

## 6. 迁移、回填与对账

### 6.1 迁移步骤（`store.go` 的 `migrate()` 扩展）

现有迁移为幂等语句列表（`store.go:260–393`）。新增：

1. 读 `PRAGMA user_version`（当前=2）
2. 若 `< 3`：
   - 用 `PRAGMA table_info(usage_sessions)` 检查并按需执行 §5.1 的 `ALTER TABLE ... ADD COLUMN`（SQLite 不支持 `ADD COLUMN IF NOT EXISTS`，需自行判重）
   - 创建 `usage_session_turn_keys`（§5.2）
   - 执行一次性回填（6.2）
   - `PRAGMA user_version = 3`
3. 整体在单事务内执行；失败回滚并报警（复用 `reportWriteFailure` 风格）

### 6.2 历史回填（SQL 一次性）

```sql
-- 1) 请求聚合成临时表（一次扫描 usage_requests）
CREATE TEMP TABLE req_agg AS
SELECT session_id,
       COUNT(*) AS total_requests,
       SUM(CASE WHEN success=1 THEN 1 ELSE 0 END) AS llm_successes,
       SUM(CASE WHEN success=0 THEN 1 ELSE 0 END) AS llm_errors,
       SUM(CASE WHEN usage_available=1 THEN 1 ELSE 0 END) AS requests_with_usage,
       COALESCE(SUM(total_tokens),0) AS total_tokens,
       COALESCE(SUM(prompt_tokens),0) AS prompt_tokens,
       COALESCE(SUM(completion_tokens),0) AS completion_tokens,
       COALESCE(SUM(cache_read_tokens),0) AS cached_tokens,
       COALESCE(SUM(reasoning_tokens),0) AS reasoning_tokens,
       COALESCE(SUM(duration_ms),0) AS total_duration_ms,
       SUM(CASE WHEN duration_ms<>0 THEN 1 ELSE 0 END) AS duration_samples,
       MIN(NULLIF(started_at_unix_nano,0)) AS first_started,
       MAX(started_at_unix_nano) AS last_started
FROM usage_requests GROUP BY session_id;

CREATE UNIQUE INDEX idx_req_agg_pk ON req_agg(session_id);

-- 2) 逐列回填（均为主键等值查找，代价可控）
UPDATE usage_sessions SET
  c_total_requests      = COALESCE((SELECT total_requests      FROM req_agg WHERE session_id = usage_sessions.session_id),0),
  c_llm_successes       = COALESCE((SELECT llm_successes       FROM req_agg WHERE session_id = usage_sessions.session_id),0),
  ... （其余列同式）

-- 3) turn 去重回填
INSERT OR IGNORE INTO usage_session_turn_keys(session_id, turn_key, failed)
SELECT session_id,
       COALESCE(NULLIF(trace_id,''), NULLIF(turn_id,''), llm_request_id),
       MAX(CASE WHEN success=0 THEN 1 ELSE 0 END)
FROM usage_requests
GROUP BY session_id, COALESCE(NULLIF(trace_id,''), NULLIF(turn_id,''), llm_request_id);

UPDATE usage_sessions SET
  c_turn_count   = (SELECT COUNT(*) FROM usage_session_turn_keys k WHERE k.session_id = usage_sessions.session_id),
  c_failed_turns = (SELECT COUNT(*) FROM usage_session_turn_keys k WHERE k.session_id = usage_sessions.session_id AND k.failed = 1);
```

库容说明：实测运行库（`backend/data/runtime/usage_analytics.sqlite`）为 **35,796 请求 / 385 会话**，回填需完整扫一遍 `usage_requests`（约 3.6 万行），预计 1–2 秒，为一次性成本；回填发生在进程 `Open()` 阶段（尚未对外提供查询），无并发风险。

回填后建议执行 `ANALYZE`（或 `PRAGMA optimize`），让查询计划器在 `sessionStatsSelect` 上线前刷新统计信息。

### 6.3 对账与修复命令（防止长期漂移）

- 新增 `Store.RebuildSessionStats(sessionID string) error` 与 `RebuildAllSessionStats()`：从 `usage_requests` 全量重算该批会话的计数字段（复用 §6.2 SQL）
- 新增 `aicli usage-analytics rebuild-stats [--session <id>] [--all]` 子命令（或挂在现有 usage/analytics 命令树下）
- `AnalyticsHealth`（`query_v2_health.go`）增加漂移抽样检查：随机抽样 N 个会话，对比 `c_*` 与实时聚合，回报 `stats_drift` 指标
- 触发时机建议：应用启动后空闲时抽样；或用户手动执行

## 7. Phase 2：首屏端点合并

现状：`/api/runtime/analytics/overview` 路由已存在，但只是 `GetAnalyticsSummary` 的别名（`handler.go:770`）。两种做法：

**方案 A（推荐）**：扩展 `/analytics/overview` 响应为一次性引导载荷

```
GET /api/runtime/analytics/overview?group_by=day&from=&to=&q=&provider=&model=&directory=&project=&status=&limit=&offset=
→ {
    "schema_version": "runtime.analytics.v1",
    "sessions":   { <ListResult> },        // 会话列表 + total/scanned/coverage/partial
    "summary":    { <SummaryResult> },     // groups + totals
    "dimensions": { <DimensionsResult> },  // 过滤控件取值
    "matched": <int>                       // 保留旧字段，兼容现有断言
  }
```

- 后端在同一个 handler 内顺序复用 `Store` 查询（单连接串行，合并后减少 HTTP 往返与重复 WHERE 解析）
- 载荷内 `summary.totals` 与 `sessions.coverage` 去重（可让前端优先取 `sessions.totals`，旧字段保留但可标记 deprecated）
- 前端 `overview.tsx` 首屏从 3 个请求合并为 1 个；`subagents/errors/health` 留 Phase 4

**方案 B（保守）**：保留 3 个端点，仅靠 Phase 1 降本。收益：前端零改动；缺点：往返与解析重复仍在。

## 8. Phase 3：Dimensions 合并 + 服务端缓存

### 8.1 Dimensions 单查询

5 个维度合并为一次扫描：

```sql
SELECT 'provider' AS dim, provider AS value FROM (<base>) WHERE ... 
UNION ALL SELECT 'model', model FROM (<base>) WHERE ...
UNION ALL ...  -- 5 段
```

配合各段独立 `LIMIT 1000`（`maxDimensionValues`）——用窗口函数 `ROW_NUMBER() OVER (PARTITION BY dim ORDER BY value)` 截断。收益：5 次扫描 → 1 次。

### 8.2 服务端短 TTL 缓存

- 位置：`usageanalytics.Service` 之上包一层 `cachedStore`，或 handler 层
- 键：`(endpoint, group_by, filters, limit, offset, admin_scope)` 的稳定 hash
- TTL：5s（首屏 6 请求 → 翻页/切换筛选时高命中）
- 失效：写入侧不做主动失效（TTL 短足够）；可选在 `upsertRequest/upsertSession` 后 bump generation 计数实现惰性失效
- 注意：不要缓存单会话明细（`SessionUsage`）与错误模式等低频路径

## 9. Phase 4：首屏瘦身与错误模式索引

1. `overview.tsx`：`subagents` / `errors` / `health` 移出首屏 `Promise.all`，改为对应区块可见或延迟 300ms 后加载（复用现有 lazy 模式）
2. 错误模式（`query_v2.go:375`）：为 `usage_requests(error_category)` 增加部分索引，或独立维护错误计数小表（收益较低，后置）
3. 前端骨架屏/分区 loading，避免"整页 10s 白屏"

## 10. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| 重复终态事件导致计数重复 | 统计偏高 | §5.3 delta 语义；`TestIngestRequestTerminalIsIdempotent` 扩展断言 |
| `trace_id/turn_id` 重试变更导致 turn 键残留 | `c_turn_count` 偏高 1 | §6.3 对账命令重建 |
| 写放大（每次请求终态多 1 SELECT + 1~2 UPDATE） | ingest 吞吐下降 | 同事务内按主键点查/点改，单连接下微秒级；新增 ingest 吞吐基准测试（§11.3），阈值 ≥ 基线 −10% |
| 旧库 / 只读库无新列 | 查询报错 | `statsReady()` 二选一读路径回退（§5.4） |
| 迁移中途失败 | 库不可用 | 单事务 + `user_version` 门控，失败回滚保持 v2 可读 |
| 回填耗时（大库） | 启动变慢一次性 | 实测 3.6 万行预计 1–2s；如遇超大库（>100 万请求）改为后台分批回填 + `stats_ready` 标记 |
| 统计与原始表长期漂移 | 数据可信度 | §6.3 抽样校验 + 修复命令 + 健康指标 |
| `AVG` 语义差异 | 平均耗时偏差 | 用 `samples` 精确复刻 `AVG(NULLIF(duration_ms,0))`，不采用 `SUM/COUNT(*)` |
| 灰度期新旧路径混跑 | 读数不一致 | 上线需重启 runtime-server（迁移在 `Open()` 执行）；停机窗口内 `user_version` 门控保证旧进程不受影响 |
| 新路径出现未知问题需回退 | 无法快速止损 | 增加 `AICLI_USAGE_ANALYTICS_DISABLE_STATS=1` 逃生开关：强制 `statsReady()=false` 走旧 `sessionSelect`（无需回滚版本） |
| 请求表持续增长（无清理） | 长期再次劣化 | 保留策略：`usage_requests`/`usage_session_turn_keys` 按会话保留期清理 + 定期 `VACUUM`（Phase 4） |

## 11. 测试计划

### 11.1 单元测试（新增）

- `store_stats_incremental_test.go`：
  - 单请求 → 列值正确
  - 同一 `llm_request_id` 重复终态（覆盖/降级重试）→ 计数不重复
  - 多请求同 turn / 跨 turn → `c_turn_count` / `c_failed_turns` 正确
  - `duration_ms=0` 行不进入 `c_duration_samples`，`AVG` 与旧表达式一致
- `store_stats_golden_test.go`：同一 fixture 下，「旧 `sessionSelect` 聚合结果」与「新预聚合列」逐字段相等（列表 + 汇总 + 维度三路）
- `store_migrate_v3_test.go`：v2 库升级 v3 回填正确；重复 Open 幂等；只读打开不迁移且回退旧路径
- `query_stats_fallback_test.go`：未迁移库走旧路径结果不变

### 11.2 现有测试回归

- `TestQuerySummarizeGroupByAndDimensions`、`TestQueryListTimeFilterAndPaging`、`TestStoreV2StatsQueries`、`TestIngestRequestTerminalIsIdempotent` 全绿
- `internal/api/skills/analytics_handlers_test.go`：`/analytics/overview` 扩展载荷后旧断言仍通过

### 11.3 性能测试

- 基准：造 200 会话 × 300 请求（6 万行）fixture
- 断言：`ListSessions`/`Summarize`/`Dimensions` 的 P95 与 SQLite 扫描行数（可对比 `EXPLAIN QUERY PLAN`）在目标内
- 端到端：首屏总耗时 < 500ms（本地）
- **ingest 吞吐基准**：连续写入 N 条请求终态，比较改造前后吞吐（阈值 ≥ 基线 −10%）
- **基线留存**：把 §2.8 的 curl 实测脚本固化为 `scripts/usage-analytics-endpoint-bench.ps1`，改造前后各跑一次对比

## 12. 验收标准

1. 首屏（含延迟加载）对 `usage_requests` 的全表聚合次数为 0（`EXPLAIN QUERY PLAN` 验证）
2. 本机实测基线（§2.8，3.6 万行）首屏 API 总耗时从 7.9s 降至 <500ms
3. 统计数值与改造前逐字段一致（golden 测试）
4. 重复/乱序终态事件不产生计数漂移；对账命令可修复人为构造的漂移
5. v2 旧库自动迁移；只读工具与旧库文件兼容不报错
6. 现有全部测试通过

## 13. 分阶段任务清单

### Phase 0（前置，1 小时）
0.1 固化端点基线脚本（`scripts/usage-analytics-endpoint-bench.ps1`，输出各端点 P50/P95）
0.2 记录改造前 `EXPLAIN QUERY PLAN`（确认 `req` CTE 全表聚合）
0.3 浏览器 Performance 面板记录首屏分解（前端模块加载 vs API 等待），确认 Vite dev 开销占比

### Phase 1（P0，核心）
1. `store.go`：v3 迁移（加列 + turn 去重表 + `user_version=3`）
2. `store.go`：回填实现（§6.2）
3. `ingest.go`：`upsertRequest` 改为事务 + delta（§5.3）
4. `query.go`：新增 `sessionStatsSelect` 与 `statsReady()`；`ListSessions/Summarize/Dimensions` 切换 + 回退
5. 新增对账方法 + CLI 子命令
6. 单测 + golden 测试 + 迁移测试
7. 逃生开关 `AICLI_USAGE_ANALYTICS_DISABLE_STATS`

### Phase 2（P0）
8. `handler.go`：`/analytics/overview` 扩展为引导端点
9. `overview.tsx`：首屏合并为 1 请求
10. handler 测试更新

### Phase 3（P1）
11. Dimensions 单查询合并
12. 服务端 TTL 缓存层
13. 缓存命中率与耗时验证

### Phase 4（P2）
14. 首屏瘦身（subagents/errors/health 延迟 + 骨架屏）
15. 错误模式索引/小表
16. 保留期清理 + `VACUUM`（§10 长期劣化对策）

## 14. 功能完整性边界：残留原始表扫描清单

Phase 1 只把**会话级统计**改为读预聚合列；以下路径仍读原始表，需明确其成本与触发条件（避免"以为全改了"的误解）：

| 路径 | 数据源 | 当前规模 | 成本 | 结论 |
|---|---|---|---|---|
| `SessionUsage`（单会话明细，`query.go:695`） | `usage_requests`（按 `idx_usage_requests_session_started`） | 单会话 ≈93 行 | 毫秒级 | 保留原始表（明细必须逐条） |
| `ToolStats`（`query_v2.go`） | `usage_tool_calls` | 382 行 | 毫秒级 | 暂不预聚合；>10 万行时再评估 |
| `SubagentStats`（`query_v2.go`） | `usage_subagents` | 0 行 | 0.002s | 同上 |
| `ErrorPatterns`（`query_v2.go:331/353/375`） | 3 表 `GROUP BY error_code` | 0.083s | 毫秒级 | Phase 4 加部分索引即可 |
| `usage_turns` 相关视图 | `usage_turns` | 8 行（采集不全，见 §5.2 验证） | – | turn 统计改由 `usage_session_turn_keys` 维护 |

**结论**：首屏 6+2 个请求中，只有 `sessions`/`summary`/`dimensions`（合计 11 次全表聚合）是瓶颈，Phase 1 全部消除；其余路径规模小、且多为明细语义，不应预聚合。

## 15. 评审记录（2026-09-17 完整性审查）

本次审查对初稿的修正与补充：

1. **修正数据源**：初稿误用 `~/.aicli/sessions/runtime/usage_analytics.sqlite`（32MB、v1 表为空、`analytics_legacy_*` 归档）估算规模。实测运行库为 `backend/data/runtime/usage_analytics.sqlite`（35,796 请求 / 385 会话），回填成本与目标随之校正。
2. **补实测基线（§2.8）**：端点级 curl 计时，确认 7.9s 后端串行成本 + Vite dev 开销 = 用户观测的 >10s；性能目标由"10 万行库 <800ms"改为"实测基线 <500ms"。
3. **补 Phase 0（§13）**：先量化再改造，避免优化对象错误。
4. **补逃生开关与灰度约束（§10）**：迁移需重启 runtime-server；提供 env 级回退，无需回滚版本。
5. **补 ingest 回归基准（§11.3）**：写入路径进入热路径，必须量化写放大。
6. **补功能边界清单（§14）**：明确哪些路径仍读原始表及理由。
7. **验证 turn 计数设计（§5.2）**：实测 `usage_turns` 仅 8 行，否定"从 usage_turns 派生 turn 数"的简化方案。

遗留待确认（实现前需拍板）：

- **`average_response_time_ms` 语义**：`aggregateTotals` 用 `SUM(c_avg_duration_ms)`（`query.go:433`）——"各会话均值的和"在数学上是无意义量（实测响应值 9,633,588 ms）。需确认前端展示口径；Phase 1 的 golden 测试会逐字段对比，若确认是缺陷，应在本方案中**显式修正**（改为 `总时长 / 总请求数`）并同步前端，而不是机械复刻。
- **`coverage`/`scanned`/`partial` 字段口径**：golden 测试须覆盖，避免切换读路径后语义漂移。
- **保留期策略**：`usage_requests` 与 `usage_session_turn_keys` 是否需要按保留期清理（影响长期稳定性）。

## 16. 实施状态与实测结果（2026-09-17 交付）

### 16.1 交付清单

| 阶段 | 交付物 | 位置 |
|---|---|---|
| Phase 1 | v3 迁移（计数列 + turn 去重表 + `user_version=3`） | `backend/internal/usageanalytics/store_stats.go` |
| Phase 1 | 写入幂等 delta（事务内 SELECT→delta→UPSERT→UPDATE） | `backend/internal/usageanalytics/ingest_stats.go` |
| Phase 1 | 单表读路径 `sessionStatsSelect` + `statsReady()` 回退 | `backend/internal/usageanalytics/query.go:214` |
| Phase 1 | 回填 + 对账（rebuild-stats）/漂移检测 | `store_stats.go`、`query_v2_drift.go`、`cmd/aicli/commands/usage_analytics_command.go` |
| Phase 1 | 逃生开关 `AICLI_USAGE_ANALYTICS_DISABLE_STATS` | `store_stats.go:32` |
| Phase 2 | `/analytics/overview` 引导载荷（sessions+summary+dimensions+matched） | `backend/internal/api/skills/analytics_handlers.go:70`、`handler.go:770` |
| Phase 2 | 前端首屏合并为 1 请求 | `frontend/src/pages/usage-analytics/overview.tsx:126` |
| Phase 3 | Dimensions 5 扫合 1（UNION ALL + 每段 LIMIT） | `query.go:701` |
| Phase 3 | 服务端 TTL 5s + 写入代数失效缓存（仅 3 个聚合读路径） | `service_cache.go`、`service.go:47/109` |
| Phase 4 | 首屏瘦身：观测区块（subagents/errors/health）延迟 300ms 独立加载 | `overview.tsx:99-119,154-157` |
| Phase 4 | `error_category` 部分索引 | `store.go:423` |
| Phase 4 | 保留期清理 + VACUUM + CLI `prune --vacuum` | `store_maintenance.go`、`usage_analytics_command.go` |
| Phase 0.1 | 端点基准脚本 | `backend/scripts/usage-analytics-endpoint-bench.ps1` |
| 收尾修复 | 工具列表补齐 p50/p95/error_top（2 次批量查询替代 N+2） | `query_v2.go` `ToolStats` + `toolDurationSamples`/`toolErrorTopByTool` |

### 16.2 实测对比（同机、同库 35,796 请求 / 386 会话；冷缓存，每次间隔 > TTL）

| 端点 | 改造前 | 改造后（冷） | 提升 |
|---|---|---|---|
| `summary?group_by=day` | 2,039 ms | **5.0 ms** | ≈408× |
| `dimensions` | 1,087 ms | **4.1 ms** | ≈265× |
| `sessions?limit=50` | 657 ms | **7.8 ms** | ≈84× |
| `errors?top=10` | 83 ms | **2.2 ms** | ≈38× |
| `overview`（Phase 2 合并 sessions+summary+dimensions） | ≈3,783 ms（三项之和） | **22.2 ms** | ≈170× |
| `tools` | – | 5.2 ms | – |

首屏后端总成本（overview + 2 个延迟 provider/model summary）：**≈32 ms（冷）/ ≈5 ms（缓存命中）**，对比 §2.8 基线约 7.9 s。

> 说明：v3 迁移已在运行库完成（`user_version=3`），runtime-server 已用新二进制重启并生效；`~/.aicli/sessions/runtime/usage_analytics.sqlite`（aicli CLI 侧旧库）未迁移，不影响本页面。

### 16.3 验收对照（§12）

| # | 验收项 | 结果 |
|---|---|---|
| 1 | 首屏对 `usage_requests` 全表聚合次数 = 0 | ✅ 读路径走 `sessionStatsSelect`（单表 `usage_sessions`） |
| 2 | 首屏 API 总耗时从 7.9s 降至 <500ms | ✅ 实测冷 32ms / 热 5ms |
| 3 | 统计数值与改造前逐字段一致 | ✅ `store_stats_golden_test.go` |
| 4 | 重复/乱序终态不漂移，可对账修复 | ✅ `store_stats_incremental_test.go`、`usage_analytics_command_test.go` |
| 5 | v2 旧库自动迁移 + 只读兼容 | ✅ `store_migrate_v3_test.go`（含只读回退断言） |
| 6 | 现有测试通过 | ⚠️ 见 §16.4（2 项非本方案失败） |

### 16.4 遗留问题

1. **2 个失败测试与本方案无关**（config-layers 工作流在制品，测试隔离失效导致读取真实 `~/.aicli/config.yaml`）：
   - `TestBootstrapChatSession_UsesActorExecutorByDefault`（`chat_setup_test.go:1200`）
   - `TestRunInitCommandUsesLocalStarterPathByDefault`（`init_test.go:31`，`ConfigPath` 落到真实用户目录）
   归属 `internal/agentconfig/config_layers*.go` 一并在制品；本方案已确认 analytics 相关测试全绿（`internal/usageanalytics`、`internal/api/skills`、`cmd/aicli/commands` 中 analytics 用例、前端 25 个用例）。
2. **写路径成本 +24%**（`BenchmarkRequestTerminalStatsWrite` 1.01ms vs `BenchmarkRequestTerminalLegacyBaseline` 0.815ms）：绝对增量 ≈0.2ms/次请求终态（一次 LLM 调用为数秒级），但超出 §3.1 设定的“≥基线 −10%”阈值。建议把阈值调整为绝对值口径（如 +0.5ms 以内），或后续合并 SELECT+UPDATE 再优化。
3. `usage_turns` 采集仍不全（8 行）——turn 计数已由 `usage_session_turn_keys` 维护，不影响统计；如后续 `usage_turns` 视图要上线需先补采集。

## 17. 附录：现状证据索引

| 证据 | 位置 |
|---|---|
| 首屏 6 请求 + 2 延迟 | `frontend/src/pages/usage-analytics/overview.tsx:104,132` |
| `req` CTE 全表聚合 | `backend/internal/usageanalytics/query.go:113,130` |
| 外层 WHERE 无法下推 | `query.go:160–162` |
| `ListSessions` COUNT + 分页 | `query.go:355–363` |
| `aggregateTotals` | `query.go:421–443` |
| `Dimensions` 5 连扫 | `query.go:629–688` |
| 错误模式 3 源扫描 | `query_v2.go:331/353/375` |
| 单连接池 | `store.go:134` |
| v2 表结构 / user_version=2 | `store.go:265–387` |
| 写入钩子 | `ingest.go:261–265, 325–334`、`ingest.go:367–393` |
| `/analytics/overview` 现为别名 | `backend/internal/api/skills/handler.go:770` |
| 工具百分位已延迟 | `query_v2.go:181` |
| 原始设计（未落地的 rollup） | `docs/plan/session-usage-analytics-and-agent-diagnostics-plan.md` §6.3 |
| 运行库真实路径 / 表计数（实测） | `/api/runtime/status` → `runtime.usage_analytics` |
| 端点实测耗时（§2.8） | `curl` 直连 8101，2026-09-17 |
| 前端代理拓扑 | `frontend/vite.config.ts:130-136`（5193 → 127.0.0.1:8101） |
