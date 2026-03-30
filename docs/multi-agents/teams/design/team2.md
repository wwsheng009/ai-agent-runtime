可以。下面我按“**基于你现有代码结构直接补齐，不推倒重来**”来给未完成功能做实现方案。

根据你上传的实现分析，当前真正的缺口集中在这几块：`Agent Teams` 完全未实现；`Hook Engine`、`Checkpoint/Rewind` 只做了一半；`Permission Engine` 缺交互式 `ask`、callback 和模式化控制；`Background Task` 缺工具化和持久化输出；`Session Actor` 还没有做成真正的 goroutine/事件驱动模型；`Tool Broker` 还缺 `AskUserQuestion` 和 `TaskOutput`；`Context Manager` 还缺 L4/L5；`Subagents` 缺 profile 外置和自动路由。你现有的 `agent loop`、`tool broker`、`context manager`、`subagents`、`MCP` 基本可以直接复用。

我建议把实现拆成两层优先级看：

业务优先级里，`Agent Teams` 最高；但工程依赖上，应该先补一条底座：**Session Actor + Event Bus + Approval/UserInput 暂停恢复机制**。因为 `Hook`、`ask` 权限、`AskUserQuestion`、`BackgroundTask`、`Rewind`、`Teams mailbox` 都会依赖这套暂停/恢复和事件订阅能力。否则越往后改动面越大。

---

# 一、先补底座：Session Actor + Event Bus + 暂停恢复

你现在的 `internal/runtime/chat/` 更像 CRUD 会话存储，不是真正的 Actor。这个要先补，因为后面所有“长生命周期能力”都需要它。

## 1.1 新增包和文件

建议在现有 `internal/runtime/chat/` 下新增：

```text
internal/runtime/chat/
  actor.go
  commands.go
  events.go
  hub.go
  runtime_state.go
  session_runtime_store.go
```

## 1.2 Actor 模型

每个活跃 session 对应一个 goroutine，串行消费命令，避免并发状态错乱。

```go
type SessionStatus string

const (
    SessionIdle            SessionStatus = "idle"
    SessionRunning         SessionStatus = "running"
    SessionWaitingApproval SessionStatus = "waiting_approval"
    SessionWaitingInput    SessionStatus = "waiting_input"
    SessionRewinding       SessionStatus = "rewinding"
    SessionStopped         SessionStatus = "stopped"
)

type SessionActor struct {
    ID          string
    CmdCh        chan Command
    State        *RuntimeState
    Hub          *EventHub
    Loop         *agent.ReActLoop
    Store        RuntimeStateStore
    SessionStore chat.Storage
}

type RuntimeState struct {
    SessionID           string
    Status              SessionStatus
    CurrentTurnID       string
    CurrentCheckpointID string
    PendingApproval     *ApprovalRequest
    PendingQuestion     *UserQuestionRequest
    HeadOffset          int64
    ActiveJobIDs        []string
    UpdatedAt           time.Time
}
```

## 1.3 命令集

```go
type Command interface{ isCommand() }

type SubmitPrompt struct{ Text string }
type ApproveTool struct {
    RequestID string
    Allow     bool
    PatchedArgs json.RawMessage
}
type AnswerQuestion struct {
    QuestionID string
    Answer     string
}
type Interrupt struct{}
type RewindTo struct {
    CheckpointID string
    Mode         string // code|conversation|both
}
type DeliverMailboxMessage struct {
    TeamID   string
    MessageID string
}
type SubscribeEvents struct {
    SubscriberID string
    Ch           chan Event
}
```

## 1.4 事件流

后面 CLI、IDE、Web 都不要直接读数据库，而是订阅事件。事件要同时写入**内存 fanout**和**持久化 event log**。

```go
type EventType string

const (
    EventAssistantDelta     EventType = "assistant_delta"
    EventToolStarted        EventType = "tool_started"
    EventToolFinished       EventType = "tool_finished"
    EventApprovalRequested  EventType = "approval_requested"
    EventQuestionAsked      EventType = "question_asked"
    EventCheckpointCreated  EventType = "checkpoint_created"
    EventRewindStarted      EventType = "rewind_started"
    EventRewindFinished     EventType = "rewind_finished"
    EventJobStarted         EventType = "job_started"
    EventJobOutput          EventType = "job_output"
    EventJobFinished        EventType = "job_finished"
    EventSubagentSpawned    EventType = "subagent_spawned"
    EventMailboxReceived    EventType = "mailbox_received"
)

type Event struct {
    SessionID string
    Seq       int64
    Type      EventType
    Payload   json.RawMessage
    At        time.Time
}
```

## 1.5 存储补齐

你现在已有 SQLite 和文件存储基础，建议补两个表：

```sql
CREATE TABLE session_runtime_state (
  session_id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  current_turn_id TEXT,
  current_checkpoint_id TEXT,
  pending_approval_json BLOB,
  pending_question_json BLOB,
  head_offset INTEGER NOT NULL DEFAULT 0,
  updated_at DATETIME NOT NULL
);

CREATE TABLE session_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  type TEXT NOT NULL,
  payload_json BLOB NOT NULL,
  created_at DATETIME NOT NULL,
  UNIQUE(session_id, seq)
);
```

## 1.6 为什么这一步必须先做

因为后面这些能力都会直接依赖这层：

* `Permission ask` 需要让 loop 暂停并等待批准
* `AskUserQuestion` 需要让 loop 暂停并等待答复
* `BackgroundTask` 需要异步输出事件
* `Rewind` 需要占用会话并切状态
* `Agent Teams` 需要 mailbox 和 teammate 事件
* `Hook Engine` 需要把生命周期事件统一发出去

---

# 二、Permission Engine：从“静态策略”升级为“运行时决策机”

你现在的策略已经有 `ReadOnly`、白黑名单、Sandbox、MCP trust level，但还缺最关键的运行时 `ask` 和 callback。

## 2.1 新增包

```text
internal/runtime/policy/
  engine.go
  rules.go
  modes.go
  approval.go
  callback.go
  matcher_path.go
  matcher_url.go
  matcher_cmd.go
```

## 2.2 统一评估顺序

建议固定为：

```text
hooks -> explicit rules -> permission mode -> callback -> interactive ask
```

这条链路不要散落在各处，全部收口到：

```go
type Engine struct {
    Hooks      HookDispatcher
    Rules      []Rule
    Mode       Mode
    Callback   CanUseToolCallback
}

func (e *Engine) Evaluate(ctx context.Context, req EvalRequest) Decision
```

## 2.3 资源级能力标签

不要只按 tool name 判权限，要引入 capability。

```go
type Capability string

const (
    CapReadOnly           Capability = "read_only"
    CapWriteFS            Capability = "write_fs"
    CapExecShell          Capability = "exec_shell"
    CapNetwork            Capability = "network"
    CapExternalSideEffect Capability = "external_side_effect"
    CapAskUser            Capability = "ask_user"
    CapBackgroundTask     Capability = "background_task"
)
```

每个 tool descriptor 增加：

```go
type ToolDescriptor struct {
    Name         string
    Capabilities []Capability
    RiskLevel    string
}
```

## 2.4 权限模式语义

```go
type Mode string

const (
    ModeDefault           Mode = "default"
    ModeAcceptEdits       Mode = "accept_edits"
    ModePlan              Mode = "plan"
    ModeBypassPermissions Mode = "bypass_permissions"
)
```

建议语义定成：

* `default`：读操作放行；写文件、shell、网络、外部副作用走规则，否则 ask
* `accept_edits`：允许本地文件编辑；shell/网络/外部副作用仍然 ask
* `plan`：只允许 read-only + AskUserQuestion，拒绝写和执行
* `bypass_permissions`：跳过交互审批，但不跳过硬性 deny 规则

## 2.5 交互式 ask 流程

```text
tool_call
 -> PermissionEngine.Evaluate()
 -> DecisionAsk
 -> 生成 ApprovalRequest
 -> SessionActor.Status = waiting_approval
 -> 发 EventApprovalRequested
 -> UI/CLI 用户批准
 -> ApproveTool 命令送回 Actor
 -> 恢复 tool 执行
```

审批请求模型：

```go
type ApprovalRequest struct {
    ID         string
    SessionID  string
    ToolName   string
    ArgsJSON   json.RawMessage
    Reason     string
    RiskLevel  string
    ExpiresAt  time.Time
}
```

## 2.6 `canUseTool` callback

给 IDE / SaaS 宿主保留一个 callback 扩展点：

```go
type CanUseToolCallback func(ctx context.Context, req EvalRequest) (Decision, string, error)
```

这样未来接企业策略、远端审批器、审计系统都不用改内核。

---

# 三、Tool Broker：补上 `AskUserQuestion`、`BackgroundTask`、`TaskOutput`

你当前工具系统已经很完整，缺的是三类“运行时工具”。

## 3.1 `AskUserQuestion`

这是一个**合成工具**，不是真正的外部执行器。它的作用是把模型提出的问题纳入 agent loop，而不是直接结束 turn。

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

执行逻辑：

1. 模型发起 `AskUserQuestion`
2. ToolBroker 不直接返回完成结果，而是返回 `requires_user_input`
3. SessionActor 进入 `waiting_input`
4. 前端展示问题
5. 用户回答后，`AnswerQuestion` 命令进入 actor
6. 结果被包装为 tool result 回灌模型
7. loop 继续

这样模型看到的仍然是“工具调用 -> 工具结果”，上下文因果链最完整。

## 3.2 `BackgroundTask`

把当前 `internal/taskqueue/` 包装成 tool，不要让后台任务绕开 runtime。

```go
type BackgroundTaskArgs struct {
    Command    string `json:"command"`
    Cwd        string `json:"cwd,omitempty"`
    TimeoutSec int    `json:"timeout_sec,omitempty"`
    Priority   int    `json:"priority,omitempty"`
}

type BackgroundTaskResult struct {
    JobID   string `json:"job_id"`
    Status  string `json:"status"`
    Message string `json:"message"`
}
```

## 3.3 `TaskOutput`

```go
type TaskOutputArgs struct {
    JobID   string `json:"job_id"`
    Offset  int64  `json:"offset,omitempty"`
    Limit   int    `json:"limit,omitempty"`
}

type TaskOutputResult struct {
    JobID      string `json:"job_id"`
    Status     string `json:"status"`
    Output     string `json:"output"`
    NextOffset int64  `json:"next_offset"`
    ExitCode   *int   `json:"exit_code,omitempty"`
}
```

---

# 四、Hook Engine：从 tool hook 扩成生命周期 Hook

你现在只有 `PreToolUse` / `PostToolUse` 的局部实现，离完整生命周期差一截。

## 4.1 不要继续塞在 `agent/hooks.go`

建议新建独立包：

```text
internal/runtime/hooks/
  manager.go
  types.go
  matcher.go
  executor_shell.go
  executor_http.go
  registry.go
  config.go
```

## 4.2 Hook 事件全集

```go
type Event string

const (
    EventSessionStart       Event = "session_start"
    EventSessionEnd         Event = "session_end"
    EventUserPromptSubmit   Event = "user_prompt_submit"
    EventPreToolUse         Event = "pre_tool_use"
    EventPermissionRequest  Event = "permission_request"
    EventPostToolUse        Event = "post_tool_use"
    EventSubagentStart      Event = "subagent_start"
    EventSubagentStop       Event = "subagent_stop"
    EventCheckpointCreated  Event = "checkpoint_created"
    EventRewindCompleted    Event = "rewind_completed"
)
```

## 4.3 Hook 配置格式

```yaml
hooks:
  - id: deny-prod-edit
    event: pre_tool_use
    match:
      tools: ["Write", "Edit", "MultiEdit"]
      path_glob: ["prod/**"]
    exec:
      type: shell
      cmd: ["./scripts/deny-prod-edit.sh"]
    timeout: 3s
    on_error: fail_closed

  - id: notify-approval
    event: permission_request
    exec:
      type: http
      url: "http://localhost:8080/hooks/approval"
      method: POST
    timeout: 2s
    on_error: fail_open
```

## 4.4 Hook 决策模型

```go
type DecisionAction string

const (
    DecisionContinue DecisionAction = "continue"
    DecisionBlock    DecisionAction = "block"
    DecisionModify   DecisionAction = "modify"
    DecisionNotify   DecisionAction = "notify"
    DecisionEnrich   DecisionAction = "enrich"
)

type HookDecision struct {
    Action         DecisionAction
    Message        string
    PatchedPayload json.RawMessage
    ExtraContext   map[string]string
}
```

## 4.5 执行策略

同步执行的事件：

* `pre_tool_use`
* `permission_request`

异步执行的事件：

* `session_start`
* `post_tool_use`
* `subagent_stop`
* `checkpoint_created`

原因很简单：同步事件会影响主路径决策；异步事件主要用于通知、格式化、审计，不该阻塞主 loop。

## 4.6 安全边界

Shell hook 至少要做这几件事：

* 限定工作目录
* 限定超时
* 限定 stdout/stderr 大小
* 限定返回 JSON 格式
* 明确 `fail_open` / `fail_closed`

---

# 五、Checkpoint + Rewind：把“可恢复”做成一等能力

你现在有 checkpoint 结构和存储，但缺真正的 rewind。这个模块不要只做“保存一个 history hash”，而要支持**代码恢复、会话恢复、联合恢复**。

## 5.1 新增包

```text
internal/runtime/checkpoint/
  manager.go
  capture.go
  diff.go
  restore.go
  preview.go
  conversation.go
```

## 5.2 存储模型

建议把 checkpoint 拆成三层：

### checkpoint 主表

```sql
CREATE TABLE checkpoints (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  parent_id TEXT,
  created_at DATETIME NOT NULL,
  mode TEXT NOT NULL,             -- code|conversation|both
  summary TEXT,
  history_hash TEXT
);
```

### checkpoint 文件变更表

```sql
CREATE TABLE checkpoint_files (
  id TEXT PRIMARY KEY,
  checkpoint_id TEXT NOT NULL,
  path TEXT NOT NULL,
  op TEXT NOT NULL,               -- create|update|delete
  before_blob_id TEXT,
  after_blob_id TEXT,
  before_hash TEXT,
  after_hash TEXT,
  diff_text BLOB
);
```

### blob 表

```sql
CREATE TABLE blobs (
  id TEXT PRIMARY KEY,
  sha256 TEXT NOT NULL UNIQUE,
  encoding TEXT NOT NULL,         -- raw|zstd
  data BLOB NOT NULL
);
```

## 5.3 自动捕获点

不要要求调用者自己记得建 checkpoint。做法应该是：

* 在 `Write`
* `Edit`
* `MultiEdit`
* `Delete`
* `Move/Rename`

这些 mutating tools 进入执行前，统一经过 `CheckpointManager.BeforeMutation(...)`。

流程：

```text
解析受影响文件
 -> 读取变更前内容
 -> 建 checkpoint 记录
 -> 执行工具
 -> 读取变更后内容
 -> 生成 diff_text
 -> 更新 checkpoint_files
 -> 发 EventCheckpointCreated
```

## 5.4 Rewind 三种模式

### code

恢复文件系统，不改对话历史。

### conversation

恢复会话视角，不改代码。

### both

先恢复代码，再恢复会话。

## 5.5 conversation rewind 的关键点

不要真正删消息。要把 transcript 做成不可变日志，然后用一个 `head_offset` 或 `visible_until_seq` 控制“当前会话看到哪儿”。

这样 rewind conversation 只需要：

1. 找到目标 checkpoint 对应的 transcript seq
2. 更新 `session_runtime_state.head_offset`
3. 重新计算 context summary / ledger
4. 发 `EventRewindFinished`

这比物理删除消息安全得多。

## 5.6 恢复过程

```go
type RestoreRequest struct {
    SessionID     string
    CheckpointID  string
    Mode          string
    PreviewOnly   bool
    ForcePartial  bool
}
```

恢复前先生成一个“forward checkpoint”，保证 rewind 自己也可撤销。

```text
create forward checkpoint
 -> preview diff
 -> apply file restore
 -> update transcript head
 -> rebuild summaries
 -> emit events
```

## 5.7 Bash 修改的处理

你上传的分析也指出当前 checkpoint 还没覆盖 shell 改动。这个一定要分阶段做。

### 第一阶段

对 bash 前后做工作区扫描：

* 记录 `git diff --name-status`
* 或者 `fsnotify + hash` 检测改动文件
* 把“bash touched files”记到 checkpoint metadata
* rewind 时标记“部分可恢复”

### 第二阶段

把 bash 放到受控容器/overlayfs 里执行，完整捕捉文件层差异。

不要试图第一版就让 shell 100% 可回滚，那会把工程复杂度拉爆。

---

# 六、Background Task：让异步任务进入 runtime 主循环

你现在有 queue 和 worker pool，但还缺会话级管理、持久化输出和断线重连。

## 6.1 新增表

```sql
CREATE TABLE background_jobs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  kind TEXT NOT NULL,             -- shell|analysis|fetch
  status TEXT NOT NULL,           -- queued|running|done|failed|cancelled
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

## 6.2 运行模型

`internal/taskqueue/` 继续做执行引擎，但要增加一个 `runtime/background` 包做会话级适配：

```text
internal/runtime/background/
  manager.go
  shell_job.go
  output_reader.go
  cancel.go
  persistence.go
```

## 6.3 输出模型

每个 job 同时维护：

* 内存 ring buffer：给 UI 快速展示最近输出
* append-only log 文件：完整输出
* `background_job_events`：结构化事件

## 6.4 必须支持的动作

* submit
* read output by offset
* cancel
* reconnect after restart
* auto cleanup policy

其中 `read output by offset` 是最关键的，因为这就是 `TaskOutput` tool 的底层。

---

# 七、Agent Teams：不要建立在“共享上下文”上，要建立在“共享任务图 + mailbox + lease”上

这是你当前最大的空白，也是你设计的核心创新点。

## 7.1 先说结论

不要把 `Agent Teams` 直接做成“多个 subagent 同时跑”：

* 现有 `SubagentScheduler` 更适合**短生命周期、单次委派**
* Team 需要的是**持久任务图、可恢复执行、消息通信、租约续期、文件冲突保护**
* 所以应当新建 `internal/team/` 作为**持久编排层**
* 现有 `SubagentScheduler` 只复用它的 prompt 组装、child policy、role defaults

## 7.2 新包结构

```text
internal/team/
  types.go
  orchestrator.go
  scheduler.go
  store.go
  task_graph.go
  lease.go
  mailbox.go
  teammate_runner.go
  path_claims.go
  summarizer.go
  config.go
```

## 7.3 核心数据模型

### Team

```go
type Team struct {
    ID             string
    WorkspaceID    string
    LeadSessionID  string
    Status         string // active|paused|done|failed
    Strategy       string
    MaxTeammates   int
    MaxWriters     int
    CreatedAt      time.Time
    UpdatedAt      time.Time
}
```

### Teammate

```go
type Teammate struct {
    ID             string
    TeamID         string
    Name           string
    Profile        string
    SessionID      string
    State          string // idle|busy|blocked|offline
    LastHeartbeat  time.Time
    Capabilities   []string
}
```

### Task

```go
type Task struct {
    ID            string
    TeamID        string
    ParentTaskID  *string
    Title         string
    Goal          string
    Status        string // pending|ready|running|blocked|done|failed|cancelled
    Priority      int
    Assignee      *string
    LeaseUntil    *time.Time
    RetryCount    int
    ReadPaths     []string
    WritePaths    []string
    Deliverables  []string
    Summary       string
    Version       int64
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```

### MailMessage

```go
type MailMessage struct {
    ID         string
    TeamID     string
    FromAgent  string
    ToAgent    string // "*" 代表广播
    TaskID     *string
    Kind       string // info|question|challenge|handoff|done|warning
    Body       string
    Metadata   map[string]any
    CreatedAt  time.Time
    AckedAt    *time.Time
}
```

## 7.4 SQLite 表

至少要有：

* `teams`
* `teammates`
* `team_tasks`
* `team_task_dependencies`
* `team_mailbox_messages`
* `team_path_claims`

其中 `team_path_claims` 很关键：

```sql
CREATE TABLE team_path_claims (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  path TEXT NOT NULL,
  mode TEXT NOT NULL,            -- read|write
  lease_until DATETIME NOT NULL
);
```

## 7.5 调度主循环

TeamOrchestrator 做两件事：

### 任务面

* 找 ready task
* 做 claim/lease
* 分配 teammate
* 监控 heartbeat
* 处理失败重试
* 解锁后续依赖

### 控制面

* lead 接收用户顶层目标
* 拆解成 task graph
* 汇总关键结果
* 决定是否继续拆分、合并、重试或终止

## 7.6 claim / lease 机制

一定要用**乐观并发**，不要用文件锁当真相源。

核心事务逻辑：

```sql
UPDATE team_tasks
SET status='running',
    assignee=?,
    lease_until=?,
    version=version+1,
    updated_at=CURRENT_TIMESTAMP
WHERE id=?
  AND status='ready'
  AND version=?;
```

只有 `rows_affected == 1` 才算 claim 成功。

然后立刻写入 `team_path_claims`，并检查路径冲突：

* read/read 可共存
* read/write 不共存
* write/write 不共存
* 前缀冲突也算冲突，如 `pkg/` 与 `pkg/a.go`

## 7.7 文件冲突保护

做一个 `PathClaimManager`：

```go
type PathClaimManager interface {
    CanClaim(teamID string, readPaths, writePaths []string) (bool, []Conflict)
    Acquire(taskID, owner string, readPaths, writePaths []string, leaseUntil time.Time) error
    Renew(taskID string, leaseUntil time.Time) error
    Release(taskID string) error
}
```

路径规范化要做：

* 转绝对路径
* `filepath.Clean`
* 统一 workspace root
* 目录写锁覆盖子路径

## 7.8 teammate 的执行方式

每个 teammate 不要自己再造 loop，而是复用现有 `runtime/agent` 和前面补出来的 `SessionActor`。

执行流：

```text
Orchestrator 选中 ready task
 -> 选择或创建 teammate session
 -> 组装 task-specific prompt
 -> DeliverMailboxDigest + TaskSpec
 -> SessionActor.SubmitPrompt(...)
 -> 监听 completion event
 -> 持久化结果 summary / artifacts / patches
 -> 更新 task 状态
 -> 解锁依赖
```

## 7.9 Mailbox 设计

Mailbox 不能直接塞进普通聊天历史，否则上下文会爆。

正确做法：

* 邮件进 `team_mailbox_messages`
* teammate 只在构造 prompt 前取“未读摘要”
* 长消息先进摘要器，生成短摘要
* 真正原文只在需要时按需拉取

所以 prompt 里不放整个 mailbox，只放 digest：

```text
Team mailbox digest:
- reviewer -> executor: “tests in pkg/auth failing on nil token path”
- researcher -> lead: “three candidate APIs found; recommend option B”
```

## 7.10 Team Lead 的工作模式

lead 不应参与大量细碎 edit，它的职责更像 orchestrator + summarizer：

* 初次拆解任务 DAG
* 监控 blocked / failed / idle teammate
* 处理升级问题
* 在关键节点发广播
* 汇总最终答案

这跟普通 executor agent 是不同角色，建议 profile 独立。

## 7.11 与现有 Subagent 的关系

你已有 `SubagentScheduler`，建议这样复用：

* `teammate_runner.go` 复用它的角色默认配置、工具白名单继承、prompt 生成
* `SubagentScheduler` 仍用于**单个 teammate 内部的局部 fan-out**
* `TeamOrchestrator` 负责跨任务、跨 session、跨 mailbox 的持久编排

不要反过来让 `SubagentScheduler` 直接管理 team task graph。

## 7.12 完成态判定

Team 模块做到下面这些，才算第一版可用：

* lead 能把顶层目标拆成 task DAG
* 两个以上 teammate 能并行执行
* 路径写冲突被阻止
* mailbox 能 direct/broadcast
* 失联 teammate 的 lease 会过期并重调度
* 最终结果能被 lead 汇总返回
* 过程中系统可重启并恢复运行状态

---

# 八、Context Manager：补 L4 / L5，但不要一上来做复杂向量库

你现在 L1-L3 已经足够强，L4/L5 是增量层。

## 8.1 L4 可检索知识层

你分析里已经提到 `artifact store` 有 FTS5。第一版不要急着上向量库，先做：

* 文档切块
* README / ADR / 设计文档索引
* 代码符号摘要索引
* FTS5 召回 + BM25 排序
* 需要时再加 embedding rerank

建议新包：

```text
internal/runtime/contextmgr/retrieval/
  indexer.go
  retriever.go
  chunker.go
  symbol_indexer.go
```

## 8.2 L5 团队共享层

这个必须建立在 Team 表之上：

* task graph 摘要
* teammate 状态
* mailbox digest
* 最新 blocker
* 已完成 deliverables

接口：

```go
type TeamContextBuilder interface {
    Build(teamID string, budget int) (*TeamContext, error)
}
```

输出到 prompt 的内容不要超过固定预算，比如 300~600 token 的 digest，而不是整个 task graph。

---

# 九、Subagents：把“已有 90%”补成可配置产品形态

你现在 subagent 已经很完整，缺的是 profile 外置和自动路由。

## 9.1 built-in profiles

先内置三种：

### explore

* 只读
* 工具：View/Ls/Grep/Glob/Fetch/WebSearch
* 便宜模型
* 适合 repo 探索、信息搜集

### planner

* 只读 + AskUserQuestion
* 工具：View/Grep/Glob/Todos/AskUserQuestion
* 适合拆任务、做方案

### executor

* 全工具
* 适合真正修改代码

## 9.2 profile 外置

```yaml
name: explore
description: repo exploration
model: sonnet
permission_mode: plan
allowed_tools: [View, Ls, Grep, Glob, Fetch, WebSearch]
max_turns: 8
system_prompt: |
  You are a read-only exploration agent...
```

加载顺序：

1. 内置 profile
2. 全局 `~/.gagent/profiles/*.yaml`
3. 项目 `.gagent/profiles/*.yaml`
4. 运行时 override

## 9.3 自动路由

第一版不要上模型分类器，先做 heuristic：

* 任务包含 “search / inspect / understand / locate” -> `explore`
* 任务包含 “plan / break down / design / compare” -> `planner`
* 有 `write_paths`、需要 patch、允许 edit -> `executor`

之后再换成 model-based classifier。

---

# 十、数据库迁移：现在就统一，不然后面会很乱

你分析里也指出缺统一 migration。这个要先定规矩。

建议：

```text
internal/store/migrations/
  0001_artifact_base.sql
  0002_session_runtime.sql
  0003_checkpoint_files.sql
  0004_background_jobs.sql
  0005_team_core.sql
  0006_team_path_claims.sql
```

并且明确：

* 所有模块的表结构都走 migration
* 代码里禁止隐式 `CREATE TABLE IF NOT EXISTS` 漫游扩表
* 启动时做 schema version check

---

# 十一、推荐实现顺序

从“尽量少返工”的角度，我建议按这个顺序推进：

## 第 1 步

`Session Actor + Event Bus + RuntimeStateStore`

这是后面暂停/恢复/订阅的底。

## 第 2 步

`Permission ask + ApproveTool + AskUserQuestion`

先把 loop 的暂停恢复跑通。

## 第 3 步

`Hook Engine` 扩成完整生命周期

这样权限和工具流都能被 hook。

## 第 4 步

`Checkpoint auto-capture + Rewind`

先支持文件编辑工具，再逐步补 bash 影响感知。

## 第 5 步

`BackgroundTask + TaskOutput + job persistence`

让异步工具进入 runtime 主路径。

## 第 6 步

`Agent Teams core`

包括 team/task/mailbox/lease/path claims/teammate runner。

## 第 7 步

`L5 Team Context + profile 外置 + 路由`

这是把 Teams 和 Subagents 体验做顺。

---

# 十二、每块的验收标准

## Session Actor

* 同一 session 不会并发跑两个 turn
* 工具审批能暂停并恢复
* 事件能被 SSE/WS/CLI 订阅
* 进程重启后能恢复 pending approval 状态

## Permission Engine

* 同一个 tool call 会按 hooks/rules/mode/callback 顺序裁决
* `ask` 触发后不会丢失执行上下文
* `plan` 模式禁止 mutating tools

## Hook Engine

* `pre_tool_use` 能 block / modify
* `permission_request` 能自动 approve/reject
* `post_tool_use` 失败不拖垮主会话

## Rewind

* 任意一次 `Write/Edit/MultiEdit` 前都会自动建 checkpoint
* 能 preview 某 checkpoint 的恢复 diff
* `code` / `conversation` / `both` 三种恢复模式可用

## BackgroundTask

* 提交长任务后会话仍能继续交互
* 可以通过 `TaskOutput` 分段读取输出
* 重启后能继续读取已完成任务日志

## Agent Teams

* 至少两个 teammate 可并行处理不同 task
* 路径写冲突被阻止
* mailbox 能 direct 和 broadcast
* teammate 丢失 heartbeat 后任务会被重新调度
* lead 能基于 task graph 汇总最终答复

---

# 十三、你这套代码最适合的改造方式

不是“重写一个新 runtime”，而是：

* 保留 `internal/runtime/agent/loop.go`
* 保留 `internal/toolkit/` 和 `internal/runtime/tools/`
* 保留 `internal/runtime/contextmgr/`
* 保留 `internal/taskqueue/`
* 保留 `internal/mcp/`

然后新增三层：

1. **会话运行时层**：`SessionActor + EventBus + Approval`
2. **可恢复执行层**：`Hook + Checkpoint + Rewind + BackgroundTask`
3. **团队编排层**：`internal/team/` + `L5 team context`

也就是说，你现有代码里“做得好的部分”不该推倒；真正要补的是“**长生命周期控制面**”。这也和你上传的实现分析完全一致：循环、工具、上下文、MCP 已接近完成，缺的是编排、恢复、交互和团队协作。

下一步最合适的是先把 `SessionActor`、`ApprovalRequest`、`AskUserQuestion`、`session_events` 这条底座接口定下来，再去接 `Rewind` 和 `Agent Teams`。
