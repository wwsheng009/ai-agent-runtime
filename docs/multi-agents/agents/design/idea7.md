下面这版直接落到“可以拆给团队写代码”的程度。

## 1. 先把最终目标定成一个 Runtime，而不是一个聊天壳

你要做的系统，本质上是一个 **Agent Runtime**。原因很直接：Claude 的 Messages API 是**无状态**的，多轮对话必须由你自己每轮重发历史；Token Count API 又支持对和消息创建相同结构的输入做预估，包括 system、tools、images、PDFs，所以“本轮到底给模型什么”必须变成一个明确的系统职责，而不是散落在 prompt 拼接里。Claude 的 agent loop 官方定义也很清楚：接收 prompt，Claude 决策，可能请求工具，宿主执行工具并收集结果，再继续循环直到没有工具调用。([Claude开发平台][1])

所以首版应当长这样：

```text
API/CLI/IDE
   -> Session Gateway
      -> Runtime
         -> ContextManager
         -> ModelAdapter
         -> ToolBroker
         -> OutputGateway
         -> ArtifactStore
         -> Scheduler
         -> Policy/Hooks
```

不是先做多个 agent 群聊，而是先做一个**能稳定管理上下文、工具与状态**的执行内核。Anthropic 在 context engineering 里把核心问题定义为：长时任务不是单轮推理问题，而是系统要持续管理 context state；他们给出的主要手段就是 compaction、structured note-taking 和 sub-agent architectures。([Anthropic][2])

---

## 2. 首版只做这 4 个能力，别一开始铺太大

首版目标建议收敛到：

1. 单代理能稳定跑 20 到 50 轮。
2. 任何大工具输出都不直接进入模型上下文。
3. 工具原始结果全部外置，并可检索召回。
4. 为 subagent 预留协议，但先只做 read-only 子代理。

这是因为工具调用的原始中间结果会快速污染上下文；Anthropic 官方在 Advanced Tool Use 里直接把“context pollution from intermediate results”列为传统工具调用的核心问题之一。Tool Search 也是为了解决“工具定义先把上下文吃掉”的问题：官方文档给出的典型多 server 场景里，工具定义本身就可能占用约 55K tokens，而 tool search 往往能减少 85% 以上，只加载 3–5 个真正需要的工具。([Anthropic][3])

---

## 3. 参考架构：控制面与数据面分离

### 3.1 控制面

控制面负责“决策”和“状态推进”：

* `SessionGateway`：收用户请求、恢复会话、启动 turn loop
* `Runtime`：总控单轮与多轮执行
* `ContextManager`：构建本轮请求、预算、压缩、召回
* `Scheduler`：子任务拆分、子代理调度
* `Policy/Hooks`：拦截危险操作、审计、审批

### 3.2 数据面

数据面负责“原始数据”和“记忆”：

* `ArtifactStore`：保存大输出、截图、日志、HTML、JSON
* `SQLite + FTS5`：索引 artifact chunk，支持按需召回
* `Memory/Ledger`：保存决策、事实、开放问题、计划、失败路径
* `Checkpoints`：保存跨 context window 的恢复点

这和 Anthropic 推荐的思路对得很齐：会话接近 context 上限时做 compaction；把关键信息以结构化方式存到窗口之外；让主代理维持高层计划，把细粒度搜索隔离在子代理里。([Anthropic][2])

---

## 4. 关键状态机，要先定义清楚

### 4.1 Session 状态机

```text
new -> running -> waiting_input -> running -> completed
                 \-> failed
                 \-> cancelled
```

### 4.2 Task 状态机

```text
queued -> running -> succeeded
                \-> failed
                \-> timed_out
                \-> cancelled
```

### 4.3 Turn 状态机

```text
build_request
 -> count_tokens
 -> maybe_compact
 -> call_model
 -> parse_response
 -> execute_tools?
 -> reduce_outputs
 -> admit_to_context
 -> next_turn / done
```

### 4.4 Tool 状态机

```text
requested -> policy_check -> executing -> raw_stored -> reduced -> admitted
```

### 4.5 Child Task 状态机

```text
delegated -> child_session_started -> child_running -> child_reported -> merged
```

这样做的好处是：后面无论是审计、回放、重试、超时处理，还是 UI 展示，都不用去猜“系统现在在哪一步”。

---

## 5. 先把 Go 内部契约定死

下面这些类型一旦稳定，后面的代码会顺很多。

### 5.1 Runtime 基础类型

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
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
```

### 5.2 模型请求与响应

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
	Role    string // user / assistant
	Content []ContentBlock
}

type ContentBlock struct {
	Type string // text / tool_use / tool_result / image / document
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

### 5.3 工具调用契约

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

### 5.4 子代理协议

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

子代理这里一定坚持一个原则：**父级只消费 `SubagentReport`，不消费 child transcript**。Anthropic 对 subagents 的官方定义就是“fresh conversation 的独立 agent”，其中间工具调用与结果留在子上下文中，主对话只拿最终结果。([Anthropic][2])

---

## 6. 首版 6 个接口，先不要再多

```go
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

## 7. Model Adapter：不用官方 Go Agent SDK 也完全能做

Claude API 本身就是 REST，主接口是 `POST /v1/messages`，配套还有 `POST /v1/messages/count_tokens`。Messages API 是主入口，count_tokens 接受与消息创建相同的结构化输入。([Claude开发平台][4])

首版 `client.go` 可以直接这样写：

```go
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	APIKey    string
	BaseURL   string
	Version   string
	HTTP      *http.Client
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: "https://api.anthropic.com",
		Version: "2023-06-01",
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (c *Client) doJSON(ctx context.Context, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", c.Version)
	req.Header.Set("content-type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("anthropic status=%d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
```

然后实现：

```go
func (c *Client) CountTokens(ctx context.Context, req MessageRequest) (int, error)
func (c *Client) CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
```

这里的关键不是 HTTP，而是 `mapper.go`：把你内部的 `MessageRequest/MessageResponse` 映射到 Claude 的消息块格式。这样未来换模型、加兼容层都不痛。

---

## 8. `runner.go`：先把单轮到多轮跑通

这是系统主干。

```go
type Runner struct {
	Model     ModelClient
	Context   ContextManager
	Broker    ToolBroker
	Artifacts ArtifactStore
	Sched     Scheduler
	Policy    PolicyEngine
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

	if n > 85000 {
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
	if len(toolCalls) == 0 {
		return r.Context.AdmitAssistantText(ctx, s, extractText(resp.Content))
	}

	for _, tc := range toolCalls {
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

### 这里最关键的不是并发，而是消息顺序

Claude 的工具使用格式要求非常严格：`tool_result` 必须**紧跟**对应 `tool_use`，中间不能插其他消息；并且用户消息里的 `tool_result` 块必须排在最前，文本只能放在后面，否则会收到 400 错误。这个规则必须内置到 `message_builder.go`，不要靠调用方记忆。([Claude开发平台][5])

---

## 9. ContextManager：这是你的“Context OS”

### 9.1 三层上下文

#### Hot Context

真正送给模型的内容：

* system prompt
* 当前 task packet
* 当前计划
* 高优先级 ledger
* 最近 2 到 4 轮对话
* 最近少量 `ToolEnvelope`
* 本轮需要的工具定义

#### Warm Memory

默认不发给模型：

* 历史决策
* 已验证事实
* 失败尝试
* 子代理结论
* 已关闭的问题

#### Cold Store

永不直接进入模型：

* Playwright snapshot
* 长日志
* `git log` 原文
* `go test -json` 原文
* MCP 原始响应
* HTML/DOM/截图/大文档全文

Anthropic 的 context engineering 里明确把 compaction、structured note-taking、sub-agent architectures 列为跨长时间任务维持一致性的三大手段；他们还特别提到，工具调用和工具结果这类旧内容往往是最安全、最先可以清理的对象。([Anthropic][2])

### 9.2 BuildRequest 算法

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

### 9.3 预算策略

首版直接用固定预算，不要让模型自己猜：

* 10% system/policy
* 10% current task
* 15% ledger
* 15% recent turns
* 20% evidence
* 10% tools
* 20% headroom

触发规则：

* > 70%：标记 `compact_pending`
* > 85%：发送前强制 compact
* > 92%：禁止注入大证据，只允许摘要
* > 97%：禁止再派生 child

`count_tokens` 应该在每个 turn 发送前执行，因为它就是为“发送前估算消息 token、支持成本和模型路由决策”设计的，而且会把 tools、images、PDFs 一并算进去。([Claude开发平台][6])

### 9.4 Compact 不是写一段自然语言总结

正确做法是把旧内容压成结构化 ledger：

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

压缩目标：

* `decision[]`
* `fact[]`
* `open_question[]`
* `plan[]`
* `failure[]`

这样后续 recall、过滤、UI 展示和回放都更稳。

---

## 10. Output Gateway：把工具输出变成“信号”，不是“垃圾洪流”

这是你方案里最关键的一层。

### 10.1 原则

所有工具输出都走这条链：

```text
raw output
 -> classify
 -> store raw
 -> reduce
 -> envelope
 -> admit / reject
```

不要让任何原始 stdout/stderr/json/html/snapshot 直接回模型。Anthropic 在 Advanced Tool Use 里对传统工具调用的主要批评，就是中间结果把上下文塞爆；而在 context engineering 里，他们又把“清掉工具结果”列成最安全、最轻量的 compaction 之一。([Anthropic][3])

### 10.2 Reducer 插件接口

```go
type Reducer interface {
	Name() string
	Match(raw ToolRawResult) bool
	Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error)
}
```

### 10.3 第一批 reducer

先只做 3 个：

#### `git_log_reducer`

输出：

* commit 数
* 高频作者
* 高频目录
* revert/merge 检测
* 最近高相关 commit ref

#### `go_test_json_reducer`

输出：

* 失败包
* 失败测试
* panic/stack 入口
* 首个高价值错误
* 重现命令

#### `playwright_snapshot_reducer`

输出：

* URL/title
* visible text top-N
* console error
* failed requests
* screenshot ref
* selector hints

### 10.4 一个 reducer 示例

```go
func (r *GitLogReducer) Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error) {
	lines := strings.Split(string(raw.Stdout), "\n")

	commits := 0
	reverts := 0

	for _, ln := range lines {
		if strings.HasPrefix(ln, "commit ") {
			commits++
		}
		if strings.Contains(strings.ToLower(ln), "revert") {
			reverts++
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

---

## 11. Artifact Store + SQLite FTS5：把“完整数据”放到窗口外

### 11.1 必要表

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

### 11.2 Recall 接口

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

### 11.3 召回 SQL

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

### 11.4 Page-in 规则

召回回来的依旧不要直接塞原文，而是再做一次 snippet envelope：

```go
type SnippetEnvelope struct {
	Summary      string
	Snippets     []string
	ArtifactRefs []ArtifactRef
}
```

---

## 12. Scheduler：先做“主从协议”，再做并发

Anthropic 在多代理架构上强调的是**主代理保持高层计划，子代理带着干净上下文做深度工作，详细搜索上下文隔离在子代理里**。([Anthropic][2])

### 12.1 第一版角色

只保留：

* `root-planner`
* `repo-reader`
* `test-runner`
* `log-investigator`
* `writer`（后期再开）

第一版不要让 child 写文件。

### 12.2 调度原则

* many readers, single writer
* child-child 不直接通信
* parent 只消费 report
* child 深度先限制为 1
* 并发先限制为 3 或 4

### 12.3 Scheduler 接口

```go
type SchedulerConfig struct {
	MaxConcurrentChildren int
	MaxDepth              int
	MaxChildTurns         int
	ChildTimeout          time.Duration
}
```

### 12.4 运行逻辑

首版甚至可以先逻辑并发、物理串行：

```go
func (s *SchedulerImpl) RunChildren(
	ctx context.Context,
	parent *Session,
	tasks []TaskPacket,
) ([]SubagentReport, error) {
	reports := make([]SubagentReport, 0, len(tasks))
	for _, task := range tasks {
		rep, err := s.runChild(ctx, parent, task)
		if err != nil {
			return nil, err
		}
		reports = append(reports, rep)
	}
	return reports, nil
}
```

等单代理 loop 稳了，再改 goroutine + semaphore。

---

## 13. MCP Gateway：一定放在模型之前，不要 JSON-RPC 直通

MCP 最新规范明确是 **host-client-server** 架构：host 负责创建和管理多个 client，控制连接权限和生命周期，执行安全策略、用户授权决策，并做上下文聚合；每个 client 与一个 server 维持 **1:1 的隔离连接**。MCP 基于 JSON-RPC，是**有状态 session 协议**。初始化阶段必须是第一步：client 发送 `initialize`，server 返回能力与版本，随后 client 发送 `initialized`。([模型上下文协议][7])

### 13.1 这意味着什么

MCP 不该被你实现成：

```text
模型 -> 直接拿到 MCP 工具定义 -> call_tool -> 原始结果回模型
```

而应该是：

```text
Runtime -> MCP Gateway(host)
        -> per-server client session
        -> initialize/capability negotiation
        -> list tools/resources
        -> local tool catalog
        -> call_tool
        -> raw result -> OutputGateway -> envelope
        -> Runtime
```

### 13.2 Roots 要用起来

MCP roots 的作用就是定义服务器可以在哪些目录和文件上操作。规范里写得很明白：roots 定义了服务器可操作的文件系统边界，server 可以向 client 请求 roots 列表。把你的 workspace root 映射成 MCP roots，是最自然的沙盒边界。([模型上下文协议][8])

### 13.3 首版 MCP Gateway 接口

```go
type MCPGateway interface {
	Initialize(ctx context.Context, serverName string) error
	ListTools(ctx context.Context, serverName string) ([]ToolDef, error)
	CallTool(ctx context.Context, serverName, toolName string, input map[string]any) (ToolRawResult, error)
	ListResources(ctx context.Context, serverName string) ([]string, error)
}
```

### 13.4 对 HTTP transport 的注意点

MCP 传输层是 transport-agnostic 的，可以跑在任何支持双向消息交换的通道上，但必须保留 JSON-RPC 格式和生命周期要求；官方 transport 文档还特别提醒，本地运行的 server 应只绑定 localhost，并注意认证与 DNS rebinding 风险。([模型上下文协议][9])

### 13.5 Tasks 先不要急着全上

MCP `tasks` 是 2025-11-25 才引入，而且规范明确标注为 experimental。第一版把同步工具调用和标准生命周期做好就够了，第二版再用 task 做长任务和 deferred result retrieval。([模型上下文协议][10])

---

## 14. Tool Search：必须预留，不然一接 MCP 就爆

Anthropic 的 Tool Search 文档给出的结论非常明确：

* 工具定义会快速吃掉 context
* 超过 30–50 个工具后，工具选择准确率明显下降
* tool search 会按需只加载 3–5 个候选工具
* 这件事可以做成 **client-side** 实现 ([Claude开发平台][11])

所以你本地一定要维护一个 `tool_catalog`：

```sql
CREATE TABLE tool_catalog (
  id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
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

首版检索用 BM25 就够：

* tool name
* description
* arg names
* arg descriptions
* tags

本轮只注入 top 3 到 5 个工具定义。大部分工具用 `defer_loading` 思维，不常用的不进热上下文。Anthropic 还给出了对 MCP server 整体 defer loading 的模式。([Claude开发平台][11])

---

## 15. Policy/Hooks：不要把安全写在 prompt 里

官方 hooks 文档明确支持这些模式：

* 拦截危险操作
* 记录和审计每次工具调用
* 变换输入输出
* 对敏感动作要求人工批准
* 跟踪 session 生命周期 ([Claude开发平台][12])

所以你在 Go 里做成事件总线即可：

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

至少记录：

* `session.started`
* `turn.started`
* `turn.tokens_counted`
* `context.compact.started`
* `context.compact.completed`
* `tool.requested`
* `tool.completed`
* `tool.reduced`
* `artifact.stored`
* `recall.performed`
* `subagent.started`
* `subagent.completed`
* `policy.denied`
* `session.completed`
* `session.failed`

---

## 16. 首个可交付场景，不要做自动改代码

最适合的首个端到端场景是：

**“分析最近一周 auth 模块的提交，结合测试结果，指出最可疑的 3 个提交并给出证据。”**

只开放这些工具：

* `git_log`
* `git_show`
* `grep_repo`
* `read_file`
* `run_command_readonly`

预期链路：

1. 用户发起任务
2. 模型调用 `git_log`
3. raw log 落 artifact
4. `git_log_reducer` 产出 envelope
5. 模型再调用 `grep_repo` / `git_show`
6. 必要时 `run_command_readonly` 跑测试
7. 输出“可疑提交 + 文件路径 + 证据 ref + 下一步建议”

这个场景的优点是：有长输出、有多轮工具、有召回需求，但没有写操作风险。

---

## 17. 按周实施，最稳的顺序

### 第 1 周

* 建项目骨架
* 打通 `messages.create`
* 打通 `count_tokens`
* 建 SQLite 基础表
* 写最小 `Runner`

### 第 2 周

* 接 `ToolBroker`
* 做 `ArtifactStore`
* 写 `git_log_reducer`
* 跑通只读端到端

### 第 3 周

* 做 `ContextManager`
* 实现 Hot/Warm/Cold
* 实现 compaction 和 ledger

### 第 4 周

* 做 FTS5 recall
* 实现 snippet page-in
* 增加 `go_test_json_reducer` / `playwright_snapshot_reducer`

### 第 5 周

* 接 `Scheduler`
* 做 child session
* 只开放 read-only subagents

### 第 6 周

* 接 MCP Gateway
* 做 tool catalog
* 做 client-side tool search
* MCP 输出仍走 Output Gateway

---

## 18. 第一版验收标准

不是“看起来很像 Claude Code”，而是满足这 8 条：

1. 同一 session 能稳定跑 20+ 轮
2. 所有大工具输出都先落 artifact
3. 进入上下文的永远是 envelope，不是 raw output
4. 超预算时会 compact，而不是崩
5. recall 能找回历史证据片段
6. `tool_use -> tool_result` 顺序始终合法
7. child 是 fresh context，父级只吃 report
8. 全链路可回放、可审计

---

## 19. 现在最值得立刻开写的 4 个文件

按投入产出比排序：

1. `internal/store/sqlite/schema.sql`
2. `internal/runtime/types.go`
3. `internal/model/anthropic/client.go`
4. `internal/runtime/runner.go`

这 4 个一旦成型，整个系统主干就稳了。接下来最值钱的补件是 `git_log_reducer` 和 `ContextManager.BuildRequest()`。

[1]: https://platform.claude.com/docs/en/build-with-claude/working-with-messages "Using the Messages API - Claude API Docs"
[2]: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents "Effective context engineering for AI agents \ Anthropic"
[3]: https://www.anthropic.com/engineering/advanced-tool-use "Introducing advanced tool use on the Claude Developer Platform \ Anthropic"
[4]: https://platform.claude.com/docs/en/api/overview?utm_source=chatgpt.com "API Overview - Claude API Docs"
[5]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use "How to implement tool use - Claude API Docs"
[6]: https://platform.claude.com/docs/en/build-with-claude/token-counting "Token counting - Claude API Docs"
[7]: https://modelcontextprotocol.io/specification/2025-11-25/architecture "Architecture - Model Context Protocol"
[8]: https://modelcontextprotocol.io/specification/2025-11-25/client/roots "Roots - Model Context Protocol"
[9]: https://modelcontextprotocol.io/specification/2025-11-25/basic/transports "Transports - Model Context Protocol"
[10]: https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks "Tasks - Model Context Protocol"
[11]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool "Tool search tool - Claude API Docs"
[12]: https://platform.claude.com/docs/en/agent-sdk/hooks?utm_source=chatgpt.com "Intercept and control agent behavior with hooks - Claude API Docs"
