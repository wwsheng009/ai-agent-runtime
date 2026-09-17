# 会话分析采集与 `aicli stats` 运行手册

> 来源方案：`docs/plan/session-analytics-subagent-reliability-implementation-plan.md`（批次 4：`aicli stats` CLI、附录 A.4）
> 适用范围：本机（CLI-local）只读排查统一用量分析库的采集健康与失败模式；不涉及 runtime-server 部署。
> 命令根目录：`backend/`（Go module 根）。

---

## 1. 用途与入口

`aicli stats` 提供无 runtime-server 进程时的只读洞察（方案 §1.1 P1-2）：

| 子命令 | 用途 | 数据来源 |
| --- | --- | --- |
| `stats sessions --limit N` | 最近会话用量汇总（请求/令牌/工具失败/子代理失败） | `usage_requests` + `usage_sessions` |
| `stats session <id>` | 单会话明细（轮次/工具/子代理/失败模式/诊断） | 同上 + `usage_tool_calls` / `usage_subagents` |
| `stats errors --top N` | 失败模式 Top-N（错误码/失败分类） | 工具/子代理/请求三来源 |
| `stats subagents [--failed-only]` | 子代理完成率、失败分类、重试 | `usage_subagents` |
| `stats doctor` | 采集健康自检（库路径/表计数/最近事件时间） | 分析库 + 会话库（只读） |

所有命令一律 `Open(Config{ReadOnly:true})`：不建库、不迁移、不写行；
库/表缺失时输出「暂无数据」并退出码 0。

```powershell
cd backend
go run ./cmd/aicli stats doctor
go run ./cmd/aicli stats sessions --limit 10
go run ./cmd/aicli stats subagents --failed-only --json
```

---

## 2. 退出码约定（脚本/runbook 依赖，勿改）

| 退出码 | 含义 | 触发示例 | 处理 |
| --- | --- | --- | --- |
| `0` | 查询成功（含空库/表缺失降级） | 库文件不存在、分析库无行 | 文本模式打印「暂无数据」；`--json` 输出空数组 |
| `1` | 参数错误 | `--limit 0`、`--top 999`、`session` 缺 ID、多余参数、未知子命令 | 修正命令；`--help` 查看范围 |
| `2` | 确定性错误 | 分析库/会话库文件存在但不可读（损坏、非 SQLite）、`--db` 指向目录 | 按 §5.4 处置；不要重试 |

补充语义：

- 库/表缺失 ≠ 错误：属于降级路径，退出码 0；
- 会话 ID 在非空库中不存在：同样按「暂无数据」处理，退出码 0（JSON 输出与真实明细同构的空对象）；
- 错误信息统一写 stderr（`Error: ...`），stdout 只承载结果，可直接管道给 `jq`；
- 采集进程侧的瞬时故障（写锁竞争、bus 未 attach）不会让查询报 2，只表现为数据为空——由 `stats doctor` 的提示区暴露。

---

## 3. 子命令用法

### 3.0 公共参数

| 参数 | 说明 | 优先级 |
| --- | --- | --- |
| `--db <path>` | 覆盖分析库路径 | 最高 |
| 环境变量 `AICLI_USAGE_ANALYTICS_DB` | 覆盖分析库默认路径 | 次之 |
| 默认 | `~/.aicli/sessions/runtime/usage_analytics.sqlite` | 最低 |
| `--json` | 输出稳定字段名（与 `usageanalytics` 结构体 JSON tag 同名） | — |

会话库路径（仅 `doctor` 使用）：默认 `~/.aicli/sessions/runtime/session_runtime.sqlite`，可用 `stats doctor --session-db <path>` 覆盖。

### 3.1 `aicli stats sessions --limit N [--json]`

`--limit` 范围 1..200（默认 20，与查询层 `maxListLimit` 对齐）。

文本输出示例：

```text
会话总数: 2，返回 2 条（--limit 上限 200）
SESSION_ID     TITLE             MODEL           REQUESTS  TOKENS  TOOL_CALLS  TOOL_FAIL  SUBAGENTS  SUBAGENT_FAIL  STATUS     LAST_ACTIVE
-------------  ----------------  --------------  --------  ------  ----------  ---------  ---------  -------------  ---------  -------------------
0f3c1a2b…      修复渲染顺序      claude-sonnet   18        42000   12          2          3          1              completed  2026-09-17T08:12:00Z
```

JSON 输出为 `usageanalytics.ListResult`（`schema_version`/`sessions`/`count`/`total`/`limit`/`totals`/`coverage`…）。
空库文本输出：

```text
暂无数据：分析库中没有会话记录（db=C:\Users\me\.aicli\sessions\runtime\usage_analytics.sqlite）
```

空库 JSON 输出：`{"schema_version":"runtime.analytics.v1",...,"sessions":[],"count":0,...}`（退出码 0）。

### 3.2 `aicli stats session <id> [--json]`

文本输出：会话汇总（模型/状态/请求/令牌/回合/工具/子代理/步骤数）+ 轮次、工具、子代理、失败模式、诊断分节表格。
`steps` 明细只报数量，完整明细用 `--json`。

JSON 输出为 `usageanalytics.SessionUsageDetail`；会话不存在时输出同构空明细（各数组为空数组，`session.session_id` 回显查询值）。

### 3.3 `aicli stats errors --top N [--json]`

`--top` 范围 1..50（默认 10，与 `maxErrorPatternRows` 对齐）。

```text
#  SOURCE     ERROR_CODE        FAILURE_CATEGORY  COUNT
-  ---------  ----------------  ----------------  -----
1  tools      timeout           timeout           7
2  subagents  subagent_failed   subagent_failed   3
```

JSON 输出为 `usageanalytics.ErrorPatternsResult`（`patterns[]`：`source`/`error_code`/`failure_category`/`count`）。

### 3.4 `aicli stats subagents [--failed-only] [--limit N] [--json]`

- `--failed-only`：仅统计/显示失败子代理（`success=0`）；
- `--limit` 范围 1..200（默认 50，与 `maxSubagentStatsRows` 对齐）。

文本输出先打印摘要行，再打印明细：

```text
子代理: total=5 succeeded=3 failed=2 unknown=0 failure_rate=40.0% timeouts=1 retried=1
失败分类: subagent_failed=1, timeout=1
来源: live=5
SUBAGENT_ID          PARENT_SESSION        SUCCESS  COMPLETION_REASON  FAILURE_CATEGORY  ATTEMPT  DURATION  COMPLETED_AT
```

JSON 输出为 `usageanalytics.SubagentStatsResult`（`summary` 含 `failure_categories`/`sources` 分布）。

### 3.5 `aicli stats doctor [--json]`

采集健康自检（等价基线脚本的轻量版；方案附录 A.1）：

1. 两库路径、是否存在、是否可读、文件大小；
2. 候选表行数：分析库 `usage_requests`/`usage_sessions`/`usage_tool_calls`/`usage_subagents`/`usage_turns`
   （另列旧 `analytics_*` 死表）、会话库 `session_events`/`session_tool_receipts`/`cache_requests`；
3. 最近事件时间：分析库取 `usage_requests.started_at_unix_nano` 最大值，会话库取 `session_events.created_at` 最大值；
4. 会话事件类型分布：`tool.*` 与 `subagent.completed` 行数；
5. 提示区：按方案 §0.3 的 G1（采集未生效）、G2（工具事件缺失）、
   G3（工具回执未持久化）、G4（子代理载荷口径）给出已知断点提示。

文本输出示例：

```text
采集健康自检 2026-09-17T08:30:00Z
分析库: C:\Users\me\.aicli\sessions\runtime\usage_analytics.sqlite
  状态: 可读（24576 B）
  表                行数
  ----------------  ------
  usage_requests    0
  usage_tool_calls  0
会话库: C:\Users\me\.aicli\sessions\runtime\session_runtime.sqlite
  状态: 可读（1236992 B）
  表                      行数
  ----------------------  ------
  session_events          16062
  session_tool_receipts   0
  最近事件时间: 2026-09-17T08:12:00Z
  事件类型:
  类型                 数量
  ------------------  ------
  subagent.completed  271
提示:
  - usage_requests 为 0 行：分析采集未生效，需要检查进程 attach（G1）
  - usage_tool_calls 为 0 行：工具生命周期未被采集（G2）
  - session_tool_receipts 为 0 行：工具回执未持久化（G3）
```

`--json` 字段：`generated_at`、`analytics_db{path,exists,readable,size_bytes,tables[],last_event_at,error}`、
`session_db{...同上，另含 event_types[]}`、`warnings[]`。
库文件不存在时 `exists=false`（正常降级，退出码 0）；存在但不可读时 `readable=false` 且退出码 2。

---

## 4. 采集健康检查步骤（推荐顺序）

1. **跑自检**

   ```powershell
   cd backend
   go run ./cmd/aicli stats doctor
   ```

2. **看分析库是否在长**：`usage_requests`/`usage_sessions` > 0 说明采集链路通；
   若为 0，对照 §5.2/§5.3 定位 attach 与路径问题。

3. **对照基线脚本**（方案附录 A.1，Python 独立读数，排除 CLI 自身问题）：

   ```powershell
   python -c "import sqlite3,os; p=os.path.expanduser('~/.aicli/sessions/runtime/usage_analytics.sqlite'); c=sqlite3.connect(p); [print(t, c.execute('select count(*) from '+t).fetchone()[0]) for t in ('usage_requests','usage_sessions')]"
   python -c "import sqlite3,os; p=os.path.expanduser('~/.aicli/sessions/runtime/session_runtime.sqlite'); c=sqlite3.connect(p); print('events', c.execute('select count(*) from session_events').fetchone()[0]); print(c.execute(\"select type,count(*) from session_events where type like 'tool.%' or type='subagent.completed' group by type\").fetchall())"
   ```

4. **完成一次真实会话后复测**：

   ```powershell
   go run ./cmd/aicli exec --prompt "ping" --yolo
   go run ./cmd/aicli stats sessions --limit 5
   ```

5. **跑一轮失败模式/子代理视图**，确认批次 1/2 的采集字段可用：

   ```powershell
   go run ./cmd/aicli stats errors --top 10
   go run ./cmd/aicli stats subagents --failed-only --json
   ```

---

## 5. 常见故障排查

### 5.1 库锁（database is locked）

- 症状：`stats` 查询报错或等待；采集进程日志出现 `database is locked`。
- 原因：aicli 与 runtime-server 多进程共享同一 SQLite 文件；写入、`migrate`、
  WAL checkpoint 需要写锁。`usageanalytics.Open` 对瞬时锁冲突有退避重试
  （`openLockRetries=10`），只读连接同样设置 `busy_timeout`（默认 5s）。
- 排查：

  ```powershell
  go run ./cmd/aicli stats doctor --json
  ```

  - 分析库 `readable=true` 且能返回计数 → 只是瞬时竞争，重试即可；
  - 长时间不可读 → 确认没有残留进程持锁（任务管理器/`Get-Process aicli,runtime-server`），
    必要时终止后重试；
  - 不要用 `--db` 指向正在以写方式打开的库做批量高频查询，避免与采集争抢。

### 5.2 路径覆盖（查询的库不是采集的库）

- 优先级：`--db` > `AICLI_USAGE_ANALYTICS_DB` > `~/.aicli/sessions/runtime/usage_analytics.sqlite`。
- 症状：Web/TUI 有数据，但 `stats sessions` 显示「暂无数据」；或反之。
- 排查：

  ```powershell
  go run ./cmd/aicli stats doctor --json | ConvertFrom-Json | Select-Object -ExpandProperty analytics_db | Select-Object path,exists,size_bytes
  echo $env:AICLI_USAGE_ANALYTICS_DB
  ```

  - 确认 CLI 实际解析路径与 runtime-server/Web 端一致；
  - 若服务端使用了非默认路径（如 runtime store 同目录推导），用 `--db` 显式指定；
  - 会话库路径不在 env 覆盖范围内，用 `stats doctor --session-db <path>` 显式指定。

### 5.3 bus 为 nil（只打开不订阅，采集为空）

- 症状：`usage_requests`/`usage_sessions` 长期为 0，但会话库 `session_events` 正常增长。
- 原因：`usageanalytics.Attach(bus, opts)` 的 `bus` 为 nil 时只打开数据库、不订阅事件；
  历史回归中 attach 失败还会被 `sync.Once` 永久缓存，导致进程整个生命周期不写库。
- 排查步骤：
  1. `stats doctor` 确认分析库路径与表存在；
  2. 检查启动日志是否有 `Warning: usage analytics ...`（attach/写失败会留痕 stderr）；
  3. 触发一次真实会话后观察 `usage_requests` 是否增长；
  4. 若仍为 0，检查调用点是否在**服务启动路径**上执行了 `Attach`（懒加载路径在
     `backend/internal/api/skills/analytics_handlers.go`、`backend/cmd/aicli/commands/chat_cache_local.go`）；
  5. 修复侧建议参考 `backend/cmd/aicli/commands/usage_attach_retry_test.go` 的回归用例（失败不得永久缓存）。

### 5.4 多进程并发迁移与库损坏

- 并发迁移：多个 aicli / runtime-server 同时启动会在同一库上执行 `migrate`（建表/建索引，需要写锁）。
  现状已有退避重试；若出现 `open usage analytics db after N lock retries`，
  说明竞争超出重试窗口——减少同时启动的进程数或错峰启动。
- 库损坏（退出码 2）：
  1. 先备份分析库：`Copy-Item <db> <db>.bak`（连同 `-wal`/`-shm` 文件）；
  2. 用 `stats doctor --json` 确认 `analytics_db.readable=false` 及 `error` 原文；
  3. 确认没有其他进程正在写（避免把并发写误判为损坏）；
  4. 如果文件不是 SQLite 格式（例如被误覆盖），移走损坏文件后由采集链路重建
     （库不存在会走空库降级，不阻塞其它功能）；
  5. 历史数据可由批次 2.2 的回放脚本从会话库重建（见方案附录 A.5），执行前务必再备份。

---

## 6. 与其它前端的一致性

| 数据点 | aicli stats CLI | HTTP API | TUI |
| --- | --- | --- | --- |
| 会话列表 | `stats sessions` | `GET /api/runtime/analytics/sessions` | `/usage` |
| 单会话明细 | `stats session <id>` | `GET /api/runtime/analytics/sessions/{id}` | `/usage` |
| 工具/子代理 | `stats subagents` | `GET /api/runtime/analytics/subagents` | `/usage subagents` |
| 失败模式 | `stats errors --top N` | `GET /api/runtime/analytics/errors` | `/usage errors` |
| 采集健康 | `stats doctor` | runtime 状态快照 `usage_analytics` 块 | `/status` 行 |

空库/降级语义保持一致：空数组（JSON）→「暂无数据」（文本）→ 退出码 0。

---

## 7. 已知边界（未决问题）

1. `stats doctor` 的会话库检查走独立的只读 SQLite 连接（会话库没有 `usageanalytics` 式查询层），
   仅用于诊断，不参与分析查询；
2. `stats` 目前不读取 runtime 配置中的 `session_runtime.store_path`：会话库路径只支持默认值或
   `--session-db` 覆盖；若后续需要与 runtime-server 配置完全对齐，应复用 `sessionruntime.ResolvePaths`；
3. `stats` 忽略根命令的 `--envelope`，不产出 `{ok,command,data}` 包装；
4. 子代理字段（`failure_category`/`attempt` 等）依赖批次 2 的载荷归一化与回放；
   归一化未覆盖的历史行会以 `unknown`（`success=null`）呈现，属于预期降级而非缺陷。

## 8. 附：相关文件与变更边界

| 文件 | 说明 |
| --- | --- |
| `backend/cmd/aicli/commands/stats.go` | `aicli stats` 命令实现（5 个子命令、退出码、只读降级、简单对齐输出） |
| `backend/cmd/aicli/commands/stats_test.go` | 空库降级、字段渲染、`--json` 稳定性、退出码 0/1/2 覆盖 |
| `backend/cmd/aicli/main.go` | 仅注册 `commands.NewStatsCommand()`；`stats` 加入免配置引导名单（不加载/创建配置） |
| `docs/plan/session-analytics-runbook.md` | 本手册 |

变更边界：不修改 `backend/internal/usageanalytics/*`；查询一律经 `usageanalytics`
查询层，CLI 不直接读分析表（`stats doctor` 的表计数与损坏探测属于诊断例外，只读且不写）。
