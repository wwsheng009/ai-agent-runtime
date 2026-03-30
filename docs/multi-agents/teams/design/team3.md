继续的话，我建议直接进入“**按模块给出可编码的实现蓝图**”。这次我不再停留在分层说明，而是把每个未完成功能拆成：

1. 要新增的包和文件
2. 核心接口
3. 数据表
4. 状态机
5. 关键事务流程
6. 伪代码骨架
7. 验收标准

先说明一个校正：根据你上传的实现分析，`Session Actor` 不是从零开始，而是**已有 90% 的会话管理基础**，真正缺的是“actor 化、审批暂停点、事件订阅”。所以后面的方案应该是**在现有 `internal/runtime/chat/` 上增量改造**，而不是另起炉灶。

---

# 一、先把最高优先级讲透：Agent Teams

你现在最大的空白是 `Agent Teams = 0%`。这个模块不能塞进现有 `SubagentScheduler` 里硬改，正确做法是：

* `SubagentScheduler` 继续负责**单会话内的短生命周期委派**
* 新增 `internal/team/` 负责**持久化协作编排**
* 两者的关系是：**Team 用 Session 跑任务，Subagent 可作为 Teammate 内部的局部加速器**

## 1.1 包结构

建议直接建这一套：

```text
internal/team/
  types.go
  orchestrator.go
  scheduler.go
  repo.go
  sqlite_repo.go
  task_graph.go
  lease.go
  mailbox.go
  teammate_runner.go
  path_claims.go
  lead_planner.go
  summarizer.go
  events.go
  api.go
```

### 每个文件职责

* `types.go`: Team / Teammate / Task / MailMessage / PathClaim
* `repo.go`: 存储接口
* `sqlite_repo.go`: SQLite 实现
* `task_graph.go`: 依赖关系、ready/unblock 判定
* `lease.go`: claim / renew / reclaim
* `mailbox.go`: 直接消息、广播、摘要
* `path_claims.go`: 文件读写冲突控制
* `teammate_runner.go`: 用 session 驱动 teammate 执行
* `lead_planner.go`: lead 拆任务、合并结果
* `orchestrator.go`: 主控制循环
* `events.go`: Team 级别事件定义
* `api.go`: 给 CLI/IDE/Web 暴露操作入口

---

## 1.2 核心数据结构

### Team

```go
type TeamStatus string

const (
    TeamActive  TeamStatus = "active"
    TeamPaused  TeamStatus = "paused"
    TeamDone    TeamStatus = "done"
    TeamFailed  TeamStatus = "failed"
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
```

### Teammate

```go
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
```

### Task

```go
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
```

### MailMessage

```go
type MessageKind string

const (
    MsgInfo      MessageKind = "info"
    MsgQuestion  MessageKind = "question"
    MsgChallenge MessageKind = "challenge"
    MsgHandoff   MessageKind = "handoff"
    MsgDone      MessageKind = "done"
    MsgWarning   MessageKind = "warning"
)

type MailMessage struct {
    ID         string
    TeamID     string
    FromAgent  string
    ToAgent    string // "*" 代表广播
    TaskID     *string
    Kind       MessageKind
    Body       string
    Metadata   map[string]any
    CreatedAt  time.Time
    AckedAt    *time.Time
}
```

### PathClaim

```go
type ClaimMode string

const (
    ClaimRead  ClaimMode = "read"
    ClaimWrite ClaimMode = "write"
)

type PathClaim struct {
    ID          string
    TeamID       string
    TaskID       string
    OwnerAgentID string
    Path         string
    Mode         ClaimMode
    LeaseUntil   time.Time
}
```

---

## 1.3 SQLite 表

### teams

```sql
CREATE TABLE teams (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  lead_session_id TEXT NOT NULL,
  status TEXT NOT NULL,
  strategy TEXT NOT NULL,
  max_teammates INTEGER NOT NULL,
  max_writers INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL
);
```

### teammates

```sql
CREATE TABLE teammates (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  name TEXT NOT NULL,
  profile TEXT NOT NULL,
  session_id TEXT NOT NULL,
  state TEXT NOT NULL,
  last_heartbeat DATETIME NOT NULL,
  capabilities_json BLOB NOT NULL,
  metadata_json BLOB,
  FOREIGN KEY(team_id) REFERENCES teams(id)
);
```

### team_tasks

```sql
CREATE TABLE team_tasks (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  parent_task_id TEXT,
  title TEXT NOT NULL,
  goal TEXT NOT NULL,
  inputs_json BLOB,
  status TEXT NOT NULL,
  priority INTEGER NOT NULL DEFAULT 0,
  assignee TEXT,
  lease_until DATETIME,
  retry_count INTEGER NOT NULL DEFAULT 0,
  read_paths_json BLOB,
  write_paths_json BLOB,
  deliverables_json BLOB,
  summary TEXT,
  result_ref TEXT,
  version INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL,
  updated_at DATETIME NOT NULL,
  FOREIGN KEY(team_id) REFERENCES teams(id)
);
```

### team_task_dependencies

```sql
CREATE TABLE team_task_dependencies (
  task_id TEXT NOT NULL,
  depends_on_task_id TEXT NOT NULL,
  PRIMARY KEY(task_id, depends_on_task_id)
);
```

### team_mailbox_messages

```sql
CREATE TABLE team_mailbox_messages (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  from_agent TEXT NOT NULL,
  to_agent TEXT NOT NULL,
  task_id TEXT,
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  metadata_json BLOB,
  created_at DATETIME NOT NULL,
  acked_at DATETIME
);
```

### team_path_claims

```sql
CREATE TABLE team_path_claims (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  path TEXT NOT NULL,
  mode TEXT NOT NULL,
  lease_until DATETIME NOT NULL
);
```

### team_events

```sql
CREATE TABLE team_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  team_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  payload_json BLOB NOT NULL,
  created_at DATETIME NOT NULL,
  UNIQUE(team_id, seq)
);
```

---

## 1.4 TeamOrchestrator 主循环

这是整个 Teams 的心脏。

```go
type Orchestrator struct {
    Repo          Repo
    TeamRunner    *TeammateRunner
    LeadPlanner   *LeadPlanner
    Claims        *PathClaimManager
    Mailbox       *MailboxService
    Events        *TeamEventBus
    Clock         func() time.Time
    LeaseDuration time.Duration
}

func (o *Orchestrator) Run(ctx context.Context, teamID string) error {
    ticker := time.NewTicker(1 * time.Second)
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

### `tick()` 逻辑

1. 处理过期租约
2. 把依赖已满足的 task 从 `pending/blocked` 变为 `ready`
3. 给空闲 teammate 分配可执行 task
4. 收集已完成 task，解锁后续依赖
5. 如所有 task 完成，触发 lead 汇总
6. 如全部失败或不可推进，标记 team failed

```go
func (o *Orchestrator) tick(ctx context.Context, teamID string) error {
    if err := o.reclaimExpiredLeases(ctx, teamID); err != nil {
        return err
    }
    if err := o.unblockReadyTasks(ctx, teamID); err != nil {
        return err
    }
    if err := o.dispatchTasks(ctx, teamID); err != nil {
        return err
    }
    if err := o.collectCompletions(ctx, teamID); err != nil {
        return err
    }
    return o.checkTerminalState(ctx, teamID)
}
```

---

## 1.5 Claim / Lease 机制

这个必须走数据库乐观并发，不能靠内存锁。

### claim 流程

```text
选择 ready task
 -> 检查路径冲突
 -> UPDATE team_tasks WHERE status='ready' AND version=?
 -> 成功后写入 team_path_claims
 -> teammate runner 启动
```

### 事务伪代码

```go
func (r *SQLiteRepo) ClaimTask(
    ctx context.Context,
    taskID string,
    assignee string,
    leaseUntil time.Time,
    expectedVersion int64,
) (bool, error) {
    tx, err := r.db.BeginTx(ctx, nil)
    if err != nil { return false, err }
    defer tx.Rollback()

    res, err := tx.ExecContext(ctx, `
        UPDATE team_tasks
        SET status='running',
            assignee=?,
            lease_until=?,
            version=version+1,
            updated_at=?
        WHERE id=?
          AND status='ready'
          AND version=?`,
        assignee, leaseUntil, time.Now(), taskID, expectedVersion,
    )
    if err != nil { return false, err }

    n, _ := res.RowsAffected()
    if n == 0 {
        return false, nil
    }

    return true, tx.Commit()
}
```

### 租约续期

teammate 每 5 秒发 heartbeat，续租 20 秒。

```go
func (o *Orchestrator) RenewLease(ctx context.Context, taskID string, until time.Time) error
```

### 租约回收

超过 `lease_until` 且无 heartbeat：

* task 变回 `ready`
* assignee 置空
* 释放 path claims
* retry_count + 1
* 发送 warning mailbox 给 lead

---

## 1.6 文件冲突保护

这个模块对 Teams 成败影响非常大。

### 冲突规则

* read/read：允许
* read/write：冲突
* write/write：冲突
* 目录前缀重叠也算冲突
* `pkg/` 写锁覆盖 `pkg/a.go`

### 接口

```go
type PathClaimManager struct {
    Repo Repo
}

func (m *PathClaimManager) CanClaim(
    ctx context.Context,
    teamID string,
    readPaths []string,
    writePaths []string,
) (bool, []string, error)

func (m *PathClaimManager) Acquire(
    ctx context.Context,
    teamID, taskID, owner string,
    readPaths, writePaths []string,
    leaseUntil time.Time,
) error

func (m *PathClaimManager) ReleaseByTask(ctx context.Context, taskID string) error
```

### 路径标准化

必须统一：

```go
func NormalizePath(root, p string) string {
    p = filepath.Clean(p)
    if !filepath.IsAbs(p) {
        p = filepath.Join(root, p)
    }
    return filepath.Clean(p)
}
```

### 前缀冲突判断

```go
func pathOverlap(a, b string) bool {
    a = filepath.Clean(a)
    b = filepath.Clean(b)
    return a == b ||
        strings.HasPrefix(a, b+string(os.PathSeparator)) ||
        strings.HasPrefix(b, a+string(os.PathSeparator))
}
```

---

## 1.7 Mailbox 机制

Mailbox 不应该直接进入普通会话 transcript，否则上下文会炸。

### 正确做法

* 原始消息存 `team_mailbox_messages`
* 每次 teammate 开始一个 task 前，只拉取“未读摘要”
* 摘要长度受预算控制
* 必要时 agent 可显式调用 `ReadMailboxMessage`

### 服务接口

```go
type MailboxService struct {
    Repo Repo
}

func (m *MailboxService) Send(ctx context.Context, msg MailMessage) error
func (m *MailboxService) Broadcast(ctx context.Context, teamID string, from string, body string) error
func (m *MailboxService) ListUnread(ctx context.Context, teamID, agentID string, limit int) ([]MailMessage, error)
func (m *MailboxService) Ack(ctx context.Context, msgIDs []string) error
func (m *MailboxService) BuildDigest(ctx context.Context, teamID, agentID string, maxItems int) (string, error)
```

### Digest 输出示例

```text
Team digest:
- lead -> explorer-1: 请确认 auth 模块是否已有 refresh token 逻辑
- reviewer -> executor-2: pkg/http/middleware.go 存在 nil header 分支遗漏
- researcher -> *: 发现 3 个候选实现位置，推荐 pkg/auth/service.go
```

---

## 1.8 TeammateRunner

teammate 不是新 runtime，而是**复用现有 Session + Agent Loop**。

### 执行流

```text
Task 被 claim
 -> 找到对应 teammate session
 -> 构造 task-specific prompt
 -> 注入 mailbox digest + task spec + path constraints
 -> SubmitPrompt 给该 session actor
 -> 订阅 session 事件
 -> 等待完成 / 失败 / 超时
 -> 写回 task summary/result_ref
```

### 核心接口

```go
type TeammateRunner struct {
    Sessions SessionFacade
    Repo     Repo
    Mailbox  *MailboxService
}

func (r *TeammateRunner) StartTask(
    ctx context.Context,
    team Team,
    mate Teammate,
    task Task,
) error
```

### Task prompt 模板

```text
You are teammate {{name}} in team {{team_id}}.

Current task:
- Title: {{task.title}}
- Goal: {{task.goal}}
- Inputs: {{task.inputs}}

Constraints:
- Read paths: {{task.read_paths}}
- Write paths: {{task.write_paths}}
- Deliverables: {{task.deliverables}}

Mailbox digest:
{{mailbox_digest}}

Rules:
- Do not modify files outside write paths.
- Summarize decisions and blockers.
- If blocked, send a mailbox message to lead.
- When done, emit a concise task summary.
```

---

## 1.9 LeadPlanner

lead 不适合干大量写代码，它是拆任务和汇总结果的控制者。

### 职责

* 把顶层目标拆成 DAG
* 对失败 task 重试或重规划
* 判断是否新增 task
* 汇总最终结果给用户

### 接口

```go
type LeadPlanner struct {
    Sessions SessionFacade
    Repo     Repo
}

func (p *LeadPlanner) InitialPlan(ctx context.Context, team Team, goal string) ([]Task, []Dependency, error)
func (p *LeadPlanner) ReplanOnFailure(ctx context.Context, team Team, failed Task) ([]Task, []Dependency, error)
func (p *LeadPlanner) FinalSummary(ctx context.Context, teamID string) (string, error)
```

### 第一版不要过度自动化

第一版建议 lead 只做两种拆解：

* 初次任务分解
* 失败重试后的补充任务分解

不要第一版就做“动态无限扩 DAG”，否则调度会很难控。

---

# 二、把 Rewind 做到能真正上线

你当前有 checkpoint 存储，但缺真正的回退能力。这个模块必须做到“**代码可回退、对话可回退、两者可联动**”。

## 2.1 包结构

```text
internal/runtime/checkpoint/
  manager.go
  types.go
  capture.go
  restore.go
  preview.go
  transcript.go
  gc.go
```

---

## 2.2 关键模型

```go
type RewindMode string

const (
    RewindCode         RewindMode = "code"
    RewindConversation RewindMode = "conversation"
    RewindBoth         RewindMode = "both"
)

type Checkpoint struct {
    ID          string
    SessionID   string
    TurnID      string
    ParentID    *string
    Mode        string
    Summary     string
    HistoryHash string
    CreatedAt   time.Time
}

type CheckpointFile struct {
    ID           string
    CheckpointID string
    Path         string
    Op           string
    BeforeBlobID *string
    AfterBlobID  *string
    BeforeHash   *string
    AfterHash    *string
    DiffText     []byte
}
```

---

## 2.3 数据表

### checkpoints

```sql
CREATE TABLE checkpoints (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  parent_id TEXT,
  mode TEXT NOT NULL,
  summary TEXT,
  history_hash TEXT,
  created_at DATETIME NOT NULL
);
```

### checkpoint_files

```sql
CREATE TABLE checkpoint_files (
  id TEXT PRIMARY KEY,
  checkpoint_id TEXT NOT NULL,
  path TEXT NOT NULL,
  op TEXT NOT NULL,
  before_blob_id TEXT,
  after_blob_id TEXT,
  before_hash TEXT,
  after_hash TEXT,
  diff_text BLOB
);
```

### blobs

```sql
CREATE TABLE blobs (
  id TEXT PRIMARY KEY,
  sha256 TEXT NOT NULL UNIQUE,
  encoding TEXT NOT NULL,
  data BLOB NOT NULL
);
```

---

## 2.4 自动捕获

所有 mutating tools 必须统一从一个入口走：

```go
type MutationAwareTool interface {
    MutatedPaths(args json.RawMessage) ([]string, error)
}
```

然后在 ToolBroker 里：

```text
if tool is mutating:
   CheckpointManager.BeforeMutation(...)
   execute tool
   CheckpointManager.AfterMutation(...)
```

### BeforeMutation

* 解析受影响文件
* 读取变更前内容
* 建 checkpoint
* 暂存 before blobs

### AfterMutation

* 读取变更后内容
* 计算 diff
* 填 checkpoint_files
* 发 checkpoint created 事件

---

## 2.5 Rewind 实施流程

### `code`

1. 找到目标 checkpoint
2. 先创建一个 forward checkpoint
3. 逐文件恢复 `before_blob`
4. 删除目标 checkpoint 后新增的文件改动
5. 发 `RewindFinished` 事件

### `conversation`

不要删除历史消息，而是移动“可见 head”。

```go
type TranscriptHead struct {
    SessionID       string
    VisibleUntilSeq int64
}
```

恢复时：

* 找 checkpoint 对应的 transcript seq
* 更新 `visible_until_seq`
* 重新生成摘要与 ledger

### `both`

先 code，再 conversation。

---

## 2.6 Preview API

这个很关键，没有 preview 用户不敢点回退。

```go
type Preview struct {
    CheckpointID string
    Files        []PreviewFile
    Summary      string
}

type PreviewFile struct {
    Path      string
    Change    string
    DiffText  string
}
```

接口：

```go
func (m *Manager) PreviewRestore(ctx context.Context, sessionID, checkpointID string, mode RewindMode) (*Preview, error)
```

---

## 2.7 Bash 改动的处理策略

第一版不要强求 shell 改动完全可逆，建议分两阶段：

### 第一阶段

记录 bash 前后工作区变化：

* `git diff --name-status`
* 或对受关注目录做 hash 快照

把结果写入 checkpoint metadata，并标记：

```go
type RestoreConfidence string

const (
    RestoreFull    RestoreConfidence = "full"
    RestorePartial RestoreConfidence = "partial"
)
```

### 第二阶段

受控 sandbox / overlayfs 执行 bash，再做文件层回滚。

---

# 三、Hook Engine 的完整化方案

你现在只有 `PreToolUse` / `PostToolUse` 雏形，要扩成真正的生命周期系统。

## 3.1 包结构

```text
internal/runtime/hooks/
  manager.go
  types.go
  registry.go
  executor_shell.go
  executor_http.go
  matcher.go
  config.go
```

---

## 3.2 Hook 类型

```go
type HookEvent string

const (
    HookSessionStart      HookEvent = "session_start"
    HookSessionEnd        HookEvent = "session_end"
    HookUserPromptSubmit  HookEvent = "user_prompt_submit"
    HookPreToolUse        HookEvent = "pre_tool_use"
    HookPermissionRequest HookEvent = "permission_request"
    HookPostToolUse       HookEvent = "post_tool_use"
    HookSubagentStart     HookEvent = "subagent_start"
    HookSubagentStop      HookEvent = "subagent_stop"
    HookCheckpointCreated HookEvent = "checkpoint_created"
    HookRewindCompleted   HookEvent = "rewind_completed"
)
```

---

## 3.3 决策模型

```go
type HookAction string

const (
    HookContinue HookAction = "continue"
    HookBlock    HookAction = "block"
    HookModify   HookAction = "modify"
    HookNotify   HookAction = "notify"
    HookEnrich   HookAction = "enrich"
)

type HookDecision struct {
    Action         HookAction
    Message        string
    PatchedPayload json.RawMessage
    ExtraContext   map[string]string
}
```

---

## 3.4 配置格式

```yaml
hooks:
  - id: block-prod-edit
    event: pre_tool_use
    match:
      tools: ["Write", "Edit", "MultiEdit"]
      path_glob: ["prod/**"]
    exec:
      type: shell
      cmd: ["./hooks/block_prod_edit.sh"]
    timeout: 3s
    on_error: fail_closed

  - id: notify-approval
    event: permission_request
    exec:
      type: http
      url: "http://localhost:8080/hook/approval"
      method: POST
    timeout: 2s
    on_error: fail_open
```

---

## 3.5 运行规则

### 同步 hook

必须影响主流程的：

* `pre_tool_use`
* `permission_request`

### 异步 hook

只做通知和审计的：

* `session_start`
* `post_tool_use`
* `subagent_stop`
* `checkpoint_created`

---

# 四、Permission Engine 的 ask 模式

你已有静态策略，但缺运行时批准。

## 4.1 审批模型

```go
type ApprovalRequest struct {
    ID         string
    SessionID  string
    ToolName   string
    ArgsJSON   json.RawMessage
    Reason     string
    RiskLevel  string
    CreatedAt  time.Time
    ExpiresAt  time.Time
}
```

### Session runtime 状态补充

```go
type RuntimeState struct {
    SessionID       string
    Status          string
    PendingApproval *ApprovalRequest
    PendingQuestion *UserQuestionRequest
    UpdatedAt       time.Time
}
```

---

## 4.2 评估顺序

统一收口到：

```go
func (e *Engine) Evaluate(ctx context.Context, req EvalRequest) Decision
```

顺序固定：

1. hooks
2. rules
3. permission mode
4. callback
5. interactive ask

### Decision

```go
type DecisionType string

const (
    DecisionAllow DecisionType = "allow"
    DecisionDeny  DecisionType = "deny"
    DecisionAsk   DecisionType = "ask"
)

type Decision struct {
    Type    DecisionType
    Reason  string
    Patched json.RawMessage
}
```

---

## 4.3 Ask 执行流

```text
tool call
 -> evaluate() returns ask
 -> session enters waiting_approval
 -> emit approval_requested
 -> UI/CLI displays request
 -> user approves/rejects
 -> actor receives ApproveTool command
 -> loop resumes
```

---

# 五、AskUserQuestion 工具

这是你现在缺的“把澄清也纳入 loop”的关键能力。

## 5.1 工具 schema

```go
type AskUserQuestionArgs struct {
    Prompt      string   `json:"prompt"`
    Suggestions []string `json:"suggestions,omitempty"`
    Required    bool     `json:"required"`
}

type AskUserQuestionResult struct {
    QuestionID string `json:"question_id"`
    Answer     string `json:"answer"`
}
```

---

## 5.2 执行模型

它不是普通外部工具，而是 runtime 内部挂起点。

```text
model requests AskUserQuestion
 -> broker creates pending question
 -> session enters waiting_input
 -> emit question_asked
 -> user answers
 -> actor receives AnswerQuestion
 -> result injected as tool result
 -> loop continues
```

---

# 六、BackgroundTask + TaskOutput

你已有 queue / worker pool，但要接入 runtime 主循环。

## 6.1 表结构

```sql
CREATE TABLE background_jobs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  command TEXT,
  cwd TEXT,
  priority INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL,
  started_at DATETIME,
  finished_at DATETIME,
  exit_code INTEGER,
  log_path TEXT,
  metadata_json BLOB
);
```

```sql
CREATE TABLE background_job_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  payload_json BLOB NOT NULL,
  created_at DATETIME NOT NULL,
  UNIQUE(job_id, seq)
);
```

---

## 6.2 工具接口

### BackgroundTask

```go
type BackgroundTaskArgs struct {
    Command    string `json:"command"`
    Cwd        string `json:"cwd,omitempty"`
    TimeoutSec int    `json:"timeout_sec,omitempty"`
    Priority   int    `json:"priority,omitempty"`
}
```

### TaskOutput

```go
type TaskOutputArgs struct {
    JobID   string `json:"job_id"`
    Offset  int64  `json:"offset,omitempty"`
    Limit   int    `json:"limit,omitempty"`
}
```

---

## 6.3 输出读取模型

* 内存 ring buffer：最近输出快速读
* 文件日志：完整输出
* DB 事件：结构化状态

```go
type TaskOutputResult struct {
    JobID      string `json:"job_id"`
    Status     string `json:"status"`
    Output     string `json:"output"`
    NextOffset int64  `json:"next_offset"`
    ExitCode   *int   `json:"exit_code,omitempty"`
}
```

---

# 七、Session Actor 的增量改造

因为你的分析里 Session 基础已经有了，所以只需要把“运行时侧”补出来。

## 7.1 新文件

```text
internal/runtime/chat/
  actor.go
  commands.go
  events.go
  runtime_state.go
  event_hub.go
```

## 7.2 运行时命令

```go
type Command interface{ isCommand() }

type SubmitPrompt struct{ Text string }
type ApproveTool struct {
    RequestID   string
    Allow       bool
    PatchedArgs json.RawMessage
}
type AnswerQuestion struct {
    QuestionID string
    Answer     string
}
type Interrupt struct{}
type RewindTo struct {
    CheckpointID string
    Mode         string
}
```

## 7.3 事件

```go
type EventType string

const (
    EventAssistantDelta    EventType = "assistant_delta"
    EventToolStarted       EventType = "tool_started"
    EventToolFinished      EventType = "tool_finished"
    EventApprovalRequested EventType = "approval_requested"
    EventQuestionAsked     EventType = "question_asked"
    EventCheckpointCreated EventType = "checkpoint_created"
    EventJobStarted        EventType = "job_started"
    EventJobFinished       EventType = "job_finished"
)
```

---

# 八、分阶段实施顺序

最省返工的顺序不是“先写 Teams”，而是：

### 第 1 阶段

Session Actor 运行时补齐：

* `runtime_state`
* `approval`
* `question`
* `event hub`

### 第 2 阶段

Permission ask + AskUserQuestion + Hook 生命周期

### 第 3 阶段

Checkpoint auto-capture + Rewind + preview

### 第 4 阶段

BackgroundTask + TaskOutput + reconnect

### 第 5 阶段

Agent Teams core：

* team repo
* task graph
* mailbox
* lease
* path claims
* teammate runner

### 第 6 阶段

lead planner + team context digest + failure replan

---

# 九、你现在最值得先写的 8 个文件

如果要我按“开工第一周”来排，我会先写这 8 个：

```text
internal/runtime/chat/actor.go
internal/runtime/policy/approval.go
internal/runtime/tools/ask_user_question.go
internal/runtime/hooks/manager.go
internal/runtime/checkpoint/manager.go
internal/runtime/background/manager.go
internal/team/orchestrator.go
internal/team/sqlite_repo.go
```

因为这 8 个文件一旦落下去，后面的大模块都能挂上去，不会再反复拆结构。

---

# 十、我建议你下一步直接这样推进

先不要同时写所有未完成功能。最合理的是先定下面三组接口：

1. `SessionActor + ApprovalRequest + UserQuestionRequest`
2. `CheckpointManager + Rewind Preview`
3. `Team Repo + Task + Lease + PathClaim`

这三组一旦稳定，Teams、Hooks、BackgroundTask 都能自然接进去。

你要的话，下一条我可以直接给你一版**可落地的 Go 接口定义草案**，包括：

* `internal/team/repo.go`
* `internal/team/orchestrator.go`
* `internal/runtime/checkpoint/manager.go`
* `internal/runtime/chat/actor.go`

四个文件的完整接口和伪实现骨架。
