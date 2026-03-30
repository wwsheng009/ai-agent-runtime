继续的话，最有价值的是把“**怎么落代码**”说透。

根据你上传的分析文档，当前最应该补齐的三块仍然是：`Agent Teams`、`Checkpoint/Rewind`、`Hook/审批/提问挂起恢复`。其中真正会牵一发而动全身的，不是 UI，也不是模型接入，而是这三个模块之间的**控制流闭环**：

```text
SessionActor
  -> Agent Loop
    -> Tool Broker
      -> Permission Engine
        -> Hook Engine
          -> Pause/Resume
            -> Checkpoint/Rewind
              -> Team Orchestrator
```

这轮我直接给你四个核心文件的**实现蓝图**：

1. `internal/team/repo.go`
2. `internal/team/orchestrator.go`
3. `internal/runtime/checkpoint/manager.go`
4. `internal/runtime/chat/actor.go`

核心目标不是“定义几个接口”，而是让你现有代码能**最小改动接进去**。

---

# 1. `internal/team/repo.go`

这个文件是 Teams 的真相源接口。不要让 orchestrator 直接到处写 SQL，也不要把 mailbox、task、lease、path claim 分散在多个包里。

## 1.1 Repo 接口

```go
package team

import (
    "context"
    "time"
)

type Repo interface {
    WithTx(ctx context.Context, fn func(TxRepo) error) error

    // Team
    CreateTeam(ctx context.Context, t Team) error
    GetTeam(ctx context.Context, teamID string) (*Team, error)
    UpdateTeamStatus(ctx context.Context, teamID string, status TeamStatus) error

    // Teammate
    UpsertTeammate(ctx context.Context, mate Teammate) error
    GetTeammate(ctx context.Context, teamID, teammateID string) (*Teammate, error)
    ListTeammates(ctx context.Context, teamID string) ([]Teammate, error)
    ListIdleTeammates(ctx context.Context, teamID string) ([]Teammate, error)
    UpdateTeammateState(ctx context.Context, teamID, teammateID string, state TeammateState) error
    HeartbeatTeammate(ctx context.Context, teamID, teammateID string, at time.Time) error

    // Task
    InsertTasks(ctx context.Context, tasks []Task, deps []TaskDependency) error
    GetTask(ctx context.Context, teamID, taskID string) (*Task, error)
    ListTasks(ctx context.Context, teamID string) ([]Task, error)
    ListReadyTasks(ctx context.Context, teamID string, limit int) ([]Task, error)
    ListRunningTasks(ctx context.Context, teamID string) ([]Task, error)
    ListBlockedTasks(ctx context.Context, teamID string) ([]Task, error)

    ClaimTask(ctx context.Context, teamID, taskID, assignee string, expectedVersion int64, leaseUntil time.Time) (bool, error)
    RenewLease(ctx context.Context, teamID, taskID string, leaseUntil time.Time) error
    CompleteTask(ctx context.Context, teamID, taskID string, summary string, resultRef *string) error
    FailTask(ctx context.Context, teamID, taskID string, reason string, retryable bool) error
    RequeueExpiredTasks(ctx context.Context, teamID string, now time.Time) ([]Task, error)
    UnblockReadyTasks(ctx context.Context, teamID string) ([]Task, error)

    // Dependencies
    AddDependencies(ctx context.Context, deps []TaskDependency) error
    ListDependents(ctx context.Context, teamID, taskID string) ([]Task, error)

    // Path claim
    ListPathClaims(ctx context.Context, teamID string) ([]PathClaim, error)
    AcquirePathClaims(ctx context.Context, claims []PathClaim) error
    ReleasePathClaimsByTask(ctx context.Context, teamID, taskID string) error
    DeleteExpiredPathClaims(ctx context.Context, teamID string, now time.Time) error

    // Mailbox
    InsertMail(ctx context.Context, msg MailMessage) error
    ListUnreadMail(ctx context.Context, teamID, agentID string, limit int) ([]MailMessage, error)
    AckMail(ctx context.Context, teamID, agentID string, msgIDs []string, at time.Time) error

    // Events / audit
    AppendEvent(ctx context.Context, evt TeamEvent) error
    ListEventsAfter(ctx context.Context, teamID string, afterSeq int64, limit int) ([]TeamEvent, error)
}
```

事务版接口单独定义：

```go
type TxRepo interface {
    Repo
}
```

这里不要设计成完全不同的方法集，保持一致即可，减少调用方心智负担。

---

## 1.2 最低限度的数据结构

```go
type TeamStatus string

const (
    TeamActive TeamStatus = "active"
    TeamPaused TeamStatus = "paused"
    TeamDone   TeamStatus = "done"
    TeamFailed TeamStatus = "failed"
)

type Team struct {
    ID            string
    WorkspaceID   string
    LeadSessionID string
    Status        TeamStatus
    Strategy      string
    MaxTeammates  int
    MaxWriters    int
    CreatedAt     time.Time
    UpdatedAt     time.Time
}

type TeammateState string

const (
    TeammateIdle    TeammateState = "idle"
    TeammateBusy    TeammateState = "busy"
    TeammateBlocked TeammateState = "blocked"
    TeammateOffline TeammateState = "offline"
)

type Teammate struct {
    ID            string
    TeamID        string
    Name          string
    Profile       string
    SessionID     string
    State         TeammateState
    LastHeartbeat time.Time
    Capabilities  []string
    Metadata      map[string]any
}

type TaskStatus string

const (
    TaskPending   TaskStatus = "pending"
    TaskReady     TaskStatus = "ready"
    TaskRunning   TaskStatus = "running"
    TaskBlocked   TaskStatus = "blocked"
    TaskDone      TaskStatus = "done"
    TaskFailed    TaskStatus = "failed"
    TaskCancelled TaskStatus = "cancelled"
)

type Task struct {
    ID           string
    TeamID       string
    ParentTaskID *string
    Title        string
    Goal         string
    Inputs       []string
    Status       TaskStatus
    Priority     int
    Assignee     *string
    LeaseUntil   *time.Time
    RetryCount   int
    ReadPaths    []string
    WritePaths   []string
    Deliverables []string
    Summary      string
    ResultRef    *string
    Version      int64
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type TaskDependency struct {
    TaskID          string
    DependsOnTaskID string
}

type ClaimMode string

const (
    ClaimRead  ClaimMode = "read"
    ClaimWrite ClaimMode = "write"
)

type PathClaim struct {
    ID           string
    TeamID       string
    TaskID       string
    OwnerAgentID string
    Path         string
    Mode         ClaimMode
    LeaseUntil   time.Time
}

type MailMessage struct {
    ID        string
    TeamID    string
    FromAgent string
    ToAgent   string
    TaskID    *string
    Kind      string
    Body      string
    Metadata  map[string]any
    CreatedAt time.Time
    AckedAt   *time.Time
}

type TeamEvent struct {
    TeamID   string
    Seq      int64
    Type     string
    Payload  []byte
    CreatedAt time.Time
}
```

---

## 1.3 Repo 层最关键的 3 个事务

### 事务 1：Claim task

这是整个 Teams 能不能稳的关键。

```go
func (r *SQLiteRepo) ClaimTask(
    ctx context.Context,
    teamID, taskID, assignee string,
    expectedVersion int64,
    leaseUntil time.Time,
) (bool, error) {
    tx, err := r.db.BeginTx(ctx, nil)
    if err != nil {
        return false, err
    }
    defer tx.Rollback()

    res, err := tx.ExecContext(ctx, `
        UPDATE team_tasks
        SET status='running',
            assignee=?,
            lease_until=?,
            version=version+1,
            updated_at=?
        WHERE team_id=?
          AND id=?
          AND status='ready'
          AND version=?`,
        assignee, leaseUntil, time.Now(), teamID, taskID, expectedVersion,
    )
    if err != nil {
        return false, err
    }

    n, _ := res.RowsAffected()
    if n == 0 {
        return false, nil
    }

    if _, err := tx.ExecContext(ctx, `
        UPDATE teammates
        SET state='busy', last_heartbeat=?
        WHERE team_id=? AND id=?`,
        time.Now(), teamID, assignee,
    ); err != nil {
        return false, err
    }

    return true, tx.Commit()
}
```

### 事务 2：Complete task

```go
func (r *SQLiteRepo) CompleteTask(
    ctx context.Context,
    teamID, taskID string,
    summary string,
    resultRef *string,
) error {
    return r.WithTx(ctx, func(tx TxRepo) error {
        task, err := tx.GetTask(ctx, teamID, taskID)
        if err != nil {
            return err
        }
        if task.Assignee == nil {
            return ErrNoAssignee
        }

        // 更新任务状态
        // 释放路径 claim
        // 把 teammate 置回 idle
        // 尝试解锁 dependents
        return nil
    })
}
```

### 事务 3：Requeue expired tasks

```go
func (r *SQLiteRepo) RequeueExpiredTasks(ctx context.Context, teamID string, now time.Time) ([]Task, error)
```

内部做：

* 找 `status='running' AND lease_until < now`
* `retry_count + 1`
* `status='ready'`
* `assignee = NULL`
* 释放 path claims
* 原 teammate 改 `offline` 或 `idle`
* 返回被回收的 task 列表，供 orchestrator 发 warning mail

---

## 1.4 索引

这个别拖到后面再补，不然后期会莫名变慢。

```sql
CREATE INDEX idx_team_tasks_ready
ON team_tasks(team_id, status, priority DESC, updated_at ASC);

CREATE INDEX idx_team_tasks_running
ON team_tasks(team_id, status, lease_until);

CREATE INDEX idx_team_deps_task
ON team_task_dependencies(task_id);

CREATE INDEX idx_team_deps_depends_on
ON team_task_dependencies(depends_on_task_id);

CREATE INDEX idx_team_claims_lookup
ON team_path_claims(team_id, lease_until);

CREATE INDEX idx_team_mail_unread
ON team_mailbox_messages(team_id, to_agent, acked_at, created_at);
```

---

# 2. `internal/team/orchestrator.go`

这个文件就是 Teams 的“控制平面”。不要让它管具体工具执行，它只负责：

* 分配任务
* 回收租约
* 汇总结果
* 触发重规划
* 结束团队

## 2.1 Orchestrator 结构

```go
package team

import (
    "context"
    "time"
)

type SessionFacade interface {
    SubmitPrompt(ctx context.Context, sessionID string, text string) error
    Subscribe(ctx context.Context, sessionID string) (<-chan SessionEvent, error)
}

type LeadPlanner interface {
    InitialPlan(ctx context.Context, team Team, goal string) ([]Task, []TaskDependency, error)
    ReplanOnFailure(ctx context.Context, team Team, failed Task) ([]Task, []TaskDependency, error)
    FinalSummary(ctx context.Context, team Team) (string, error)
}

type MailboxBuilder interface {
    BuildDigest(ctx context.Context, teamID, agentID string, maxItems int) (string, error)
}

type PathGuard interface {
    CanClaim(ctx context.Context, teamID string, readPaths, writePaths []string) (bool, []string, error)
    Acquire(ctx context.Context, teamID, taskID, owner string, readPaths, writePaths []string, leaseUntil time.Time) error
    ReleaseByTask(ctx context.Context, teamID, taskID string) error
}

type Orchestrator struct {
    Repo          Repo
    Sessions      SessionFacade
    Planner       LeadPlanner
    Mailbox       MailboxBuilder
    PathGuard     PathGuard
    LeaseDuration time.Duration
    TickInterval  time.Duration
    Now           func() time.Time
}
```

---

## 2.2 入口

```go
func (o *Orchestrator) Start(ctx context.Context, team Team, goal string) error {
    tasks, deps, err := o.Planner.InitialPlan(ctx, team, goal)
    if err != nil {
        return err
    }
    if err := o.Repo.InsertTasks(ctx, tasks, deps); err != nil {
        return err
    }
    return nil
}

func (o *Orchestrator) Run(ctx context.Context, teamID string) error {
    ticker := time.NewTicker(o.TickInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if err := o.tick(ctx, teamID); err != nil {
                return err
            }
        }
    }
}
```

---

## 2.3 tick 顺序不要乱

建议固定为：

```text
1. reclaimExpired
2. unblockReady
3. dispatch
4. collectCompleted
5. replanIfStuck
6. finalizeIfDone
```

```go
func (o *Orchestrator) tick(ctx context.Context, teamID string) error {
    if err := o.reclaimExpired(ctx, teamID); err != nil {
        return err
    }
    if err := o.unblockReady(ctx, teamID); err != nil {
        return err
    }
    if err := o.dispatch(ctx, teamID); err != nil {
        return err
    }
    if err := o.collectCompleted(ctx, teamID); err != nil {
        return err
    }
    if err := o.replanIfStuck(ctx, teamID); err != nil {
        return err
    }
    return o.finalizeIfDone(ctx, teamID)
}
```

---

## 2.4 dispatch 的真实逻辑

这一步不要简单“找个 ready task 就扔给第一个 idle teammate”。需要三层筛选：

### 第一层：状态

* teammate 必须 idle
* task 必须 ready

### 第二层：能力

* profile 与 task 类型匹配
* planner 不接写任务
* executor 不优先接纯搜索任务

### 第三层：路径冲突

* path guard 通过
* writer 数量不超过 team.MaxWriters

### 最小可用评分

```go
func scoreTaskForMate(m Teammate, t Task) int {
    score := t.Priority * 100

    switch m.Profile {
    case "explore":
        if len(t.WritePaths) == 0 {
            score += 30
        } else {
            score -= 1000
        }
    case "planner":
        if len(t.WritePaths) == 0 && len(t.Deliverables) == 0 {
            score += 20
        }
    case "executor":
        if len(t.WritePaths) > 0 {
            score += 40
        }
    }

    score -= t.RetryCount * 10
    return score
}
```

### dispatch 伪代码

```go
func (o *Orchestrator) dispatch(ctx context.Context, teamID string) error {
    mates, err := o.Repo.ListIdleTeammates(ctx, teamID)
    if err != nil {
        return err
    }
    tasks, err := o.Repo.ListReadyTasks(ctx, teamID, 100)
    if err != nil {
        return err
    }

    for _, mate := range mates {
        var chosen *Task
        best := -1 << 30

        for i := range tasks {
            t := &tasks[i]
            ok, _, err := o.PathGuard.CanClaim(ctx, teamID, t.ReadPaths, t.WritePaths)
            if err != nil {
                return err
            }
            if !ok {
                continue
            }

            s := scoreTaskForMate(mate, *t)
            if s > best {
                best = s
                chosen = t
            }
        }

        if chosen == nil {
            continue
        }

        if err := o.dispatchOne(ctx, teamID, mate, *chosen); err != nil {
            return err
        }
    }

    return nil
}
```

---

## 2.5 dispatchOne

```go
func (o *Orchestrator) dispatchOne(ctx context.Context, teamID string, mate Teammate, task Task) error {
    leaseUntil := o.Now().Add(o.LeaseDuration)

    claimed, err := o.Repo.ClaimTask(ctx, teamID, task.ID, mate.ID, task.Version, leaseUntil)
    if err != nil {
        return err
    }
    if !claimed {
        return nil
    }

    if err := o.PathGuard.Acquire(ctx, teamID, task.ID, mate.ID, task.ReadPaths, task.WritePaths, leaseUntil); err != nil {
        _ = o.Repo.FailTask(ctx, teamID, task.ID, "failed to acquire path claims", true)
        return err
    }

    digest, err := o.Mailbox.BuildDigest(ctx, teamID, mate.ID, 10)
    if err != nil {
        return err
    }

    prompt := BuildTeammatePrompt(mate, task, digest)

    if err := o.Sessions.SubmitPrompt(ctx, mate.SessionID, prompt); err != nil {
        _ = o.PathGuard.ReleaseByTask(ctx, teamID, task.ID)
        _ = o.Repo.FailTask(ctx, teamID, task.ID, err.Error(), true)
        return err
    }

    return nil
}
```

---

## 2.6 collectCompleted

不要靠轮询 session 状态文本。让 teammate session 在完成时发结构化 event，比如：

```go
type SessionEvent struct {
    SessionID string
    Type      string
    Payload   []byte
}
```

任务侧至少约定三种：

* `task_completed`
* `task_blocked`
* `task_failed`

### 完成时的处理

```go
func (o *Orchestrator) handleTaskCompleted(
    ctx context.Context,
    teamID string,
    taskID string,
    summary string,
    resultRef *string,
) error {
    if err := o.Repo.CompleteTask(ctx, teamID, taskID, summary, resultRef); err != nil {
        return err
    }
    return o.Repo.AppendEvent(ctx, TeamEvent{
        TeamID: teamID,
        Type:   "task_completed",
        // payload...
    })
}
```

---

## 2.7 replanIfStuck

这个非常关键，不然 DAG 稍微复杂一点就死住。

触发条件：

* 没有 ready task
* 没有 running teammate
* 仍然存在 blocked / failed task
* team 还不是 done

```go
func (o *Orchestrator) replanIfStuck(ctx context.Context, teamID string) error {
    tasks, err := o.Repo.ListTasks(ctx, teamID)
    if err != nil {
        return err
    }

    var ready, running, blocked, failed int
    for _, t := range tasks {
        switch t.Status {
        case TaskReady:
            ready++
        case TaskRunning:
            running++
        case TaskBlocked:
            blocked++
        case TaskFailed:
            failed++
        }
    }

    if ready > 0 || running > 0 {
        return nil
    }
    if blocked == 0 && failed == 0 {
        return nil
    }

    team, err := o.Repo.GetTeam(ctx, teamID)
    if err != nil {
        return err
    }

    // 最简单第一版：只挑一个 failed task 让 lead 重规划
    for _, t := range tasks {
        if t.Status == TaskFailed {
            newTasks, deps, err := o.Planner.ReplanOnFailure(ctx, *team, t)
            if err != nil {
                return err
            }
            return o.Repo.InsertTasks(ctx, newTasks, deps)
        }
    }

    return nil
}
```

---

# 3. `internal/runtime/checkpoint/manager.go`

这个文件要解决的是：**怎么真正恢复**，而不是“怎么保存一个 checkpoint 记录”。

关键设计原则只有一句：

**不要直接存“恢复后的全工作区快照”，而是存每次 mutation 的 before/after，再用反向回放恢复。**

这样最贴合你现有结构，改造最小。

## 3.1 Manager 结构

```go
package checkpoint

import (
    "context"
    "time"
)

type Repo interface {
    CreateCheckpoint(ctx context.Context, cp Checkpoint) error
    AddCheckpointFiles(ctx context.Context, files []CheckpointFile) error
    GetCheckpoint(ctx context.Context, sessionID, checkpointID string) (*Checkpoint, error)
    ListCheckpointsAfter(ctx context.Context, sessionID, checkpointID string) ([]Checkpoint, error)
    ListCheckpointFiles(ctx context.Context, checkpointID string) ([]CheckpointFile, error)

    SaveBlob(ctx context.Context, blob Blob) (string, error)
    LoadBlob(ctx context.Context, blobID string) ([]byte, error)

    GetConversationHead(ctx context.Context, sessionID string) (int64, error)
    SetConversationHead(ctx context.Context, sessionID string, visibleUntilSeq int64) error
    InvalidateSummariesAfter(ctx context.Context, sessionID string, seq int64) error
}

type Workspace interface {
    ReadFile(path string) ([]byte, error)
    WriteFile(path string, data []byte) error
    RemoveFile(path string) error
    Exists(path string) (bool, error)
}

type Manager struct {
    Repo      Repo
    Workspace Workspace
    Diff      DiffEngine
    Now       func() time.Time
}

type DiffEngine interface {
    Unified(path string, before, after []byte) (string, error)
}
```

---

## 3.2 捕获句柄

为了不把 before/after 信息散到各层，定义一个 handle。

```go
type CaptureHandle struct {
    CheckpointID string
    SessionID    string
    TurnID       string
    Paths        []string
    Before       map[string]*FileSnapshot
    VisibleSeq   int64
}

type FileSnapshot struct {
    Exists bool
    BlobID *string
    Hash   *string
}
```

---

## 3.3 BeforeMutation

所有 mutating tools 一律先走这里。

```go
func (m *Manager) BeforeMutation(
    ctx context.Context,
    sessionID, turnID string,
    paths []string,
    visibleSeq int64,
) (*CaptureHandle, error) {
    handle := &CaptureHandle{
        CheckpointID: NewID(),
        SessionID:    sessionID,
        TurnID:       turnID,
        Paths:        paths,
        Before:       map[string]*FileSnapshot{},
        VisibleSeq:   visibleSeq,
    }

    cp := Checkpoint{
        ID:               handle.CheckpointID,
        SessionID:        sessionID,
        TurnID:           turnID,
        Mode:             "code",
        VisibleUntilSeq:  visibleSeq,
        CreatedAt:        m.Now(),
    }
    if err := m.Repo.CreateCheckpoint(ctx, cp); err != nil {
        return nil, err
    }

    for _, p := range paths {
        exists, err := m.Workspace.Exists(p)
        if err != nil {
            return nil, err
        }
        snap := &FileSnapshot{Exists: exists}
        if exists {
            b, err := m.Workspace.ReadFile(p)
            if err != nil {
                return nil, err
            }
            blobID, err := m.Repo.SaveBlob(ctx, BlobFromBytes(b))
            if err != nil {
                return nil, err
            }
            h := HashBytes(b)
            snap.BlobID = &blobID
            snap.Hash = &h
        }
        handle.Before[p] = snap
    }

    return handle, nil
}
```

---

## 3.4 AfterMutation

```go
func (m *Manager) AfterMutation(ctx context.Context, h *CaptureHandle) error {
    var rows []CheckpointFile

    for _, p := range h.Paths {
        before := h.Before[p]

        exists, err := m.Workspace.Exists(p)
        if err != nil {
            return err
        }

        var afterBlobID *string
        var afterHash *string
        var afterData []byte

        if exists {
            afterData, err = m.Workspace.ReadFile(p)
            if err != nil {
                return err
            }
            blobID, err := m.Repo.SaveBlob(ctx, BlobFromBytes(afterData))
            if err != nil {
                return err
            }
            hash := HashBytes(afterData)
            afterBlobID = &blobID
            afterHash = &hash
        }

        op := detectOp(before.Exists, exists)

        var beforeData []byte
        if before.BlobID != nil {
            beforeData, err = m.Repo.LoadBlob(ctx, *before.BlobID)
            if err != nil {
                return err
            }
        }

        diffText, err := m.Diff.Unified(p, beforeData, afterData)
        if err != nil {
            return err
        }

        rows = append(rows, CheckpointFile{
            ID:           NewID(),
            CheckpointID: h.CheckpointID,
            Path:         p,
            Op:           op,
            BeforeBlobID: before.BlobID,
            AfterBlobID:  afterBlobID,
            BeforeHash:   before.Hash,
            AfterHash:    afterHash,
            DiffText:     []byte(diffText),
        })
    }

    return m.Repo.AddCheckpointFiles(ctx, rows)
}
```

---

## 3.5 Restore 的正确做法：反向回放

恢复到目标 checkpoint 时，不是“直接把目标 checkpoint 写回去”，而是：

* 找到从目标 checkpoint 之后到当前 head 的所有 checkpoint
* 按时间逆序遍历
* 对每个文件应用 `before_blob`

这就相当于撤销目标之后所有 mutation。

```go
func (m *Manager) RestoreCode(ctx context.Context, sessionID, checkpointID string) error {
    cps, err := m.Repo.ListCheckpointsAfter(ctx, sessionID, checkpointID)
    if err != nil {
        return err
    }

    // 逆序撤销
    for i := len(cps) - 1; i >= 0; i-- {
        files, err := m.Repo.ListCheckpointFiles(ctx, cps[i].ID)
        if err != nil {
            return err
        }

        for _, f := range files {
            switch f.Op {
            case "create":
                // 该文件是在后续 checkpoint 创建的，回退时删除
                if err := m.Workspace.RemoveFile(f.Path); err != nil {
                    return err
                }
            case "update", "delete":
                if f.BeforeBlobID == nil {
                    if err := m.Workspace.RemoveFile(f.Path); err != nil {
                        return err
                    }
                    continue
                }
                b, err := m.Repo.LoadBlob(ctx, *f.BeforeBlobID)
                if err != nil {
                    return err
                }
                if err := m.Workspace.WriteFile(f.Path, b); err != nil {
                    return err
                }
            }
        }
    }

    return nil
}
```

这个方案第一版就够用。等后面 checkpoint 数量很多，再加“每 N 次 mutation 做一个 anchor snapshot”。

---

## 3.6 Conversation rewind

这一块不要删 transcript，只维护“可见头”。

```go
func (m *Manager) RestoreConversation(ctx context.Context, sessionID, checkpointID string) error {
    cp, err := m.Repo.GetCheckpoint(ctx, sessionID, checkpointID)
    if err != nil {
        return err
    }

    if err := m.Repo.SetConversationHead(ctx, sessionID, cp.VisibleUntilSeq); err != nil {
        return err
    }
    return m.Repo.InvalidateSummariesAfter(ctx, sessionID, cp.VisibleUntilSeq)
}
```

`both` 模式就是：

```go
func (m *Manager) Restore(ctx context.Context, sessionID, checkpointID string, mode string) error {
    switch mode {
    case "code":
        return m.RestoreCode(ctx, sessionID, checkpointID)
    case "conversation":
        return m.RestoreConversation(ctx, sessionID, checkpointID)
    case "both":
        if err := m.RestoreCode(ctx, sessionID, checkpointID); err != nil {
            return err
        }
        return m.RestoreConversation(ctx, sessionID, checkpointID)
    default:
        return ErrInvalidMode
    }
}
```

---

## 3.7 PreviewRestore

这个要和 `RestoreCode` 走同一套逻辑，只是不真正写文件，而是生成 preview。

```go
type PreviewFile struct {
    Path     string
    Action   string
    DiffText string
}

type RestorePreview struct {
    CheckpointID string
    Mode         string
    Files        []PreviewFile
}
```

---

# 4. `internal/runtime/chat/actor.go`

这是你现在最需要的“运行时胶水层”。关键不是再造一个会话系统，而是给现有 session 管理加上：

* 命令驱动
* 暂停恢复
* 事件订阅
* 与 agent loop 的桥接

## 4.1 最重要的设计决策

**不要试图序列化整个 ReAct 调用栈。**

审批暂停和 `AskUserQuestion` 暂停，完全可以通过“**保存 pending tool call，然后把工具结果补写回 transcript，再重新进入 loop**”来实现。

这比保存堆栈安全得多。

---

## 4.2 Actor 结构

```go
package chat

import (
    "context"
    "encoding/json"
    "time"
)

type SessionStatus string

const (
    SessionIdle            SessionStatus = "idle"
    SessionRunning         SessionStatus = "running"
    SessionWaitingApproval SessionStatus = "waiting_approval"
    SessionWaitingInput    SessionStatus = "waiting_input"
    SessionRewinding       SessionStatus = "rewinding"
    SessionStopped         SessionStatus = "stopped"
)

type Actor struct {
    ID         string
    CmdCh      chan Command
    Hub        *EventHub
    StateStore RuntimeStateStore
    Sessions   Storage
    Runner     TurnRunner
    Tools      ToolExecutor
    Now        func() time.Time
}

type RuntimeState struct {
    SessionID       string
    Status          SessionStatus
    PendingTool     *PendingToolInvocation
    PendingApproval *ApprovalRequest
    PendingQuestion *UserQuestionRequest
    VisibleUntilSeq int64
    UpdatedAt       time.Time
}
```

---

## 4.3 Pending 对象

```go
type PendingToolInvocation struct {
    ToolCallID   string
    ToolName     string
    ArgsJSON     json.RawMessage
    AssistantMsgID string
}

type ApprovalRequest struct {
    ID         string
    SessionID  string
    ToolName   string
    ToolCallID string
    ArgsJSON   json.RawMessage
    Reason     string
    RiskLevel  string
    CreatedAt  time.Time
    ExpiresAt  time.Time
}

type UserQuestionRequest struct {
    ID         string
    SessionID  string
    ToolCallID string
    Prompt     string
    Suggestions []string
    Required   bool
    CreatedAt  time.Time
}
```

---

## 4.4 命令

```go
type Command interface{ isCommand() }

type SubmitPrompt struct {
    Text string
}

type ApproveTool struct {
    RequestID string
    Allow     bool
    PatchedArgs json.RawMessage
}

type AnswerQuestion struct {
    QuestionID string
    Answer     string
}

type RewindTo struct {
    CheckpointID string
    Mode         string
}

type Interrupt struct{}
```

---

## 4.5 事件

```go
type EventType string

const (
    EventAssistantDelta    EventType = "assistant_delta"
    EventToolStarted       EventType = "tool_started"
    EventToolFinished      EventType = "tool_finished"
    EventApprovalRequested EventType = "approval_requested"
    EventQuestionAsked     EventType = "question_asked"
    EventCheckpointCreated EventType = "checkpoint_created"
    EventRewindStarted     EventType = "rewind_started"
    EventRewindFinished    EventType = "rewind_finished"
    EventTurnCompleted     EventType = "turn_completed"
)

type Event struct {
    SessionID string
    Seq       int64
    Type      EventType
    Payload   []byte
    At        time.Time
}
```

---

## 4.6 Actor 主循环

```go
func (a *Actor) Run(ctx context.Context) error {
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case cmd := <-a.CmdCh:
            if err := a.handle(ctx, cmd); err != nil {
                return err
            }
        }
    }
}
```

---

## 4.7 真正关键：如何暂停恢复

### 提交 prompt

```go
func (a *Actor) handleSubmit(ctx context.Context, cmd SubmitPrompt) error {
    state, err := a.StateStore.Load(ctx, a.ID)
    if err != nil {
        return err
    }
    if state.Status != SessionIdle {
        return ErrSessionBusy
    }

    state.Status = SessionRunning
    if err := a.StateStore.Save(ctx, state); err != nil {
        return err
    }

    return a.runTurn(ctx, cmd.Text)
}
```

### runTurn 的桥接思想

`Runner.Run()` 不直接执行工具，而是遇到工具调用时通过 callback 回到 actor：

```go
type TurnRunner interface {
    Run(ctx context.Context, sessionID string, input string, cb TurnCallbacks) error
}

type TurnCallbacks struct {
    OnAssistantDelta func(text string) error
    OnToolCall       func(call ToolCall) error
    OnDone           func() error
}
```

### OnToolCall 内部流程

1. 先把 assistant 的 tool call 写入 transcript
2. 再做权限检查
3. 如果需要审批，保存 `PendingToolInvocation` + `ApprovalRequest`
4. 把 session 状态置 `waiting_approval`
5. 发事件
6. **直接结束当前 turn，不报错，不丢上下文**

这一步最关键。

```go
func (a *Actor) onToolCall(ctx context.Context, call ToolCall) error {
    decision, err := a.Tools.Authorize(ctx, call)
    if err != nil {
        return err
    }

    switch decision.Type {
    case DecisionAllow:
        result, err := a.Tools.Execute(ctx, call)
        if err != nil {
            return err
        }
        return a.Tools.AppendToolResult(ctx, a.ID, call.ToolCallID, result)

    case DecisionAsk:
        state, err := a.StateStore.Load(ctx, a.ID)
        if err != nil {
            return err
        }
        state.Status = SessionWaitingApproval
        state.PendingTool = &PendingToolInvocation{
            ToolCallID: call.ToolCallID,
            ToolName:   call.ToolName,
            ArgsJSON:   call.ArgsJSON,
        }
        state.PendingApproval = &ApprovalRequest{
            ID:         NewID(),
            SessionID:  a.ID,
            ToolName:   call.ToolName,
            ToolCallID: call.ToolCallID,
            ArgsJSON:   call.ArgsJSON,
            Reason:     decision.Reason,
            CreatedAt:  a.Now(),
            ExpiresAt:  a.Now().Add(30 * time.Minute),
        }
        if err := a.StateStore.Save(ctx, state); err != nil {
            return err
        }

        return a.Hub.Publish(ctx, Event{
            SessionID: a.ID,
            Type:      EventApprovalRequested,
            Payload:   MustJSON(state.PendingApproval),
            At:        a.Now(),
        })

    default:
        return ErrDenied
    }
}
```

### 这里为什么能暂停？

因为 assistant 的 tool call 已经在 transcript 里了。恢复时你只需要：

* 执行挂起的工具，或者
* 合成一个工具结果

然后把这个工具结果写回 transcript，再重新跑 loop。

不需要恢复 Go 调用栈。

---

## 4.8 ApproveTool 恢复

```go
func (a *Actor) handleApprove(ctx context.Context, cmd ApproveTool) error {
    state, err := a.StateStore.Load(ctx, a.ID)
    if err != nil {
        return err
    }
    if state.Status != SessionWaitingApproval || state.PendingTool == nil || state.PendingApproval == nil {
        return ErrNoPendingApproval
    }
    if state.PendingApproval.ID != cmd.RequestID {
        return ErrApprovalMismatch
    }

    if !cmd.Allow {
        result := ToolResult{
            Success: false,
            Output:  "user denied tool execution",
        }
        if err := a.Tools.AppendToolResult(ctx, a.ID, state.PendingTool.ToolCallID, result); err != nil {
            return err
        }
    } else {
        args := state.PendingTool.ArgsJSON
        if len(cmd.PatchedArgs) > 0 {
            args = cmd.PatchedArgs
        }
        result, err := a.Tools.Execute(ctx, ToolCall{
            ToolCallID: state.PendingTool.ToolCallID,
            ToolName:   state.PendingTool.ToolName,
            ArgsJSON:   args,
        })
        if err != nil {
            return err
        }
        if err := a.Tools.AppendToolResult(ctx, a.ID, state.PendingTool.ToolCallID, result); err != nil {
            return err
        }
    }

    state.Status = SessionIdle
    state.PendingTool = nil
    state.PendingApproval = nil
    if err := a.StateStore.Save(ctx, state); err != nil {
        return err
    }

    return a.runTurn(ctx, "")
}
```

这里的 `runTurn(ctx, "")` 代表“不追加新的用户输入，只让 loop 从当前 transcript 继续”。

---

## 4.9 AskUserQuestion 恢复

`AskUserQuestion` 不应该直接跳出到 UI 层，而是也走 pending tool 模型。

```go
func (a *Actor) pauseForQuestion(ctx context.Context, call ToolCall, q UserQuestionRequest) error {
    state, err := a.StateStore.Load(ctx, a.ID)
    if err != nil {
        return err
    }

    state.Status = SessionWaitingInput
    state.PendingTool = &PendingToolInvocation{
        ToolCallID: call.ToolCallID,
        ToolName:   call.ToolName,
        ArgsJSON:   call.ArgsJSON,
    }
    state.PendingQuestion = &q

    if err := a.StateStore.Save(ctx, state); err != nil {
        return err
    }

    return a.Hub.Publish(ctx, Event{
        SessionID: a.ID,
        Type:      EventQuestionAsked,
        Payload:   MustJSON(q),
        At:        a.Now(),
    })
}
```

用户答复后：

```go
func (a *Actor) handleAnswer(ctx context.Context, cmd AnswerQuestion) error {
    state, err := a.StateStore.Load(ctx, a.ID)
    if err != nil {
        return err
    }
    if state.Status != SessionWaitingInput || state.PendingQuestion == nil || state.PendingTool == nil {
        return ErrNoPendingQuestion
    }
    if state.PendingQuestion.ID != cmd.QuestionID {
        return ErrQuestionMismatch
    }

    result := ToolResult{
        Success: true,
        Output:  cmd.Answer,
    }
    if err := a.Tools.AppendToolResult(ctx, a.ID, state.PendingTool.ToolCallID, result); err != nil {
        return err
    }

    state.Status = SessionIdle
    state.PendingTool = nil
    state.PendingQuestion = nil
    if err := a.StateStore.Save(ctx, state); err != nil {
        return err
    }

    return a.runTurn(ctx, "")
}
```

---

# 5. Hook / Permission / Actor 的接线顺序

这里最容易写乱，所以我直接给一个固定顺序。

## 5.1 一次 tool call 的完整链路

```text
model emits tool_call
 -> actor.onToolCall
   -> hook.pre_tool_use
   -> permission rules
   -> permission mode
   -> canUseTool callback
   -> if ask:
        hook.permission_request
        actor.pause(waiting_approval)
      else if allow:
        checkpoint.before_mutation (if mutating)
        tool.execute
        checkpoint.after_mutation
        hook.post_tool_use
        append tool result
```

这个顺序尽量不要变。

---

## 5.2 HookManager 接口

```go
type HookManager interface {
    DispatchSync(ctx context.Context, event string, payload any) (HookDecision, error)
    DispatchAsync(ctx context.Context, event string, payload any)
}

type HookDecision struct {
    Action         string
    Message        string
    PatchedPayload []byte
    ExtraContext   map[string]string
}
```

### 同步事件

* `pre_tool_use`
* `permission_request`

### 异步事件

* `post_tool_use`
* `session_start`
* `subagent_stop`
* `checkpoint_created`
* `rewind_completed`

---

# 6. 这四个文件的依赖顺序

你不要四个一起写。最省返工的顺序是：

## 第一步：`actor.go`

先把暂停恢复打通。没有它，审批、提问、后台任务都没法稳定挂进去。

## 第二步：`checkpoint/manager.go`

然后把 mutating tool 全部挂上 auto checkpoint，先支持 `code` 恢复。

## 第三步：`team/repo.go`

把 task、lease、mailbox、path claim 的表和 repo 定稳。

## 第四步：`team/orchestrator.go`

最后做调度和 teammate 协作。

---

# 7. 每个文件写完后的验收点

## `actor.go`

* 同一 session 不能并发跑两个 turn
* tool ask 可以暂停
* 用户批准后能继续
* `AskUserQuestion` 能暂停并继续
* 重启后 pending approval / pending question 不丢

## `checkpoint/manager.go`

* `Write/Edit/MultiEdit` 自动建 checkpoint
* 能 preview 恢复内容
* `code` 恢复可用
* `conversation` 恢复通过 `visible_until_seq` 生效

## `team/repo.go`

* 多 goroutine claim 同一 task 只有一个成功
* 过期 lease 可被回收
* unread mailbox 可分页
* path claim 可释放

## `team/orchestrator.go`

* 至少两个 teammate 可并发处理两个 ready task
* 写路径冲突会被阻止
* blocked/failed 场景能触发 replan
* 所有 task done 后 lead 能汇总结果

---

# 8. 你现在最应该直接创建的文件

如果你准备立刻开工，我建议先落这几个空文件和接口：

```text
internal/runtime/chat/actor.go
internal/runtime/chat/runtime_state_store.go
internal/runtime/checkpoint/manager.go
internal/runtime/checkpoint/types.go
internal/team/repo.go
internal/team/sqlite_repo.go
internal/team/orchestrator.go
internal/team/path_guard.go
```

然后第一轮只把接口和空实现跑通，确保能编译；第二轮再接现有 `loop.go`、`toolkit/registry.go`、`contextmgr/manager.go`。

---

下一条我可以直接把这四个文件写成“接近可编译”的 Go 代码骨架。
