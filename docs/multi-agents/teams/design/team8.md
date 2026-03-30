继续。这一轮我把上条里留的三处关键补丁补齐，直接按“能接进你现有骨架”的方式给：

1. `RunMeta` 最终落地
2. `SendTeamMessage` 切到 `RunMeta`
3. `sqlite_repo.go` 的 `BlockTask` / mailbox receipts 最终版
4. `SessionFacade` 和 `orchestrator` 的联动补丁

这样接完以后，Team 相关的工具调用、pause/resume、mailbox 已读状态、blocked/failed/done 闭环就能统一起来。

---

# 1. `internal/runtime/chat/runmeta.go`

这个文件单独拆出来最干净。后面所有 team-aware tools 都从这里取身份，而不是各自发明上下文注入方式。

```go
package chat

import "context"

type TeamRunMeta struct {
	TeamID        string `json:"team_id"`
	AgentID       string `json:"agent_id"`
	CurrentTaskID string `json:"current_task_id,omitempty"`
}

type RunMeta struct {
	Team *TeamRunMeta `json:"team,omitempty"`
}

type runMetaKey struct{}

func WithRunMeta(ctx context.Context, meta *RunMeta) context.Context {
	if meta == nil {
		return ctx
	}
	return context.WithValue(ctx, runMetaKey{}, meta)
}

func GetRunMeta(ctx context.Context) (*RunMeta, bool) {
	v := ctx.Value(runMetaKey{})
	if v == nil {
		return nil, false
	}
	meta, ok := v.(*RunMeta)
	return meta, ok
}
```

---

# 2. `runtime_state_store.go` 的最终补丁

在你上一版 `RuntimeState` 上只加一处：`CurrentRunMeta`。

```go
type RuntimeState struct {
	SessionID       string                 `json:"session_id"`
	Status          SessionStatus          `json:"status"`
	PendingTool     *PendingToolInvocation `json:"pending_tool,omitempty"`
	PendingApproval *ApprovalRequest       `json:"pending_approval,omitempty"`
	PendingQuestion *UserQuestionRequest   `json:"pending_question,omitempty"`

	CurrentRunMeta  *RunMeta `json:"current_run_meta,omitempty"`

	VisibleUntilSeq int64     `json:"visible_until_seq"`
	UpdatedAt       time.Time `json:"updated_at"`
}
```

`DefaultRuntimeState()` 不需要额外处理，默认 `CurrentRunMeta=nil` 即可。

---

# 3. `actor.go` 的最终补丁

这里不要重写整文件，只改 5 个点最稳。

## 3.1 `SubmitPrompt` 和 `RunRequest`

```go
type SubmitPrompt struct {
	Text string
	Meta *RunMeta
}
func (SubmitPrompt) isCommand() {}

type RunRequest struct {
	SessionID    string
	Input        string
	ContinueOnly bool
	Meta         *RunMeta
}
```

## 3.2 `handleSubmit()` 保存当前 run meta

把你之前的：

```go
state.Status = SessionRunning
state.UpdatedAt = a.Now()
```

改成：

```go
state.Status = SessionRunning
state.CurrentRunMeta = cmd.Meta
state.UpdatedAt = a.Now()
```

完整片段：

```go
func (a *Actor) handleSubmit(ctx context.Context, cmd SubmitPrompt) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}
	if state.Status != SessionIdle {
		return ErrSessionBusy
	}

	state.Status = SessionRunning
	state.CurrentRunMeta = cmd.Meta
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	return a.runTurn(ctx, cmd.Text, false)
}
```

## 3.3 `runTurn()` 启动时把 meta 带进 Runner

你原来：

```go
err := a.Runner.Run(ctx, RunRequest{
	SessionID:    a.ID,
	Input:        input,
	ContinueOnly: continueOnly,
}, TurnCallbacks{...})
```

改成先加载 state，再把 `CurrentRunMeta` 带进去：

```go
func (a *Actor) runTurn(ctx context.Context, input string, continueOnly bool) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}

	err = a.Runner.Run(ctx, RunRequest{
		SessionID:    a.ID,
		Input:        input,
		ContinueOnly: continueOnly,
		Meta:         state.CurrentRunMeta,
	}, TurnCallbacks{
		OnAssistantDelta: func(text string) error {
			return a.Hub.Publish(ctx, Event{
				SessionID: a.ID,
				Type:      EventAssistantDelta,
				Payload:   mustJSON(map[string]string{"text": text}),
				At:        a.Now(),
			})
		},
		OnToolCall: func(call ToolCall) error {
			return a.onToolCall(ctx, call)
		},
		OnDone: func() error {
			return nil
		},
	})

	if errors.Is(err, ErrTurnPaused) {
		return nil
	}
	if err != nil {
		_ = a.resetToIdle(ctx, false)
		_ = a.Hub.Publish(ctx, Event{
			SessionID: a.ID,
			Type:      EventTurnFailed,
			Payload:   mustJSON(map[string]string{"error": err.Error()}),
			At:        a.Now(),
		})
		return err
	}

	if err := a.resetToIdle(ctx, true); err != nil {
		return err
	}

	return a.Hub.Publish(ctx, Event{
		SessionID: a.ID,
		Type:      EventTurnCompleted,
		Payload:   mustJSON(map[string]any{}),
		At:        a.Now(),
	})
}
```

注意这里我把 `resetToIdle()` 改成带参数，原因是：

* 正常结束时清空 `CurrentRunMeta`
* pause 时不能清空
* 异常结束时你要自己决定是否保留；我建议失败直接清空

## 3.4 `onToolCall()` 里把 RunMeta 注入 tool context

你之前直接：

```go
decision, err := a.Tools.Authorize(ctx, a.ID, call)
result, err := a.Tools.Execute(ctx, a.ID, call)
```

现在改成：

```go
func (a *Actor) onToolCall(ctx context.Context, call ToolCall) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}

	execCtx := WithRunMeta(ctx, state.CurrentRunMeta)

	if err := a.Hub.Publish(execCtx, Event{
		SessionID: a.ID,
		Type:      EventToolStarted,
		Payload:   mustJSON(call),
		At:        a.Now(),
	}); err != nil {
		return err
	}

	if call.ToolName == "AskUserQuestion" {
		var args AskUserQuestionArgs
		if err := json.Unmarshal(call.ArgsJSON, &args); err != nil {
			return fmt.Errorf("parse AskUserQuestion args: %w", err)
		}
		return a.pauseForQuestion(execCtx, call, args)
	}

	decision, err := a.Tools.Authorize(execCtx, a.ID, call)
	if err != nil {
		return err
	}

	switch decision.Type {
	case DecisionAllow:
		args := call.ArgsJSON
		if len(decision.Patched) > 0 {
			args = decision.Patched
		}

		result, err := a.Tools.Execute(execCtx, a.ID, ToolCall{
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
			ArgsJSON:   args,
		})
		if err != nil {
			return err
		}
		if err := a.Tools.AppendToolResult(execCtx, a.ID, call.ToolCallID, result); err != nil {
			return err
		}
		return a.Hub.Publish(execCtx, Event{
			SessionID: a.ID,
			Type:      EventToolFinished,
			Payload:   mustJSON(map[string]any{"tool_call_id": call.ToolCallID, "success": result.Success}),
			At:        a.Now(),
		})

	case DecisionAsk:
		return a.pauseForApproval(execCtx, call, decision)

	case DecisionDeny:
		denied := ToolResult{
			Success: false,
			Output:  "tool denied by permission engine",
		}
		if err := a.Tools.AppendToolResult(execCtx, a.ID, call.ToolCallID, denied); err != nil {
			return err
		}
		return a.Hub.Publish(execCtx, Event{
			SessionID: a.ID,
			Type:      EventToolFinished,
			Payload:   mustJSON(map[string]any{"tool_call_id": call.ToolCallID, "success": false}),
			At:        a.Now(),
		})

	default:
		return ErrToolDenied
	}
}
```

## 3.5 `resetToIdle()` 分成两种模式

```go
func (a *Actor) resetToIdle(ctx context.Context, clearRunMeta bool) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}
	state.Status = SessionIdle
	if clearRunMeta {
		state.CurrentRunMeta = nil
	}
	state.UpdatedAt = a.Now()
	return a.StateStore.Save(ctx, state)
}
```

然后：

* 正常 turn 结束：`resetToIdle(ctx, true)`
* pause：不调
* rewind 结束：建议清空
* 非预期失败：建议清空

## 3.6 `handleApprove()` / `handleAnswer()` 不要丢 `CurrentRunMeta`

这两个方法里不用额外改赋值，只要不要清空 `CurrentRunMeta` 即可。恢复继续跑的时候，`runTurn()` 会从 state 里把 meta 带进去。

---

# 4. `SessionFacade` 和 orchestrator 补丁

现在 Team 派发任务时必须把 run meta 一起塞进 teammate session。

## 4.1 `SessionFacade`

原来：

```go
type SessionFacade interface {
	SubmitPrompt(ctx context.Context, sessionID string, text string) error
}
```

改成：

```go
type SessionFacade interface {
	SubmitPrompt(ctx context.Context, sessionID string, text string, meta *chat.RunMeta) error
}
```

## 4.2 `dispatchOne()` 里构造 run meta

把你原来的：

```go
if err := o.Sessions.SubmitPrompt(ctx, mate.SessionID, prompt); err != nil {
```

改成：

```go
meta := &chat.RunMeta{
	Team: &chat.TeamRunMeta{
		TeamID:        team.ID,
		AgentID:       mate.ID,
		CurrentTaskID: task.ID,
	},
}

if err := o.Sessions.SubmitPrompt(ctx, mate.SessionID, prompt, meta); err != nil {
	_ = o.Repo.FailTask(ctx, team.ID, task.ID, err.Error(), true)
	_ = o.Repo.ReleasePathClaimsByTask(ctx, team.ID, task.ID)
	return err
}
```

## 4.3 具体 facade 实现

如果你现在 session facade 只是简单往 actor.CmCh 塞命令，那就这样：

```go
type SessionManager struct {
	actors map[string]*chat.Actor
	mu     sync.RWMutex
}

func (m *SessionManager) SubmitPrompt(ctx context.Context, sessionID string, text string, meta *chat.RunMeta) error {
	m.mu.RLock()
	actor := m.actors[sessionID]
	m.mu.RUnlock()
	if actor == nil {
		return fmt.Errorf("session actor not found: %s", sessionID)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case actor.CmdCh <- chat.SubmitPrompt{
		Text: text,
		Meta: meta,
	}:
		return nil
	}
}
```

---

# 5. `sqlite_repo.go` 的 `BlockTask()` 最终版

这个必须落地，不然 `blocked` 和 `failed` 会混淆。

```go
func (r *SQLiteRepo) BlockTask(ctx context.Context, teamID, taskID string, reason string) error {
	return r.WithTx(ctx, func(tx TxRepo) error {
		core := tx.(*txRepo).sqliteCore

		task, err := core.GetTask(ctx, teamID, taskID)
		if err != nil {
			return err
		}

		_, err = core.q.ExecContext(ctx, `
			UPDATE team_tasks
			SET status='blocked',
			    summary=?,
			    lease_until=NULL,
			    updated_at=?
			WHERE team_id=? AND id=?`,
			reason, time.Now(), teamID, taskID,
		)
		if err != nil {
			return err
		}

		if _, err := core.q.ExecContext(ctx, `
			DELETE FROM team_path_claims
			WHERE team_id=? AND task_id=?`,
			teamID, taskID,
		); err != nil {
			return err
		}

		if task.Assignee != nil {
			if _, err := core.q.ExecContext(ctx, `
				UPDATE teammates
				SET state='blocked', last_heartbeat=?
				WHERE team_id=? AND id=?`,
				time.Now(), teamID, *task.Assignee,
			); err != nil {
				return err
			}
		}

		return nil
	})
}
```

### 为什么 teammate 状态这里是 `blocked` 而不是 `idle`

因为：

* 这能让调度器看出来这个 teammate 刚经历了阻塞
* 后面你可以决定：是立刻把 blocked teammate 重新置回 idle，还是等 lead/replan 处理
* 比直接改成 idle 更可观察

如果你想第一版更简单，也可以在 orchestrator 成功 replan 后再把 संबंधित teammate 改回 idle。

---

# 6. Mailbox receipts 最终版接口实现

下面这版是你仓库里应该保留的“最终形态”。

## 6.1 `MailMessage` 结构去掉 `AckedAt`

把 `team/repo.go` 里的：

```go
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
```

改成：

```go
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
}
```

已读状态只存在 receipts 表。

## 6.2 `InsertMail()`

```go
func (r *SQLiteRepo) InsertMail(ctx context.Context, msg MailMessage) error {
	return r.WithTx(ctx, func(tx TxRepo) error {
		core := tx.(*txRepo).sqliteCore

		_, err := core.q.ExecContext(ctx, `
			INSERT INTO team_mailbox_messages (
				id, team_id, from_agent, to_agent, task_id,
				kind, body, metadata_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			msg.ID, msg.TeamID, msg.FromAgent, msg.ToAgent, msg.TaskID,
			msg.Kind, msg.Body, mustJSON(msg.Metadata), msg.CreatedAt,
		)
		if err != nil {
			return err
		}

		recipients, err := core.resolveRecipients(ctx, msg.TeamID, msg.ToAgent)
		if err != nil {
			return err
		}

		for _, agentID := range recipients {
			_, err := core.q.ExecContext(ctx, `
				INSERT INTO team_mailbox_receipts (
					message_id, team_id, agent_id, delivered_at, read_at
				) VALUES (?, ?, ?, ?, NULL)`,
				msg.ID, msg.TeamID, agentID, msg.CreatedAt,
			)
			if err != nil {
				return err
			}
		}

		return nil
	})
}
```

## 6.3 `resolveRecipients()`

```go
func (c *sqliteCore) resolveRecipients(ctx context.Context, teamID, toAgent string) ([]string, error) {
	if toAgent != "*" {
		return []string{toAgent}, nil
	}

	rows, err := c.q.QueryContext(ctx, `
		SELECT id
		FROM teammates
		WHERE team_id=?`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[string]struct{}{
		OrchestratorAgentID: {},
	}
	out := []string{OrchestratorAgentID}

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, rows.Err()
}
```

## 6.4 `ListUnreadMail()`

```go
func (c *sqliteCore) ListUnreadMail(ctx context.Context, teamID, agentID string, limit int) ([]MailMessage, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT m.id, m.team_id, m.from_agent, m.to_agent, m.task_id,
		       m.kind, m.body, m.metadata_json, m.created_at
		FROM team_mailbox_receipts r
		JOIN team_mailbox_messages m ON m.id = r.message_id
		WHERE r.team_id=? AND r.agent_id=? AND r.read_at IS NULL
		ORDER BY r.delivered_at ASC
		LIMIT ?`, teamID, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MailMessage
	for rows.Next() {
		var m MailMessage
		var taskID sql.NullString
		var meta []byte

		if err := rows.Scan(
			&m.ID, &m.TeamID, &m.FromAgent, &m.ToAgent, &taskID,
			&m.Kind, &m.Body, &meta, &m.CreatedAt,
		); err != nil {
			return nil, err
		}

		m.TaskID = nullStringPtr(taskID)
		_ = json.Unmarshal(meta, &m.Metadata)
		out = append(out, m)
	}
	return out, rows.Err()
}
```

## 6.5 `AckMail()`

```go
func (c *sqliteCore) AckMail(ctx context.Context, teamID, agentID string, msgIDs []string, at time.Time) error {
	if len(msgIDs) == 0 {
		return nil
	}

	inArgs := stringsToAny(msgIDs)
	query, args := buildIn(`
		UPDATE team_mailbox_receipts
		SET read_at=?
		WHERE team_id=?
		  AND agent_id=?
		  AND message_id IN (%s)`,
		append([]any{at, teamID, agentID}, inArgs...)...,
	)

	_, err := c.q.ExecContext(ctx, query, args...)
	return err
}
```

---

# 7. `SendTeamMessage` 最终版：切到 `RunMeta`

把上一轮基于 `TeamContextProvider` 的版本收掉，统一走 `chat.GetRunMeta(ctx)`。

```go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"your/module/internal/runtime/chat"
	"your/module/internal/team"
)

var (
	ErrNoTeamRunMeta      = errors.New("no team run metadata")
	ErrInvalidMessageKind = errors.New("invalid message kind")
)

type TeamMailWriter interface {
	InsertMail(ctx context.Context, msg team.MailMessage) error
}

type SendTeamMessageArgs struct {
	ToAgent  string         `json:"to_agent"`
	Kind     string         `json:"kind"`
	TaskID   string         `json:"task_id,omitempty"`
	Body     string         `json:"body"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type SendTeamMessageResult struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

type SendTeamMessageTool struct {
	Repo TeamMailWriter
	Now  func() time.Time
}

func NewSendTeamMessageTool(repo TeamMailWriter, now func() time.Time) *SendTeamMessageTool {
	if now == nil {
		now = time.Now
	}
	return &SendTeamMessageTool{
		Repo: repo,
		Now:  now,
	}
}

func (t *SendTeamMessageTool) Name() string {
	return "SendTeamMessage"
}

func (t *SendTeamMessageTool) Execute(ctx context.Context, raw json.RawMessage) (*SendTeamMessageResult, error) {
	var args SendTeamMessageArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}

	meta, ok := chat.GetRunMeta(ctx)
	if !ok || meta == nil || meta.Team == nil {
		return nil, ErrNoTeamRunMeta
	}

	if !isValidMessageKind(args.Kind) {
		return nil, ErrInvalidMessageKind
	}
	if args.ToAgent == "" {
		return nil, errors.New("to_agent is required")
	}
	if args.Body == "" && len(args.Metadata) == 0 {
		return nil, errors.New("body or metadata is required")
	}

	taskID := args.TaskID
	if taskID == "" {
		taskID = meta.Team.CurrentTaskID
	}
	if taskID == "" && (args.Kind == "done" || args.Kind == "blocked" || args.Kind == "failed") {
		return nil, errors.New("task_id is required for done/blocked/failed")
	}

	if args.Metadata == nil {
		args.Metadata = map[string]any{}
	}
	args.Metadata["task_id"] = taskID
	args.Metadata["from_agent"] = meta.Team.AgentID

	switch args.Kind {
	case "done":
		if _, ok := args.Metadata["summary"]; !ok {
			return nil, errors.New("done message requires metadata.summary")
		}
	case "blocked", "failed":
		if _, ok := args.Metadata["reason"]; !ok && args.Body == "" {
			return nil, errors.New("blocked/failed message requires reason")
		}
	}

	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}

	msg := team.MailMessage{
		ID:        newID(),
		TeamID:    meta.Team.TeamID,
		FromAgent: meta.Team.AgentID,
		ToAgent:   args.ToAgent,
		TaskID:    taskIDPtr,
		Kind:      args.Kind,
		Body:      args.Body,
		Metadata:  args.Metadata,
		CreatedAt: t.Now(),
	}

	if err := t.Repo.InsertMail(ctx, msg); err != nil {
		return nil, err
	}

	return &SendTeamMessageResult{
		MessageID: msg.ID,
		Status:    "queued",
	}, nil
}

func isValidMessageKind(kind string) bool {
	switch kind {
	case "info", "question", "challenge", "handoff", "done", "blocked", "failed", "warning":
		return true
	default:
		return false
	}
}

func newID() string {
	return fmt.Sprintf("id_%d", time.Now().UnixNano())
}
```

---

# 8. `ReadMailboxDigest` 的最终接线方式

你上一轮那版 `ReadMailboxDigest` 基本已经够用了，现在只要保证它也走 `chat.GetRunMeta(ctx)` 就行。接口不用再改。

调用路径会是：

* `orchestrator.dispatchOne()` 注入 `RunMeta`
* `actor.onToolCall()` 用 `WithRunMeta()` 包装 `execCtx`
* `ReadMailboxDigest.Execute(execCtx, rawArgs)` 读取 unread mails
* 可选 `MarkRead=true` 时只标记当前 agent 的 receipts

这样广播消息对不同 teammate 的可见性就完全正确了。

---

# 9. `handleBlockedMail()` 最终补丁

把上一轮的：

```go
if err := o.Repo.FailTask(ctx, msg.TeamID, taskID, reason, true); err != nil {
```

换成：

```go
if err := o.Repo.BlockTask(ctx, msg.TeamID, taskID, reason); err != nil {
```

完整最终版：

```go
func (o *Orchestrator) handleBlockedMail(ctx context.Context, msg MailMessage) error {
	taskID, _ := msg.Metadata["task_id"].(string)
	if taskID == "" && msg.TaskID != nil {
		taskID = *msg.TaskID
	}

	reason, _ := msg.Metadata["reason"].(string)
	if reason == "" {
		reason = msg.Body
	}

	o.stopLeaseKeepalive(taskID)

	if err := o.Repo.BlockTask(ctx, msg.TeamID, taskID, reason); err != nil {
		return err
	}
	return o.Repo.AppendEvent(ctx, TeamEvent{
		TeamID:    msg.TeamID,
		Type:      "task_blocked",
		Payload:   mustJSON(msg.Metadata),
		CreatedAt: o.Now(),
	})
}
```

---

# 10. 这轮改完后的真实收益

现在这几块接上之后，系统会出现三个非常重要的性质：

## 10.1 pause/resume 不丢 Team 身份

审批、提问、恢复继续跑时，`RunMeta` 还在，所以：

* `SendTeamMessage`
* `ReadMailboxDigest`
* 未来的 `ClaimMoreContext` / `ReadTaskSpec`
  都不会失去 team/task 身份

## 10.2 广播消息不会串读

因为用了 receipts：

* orchestrator 读过，不影响 teammate
* teammate A 读过，不影响 teammate B
* 同一条广播能被多个 agent 独立消费

## 10.3 blocked / failed 语义分离

这会直接影响：

* scheduler 是否尝试 replan
* lead 怎么总结团队状态
* teammate 是否该转为 idle / blocked / offline

---

# 11. 我建议你下一步直接做的测试

这几条最值：

### `TestActor_PreservesRunMetaAcrossApprovalResume`

* 提交带 `RunMeta` 的 prompt
* 模型触发 ask 权限
* 批准后恢复执行
* tool 执行时仍能读到同一个 `TeamID/AgentID/TaskID`

### `TestMailboxBroadcast_IsIndependentPerRecipient`

* 插入 `to_agent='*'` 的消息
* orchestrator 读并 ack
* teammate A 仍然能读到
* teammate B 仍然能读到

### `TestBlockedMail_TransitionsTaskToBlocked`

* teammate 发 `kind=blocked`
* orchestrator 消费后
* task -> blocked
* teammate -> blocked
* path claims 被释放

### `TestSendTeamMessage_UsesRunMetaIdentity`

* tool args 不包含可信身份
* 运行时 ctx 里有 `RunMeta`
* 落库 message 的 `TeamID/FromAgent/TaskID` 来自 `RunMeta`

---

# 12. 下一步最值得补的两个点

现在最值钱的两个后续点是：

1. **`sqlite_repo.go` 的 `WithTx` 升级为更强的立即写事务语义**
   这样 Team 的 claim/lease/path-claim 在高并发下更稳。

2. **补一个 `ReadTaskSpec` / `ReadTaskContext` 工具**
   让 teammate 不只是靠 prompt 里的任务描述，还能在任务执行中主动拉自己的 task spec、依赖结果和 deliverables。

下一条我可以直接给你这两个东西的代码骨架：

* `ReadTaskSpec` 工具
* `sqlite_repo.go` 的强事务版 `WithTx` 和高并发注意事项
