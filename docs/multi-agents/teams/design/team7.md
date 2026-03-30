继续的话，我先补一个关键修正，再给你剩余骨架。

## 先修一个关键坑：Mailbox 不能用单个 `acked_at`

上一轮我给的 `team_mailbox_messages.acked_at` 设计，对广播消息有问题：

* `to_agent='*'` 的消息如果被一个 agent 标记已读
* 其他 agent 就都看不到了

这会直接把 Team 通信搞坏。所以这里建议立刻改成：

* `team_mailbox_messages` 只存消息本体
* 新增 `team_mailbox_receipts`，按 **message_id + agent_id** 记录每个收件人的已读状态

这个改动非常值，因为它会影响：

* `ReadMailboxDigest`
* Orchestrator 消费 mailbox
* teammate 读取广播/直达消息
* 未来 Web/IDE 的消息状态展示

---

# 一、Mailbox schema 修正

## 1.1 改后的表结构

把原来的 `acked_at` 从 message 表拿掉，改成 receipts：

```sql
CREATE TABLE team_mailbox_messages (
  id TEXT PRIMARY KEY,
  team_id TEXT NOT NULL,
  from_agent TEXT NOT NULL,
  to_agent TEXT NOT NULL,
  task_id TEXT,
  kind TEXT NOT NULL CHECK (
    kind IN ('info', 'question', 'challenge', 'handoff', 'done', 'blocked', 'failed', 'warning')
  ),
  body TEXT NOT NULL,
  metadata_json BLOB,
  created_at DATETIME NOT NULL
);

CREATE TABLE team_mailbox_receipts (
  message_id TEXT NOT NULL,
  team_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  delivered_at DATETIME NOT NULL,
  read_at DATETIME,
  PRIMARY KEY (message_id, agent_id),
  FOREIGN KEY(message_id) REFERENCES team_mailbox_messages(id)
);

CREATE INDEX idx_team_mail_receipts_unread
ON team_mailbox_receipts(team_id, agent_id, read_at, delivered_at);

CREATE INDEX idx_team_mail_messages_team_created
ON team_mailbox_messages(team_id, created_at);
```

## 1.2 Repo 接口不变

接口仍然可以保持：

```go
InsertMail(ctx, msg)
ListUnreadMail(ctx, teamID, agentID, limit)
AckMail(ctx, teamID, agentID, msgIDs, at)
```

但是实现要改成基于 receipts。

---

# 二、`sqlite_repo.go` 里建议直接补的 helper

这些 helper 现在就该落，不然后面每个 CRUD 都会重复写。

```go
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func stringsToAny(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func buildIn(queryFmt string, args ...any) (string, []any) {
	if len(args) == 0 {
		return fmt.Sprintf(queryFmt, "NULL"), nil
	}
	holders := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
	return fmt.Sprintf(queryFmt, holders), args
}
```

## 2.1 `scanTasks`

```go
func scanTasks(rows *sql.Rows) ([]Task, error) {
	var out []Task

	for rows.Next() {
		var t Task
		var parentID sql.NullString
		var assignee sql.NullString
		var leaseUntil sql.NullTime
		var resultRef sql.NullString

		var inputs []byte
		var readPaths []byte
		var writePaths []byte
		var deliverables []byte

		if err := rows.Scan(
			&t.ID, &t.TeamID, &parentID, &t.Title, &t.Goal, &inputs,
			&t.Status, &t.Priority, &assignee, &leaseUntil, &t.RetryCount,
			&readPaths, &writePaths, &deliverables,
			&t.Summary, &resultRef, &t.Version, &t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}

		t.ParentTaskID = nullStringPtr(parentID)
		t.Assignee = nullStringPtr(assignee)
		t.LeaseUntil = nullTimePtr(leaseUntil)
		t.ResultRef = nullStringPtr(resultRef)

		_ = json.Unmarshal(inputs, &t.Inputs)
		_ = json.Unmarshal(readPaths, &t.ReadPaths)
		_ = json.Unmarshal(writePaths, &t.WritePaths)
		_ = json.Unmarshal(deliverables, &t.Deliverables)

		out = append(out, t)
	}

	return out, rows.Err()
}
```

## 2.2 `scanTeammates`

```go
func scanTeammates(rows *sql.Rows) ([]Teammate, error) {
	var out []Teammate

	for rows.Next() {
		var m Teammate
		var caps, meta []byte

		if err := rows.Scan(
			&m.ID, &m.TeamID, &m.Name, &m.Profile, &m.SessionID,
			&m.State, &m.LastHeartbeat, &caps, &meta,
		); err != nil {
			return nil, err
		}

		_ = json.Unmarshal(caps, &m.Capabilities)
		_ = json.Unmarshal(meta, &m.Metadata)
		out = append(out, m)
	}

	return out, rows.Err()
}
```

---

# 三、`sqlite_repo.go` 剩余常用 CRUD

下面这些建议你直接补齐。

## 3.1 `ListTasks`

```go
func (c *sqliteCore) ListTasks(ctx context.Context, teamID string) ([]Task, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, parent_task_id, title, goal, inputs_json,
		       status, priority, assignee, lease_until, retry_count,
		       read_paths_json, write_paths_json, deliverables_json,
		       summary, result_ref, version, created_at, updated_at
		FROM team_tasks
		WHERE team_id=?
		ORDER BY created_at ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}
```

## 3.2 `ListBlockedTasks`

```go
func (c *sqliteCore) ListBlockedTasks(ctx context.Context, teamID string) ([]Task, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, parent_task_id, title, goal, inputs_json,
		       status, priority, assignee, lease_until, retry_count,
		       read_paths_json, write_paths_json, deliverables_json,
		       summary, result_ref, version, created_at, updated_at
		FROM team_tasks
		WHERE team_id=? AND status='blocked'
		ORDER BY updated_at ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}
```

## 3.3 `ListTeammates`

```go
func (c *sqliteCore) ListTeammates(ctx context.Context, teamID string) ([]Teammate, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, name, profile, session_id, state,
		       last_heartbeat, capabilities_json, metadata_json
		FROM teammates
		WHERE team_id=?
		ORDER BY name ASC`, teamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTeammates(rows)
}
```

## 3.4 `GetTeammate`

```go
func (c *sqliteCore) GetTeammate(ctx context.Context, teamID, teammateID string) (*Teammate, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT id, team_id, name, profile, session_id, state,
		       last_heartbeat, capabilities_json, metadata_json
		FROM teammates
		WHERE team_id=? AND id=?`, teamID, teammateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items, err := scanTeammates(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return &items[0], nil
}
```

## 3.5 `AddDependencies`

```go
func (c *sqliteCore) AddDependencies(ctx context.Context, deps []TaskDependency) error {
	for _, d := range deps {
		_, err := c.q.ExecContext(ctx, `
			INSERT INTO team_task_dependencies (task_id, depends_on_task_id)
			VALUES (?, ?)`, d.TaskID, d.DependsOnTaskID)
		if err != nil {
			return err
		}
	}
	return nil
}
```

## 3.6 `ListDependents`

```go
func (c *sqliteCore) ListDependents(ctx context.Context, teamID, taskID string) ([]Task, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT t.id, t.team_id, t.parent_task_id, t.title, t.goal, t.inputs_json,
		       t.status, t.priority, t.assignee, t.lease_until, t.retry_count,
		       t.read_paths_json, t.write_paths_json, t.deliverables_json,
		       t.summary, t.result_ref, t.version, t.created_at, t.updated_at
		FROM team_task_dependencies d
		JOIN team_tasks t ON t.id = d.task_id
		WHERE t.team_id=? AND d.depends_on_task_id=?
		ORDER BY t.created_at ASC`, teamID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanTasks(rows)
}
```

## 3.7 `DeleteExpiredPathClaims`

```go
func (c *sqliteCore) DeleteExpiredPathClaims(ctx context.Context, teamID string, now time.Time) error {
	_, err := c.q.ExecContext(ctx, `
		DELETE FROM team_path_claims
		WHERE team_id=? AND lease_until < ?`,
		teamID, now)
	return err
}
```

## 3.8 `ListEventsAfter`

```go
func (c *sqliteCore) ListEventsAfter(ctx context.Context, teamID string, afterSeq int64, limit int) ([]TeamEvent, error) {
	rows, err := c.q.QueryContext(ctx, `
		SELECT team_id, seq, type, payload_json, created_at
		FROM team_events
		WHERE team_id=? AND seq > ?
		ORDER BY seq ASC
		LIMIT ?`, teamID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TeamEvent
	for rows.Next() {
		var e TeamEvent
		if err := rows.Scan(&e.TeamID, &e.Seq, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

---

# 四、把 Mailbox 实现切到 receipts

下面这块建议你直接替换掉上一轮 `AckMail`/`ListUnreadMail` 的实现。

## 4.1 `InsertMail`

这里有一个关键点：**写消息时就 fan-out receipts**。

* 直达消息：插 1 条 receipt
* 广播消息：给所有 teammate 插 receipt
* 广播是否也给 orchestrator 插 receipt，要看你的语义
  我建议给 orchestrator 也插，这样它也能消费广播

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

## 4.2 `resolveRecipients`

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

	seen := map[string]struct{}{OrchestratorAgentID: {}}
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

## 4.3 `ListUnreadMail`

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

## 4.4 `AckMail`

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

# 五、`ReadMailboxDigest` 工具

这就是你上一条里提到的“下一步最值钱的小工具”。

但这里我建议你**不要直接依赖静态 TeamContextProvider**，而是把 Team 运行时元数据挂进 session run metadata。因为：

* teammate turn 里会调用 `SendTeamMessage`
* 也会调用 `ReadMailboxDigest`
* turn 中途可能 pause/resume
* 恢复后仍然要保留 Team 身份

这意味着 Team metadata 不能只是外层函数参数，而要进入 **session runtime state**。

---

# 六、先补一刀：Session Run Metadata

## 6.1 `runtime_state_store.go` 增加当前运行元数据

```go
type TeamRunMeta struct {
	TeamID        string `json:"team_id"`
	AgentID       string `json:"agent_id"`
	CurrentTaskID string `json:"current_task_id,omitempty"`
}

type RunMeta struct {
	Team *TeamRunMeta `json:"team,omitempty"`
}

type RuntimeState struct {
	SessionID       string                `json:"session_id"`
	Status          SessionStatus         `json:"status"`
	PendingTool     *PendingToolInvocation `json:"pending_tool,omitempty"`
	PendingApproval *ApprovalRequest      `json:"pending_approval,omitempty"`
	PendingQuestion *UserQuestionRequest  `json:"pending_question,omitempty"`

	CurrentRunMeta  *RunMeta `json:"current_run_meta,omitempty"`

	VisibleUntilSeq int64     `json:"visible_until_seq"`
	UpdatedAt       time.Time `json:"updated_at"`
}
```

## 6.2 `actor.go` 里的 `SubmitPrompt` / `RunRequest` 带 metadata

```go
type SubmitPrompt struct {
	Text string
	Meta *RunMeta
}

type RunRequest struct {
	SessionID    string
	Input        string
	ContinueOnly bool
	Meta         *RunMeta
}
```

## 6.3 `handleSubmit()` 保存 metadata

```go
state.Status = SessionRunning
state.CurrentRunMeta = cmd.Meta
state.UpdatedAt = a.Now()
```

## 6.4 `runTurn()` 继续时带上 `state.CurrentRunMeta`

在 `handleApprove()` / `handleAnswer()` 恢复 loop 时，不要丢掉 meta。
也就是说 `runTurn(ctx, "", true)` 之前，Runner 要拿到 `state.CurrentRunMeta`。

## 6.5 turn 完成后清空 meta

```go
state.Status = SessionIdle
state.CurrentRunMeta = nil
```

---

# 七、把 RunMeta 放进 context

建议你加两个 helper。

```go
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

然后在真正调 `ToolExecutor.Authorize/Execute` 之前：

```go
execCtx := WithRunMeta(ctx, state.CurrentRunMeta)
decision, err := a.Tools.Authorize(execCtx, a.ID, call)
result, err := a.Tools.Execute(execCtx, a.ID, call)
```

这一步很关键。
没有它，`SendTeamMessage` / `ReadMailboxDigest` 在暂停恢复后拿不到 team 身份。

---

# 八、`ReadMailboxDigest` 工具骨架

下面给你一个可直接落的版本。

```go
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"your/module/internal/runtime/chat"
	"your/module/internal/team"
)

var (
	ErrNoTeamRunMeta = errors.New("no team run metadata")
)

type TeamMailReader interface {
	ListUnreadMail(ctx context.Context, teamID, agentID string, limit int) ([]team.MailMessage, error)
	AckMail(ctx context.Context, teamID, agentID string, msgIDs []string, at time.Time) error
}

type ReadMailboxDigestArgs struct {
	Limit        int      `json:"limit,omitempty"`
	Kinds        []string `json:"kinds,omitempty"`
	MarkRead     bool     `json:"mark_read,omitempty"`
	MaxBodyChars int      `json:"max_body_chars,omitempty"`
}

type MailDigestItem struct {
	MessageID string    `json:"message_id"`
	FromAgent string    `json:"from_agent"`
	Kind      string    `json:"kind"`
	TaskID    string    `json:"task_id,omitempty"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type ReadMailboxDigestResult struct {
	Count  int              `json:"count"`
	Digest string           `json:"digest"`
	Items  []MailDigestItem `json:"items"`
}

type ReadMailboxDigestTool struct {
	Repo TeamMailReader
	Now  func() time.Time
}

func NewReadMailboxDigestTool(repo TeamMailReader, now func() time.Time) *ReadMailboxDigestTool {
	if now == nil {
		now = time.Now
	}
	return &ReadMailboxDigestTool{
		Repo: repo,
		Now:  now,
	}
}

func (t *ReadMailboxDigestTool) Name() string {
	return "ReadMailboxDigest"
}

func (t *ReadMailboxDigestTool) Execute(ctx context.Context, raw json.RawMessage) (*ReadMailboxDigestResult, error) {
	var args ReadMailboxDigestArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("parse args: %w", err)
	}

	meta, ok := chat.GetRunMeta(ctx)
	if !ok || meta == nil || meta.Team == nil {
		return nil, ErrNoTeamRunMeta
	}

	if args.Limit <= 0 {
		args.Limit = 10
	}
	if args.MaxBodyChars <= 0 {
		args.MaxBodyChars = 160
	}

	msgs, err := t.Repo.ListUnreadMail(ctx, meta.Team.TeamID, meta.Team.AgentID, args.Limit)
	if err != nil {
		return nil, err
	}

	filtered := make([]team.MailMessage, 0, len(msgs))
	if len(args.Kinds) == 0 {
		filtered = msgs
	} else {
		want := map[string]struct{}{}
		for _, k := range args.Kinds {
			want[k] = struct{}{}
		}
		for _, m := range msgs {
			if _, ok := want[m.Kind]; ok {
				filtered = append(filtered, m)
			}
		}
	}

	items := make([]MailDigestItem, 0, len(filtered))
	lines := make([]string, 0, len(filtered))
	ackIDs := make([]string, 0, len(filtered))

	for _, m := range filtered {
		body := truncate(m.Body, args.MaxBodyChars)

		var taskID string
		if m.TaskID != nil {
			taskID = *m.TaskID
		}

		items = append(items, MailDigestItem{
			MessageID: m.ID,
			FromAgent: m.FromAgent,
			Kind:      m.Kind,
			TaskID:    taskID,
			Body:      body,
			CreatedAt: m.CreatedAt,
		})

		line := fmt.Sprintf("- [%s] from %s", m.Kind, m.FromAgent)
		if taskID != "" {
			line += fmt.Sprintf(" (task %s)", taskID)
		}
		line += fmt.Sprintf(": %s", body)
		lines = append(lines, line)

		ackIDs = append(ackIDs, m.ID)
	}

	if args.MarkRead && len(ackIDs) > 0 {
		if err := t.Repo.AckMail(ctx, meta.Team.TeamID, meta.Team.AgentID, ackIDs, t.Now()); err != nil {
			return nil, err
		}
	}

	return &ReadMailboxDigestResult{
		Count:  len(items),
		Digest: strings.Join(lines, "\n"),
		Items:  items,
	}, nil
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
```

---

# 九、`SendTeamMessage` 也建议切到 RunMeta

你上一轮那版 `SendTeamMessage` 用 `TeamContextProvider` 也能做，但现在既然已经有 `RunMeta`，建议统一。

核心改法就是：

```go
meta, ok := chat.GetRunMeta(ctx)
if !ok || meta == nil || meta.Team == nil {
    return nil, ErrNoTeamRunMeta
}
teamID := meta.Team.TeamID
fromAgent := meta.Team.AgentID
currentTaskID := meta.Team.CurrentTaskID
```

这样两类 team tool 的身份来源完全一致，不会有两套上下文注入方式。

---

# 十、Orchestrator 需要一个小补丁

你上一轮 `SessionFacade` 只有：

```go
SubmitPrompt(ctx, sessionID, text string) error
```

现在要改成：

```go
type SessionFacade interface {
	SubmitPrompt(ctx context.Context, sessionID string, text string, meta *chat.RunMeta) error
}
```

然后 `dispatchOne()` 里调用：

```go
meta := &chat.RunMeta{
	Team: &chat.TeamRunMeta{
		TeamID:        team.ID,
		AgentID:       mate.ID,
		CurrentTaskID: task.ID,
	},
}

if err := o.Sessions.SubmitPrompt(ctx, mate.SessionID, prompt, meta); err != nil {
	...
}
```

这样 teammate turn 里，tool 才能知道：

* 我属于哪个 team
* 我是谁
* 我现在在干哪个 task

---

# 十一、`handleBlockedMail()` 要改成 `BlockTask()`

上一轮我提到这个改动，这里顺手补上。

```go
func (o *Orchestrator) handleBlockedMail(ctx context.Context, msg MailMessage) error {
	taskID, _ := msg.Metadata["task_id"].(string)
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

原因很简单：
`blocked` 和 `failed` 是不同状态，不要混成一个。

---

# 十二、现在你最值得写的 4 个增量文件

如果按“最小闭环”推进，我建议下一批直接落：

```text
internal/team/sqlite_repo.go
internal/runtime/tools/send_team_message.go
internal/runtime/tools/read_mailbox_digest.go
internal/runtime/chat/runmeta.go
```

以及两个已有文件的小 patch：

```text
internal/runtime/chat/actor.go
internal/runtime/chat/runtime_state_store.go
```

---

# 十三、这一轮完成后能跑通什么

完成上面这些后，你应该已经能跑通：

* teammate 接到 task prompt
* teammate 在 turn 中调用 `ReadMailboxDigest`
* teammate 完成后调用 `SendTeamMessage(kind=done)`
* orchestrator 消费 mailbox 并完成 task
* 广播消息不会因为一个 agent 已读而被其他 agent 丢失
* tool pause/resume 后 team 身份不丢

这时 Team 的“最小通信闭环”就真的成立了。

下一条我可以继续直接给你：

`sqlite_repo.go` 的 `BlockTask()` / `ListUnreadMail()` 完整最终版整合、`SendTeamMessage` 切到 `RunMeta` 的最终代码，以及 `SessionFacade` / actor 的补丁代码。
