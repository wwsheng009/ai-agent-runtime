# 自动化任务（Schedule）：数据存储设计

> 配套文档：[`analysis-and-implementation-plan.md`](./analysis-and-implementation-plan.md)（架构与领域）、[`completeness-review-and-integration.md`](./completeness-review-and-integration.md)（多端集成）
> 目标：明确 Schedule 的**数据落盘位置、schema、迁移、并发、保留、备份与多实例语义**，并直接对齐本项目既有约定。
>
> **修订记录（2026-10-11）**：已按 [`review-of-design-docs.md`](./review-of-design-docs.md) 修订——投递确认语义（F2）、多实例不跨库（F10）、wake_link 不建跨模块 FK（F12）、外键级联显式处理（F16）、UTC/本地展示边界（F17）。

---

## 1. 存储介质与位置

| 项 | 取值 | 依据 |
|---|---|---|
| 引擎 | SQLite（行级表） | 与 `internal/background` / `internal/supervision` 一致 |
| 文件 | `data/schedule/schedule.sqlite`（+ `-wal` / `-shm` 附属文件） | 与 `background.sqlite`、`supervision/` 同数据目录族 |
| 数据根 | `filepath.Join(filepath.Dir(config.DefaultAuthStorePath()), "data", "schedule")` | 与 `runtime-server` 既有的 `data/supervision`、`data/subagent-batches` 同源 |
| 打开方式 | `sqliteutil.OpenFile(dsn, true)` | 统一多进程并发基线 |
| 时间格式 | UTC RFC3339 毫秒（`2006-01-02T15:04:05.000Z`） | 与 dsh 的 `scheduledAt` 口径一致；避免本地时区歧义 |
| ID | `schedule-<uuid>`（`crypto/rand`） | 全局唯一，跨实例不冲突 |

> **为什么不用 JSON 文档（dsh 的做法）**：dsh 的 `schedule.json` 每次变更重写整个 domain、启动时把全部历史加载进内存。本项目已有 SQLite 基线，行级表可避免"整文档重写"和"全量入内存"，且天然支持多进程锁。故**明确采用 SQLite**。

---

## 2. 并发基线（复用 `sqliteutil`）

`sqliteutil.OpenFile(dsn, true)` 提供并强制以下基线（见 `internal/sqliteutil/sqliteutil.go` 包注释）：

- `journal_mode=WAL`（主线）；win7compat 下 `DELETE`；
- `busy_timeout` 默认 5000ms；
- `MaxOpenConns(1)` / `MaxIdleConns(1)`（单写者连接，避免同库多连接自锁）；
- 打开前对账 `-wal` / `-shm` 附属文件，清理被带外替换/截断的陈旧 `-shm`；
- `RetryLockedCtx`：仍遇 `database is locked` 时退避重试（10 次，50ms→500ms）。

**结论**：Schedule **不自行管理连接池或锁策略**，一律经 `sqliteutil`。

---

## 3. Schema

### 3.1 迁移框架

使用 `internal/migrate`（`migrate.Migration` + `migrate.Apply(ctx, db, migrations)`，落 `schema_migrations` 表，幂等且只应用一次）。与 `internal/supervision/sqlite_store.go` 的写法一致。

### 3.2 `schedule_tasks`（权威任务表）

```sql
CREATE TABLE IF NOT EXISTS schedule_tasks (
  id                TEXT PRIMARY KEY,          -- schedule-<uuid>
  session_id        TEXT NOT NULL,             -- 原会话绑定（不激活会话）
  kind              TEXT NOT NULL,             -- after|at|every|daily|weekly|cron
  title             TEXT NOT NULL,             -- trim 后非空、<=120
  prompt            TEXT NOT NULL,             -- trim 后非空

  scheduled_at      TEXT NOT NULL,             -- UTC RFC3339 毫秒（已提交目标）

  after_seconds     INTEGER,                   -- kind=after
  every_seconds     INTEGER,                   -- kind=every
  local_time        TEXT,                      -- kind=daily|weekly，HH:MM:SS.SSS
  time_zone         TEXT,                      -- kind=daily|weekly|cron，显式 IANA/UTC
  weekdays_json     TEXT,                      -- kind=weekly，升序去重 [1,3,5]
  expression        TEXT,                      -- kind=cron，规范化五字段

  status            TEXT NOT NULL,             -- active | inactive
  last_scheduled_at TEXT,                      -- 最近一次回执
  last_delivered_at TEXT,
  last_message_id   TEXT,

  state_version     INTEGER NOT NULL DEFAULT 0,-- CAS
  owner_instance_id TEXT,                      -- 多实例 owner（复用 background 范式）
  lease_expires_at  TEXT,                      -- owner 租约

  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,

  CHECK (kind IN ('after','at','every','daily','weekly','cron')),
  CHECK (status IN ('active','inactive'))
);

CREATE INDEX IF NOT EXISTS idx_schedule_due     ON schedule_tasks(status, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_schedule_session ON schedule_tasks(session_id, status);
CREATE INDEX IF NOT EXISTS idx_schedule_lease   ON schedule_tasks(lease_expires_at);
```

**字段与 Kind 的一致性校验**在应用层 `Validate()` 中做（SQLite 不便表达"按 kind 二选一非空"）；解码时违反即拒绝（权威数据，不做备份跳过）。

### 3.3 `schedule_delivery_history`（发送记录，追加型）

```sql
CREATE TABLE IF NOT EXISTS schedule_delivery_history (
  schedule_id  TEXT NOT NULL,
  seq          INTEGER NOT NULL,               -- 追加序号（分页游标，不受墙钟回拨影响）
  scheduled_at TEXT NOT NULL,
  delivered_at TEXT NOT NULL,
  message_id   TEXT NOT NULL,
  prompt       TEXT,                           -- 发送时提示快照（可空：legacy 回执）
  PRIMARY KEY (schedule_id, seq),
  FOREIGN KEY (schedule_id) REFERENCES schedule_tasks(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_schedule_history_task_seq
  ON schedule_delivery_history(schedule_id, seq);
```

**分页**：`WHERE schedule_id=? AND seq < ? ORDER BY seq DESC LIMIT ?`；`before` 为排他游标（message_id 映射到 seq）；未知游标返回 `delivery_cursor_not_found`。

> **外键与级联（评审 F16）**：SQLite **默认 `foreign_keys=OFF`**，上面的 `ON DELETE CASCADE` 不会自动生效。两种处理二选一并写死：① 打开连接后执行 `PRAGMA foreign_keys=ON`（`sqliteutil` 需确认是否已开）；② 删除时在**同一事务**内显式 `DELETE FROM schedule_delivery_history WHERE schedule_id=?` 再删任务行。**不依赖默认行为**。

### 3.4 `schedule_wake_link`（**已否决，保留备选**）

> **状态（一致性复核）**：调度改由独立守护进程 `cmd/scheduled` 长驻持定时器（见 [`scheduler-daemon-design.md`](./scheduler-daemon-design.md)），**不再使用 supervision 的 wake 做调度**。本表**不作为交付项**；仅在"未来改用 wake 唤醒"时才考虑建表。原设计保留如下以备参考。

若采用"把下一目标登记为 supervision wake"的实现，需要一张关联表避免重复唤醒：

```sql
CREATE TABLE IF NOT EXISTS schedule_wake_link (
  schedule_id  TEXT PRIMARY KEY,
  wake_id      TEXT NOT NULL,                  -- supervision 侧 wake 记录 id
  target_at    TEXT NOT NULL,                  -- 该 wake 对应的目标时点
  updated_at   TEXT NOT NULL
);
```

> **决策**：daemon 自长驻，**不使用 wake**，本表不建。**二选一，不可同时存在两套定时器。**
>
> **耦合约束（评审 F12）**：`wake_id` 只取 `WakeScheduler.ScheduleWake` 的**返回值**，**不建跨模块外键**，也不读 `supervision_wake_*` 表；wake 的取消/重排一律经 supervision 的 API，避免 Schedule 依赖其内部 schema。

### 3.5 `schedule_events`（可选，P2：审计/可观测）

```sql
CREATE TABLE IF NOT EXISTS schedule_events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  schedule_id  TEXT,
  session_id   TEXT,
  event_type   TEXT NOT NULL,                  -- created|updated|deleted|delivered|delivery_failed|stopped
  detail_json  TEXT,
  created_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_events_created ON schedule_events(created_at);
```

---

## 4. 写入模型（CAS + 单写者）

### 4.1 CAS 写

所有状态变更（推进目标、置 inactive、追加回执、编辑、删除）走：

```sql
UPDATE schedule_tasks
   SET scheduled_at=?, status=?, last_scheduled_at=?, last_delivered_at=?, last_message_id=?,
       state_version = state_version + 1, updated_at=?
 WHERE id=? AND state_version=? AND status <> 'deleted';
```

影响行数 = 0 → 读回放弃（陈旧写不覆盖）。**终态（删除）不可逆**，杜绝 `inactive → active` 的复活。

### 4.2 单写者队列

进程内所有管理写与投递推进经**同一串行队列**（对齐 dsh 的 `serialize()`）；多进程由 owner 租约 + CAS 兜底。写入开始后取消不回滚（与 dsh 一致，需在文档声明）。

### 4.3 事务边界

| 操作 | 事务范围 |
|---|---|
| create | `INSERT schedule_tasks`（单行） |
| update | `SELECT` 校验 expected → `UPDATE ... WHERE state_version=?`（同一事务） |
| delete | `DELETE schedule_tasks` + `DELETE schedule_delivery_history`（同一事务，见 §3.3 的 FK 说明） |
| 投递提交 | 追加回执（`INSERT history` + `UPDATE task`）**同一事务** |
| 投递本身 | **不**在事务内：`SubmitPromptAsync` 是外部调用；返回 nil 后**才**在同一事务内追加回执 + 推进任务 |

> **与原方案（照搬 dsh）的差异**：dsh 中"任务 put"与"历史追加"是同一行 JSON 的一次写；本项目拆成两张表后，**必须在同一事务内**完成"追加 history + 推进 task"，否则崩溃会留下"有回执但目标未推进"或反之。这是本项目的改进点。
>
> **投递确认语义（评审 F2 更正）**：原稿写"入队 + 会话 flush 两阶段确认"——本项目**没有 `sessions.flush`**，supervision 的 "durable inbox" 也不是通用会话注入。正确口径见主方案 §6.3：确认 = `SessionHub.GetOrCreate` 成功且 `SessionActor.SubmitPromptAsync` 返回 nil（prompt 被 actor 接受），随后写回执。**不等待**消息落盘或 turn 完成。

---

## 5. 保留策略（历史裁剪）

- 配置：`schedule.deliveryHistoryDays`（默认 30）、`schedule.deliveryHistoryRecords`（默认 200），取更严者。
- 时机：**唯一写路径**（追加回执时）在同一事务内裁剪：
  ```sql
  DELETE FROM schedule_delivery_history
   WHERE schedule_id=? AND (delivered_at < ? OR seq <= (SELECT MAX(seq)-? FROM ...));
  ```
- 最新回执始终保留；发生裁剪时任务行置 `earlierRecordsPruned`（可加列或记入 events）。
- 读取**不裁剪**（与 dsh 一致：读取声明自身边界）。

---

## 6. 多实例与租约

复用 `docs/background-job-lifecycle.md` 的结论：

1. 每实例启动注册 `instance_id`，心跳；owner 持有 `lease_expires_at`。
2. **只有 owner 实例执行定时、投递与状态推进**；其他实例只读。
3. 租约过期 + 任务仍 active → 新实例 CAS 接管（`state_version` 保证不双写）。
4. **owner 是调度守护进程**（`cmd/scheduled`），不是交互宿主：租约经 `internal/mesh`（`mesh.Acquire/Renew/Release`）实现，只有持租约的 daemon 触发与投递（见 [`scheduler-daemon-design.md`](./scheduler-daemon-design.md) §4）。**不再使用** supervision 的 wake 做调度。

> **不跨库复用（评审 F10）**：`background` 的实例表 `runtime_instances` 在 **`background.sqlite`**，与 `schedule.sqlite` **不同库**，不能共用实例注册或直接读它的表。多实例启用时 schedule **自建**实例/租约（`schedule_tasks.owner_instance_id` + `lease_expires_at`），或经 supervision 的实例 API 复用其身份；**不跨库 JOIN**。
>
> `owner_instance_id` 记为 **daemon 实例 id**（不是交互宿主）。列与 CAS 一开始就建好。

---

## 7. 备份 / 恢复 / 升级

| 场景 | 处理 |
|---|---|
| 冷备份 | 停宿主后复制 `schedule.sqlite`（含 `-wal`/`-shm`）；或 SQLite `VACUUM INTO` |
| 崩溃恢复 | WAL 自动恢复；启动时 `sqliteutil` 对账附属文件 |
| schema 升级 | `internal/migrate` 逐版本应用；旧版本任务解码失败 → 拒绝启动（权威数据）而非丢弃 |
| 数据损坏 | 与 background 同口径：一条非法记录拒绝打开整个库；提供显式修复工具（可选，P6） |
| 会话导出/导入 | Schedule **独立于会话日志**：导出会话不含任务；任务按 `session_id` 存续（与 dsh 一致，历史 session 事件不迁移） |
| 删除 | 硬删除，不可恢复（需在 UI 与工具描述声明） |

---

## 8. 与其他存储的边界

| 存储 | 表/文件 | 与 Schedule 的关系 |
|---|---|---|
| 后台作业 | `background.sqlite` / `background_jobs` | **不共享**；Schedule 不写作业表，作业不写 Schedule 表 |
| 监督/唤醒 | `supervision/*.sqlite` / `supervision_wake_*` | **只通过 wake API 交互**（可选），不直接写其表 |
| 会话日志 | 会话 SQLite / 事件流 | **不共享**；Schedule 经 `SessionHub.GetOrCreate` + `SessionActor.SubmitPromptAsync` 提交一条 user 消息（触发会话 run），不直接写会话表 |
| Agent 注册 | `agent_control_*` | **只读**：读 `Depth` 判子 Agent、读会话存在性 |

---

## 9. 时间与时区边界（评审 F17）

- **后端一律 UTC**：`scheduled_at` / `last_delivered_at` 等全部存 UTC RFC3339 毫秒；后端**绝不依赖浏览器/进程时区**。
- **设备时区仅前端展示**：详情里的"下次运行（设备时区）+ 相对时长"由前端计算；规则原文时区（`time_zone`）只在频率文本中显示。
- 该边界同时约束四端：micro web / TUI 也按"UTC 存储、本地展示"处理，避免各端各自解析时区。

---

## 10. 数据一致性检查清单

- [ ] `kind` 与规则字段一一对应（其余为空）；
- [ ] `scheduled_at` 为 UTC、毫秒、年份 0001–9999；
- [ ] `title` trim 后非空且 ≤120；`prompt` trim 后非空；
- [ ] `every_seconds` ≥ 60 且为安全整数；`weekdays_json` 升序去重且 ∈ 1..7；
- [ ] `expression` 为规范化五字段；`time_zone` 为可解析 IANA/UTC；
- [ ] `status` ∈ {active, inactive}；`state_version` 单调递增；
- [ ] `schedule_delivery_history.message_id` 在单任务内唯一；最新回执与任务行 `last_*` 一致；
- [ ] 无孤立历史行（`PRAGMA foreign_keys=ON` 或显式同事务删除保证）。
- [ ] 投递失败的成员在 `MaxDeliveryAttempts` 后转 `inactive` 并留痕（不留空转任务）。
