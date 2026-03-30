下面直接补你下一批最关键的实现骨架，目标是让 P0 能真正跑起来，并且给 P1 预留扩展点。

这次我重点展开 6 个文件：

* `runtime/runner.go`
* `store/sqlite/messages.go`
* `store/sqlite/memory.go`
* `store/sqlite/sessions.go`
* `contextmgr/recall.go`
* `scheduler/child_runner.go`

以及最后给你一套联调顺序和测试清单。

---

## 1. `runtime/runner.go` 完整一点的主循环

这里我们把“一次 turn”和“跑到收敛”为两个层次拆开：

* `RunTurn()`：只跑一轮
* `RunUntilIdle()`：持续循环，直到没有新的工具调用或达到限制

```go
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Runner struct {
	Model      ModelClient
	Context    ContextManager
	Broker     ToolBroker
	Artifacts  ArtifactStore
	Scheduler  Scheduler
	Policy     PolicyEngine
	EventBus   EventBus

	// 用于将 raw result 映射成 envelope
	Reducer interface {
		Reduce(ctx context.Context, raw ToolRawResult, refs []ArtifactRef) (ToolEnvelope, error)
	}

	MaxTurnsPerRun int
}

func (r *Runner) RunUntilIdle(ctx context.Context, s *Session) error {
	maxTurns := r.MaxTurnsPerRun
	if maxTurns <= 0 {
		maxTurns = 12
	}

	for i := 0; i < maxTurns; i++ {
		done, err := r.RunTurn(ctx, s)
		if err != nil {
			_ = r.emit(ctx, s, "session.failed", map[string]any{"error": err.Error(), "turn_index": i})
			return err
		}
		if done {
			_ = r.emit(ctx, s, "session.completed", map[string]any{"turn_index": i})
			return nil
		}
	}

	return errors.New("max turns exceeded")
}

// done=true 表示本次运行结束；done=false 表示应继续下一轮
func (r *Runner) RunTurn(ctx context.Context, s *Session) (bool, error) {
	start := time.Now()
	_ = r.emit(ctx, s, "turn.started", nil)

	req, err := r.Context.BuildRequest(ctx, s)
	if err != nil {
		return false, err
	}

	n, err := r.Model.CountTokens(ctx, req)
	if err != nil {
		return false, err
	}
	_ = r.emit(ctx, s, "turn.tokens_counted", map[string]any{"input_tokens": n})

	// 简单的阈值策略，后面可移到 ContextManager/BudgetPolicy
	if n > 85000 {
		_ = r.emit(ctx, s, "context.compact.started", map[string]any{"reason": "token_threshold", "input_tokens": n})
		if err := r.Context.Compact(ctx, s, "token_threshold"); err != nil {
			return false, err
		}
		_ = r.emit(ctx, s, "context.compact.completed", nil)

		req, err = r.Context.BuildRequest(ctx, s)
		if err != nil {
			return false, err
		}
	}

	resp, err := r.Model.CreateMessage(ctx, req)
	if err != nil {
		return false, err
	}
	_ = r.emit(ctx, s, "model.responded", map[string]any{
		"stop_reason":   resp.StopReason,
		"input_tokens":  resp.InputTokens,
		"output_tokens": resp.OutputTokens,
		"duration_ms":   time.Since(start).Milliseconds(),
	})

	toolCalls := extractToolCalls(resp.Content)
	if len(toolCalls) == 0 {
		text := extractText(resp.Content)
		if text == "" {
			text = "(no text output)"
		}
		if err := r.Context.AdmitAssistantText(ctx, s, text); err != nil {
			return false, err
		}
		return true, nil
	}

	// 处理所有 tool_use
	for _, tc := range toolCalls {
		if err := r.handleToolCall(ctx, s, tc); err != nil {
			return false, err
		}
	}

	// 有工具调用时，通常意味着要继续下一轮
	return false, nil
}

func (r *Runner) handleToolCall(ctx context.Context, s *Session, tc ToolCall) error {
	_ = r.emit(ctx, s, "tool.requested", map[string]any{"tool": tc.Name, "tool_use_id": tc.ID})

	// 特殊工具：spawn_subagents
	if tc.Name == "spawn_subagents" {
		tasks, err := decodeTaskPackets(tc.Input)
		if err != nil {
			return err
		}

		_ = r.emit(ctx, s, "subagent.batch.started", map[string]any{"count": len(tasks)})

		reports, err := r.Scheduler.RunChildren(ctx, s, tasks)
		if err != nil {
			return err
		}

		for _, rep := range reports {
			if err := r.Context.AdmitSubagentReport(ctx, s, rep); err != nil {
				return err
			}
			_ = r.emit(ctx, s, "subagent.completed", map[string]any{
				"task_name": rep.TaskName,
				"status":    rep.Status,
			})
		}
		return nil
	}

	// 普通工具先过 policy
	if err := r.Policy.AllowTool(ctx, tc); err != nil {
		env := ToolEnvelope{
			Summary:  fmt.Sprintf("Tool %q denied by policy", tc.Name),
			Warnings: []string{err.Error()},
			IsError:  true,
		}
		if err := r.Context.AdmitToolEnvelope(ctx, s, env); err != nil {
			return err
		}
		_ = r.emit(ctx, s, "policy.denied", map[string]any{"tool": tc.Name, "reason": err.Error()})
		return nil
	}

	raw, err := r.Broker.Exec(ctx, tc)
	if err != nil {
		raw = ToolRawResult{
			ToolName:   tc.Name,
			ExitCode:   1,
			Stderr:     []byte(err.Error()),
			Metadata:   map[string]any{"tool_use_id": tc.ID},
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		}
	}

	if raw.Metadata == nil {
		raw.Metadata = map[string]any{}
	}
	raw.Metadata["session_id"] = s.ID
	raw.Metadata["tool_use_id"] = tc.ID

	_ = r.emit(ctx, s, "tool.completed", map[string]any{
		"tool":      tc.Name,
		"exit_code": raw.ExitCode,
	})

	refs, err := r.Artifacts.PutRaw(ctx, raw)
	if err != nil {
		return err
	}
	_ = r.emit(ctx, s, "artifact.stored", map[string]any{
		"tool":          tc.Name,
		"artifact_refs": len(refs),
	})

	env, err := r.Reducer.Reduce(ctx, raw, refs)
	if err != nil {
		return err
	}

	if err := r.Context.AdmitToolEnvelope(ctx, s, env); err != nil {
		return err
	}
	_ = r.emit(ctx, s, "tool.reduced", map[string]any{
		"tool":     tc.Name,
		"is_error": env.IsError,
	})

	return nil
}

func (r *Runner) emit(ctx context.Context, s *Session, typ string, payload map[string]any) error {
	if r.EventBus == nil {
		return nil
	}
	return r.EventBus.Emit(ctx, Event{
		SessionID: s.ID,
		Type:      typ,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
}

func extractToolCalls(blocks []ContentBlock) []ToolCall {
	var out []ToolCall
	for _, b := range blocks {
		if b.Type == "tool_use" {
			out = append(out, ToolCall{
				ID:    b.ToolUseID,
				Name:  b.Name,
				Input: b.Input,
			})
		}
	}
	return out
}

func extractText(blocks []ContentBlock) string {
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

func decodeTaskPackets(input map[string]any) ([]TaskPacket, error) {
	raw, ok := input["tasks"]
	if !ok {
		return nil, errors.New("spawn_subagents missing tasks")
	}

	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("spawn_subagents tasks must be array")
	}

	out := make([]TaskPacket, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("invalid task packet")
		}
		tp := TaskPacket{}
		if v, ok := m["name"].(string); ok {
			tp.Name = v
		}
		if v, ok := m["goal"].(string); ok {
			tp.Goal = v
		}
		if v, ok := m["max_turns"].(float64); ok {
			tp.MaxTurns = int(v)
		}
		out = append(out, tp)
	}
	return out, nil
}
```

### 这个版本已经具备的能力

* 可持续循环
* 统一事件
* 子代理特殊分支
* 原始工具输出外置
* reducer 后再入上下文

---

## 2. `store/sqlite/sessions.go`

这里负责 session 和 task 的最小读写。

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
	"yourmod/internal/runtime"
)

type SessionRepo struct {
	DB *sql.DB
}

func (r *SessionRepo) CreateSession(ctx context.Context, s *runtime.Session, rootTask *runtime.Task) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO sessions (id, user_id, root_task_id, model, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.UserID, s.RootTaskID, s.Model, s.Status, s.CreatedAt.Format(time.RFC3339), s.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		return err
	}

	sc, _ := json.Marshal(rootTask.SuccessCriteria)
	at, _ := json.Marshal(rootTask.AllowedTools)

	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks (id, session_id, parent_task_id, role, goal, success_criteria_json, allowed_tools_json, status, budget_tokens, budget_usd, max_turns, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rootTask.ID, rootTask.SessionID, rootTask.ParentTaskID, rootTask.Role, rootTask.Goal, string(sc), string(at),
		rootTask.Status, rootTask.BudgetTokens, rootTask.BudgetUSD, rootTask.MaxTurns,
		rootTask.CreatedAt.Format(time.RFC3339), rootTask.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (r *SessionRepo) GetSession(ctx context.Context, sessionID string) (*runtime.Session, error) {
	row := r.DB.QueryRowContext(ctx, `
		SELECT id, user_id, root_task_id, model, status, created_at, updated_at
		FROM sessions WHERE id = ?
	`, sessionID)

	var s runtime.Session
	var created, updated string
	if err := row.Scan(&s.ID, &s.UserID, &s.RootTaskID, &s.Model, &s.Status, &created, &updated); err != nil {
		return nil, err
	}
	s.CreatedAt, _ = time.Parse(time.RFC3339, created)
	s.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &s, nil
}

func (r *SessionRepo) GetRootTask(ctx context.Context, sessionID string) (*runtime.Task, error) {
	row := r.DB.QueryRowContext(ctx, `
		SELECT id, session_id, parent_task_id, role, goal, success_criteria_json, allowed_tools_json, status, budget_tokens, budget_usd, max_turns, created_at, updated_at
		FROM tasks
		WHERE session_id = ? AND parent_task_id IS NULL
		LIMIT 1
	`, sessionID)

	var t runtime.Task
	var scJSON, atJSON string
	var created, updated string
	if err := row.Scan(
		&t.ID, &t.SessionID, &t.ParentTaskID, &t.Role, &t.Goal,
		&scJSON, &atJSON, &t.Status, &t.BudgetTokens, &t.BudgetUSD, &t.MaxTurns,
		&created, &updated,
	); err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(scJSON), &t.SuccessCriteria)
	_ = json.Unmarshal([]byte(atJSON), &t.AllowedTools)
	t.CreatedAt, _ = time.Parse(time.RFC3339, created)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &t, nil
}
```

---

## 3. `store/sqlite/messages.go`

这里负责 recent turns 和 assistant/tool envelope 落盘。

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
	"yourmod/internal/runtime"
)

type MessageRepo struct {
	DB *sql.DB
}

func (r *MessageRepo) LoadRecentMessages(ctx context.Context, sessionID string, limit int) ([]runtime.Message, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT role, content_json
		FROM messages
		WHERE session_id = ?
		ORDER BY turn_no DESC, created_at DESC
		LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rev []runtime.Message
	for rows.Next() {
		var role, contentJSON string
		if err := rows.Scan(&role, &contentJSON); err != nil {
			return nil, err
		}
		var blocks []runtime.ContentBlock
		if err := json.Unmarshal([]byte(contentJSON), &blocks); err != nil {
			return nil, err
		}
		rev = append(rev, runtime.Message{
			Role:    role,
			Content: blocks,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 反转成时间正序
	out := make([]runtime.Message, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out, nil
}

func (r *MessageRepo) SaveAssistantMessage(ctx context.Context, sessionID, text string) error {
	return r.insertMessage(ctx, sessionID, "assistant", []runtime.ContentBlock{
		{Type: "text", Text: text},
	})
}

func (r *MessageRepo) SaveEnvelopeMessage(ctx context.Context, sessionID string, env runtime.ToolEnvelope) error {
	text := env.Summary
	if len(env.KeyFacts) > 0 {
		text += "\nFacts:\n- " + joinLines(env.KeyFacts)
	}
	if len(env.Warnings) > 0 {
		text += "\nWarnings:\n- " + joinLines(env.Warnings)
	}

	return r.insertMessage(ctx, sessionID, "user", []runtime.ContentBlock{
		{
			Type:   "text",
			Text:   text,
			Result: nil,
		},
	})
}

func (r *MessageRepo) insertMessage(ctx context.Context, sessionID, role string, blocks []runtime.ContentBlock) error {
	taskID, err := r.lookupRootTaskID(ctx, sessionID)
	if err != nil {
		return err
	}

	turnNo, err := r.nextTurnNo(ctx, sessionID)
	if err != nil {
		return err
	}

	contentJSON, _ := json.Marshal(blocks)
	_, err = r.DB.ExecContext(ctx, `
		INSERT INTO messages (id, session_id, task_id, turn_no, role, content_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, newID(), sessionID, taskID, turnNo, role, string(contentJSON), time.Now().Format(time.RFC3339))
	return err
}

func (r *MessageRepo) nextTurnNo(ctx context.Context, sessionID string) (int, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(turn_no), 0) FROM messages WHERE session_id = ?`, sessionID)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n + 1, nil
}

func (r *MessageRepo) lookupRootTaskID(ctx context.Context, sessionID string) (string, error) {
	row := r.DB.QueryRowContext(ctx, `SELECT id FROM tasks WHERE session_id = ? AND parent_task_id IS NULL LIMIT 1`, sessionID)
	var id string
	if err := row.Scan(&id); err != nil {
		return "", err
	}
	return id, nil
}

func joinLines(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := ""
	for i, it := range items {
		if i > 0 {
			out += "\n- "
		} else {
			out += it
		}
	}
	return out
}

func newID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
```

### 这里有个小提醒

P0 阶段这里是“把 envelope 作为精简过的消息持久化”，不是严格还原 Claude 的原始块。这样更利于后面 compaction 和 replay。

---

## 4. `store/sqlite/memory.go`

这个文件是 P1 的基础。哪怕 P0 先不用复杂 compaction，也先把 ledger 表读写补齐。

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
	"yourmod/internal/runtime"
)

type MemoryRepo struct {
	DB *sql.DB
}

func (r *MemoryRepo) InsertMemoryEntry(
	ctx context.Context,
	sessionID string,
	taskID string,
	kind string,
	priority int,
	content map[string]any,
	sourceRefs []string,
) error {
	contentJSON, _ := json.Marshal(content)
	sourceJSON, _ := json.Marshal(sourceRefs)

	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO memory_entries (id, session_id, task_id, kind, priority, content_json, source_refs_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, newID(), sessionID, taskID, kind, priority, string(contentJSON), string(sourceJSON), time.Now().Format(time.RFC3339))
	return err
}

func (r *MemoryRepo) LoadMemoryEntriesAsMessages(
	ctx context.Context,
	sessionID string,
	kinds []string,
	limit int,
) ([]runtime.Message, error) {
	if len(kinds) == 0 {
		return nil, nil
	}

	query := `
		SELECT kind, content_json
		FROM memory_entries
		WHERE session_id = ? AND kind IN (` + placeholders(len(kinds)) + `)
		ORDER BY priority DESC, created_at DESC
		LIMIT ?
	`
	args := make([]any, 0, 2+len(kinds))
	args = append(args, sessionID)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, limit)

	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []runtime.Message
	for rows.Next() {
		var kind, contentJSON string
		if err := rows.Scan(&kind, &contentJSON); err != nil {
			return nil, err
		}
		var content map[string]any
		if err := json.Unmarshal([]byte(contentJSON), &content); err != nil {
			return nil, err
		}

		text := formatMemory(kind, content)
		out = append(out, runtime.Message{
			Role: "user",
			Content: []runtime.ContentBlock{
				{Type: "text", Text: text},
			},
		})
	}
	return out, rows.Err()
}

func formatMemory(kind string, content map[string]any) string {
	switch kind {
	case "decision":
		return "Decision: " + stringify(content["summary"])
	case "fact":
		return "Fact: " + stringify(content["summary"])
	case "open_question":
		return "Open question: " + stringify(content["summary"])
	case "plan":
		return "Plan: " + stringify(content["summary"])
	case "failure":
		return "Known failed path: " + stringify(content["summary"])
	case "child_summary":
		return "Child report: " + stringify(content["summary"])
	default:
		return stringify(content["summary"])
	}
}

func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	s := "?"
	for i := 1; i < n; i++ {
		s += ",?"
	}
	return s
}
```

---

## 5. `contextmgr/recall.go`

Recall 的职责只有一个：从 artifact 索引中拿回最相关的 snippet，并将其变成可注入 hot context 的消息。

```go
package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"yourmod/internal/runtime"
)

type RecallStore interface {
	Recall(ctx context.Context, q runtime.RecallQuery) ([]runtime.ArtifactSnippet, error)
}

type RecallManager struct {
	Store RecallStore
}

func (r *RecallManager) RecallAsMessages(ctx context.Context, q runtime.RecallQuery) ([]runtime.Message, error) {
	if q.K <= 0 {
		q.K = 3
	}

	snippets, err := r.Store.Recall(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(snippets) == 0 {
		return nil, nil
	}

	var lines []string
	for _, s := range snippets {
		lines = append(lines, fmt.Sprintf(
			"artifact=%s path=%s chunk=%d\n%s",
			s.ArtifactID, s.Path, s.ChunkNo, s.Text,
		))
	}

	msg := runtime.Message{
		Role: "user",
		Content: []runtime.ContentBlock{
			{
				Type: "text",
				Text: "Retrieved evidence snippets:\n\n" + strings.Join(lines, "\n\n---\n\n"),
			},
		},
	}
	return []runtime.Message{msg}, nil
}
```

### 用法

你可以在 `BuildRequest()` 里加一个阶段：当当前 task 标记了“需要 recall”时，把这些 snippets 加进 hot context。

---

## 6. `scheduler/child_runner.go`

子代理第一版不要追求复杂，就做“独立 session + 受限工具 + 最终生成 report”。

```go
package scheduler

import (
	"context"
	"fmt"
	"time"
	"yourmod/internal/runtime"
)

type SessionFactory interface {
	NewChildSession(ctx context.Context, parent *runtime.Session, task runtime.TaskPacket) (*runtime.Session, error)
}

type ChildContext interface {
	AdmitAssistantText(ctx context.Context, s *runtime.Session, text string) error
}

type ChildRunner struct {
	Sessions SessionFactory
	Runner   interface {
		RunUntilIdle(ctx context.Context, s *runtime.Session) error
	}
	Reader interface {
		GetLatestAssistantText(ctx context.Context, sessionID string) (string, error)
	}
}

func (c *ChildRunner) RunChild(
	ctx context.Context,
	parent *runtime.Session,
	task runtime.TaskPacket,
) (runtime.SubagentReport, error) {
	start := time.Now()

	child, err := c.Sessions.NewChildSession(ctx, parent, task)
	if err != nil {
		return runtime.SubagentReport{}, err
	}

	if err := c.Runner.RunUntilIdle(ctx, child); err != nil {
		return runtime.SubagentReport{
			TaskName:   task.Name,
			Status:     "failed",
			Summary:    err.Error(),
			DurationMs: time.Since(start).Milliseconds(),
		}, nil
	}

	finalText, err := c.Reader.GetLatestAssistantText(ctx, child.ID)
	if err != nil {
		return runtime.SubagentReport{}, err
	}

	return runtime.SubagentReport{
		TaskName:   task.Name,
		Status:     "succeeded",
		Summary:    finalText,
		Findings:   []string{fmt.Sprintf("Child task %q completed", task.Name)},
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}
```

### 这里的关键点

* child 一定是**独立 session**
* child 的 tools 在 `NewChildSession()` 时就限制好
* parent 只拿 `SubagentReport`

---

## 7. `store/sqlite/events.go`

把事件打出来，后面查问题会轻松很多。

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
	"yourmod/internal/runtime"
)

type EventRepo struct {
	DB *sql.DB
}

func (r *EventRepo) Emit(ctx context.Context, evt runtime.Event) error {
	if evt.CreatedAt.IsZero() {
		evt.CreatedAt = time.Now()
	}
	payload, _ := json.Marshal(evt.Payload)

	_, err := r.DB.ExecContext(ctx, `
		INSERT INTO events (id, session_id, task_id, event_type, payload_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, newID(), evt.SessionID, evt.TaskID, evt.Type, string(payload), evt.CreatedAt.Format(time.RFC3339))
	return err
}
```

---

## 8. ContextManager P1 怎么把 ledger 真正接起来

你现在已经有了 `memory_entries` 表和 `LoadMemoryEntriesAsMessages()`，下一步 `Compact()` 就可以这样做：

### `contextmgr/compact.go` 设计思路

```go
func (m *Manager) Compact(ctx context.Context, s *runtime.Session, reason string) error {
	// 1. 读取旧消息（不包括最近 2-4 轮）
	// 2. 提炼成 decision / fact / open_question / plan / failure
	// 3. 写入 memory_entries
	// 4. 更新 checkpoints
	// 5. 标记旧消息已 compacted（可选）
	return nil
}
```

### 第一版不要用 LLM 做 compaction

先用确定性规则即可：

* assistant 明确给出的结论 -> `decision`
* tool envelope 的关键字段 -> `fact`
* 包含 “unknown”, “need to verify” -> `open_question`
* 包含 numbered steps / next steps -> `plan`
* 失败工具 / rejected path -> `failure`

这一步和你整体思路一致：**上下文管理优先是系统设计问题，不是再加一次 LLM 调用的问题**。

---

## 9. SessionService：把 API 和 Runtime 串起来

你前面 `api/handlers.go` 已经留了入口，现在需要一个应用服务。

```go
package runtime

import (
	"context"
	"time"
)

type SessionRepo interface {
	CreateSession(ctx context.Context, s *Session, rootTask *Task) error
	GetSession(ctx context.Context, sessionID string) (*Session, error)
}

type SessionService struct {
	Repo   SessionRepo
	Runner *Runner
}

func (s *SessionService) Create(ctx context.Context, userID, model, goal string) (string, error) {
	now := time.Now()
	session := &Session{
		ID:         generateID(),
		UserID:     userID,
		RootTaskID: generateID(),
		Model:      model,
		Status:     "new",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	task := &Task{
		ID:              session.RootTaskID,
		SessionID:       session.ID,
		Role:            "root",
		Goal:            goal,
		SuccessCriteria: []string{"Provide an actionable result with evidence"},
		AllowedTools:    []string{"git_log", "grep_repo", "read_file", "run_command_readonly"},
		Status:          "queued",
		BudgetTokens:    20000,
		MaxTurns:        12,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.Repo.CreateSession(ctx, session, task); err != nil {
		return "", err
	}
	return session.ID, nil
}

func generateID() string {
	return newID()
}
```

---

## 10. P0 联调顺序

按下面顺序打通，最稳。

### A. 先打通模型单轮

只测：

* `CreateSession`
* `BuildRequest`
* `CreateMessage`
* `SaveAssistantMessage`

目标：

* 能拿到模型文本回复

### B. 再打通单工具循环

接：

* `git_log`
* `Runner.RunUntilIdle()`
* `Reducer`
* `SaveEnvelopeMessage`

目标：

* 模型请求工具
* 工具执行
* raw 存 artifact
* envelope 入上下文
* 下一轮继续

### C. 再打通 FTS5 artifact recall

接：

* `PutRaw`
* `InsertChunks`
* `Recall`

目标：

* 一条 `git log` 能被切片检索

### D. 最后接 child runner

接：

* `NewChildSession`
* `RunChild`
* `AdmitSubagentReport`

目标：

* child 独立 session
* parent 只拿 report

---

## 11. 最小测试清单

### 单元测试

先写这些：

#### `extractToolCalls`

输入：Claude 风格 `tool_use`
输出：内部 `ToolCall`

#### `GitLogReducer`

输入：模拟 git log 文本
输出：summary / keyFacts / recallHints

#### `chunkText`

输入：长文本
输出：分片数量与长度符合预期

#### `BuildToolResultBlock`

确保：

* `tool_result`
* `tool_use_id`
* 内容完整

### 集成测试

建议最先写 3 个：

#### 测试 1：单轮问答

验证：

* session 可创建
* model 可调用
* assistant 消息落库

#### 测试 2：单工具循环

验证：

* tool 被调用
* artifact 落盘
* envelope 入消息表
* 最终回答生成

#### 测试 3：artifact recall

验证：

* raw output 能切 chunk
* FTS5 可召回 top-k

---

## 12. P1 扩展路线

P0 跑起来后，不要急着加十几个工具。优先补下面 4 件事：

### 1) 真正的 `Compact()`

目标：

* 长会话能持续 20+ 轮
* ledger 替代旧 transcript

### 2) `go_test_json_reducer`

目标：

* 大量测试输出不再污染上下文

### 3) `playwright_snapshot_reducer`

目标：

* 大 DOM / snapshot 不直接进上下文

### 4) 真正并发的 `Scheduler`

目标：

* `MaxConcurrentChildren`
* semaphore
* timeout / cancel

---

## 13. 这一步之后最值得你马上写的文件

优先级最高的是这 5 个：

1. `runtime/runner.go`
2. `store/sqlite/messages.go`
3. `store/sqlite/memory.go`
4. `store/sqlite/artifacts.go`
5. `scheduler/child_runner.go`

这 5 个写完以后，P0 的系统主链基本就闭环了。

下一条我可以继续直接给你：
**`main.go`、`server.go`、`api/handlers.go`、`anthropic/client.go`、`grep_repo.go`、`read_file.go` 的完整骨架**。
