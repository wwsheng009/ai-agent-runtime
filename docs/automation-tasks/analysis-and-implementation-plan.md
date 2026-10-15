# 自动化任务（Schedule）：分析方案与实施方案

> 目标：在本项目 `ai-agent-runtime`（Go）中，参考 `deepseek-harness`（dsh）的 Schedule 子系统，实现"自动化任务"能力。
> 范围：`backend/internal/schedule`（新增）、`backend/internal/toolbroker`（新增 `schedule_*` 工具）、`backend/internal/config`（新增 `schedule` 配置）、`backend/cmd/runtime-server` 与 `backend/cmd/aicli`（装配）、`backend/internal/webui`（任务页面/API）。
> 非目标：不改写 `task_*`（后台任务）与 `workflow`/team（工作流）语义；不引入跨会话转交、不引入 push/邮件通知。
>
> **修订记录（2026-10-11）**：已按 [`review-of-design-docs.md`](./review-of-design-docs.md) 修订 F1–F17。关键更正：投递入口为 `SessionHub.GetOrCreate` + `SessionActor.SubmitPromptAsync`（非 durable inbox/flush）；工具用 `types.ToolDefinition`（无 output schema）；归档准入属**新增**检查点；补 DST 算法、cron 工作量、重试/死信、鉴权、多实例跨库约束。

---

## 0. 阅读指引

- 第 1 节：dsh Schedule 的机制提炼（只保留可移植的设计决策）。
- 第 2 节：本项目现状与差距（哪些能复用、哪些必须新建）。
- 第 3 节起：目标架构与实现方案（数据模型、时间语义、运行时、工具、权限、Web、分期与验收）。
- 附录 A：Go 接口/结构体草案；附录 B：错误码与工具 schema；附录 C：验收用例清单。

---

## 1. 参考系统分析：dsh 的 Schedule 机制

### 1.1 分层

| 层 | dsh 组件 | 关键点 |
|---|---|---|
| Host 服务 | `@deepseek-ai/dsh-schedule` 的 `ScheduleService` | 拥有存储 domain、全局唯一 id、**单定时器**、管理/投递共用 **单 FIFO** |
| 模型工具 | `@deepseek-ai/dsh-tool-schedule` | 四个工具注册进挂载 preset；工具层做选择器/标题校验与子 Agent 拒绝 |
| 客户端 UI | `@deepseek-ai/dsh-client-ui-schedule` | 全局任务页、会话页头入口、右侧栏详情、侧栏行标记、创建卡片；共享一个 catalog 读取源 |
| 组合 | `web-app/cordis.patch.yml` + presets | 服务行与页面行由发行组合挂载；工具归属由 preset 决定，`minimal` 不含 |

### 1.2 持久模型

- 存储 domain `schedule`（version 1）单表 `tasks`，键为 `ScheduleId`，值为 `ScheduleTask`：
  `{ sessionId, record, status: active|inactive, lastDelivery?, deliveryHistory? }`。
- `ScheduleRecord` 六种判别式：`after` / `at` / `every` / `daily` / `weekly` / `cron`。
- 公共字段：`id`、`title`（必填、trim 后非空、≤120 字符，**绝不从 prompt 派生**）、`prompt`、`scheduledAt`（规范化 RFC3339 UTC）。
- 只有对应规则保留自身参数：`afterSeconds`、`everySeconds`、`time`+`timeZone`（weekly 加 `weekdays`，cron 加 `expression`）。
- 生命周期 `status`（active/inactive）与派生时间态 `state`（scheduled/overdue）**是两个概念**。
- 存储校验严格：一条非法记录拒绝整个 domain 打开（权威数据，不做备份跳过）。

### 1.3 调度运行时

- 全程**至多一个定时器**；`requestDrive()` 清旧 timer、置 `requested`、若已有 drive 在跑则合并。
- 管理写与投递写共用**单 FIFO**，避免"读-改-写"与投递推进交错。
- 一次 drive：筛 active 且到期 → 周期任务按 sessionId 分组 → `resolveAgent(sessionId)` **按需冷恢复会话** → 按恢复后的实际时间重新过滤 → 渲染文本 → `followup(message)` → `sessions.flush()` 确认 → 逐条 commit（一次性置 inactive；周期写下一目标）→ 失败的成员保留定时义务下轮重试。
- 超过平台定时器上限的延迟分段处理；timer `unref()`。

### 1.4 时间语义（可移植的关键约定）

- 采用日历运算（dsh 用 `Temporal`）：**DST 缺口跳过该日期、重叠取较早时点**。
- 停机后周期任务**只补发最近一次**错过，不累积。
- `every` 是固定经过时间（最小 60s），与 `daily`（本地钟表时间）语义不同；`every_seconds: 86400 ≠ daily`。
- cron 为五字段 Vixie 方言（`minute hour dom month dow`），创建时**规范化表达式**，解码拒绝非规范化存储；DOM/DOW 遵循 Vixie 语义（任一为 `*` 则两者都需匹配，否则任一匹配即可）。
- 只提交 UTC `scheduledAt`；重启/时区数据变化不重算已承诺目标。

### 1.5 投递与一致性

- 到期以 **user-role 消息**进入原会话（dsh 中 producer kind = `schedule`），固定文本 + JSON 载荷（id / occurrence_at / prompt）。
- 投递 = `followup`（同步入会话收件箱）+ `flush`（持久化确认）两阶段；**不保证 exactly-once**，崩溃可能重复投递，设计上选择"未确认则下次重试"。
- 保存发送记录 `deliveryHistory`（含 prompt 快照），按窗口（默认 30 天）与条数（默认 200）在**唯一写路径**裁剪；分页按追加顺序逆序，不按墙钟。
- `update` 用完整 `expected` 记录做 compare-and-set：不一致返回 `schedule_conflict`，不覆盖。

### 1.6 权限与边界

- 任务绑定创建会话；所有管理操作要求 `(sessionId, id)` 绑定匹配，否则 `schedule_not_found`。
- 被委派子 Agent（`delegationDepth > 0`）的会话：`create` 拒绝、`update` 返回非变更拒绝；`list/catalog/history/delete` 仍可服务。工具层对四个工具也整体拒绝子 Agent 调用。
- 会话归档准入：活动任务作为 `schedule` 族阻止归档；归档 stop 在**同一 FIFO 槽位**删除活动行。

---

## 2. 本项目现状与差距

### 2.1 本项目已有、可直接复用的能力

| 能力 | 位置 | 复用方式 |
|---|---|---|
| 工具注册与 schema | `backend/internal/toolbroker/broker.go`（工具名常量 + JSON schema + execute） | 新增 `ToolScheduleCreate/List/Update/Delete` 常量与 schema，与 `task_*` 同族并列 |
| 持久任务存储 + CAS + 状态机 | `backend/internal/background`（`manager.go` / `store.go` / `types.go`）、`docs/background-job-lifecycle.md` | 沿用其 SQLite migration、`state_version` CAS、一任务一 owner 原则 |
| durable wake | `backend/internal/supervision/wake_scheduler.go` | **不用于 Schedule 调度**（评审后改由独立守护进程 `cmd/scheduled` 长驻持定时器；见 §6.2 与 [`scheduler-daemon-design.md`](./scheduler-daemon-design.md)） |
| 会话投递 | `chat.SessionHub.GetOrCreate`（`chat/hub.go:157`）+ `SessionActor.SubmitPromptAsync`（`chat/actor.go:593`） | 到期提交一条 user 消息（触发一次会话 run）；**不是** supervision 的 durable inbox |
| 运行时装配 | `backend/cmd/runtime-server/main.go`（`BuildSupervisionControlPlane`、`handler.SetSupervision*`）、`backend/cmd/aicli/commands/chat_actor_host.go` | 新增 `internal/schedule` 服务并在两个宿主接线 |
| 配置 | `backend/internal/config/manager.go`（`Background BackgroundConfig`、`Supervision`） | 新增 `Schedule ScheduleConfig`，走同一 YAML 校验/默认值路径 |
| 数据目录约定 | `filepath.Join(filepath.Dir(config.DefaultAuthStorePath()), "data", ...)` | 新增 `data/schedule/schedule.sqlite` |
| Web/API | `backend/internal/webui`、`cmd/runtime-server` 的 HTTP 路由 | 新增任务列表/详情/删除/历史 API 与页面 |

### 2.2 差距（必须新建）

1. **领域层缺失**：没有"六种规则 + 规范化 + 发生点解析（DST/cron）"的实现。
2. **定时触发语义缺失**：`background` 是"提交即执行"，没有"到点触发"；`supervision` wake 是内部投递机制，没有面向用户的定时任务模型。
3. **模型工具缺失**：没有 `schedule_*` 工具族。
4. **会话绑定与归档准入**：没有"任务绑定会话、归档前必须停止"的规则。
5. **Web 任务页缺失**：没有跨会话任务列表/详情/历史 UI。

### 2.3 与 dsh 的映射（本项目落地表）

| dsh 概念 | 本项目落地 |
|---|---|
| `ScheduleService`（Cordis Service + Remote） | `internal/schedule.Service`（Go，注入 store/config/session delivery） |
| `schedule` storage domain v1 | `schedule_tasks` 表 + `schedule_delivery_history` 表（SQLite migration） |
| `ScheduleRuntime`（单定时器 + FIFO） | `internal/schedule/runtime.go`：单定时器 + 单写者队列；跨重启由 wake/启动扫描承接 |
| `tool-schedule` | `internal/toolbroker` 新增 `schedule_*` 工具 |
| `ui-schedule` | `internal/webui` 新增"自动化任务"面板 + runtime API |
| `schedule/changed` 事件 | 现有事件总线（`internal/events`）新增 `schedule.changed` |
| profile 归属 | `internal/config` 的 **profile 工具策略**（`ToolPolicySpec{Allowlist,Denylist}`）决定是否暴露 `schedule_*` |

---

## 3. 目标架构

### 3.1 分层视图

```
┌──────────────────────────────────────────────────────────────────┐
│ Web / CLI 表现层                                                   │
│  webui「自动化任务」面板 · 会话页头提醒入口 · 侧栏行标记           │
│  runtime API: schedule.list / catalog / history / update / delete │
└───────────────▲────────────────────────────┬─────────────────────┘
    schedule.changed（事件总线）              │ HTTP/SSE
┌───────────────┴────────────────────────────▼─────────────────────┐
│ 控制面：internal/schedule.Service（Create/List/Catalog/History/Update/Delete）
│  + internal/toolbroker：schedule_* 工具  —— 宿主内，**不持定时器**        │
└───────────────┬───────────────────────────────────────────────────┘
                │ 共享 schedule.sqlite（WAL）
                ▼
┌───────────────────────────────────────────────────────────────────┐
│ 调度面：cmd/scheduled 守护进程（Windows 服务 / systemd）            │
│  Runtime: 单定时器 + 单写者队列（CAS）；mesh 租约保证单实例          │
│  Domain: 六种规则解析 / 规范化 / 发生点计算（纯函数，可单测）        │
└───────────────┬───────────────────────────────────────────────────┘
                │ 投递（跨进程）
                ▼
  POST /api/runtime/sessions/{id}/runtime/commands (submit_prompt)
  → 宿主 chat.SessionHub.GetOrCreate → SessionActor.SubmitPromptAsync
  （宿主离线：写持久 mailbox trigger_turn 兜底）
┌──────────────────────────────────────────────────────────────────┐
│ 执行面：runtime-server（会话 actor 跑一次 turn）                    │
└──────────────────────────────────────────────────────────────────┘
```

### 3.2 设计原则（沿用本项目既有结论）

1. **一任务一 owner、CAS、终态不可逆**——直接采用 `docs/background-job-lifecycle.md` §2/§4 的原则，避免后台任务踩过的"多实例重复执行/陈旧写覆盖"问题。
2. **领域逻辑纯函数化**——规则解析、规范化、发生点计算不依赖存储与时钟注入，便于单测与回放。
3. **单一调度入口**——所有变更（create/update/delete/stop/投递推进）走同一串行队列；定时器重算只从该队列发起。
4. **投递即一次会话 run**——到期经 `SessionHub` 解析原会话并提交一条 user 消息（会触发该会话的一次模型 turn，与 dsh 的 followup 语义一致）；Schedule 自身不新建会话、不编排执行内容。
5. **显式删除**——删除是硬删除（含历史），不做"软删除 + 复活"。

---

## 4. 数据模型

### 4.1 Go 类型（`internal/schedule/types.go`）

```go
type ScheduleID string // 全局唯一：schedule-<uuid>

type ScheduleKind string

const (
    KindAfter  ScheduleKind = "after"  // 正安全整数秒延迟，一次性
    KindAt     ScheduleKind = "at"     // 绝对时点，一次性
    KindEvery  ScheduleKind = "every"  // 固定速率（>=60s）
    KindDaily  ScheduleKind = "daily"  // 本地钟表时间
    KindWeekly ScheduleKind = "weekly" // 本地钟表时间 + ISO 星期集合
    KindCron   ScheduleKind = "cron"   // 五字段 Vixie
)

type ScheduleRecord struct {
    ID          ScheduleID `json:"id"`
    Kind        ScheduleKind `json:"kind"`
    Title       string     `json:"title"`        // trim 后非空、<=120
    Prompt      string     `json:"prompt"`       // trim 后非空
    ScheduledAt time.Time  `json:"scheduledAt"`  // UTC，RFC3339（毫秒精度）

    AfterSeconds int64  `json:"afterSeconds,omitempty"`
    EverySeconds int64  `json:"everySeconds,omitempty"`
    Time         string `json:"time,omitempty"`      // HH:MM:SS(.mmm)
    TimeZone     string `json:"timeZone,omitempty"`  // 显式 IANA/UTC
    Weekdays     []int  `json:"weekdays,omitempty"`  // 1..7 升序去重
    Expression   string `json:"expression,omitempty"`// 规范化五字段
}

type ScheduleStatus string // "active" | "inactive"

type DeliveryReceipt struct {
    ScheduledAt time.Time `json:"scheduledAt"`
    DeliveredAt time.Time `json:"deliveredAt"`
    MessageID   string    `json:"messageId"`
}

type DeliveryRecord struct {
    DeliveryReceipt
    Prompt string `json:"prompt,omitempty"` // 发送时的提示快照
}

type ScheduleTask struct {
    SessionID       string            `json:"sessionId"`
    Record          ScheduleRecord    `json:"record"`
    Status          ScheduleStatus    `json:"status"`
    LastDelivery    *DeliveryReceipt  `json:"lastDelivery,omitempty"`
    DeliveryHistory *DeliveryHistory  `json:"deliveryHistory,omitempty"`
    StateVersion    int64             `json:"stateVersion"` // CAS
}
```

> 说明：Go 无判别式联合，采用**单一结构 + Kind 判别 + 规则字段可选**，并用 `Validate()` 强制"对应 Kind 只允许对应字段非零"。这比 dsh 的 TS union 弱，但换来 SQLite 列式存储与迁移的简单。

### 4.2 SQLite schema（`internal/schedule/store.go`，migration）

```sql
CREATE TABLE IF NOT EXISTS schedule_tasks (
  id             TEXT PRIMARY KEY,
  session_id     TEXT NOT NULL,
  kind           TEXT NOT NULL,
  title          TEXT NOT NULL,
  prompt         TEXT NOT NULL,
  scheduled_at   TEXT NOT NULL,          -- UTC RFC3339（毫秒）
  after_seconds  INTEGER,
  every_seconds  INTEGER,
  local_time     TEXT,
  time_zone      TEXT,
  weekdays_json  TEXT,                   -- 升序去重数组
  expression     TEXT,                   -- 规范化五字段
  status         TEXT NOT NULL,          -- active | inactive
  last_scheduled_at TEXT, last_delivered_at TEXT, last_message_id TEXT,
  state_version  INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_schedule_due      ON schedule_tasks(status, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_schedule_session  ON schedule_tasks(session_id, status);

CREATE TABLE IF NOT EXISTS schedule_delivery_history (
  schedule_id  TEXT NOT NULL,
  seq          INTEGER NOT NULL,          -- 追加序号（分页游标，不受墙钟回拨影响）
  scheduled_at TEXT NOT NULL,
  delivered_at TEXT NOT NULL,
  message_id   TEXT NOT NULL,
  prompt       TEXT,
  PRIMARY KEY (schedule_id, seq)
);
```

### 4.3 存储规则

- **校验严格**：解码时 `title` 缺失/空白/带首尾空白/超长一律拒绝；规则字段与 Kind 不匹配拒绝；`scheduledAt` 非 UTC 或年份越界拒绝。
- **CAS 写**：所有状态写为 `UPDATE ... WHERE id=? AND state_version=? AND status NOT IN ('deleted')`，影响行数为 0 即放弃并重读。
- **历史裁剪**：在**唯一写路径**（追加回执时）按 `schedule.deliveryHistoryDays`（默认 30）与 `schedule.deliveryHistoryRecords`（默认 200）取更严者；最新回执始终保留；裁剪后置 `earlierRecordsPruned`。
- **删除**：`DELETE FROM schedule_tasks WHERE id=?` + `DELETE FROM schedule_delivery_history WHERE schedule_id=?`，同一事务。

> **完整存储设计**（文件位置、并发基线、migration、索引、保留、多实例租约、备份恢复、与其他存储的边界、一致性检查清单）见 [`data-storage-design.md`](./data-storage-design.md)。要点：采用 SQLite 行级表（**不采用 dsh 的 JSON 文档**，避免整文档重写与全量入内存），经 `sqliteutil.OpenFile(dsn, true)` 取得 WAL/`busy_timeout`/单连接池/锁重试基线，用 `internal/migrate` 做版本化迁移，并强制"追加回执 + 推进任务"在**同一事务**内完成（本项目对 dsh 的改进点）。

---

## 5. 时间语义（`internal/schedule/domain.go`）

### 5.1 六种规则

| 选择器 | 语义 | 约束 |
|---|---|---|
| `after_seconds` | 延迟一次性 | 正安全整数；仅创建时可用 |
| `at` | 绝对时点（RFC3339 带偏移，或 `{date,time,time_zone}` 本地形式） | 严格未来；字符串必须带偏移 |
| `every_seconds` | 固定经过时间 | ≥60s，安全整数；与创建/保存时刻对齐 |
| `daily` | `{time, time_zone}` 本地钟表 | `HH:MM:SS(.mmm)`；显式 IANA |
| `weekly` | `daily` + `weekdays`（ISO 1..7） | 升序去重非空 |
| `cron` | `{expression, time_zone}` 五字段 Vixie | 见 §5.3 |

### 5.2 通用时间约定

- 用 Go 标准库 `time` + `time.LoadLocation`（IANA）。**Go 没有 `Temporal.PlainTime` 语义**，缺口/重叠必须显式判定，算法如下（`domain.go` 的 `localInstant(date, time, loc) (utc, ok)`）：
  1. 用 `time.Date(y,m,d,hh,mm,ss,ns,loc)` 构造 `t`；
  2. 回读墙钟 `t.In(loc)`，若 `(年,月,日,时,分,秒)` 与输入不一致 → 该本地时间**落在缺口**，返回 `ok=false`（调用方跳过该日期）；
  3. 缺口探测：再试 `time.Date(..., loc).Add(-time.Hour)` 与 `+time.Hour` 两个候选，回读墙钟仍不一致即确认缺口；
  4. **重叠**：同一墙钟在 fall-back 出现两次，用固定偏移法枚举两个候选 UTC——`cand1 = time.Date(..., time.UTC).Add(-offsetBefore)`、`cand2 = ...Add(-offsetAfter)`（offset 取自 `loc` 在该日切换前后两次 `Zone()`），两者回读墙钟都等于输入时，取**较小（较早）**的 UTC。
- 该函数是 `daily/weekly/cron` 首个目标与后续目标计算的唯一入口，必须与 DST 边界测试同批交付（缺口/重叠各 ≥2 例，含 `America/New_York`、`Asia/Shanghai`、`Australia/Lord_Howe` 半时区）。
- **首个目标严格晚于当前时间**；一次性记录只存 UTC，不保留输入时区。
- **补发**：`every` 用算术 `floor((now-target)/interval)` 直接推进到最近一次；`daily/weekly/cron` 从 `now` 的本地日期向前回溯到最近一次匹配（不跨历史累积）。
- 年份范围限制 `0001–9999`；无未来目标时任务投递后保留 `inactive`。

### 5.3 cron 方言与规范化

- 五字段：`minute(0-59) hour(0-23) dom(1-31) month(1-12) dow(0-7，0 与 7 均为周日)`。
- 每字段接受 `*`、单值、`a-b`、`*/n`、`a-b/n`、逗号列表；拒绝 `L/W/#`、名称、`@daily` 宏、六字段、越界、反向范围、零步长、空字段。
- **规范化**：展开为匹配值集合后确定性重编码——重复合并、相邻合并、均匀步长写 `a-b/n`、以 `*` 开头的字段写星号步长、步长 1 移除、周日统一写 `0`。解码器拒绝非规范化存储。
- DOM/DOW：字段文本以 `*` 开头即为星号字段；任一为星号字段则两者都需匹配，否则任一匹配即可。
- 最小粒度 1 分钟。

> **工作量提示（评审 F6）**：cron 应作为**独立子模块 `cron.go` + 独立测试集**交付（解析、规范化、DOM/DOW、400 年搜索地平线、与 DST 叠加），预估数百行实现 + 大量边界用例；**不随领域层"顺手"完成**。选型上优先自实现以精确控制规范化与 Vixie 语义（Go 标准库无 cron）；若引第三方库，仅用于解析、规范化与 DOM/DOW 判定仍自控。

### 5.4 领域函数（纯函数，便于单测）

```go
// 创建
func CreateAfter(id ScheduleID, prompt, title string, afterSeconds int64, now time.Time) (ScheduleRecord, error)
func CreateAt(id ScheduleID, prompt, title string, at AtInput, now time.Time) (ScheduleRecord, error)
func CreateEvery(id ScheduleID, prompt, title string, everySeconds int64, now time.Time) (ScheduleRecord, error)
func CreateDaily(id ScheduleID, prompt, title string, in DailyInput, now time.Time) (ScheduleRecord, error)
func CreateWeekly(id ScheduleID, prompt, title string, in WeeklyInput, now time.Time) (ScheduleRecord, error)
func CreateCron(id ScheduleID, prompt, title string, in CronInput, now time.Time) (ScheduleRecord, error)

// 发生点推进：返回最近一次到期 + 下一个未来目标（无则 nil）
func ResolveOccurrence(rec ScheduleRecord, acceptedAt time.Time) (Occurrence, error)

// 规范化与解析
func NormalizeCron(expr string) (string, error)
func ParseTimeZone(tz string) (*time.Location, error)

// 模型可见视图与投递文本
func View(rec ScheduleRecord, now time.Time) ScheduleView
func RenderOneShotFraming(rec ScheduleRecord) string
func RenderRecurringBatchFraming(items []Occurrence) string
```

---

## 6. 调度运行时（`internal/schedule/runtime.go`）

### 6.1 单定时器 + 单写者队列

```go
type Runtime struct {
    svc      *Service
    timer    *time.Timer
    running  bool
    requested bool
    stopping bool
    chain    chan func() // 或基于 mutex+cond 的串行队列，对齐 background 的写者模型
}
```

- `RequestDrive()`：清旧 timer、置 `requested`；若已有 drive 在跑则合并；否则启动一次 drive。
- 所有管理写（create/update/delete/stopSession）与投递推进都进入**同一串行队列**，保证"读-改-写"与投递推进不交错。
- 一次 drive（对齐 dsh `ScheduleRuntime.drive`）：

```
1. now := clock.Now()
2. due := store.ListActiveDue(now)                 // status=active AND scheduled_at <= now
3. 按 session_id 分组周期任务；一次性任务各自成组
4. for each group:
     a. actor, err := hub.GetOrCreate(group.sessionID)   // chat.SessionHub；冷会话在此重建 actor
        （失败 → 记 failed，见步骤 7 的重试策略）
     b. admitted := group 中 scheduled_at <= now 的成员（恢复可能跨时钟回拨）
     c. 渲染文本：一次性 → RenderOneShotFraming；周期 → RenderRecurringBatchFraming
     d. actor.SubmitPromptAsync(ctx, text, scheduleRunMeta())  // 见 §6.3；这会启动一次会话 run
     e. 逐条 CAS commit（投递确认 = 步骤 d 返回 nil）：
          - 一次性 → status=inactive + 追加回执
          - 周期   → scheduled_at=next（无则 inactive）+ 追加回执
     f. 失败的成员进入 failed 集合，按退避重试（见下）
5. next := min(active 且非 failed 的 scheduled_at)
6. 设 timer = next（超过 MAX_TIMER_DELAY 分段；unref 等价物由宿主决定）
7. failed 成员：attempt += 1；attempt < MaxDeliveryAttempts 时按退避（30s/1m/2m/… 上限 10m）重排；
   attempt 达到上限 → 任务置 inactive 并记 delivery_failed 事件（死信），不再空转
```

**投递重试与死信（评审 F8）**：复用 `background` 的 `RecoveryBackoffSchedule` 口径（`internal/config/manager.go:385,595`）。永久不可达的会话（已删除/已归档/`GetOrCreate` 持续失败）在 `MaxDeliveryAttempts`（默认 5）后转为 `inactive` 并留痕，**避免每轮 drive 空转**。

### 6.2 跨重启与多实例

> **架构修订（守护进程）**：定时器**不放宿主进程内**——宿主不运行则任务永不触发。改为**独立守护进程** `cmd/scheduled`（Windows 服务 / systemd）长驻调度，宿主只做控制与执行。详见 [`scheduler-daemon-design.md`](./scheduler-daemon-design.md)。

> **v1.1 补记（2026-10-14，联盟式调度）**：当时否掉"宿主内嵌定时器"的三个理由——① 宿主不运行=不触发、② 多实例竞争、③ 双投递——现已分别由 ① mesh 租约选主（任一成员在岗即可触发，daemon 仍为默认常驻成员与兜底）、② 单实例租约 + 任务级 CAS claim、③ 交付账本 + `occurrence_key` 幂等去重 化解。宿主（runtime-server，可选 aicli）可按配置参与调度领导权竞争；控制面"不持定时器"的表述保留不变。见 [`mesh-scheduler-federation-design.md`](./mesh-scheduler-federation-design.md) §3.5/§12.6。

- **调度者**：`cmd/scheduled` 启动时扫描一次 `active` 任务并 `RequestDrive()`，随后持**单定时器**（"至多一个待触发"）；进程重启由启动扫描恢复。
- **单实例（取代原 F9/F10 的"单实例假设 + wake"）**：复用 `internal/mesh` 的**租约**（`mesh.Acquire(paths, "schedule-scheduler", "global", owner, …)`）——**只有持租约的 daemon 触发与投递**，多实例自动接管（`mesh.Takeover`）。**不再需要** supervision 的 `WakeScheduler`/`schedule_wake_link`（daemon 自长驻）。
- **存储共享**：daemon 与控制面共享 `schedule.sqlite`（WAL + busy_timeout + 单连接池）；`owner_instance_id` 记为 **daemon 实例**。跨库约束不变：**不读 `background.sqlite` 的 `runtime_instances`**。
- **变更通知**：daemon 短周期轮询 `MIN(scheduled_at)`（1s 级）；控制面可选地写库后提示 daemon 立即重算。

### 6.3 投递语义（本项目真实入口）

> **评审 F1/F2 更正**：原稿称"复用 durable inbox + `sessions.flush()` 两阶段确认"——**两者在本项目都不存在/不适用**。supervision 的 "durable inbox" 是**监督通知存储**（`supervision/background_job.go:87`），不是通用会话消息注入；本项目也**没有 `sessions.flush` API**。正确入口如下。

- **注入入口（同进程）**：`hub.GetOrCreate(sessionID)`（`chat/hub.go:157`）取到 `*SessionActor`，再调 **`actor.SubmitPromptAsync(ctx, text, runMeta, opts...)`**（`chat/actor.go:593`）。同步版 `SubmitPrompt`（`chat/actor.go:525`）仅用于需要等待结果的场景；Schedule 用异步版。
- **注入入口（跨进程，daemon 场景）**：daemon 不在宿主进程内，**不能直接调 actor**。三级兜底：① **delegate**——`POST /api/runtime/sessions/{id}/runtime/commands`（`submit_prompt`，`handler.go:1089`）→ 宿主 `GetOrCreate` + `SubmitPromptAsync`；② **embedded**——daemon 内嵌执行栈，取 **session 租约**后自行 `GetOrCreate` + `SubmitPromptAsync`（无人值守）；③ **mailbox**——写持久 `trigger_turn` 消息（`chat/trigger_turn_drain.go`）兜底。详见 [`scheduler-daemon-design.md`](./scheduler-daemon-design.md) §6 与 [`embedded-execution-design.md`](./embedded-execution-design.md)。
- **语义**：这会**真正启动一次该会话的模型 turn**（与 dsh 的 followup 一致）；Schedule 不等待 turn 完成。
- **消息形态**：提交的 `text` 为固定文本 + JSON 载荷，`runMeta` 携带 `source="schedule"` 标识（与后台作业完成通知区分）：
  - 一次性：`[SCHEDULE REMINDER]` + `schedule_id_json` / `occurrence_at` / `reminder_prompt_json`
  - 周期批次：`[SCHEDULE REMINDER BATCH]` + `reminders_json`（每条 `schedule_id`/`occurrence_at`/`reminder_prompt`）
- **投递确认（评审 F2，采用方案 A）**：确认 = `SubmitPromptAsync` 返回 nil（prompt 被 actor 接受）；随后写回执。**不等待**消息落盘或 turn 完成。
- **明确不保证 exactly-once**：提交与任务写是两次独立写入，崩溃可能重复投递；文档与工具描述中显式声明。
- **到期投递不等待目标 Agent 空闲**（`SubmitPromptAsync` 对 busy 会话按 actor 自身语义排队）。

### 6.4 一致性

| 场景 | 机制 |
|---|---|
| 并发管理写 | 单写者串行队列 |
| 投递 vs 编辑 | 同队列串行；投递推进目标后，旧 `expected` 的 update 返回 `schedule_conflict` |
| 多实例 | schedule 自建 owner 租约 + CAS（P2；不跨库复用 background 的 `runtime_instances`） |
| 取消 | 写入开始后不可回滚（与 dsh 一致），取消只影响尚未开始的写 |

---

## 7. 模型工具层（`internal/toolbroker`）

### 7.1 工具族

在 `broker.go` 工具名常量区新增：

```go
ToolScheduleCreate = "schedule_create"
ToolScheduleList   = "schedule_list"
ToolScheduleUpdate = "schedule_update"
ToolScheduleDelete = "schedule_delete"
```

**命名与描述必须与后台作业彻底区分（评审后补充）**。本项目 `task_*` 词根已被三个域重载——后台作业（`task_output`/`task_kill`/…）、团队任务（`read_task_spec`/`report_task_outcome`/…）、子代理任务（`subagent_inspect_task`）——项目自己都在 `broker_arg_audit.go:87-90` 用 `job_id` vs `task_id` 加注释区分。因此：

| 族 | 命名 | 描述首句必须含 | 禁止 |
|---|---|---|---|
| **自动化任务**（本方案） | `schedule_*` | "**reminder**" / "schedule" / 自动化任务 | 不写 "job"；"task" 仅用于"触发一次会话 run" |
| 后台作业 | **`job_*`**（canonical；`task_*` 保留为兼容别名） | "background **job**" / `job_id` | 不用 "task" 作主语 |
| 团队任务 | `read_task_spec` 等（`task_*` 归团队） | "**team** task" / "team run" | 不写 "job" |

`schedule_*` 与 `job_*` **并列但独立**：不复用词根、不互相借用参数名（`schedule` 用 `id`；作业用 `job_id`）。`schedule_create` 的描述需显式声明"这**不是**后台作业：不会立即执行任何命令，只登记提醒，到点由原会话处理"。作业工具改名为 `job_*` 的完整方案（含 `task_*` 兼容别名与迁移）见 [`background-jobs-tool-surface-and-profile-governance.md`](./background-jobs-tool-surface-and-profile-governance.md) §2。

### 7.2 工具契约

> **评审 F4 更正**：本项目**没有 dsh 的 `defineTool({output:{schema,render}})`**。工具用 `types.ToolDefinition`（`internal/toolbroker/broker.go:216` 的 `Definitions()` 返回，参考 `:406` 的 `ToolTaskOutput`），参数为 `Parameters map[string]interface{}`（JSON Schema，见 `internal/toolkit/interface.go:22`）。**无 output schema 概念**，结果由 execute 直接返回结构化值。

| 工具 | 参数 | 返回 |
|---|---|---|
| `schedule_create` | `prompt`(必填)、`title`(必填)、恰好一个 `after_seconds`/`at`/`every_seconds`/`daily`/`weekly`/`cron` | 规范视图 或 错误码 |
| `schedule_list` | 无 | 活动提醒视图数组 |
| `schedule_update` | `id`(必填) + 可选 `title`/`prompt` + 至多一个时间选择器 | 视图、`{updated:false, code}` 或错误码 |
| `schedule_delete` | `id`(必填) | `{id, deleted:true}` 或 `{id, deleted:false, code:"schedule_not_found"}` |

- **选择器 XOR 校验**在工具层与服务层各做一次（纵深防御）。
- **title 校验**：trim 后非空、≤120；缺失/空白/超长返回 `invalid_prompt`；**绝不从 prompt 派生**。
- **update 语义**：省略字段保留原值；时间变更可跨 Kind；归一化等价则 `updated:false` 且不重写、不重设目标；`after_seconds` 不允许出现在 update。
- **错误码收敛**：`ScheduleInputError` → 原样错误码；其他失败 → `internal_error`（不外泄存储细节）。
- **子 Agent 拒绝**：执行上下文无调用 Agent → `internal_error`；委派深度 > 0 → `subagent_session`（判据 `agentcontrol.AgentRecord.Depth`）。
- **参数审计表同步**：新增 `schedule_*` 必须在 `broker_arg_kinds.go` / `broker_arg_audit.go` 登记字段类型，否则参数规范化与漂移测试（`broker_arg_audit_test.go`）会失败。
- 工具描述中显式写明："恢复/重启后周期提醒只补发最近一次"、"崩溃后可能重复投递"、"删除不会撤回已入队消息"。

### 7.3 错误码（对齐 dsh，新增 `schedule_*` 前缀语义）

`invalid_prompt` / `invalid_selector` / `invalid_rule` / `invalid_time_zone` / `not_future` / `time_out_of_range` / `frequency_too_high` / `subagent_session` / `schedule_not_found` / `schedule_ended` / `schedule_conflict` / `delivery_cursor_not_found` / `internal_error`。

---

## 8. 权限与边界

1. **会话绑定**：任务绑定创建时的 `session_id`；`list/catalog/history/update/delete` 均要求绑定匹配，否则 `schedule_not_found`。绑定校验 ≠ 调用者鉴权，需在文档中声明。
2. **子 Agent 拒绝**：`create` 拒绝、`update` 返回非变更拒绝；判据为"该会话的 Agent 委派深度 > 0"，且需**冷恢复后仍成立**（读取持久化的 session header，而非仅内存态）。`list/catalog/history/delete` 仍可服务，使旧任务可删。
3. **归档准入（评审 F3：本项目没有 dsh 式可插拔准入族，必须新增检查点）**：
   - 现状：归档是直接执行的——`chat.Manager.Archive`（`chat/manager.go:465`）、`handler.ArchiveSession`（`api/runtimeapi/handler.go:3473`）、`BatchArchiveSessions`（`:3523`）、自动归档 `startArchive`（`chat/manager.go:579`）。
   - 新增：在归档路径调用 `ScheduleService.ActiveTasks(sessionID)`；有活动任务则拒绝归档并返回可解释原因。
   - 停止语义：归档时若用户选择"停止任务"，在同一串行队列槽位内删除活动行（不重入公开 `Delete`，避免自锁）；自动归档同样需要该检查，否则会在无提示下丢弃活动任务。
   - 该改动**属于新增**，不是"接入已有钩子"。
4. **profile 归属**：`schedule_*` 工具是否暴露由**本项目 profile 的工具策略**决定（`internal/profile/spec.go` 的 `ToolPolicySpec{Allowlist,Denylist}`；本项目**没有 dsh 的 `toolFilter.deny`**）。子 Agent 的收紧走 `agent.ToolExecutionPolicy`（`AllowedTools`/`DeniedTools`/`AllowlistEnabled`，`internal/agent/tool_policy.go`）与工具分组（见 [`tool-surface-inventory-and-naming-review.md`](./tool-surface-inventory-and-naming-review.md)）。

---

## 9. Web / API 设计

### 9.1 HTTP API（`backend/internal/api/runtimeapi/`）

新增 `schedule_handlers.go` + `schedule_routes.go`，挂在 `/api/runtime` 子路由；**查询参数用本项目 snake_case 规范**（非 dsh 的 camelCase）。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/runtime/schedule/catalog` | 全部活动+未运行任务（含 `session_id`、`status`、`last_delivery`），按 `scheduled_at` 升序、`id` 字典序 |
| `GET` | `/api/runtime/schedule/list?session_id=` | 指定会话的活动任务 |
| `GET` | `/api/runtime/schedule/history?session_id=&id=&limit=&before=` | 发送记录，`limit` 1..100，追加序逆序 |
| `POST` | `/api/runtime/schedule/update` | `{session_id,id,expected,change?,title?,prompt?}` |
| `POST` | `/api/runtime/schedule/delete` | `{session_id,id}` |
| `SSE` | `schedule.changed` | 无载荷通知，客户端刷新列表；重连后重新拉取 |

**鉴权（评审 F5）**：与既有 runtime API 一致——schedule 端点需遵循 `frontend/src/lib/admin-token.ts` 的 admin token 约定；**SSE 同样需要 token 校验**（不得因无鉴权而泄露跨会话任务元数据）。会话绑定校验是**数据归属**校验，**不等于**调用者鉴权，二者都要做。

### 9.2 多端入口（四个前端 + 统一 API）

本项目实际有 **4 个用户可达前端**，Schedule 必须逐端设计（详见 [`frontend-business-chain.md`](./frontend-business-chain.md)）：

| 端 | 代码位置 | 技术 | 交付 |
|---|---|---|---|
| **E1 frontend（主 Web UI）** | `frontend/src/` | React + Vite + TS | 全局页 `/runtime/schedule` + `panel-registry.ts` 新增 `schedule` 面 + `api/runtime/schedule.ts` / `hooks/workspace/use-schedule-tasks.ts` / `types/runtime/schedule.ts` / `i18n/.../panels-schedule.ts`（照抄 Jobs 范式） |
| **E2 micro web client** | `backend/cmd/aicli/commands/web/` | 内嵌 HTML + 原生 JS（免构建） | `js/schedule.js` + `index.html` 页签 + `ui.js`/`sse.js` 接线 + `scripts/verify-micro-web-schedule.mjs` |
| **E3 aicli TUI** | `backend/cmd/aicli/{ui,commands}/` | Go 自绘终端 | `/schedule` slash 命令 + `statusbar` 计数，复用 `fullscreen_list` |
| **E4 gateway console（可选）** | `backend/cmd/gateway/console/` | PWA | 只读列表/详情（P6） |

**业务场景覆盖（14 项）**：创建/列表(全局+会话)/详情/编辑保存/删除/运行记录/搜索筛选/会话跳转/实时刷新/状态机/归档联动/时间展示/权限禁用。E1 覆盖全量，E2/E3 做会话内最小闭环，**创建始终由模型完成**（UI 只做"新建"引导）。

**关键链路要点**：
- **保存**：进入编辑时捕获完整 `expected`，表单维护本地草稿；保存处理成功 / `schedule_conflict` / `schedule_ended|schedule_not_found` 三类结果；保存中或失败期间保留所显示任务。
- **读取生命周期**：单一 catalog 源 + `schedule.changed`/连接重置失效；用**序号**而非时间戳判定"删除已生效"，避免比较浏览器与宿主时钟。
- **删除提示**：应用级 overlay 提示，生命周期长于发起它的面板/页签。

---

## 10. 分期实施计划（修订版）

> 本节已按 [`completeness-review-and-integration.md`](./completeness-review-and-integration.md) §7、[`frontend-business-chain.md`](./frontend-business-chain.md) §5、[`scheduler-daemon-design.md`](./scheduler-daemon-design.md)、[`embedded-execution-design.md`](./embedded-execution-design.md) 修订。

### 总览（统一路线图）

| 阶段 | 后端 | 调度/执行 | 前端 | 出口 |
|---|---|---|---|---|
| **P0** | 领域层 + 存储 + 工具 + 控制面 + **前台 daemon（D0）** | 单定时器 + mesh 租约 + delegate 投递 | — | 一条提醒可到点投递 |
| **P1** | 完整时间语义 + 权限边界 + HTTP API | — | — | 六规则 + API 可用 |
| **P2** | — | **守护进程服务化（D1–D3）** | — | 长驻/开机自启/离线兜底 |
| **P3** | — | — | **E1 frontend** | 主 Web UI 全量闭环 |
| **P4** | — | — | **E2 micro web + E3 TUI** | 四端口径一致 |
| **P5** | 可选抽取 `runtimehost.Executor` | **内嵌执行（E0–E3）** | — | 完全无人值守 |
| **P6** | 观测/收敛 | 租约/wake 收敛 | E4 gateway（可选） | 增强 |

### P0 —— 领域层 + 存储 + 工具 + 前台守护进程（D0）

| 步骤 | 交付物 | 文件 |
|---|---|---|
| P0-1 | `ScheduleRecord`/`ScheduleTask` 类型、校验、错误码 | `internal/schedule/types.go`、`errors.go` |
| P0-2 | **仅** `after`/`at`/`every` 三选一的创建 + 发生点计算（`daily/weekly/cron` 归 P1） | `internal/schedule/domain.go` |
| P0-3 | SQLite store + `migrate` + `state_version` CAS + owner 列 | `internal/schedule/store.go` |
| P0-4 | `Service`（Create/List/Delete/Update）+ 单写者队列（**控制面，不持定时器**） | `internal/schedule/service.go` |
| P0-5 | **`cmd/scheduled run`（前台 D0）**：单定时器 + 启动扫描 + `mesh` 单实例租约 + delegate 投递（HTTP `submit_prompt`） | `cmd/scheduled/`、`internal/schedule/runtime.go` |
| P0-6 | 四个 `schedule_*` 工具 + 参数审计表 | `internal/toolbroker/schedule_tools.go`、`broker_arg_kinds.go` |
| P0-7 | `ScheduleConfig`（`enabled`、`deliveryHistory*`、`maxActivePerSession`、`execution.mode`） | `internal/config/manager.go` |
| P0-8 | runtime-server / aicli 宿主接线（控制面；**不持定时器**） | `cmd/runtime-server/main.go`、`cmd/aicli/commands/chat_actor_host.go` |

**P0 出口标准**：`schedule_create` 一条 `after_seconds` 任务 → 前台 `scheduled run` 到点投递到原会话；`schedule_list` 可见；`schedule_delete` 后不再投递；daemon 重启后任务仍在且只补发最近一次。

### P1 —— 完整时间语义 + 权限边界 + HTTP API

- `daily` / `weekly` / `cron` 三种钟表规则 + DST 缺口/重叠 + cron 规范化（自实现五字段解析器）。
- `schedule_update` 完整 compare-and-set（`expected` + `schedule_conflict`）。
- 子 Agent 拒绝（工具层 + 服务层 + 工具过滤；判据 `agentcontrol.AgentRecord.Depth > 0`）。
- 归档准入（活动任务阻止归档、stop 在同一队列槽位删除活动行）。
- 发送历史表 + 裁剪 + 分页（`schedule_delivery_history`）。
- **HTTP API**：`internal/api/runtimeapi/schedule_handlers.go` + `schedule_routes.go`；`schedule.changed` 接入事件投递。

**P1 出口标准**：三种钟表规则的 DST 边界用例通过；`update` 冲突返回 `schedule_conflict` 且不覆盖；子 Agent 调用被拒；归档被活动任务阻止；API 可查 catalog/list/history。

### P2 —— 调度守护进程服务化（D1–D3）

| 步骤 | 交付物 | 出口 |
|---|---|---|
| D1 | 持久 mailbox 兜底（`trigger_turn`）+ 重试退避 + `MaxDeliveryAttempts` 死信 | 宿主离线时消息落库，恢复后补发 |
| D2 | `scheduled install/uninstall/start/stop/status`（Windows Service + systemd）+ `doctor` | 随系统启动；重启机器后仍触发 |
| D3 | 本机 `healthz`/`status` + 观测（租约、下次触发、待投递） | 运维可查 |

**P2 出口标准**：装为系统服务后长驻；杀进程由系统重启；两个实例只有一个触发（mesh 租约）。

### P3 —— E1 frontend（主 Web UI，全量业务闭环）

- 全局页 `/runtime/schedule`（`App.tsx` 一行）+ `panel-registry.ts` 新增 `schedule` 面。
- `api/runtime/schedule.ts`、`types/runtime/schedule.ts`、`hooks/workspace/use-schedule-tasks.ts`。
- 面板：列表（搜索/筛选）、详情（规则 / 运行记录两页签）、**编辑保存（草稿 + 冲突 UX）**、删除确认 + 应用级提示。
- `i18n/resources/{zh-CN,en}/workspace/panels-schedule.ts`。

**P3 出口标准**：E1 覆盖 14 项业务场景（见 `frontend-business-chain.md` §2）。

### P4 —— E2 micro web + E3 aicli TUI（会话内最小闭环）

- **E2**：`js/schedule.js` + `index.html` 页签 + `ui.js`/`sse.js` 接线 + `scripts/verify-micro-web-schedule.mjs`。
- **E3**：`/schedule` slash 命令（list/show/delete/new）+ `statusbar` 活动任务计数。
- 归档联动禁用态接入（E1/E2）。

**P4 出口标准**：四端对同一任务的展示口径一致；micro web 验证脚本全过。

### P5 —— 内嵌执行（E0–E3，完全无人值守）

| 步骤 | 交付物 | 出口 |
|---|---|---|
| E0 | 路径 A：daemon 构造 `bootstrap.Manager` + `runtimeapi.Handler`（不启 HTTP）；`execution.mode` 配置 | 空闲会话可被 daemon 自行执行一次 |
| E1 | **session 租约仲裁** + 专用 `scheduled` profile（受限工具面 / 审批预置拒绝） | 用户聊天中的会话不被并发执行 |
| E2 | 并发/时长上限（`maxConcurrentExecutions`/`maxTurnDuration`）+ 观测 | 超时中断并留痕 |
| E3（可选） | 路径 B：抽取 `internal/runtimehost.Executor`，runtime-server 与 scheduled 共用 | 两宿主同源执行 |

**P5 出口标准**：关闭 runtime-server 后，daemon 仍能对空闲会话执行提醒；用户活跃会话不被抢占。

### P6 —— 可选增强

- **E4** gateway console 只读列表/详情。
- 观测指标收敛（投递成功/失败/重试、逾期数、执行时长）。
- 作业终态 → `at` 提醒桥（组合场景 C3）。

---

## 11. 验收标准（对齐 dsh 的已知行为）

1. **六种选择器**各自创建成功并得到规范视图；选择器 XOR 校验生效。
2. **title 必填**：缺失/空白/超长返回 `invalid_prompt`，不从 prompt 派生。
3. **未来性**：`at` 非未来返回 `not_future`；`every_seconds < 60` 返回 `frequency_too_high`；无未来目标返回 `time_out_of_range`。
4. **DST**：缺口本地时间被跳过；重叠取较早时点；每个本地日期只触发一次。
5. **补发**：停机跨过多次 `every`/`daily` 发生时点，重启后只投递最近一次。
6. **批次**：同一会话同一时刻到期的多条周期任务合并为一条 follow-up。
7. **更新**：等价归一化返回 `updated:false` 且不重设目标；`expected` 过期返回 `schedule_conflict`。
8. **删除**：任务从 `list`/`catalog` 消失，历史不可读，已入队消息不撤回。
9. **权限**：子 Agent 的 `create/update` 被拒，`list/delete` 可用；归档被活动任务阻止。
10. **崩溃语义**：文档与工具描述明确"可能重复投递"，且实现不谎称 exactly-once。
11. **守护进程**：装为系统服务（Windows Service / systemd）后长驻；杀进程由系统重启；两个实例只有一个触发（mesh 租约）；daemon 停止期间任务不丢，重启补发。
12. **离线兜底**：宿主离线时投递落持久 mailbox，宿主恢复后补发；达 `MaxDeliveryAttempts` 转 `inactive` 并留痕。
13. **内嵌执行（embedded）**：关闭 runtime-server 后，daemon 仍能对**空闲**会话执行提醒；用户活跃会话**不被抢占**（session 租约仲裁）；无人值守下审批类工具被预置拒绝而非挂起。

---

## 12. 测试策略

| 层级 | 手段 |
|---|---|
| 领域纯函数 | 表驱动单测：六种规则、DST 边界（用固定 `time.Location` + 构造时点）、cron 规范化/拒绝、发生点推进 |
| 存储 | SQLite 迁移/回放；CAS 影响行数=0 的放弃路径；历史裁剪与分页游标 |
| 运行时 | 注入假时钟；单定时器重算；drive 分组/合并；失败保留义务；停止排空 |
| 守护进程 | mesh 租约单实例（两实例只有一个触发）；安装/卸载/自启；`doctor`；离线投递落 mailbox 与恢复补发 |
| 内嵌执行 | 模式切换（delegate/embedded/auto）；session 租约仲裁（活跃会话不被抢占）；超时中断；`scheduled` profile 审批预置拒绝 |
| 工具 | 参数 XOR/title/错误码；子 Agent 拒绝；`internal_error` 不外泄 |
| 集成 | daemon 启动扫描 + 重启补发；归档准入；经 `SessionHub`/`SubmitPromptAsync`（或跨进程 HTTP/mailbox）投递到原会话 |
| E2E | 对齐本项目 `docs/e2e/` 方法论：真实进程 + 屏幕回读/接口断言 |

---

## 13. 风险与已知限制（需在实现文档中声明）

1. **非 exactly-once**：投递与会话持久化非原子，崩溃可能重复投递。
2. **执行依赖（默认）**：`delegate` 模式需 runtime-server 在线；由**独立守护进程**消除"调度依赖宿主"（见 [`scheduler-daemon-design.md`](./scheduler-daemon-design.md)），由 `embedded` 模式消除"执行依赖宿主"（见 [`embedded-execution-design.md`](./embedded-execution-design.md)）。投递失败按退避重试，达上限转 `inactive`。
3. **cron 最小 1 分钟**：五字段方言，不支持秒级与 `L/W/#/名称/@宏`。
4. **存储为整文档/整表重写风险**：若沿用单 JSON 文档后端则每次变更重写全部；建议用 SQLite 行级表（本方案已采用）。
5. **多实例**：由 `internal/mesh` 的**调度租约**保证单实例触发（见守护进程文档 §4）；`embedded` 另加 **session 租约**避免与交互宿主并发执行。
6. **删除即硬删**：历史一并移除，不可恢复。
7. **时区数据**：未来目标用宿主当前 IANA 数据；已提交的 UTC 目标在重启时**不重算**。

---

## 14. 与现有子系统的关系

```
background（后台作业）  ── 提交即执行、进程托管、job_* 工具（现 task_*，见命名规范）
        │  共享：SQLite + CAS + 一任务一 owner + 状态机原则
        ▼
schedule（自动化任务）  ── 到点触发、原会话投递、schedule_* 工具   ← 本方案
        │  共享：SessionHub/SessionActor 提交路径、事件总线、mesh 租约
        ▼
supervision（监督/唤醒）── durable wake、投递预算、生命周期（**不再承担 Schedule 调度**）
        │
        ▼
team / subagentbatch（工作流）── 多 Agent 编排（team_* 工具，与本方案无重叠语义）
```

**关键区分**：后台作业回答"帮我把这条命令跑完"；`schedule_*` 回答"到点提醒我/在会话里触发这件事"。二者可组合（例如定时任务的内容是让模型发起一个后台作业），但**不共用存储与工具命名空间**（作业用 `job_*`，任务用 `schedule_*`，团队用 `team_*`；见 [`tool-surface-inventory-and-naming-review.md`](./tool-surface-inventory-and-naming-review.md)）。

---

## 附录 A：宿主接线要点（草案）

**控制面**（`cmd/runtime-server/main.go` / `cmd/aicli`）——只做 create/list/update/delete，**不持定时器**：

```go
scheduleDataDir := filepath.Join(filepath.Dir(config.DefaultAuthStorePath()), "data", "schedule")
scheduleSvc, err := schedule.New(schedule.Options{
    Store:  schedule.NewSQLiteStore(filepath.Join(scheduleDataDir, "schedule.sqlite")),
    Config: cfg.Schedule.WithDefaults(),
    Clock:  clock.System(),
    Events: handler.EventBus(),
})
if err != nil { return nil, fmt.Errorf("failed to initialize schedule service: %w", err) }
handler.SetScheduleService(scheduleSvc)   // 仅管理接口；无 Runtime/Deliverer/WakeScheduler
defer scheduleSvc.Close()
```

**调度面**（`cmd/scheduled`）——独立守护进程，持有 Runtime 与投递：

```go
// cmd/scheduled（示意）
owner := mesh.LeaseOwner{...}
lease := mesh.Acquire(paths, "schedule-scheduler", "global", owner, mesh.AcquireOptions{TTL: 30 * time.Second})
if !lease.Granted { /* 待命：另一实例在调度 */ }
rt := schedule.NewRuntime(schedule.RuntimeOptions{
    Store:     store,                      // 共享 schedule.sqlite
    Clock:     clock.System(),
    Deliverer: schedule.NewHTTPDeliverer(cfg.Schedule.Execution.RuntimeServerURL, mailboxWriter), // delegate→mailbox
    Embedded:  cfg.Schedule.Execution.Mode == "embedded" || "auto", // 可选：内嵌执行栈
})
rt.Start(ctx)   // 启动扫描 + 单定时器；到点投递
```

## 附录 B：错误码与工具返回值摘要

- 本项目**无 dsh 的 `output.schema`**（评审 F4）：工具用 `types.ToolDefinition`，`execute` 直接返回结构化值（错误以 `{code,message}` 形式返回）。
- 视图字段：`id, kind, title, prompt, scheduledAt, state(scheduled|overdue), deliveryMode(host)` + 规则字段（`afterSeconds` / `everySeconds` / `time`+`timeZone` / `weekdays` / `expression`）。

## 附录 C：验收用例清单（节选）

| # | 用例 | 期望 |
|---|---|---|
| 1 | `after_seconds=600` 创建，假时钟 +600s | 原会话收到 1 条提醒，任务转 inactive |
| 2 | `every_seconds=300`，停机 25 分钟 | 重启后只投递 1 次（最近一次），目标推进到未来 |
| 3 | `daily 02:30 America/New_York` 落在 spring-forward 缺口 | 该日期跳过，次日 02:30 触发 |
| 4 | `daily 01:30` 落在 fall-back 重叠 | 只在较早时点触发一次 |
| 5 | `cron "*/15 9-17 * * 1-5"` | 创建后存储规范化表达式；工作日每 15 分钟 |
| 6 | `update` 携带过期 `expected` | 返回 `schedule_conflict`，存储不变 |
| 7 | 子 Agent 调用 `schedule_create` | 返回 `subagent_session` |
| 8 | 归档含活动任务的会话 | 被阻止；选择停止后活动任务被删除 |
| 9 | `schedule_delete` 后原会话 | 任务消失、历史不可读、已入队消息仍在 |
| 10 | 同会话两条周期任务同时到期 | 合并为一条 batch 消息 |

---

*本方案为设计文档，未包含代码实现；实施时按 P0→P1→P2 分阶段推进，每阶段以第 11 节验收标准与第 12 节测试策略把关。*
