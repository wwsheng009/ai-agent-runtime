下面直接给你 **首批可实现文件的详细设计**。目标是让你或团队成员拿着这份内容就能开工。

## 1. `runtime/types.go`

先把系统最核心的数据结构固定下来。

```go
package runtime

import "time"

type Session struct {
	ID         string
	UserID     string
	RootTaskID string
	Model      string
	Status     string // new/running/waiting_input/completed/failed/cancelled
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Task struct {
	ID              string
	SessionID       string
	ParentTaskID    *string
	Role            string // root/researcher/tester/writer/verifier
	Goal            string
	SuccessCriteria []string
	AllowedTools    []string
	Status          string // queued/running/succeeded/failed/timed_out/cancelled
	BudgetTokens    int
	BudgetUSD       float64
	MaxTurns        int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type MessageRequest struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
	Metadata  map[string]string
}

type Message struct {
	Role    string // user/assistant
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

type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type ToolCall struct {
	ID    string
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

type Event struct {
	ID        string
	SessionID string
	TaskID    string
	Type      string
	Payload   map[string]any
	CreatedAt time.Time
}
```

---

## 2. `model/anthropic/dto.go`

这层只负责对接 Claude 的 HTTP JSON，不要把这些结构泄漏到 runtime 里。

```go
package anthropic

type MessagesRequest struct {
	Model     string         `json:"model"`
	System    string         `json:"system,omitempty"`
	Messages  []MessageParam `json:"messages"`
	Tools     []ToolParam    `json:"tools,omitempty"`
	MaxTokens int            `json:"max_tokens"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type MessageParam struct {
	Role    string         `json:"role"`
	Content []ContentParam `json:"content"`
}

type ContentParam struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type ToolParam struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type MessagesResponse struct {
	ID         string               `json:"id"`
	Type       string               `json:"type"`
	Role       string               `json:"role"`
	Content    []ResponseContent    `json:"content"`
	StopReason string               `json:"stop_reason"`
	Usage      ResponseUsage        `json:"usage"`
}

type ResponseContent struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

type ResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type CountTokensRequest = MessagesRequest

type CountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}
```

---

## 3. `model/anthropic/mapper.go`

把内部结构映射到 Anthropic DTO。

```go
package anthropic

import "yourmod/internal/runtime"

func MapToAnthropicMessages(req runtime.MessageRequest) MessagesRequest {
	out := MessagesRequest{
		Model:     req.Model,
		System:    req.System,
		MaxTokens: req.MaxTokens,
		Metadata:  req.Metadata,
	}

	for _, m := range req.Messages {
		pm := MessageParam{Role: m.Role}
		for _, c := range m.Content {
			pc := ContentParam{
				Type:      c.Type,
				Text:      c.Text,
				ID:        c.ToolUseID,
				Name:      c.Name,
				Input:     c.Input,
				ToolUseID: c.ToolUseID,
			}
			if c.Type == "tool_result" {
				if s, ok := c.Result.(string); ok {
					pc.Content = s
				}
			}
			pm.Content = append(pm.Content, pc)
		}
		out.Messages = append(out.Messages, pm)
	}

	for _, t := range req.Tools {
		out.Tools = append(out.Tools, ToolParam{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}

	return out
}

func MapToAnthropicCountTokens(req runtime.MessageRequest) CountTokensRequest {
	return CountTokensRequest(MapToAnthropicMessages(req))
}

func MapFromAnthropicMessage(resp MessagesResponse) runtime.MessageResponse {
	out := runtime.MessageResponse{
		ID:           resp.ID,
		StopReason:   resp.StopReason,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}

	for _, c := range resp.Content {
		block := runtime.ContentBlock{
			Type: c.Type,
			Text: c.Text,
		}
		if c.Type == "tool_use" {
			block.ToolUseID = c.ID
			block.Name = c.Name
			block.Input = c.Input
		}
		out.Content = append(out.Content, block)
	}

	return out
}
```

---

## 4. `runtime/message_builder.go`

这个文件专门保证 Claude 工具消息格式合法。

```go
package runtime

import "strings"

func BuildToolResultBlock(toolUseID string, env ToolEnvelope) ContentBlock {
	parts := []string{env.Summary}

	if len(env.KeyFacts) > 0 {
		parts = append(parts, "Facts:\n- "+strings.Join(env.KeyFacts, "\n- "))
	}
	if len(env.Warnings) > 0 {
		parts = append(parts, "Warnings:\n- "+strings.Join(env.Warnings, "\n- "))
	}
	if len(env.ArtifactRefs) > 0 {
		lines := make([]string, 0, len(env.ArtifactRefs))
		for _, ref := range env.ArtifactRefs {
			lines = append(lines, ref.ID+" "+ref.Path)
		}
		parts = append(parts, "Artifacts:\n- "+strings.Join(lines, "\n- "))
	}

	return ContentBlock{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Result:    strings.Join(parts, "\n\n"),
	}
}

func AppendToolResultMessage(messages []Message, toolUseID string, env ToolEnvelope) []Message {
	return append(messages, Message{
		Role: "user",
		Content: []ContentBlock{
			BuildToolResultBlock(toolUseID, env),
		},
	})
}
```

---

## 5. `contextmgr/budget.go`

先把预算规则硬编码，后面再调优。

```go
package contextmgr

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

func DefaultBudget() Budget {
	return Budget{
		MaxInputTokens: 100000,
		SystemPct:      10,
		TaskPct:        10,
		LedgerPct:      15,
		RecentPct:      15,
		EvidencePct:    20,
		ToolsPct:       10,
		HeadroomPct:    20,
	}
}

func (b Budget) ThresholdCompact() int {
	return b.MaxInputTokens * 85 / 100
}

func (b Budget) ThresholdWarn() int {
	return b.MaxInputTokens * 70 / 100
}

func (b Budget) ThresholdNoBigEvidence() int {
	return b.MaxInputTokens * 92 / 100
}
```

---

## 6. `contextmgr/manager.go`

首版只做最小可用逻辑：从存储层装配 hot context。

```go
package contextmgr

import (
	"context"
	"yourmod/internal/runtime"
)

type Store interface {
	LoadSessionTask(ctx context.Context, sessionID string) (*runtime.Task, error)
	LoadRecentMessages(ctx context.Context, sessionID string, limit int) ([]runtime.Message, error)
	LoadMemoryEntries(ctx context.Context, sessionID string, kinds []string, limit int) ([]runtime.Message, error)
	LoadRecentEvidence(ctx context.Context, sessionID string, limit int) ([]runtime.Message, error)
	SaveAssistantMessage(ctx context.Context, sessionID string, text string) error
	SaveToolEnvelope(ctx context.Context, sessionID string, env runtime.ToolEnvelope) error
	SaveSubagentReport(ctx context.Context, sessionID string, rep runtime.SubagentReport) error
}

type Manager struct {
	Store  Store
	Budget Budget
	Tools  func(*runtime.Session) []runtime.ToolDef
}

func (m *Manager) BuildRequest(ctx context.Context, s *runtime.Session) (runtime.MessageRequest, error) {
	task, err := m.Store.LoadSessionTask(ctx, s.ID)
	if err != nil {
		return runtime.MessageRequest{}, err
	}

	req := runtime.MessageRequest{
		Model:     s.Model,
		System:    buildSystemPrompt(task),
		MaxTokens: 4096,
		Metadata:  map[string]string{"session_id": s.ID},
	}

	req.Messages = append(req.Messages, buildTaskMessage(task))

	recent, err := m.Store.LoadRecentMessages(ctx, s.ID, 4)
	if err != nil {
		return runtime.MessageRequest{}, err
	}
	req.Messages = append(req.Messages, recent...)

	ledger, err := m.Store.LoadMemoryEntries(ctx, s.ID, []string{"decision", "fact", "open_question", "plan"}, 12)
	if err != nil {
		return runtime.MessageRequest{}, err
	}
	req.Messages = append(req.Messages, ledger...)

	evidence, err := m.Store.LoadRecentEvidence(ctx, s.ID, 6)
	if err != nil {
		return runtime.MessageRequest{}, err
	}
	req.Messages = append(req.Messages, evidence...)

	if m.Tools != nil {
		req.Tools = m.Tools(s)
	}

	return req, nil
}

func (m *Manager) AdmitToolEnvelope(ctx context.Context, s *runtime.Session, env runtime.ToolEnvelope) error {
	return m.Store.SaveToolEnvelope(ctx, s.ID, env)
}

func (m *Manager) AdmitSubagentReport(ctx context.Context, s *runtime.Session, rep runtime.SubagentReport) error {
	return m.Store.SaveSubagentReport(ctx, s.ID, rep)
}

func (m *Manager) AdmitAssistantText(ctx context.Context, s *runtime.Session, text string) error {
	return m.Store.SaveAssistantMessage(ctx, s.ID, text)
}

func (m *Manager) Compact(ctx context.Context, s *runtime.Session, reason string) error {
	// P0 先留空壳；P1 再实现真正的 ledger compaction
	return nil
}

func buildSystemPrompt(task *runtime.Task) string {
	return "You are a focused coding agent. Prefer concise reasoning. Use tools when needed. Keep track of evidence and summarize tool results."
}

func buildTaskMessage(task *runtime.Task) runtime.Message {
	return runtime.Message{
		Role: "user",
		Content: []runtime.ContentBlock{
			{
				Type: "text",
				Text: "Task goal: " + task.Goal,
			},
		},
	}
}
```

---

## 7. `tools/broker.go`

统一执行本地工具，后面再扩 MCP。

```go
package tools

import (
	"context"
	"fmt"
	"yourmod/internal/runtime"
)

type Tool interface {
	Name() string
	Run(ctx context.Context, input map[string]any) (runtime.ToolRawResult, error)
}

type Broker struct {
	tools map[string]Tool
}

func NewBroker(ts ...Tool) *Broker {
	m := make(map[string]Tool, len(ts))
	for _, t := range ts {
		m[t.Name()] = t
	}
	return &Broker{tools: m}
}

func (b *Broker) Exec(ctx context.Context, call runtime.ToolCall) (runtime.ToolRawResult, error) {
	t, ok := b.tools[call.Name]
	if !ok {
		return runtime.ToolRawResult{}, fmt.Errorf("tool not found: %s", call.Name)
	}
	return t.Run(ctx, call.Input)
}
```

---

## 8. 本地工具示例：`tools/local/git_log.go`

```go
package local

import (
	"context"
	"os/exec"
	"time"
	"yourmod/internal/runtime"
)

type GitLogTool struct{}

func (t *GitLogTool) Name() string { return "git_log" }

func (t *GitLogTool) Run(ctx context.Context, input map[string]any) (runtime.ToolRawResult, error) {
	since := "7 days ago"
	if v, ok := input["since"].(string); ok && v != "" {
		since = v
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, "git", "log", "--since="+since, "--stat", "--decorate")
	out, err := cmd.CombinedOutput()
	end := time.Now()

	res := runtime.ToolRawResult{
		ToolName:   "git_log",
		Stdout:     out,
		ExitCode:   0,
		Metadata:   map[string]any{"since": since},
		StartedAt:  start,
		FinishedAt: end,
	}
	if err != nil {
		res.ExitCode = 1
		res.Stderr = []byte(err.Error())
	}
	return res, nil
}
```

---

## 9. `tools/reducers/reducer.go`

```go
package reducers

import (
	"context"
	"fmt"
	"yourmod/internal/runtime"
)

type Reducer interface {
	Name() string
	Match(raw runtime.ToolRawResult) bool
	Reduce(ctx context.Context, raw runtime.ToolRawResult, refs []runtime.ArtifactRef) (runtime.ToolEnvelope, error)
}

type Chain struct {
	items []Reducer
}

func NewChain(rs ...Reducer) *Chain {
	return &Chain{items: rs}
}

func (c *Chain) Reduce(ctx context.Context, raw runtime.ToolRawResult, refs []runtime.ArtifactRef) (runtime.ToolEnvelope, error) {
	for _, r := range c.items {
		if r.Match(raw) {
			return r.Reduce(ctx, raw, refs)
		}
	}
	return runtime.ToolEnvelope{
		Summary:      fmt.Sprintf("Tool %s finished with exit_code=%d", raw.ToolName, raw.ExitCode),
		ArtifactRefs: refs,
		IsError:      raw.ExitCode != 0,
	}, nil
}
```

---

## 10. `tools/reducers/git_log.go`

```go
package reducers

import (
	"context"
	"fmt"
	"strings"
	"yourmod/internal/runtime"
)

type GitLogReducer struct{}

func (r *GitLogReducer) Name() string { return "git_log_reducer" }
func (r *GitLogReducer) Match(raw runtime.ToolRawResult) bool {
	return raw.ToolName == "git_log"
}

func (r *GitLogReducer) Reduce(ctx context.Context, raw runtime.ToolRawResult, refs []runtime.ArtifactRef) (runtime.ToolEnvelope, error) {
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

	keyFacts := []string{
		fmt.Sprintf("commit_count=%d", commits),
		fmt.Sprintf("revert_like=%d", reverts),
	}

	for a, n := range authors {
		keyFacts = append(keyFacts, fmt.Sprintf("author=%s count=%d", a, n))
		if len(keyFacts) >= 6 {
			break
		}
	}

	return runtime.ToolEnvelope{
		Summary:      fmt.Sprintf("Parsed git history: %d commits, %d revert-like entries", commits, reverts),
		KeyFacts:     keyFacts,
		ArtifactRefs: refs,
		RecallHints: []string{
			"recent commits touching auth",
			"revert commits",
			"commits by frequent authors",
		},
		IsError: raw.ExitCode != 0,
	}, nil
}
```

---

## 11. `artifact/store.go`

先做本地文件存储 + SQLite metadata。

```go
package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
	"yourmod/internal/runtime"
)

type MetadataStore interface {
	InsertArtifact(ctx context.Context, ref runtime.ArtifactRef, raw runtime.ToolRawResult, sha string, size int64) error
	InsertChunks(ctx context.Context, ref runtime.ArtifactRef, chunks []string, raw runtime.ToolRawResult) error
	Recall(ctx context.Context, q runtime.RecallQuery) ([]runtime.ArtifactSnippet, error)
}

type Store struct {
	BaseDir string
	Meta    MetadataStore
}

func (s *Store) PutRaw(ctx context.Context, raw runtime.ToolRawResult) ([]runtime.ArtifactRef, error) {
	id := newID()
	path := filepath.Join(s.BaseDir, id+".txt")

	payload := raw.Stdout
	if len(payload) == 0 {
		payload = raw.Stderr
	}

	if err := os.WriteFile(path, payload, 0644); err != nil {
		return nil, err
	}

	sum := sha256.Sum256(payload)
	sha := hex.EncodeToString(sum[:])

	ref := runtime.ArtifactRef{
		ID:   id,
		Type: "text",
		Path: path,
	}

	if err := s.Meta.InsertArtifact(ctx, ref, raw, sha, int64(len(payload))); err != nil {
		return nil, err
	}

	chunks := chunkText(string(payload), 2000)
	if err := s.Meta.InsertChunks(ctx, ref, chunks, raw); err != nil {
		return nil, err
	}

	return []runtime.ArtifactRef{ref}, nil
}

func (s *Store) Recall(ctx context.Context, q runtime.RecallQuery) ([]runtime.ArtifactSnippet, error) {
	return s.Meta.Recall(ctx, q)
}

func newID() string {
	return time.Now().Format("20060102150405.000000000")
}

func chunkText(text string, max int) []string {
	if len(text) <= max {
		return []string{text}
	}
	var out []string
	for i := 0; i < len(text); i += max {
		j := i + max
		if j > len(text) {
			j = len(text)
		}
		out = append(out, text[i:j])
	}
	return out
}
```

---

## 12. `store/sqlite/schema.sql`

先给最小可用版：

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

---

## 13. `store/sqlite/artifacts.go`

重点是 insert artifact、insert chunks、recall。

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
	"yourmod/internal/runtime"
)

type ArtifactRepo struct {
	DB *sql.DB
}

func (r *ArtifactRepo) InsertArtifact(ctx context.Context, ref runtime.ArtifactRef, raw runtime.ToolRawResult, sha string, size int64) error {
	meta, _ := json.Marshal(raw.Metadata)

	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO artifacts (id, session_id, task_id, tool_name, artifact_type, path, sha256, size_bytes, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, ref.ID, raw.Metadata["session_id"], raw.Metadata["task_id"], raw.ToolName, ref.Type, ref.Path, sha, size, string(meta), time.Now().Format(time.RFC3339))
	return err
}

func (r *ArtifactRepo) InsertChunks(ctx context.Context, ref runtime.ArtifactRef, chunks []string, raw runtime.ToolRawResult) error {
	for i, c := range chunks {
		res, err := r.DB.ExecContext(ctx, `
			INSERT INTO artifact_chunks (artifact_id, session_id, task_id, tool_name, chunk_no, chunk_text, path, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, ref.ID, raw.Metadata["session_id"], raw.Metadata["task_id"], raw.ToolName, i, c, ref.Path, time.Now().Format(time.RFC3339))
		if err != nil {
			return err
		}
		rowid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := r.DB.ExecContext(ctx, `
			INSERT INTO artifact_chunks_fts(rowid, chunk_text) VALUES (?, ?)
		`, rowid, c); err != nil {
			return err
		}
	}
	return nil
}

func (r *ArtifactRepo) Recall(ctx context.Context, q runtime.RecallQuery) ([]runtime.ArtifactSnippet, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT c.artifact_id, c.chunk_no, c.chunk_text, c.path, bm25(artifact_chunks_fts) AS score
		FROM artifact_chunks_fts
		JOIN artifact_chunks c ON artifact_chunks_fts.rowid = c.id
		WHERE artifact_chunks_fts MATCH ?
		ORDER BY score
		LIMIT ?
	`, q.Query, q.K)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []runtime.ArtifactSnippet
	for rows.Next() {
		var s runtime.ArtifactSnippet
		if err := rows.Scan(&s.ArtifactID, &s.ChunkNo, &s.Text, &s.Path, &s.Score); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```

---

## 14. `scheduler/scheduler.go`

首版先顺序执行 child，逻辑上已经是独立上下文。

```go
package scheduler

import (
	"context"
	"time"
	"yourmod/internal/runtime"
)

type ChildRunner interface {
	RunChild(ctx context.Context, parent *runtime.Session, task runtime.TaskPacket) (runtime.SubagentReport, error)
}

type Config struct {
	MaxConcurrentChildren int
	MaxDepth              int
	MaxChildTurns         int
	ChildTimeout          time.Duration
}

type Scheduler struct {
	Config Config
	Child  ChildRunner
}

func (s *Scheduler) RunChildren(ctx context.Context, parent *runtime.Session, tasks []runtime.TaskPacket) ([]runtime.SubagentReport, error) {
	reports := make([]runtime.SubagentReport, 0, len(tasks))
	for _, t := range tasks {
		rep, err := s.Child.RunChild(ctx, parent, t)
		if err != nil {
			return nil, err
		}
		reports = append(reports, rep)
	}
	return reports, nil
}
```

---

## 15. `tools/mcp/gateway.go`

第一版先留标准接口，第二版再接真正 transport。

MCP 的 host-client-server 架构、每个 server 一个隔离 client、初始化生命周期和 roots 概念，都适合被封在 gateway 这一层，而不是让 runtime 直接碰 JSON-RPC。([modelcontextprotocol.io](https://modelcontextprotocol.io/docs/learn/architecture))

```go
package mcp

import (
	"context"
	"yourmod/internal/runtime"
)

type Gateway interface {
	Initialize(ctx context.Context, serverName string) error
	ListTools(ctx context.Context, serverName string) ([]runtime.ToolDef, error)
	CallTool(ctx context.Context, serverName, toolName string, input map[string]any) (runtime.ToolRawResult, error)
	ListResources(ctx context.Context, serverName string) ([]string, error)
}
```

### 为什么要先留这个接口

因为后面接 MCP 时，工具和资源的发现、索引、按需加载、输出压缩，都要从这里穿过去，不能让 MCP 直通模型。

---

## 16. API 层最小实现

### `api/dto.go`

```go
package api

type CreateSessionRequest struct {
	UserID string `json:"user_id"`
	Model  string `json:"model"`
	Goal   string `json:"goal"`
}

type PostMessageRequest struct {
	Content string `json:"content"`
}

type SessionResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
```

### `api/handlers.go`

```go
package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) CreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := s.SessionService.Create(r.Context(), req.UserID, req.Model, req.Goal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = json.NewEncoder(w).Encode(SessionResponse{
		ID:     id,
		Status: "created",
	})
}
```

---

## 17. P0 到 P1 的联调顺序

按这个顺序最稳：

### 第一步

打通：

* `anthropic/client.go`
* `runtime/types.go`
* `schema.sql`

目标：能建表、能发单轮消息。

### 第二步

打通：

* `runtime/runner.go`
* `runtime/message_builder.go`
* `tools/broker.go`
* `tools/local/git_log.go`

目标：能跑单工具循环。

### 第三步

打通：

* `artifact/store.go`
* `store/sqlite/artifacts.go`
* `tools/reducers/git_log.go`

目标：raw output 外置，envelope 入上下文。

### 第四步

打通：

* `contextmgr/manager.go`
* `store/sqlite/messages.go`
* `store/sqlite/memory.go`

目标：能构建 hot context，并让 session 连续跑多轮。

### 第五步

增加：

* `recall.go`
* `artifact_chunks_fts`
* recall 查询

目标：能按查询拉回历史 evidence 片段。

### 第六步

增加：

* `scheduler/scheduler.go`
* `child_runner.go`

目标：跑 read-only subagent。

---

## 18. 首个测试场景

先只测这个：

**“分析最近 7 天 auth 模块相关提交，并指出最可疑的 3 个提交与证据。”**

### 开放工具

* `git_log`
* `grep_repo`
* `read_file`
* `run_command_readonly`

### 验收点

* `git_log` 原文不直接进上下文
* 上下文里只看到 reducer 产物
* 最终答案带 artifact refs
* session 能跑 10+ 轮不崩

---

## 19. 这一步之后你最该马上写的文件

按优先级：

1. `internal/runtime/types.go`
2. `internal/model/anthropic/client.go`
3. `internal/store/sqlite/schema.sql`
4. `internal/runtime/runner.go`
5. `internal/tools/local/git_log.go`
6. `internal/tools/reducers/git_log.go`
7. `internal/artifact/store.go`

这 7 个文件一旦成型，P0 基本就能跑起来。

下一条我可以继续直接给你：
**`runner.go`、`schema.sql`、`messages.go`、`memory.go`、`child_runner.go` 的更完整代码骨架**。
