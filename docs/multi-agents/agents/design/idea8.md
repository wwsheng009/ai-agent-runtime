好，下面直接给你一版**可拆分到工程实施**的详细方案。我这次不再讲抽象概念，而是把首版系统拆成：

* 运行时主链
* Go 包与文件职责
* 关键数据结构
* 核心函数骨架
* SQLite 表结构
* 工具网关与 reducer 设计
* subagent 调度协议
* MCP Gateway 落地方式
* 测试与上线顺序

先把一个事实钉住：**Claude 的 Messages API 是无状态的**，所以每一轮都要由你自己构造请求、管理历史消息、写工具循环；Token counting 也接受与消息创建相同的结构化输入，包括 system、tools、images、PDFs，这意味着“当前轮把什么送进上下文”必须成为一个一等系统能力，而不是拼 prompt 的副作用。Claude 官方 Agent loop 也把核心流程定义成“Claude 决策 → 请求工具 → 宿主执行工具 → 把结果送回 → 重复直到完成”。([Claude开发平台][1])

## 一、先把第一版目标收敛成 P0 / P1

### P0：单代理 Runtime

目标只有 4 个：

1. 跑通 `messages.create` + `count_tokens`
2. 跑通手写 tool loop
3. 所有工具原始输出先外置
4. 回模型的永远不是原始输出，而是摘要 envelope

这样做的原因是，Anthropic 官方已经明确把“中间结果污染上下文”列为复杂工具工作流的核心问题之一；同时，工具结果在回给 Claude 之前是允许被你拦截和修改的，这正好给了你做 Output Gateway 的制度基础。([Anthropic][2])

### P1：Context OS

在 P0 跑稳后，再上：

* 热 / 温 / 冷 三层上下文
* compaction
* artifact recall
* decision ledger
* read-only subagents

Anthropic 对 context engineering 的定义就是：在多轮长任务里，系统持续策展“本轮最值得给模型看的 token 集”，而不是简单累积历史消息。([Anthropic][3])

---

## 二、整体工程结构

建议先做**模块化单体**，目录这样落：

```text
agent-runtime/
├── cmd/agentd/
│   └── main.go
├── internal/api/
│   ├── server.go
│   ├── handlers.go
│   └── dto.go
├── internal/runtime/
│   ├── types.go
│   ├── runner.go
│   ├── message_builder.go
│   └── session_service.go
├── internal/model/anthropic/
│   ├── client.go
│   ├── dto.go
│   └── mapper.go
├── internal/contextmgr/
│   ├── manager.go
│   ├── budget.go
│   ├── compact.go
│   └── recall.go
├── internal/tools/
│   ├── broker.go
│   ├── registry.go
│   ├── local/
│   │   ├── read_file.go
│   │   ├── grep_repo.go
│   │   ├── git_log.go
│   │   └── run_command.go
│   ├── reducers/
│   │   ├── reducer.go
│   │   ├── git_log.go
│   │   ├── go_test_json.go
│   │   └── playwright_snapshot.go
│   └── mcp/
│       ├── gateway.go
│       ├── client.go
│       └── catalog.go
├── internal/artifact/
│   ├── store.go
│   ├── chunker.go
│   └── types.go
├── internal/scheduler/
│   ├── scheduler.go
│   └── child_runner.go
├── internal/policy/
│   ├── engine.go
│   └── rules.go
├── internal/events/
│   ├── bus.go
│   └── types.go
├── internal/store/sqlite/
│   ├── schema.sql
│   ├── sessions.go
│   ├── tasks.go
│   ├── messages.go
│   ├── artifacts.go
│   ├── memory.go
│   └── events.go
└── go.mod
```

这套结构把“控制面”和“数据面”天然分开了。控制面负责 loop、context、scheduler、policy；数据面负责 artifact、memory、索引、checkpoint。

---

## 三、先定 7 个核心接口

这一步最重要。接口先定死，团队才能并行开发。

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

type EventBus interface {
	Emit(ctx context.Context, evt Event) error
}
```

---

## 四、基础数据结构

### 1) Session / Task

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

### 2) MessageRequest / MessageResponse

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

### 3) ToolRawResult / ToolEnvelope

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

### 4) Subagent 协议

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

subagent 这里要坚持一个硬规则：**child 是 fresh conversation，父级只接收最终报告，不接收 child transcript**。Anthropic 官方 subagents 文档明确把这一点作为核心收益：上下文隔离、并行运行、主 prompt 不膨胀。([Claude开发平台][4])

---

## 五、Model Adapter：直接走 REST，不等 Go Agent SDK

Agent SDK 公开文档目前主打 Python / TypeScript，且强调它封装了 Claude Code 的 tools、agent loop 和 context management；但 Messages API 本身足够让你在 Go 里自建 runtime。([Claude开发平台][5])

### `internal/model/anthropic/client.go`

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
	APIKey  string
	BaseURL string
	Version string
	HTTP    *http.Client
}

func New(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: "https://api.anthropic.com",
		Version: "2023-06-01",
		HTTP: &http.Client{
			Timeout: 90 * time.Second,
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

### `CountTokens` / `CreateMessage`

```go
func (c *Client) CountTokens(ctx context.Context, req MessageRequest) (int, error) {
	payload := MapToAnthropicCountTokens(req)
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := c.doJSON(ctx, "/v1/messages/count_tokens", payload, &out); err != nil {
		return 0, err
	}
	return out.InputTokens, nil
}

func (c *Client) CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error) {
	payload := MapToAnthropicMessages(req)
	var out AnthropicMessageResponse
	if err := c.doJSON(ctx, "/v1/messages", payload, &out); err != nil {
		return nil, err
	}
	resp := MapFromAnthropicMessage(out)
	return &resp, nil
}
```

这里要单独放一个 `mapper.go`，别让运行时直接依赖 Anthropic 的 JSON 结构。

---

## 六、Runner：系统主循环

Anthropic 官方对 tool use 的手动循环要求很明确：模型返回 `tool_use` 后，你需要执行工具，再把 `tool_result` 作为新的 user content block 发回；而且 `tool_result` 必须紧跟对应的 `tool_use`，并且在 user content 数组里排在最前，否则会触发 400 错。([Claude开发平台][6])

### `internal/runtime/runner.go`

```go
package runtime

import "context"

type Runner struct {
	Model      ModelClient
	Context    ContextManager
	Broker     ToolBroker
	Artifacts  ArtifactStore
	Scheduler  Scheduler
	Policy     PolicyEngine
	EventBus   EventBus
}

func (r *Runner) RunTurn(ctx context.Context, s *Session) error {
	_ = r.EventBus.Emit(ctx, Event{SessionID: s.ID, Type: "turn.started"})

	req, err := r.Context.BuildRequest(ctx, s)
	if err != nil {
		return err
	}

	n, err := r.Model.CountTokens(ctx, req)
	if err != nil {
		return err
	}
	_ = r.EventBus.Emit(ctx, Event{
		SessionID: s.ID,
		Type:      "turn.tokens_counted",
		Payload:   map[string]any{"input_tokens": n},
	})

	if n > 85000 {
		_ = r.EventBus.Emit(ctx, Event{SessionID: s.ID, Type: "context.compact.started"})
		if err := r.Context.Compact(ctx, s, "token_threshold"); err != nil {
			return err
		}
		_ = r.EventBus.Emit(ctx, Event{SessionID: s.ID, Type: "context.compact.completed"})
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
		_ = r.EventBus.Emit(ctx, Event{
			SessionID: s.ID,
			Type:      "tool.requested",
			Payload:   map[string]any{"tool": tc.Name},
		})

		if tc.Name == "spawn_subagents" {
			tasks := decodeTaskPackets(tc.Input)
			reports, err := r.Scheduler.RunChildren(ctx, s, tasks)
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
			_ = r.EventBus.Emit(ctx, Event{
				SessionID: s.ID,
				Type:      "policy.denied",
				Payload:   map[string]any{"tool": tc.Name, "reason": err.Error()},
			})
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

		_ = r.EventBus.Emit(ctx, Event{
			SessionID: s.ID,
			Type:      "tool.reduced",
			Payload:   map[string]any{"tool": tc.Name},
		})
	}

	return nil
}
```

---

## 七、MessageBuilder：强制保证 `tool_use -> tool_result` 顺序

这个文件非常关键，最好别让其他模块自己拼消息。

### `internal/runtime/message_builder.go`

```go
package runtime

func AppendToolResult(messages []Message, toolUseID string, env ToolEnvelope) []Message {
	resultText := env.Summary
	if len(env.KeyFacts) > 0 {
		resultText += "\n\nFacts:\n- " + joinWithPrefix(env.KeyFacts, "\n- ")
	}
	if len(env.Warnings) > 0 {
		resultText += "\n\nWarnings:\n- " + joinWithPrefix(env.Warnings, "\n- ")
	}
	if len(env.ArtifactRefs) > 0 {
		resultText += "\n\nArtifacts:\n- " + artifactRefLines(env.ArtifactRefs)
	}

	msg := Message{
		Role: "user",
		Content: []ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: toolUseID,
				Result:    resultText,
			},
		},
	}
	return append(messages, msg)
}
```

不要允许生成“先写 text，再写 tool_result”的 user message。Claude 明确要求 tool result blocks 必须在 content 数组最前。([Claude开发平台][6])

---

## 八、ContextManager：你真正的 Context OS

### 1) 三层上下文

**Hot Context**：真正发给模型
包含：

* system prompt
* 当前任务
* 当前计划
* 高优先级 ledger
* 最近 2~4 轮消息
* 最近 3~6 个高价值 ToolEnvelope
* 本轮必要工具定义

**Warm Memory**：默认不发
包含：

* 历史决策
* 已确认事实
* 失败路径
* 子代理结论
* 已关闭问题

**Cold Store**：永不直接进模型
包含：

* Playwright 快照
* HTML / DOM
* 长日志
* `git log` 原文
* `go test -json`
* MCP 原始响应
* 大文档全文

Anthropic 公开的 context engineering 核心观点就是：多轮 agent 需要管理 system、tools、MCP、历史消息、外部数据等整体 context state，而不只是 prompt；上下文越大，模型会出现 recall 下降和“context rot”。([Anthropic][3])

### 2) 预算策略

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

推荐首版：

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

触发规则：

* > 70%：标记 `compact_pending`
* > 85%：发送前必须 compact
* > 92%：停止注入大证据
* > 97%：禁止再 spawn child

官方还已经提供了 server-side 的 context editing beta，其中 `clear_tool_uses` 策略就是在上下文增长时清除旧工具结果；这和你自己做的 Context Mode 思路是一致的。([Claude开发平台][7])

### 3) `BuildRequest()`

```go
func (m *Manager) BuildRequest(ctx context.Context, s *Session) (MessageRequest, error) {
	req := MessageRequest{
		Model:     s.Model,
		System:    m.buildSystemPrompt(s),
		MaxTokens: 4096,
		Metadata:  map[string]string{"session_id": s.ID},
	}

	req.Messages = append(req.Messages, m.buildTaskMessage(s))
	req.Messages = append(req.Messages, m.loadRecentTurns(s, 4)...)
	req.Messages = append(req.Messages, m.loadLedgerMessages(s, 12)...)
	req.Messages = append(req.Messages, m.loadRecentEvidence(s, 6)...)

	req.Tools = m.selectToolsForTurn(s)
	return req, nil
}
```

### 4) Admit 逻辑

所有工具输出都走：

```text
raw output
-> store raw
-> reduce
-> envelope
-> token budget check
-> admit / reject
```

### 5) Compact 逻辑

不要把旧消息压成一大段 prose。要压成结构化 ledger：

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

---

## 九、Artifact Store + SQLite FTS5

### `schema.sql`

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

### Recall 查询对象

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

### FTS5 查询

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

page-in 不回全文，只回 snippet envelope。

---

## 十、ToolBroker + Reducer 体系

Anthropic 官方工具文档明确建议设计**高信号、低冗余**的工具响应；Advanced Tool Use 则直接指出，中间结果污染 context 是传统 tool-calling 的根本问题。([Claude开发平台][6])

### Reducer 接口

```go
package reducers

import "context"

type Reducer interface {
	Name() string
	Match(raw ToolRawResult) bool
	Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error)
}
```

### 第一批 3 个 reducer

#### `git_log_reducer`

抽取：

* commit 数
* 高频作者
* 高频目录
* revert/merge
* 最近相关 commit refs

#### `go_test_json_reducer`

抽取：

* 失败包
* 失败测试
* panic 入口
* 首个 stack
* 重现命令

#### `playwright_snapshot_reducer`

抽取：

* URL / title
* visible text top-N
* console error
* failed requests
* screenshot ref

### `git_log_reducer` 示例

```go
func (r *GitLogReducer) Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error) {
	lines := strings.Split(string(raw.Stdout), "\n")

	commits := 0
	reverts := 0
	authors := map[string]int{}

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
			"top modified files",
		},
	}, nil
}
```

---

## 十一、Scheduler：先逻辑并发，后物理并发

Agent SDK 的公开文档把 subagent 的价值定义得很明确：隔离上下文、并行分析、专门指令；Agent loop 文档也给出了 `SubagentStart/SubagentStop`、`PreCompact`、`PreToolUse/PostToolUse` 等 hook 点。([Claude开发平台][4])

### 调度硬规则

1. many readers, single writer
2. child-child 不直接通信
3. parent 只消费 `SubagentReport`
4. 第一版 child 只读，不写代码
5. 最大深度先设为 1
6. 最大并发先设为 3~4

### Scheduler 骨架

```go
type SchedulerConfig struct {
	MaxConcurrentChildren int
	MaxDepth              int
	MaxChildTurns         int
	ChildTimeout          time.Duration
}

type SchedulerImpl struct {
	Config SchedulerConfig
	Runner *Runner
}

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

等单代理 loop 稳了，再改成 goroutine + semaphore。

---

## 十二、Policy / Hooks / EventBus

Agent loop 文档明确说明：你对 tools、permissions、cost limits、output 拥有控制权；并且 hooks 可在工具执行、subagent 生命周期、compaction 前等关键点介入。([Claude开发平台][8])

### Event 类型

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

### 至少记录这些事件

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

### Policy 规则建议

* 只读工具默认允许
* 写文件工具默认拒绝
* `run_command` 白名单命令
* 限制 workspace root
* 限制网络白名单
* 限制 stdout/stderr 大小
* 限制超时

---

## 十三、MCP Gateway：一定做成 Host，不要直通

MCP 最新官方架构说明很清楚：它是 **host-client-server** 架构；host 创建并管理多个 client，控制权限、生命周期、安全策略和上下文聚合；协议基于 JSON-RPC，是**有状态 session 协议**；初始化必须是第一步；roots 用来限定服务器可操作的文件系统边界；Streamable HTTP transport 用 POST/GET + 可选 SSE。([Model Context Protocol][9])

### 这意味着你的落地方式应该是：

```text
Runtime
 -> MCP Gateway(host)
   -> per-server MCP client
   -> initialize / capabilities
   -> list tools / list resources
   -> local tool catalog
   -> call_tool
   -> raw result -> OutputGateway -> ToolEnvelope
   -> ContextManager
```

### Gateway 接口

```go
type MCPGateway interface {
	Initialize(ctx context.Context, serverName string) error
	ListTools(ctx context.Context, serverName string) ([]ToolDef, error)
	CallTool(ctx context.Context, serverName, toolName string, input map[string]any) (ToolRawResult, error)
	ListResources(ctx context.Context, serverName string) ([]string, error)
}
```

### Roots 的具体落法

把当前 repo / workspace 映射成 roots：

```go
type MCPRoot struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}
```

例如：

```json
[
  {"uri":"file:///workspace/project-a","name":"project-a"}
]
```

### 为什么不要 MCP 直通模型

因为工具数量一大，schema 会先吃 context；工具结果一大，原始 JSON-RPC 响应又会污染 context。Tool Search 的官方文档已经明确指出，大工具集会导致 context bloat 和工具选择精度下降，而且可以做 client-side tool search。([Claude开发平台][10])

---

## 十四、Tool Search：现在就预留，不要等接 MCP 后再补

官方 Tool Search 文档给出了很硬的工程信号：

* 工具定义可能先消耗约 55K tokens
* 超过 30–50 个工具后，选择正确率明显下降
* tool search 常把定义开销降低 85%+
* 只加载 3–5 个最相关工具
* 可以自己实现 client-side tool search ([Claude开发平台][10])

### Catalog 检索接口

```go
type ToolCatalog interface {
	UpsertTool(ctx context.Context, t ToolDef) error
	Search(ctx context.Context, query string, limit int) ([]ToolDef, error)
}
```

### 第一版检索字段

* tool name
* description
* arg names
* arg descriptions
* tags

### 第一版算法

先用 SQLite FTS5 / BM25 就够了，不要一开始上向量库。

---

## 十五、首个端到端场景

不要一开始做“自动修 bug”。首个场景建议是：

**“分析最近一周 auth 模块的提交，结合测试结果，指出最可疑的 3 个提交并给出证据。”**

开放工具：

* `git_log`
* `git_show`
* `grep_repo`
* `read_file`
* `run_command_readonly`

预期链路：

1. 用户发起任务
2. 模型调用 `git_log`
3. raw log 落 artifact
4. `git_log_reducer` 返回 envelope
5. 模型再调用 `grep_repo` / `git_show`
6. 必要时调用 `run_command_readonly`
7. 输出“可疑提交 + 路径 + 证据 ref + 下一步建议”

这个场景有多轮工具、有长输出、有 recall 需求，但没有写操作风险。

---

## 十六、分周实施

### 第 1 周

* 建项目骨架
* 写 `schema.sql`
* 实现 `anthropic/client.go`
* 实现 `runtime/types.go`
* 实现最小 `runner.go`

### 第 2 周

* 写 `ToolBroker`
* 写 `ArtifactStore`
* 写 `git_log_reducer`
* 跑通首个只读场景

### 第 3 周

* 做 `ContextManager`
* 做 Hot/Warm/Cold
* 做 compaction 和 ledger

### 第 4 周

* 做 FTS5 recall
* 做 snippet page-in
* 增加 `go_test_json_reducer`
* 增加 `playwright_snapshot_reducer`

### 第 5 周

* 加 `Scheduler`
* 跑通 read-only subagents
* 父级只吃 `SubagentReport`

### 第 6 周

* 加 MCP Gateway
* 做 tool catalog
* 做 client-side tool search
* MCP 输出仍走 Output Gateway

---

## 十七、第一版验收标准

满足下面 8 条就算首版合格：

1. 单 session 能稳定跑 20+ 轮
2. 所有大工具输出都先外置
3. 进入上下文的永远是 envelope
4. 超预算时能 compact
5. recall 能找回旧证据片段
6. `tool_use -> tool_result` 顺序始终合法
7. child 是 fresh context，父级只接报告
8. 全链路事件可审计、可回放

下一步最合适的是直接把这套方案继续往代码落：先展开 `schema.sql`、`runtime/types.go`、`anthropic/client.go`、`runtime/runner.go` 这 4 个文件的首版实现。

[1]: https://platform.claude.com/docs/en/home?utm_source=chatgpt.com "Documentation - Claude API Docs - Anthropic"
[2]: https://www.anthropic.com/engineering/advanced-tool-use "Introducing advanced tool use on the Claude Developer Platform \ Anthropic"
[3]: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents "Effective context engineering for AI agents \ Anthropic"
[4]: https://platform.claude.com/docs/en/agent-sdk/subagents "Subagents in the SDK - Claude API Docs"
[5]: https://platform.claude.com/docs/en/agent-sdk/overview "Agent SDK overview - Claude API Docs"
[6]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/implement-tool-use "How to implement tool use - Claude API Docs"
[7]: https://platform.claude.com/docs/en/build-with-claude/context-editing "Context editing - Claude API Docs"
[8]: https://platform.claude.com/docs/en/agent-sdk/agent-loop "How the agent loop works - Claude API Docs"
[9]: https://modelcontextprotocol.io/specification/2025-03-26/architecture "Architecture - Model Context Protocol"
[10]: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool "Tool search tool - Claude API Docs"
