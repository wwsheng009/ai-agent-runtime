下面这版我不再停留在原则层，而是给你一套能直接映射到 Go 项目的**详细实施蓝图**。它的核心前提有两个：第一，Claude 的 Messages API 是**无状态**的，所以你必须自己维护会话、上下文和工具循环；第二，Anthropic 公开出来的 Claude Code/Agent SDK 的关键能力，本质上是 **agent loop、tool execution、subagent delegation、context management**，只是现成 SDK 主要在 Python / TypeScript。换句话说，Go 侧要做的是一个 **runtime**，不是去等一个官方 Go Agent SDK。([Claude开发平台][1])

Anthropic 这两年的公开材料，其实已经把你要实现的重点说得很清楚了：长任务难点不在“多调几次模型”，而在**跨多个 context window 的持续推进**；官方给出的解法方向是 **compaction、structured note-taking、multi-agent architectures**，并且强调 agent 应该只在工作内存里保留必要内容，其他内容通过笔记、检索和逐步披露来管理。([Anthropic][2])

## 1. 先把目标收敛成一个产品形态

你真正要落地的不是“Claude Code 风格的 UI”，而是一个 **Agent Runtime**。这套 runtime 要提供 6 个能力：

1. 会话可持续：单任务能跑很多轮，不因 transcript 膨胀而被迫重开。
2. 工具可治理：任何工具输出都先经过网关，不允许原始大输出直灌模型。
3. 记忆可分层：热上下文、温记忆、冷存储三层分离。
4. 子代理可并发：主代理只做计划和综合，子代理独立上下文执行。
5. MCP 可接入：但必须先经过 host/gateway，而不是 JSON-RPC 直通模型。
6. 全链路可回放：任意 turn、tool call、compaction、subagent 都能审计复现。

这个方向和 Anthropic 公开的 agent loop、subagents、hooks、tool search、context engineering 设计意图是一致的。官方 Agent SDK 明确把 Claude Code 的自动执行循环、工具、权限、输出控制和上下文管理开放出来；subagents 则明确用于**隔离上下文、并行任务、避免主提示膨胀**。([Claude开发平台][3])

---

## 2. 总体架构：分成控制面和数据面

我建议做成一个**模块化单体**，先不要拆微服务。

```text
                        +----------------------+
                        |   API / CLI / IDE    |
                        +----------+-----------+
                                   |
                                   v
                    +-------------------------------+
                    | Session Gateway / Orchestrator|
                    +-------------------------------+
                                   |
                                   v
+-------------------------------------------------------------------+
|                         Agent Runtime                             |
|  +----------------+  +----------------+  +----------------------+  |
|  | ContextManager |  | Scheduler      |  | Policy / Event Bus   |  |
|  | Hot/Warm/Cold  |  | Root/Subagents |  | Hooks / Audit        |  |
|  +----------------+  +----------------+  +----------------------+  |
+-------------------------------------------------------------------+
          |                         |                      |
          v                         v                      v
+------------------+     +------------------+     +------------------+
| Model Adapter    |     | Tool Broker      |     | Memory / Recall  |
| Claude Messages  |     | Local/MCP/Internal|    | SQLite FTS5      |
| count_tokens     |     | Sandbox Exec     |     | Ledger/Checkpoints|
+------------------+     +------------------+     +------------------+
                                   |
                                   v
                        +----------------------+
                        |   Output Gateway     |
                        | raw -> artifact      |
                        | reduce -> envelope   |
                        +----------------------+
                                   |
                                   v
                        +----------------------+
                        | Artifact Store        |
                        | blobs/files + meta    |
                        +----------------------+
```

之所以这样拆，是因为 Messages API 每轮都要你自己提交完整历史；tool result 在回给 Claude 前又允许被你拦截和变形；tool search 还支持 **client-side** 实现，不必把全部工具定义预先塞进上下文。也就是说，**上下文、工具、编排、审计** 都天然属于 host/runtime，不属于模型。([Claude开发平台][1])

---

## 3. 模块职责，按实现视角拆开

### 3.1 Session Gateway

它是对外入口，负责：

* 创建 session
* 接收用户输入
* 恢复 checkpoint
* 驱动 root task
* 暴露 streaming 结果
* 查询审计记录 / artifacts / child reports

建议协议：

* `POST /sessions`
* `POST /sessions/{id}/messages`
* `GET /sessions/{id}`
* `GET /sessions/{id}/events`
* `GET /sessions/{id}/artifacts`
* `POST /sessions/{id}/cancel`

### 3.2 Agent Runtime

这是总控器。核心循环只做 5 件事：

1. `ContextManager.BuildRequest()`
2. `ModelAdapter.CountTokens()`
3. `ModelAdapter.CreateMessage()`
4. 分发 `tool_use / text / delegate`
5. 写回 `ToolEnvelope / SubagentReport / final answer`

Claude 的 agent loop 本质就是：Claude 评估 prompt，调用工具，接收结果，再重复直到任务结束。这个执行逻辑是 Claude Code/Agent SDK 的核心，也是你在 Go 里最该自己实现的部分。([Claude开发平台][3])

### 3.3 Context Manager

这是最重要的模块，不是辅助模块。

职责：

* 构建发送给模型的 **Hot Context**
* 管理不总是发送的 **Warm Memory**
* 管理只通过引用存在的 **Cold Artifacts**
* 在 token 高位时触发 compaction
* 在模型需要更多证据时做 page-in / recall
* 生成和维护 decision ledger

Anthropic 的 context engineering 文章强调，agent 应该只在 working memory 里保留必要内容，其他内容通过逐步发现、笔记和结构化状态来管理。([Anthropic][2])

### 3.4 Tool Broker

统一接入三类工具：

* 本地工具：读文件、grep、git、测试、Playwright、bash
* 内部 API：日志平台、搜索平台、知识库、数据库
* MCP 工具：来自 MCP server 的 tools/resources/prompts

Broker 只负责“执行”和“返回原始结果”，不负责把结果直接喂模型。

### 3.5 Output Gateway

这是你方案里最值钱的一层。

职责：

* 接收 `ToolRawResult`
* 将原始输出写入 Artifact Store
* 调用 reducer 做**确定性压缩**
* 输出统一的 `ToolEnvelope`
* 决定哪些字段允许进入上下文

这层存在的制度依据很明确：Anthropic 官方文档明确写了，**tool results 可以在送回 Claude 前被修改**，包括为了缓存或为了变换输出；而这正好给你实现 `raw -> artifact -> reduced envelope` 的空间。([Claude开发平台][4])

### 3.6 Scheduler / Supervisor

负责任务拆分和子代理并发。

职责：

* 把 root 任务拆成子任务包
* 启动独立 child session
* 限制并发、深度、预算、超时
* 汇总 child report
* 把 child transcript 留在子上下文，不回流给父级

Anthropic 的 subagents 文档明确说，subagents 是**独立 agent 实例**，用于隔离上下文、并行分析和专门指令，而不是共享主代理那坨上下文。([Claude开发平台][5])

### 3.7 Policy / Hooks / Event Bus

把安全、审计、审批从 prompt 里拿出来。

你在 Go 里直接做自己的 hooks 体系即可。Anthropic 官方 hooks 文档给出的典型事件包括 `PreToolUse`、`PostToolUse`、`SubagentStart`、`SubagentStop`、`PreCompact` 等，说明“拦截工具、跟踪子代理、归档压缩前状态”本来就是 agent runtime 的一等公民。([Claude开发平台][6])

---

## 4. 关键数据流，按真实执行顺序展开

### 4.1 启动一个新 session

```text
User request
-> Create session
-> Create root task
-> Load project memory / policy
-> Build initial context
-> Count tokens
-> Send to Claude
```

这里一定要在真正调用模型前做 token 估算，因为官方 token counting 接口就是为“在发送前估算消息 token、做成本和模型路由决策”设计的，而且它接受与消息创建相同的结构化输入。([Claude开发平台][7])

### 4.2 标准 agent loop

```text
BuildRequest
-> CountTokens
-> Maybe Compact
-> Messages API
-> Assistant text? append summary
-> Tool use? execute tools
-> Tool result reduce/admit
-> continue
-> Final answer
```

要注意一个实现细节：手动 tool loop 时，Claude 返回 `stop_reason = tool_use` 后，你要提取 `tool_use.id / name / input`，执行工具，再把 `tool_result` 以新的 `user` 消息回传。并且 `tool_result` 必须紧跟对应的 `tool_use`，而且在该 user 消息里必须排在最前。这个约束最好直接固化进 `MessageBuilder`。([Claude开发平台][4])

### 4.3 工具执行数据流

```text
tool_use
-> Tool Broker Exec
-> ToolRawResult
-> ArtifactStore.Put(raw)
-> OutputGateway.Reduce(raw, refs)
-> ToolEnvelope
-> ContextManager.Admit(env)
-> build tool_result message
-> next model turn
```

真正进模型的不是 stdout/stderr/html/snapshot/json 原文，而是 reducer 产出的 envelope。原始内容只存在 artifact store 和索引里。

### 4.4 Context Compaction 数据流

```text
BuildRequest
-> CountTokens > threshold?
-> PreCompact event
-> summarize dialogue to ledger
-> demote old evidence to warm/cold
-> keep latest N turns + current plan + open questions
-> rebuild request
```

Anthropic 明确把 compaction 当成处理长任务 context pollution 的核心手段之一。([Anthropic][2])

### 4.5 Recall / Page-in 数据流

```text
Model/Planner asks for more evidence
-> Recall(query, filters)
-> SQLite FTS5 top-k chunks
-> budget filter
-> snippet envelope
-> admit to hot context
```

Recall 不是“把旧 transcript 再塞回来”，而是“只把当前问题需要的相关片段 page-in”。

### 4.6 主代理委派给子代理

```text
Root decides delegate
-> spawn_subagents tool / internal command
-> create child tasks
-> fresh child sessions
-> child loops independently
-> child returns SubagentReport
-> parent admits only report
-> parent replans or finalizes
```

这正对应 Anthropic 在多代理研究系统里描述的 orchestrator-worker 模式：lead agent 先规划，再起并行 agent 搜索，最后由 lead agent 综合结果。([Anthropic][8])

### 4.7 MCP 工具调用数据流

```text
MCP Gateway connect
-> initialize / capabilities / initialized
-> list tools/resources
-> build local catalog
-> tool search
-> call tool
-> raw MCP result
-> Output Gateway
-> ArtifactStore
-> ToolEnvelope
-> back to runtime
```

MCP 是 host-client-server 架构，协议是**有状态 session**，初始化必须是第一步；因此真正负责 orchestration、权限、聚合和隔离的是 host，不是 server，更不是模型本身。([模型上下文协议][9])

---

## 5. Go 项目结构，建议直接按这个拆

```text
/cmd/agentd
/internal/api
/internal/runtime
/internal/runtime/loop
/internal/runtime/messagebuilder
/internal/context
/internal/context/hot
/internal/context/compact
/internal/context/recall
/internal/model/anthropic
/internal/scheduler
/internal/tools
/internal/tools/local
/internal/tools/mcp
/internal/tools/reducers
/internal/artifact
/internal/store/sqlite
/internal/store/blob
/internal/policy
/internal/events
/internal/evals
/internal/telemetry
/pkg/types
```

### 核心接口先定死

```go
type ModelClient interface {
    CountTokens(ctx context.Context, req MessageRequest) (int, error)
    CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
}

type ContextManager interface {
    BuildRequest(ctx context.Context, s *Session) (MessageRequest, error)
    AdmitToolEnvelope(ctx context.Context, s *Session, env ToolEnvelope) error
    AdmitSubagentReport(ctx context.Context, s *Session, rep SubagentReport) error
    Compact(ctx context.Context, s *Session, reason string) error
}

type ToolBroker interface {
    Exec(ctx context.Context, tc ToolCall) (ToolRawResult, error)
}

type OutputGateway interface {
    Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error)
}

type ArtifactStore interface {
    PutRaw(ctx context.Context, raw ToolRawResult) ([]ArtifactRef, error)
    Recall(ctx context.Context, q RecallQuery) ([]ArtifactSnippet, error)
}

type Scheduler interface {
    RunChildren(ctx context.Context, parent *Session, tasks []TaskPacket) ([]SubagentReport, error)
}

type PolicyEngine interface {
    AllowTool(ctx context.Context, call ToolCall) error
    AllowDelegate(ctx context.Context, tasks []TaskPacket) error
}

type EventBus interface {
    Emit(ctx context.Context, evt Event) error
}
```

---

## 6. 数据模型，建议第一版就按事件溯源思路来

### 6.1 关键实体

* `sessions`
* `tasks`
* `messages`
* `turn_snapshots`
* `tool_calls`
* `artifacts`
* `artifact_chunks`
* `memory_entries`
* `events`
* `mcp_servers`
* `tool_catalog`
* `checkpoints`

### 6.2 关键表结构

```sql
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  root_task_id TEXT,
  model TEXT NOT NULL,
  status TEXT NOT NULL,           -- new/running/waiting/completed/failed/cancelled
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  parent_task_id TEXT,
  role TEXT NOT NULL,             -- root/researcher/tester/writer/verifier
  goal TEXT NOT NULL,
  success_criteria_json TEXT NOT NULL,
  allowed_tools_json TEXT NOT NULL,
  status TEXT NOT NULL,
  budget_tokens INTEGER NOT NULL,
  budget_usd REAL NOT NULL DEFAULT 0,
  max_turns INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE messages (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  turn_no INTEGER NOT NULL,
  role TEXT NOT NULL,             -- system/user/assistant/tool/internal
  content_json TEXT NOT NULL,
  input_tokens INTEGER,
  output_tokens INTEGER,
  created_at TEXT NOT NULL
);

CREATE TABLE artifacts (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  artifact_type TEXT NOT NULL,    -- stdout/stderr/json/html/snapshot/image/log
  path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  metadata_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE artifact_chunks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  artifact_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  chunk_no INTEGER NOT NULL,
  chunk_text TEXT NOT NULL,
  path TEXT,
  created_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE artifact_chunks_fts USING fts5(
  chunk_text,
  content='artifact_chunks',
  content_rowid='id'
);

CREATE TABLE memory_entries (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  task_id TEXT NOT NULL,
  kind TEXT NOT NULL,             -- decision/fact/open_question/plan/failure/child_summary
  priority INTEGER NOT NULL,
  content_json TEXT NOT NULL,
  source_refs_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE events (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  task_id TEXT,
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE checkpoints (
  session_id TEXT PRIMARY KEY,
  plan_json TEXT NOT NULL,
  ledger_json TEXT NOT NULL,
  open_questions_json TEXT NOT NULL,
  latest_artifact_refs_json TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

### 6.3 这三个表最重要

* `memory_entries`：不是存“总结长文”，而是存结构化 ledger
* `artifacts`：存原始大输出
* `artifact_chunks_fts`：存召回入口

这样 compact 和 recall 才不会重新依赖 transcript。

---

## 7. Context Manager 的详细设计

### 7.1 三层上下文

**Hot Context**：每轮真实发送给模型
内容只包含：

* system prompt
* 当前任务包
* 当前计划
* decision ledger 的高优先级条目
* open questions
* 最近 2~4 轮对话
* 最近少量 `ToolEnvelope`
* 必要工具定义

**Warm Memory**：默认不发送
包含：

* 历史决策
* 已证事实
* 被验证失败的路径
* 过时但可能有用的子代理结论

**Cold Store**：永不直接进模型
包含：

* Playwright DOM/snapshot
* 长日志
* `git log` 原文
* 测试 JSON
* MCP 原始响应
* 大文档全文

### 7.2 BuildRequest 算法

```go
func (m *Manager) BuildRequest(ctx context.Context, s *Session) (MessageRequest, error) {
    hot := NewContextBudget(m.MaxInputTokens)

    hot.AddSystem(m.SystemPrompt(s))
    hot.AddTaskPacket(m.CurrentTaskPacket(s))
    hot.AddLedger(m.LoadHighPriorityLedger(s, 12))
    hot.AddOpenQuestions(m.LoadOpenQuestions(s, 8))
    hot.AddRecentTurns(m.LoadRecentTurns(s, 4))
    hot.AddEvidence(m.LoadFreshEvidence(s, 6))
    hot.AddTools(m.SelectToolsForThisTurn(s))

    req := hot.ToMessageRequest()

    n, _ := m.model.CountTokens(ctx, req)
    if n > m.CompactThreshold {
        _ = m.Compact(ctx, s, "threshold_exceeded")
        req = m.RebuildAfterCompaction(ctx, s)
    }
    return req, nil
}
```

### 7.3 预算建议

第一版直接用固定配额：

* 10% system / policy
* 10% current task packet
* 15% ledger / notes
* 15% recent turns
* 20% evidence
* 10% tools
* 20% headroom

触发规则：

* `> 70%`：允许当前轮继续，但标记 `compact_pending`
* `> 85%`：发送前必须 compact
* `> 92%`：禁止注入新大证据，只允许摘要
* `> 97%`：拒绝再委派 subagent，只能收束

### 7.4 Admission Rule

所有工具输出统一走：

```text
raw output
-> classify
-> store raw
-> run reducer
-> build envelope
-> token budget check
-> admit yes/no
```

Admission Rule 要硬编码成系统规则，不要交给模型自己决定。

### 7.5 Compaction 规则

旧内容压成 3 类：

* `decisions[]`
* `facts[]`
* `open_questions[]`

不要压成一大段自然语言摘要，那会让之后的 recall 和过滤很痛苦。

### 7.6 Recall 规则

召回函数只做三件事：

1. 按 `session_id / task_id / tool_name / artifact_type` 过滤
2. FTS5 top-k 检索
3. 对命中片段再次做 budget filter 和 envelope 化

```go
type RecallQuery struct {
    SessionID    string
    TaskID       string
    ToolNames    []string
    ArtifactType []string
    Query        string
    K            int
}
```

---

## 8. Output Gateway：把“Context Mode”做成插件体系

### 8.1 核心接口

```go
type Reducer interface {
    Name() string
    Match(raw ToolRawResult) bool
    Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error)
}
```

### 8.2 统一输入

```go
type ToolRawResult struct {
    ToolName   string
    ExitCode   int
    Stdout     []byte
    Stderr     []byte
    Files      []string
    Metadata   map[string]any
    StartedAt  time.Time
    FinishedAt time.Time
}
```

### 8.3 统一输出

```go
type ToolEnvelope struct {
    Summary       string
    KeyFacts      []string
    Warnings      []string
    ArtifactRefs  []string
    RecallHints   []string
    SuggestedNext []string
    IsError       bool
}
```

### 8.4 第一批 reducer

`git_log_reducer`

* commit 总数
* 最近窗口
* 高频作者
* 高频目录
* 可疑 revert / merge
* 相关 commit refs

`go_test_json_reducer`

* 失败包
* 失败用例
* 首个 stack 入口
* panic 摘要
* 重现命令
* 相关日志 artifact ref

`playwright_snapshot_reducer`

* URL / title
* 可见关键文本 top-N
* console errors
* failed requests
* screenshot ref
* selector hints

`grep_repo_reducer`

* 命中数量
* 最高相关文件
* 关键片段摘要
* 路径 refs

这样做的目的不是“优雅”，而是把 token 消耗从“原文回流”变成“可控 envelope”。

---

## 9. 主代理与子代理：把协同变成任务协议

### 9.1 角色设计

只保留 5 类角色：

* `root-planner`
* `repo-reader`
* `test-runner`
* `log-investigator`
* `writer`（后期才开放）

第一版不要让任何 child 写文件。

### 9.2 任务包协议

```go
type TaskPacket struct {
    Name            string
    Goal            string
    SuccessCriteria []string
    AllowedTools    []string
    ArtifactRefs    []string
    MaxTurns        int
    BudgetTokens    int
    BudgetUSD       float64
    ReportSchema    string
}
```

### 9.3 子代理回执协议

```go
type SubagentReport struct {
    TaskName       string
    Status         string
    Summary        string
    Findings       []string
    EvidenceRefs   []string
    OpenQuestions  []string
    SuggestedNext  []string
    CostTokens     int
    DurationMs     int64
}
```

### 9.4 三条协同硬规则

1. `many readers, single writer`
2. `child-child` 不直接通信
3. 父级只消费 `SubagentReport`，不消费 child transcript

### 9.5 Scheduler 逻辑

```go
type SchedulerConfig struct {
    MaxConcurrentChildren int
    MaxDepth              int
    MaxChildTurns         int
    ChildTimeout          time.Duration
}
```

调度规则建议：

* 最大并发先设 `3~4`
* 子代理深度先限制为 `1`
* 单 child 最多 `4~6 turns`
* child 超时就强制回收并写失败报告
* 同类任务优先去重，避免重复搜索

---

## 10. root loop 的实现伪代码

```go
for !session.Done() {
    req := contextMgr.BuildRequest(ctx, session)

    tokens, err := model.CountTokens(ctx, req)
    if err != nil { return err }

    if tokens > cfg.HardThreshold {
        if err := contextMgr.Compact(ctx, session, "hard_threshold"); err != nil {
            return err
        }
        continue
    }

    resp, err := model.CreateMessage(ctx, req)
    if err != nil { return err }

    switch {
    case resp.HasToolUse():
        for _, tc := range resp.ToolCalls {
            if tc.Name == "spawn_subagents" {
                tasks := decodeTaskPackets(tc.Input)
                reports, err := scheduler.RunChildren(ctx, session, tasks)
                if err != nil { return err }
                for _, rep := range reports {
                    _ = contextMgr.AdmitSubagentReport(ctx, session, rep)
                }
                continue
            }

            if err := policy.AllowTool(ctx, tc); err != nil {
                _ = contextMgr.AdmitToolEnvelope(ctx, session, deniedEnvelope(tc, err))
                continue
            }

            raw, err := broker.Exec(ctx, tc)
            if err != nil { raw = buildErrorRaw(tc, err) }

            refs, err := artifactStore.PutRaw(ctx, raw)
            if err != nil { return err }

            env, err := outputGateway.Reduce(ctx, raw, refs)
            if err != nil { return err }

            if err := contextMgr.AdmitToolEnvelope(ctx, session, env); err != nil {
                return err
            }
        }

    case resp.HasFinalText():
        _ = contextMgr.AdmitAssistantFinal(ctx, session, resp.FinalText)
        session.MarkDone()
    }
}
```

---

## 11. Tool Broker 和 Sandbox，第一版就要带安全边界

### 11.1 本地工具分级

**Level 0：只读**

* `read_file`
* `glob`
* `grep`
* `git_status`
* `git_log`
* `git_show`
* `run_command_readonly`

**Level 1：可执行但无写权限**

* `go test`
* `pytest`
* `npm test`
* `playwright test`
* `curl` 只读白名单域名

**Level 2：写操作**

* `write_file`
* `apply_patch`
* `git_checkout`
* `rm`
* 内部写 API

第一版只开放 Level 0/1。

### 11.2 沙盒边界

* workspace root 白名单
* tmp 目录隔离
* 进程超时
* stdout/stderr 大小限制
* 网络白名单
* 环境变量白名单
* 命令 denylist

### 11.3 hooks 映射到 Go

把 Anthropic hooks 概念落成你的 event interception：

* `PreToolUse`
* `PostToolUse`
* `ToolFailure`
* `PreCompact`
* `SubagentStart`
* `SubagentStop`
* `SessionStart`
* `SessionEnd`

官方 hooks 的示例本来就包括：阻止危险 shell、记录文件变更、跟踪子代理、在 compact 前归档。([Claude开发平台][6])

---

## 12. MCP Gateway：不要直连模型，要先 host 化

MCP 规范的架构是 **host-client-server**。host 负责创建多个 client、控制连接权限和生命周期、做安全边界和上下文聚合；每个 client 与某个 server 维持 **1:1 的隔离连接**；协议本身是有状态 session，并要求 `initialize` 是首个交互。([模型上下文协议][9])

所以你的 MCP Gateway 最少要做 8 件事：

1. 维护 `mcp_servers` 配置
2. 对每个 server 建独立 client session
3. 执行 `initialize -> initialized` 生命周期
4. 同步 `tools/resources/prompts` 到本地 catalog
5. 根据当前任务做 tool search
6. `call_tool` 的原始结果先写 artifact，再 reduce
7. 处理 progress / cancel / reconnect
8. 统一做 auth、origin、roots 和审计

### 12.1 MCP session 初始化

```text
connect
-> initialize
-> server capabilities
-> initialized notification
-> list_tools / list_resources / list_prompts
-> local cache
```

### 12.2 roots 的使用

MCP 的 roots 机制本来就是让客户端暴露文件系统边界，告诉 server 它能操作哪些目录。把 workspace root 映射到 roots，是最自然的做法。([模型上下文协议][10])

### 12.3 HTTP transport 注意点

若你接远端 Streamable HTTP MCP server，要做两件事：

* 客户端支持 POST/GET + SSE 读取
* 服务端必须校验 `Origin` 以防 DNS rebinding；你自己的 gateway 也应做对应域名白名单和来源控制。([模型上下文协议][11])

### 12.4 不建议第一版上 MCP tasks

MCP `tasks` 是 2025-11-25 版本引入、目前仍标注为 experimental。第一版只做普通同步 `tools/call` + progress/cancel 足够，第二版再引入 deferred result retrieval。([模型上下文协议][12])

---

## 13. Tool Search：一定要自己做客户端版本

官方文档已经把这件事说得很直接：工具多起来后，光工具定义就会迅速吃掉上下文；典型多 server 场景下，定义可能先占掉大约 55K tokens，而 tool search 往往能减少 85% 以上，只加载 3–5 个真正需要的工具。官方还明确写了，**你可以自己实现 client-side tool search**。([Claude开发平台][13])

### 13.1 本地工具目录表

```sql
CREATE TABLE tool_catalog (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,          -- local/mcp/internal
  server_name TEXT,
  tool_name TEXT NOT NULL,
  description TEXT NOT NULL,
  input_schema_json TEXT NOT NULL,
  arg_names_json TEXT NOT NULL,
  tags_json TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL
);
```

### 13.2 检索策略

第一版先不用向量：

* BM25 on `tool_name + description + arg_names + tags`
* 再加规则过滤：当前任务角色、允许源、风险级别、资源域
* 最后只把 top 3~5 的 schema 注入当前 turn

### 13.3 工具加载策略

* session 启动：只加载基础工具
* 遇到明确需求：搜索候选工具
* 选中后：把 schema 注入当前请求
* 下一轮若不再需要：从 hot context 中移除

---

## 14. 观测和评估，第一版就要做

### 14.1 事件流

至少记录这些事件：

* `session.started`
* `turn.started`
* `turn.tokens_counted`
* `tool.requested`
* `tool.completed`
* `tool.reduced`
* `context.compacted`
* `recall.performed`
* `subagent.started`
* `subagent.completed`
* `policy.denied`
* `session.completed`
* `session.failed`

### 14.2 每个任务都打 8 个指标

* 总 turn 数
* 总输入/输出 tokens
* raw tool bytes
* admitted tool tokens
* compaction 次数
* recall 次数
* subagent 并发收益
* 完成/失败原因

### 14.3 必做 eval 集

做 20~30 个固定任务，分成 5 类：

1. repo 搜索与定位
2. 失败测试排查
3. 大日志诊断
4. Playwright UI 失败定位
5. 多子任务综合问答

你最该盯的两个 KPI：

* `compression_ratio = raw_bytes / admitted_tokens`
* `continuation_rate = 无需重开 session 的任务完成率`

---

## 15. 分阶段实施，按 8 周推进

### P0：第 1~2 周

目标：单代理 loop + SQLite 持久化

做完：

* `messages.create`
* `count_tokens`
* `MessageBuilder`
* `sessions/messages/events`
* 三个只读工具

### P1：第 3~4 周

目标：Output Gateway + Artifact Store + 3 个 reducer

做完：

* `artifacts`
* `artifact_chunks + FTS5`
* `git_log_reducer`
* `go_test_json_reducer`
* `playwright_snapshot_reducer`

### P2：第 5 周

目标：Context Manager

做完：

* Hot/Warm/Cold
* compaction
* decision ledger
* recall

### P3：第 6 周

目标：read-only subagents

做完：

* `spawn_subagents`
* child sessions
* `SubagentReport`
* 并发调度

### P4：第 7~8 周

目标：MCP Gateway + tool search

做完：

* MCP lifecycle
* local tool catalog
* client-side tool search
* MCP output reduction

---

## 16. 第一版最容易踩的坑

**坑 1：把 transcript 当真相源。**
解决：让 `memory_entries + checkpoints + artifacts` 成为真相源，transcript 只做最近几轮交互缓存。

**坑 2：让工具原始输出直接回模型。**
解决：没有 reducer 的工具，默认不准进入上下文。

**坑 3：多个子代理同时写代码。**
解决：第一版只有 readers 和 verifiers，没有 writer。

**坑 4：MCP 直通。**
解决：MCP 只进 gateway，不进模型。

**坑 5：没有 token headroom。**
解决：强制预留 20% headroom，避免下一轮工具结果塞不进去。

---

## 17. 现在就可以开始写的 5 个文件

先从这 5 个文件起步，别再扩散：

1. `internal/model/anthropic/client.go`
2. `internal/runtime/loop/runner.go`
3. `internal/context/manager.go`
4. `internal/tools/reducers/git_log.go`
5. `internal/store/sqlite/schema.sql`

最先打通的一条主链是：

```text
user -> session -> build request -> count tokens -> messages.create
-> tool_use -> local tool -> artifact store -> reducer -> envelope
-> tool_result -> next turn -> final answer
```

这条链一旦稳定，后面的 recall、subagents、MCP 都只是往 runtime 两边加模块，不会推翻核心结构。

一句话收束：**你要做的不是“Claude Code 的 Go 复刻”，而是一个带 Context OS、Tool Gateway 和 Subagent Scheduler 的 Agent Runtime。** 下一步最值得先产出的，是 `schema.sql`、`Go 接口骨架` 和 `3 个 reducer` 的首版实现。

[1]: https://platform.claude.com/docs/en/build-with-claude/working-with-messages "Using the Messages API - Claude API Docs"
[2]: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents "Effective context engineering for AI agents \ Anthropic"
[3]: https://platform.claude.com/docs/en/agent-sdk/agent-loop "How the agent loop works - Claude API Docs"
[4]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use "How to implement tool use - Claude API Docs"
[5]: https://platform.claude.com/docs/en/agent-sdk/subagents "Subagents in the SDK - Claude API Docs"
[6]: https://platform.claude.com/docs/zh-CN/agent-sdk/hooks "使用钩子拦截和控制代理行为 - Claude API Docs"
[7]: https://platform.claude.com/docs/en/build-with-claude/token-counting "Token counting - Claude API Docs"
[8]: https://www.anthropic.com/engineering/multi-agent-research-system "How we built our multi-agent research system \ Anthropic"
[9]: https://modelcontextprotocol.io/specification/2025-11-25/architecture?utm_source=chatgpt.com "Architecture - Model Context Protocol"
[10]: https://modelcontextprotocol.io/specification/2025-11-25/client/roots?utm_source=chatgpt.com "Roots - Model Context Protocol"
[11]: https://modelcontextprotocol.io/specification/2025-11-25/basic/transports?utm_source=chatgpt.com "Transports - Model Context Protocol"
[12]: https://modelcontextprotocol.io/specification/draft/basic/utilities/tasks?utm_source=chatgpt.com "Tasks - Model Context Protocol"
[13]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool "Tool search tool - Claude API Docs"
