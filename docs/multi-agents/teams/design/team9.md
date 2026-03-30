继续。这一轮我把你现在最值得补的两个点展开成可直接落地的方案：

1. `ReadTaskSpec` / `ReadTaskContext` 工具
2. `sqlite_repo.go` 的强事务版 `WithTx` 与高并发注意事项

这两块补完之后，Team 第一版基本就从“能跑”进入“能稳定跑”。

---

# 一、为什么现在要补 `ReadTaskSpec`

你现在的 teammate 主要靠 `dispatchOne()` 里一次性塞进去的 prompt 执行任务。这有两个问题：

第一，任务一旦长一点，agent 会忘记细节。
第二，任务在运行中可能被 replan、补充依赖、加 deliverables，仅靠初始 prompt 不够。

所以建议加一个**只读内建工具**：

* `ReadTaskSpec`
* 可选增强版 `ReadTaskContext`

其中第一版先做 `ReadTaskSpec` 就够了。

---

# 二、`ReadTaskSpec` 工具设计

## 2.1 目标

让当前 teammate 在执行中可以随时读取自己当前 task 的结构化定义，而不是只能靠 prompt 和记忆。

## 2.2 工具行为

它不接受 `team_id`、`task_id`、`agent_id` 这种可信身份参数，全部从 `RunMeta` 注入。

### 输入

第一版可以几乎无参：

```go
type ReadTaskSpecArgs struct {
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
}
```

### 输出

```go
type ReadTaskSpecResult struct {
	TaskID        string   `json:"task_id"`
	Title         string   `json:"title"`
	Goal          string   `json:"goal"`
	Inputs        []string `json:"inputs,omitempty"`
	ReadPaths     []string `json:"read_paths,omitempty"`
	WritePaths    []string `json:"write_paths,omitempty"`
	Deliverables  []string `json:"deliverables,omitempty"`
	Status        string   `json:"status"`
	Dependencies  []TaskDependencyInfo `json:"dependencies,omitempty"`
}

type TaskDependencyInfo struct {
	TaskID   string  `json:"task_id"`
	Title    string  `json:"title"`
	Status   string  `json:"status"`
	Summary  string  `json:"summary,omitempty"`
	ResultRef *string `json:"result_ref,omitempty"`
}
```

---

## 2.3 Repo 侧需要补的接口

在 `team/repo.go` 增加：

```go
GetTaskDependencies(ctx context.Context, teamID, taskID string) ([]Task, error)
```

它和 `ListDependents` 相反：
`ListDependents(taskID)` 查“谁依赖我”，
`GetTaskDependencies(taskID)` 查“我依赖谁”。

### SQLite 实现

```go
func (c *sqliteCore) GetTaskDependencies(ctx context.Context, teamID, taskID string) ([]Task, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT dep.id, dep.team_id, dep.parent_task_id, dep.title, dep.goal, dep.inputs_json,
		       dep.status, dep.priority, dep.assignee, dep.lease_until, dep.retry_count,
		       dep.read_paths_json, dep.write_paths_json, dep.deliverables_json,
		       dep.summary, dep.result_ref, dep.version, dep.created_at, dep.updated_at
		FROM team_task_dependencies d
		JOIN team_tasks dep ON dep.id = d.depends_on_task_id
		WHERE dep.team_id=? AND d.task_id=?
		ORDER BY dep.created_at ASC`, teamID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}
```

---

## 2.4 工具骨架

```go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"your/module/internal/runtime/chat"
	"your/module/internal/team"
)

var (
	ErrNoTaskRunMeta = errors.New("no task run metadata")
)

type TeamTaskReader interface {
	GetTask(ctx context.Context, teamID, taskID string) (*team.Task, error)
	GetTaskDependencies(ctx context.Context, teamID, taskID string) ([]team.Task, error)
}

type ReadTaskSpecArgs struct {
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
}

type TaskDependencyInfo struct {
	TaskID    string  `json:"task_id"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Summary   string  `json:"summary,omitempty"`
	ResultRef *string `json:"result_ref,omitempty"`
}

type ReadTaskSpecResult struct {
	TaskID       string               `json:"task_id"`
	Title        string               `json:"title"`
	Goal         string               `json:"goal"`
	Inputs       []string             `json:"inputs,omitempty"`
	ReadPaths    []string             `json:"read_paths,omitempty"`
	WritePaths   []string             `json:"write_paths,omitempty"`
	Deliverables []string             `json:"deliverables,omitempty"`
	Status       string               `json:"status"`
	Dependencies []TaskDependencyInfo `json:"dependencies,omitempty"`
}

type ReadTaskSpecTool struct {
	Repo TeamTaskReader
}

func NewReadTaskSpecTool(repo TeamTaskReader) *ReadTaskSpecTool {
	return &ReadTaskSpecTool{Repo: repo}
}

func (t *ReadTaskSpecTool) Name() string {
	return "ReadTaskSpec"
}

func (t *ReadTaskSpecTool) Execute(ctx context.Context, raw json.RawMessage) (*ReadTaskSpecResult, error) {
	var args ReadTaskSpecArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("parse args: %w", err)
		}
	}

	meta, ok := chat.GetRunMeta(ctx)
	if !ok || meta == nil || meta.Team == nil || meta.Team.TeamID == "" || meta.Team.CurrentTaskID == "" {
		return nil, ErrNoTaskRunMeta
	}

	task, err := t.Repo.GetTask(ctx, meta.Team.TeamID, meta.Team.CurrentTaskID)
	if err != nil {
		return nil, err
	}

	out := &ReadTaskSpecResult{
		TaskID:       task.ID,
		Title:        task.Title,
		Goal:         task.Goal,
		Inputs:       task.Inputs,
		ReadPaths:    task.ReadPaths,
		WritePaths:   task.WritePaths,
		Deliverables: task.Deliverables,
		Status:       string(task.Status),
	}

	if args.IncludeDependencies {
		deps, err := t.Repo.GetTaskDependencies(ctx, meta.Team.TeamID, meta.Team.CurrentTaskID)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			out.Dependencies = append(out.Dependencies, TaskDependencyInfo{
				TaskID:    d.ID,
				Title:     d.Title,
				Status:    string(d.Status),
				Summary:   d.Summary,
				ResultRef: d.ResultRef,
			})
		}
	}

	return out, nil
}
```

---

# 三、`ReadTaskContext` 增强版怎么做

如果你还想再往前走一步，可以做 `ReadTaskContext`，它是 `ReadTaskSpec + mailbox digest + dependency summaries` 的组合。

### 输入

```go
type ReadTaskContextArgs struct {
	IncludeDependencies bool `json:"include_dependencies,omitempty"`
	IncludeMailbox      bool `json:"include_mailbox,omitempty"`
	MailboxLimit        int  `json:"mailbox_limit,omitempty"`
}
```

### 输出

```go
type ReadTaskContextResult struct {
	Spec          ReadTaskSpecResult `json:"spec"`
	MailboxDigest string             `json:"mailbox_digest,omitempty"`
}
```

实现方式很简单：

* 内部复用 `ReadTaskSpecTool`
* 再复用 `ReadMailboxDigestTool`
* 拼成一个组合结果

但第一版我建议先不上，因为：

* prompt 里已经有初始 mailbox digest
* 团队通路刚搭起来，先保证身份和已读逻辑稳定更重要

---

# 四、在 prompt 里如何引导 agent 用 `ReadTaskSpec`

你现有 `BuildTeammatePrompt()` 可以补一句非常重要的话：

```text
When you need to re-check your exact task definition, allowed paths, deliverables, or dependency status, call ReadTaskSpec.
```

再加一条：

```text
Before making edits, verify your allowed write paths via ReadTaskSpec if uncertain.
```

这样可以明显减少 agent 漫游到任务边界外。

---

# 五、为什么要升级 `WithTx`

现在 `WithTx()` 还是：

```go
tx, err := db.BeginTx(ctx, nil)
```

对于普通 CRUD 没问题，但对 Teams 的这几个路径不够稳：

* `dispatchOne(): CanClaim -> ClaimTask -> AcquirePathClaims`
* `RenewLease`
* `RequeueExpiredTasks`
* `InsertMail + fanout receipts`

尤其是 task claim / path claim，在高并发下很容易出现“都看见可用，然后抢同一资源”的窗口。

虽然 SQLite 最终会串行化写，但如果你没有尽早拿写锁，读阶段仍然可能基于过时快照做判断，造成大量无谓重试、冲突或错误行为。

所以建议给 repo 增加一个**强事务入口**，专门给编排关键路径用。

---

# 六、强事务设计

## 6.1 新增接口

在 `team/repo.go` 增加：

```go
type Repo interface {
	WithTx(ctx context.Context, fn func(TxRepo) error) error
	WithImmediateTx(ctx context.Context, fn func(TxRepo) error) error
	...
}
```

### 语义区别

* `WithTx`: 普通事务，适合大部分 CRUD
* `WithImmediateTx`: 立即写事务，适合 claim / lease / mailbox fanout 这种强一致路径

---

## 6.2 SQLite 实现思路

Go 的 `database/sql` 没有直接暴露 SQLite 的 `BEGIN IMMEDIATE` 选项，所以常见做法是：

1. 拿一个专属连接 `db.Conn(ctx)`
2. 显式执行 `BEGIN IMMEDIATE`
3. 在这个连接上执行事务逻辑
4. 成功 `COMMIT`
5. 失败 `ROLLBACK`

### 关键点

这一套不能混着 `db.BeginTx()` 用，否则你拿不到明确的 `BEGIN IMMEDIATE` 语义。

---

## 6.3 实现骨架

```go
type connTxRepo struct {
	conn *sql.Conn
	*sqliteCore
}

func (r *SQLiteRepo) WithImmediateTx(ctx context.Context, fn func(TxRepo) error) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}

	tr := &connTxRepo{
		conn:       conn,
		sqliteCore: &sqliteCore{q: conn},
	}

	defer func() {
		// 如果上层已经 COMMIT/ROLLBACK，再次执行会返回错误，可忽略
	}()

	if err := fn(tr); err != nil {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		return err
	}

	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func (r *connTxRepo) WithTx(ctx context.Context, fn func(TxRepo) error) error {
	return fn(r)
}

func (r *connTxRepo) WithImmediateTx(ctx context.Context, fn func(TxRepo) error) error {
	return fn(r)
}
```

---

# 七、哪些地方必须切到 `WithImmediateTx`

不是所有事务都要用强事务，太多会降低吞吐。

我建议只有下面四类必须切：

## 7.1 `dispatchOne()`

原来：

```go
err := o.Repo.WithTx(ctx, func(tx TxRepo) error { ... })
```

改成：

```go
err := o.Repo.WithImmediateTx(ctx, func(tx TxRepo) error { ... })
```

因为这里包含：

* 看 path claim
* claim task
* 插 path claim

这是最需要原子性的地方。

---

## 7.2 `RenewLease()`

原因：

* 要同时续 task lease 和 path claim lease
* 避免 lease 已续、claim 没续的中间态

建议：

```go
func (r *SQLiteRepo) RenewLease(...) error {
	return r.WithImmediateTx(ctx, func(tx TxRepo) error {
		...
	})
}
```

---

## 7.3 `RequeueExpiredTasks()`

原因：

* 批量选中过期任务
* 再把它们重新 ready
* 释放 claim
* 改 teammate 状态

如果不是同一个立即写事务，容易在中途被其他调度操作穿插。

---

## 7.4 `InsertMail()`

理由相对没那么强，但我仍建议切过去，因为：

* message 本体 + receipts fanout 应该原子
* 尤其是广播消息，不能出现 message 写进去了但 receipts 没完全写完

---

# 八、哪些地方不要用 `WithImmediateTx`

避免过度锁表。

下面这些继续用普通事务或无事务即可：

* `GetTask`
* `ListTasks`
* `ListReadyTasks`
* `ListUnreadMail`
* `ReadMailboxDigest`
* `ReadTaskSpec`
* `ListEventsAfter`

这些都是读多写少，不需要一上来拿写锁。

---

# 九、SQLite 高并发下你还要补的三个设置

如果你打算用 SQLite 支撑单机多 agent 并行，这三个设置很重要。

## 9.1 WAL 模式

启动时执行一次：

```sql
PRAGMA journal_mode=WAL;
```

原因：

* 读写并发更友好
* 避免大量读操作阻塞写事务

## 9.2 busy timeout

设置一个合理超时，比如 3~5 秒：

```sql
PRAGMA busy_timeout=5000;
```

原因：

* `BEGIN IMMEDIATE` 冲突时不至于立刻报 `database is locked`
* 给 orchestrator tick/lease keepalive 留一点重试空间

## 9.3 foreign_keys=ON

```sql
PRAGMA foreign_keys=ON;
```

至少可以避免 message/task/team 清理时留下太多脏引用。

---

# 十、调度层的高并发注意事项

## 10.1 不要跑多个 orchestrator 实例管理同一个 team

第一版最稳的策略是：

* 一个 `teamID` 只绑定一个 orchestrator worker
* 这会显著降低 claim 冲突复杂度

后面要多实例高可用，再做 leader election。

## 10.2 tick 间隔不要过短

我建议：

* `TickInterval = 1s`
* `LeaseDuration = 20s`
* keepalive 每 `LeaseDuration/3`

原因：

* 避免大量无意义的 claim/scan
* SQLite 单机模式下更稳

## 10.3 大批量 task 不要一次全 dispatch

虽然 `ListReadyTasks(limit=100)` 看起来方便，但真正派发时最好只按空闲 teammate 数量取前 N 个。否则每次 tick 都会做很多无意义 scoring。

也就是把：

```go
tasks, err := o.Repo.ListReadyTasks(ctx, teamID, 100)
```

优化成：

```go
tasks, err := o.Repo.ListReadyTasks(ctx, teamID, max(10, len(mates)*4))
```

---

# 十一、`ReadTaskSpec` 的测试建议

这几个测试很值。

## `TestReadTaskSpec_UsesRunMeta`

* ctx 中带 `RunMeta{TeamID, AgentID, CurrentTaskID}`
* tool 无需显式 task_id
* 能正确读回 task spec

## `TestReadTaskSpec_IncludesDependencies`

* task A 依赖 B、C
* `IncludeDependencies=true`
* 返回 B、C 的 title/status/summary/result_ref

## `TestReadTaskSpec_FailsWithoutRunMeta`

* 普通 session 调这个工具
* 返回 `ErrNoTaskRunMeta`

---

# 十二、强事务的测试建议

## `TestWithImmediateTx_ClaimTaskAndClaimsAtomic`

* 并发两个 goroutine 试图抢同一个 ready task
* 只有一个 claim 成功
* 只有一组 path claims 被写入

## `TestWithImmediateTx_InsertMailAndReceiptsAtomic`

* 广播消息 fanout 给多个收件人
* 故意在中间制造错误
* 要么 message+receipts 全成功，要么都回滚

## `TestWithImmediateTx_RequeueExpiredTasksStable`

* 运行中 task 已过期
* 同时另一个 goroutine 尝试 dispatch
* 最终状态不出现“双 assignee”或 claim 残留

---

# 十三、我建议你现在立刻补的两个 patch

## Patch A：给 `Repo` 增加 `WithImmediateTx`

然后把这四个调用点切过去：

* `dispatchOne`
* `RenewLease`
* `RequeueExpiredTasks`
* `InsertMail`

## Patch B：增加 `ReadTaskSpec`

并在 `BuildTeammatePrompt()` 补一句：

```text
If you are unsure about your exact task definition, dependency status, or allowed write paths, call ReadTaskSpec.
```

这两个 patch 做完，你的 Team 第一版稳定性会明显提升。

下一条我可以直接给你：

* `ReadTaskSpec` 的 prompt 使用范式
* `BuildTeammatePrompt()` 最终增强版
* 以及一个 `ReadTaskContext` 组合工具骨架
