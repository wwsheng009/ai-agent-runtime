我先给一个判断：从 Anthropic 公开出来的信息看，Claude Code 本质上不是“一个大 prompt + 一个 CLI”，而是一套**可复用的 headless agent runtime**。官方明确说，Claude Agent SDK 暴露的是驱动 Claude Code 的同一套 **tools、agent loop、context management、permissions framework**；同时，Claude Code 文档也写明不同界面都连接到同一个底层引擎，终端、VS Code、桌面、网页只是这个引擎的不同前端。([Anthropic][1])

这套 runtime 的核心循环是公开的：把用户 prompt、system prompt、工具定义和会话历史一起送进模型；模型先判断是直接回答还是发起一个或多个 tool call；运行工具后把结果回灌给模型；如此反复直到没有 tool call，再输出最终结果。Anthropic 在 Agent SDK 文档里把这套循环直接称为“the same execution loop that powers Claude Code”。([Claude 开发者平台][2])

从功能分层看，Claude Code 至少有六个层面：持久上下文（CLAUDE.md）、skills、subagents、agent teams、MCP 工具接入、hooks；plugins 则是把 slash commands、subagents、MCP servers、hooks 打包成可分发扩展。也就是说，它不是把所有能力写死在主循环里，而是一个“**内核 + 工具 + 扩展点 + 编排器**”的架构。([Claude][3])

你上传的文档对 Agent Teams 的总结，与官方文档是高度一致的：**Team Lead + 多个独立 Teammate + 共享任务列表 + agent 间消息系统**。官方确认了 team config 和 task list 会落到本地目录，teammate 拥有各自独立的 context window，会加载与普通会话相同的项目 context（CLAUDE.md、MCP、skills），但不会继承 lead 的对话历史；你的文档还补充了“文件锁避免重复认领”等实现线索。这里我会区分一下：前半部分是 Anthropic 官方确认的行为，后面的“文件锁”更像是你资料中的实现推断，我认为很合理，但不把它当作 Anthropic 明确公开的内部细节。([Claude][4]) 

基于这些公开能力，我建议你在 Golang 里不要“复刻 Claude Code 的 UI”，而是复刻它的**运行时结构**。最稳妥的目标形态是：

```text
                +------------------------------+
                |        CLI / TUI / IDE       |
                |  Cobra/BubbleTea/VSCode/Web  |
                +--------------+---------------+
                               |
                               v
                 +-------------+-------------+
                 |   Session API / Gateway   |
                 |   gRPC / WebSocket / SSE  |
                 +-------------+-------------+
                               |
                               v
+-------------------------------------------------------------------+
|                         AGENT RUNTIME CORE                         |
|                                                                   |
|  +----------------+  +----------------+  +----------------------+  |
|  | SessionManager |  | LoopOrchestr.  |  |  Model Adapter      |  |
|  +----------------+  +----------------+  +----------------------+  |
|  | ContextManager |  | Permission     |  |  Hook Engine        |  |
|  +----------------+  +----------------+  +----------------------+  |
|  | Tool Broker    |  | CheckpointMgr  |  |  BackgroundTaskMgr  |  |
|  +----------------+  +----------------+  +----------------------+  |
|  | SubagentMgr    |  | TeamOrchestr.  |  |  Skill/Plugin Loader|  |
|  +----------------+  +----------------+  +----------------------+  |
+-------------------------------------------------------------------+
                               |
          +--------------------+---------------------+
          |                                          |
          v                                          v
+---------------------------+             +--------------------------+
| Local Persistence         |             | External Capability      |
| SQLite + FS + NDJSON      |             | MCP / Git / Shell / Web  |
| sessions/checkpoints/tasks|             | custom tools / browsers  |
+---------------------------+             +--------------------------+
```

## 1. 内核设计：把“会话”做成 Actor，而不是普通请求

Claude Code 的很多能力——流式输出、长时间运行、用户中途打断、审批暂停、后台任务、子代理——都不适合简单的 HTTP request/response。更适合的模型是：**一个 Session = 一个有状态 Actor / goroutine**。

### SessionActor 的职责

每个会话维护自己的：

* 对话状态
* 当前上下文快照
* 正在执行的 turn
* 工具执行状态
* 审批等待点
* checkpoint 指针
* 子代理/队友引用
* 事件流订阅者（CLI、IDE、Web）

Go 里建议这样抽象：

```go
type SessionCommand interface{ isSessionCommand() }

type SubmitPrompt struct {
    UserText string
}

type ApproveTool struct {
    RequestID string
    Allow     bool
    PatchJSON []byte
}

type Interrupt struct{}
type Rewind struct {
    CheckpointID string
    Mode         string // code | conversation | both
}

type SessionActor struct {
    ID        string
    CmdCh     chan SessionCommand
    EventCh   chan Event
    State     *SessionState
    Runtime   *RuntimeDeps
}
```

这样做的好处是：
第一，天然适合流式输出和中断；第二，不容易把“审批中”“后台 Bash 还在跑”“队友消息到了”这些异步事件搞乱；第三，后面扩展到 subagent / team 时，本质上就是更多 actor 之间通信。

## 2. Agent Loop：这是整个系统的心脏

你要复刻的不是“聊天”，而是“带工具的推理循环”。建议把 loop 写成明确的状态机：

```text
INIT
 -> LOAD_CONTEXT
 -> BUILD_PROMPT
 -> CALL_MODEL
 -> EMIT_ASSISTANT_TEXT
 -> IF tool_calls? YES -> AUTHZ -> EXECUTE_TOOLS -> APPEND_RESULTS -> BUILD_PROMPT
 -> IF tool_calls? NO  -> FINALIZE
```

关键点有四个：

### 2.1 Prompt Builder

把这些输入拼成一次 turn：

* system prompt
* 项目/工作区 instructions（等价 CLAUDE.md）
* skills / plugins 提供的补充指令
* 最近若干轮原始消息
* 历史压缩摘要
* 当前任务/子任务描述
* 工具 schema
* 来自 task graph / mailbox 的团队信息

### 2.2 Model Adapter

不要把 Anthropic SDK 绑死在核心层，定义一个统一接口：

```go
type ModelProvider interface {
    RunTurn(ctx context.Context, req *ModelTurnRequest) (<-chan ModelEvent, error)
}
```

`ModelEvent` 至少包括：

* `TextDelta`
* `ToolCallRequested`
* `TurnCompleted`
* `UsageReported`
* `NeedUserInput`

这样以后可以切 Anthropic / OpenAI / 本地模型，甚至做双模型策略（比如 Explore 用便宜模型，Executor 用强模型）。

### 2.3 Tool Broker

官方文档表明 Claude Code/Agent SDK 的工具模型是“一组内置工具 + MCP 接入 + 权限框架”。你的 Go 架构也应该这样：所有工具都统一注册到 `ToolBroker`，而不是在业务代码里到处 if/else。([Claude 开发者平台][5])

```go
type Tool interface {
    Name() string
    Descriptor() ToolDescriptor
    Execute(ctx context.Context, call ToolCall) (*ToolResult, error)
}
```

内置建议先做这几类：

* Read / Write / Edit / Glob / Grep
* Bash
* Git
* AskUserQuestion
* BackgroundTask / TaskOutput
* WebSearch / WebFetch
* MCP bridge

### 2.4 事件流

所有中间态都发事件，不要只保存最终答案。CLI、IDE、Web 只是订阅事件：

* `AssistantTextDelta`
* `ToolCallStarted`
* `ToolCallFinished`
* `ApprovalRequested`
* `CheckpointCreated`
* `BackgroundTaskStarted`
* `SubagentSpawned`
* `TaskClaimed`
* `MailboxMessageReceived`

这会让你后面做 VS Code/网页前端非常轻松。

## 3. Context Manager：Claude Code 强的地方，不是上下文大，而是上下文有层次

Anthropic 公开推荐在长会话里用 **compaction**；原因不是只为省 token，而是上下文太长后模型会失焦，所以要用摘要替换旧内容、保持 active context 聚焦。([Claude 开发者平台][6])

因此你的 Go 设计里，`ContextManager` 不能只是“把所有历史消息拼起来”。我建议做成 5 层：

### L1. 永久指令层

* `CLAUDE.md` 等价文件
* 团队级规则
* 项目级规则
* 用户偏好

### L2. 工作记忆层

* 最近 N 轮原始消息
* 当前 turn 的工具结果
* 当前任务/子任务状态

### L3. 压缩摘要层

* 历史讨论摘要
* 已做决策摘要
* 未解决问题摘要
* 风险/假设摘要

### L4. 可检索知识层

* 文件索引
* 代码符号索引
* 文档/README/设计文档
* 向量检索结果（可选）

### L5. 团队共享层

* task graph 概要
* teammate 摘要
* mailbox 中的关键结论
* reviewer 反馈

### 推荐的 compaction 策略

不是做一个“总摘要”，而是分 4 种摘要对象：

* `DecisionSummary`
* `OpenQuestions`
* `WorkCompleted`
* `ArtifactIndex`

这样模型在后续 turn 更容易利用。

## 4. Permission Engine：按 Claude Code 的顺序实现

Anthropic 已经把权限评估顺序公开了：**hooks -> permission rules -> permission mode -> canUseTool callback**。这套顺序非常合理，你最好照抄，而不是重发明。([Claude 开发者平台][7])

在 Go 里我建议：

```go
type PermissionEngine interface {
    Evaluate(ctx context.Context, sess *SessionState, call ToolCall) PermissionDecision
}
```

决策链路：

1. `PreToolUse` hooks 先跑，可 allow/deny/modify
2. 声明式规则：

   * `deny`
   * `allow`
   * `ask`
3. 权限模式：

   * `default`
   * `acceptEdits`
   * `bypassPermissions`
   * `plan`
4. 如果还没定论，进入交互式审批

这也解释了为什么**审批必须是会话内状态的一部分**：官方文档明确说，工具审批和 `AskUserQuestion` 都会暂停执行，直到你把用户决定返回给 agent。([Claude 开发者平台][8])

### 一个很实用的额外设计

给每个工具打 capability 标签：

* `read_only`
* `write_fs`
* `exec_shell`
* `network`
* `external_side_effect`
* `high_risk`

这样规则可以按 capability，而不只是按 tool name 写。

## 5. Hook Engine：把自动化插在生命周期，而不是塞进 prompt

Claude Code 的 hooks 是在生命周期关键点触发的用户定义命令/HTTP/LLM prompt，事件包括 `SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PermissionRequest`、`PostToolUse`、`SubagentStart/Stop` 等。([Claude][9])

Go 里建议先只支持两类 hook：

* Shell hook
* HTTP hook

接口类似：

```go
type Hook interface {
    Match(event HookEvent, ctx HookContext) bool
    Run(ctx context.Context, input HookPayload) (*HookDecision, error)
}
```

`HookDecision` 支持：

* continue
* block
* modify input
* emit notification
* attach context

典型用法：

* `PreToolUse(Edit)` 后跑 formatter
* `PostToolUse(Write)` 后跑 lint/test
* `PermissionRequest` 发 Slack 通知
* `SubagentStop` 自动汇总产物

## 6. Checkpoint + Rewind：这是“放心放权”的前提

Claude Code 的 checkpointing 会在每次编辑前保存代码状态；checkpoint 持久化；可以恢复代码、对话或两者；但官方也明确说它只跟踪**文件编辑工具**做出的改动，不跟踪 bash 命令导致的文件变化。([Claude][10])

所以你的实现里建议把 checkpoint 做成独立模块，而不是 git 的附属品：

### 设计方案

* 每次 `Write/Edit` 前创建 checkpoint
* 存：

  * affected files
  * unified diff
  * optional full snapshot
  * session turn id
  * task id
* rewind 模式：

  * `code`
  * `conversation`
  * `both`

### 比 Claude Code 更进一步的一点

如果你愿意多做一步，可以把 Bash 放进受控工作目录或 overlayfs/container，这样**连 shell 改动也能纳入 checkpoint**。这会比公开的 Claude Code 更安全。

## 7. BackgroundTaskManager：长任务不要阻塞主 loop

官方文档说明了后台 Bash 的行为：命令异步执行，立刻返回 task ID，输出缓冲，可通过 `TaskOutput` 获取，Claude 还能继续响应新 prompt。([Claude][11])

你的 Go 里建议把后台任务抽象成 Job：

```go
type BackgroundJob struct {
    ID          string
    SessionID   string
    Command     string
    Status      string
    StartedAt   time.Time
    FinishedAt  *time.Time
    OutputPath  string
    ExitCode    *int
}
```

实现上：

* 每个后台进程独立 process group
* stdout/stderr 写 ring buffer + append-only log
* 提供 `TaskOutput(jobID, offset, limit)`
* 会话结束自动清理，或允许“detach + reconnect”

## 8. Subagents：先做成“受限 profile”，再做智能路由

Claude Code 文档已经把 subagents 的关键抽象讲得很清楚：每个 subagent 有独立 context、自定义 system prompt、特定工具访问和独立权限；内置还有 Explore（便宜、只读）、Plan（只读、服务于 plan mode）、General-purpose（全工具）。([Claude][12])

在 Go 里，不要把 subagent 先做成“另一个复杂系统”，而是先做成**Profile + SpawnPolicy**：

```go
type AgentProfile struct {
    Name           string
    Description    string
    SystemPrompt   string
    Model          string
    AllowedTools   []string
    PermissionMode string
    MaxTurns       int
}
```

建议内置 3 个：

* `explore`: 只读，便宜模型
* `planner`: 只读 + ask user
* `executor`: 全工具

路由方式分两层：

1. 显式调用：用户/主代理指定 profile
2. 自动调用：一个简单的 task classifier 判断是否该委托

## 9. Agent Teams：真正像 Claude Code 的部分

公开文档确认，Agent Teams 的结构就是：

* Team lead
* Teammates
* Shared task list
* Mailbox
* 本地 team config / task list 持久化
* 队友独立上下文
* 直接消息 / broadcast
* 自动解除依赖阻塞。([Claude][4])

### 我建议你在 Go 里这样做

#### 9.1 TeamOrchestrator

职责：

* 创建 team
* 生成 teammate session
* 拆分任务为 DAG
* 分配/重分配任务
* 汇总结果
* 监控 idle / timeout / failure

#### 9.2 Task Graph

不要只做“任务列表”，一定要做 DAG：

```go
type Task struct {
    ID            string
    TeamID        string
    Title         string
    Goal          string
    Inputs        []string
    DependsOn     []string
    Status        string // pending, ready, running, blocked, done, failed
    Assignee      string
    LeaseUntil    *time.Time
    Priority      int
    Deliverables  []string
    AffectedPaths []string
    Summary       string
}
```

#### 9.3 Claim 机制

我不建议直接用文件锁做真相源，推荐：

* **SQLite / Postgres 作为 source of truth**
* JSON 文件只是 projection/debug 视图

claim 用乐观并发即可：

```sql
UPDATE tasks
SET status='running', assignee=?, lease_until=?, version=version+1
WHERE id=? AND status='ready' AND version=?;
```

这样比文件锁更稳，也更容易做 lease 续期和故障恢复。

#### 9.4 Mailbox

消息不要直接塞进 prompt history；应该做成独立 mailbox：

* direct message
* broadcast
* task comment
* idle notification
* result summary

```go
type MailMessage struct {
    ID         string
    TeamID     string
    FromAgent  string
    ToAgent    string // "*" for broadcast
    TaskID     string
    Kind       string // info, question, challenge, handoff, done
    Body       string
    CreatedAt  time.Time
}
```

#### 9.5 文件冲突保护

Claude Code 官方也提醒 team work 要避免编辑同一文件。你的实现里可以显式加“文件所有权”机制：任务创建时声明 `AffectedPaths`，调度器避免把重叠写路径分给两个 write-capable agent。([Claude][4])

#### 9.6 默认调度策略

官方建议大多数工作流从 3–5 个队友开始，每人 5–6 个任务较合适。你可以把这个直接做成 scheduler 的默认启发式。([Claude][4])

## 10. 本地存储：我建议“SQLite 为主，文件系统为辅”

如果你的目标是“像 Claude Code 那样本地优先”，我推荐这个布局：

```text
~/.gagent/
  config.yaml
  sessions.db                # SQLite 真相源
  sessions/
    <session-id>/
      transcript.ndjson
      summaries/
      checkpoints/
      artifacts/
  teams/
    <team-id>/
      config.json            # projection，便于调试
      tasks/
      mailbox/
  plugins/
  skills/
```

### 为什么不用纯文件？

纯文件可读性高，但：

* claim/lease/依赖更新麻烦
* 容易出现并发一致性问题
* 统计和查询困难

所以最佳实践是：

* SQLite 存真相
* JSON/NDJSON 做调试投影
* 大输出和快照走文件系统

## 11. Go 包结构建议

```text
cmd/
  agent/              # CLI/TUI
  agentd/             # headless daemon
internal/
  api/                # gRPC, WS, SSE
  session/            # session actor, commands, events
  loop/               # execution loop
  model/              # anthropic/openai/local adapters
  context/            # builders, compaction, retrieval
  tools/              # builtin tools
  permissions/        # rules, modes, approvals
  hooks/              # lifecycle hooks
  checkpoint/         # snapshot/rewind
  background/         # async jobs, task output
  subagent/           # profile + delegation
  team/               # teams, tasks, mailbox, scheduler
  store/              # sqlite repos, blobs, projections
  workspace/          # fs sandbox, git, path policies
  plugins/            # skills/commands/plugins loader
pkg/
  protocol/           # shared DTOs
```

## 12. 我会怎么分阶段落地

### Phase 1：单 agent 内核

先只做：

* session actor
* loop orchestrator
* Read/Write/Edit/Bash
* permissions
* checkpoint
* CLI

这个阶段就能做出 70% 的“Claude Code 体验”。

### Phase 2：hooks + background tasks + compaction

补上：

* hook lifecycle
* BackgroundTask / TaskOutput
* context compaction
* AskUserQuestion

### Phase 3：subagents

引入：

* agent profile
* spawn/delegate
* summarize-back
* read-only explore agent

### Phase 4：agent teams

再上：

* task DAG
* mailbox
* claim/lease
* team lead orchestration
* file ownership/conflict guard

### Phase 5：多前端

最后接：

* VS Code
* Web UI
* remote session / daemon mode

## 13. 一些关键实现建议

第一，不要把“工具执行”写在模型适配层里。模型只负责产出 tool call；执行一定要走 Tool Broker。

第二，不要把团队协作建在“共享上下文”上，而要建在“共享任务图 + mailbox + 摘要”上。Claude Code 的公开设计也是这个方向。([Claude][4])

第三，不要把所有历史消息无脑塞回模型。长会话一定会失焦，必须做 compaction。([Claude 开发者平台][6])

第四，审批和用户澄清问题要当成 loop 内事件，不是 loop 外交互。Anthropic 的 `canUseTool` / `AskUserQuestion` 机制已经说明了这一点。([Claude 开发者平台][8])

第五，团队模式不要一上来就分布式。先做**单机多 session + SQLite + 本地 shell**，把编排跑顺了，再抽控制面和数据面。

---

如果你要我继续往下走，我下一步最有价值的是直接给你一版 **Golang 包结构 + 核心接口定义 + SessionActor/TeamOrchestrator 的代码骨架**。

[1]: https://www.anthropic.com/news/enabling-claude-code-to-work-more-autonomously "Enabling Claude Code to work more autonomously \ Anthropic"
[2]: https://platform.claude.com/docs/en/agent-sdk/agent-loop "How the agent loop works - Claude API Docs"
[3]: https://code.claude.com/docs/en/features-overview "Extend Claude Code - Claude Code Docs"
[4]: https://code.claude.com/docs/zh-CN/agent-teams "协调 Claude Code 会话团队 - Claude Code Docs"
[5]: https://platform.claude.com/docs/en/agent-sdk/overview "Agent SDK overview - Claude API Docs"
[6]: https://platform.claude.com/docs/en/build-with-claude/compaction "Compaction - Claude API Docs"
[7]: https://platform.claude.com/docs/zh-CN/agent-sdk/permissions "配置权限 - Claude API Docs"
[8]: https://platform.claude.com/docs/en/agent-sdk/user-input "Handle approvals and user input - Claude API Docs"
[9]: https://code.claude.com/docs/zh-CN/hooks "Hooks 参考 - Claude Code Docs"
[10]: https://code.claude.com/docs/zh-CN/checkpointing "checkpointing - Claude Code Docs"
[11]: https://code.claude.com/docs/en/interactive-mode "Interactive mode - Claude Code Docs"
[12]: https://code.claude.com/docs/zh-CN/sub-agents "创建自定义 subagents - Claude Code Docs"
