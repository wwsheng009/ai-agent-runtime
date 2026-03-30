好，下面直接进入**可开工版本**。这一步不再讲“为什么”，只讲“怎么搭第一套能跑的东西”。

先把实现约束钉死：

* Claude 的 Messages API 是**无状态**的，你需要自己构造每一轮请求、管理会话历史、维护工具循环。Anthropic 还明确写了：你要自己构造每个 turn、管理 conversation state、写自己的 tool loop。([Claude开发平台][1])
* `count_tokens` 可以在真正发请求前估算 token，而且会把 system、tools、images、documents 都算进去，所以它应该成为 `BuildRequest()` 的固定前置步骤。([Claude开发平台][2])
* 如果你做自定义工具循环，`tool_use -> tool_result` 的消息顺序必须严格满足官方要求：`tool_result` 必须紧跟对应的 `tool_use`，而且在该 user 消息里要排在最前，否则会报错。([Claude开发平台][3])
* subagent 的正确语义不是“共享对话的第二个机器人”，而是**fresh conversation 的独立 agent**；其中间工具调用和结果留在子上下文里，父级只拿最终消息。([Claude开发平台][4])
* 工具多起来时，不要把全部工具 schema 直接塞进上下文。Anthropic 官方文档给出的经验是：大工具集会快速吃掉 context，tool search 常常能把这部分削掉 85% 以上，并把候选控制在 3–5 个。([Claude开发平台][5])
* MCP 是 **host-client-server** 架构，host 给每个 server 建一个独立 client；协议基于 JSON-RPC，常见 transport 是 stdio 和 Streamable HTTP，初始化是生命周期第一步。([模型上下文协议][6])

---

# 1. 第一版就按这套目录建项目

```text
agent-runtime/
├── cmd/
│   └── agentd/
│       └── main.go
├── internal/
│   ├── api/
│   │   ├── server.go
│   │   └── handlers.go
│   ├── runtime/
│   │   ├── runner.go
│   │   ├── types.go
│   │   └── message_builder.go
│   ├── model/
│   │   └── anthropic/
│   │       ├── client.go
│   │       ├── dto.go
│   │       └── mapper.go
│   ├── contextmgr/
│   │   ├── manager.go
│   │   ├── compact.go
│   │   ├── recall.go
│   │   └── budget.go
│   ├── scheduler/
│   │   ├── scheduler.go
│   │   └── child_runner.go
│   ├── tools/
│   │   ├── broker.go
│   │   ├── registry.go
│   │   ├── local/
│   │   │   ├── read_file.go
│   │   │   ├── grep_repo.go
│   │   │   ├── git_log.go
│   │   │   └── run_command.go
│   │   ├── reducers/
│   │   │   ├── reducer.go
│   │   │   ├── git_log.go
│   │   │   ├── go_test_json.go
│   │   │   └── playwright_snapshot.go
│   │   └── mcp/
│   │       ├── gateway.go
│   │       ├── client.go
│   │       └── catalog.go
│   ├── artifact/
│   │   ├── store.go
│   │   ├── chunker.go
│   │   └── types.go
│   ├── store/
│   │   └── sqlite/
│   │       ├── schema.sql
│   │       ├── sessions.go
│   │       ├── messages.go
│   │       ├── artifacts.go
│   │       ├── memory.go
│   │       └── events.go
│   ├── policy/
│   │   ├── engine.go
│   │   └── rules.go
│   └── events/
│       ├── bus.go
│       └── types.go
└── go.mod
```

这版结构是“**模块化单体**”。先别拆服务。你最先要跑通的是：

`api -> runtime -> model/tool/context -> sqlite`

---

# 2. 先定 6 个核心接口

这 6 个接口一旦稳定，后面的 reducer、MCP、subagent 都只是挂件。

```go
package runtime

import "context"

type ModelClient interface {
	CountTokens(ctx context.Context, req MessageRequest) (int, error)
	CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
}

type ContextManager interface {
	BuildRequest(ctx context.Context, s *Session) (MessageRequest, error)
	AdmitToolEnvelope(ctx context.Context, s *Session, env ToolEnvelope) error
	AdmitSubagentReport(ctx context.Context, s *Session, rep SubagentReport) error
	AdmitAssistantText(ctx context.Context, s *Session, text string) error
	Compact(ctx context.Context, s *Session, reason string) error
}

type ToolBroker interface {
	Exec(ctx context.Context, call ToolCall) (ToolRawResult, error)
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
}
```

---

# 3. 先把内部数据契约定死

## 3.1 Session / Task

```go
package runtime

import "time"

type Session struct {
	ID         string
	UserID     string
	RootTaskID string
	Model      string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Task struct {
	ID              string
	SessionID       string
	ParentTaskID    *string
	Role            string
	Goal            string
	SuccessCriteria []string
	AllowedTools    []string
	Status          string
	BudgetTokens    int
	BudgetUSD       float64
	MaxTurns        int
}
```

## 3.2 MessageRequest / Response

你的内部结构不要一开始就绑定 Anthropic JSON。

```go
type MessageRequest struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	Metadata  map[string]string
}

type Message struct {
	Role    string // system/user/assistant
	Content []ContentBlock
}

type ContentBlock struct {
	Type string // text/tool_use/tool_result/image/document
	Text string

	ToolUseID string
	Name      string
	Input     map[string]any
	Result    any
}

type MessageResponse struct {
	ID           string
	StopReason   string
	InputTokens  int
	OutputTokens int
	Content      []ContentBlock
}
```

## 3.3 Tool Raw / Envelope

这是 Context Mode 的核心契约。

```go
type ToolCall struct {
	Name  string
	Input map[string]any
}

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

type ArtifactRef struct {
	ID   string
	Type string
	Path string
}

type ToolEnvelope struct {
	Summary       string
	KeyFacts      []string
	Warnings      []string
	ArtifactRefs  []ArtifactRef
	RecallHints   []string
	SuggestedNext []string
	IsError       bool
}
```

## 3.4 Subagent 协议

```go
type TaskPacket struct {
	Name            string
	Goal            string
	SuccessCriteria []string
	AllowedTools    []string
	ArtifactRefs    []ArtifactRef
	MaxTurns        int
	BudgetTokens    int
	BudgetUSD       float64
	ReportSchema    string
}

type SubagentReport struct {
	TaskName      string
	Status        string
	Summary       string
	Findings      []string
	EvidenceRefs  []ArtifactRef
	OpenQuestions []string
	SuggestedNext []string
	CostTokens    int
	DurationMs    int64
}
```

---

# 4. 第一版时序图：只做单代理 + 工具网关

这条链必须先跑稳。

```text
用户请求
 -> 创建 Session / Task
 -> ContextManager.BuildRequest()
 -> CountTokens()
 -> 如超阈值则 Compact()
 -> CreateMessage()
 -> assistant 返回 text 或 tool_use
 -> 如果 tool_use:
      ToolBroker.Exec()
      ArtifactStore.PutRaw()
      Reducer.Reduce()
      ContextManager.AdmitToolEnvelope()
      MessageBuilder 生成 tool_result
      再次 CreateMessage()
 -> 直到 final text
 -> 写 Session completed
```

### 这条链里最关键的 3 个点

1. **发模型前一定 count tokens**
2. **工具原始输出一定先落 artifact**
3. **进入上下文的一定是 envelope，不是 raw stdout**

---

# 5. `runner.go` 直接按这个写

```go
package runtime

import "context"

type Runner struct {
	Model    ModelClient
	Context  ContextManager
	Broker   ToolBroker
	Artifacts ArtifactStore
	Sched    Scheduler
	Policy   PolicyEngine
}

func (r *Runner) RunTurn(ctx context.Context, s *Session) error {
	req, err := r.Context.BuildRequest(ctx, s)
	if err != nil {
		return err
	}

	n, err := r.Model.CountTokens(ctx, req)
	if err != nil {
		return err
	}

	if n > 85000 { // 示例阈值，按你的模型再配
		if err := r.Context.Compact(ctx, s, "token_threshold"); err != nil {
			return err
		}
		req, err = r.Context.BuildRequest(ctx, s)
		if err != nil {
			return err
		}
	}

	resp, err := r.Model.CreateMessage(ctx, req)
	if err != nil {
		return err
	}

	toolCalls := extractToolCalls(resp.Content)

	// 没有工具调用，直接收最终文本
	if len(toolCalls) == 0 {
		text := extractText(resp.Content)
		return r.Context.AdmitAssistantText(ctx, s, text)
	}

	for _, tc := range toolCalls {
		// 子代理委派
		if tc.Name == "spawn_subagents" {
			tasks := decodeTaskPackets(tc.Input)
			reports, err := r.Sched.RunChildren(ctx, s, tasks)
			if err != nil {
				return err
			}
			for _, rep := range reports {
				if err := r.Context.AdmitSubagentReport(ctx, s, rep); err != nil {
					return err
				}
			}
			continue
		}

		if err := r.Policy.AllowTool(ctx, tc); err != nil {
			env := ToolEnvelope{
				Summary:  "Tool call denied by policy",
				Warnings: []string{err.Error()},
				IsError:  true,
			}
			if err := r.Context.AdmitToolEnvelope(ctx, s, env); err != nil {
				return err
			}
			continue
		}

		raw, err := r.Broker.Exec(ctx, tc)
		if err != nil {
			raw = ToolRawResult{
				ToolName: tc.Name,
				ExitCode: 1,
				Stderr:   []byte(err.Error()),
			}
		}

		refs, err := r.Artifacts.PutRaw(ctx, raw)
		if err != nil {
			return err
		}

		env, err := DefaultReduce(ctx, raw, refs)
		if err != nil {
			return err
		}

		if err := r.Context.AdmitToolEnvelope(ctx, s, env); err != nil {
			return err
		}
	}

	return nil
}
```

---

# 6. `ContextManager` 的第一版实现思路

第一版不要做得太聪明，只做**硬预算 + 三层存储**。

## 6.1 三层上下文

### Hot Context

真正送给模型的内容：

* system prompt
* 当前 task 目标
* 最近 2 到 4 轮对话
* 当前 plan
* 高优先级 decision ledger
* 最近 3 到 6 个 ToolEnvelope
* 当前 turn 需要的少量工具定义

### Warm Memory

不总是发：

* 已确认事实
* 历史决策
* 子代理结论
* 失败尝试
* 已关闭问题

### Cold Store

永不直接发：

* Playwright snapshot
* 长日志
* `git log` 原文
* 测试 JSON
* MCP 原始返回
* 大文档全文

Anthropic 官方文档把长对话/智能体工作流里的主要上下文管理策略指向**服务器端压缩**，并强调上下文窗口会随着轮次线性累积。([Claude开发平台][7])

## 6.2 第一版预算

```go
type Budget struct {
	MaxInputTokens int
	SystemPct      int
	TaskPct        int
	LedgerPct      int
	RecentPct      int
	EvidencePct    int
	ToolsPct       int
	HeadroomPct    int
}
```

推荐：

```go
Budget{
	MaxInputTokens: 100000,
	SystemPct:      10,
	TaskPct:        10,
	LedgerPct:      15,
	RecentPct:      15,
	EvidencePct:    20,
	ToolsPct:       10,
	HeadroomPct:    20,
}
```

### 触发规则

* 超过 70%：标记 `compact_pending`
* 超过 85%：发送前必须 compact
* 超过 92%：禁止新注入大证据，只允许摘要
* 超过 97%：禁止再派生子代理

## 6.3 `BuildRequest()` 伪代码

```go
func (m *Manager) BuildRequest(ctx context.Context, s *Session) (MessageRequest, error) {
	req := MessageRequest{
		Model:     s.Model,
		System:    m.buildSystemPrompt(s),
		MaxTokens: 4096,
	}

	req.Messages = append(req.Messages, m.buildTaskMessage(s))
	req.Messages = append(req.Messages, m.loadRecentTurns(s, 4)...)
	req.Messages = append(req.Messages, m.loadLedgerMessages(s, 12)...)
	req.Messages = append(req.Messages, m.loadRecentEvidence(s, 6)...)

	req.Tools = m.selectToolsForTurn(s)

	return req, nil
}
```

## 6.4 `Compact()` 第一版怎么做

不要总结成一大段 prose。压成结构化 ledger。

```go
type MemoryEntry struct {
	ID         string
	SessionID  string
	TaskID     string
	Kind       string // decision/fact/open_question/plan/failure/child_summary
	Priority   int
	Content    map[string]any
	SourceRefs []string
}
```

压缩规则：

* 把旧 turn 提炼成 `decision`
* 把验证过的信息提炼成 `fact`
* 把没解决的提炼成 `open_question`
* 把最近 plan 提炼成 `plan`
* 把失败路径提炼成 `failure`

---

# 7. 先给你最小 SQLite DDL

`schema.sql` 第一版这样建就够用了：

```sql
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  root_task_id TEXT,
  model TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE tasks (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  parent_task_id TEXT,
  role TEXT NOT NULL,
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
  role TEXT NOT NULL,
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
  artifact_type TEXT NOT NULL,
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
  kind TEXT NOT NULL,
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

### 为什么一定要 `artifact_chunks_fts`

因为你的 recall 不是“重放 transcript”，而是“按需找片段”。

---

# 8. Reducer 体系，第一版只做 3 个

## 8.1 接口

```go
package reducers

import "context"

type Reducer interface {
	Name() string
	Match(raw ToolRawResult) bool
	Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error)
}
```

## 8.2 `git_log_reducer`

```go
func (r *GitLogReducer) Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error) {
	lines := strings.Split(string(raw.Stdout), "\n")

	commits := 0
	authors := map[string]int{}
	paths := map[string]int{}
	reverts := 0

	for _, ln := range lines {
		if strings.HasPrefix(ln, "commit ") {
			commits++
		}
		if strings.HasPrefix(ln, "Author: ") {
			authors[strings.TrimPrefix(ln, "Author: ")]++
		}
		if strings.Contains(strings.ToLower(ln), "revert") {
			reverts++
		}
		if strings.Contains(ln, "/") && strings.Contains(ln, ".go") {
			paths[ln]++
		}
	}

	return ToolEnvelope{
		Summary: fmt.Sprintf("Parsed git history: %d commits, %d revert-like entries", commits, reverts),
		KeyFacts: []string{
			fmt.Sprintf("commit_count=%d", commits),
			fmt.Sprintf("revert_like=%d", reverts),
		},
		ArtifactRefs: refs,
		RecallHints: []string{
			"recent commits touching auth",
			"revert commits",
			"top modified paths",
		},
	}, nil
}
```

## 8.3 `go_test_json_reducer`

目标抽取：

* 失败 package
* 失败测试名
* panic 入口
* 首个高价值错误
* 重现命令

## 8.4 `playwright_snapshot_reducer`

目标抽取：

* URL
* title
* console error
* failed network requests
* visible text top-N
* screenshot ref

### 这里的原则

**Reducer 不调用 LLM。**
它就是纯规则、纯程序、纯摘要。

---

# 9. Recall 先做成简单版

FTS5 先够用，不要一上来上向量库。

```go
type RecallQuery struct {
	SessionID     string
	TaskID        string
	ToolNames     []string
	ArtifactTypes []string
	Query         string
	K             int
}

type ArtifactSnippet struct {
	ArtifactID string
	ChunkNo    int
	Text       string
	Path       string
	Score      float64
}
```

### SQL 思路

```sql
SELECT
  c.artifact_id,
  c.chunk_no,
  c.chunk_text,
  c.path,
  bm25(artifact_chunks_fts) AS score
FROM artifact_chunks_fts
JOIN artifact_chunks c ON artifact_chunks_fts.rowid = c.id
WHERE artifact_chunks_fts MATCH ?
ORDER BY score
LIMIT ?;
```

### Page-in 规则

召回回来的不是原文，而是新的 snippet envelope：

```go
type SnippetEnvelope struct {
	Summary      string
	Snippets     []string
	ArtifactRefs []ArtifactRef
}
```

---

# 10. P0 不做真正 subagent，只预留协议

先把 `spawn_subagents` 当内部特殊工具。

```go
type SpawnSubagentsInput struct {
	Tasks []TaskPacket `json:"tasks"`
}
```

第一版 scheduler 可以先做“串行假并发”，也就是逻辑上走 child session，物理上顺序执行。等单代理稳定，再切 goroutine 并发。

### 父子协同的硬约束

* child 用 fresh context
* child 不继承父的完整 transcript
* child 只拿 task packet + artifact refs +必要记忆
* parent 只接收 `SubagentReport`

这正是官方 subagent 设计强调的点。([Claude开发平台][4])

---

# 11. MCP Gateway 第一版先留壳，不接全量

MCP 第一版只做这 4 个动作：

1. `initialize`
2. `list_tools`
3. `call_tool`
4. `list_resources`

MCP 的 host/client/server 关系、1:1 client-server 连接、JSON-RPC 基础、以及 stdio / Streamable HTTP 两种主 transport，都应该被你封进 gateway，不要暴露给 runtime。([模型上下文协议][6])

### gateway 的核心接口

```go
type MCPGateway interface {
	Initialize(ctx context.Context, serverName string) error
	ListTools(ctx context.Context, serverName string) ([]ToolDef, error)
	CallTool(ctx context.Context, serverName, toolName string, input map[string]any) (ToolRawResult, error)
	ListResources(ctx context.Context, serverName string) ([]string, error)
}
```

### 一个重要的安全点

如果你走 Streamable HTTP，MCP 规范明确要求服务端校验 `Origin`，本地运行建议只绑定 localhost。你的 gateway 也应该沿用这个约束。([模型上下文协议][8])

---

# 12. Tool Search 现在就要留口子

哪怕 P0 只有 5 个工具，接口也先设计好。因为一旦你后面接 MCP，工具数量会膨胀得非常快；官方文档明确建议工具多时做按需发现，且支持 client-side 实现。([Claude开发平台][5])

```go
type ToolCatalog interface {
	UpsertTool(ctx context.Context, t ToolDef) error
	Search(ctx context.Context, query string, limit int) ([]ToolDef, error)
}
```

### 第一版检索就用 BM25

检索字段：

* tool name
* description
* arg names
* arg descriptions
* tags

### 加载策略

* 常用 3~5 个工具常驻
* 其他工具按需搜索再注入

---

# 13. API 层先做这 5 个接口

```text
POST   /sessions
POST   /sessions/{id}/messages
GET    /sessions/{id}
GET    /sessions/{id}/events
GET    /sessions/{id}/artifacts
```

### 典型流程

## 创建 session

```json
POST /sessions
{
  "user_id": "u_001",
  "model": "claude-sonnet",
  "goal": "定位 auth 回归来源"
}
```

## 发一条用户消息

```json
POST /sessions/s_001/messages
{
  "content": "请先分析最近一周 auth 模块的改动并指出可疑提交"
}
```

### handler 里做的事

* 记录 user message
* 调 `runner.RunTurn()`
* 如返回 tool_use，继续 loop
* 直到没有 tool_use
* streaming 给前端

---

# 14. 第一周就要把 event bus 打上

你后面调试 90% 都靠它。

```go
type Event struct {
	ID        string
	SessionID string
	TaskID    string
	Type      string
	Payload   map[string]any
	CreatedAt time.Time
}
```

至少记录这些事件：

```text
session.started
turn.started
turn.tokens_counted
context.compact.started
context.compact.completed
tool.requested
tool.completed
tool.reduced
artifact.stored
recall.performed
policy.denied
session.completed
session.failed
```

---

# 15. 首个端到端场景，就做这个

不要一开始做“自动修 bug”。先做一个**只读排障场景**：

## 任务

“分析最近一周 auth 模块的提交，结合测试结果，指出最可疑的 3 个提交并给出证据。”

## 只开这些工具

* `git_log`
* `git_show`
* `grep_repo`
* `read_file`
* `run_command_readonly`

## 预期链路

1. 用户发起任务
2. 模型先调用 `git_log`
3. raw log 落 artifact
4. `git_log_reducer` 返回摘要
5. 模型再调用 `grep_repo` / `git_show`
6. 可能再调用 `run_command_readonly` 跑测试
7. 最终输出“可疑提交 + 文件 + 原因 + artifact 引用”

### 这个场景的价值

* 有长输出
* 有多轮工具
* 有 recall 需求
* 但没有写操作风险

---

# 16. 你这周真正要写的文件清单

按顺序，不要并行乱开：

### 第 1 天

* `internal/model/anthropic/client.go`
* `internal/runtime/types.go`
* `internal/store/sqlite/schema.sql`

### 第 2 天

* `internal/runtime/runner.go`
* `internal/runtime/message_builder.go`
* `internal/api/handlers.go`

### 第 3 天

* `internal/tools/local/git_log.go`
* `internal/tools/reducers/git_log.go`
* `internal/artifact/store.go`

### 第 4 天

* `internal/contextmgr/manager.go`
* `internal/contextmgr/budget.go`
* `internal/events/bus.go`

### 第 5 天

* 跑通“只读排障”端到端
* 打日志
* 修消息顺序 bug
* 补回放

---

# 17. 第一版验收标准

不是“看起来聪明”，而是满足这 7 条：

1. 单任务能稳定跑 10+ 轮
2. `tool_use -> tool_result` 顺序永远合法
3. 所有大工具输出先落 artifact
4. 进入上下文的内容始终是 envelope
5. 超预算时能 compact，而不是崩
6. 所有 turn、tool、artifact 都可追踪
7. 同一个 session 可以继续，而不是每次都重开

---

# 18. 下一步最值得直接产出的东西

现在最划算的不是继续聊概念，而是把这 3 个文件一次性写出来：

1. `schema.sql`
2. `runtime/types.go`
3. `runner.go`

把这三个写完，系统骨架就定了。

下一条我可以直接给你：
**`schema.sql` 完整版 + `types.go` 完整版 + `runner.go` 的可编译骨架**。

[1]: https://platform.claude.com/docs/zh-CN/build-with-claude/working-with-messages?utm_source=chatgpt.com "使用 Messages API - Claude API Docs"
[2]: https://platform.claude.com/docs/en/build-with-claude/token-counting "Token counting - Claude API Docs"
[3]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use "How to implement tool use - Claude API Docs"
[4]: https://platform.claude.com/docs/en/agent-sdk/subagents "Subagents in the SDK - Claude API Docs"
[5]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool "Tool search tool - Claude API Docs"
[6]: https://modelcontextprotocol.io/docs/learn/architecture "Architecture overview - Model Context Protocol"
[7]: https://platform.claude.com/docs/zh-CN/build-with-claude/context-windows "上下文窗口 - Claude API Docs"
[8]: https://modelcontextprotocol.io/specification/2025-11-25/basic/transports "Transports - Model Context Protocol"
