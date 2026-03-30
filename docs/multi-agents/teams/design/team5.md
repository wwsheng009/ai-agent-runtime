下面我直接给你“**第一批可以落仓库的 Go 骨架**”。

这一版目标不是把所有细节一次性写满，而是把 **控制流闭环** 先搭起来，让后面的 Hook、审批、提问、Checkpoint、Teams 都能挂进去。

我按 6 个文件给：

1. `internal/runtime/chat/actor.go`
2. `internal/runtime/chat/runtime_state_store.go`
3. `internal/runtime/checkpoint/manager.go`
4. `internal/team/repo.go`
5. `internal/team/path_guard.go`
6. `internal/team/orchestrator.go`

另外我会补一个很关键的说明：**Team 的“完成回报”不要新造一套复杂协议，第一版直接走 mailbox 即可**。也就是说，teammate 完成任务后发一条 `kind=done` 的 team mail 给 `__orchestrator__`，orchestrator 收到后落 task complete。这是最省改动的一版闭环。

---

# 1) `internal/runtime/chat/runtime_state_store.go`

这个文件很小，但 actor 能不能恢复，全靠它。

```go
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrStateNotFound = errors.New("runtime state not found")
)

type SessionStatus string

const (
	SessionIdle            SessionStatus = "idle"
	SessionRunning         SessionStatus = "running"
	SessionWaitingApproval SessionStatus = "waiting_approval"
	SessionWaitingInput    SessionStatus = "waiting_input"
	SessionRewinding       SessionStatus = "rewinding"
	SessionStopped         SessionStatus = "stopped"
)

type PendingToolInvocation struct {
	ToolCallID     string          `json:"tool_call_id"`
	ToolName       string          `json:"tool_name"`
	ArgsJSON       json.RawMessage `json:"args_json"`
	AssistantMsgID string          `json:"assistant_msg_id,omitempty"`
}

type ApprovalRequest struct {
	ID         string          `json:"id"`
	SessionID  string          `json:"session_id"`
	ToolName   string          `json:"tool_name"`
	ToolCallID string          `json:"tool_call_id"`
	ArgsJSON   json.RawMessage `json:"args_json"`
	Reason     string          `json:"reason"`
	RiskLevel  string          `json:"risk_level"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
}

type UserQuestionRequest struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	ToolCallID  string    `json:"tool_call_id"`
	Prompt      string    `json:"prompt"`
	Suggestions []string  `json:"suggestions,omitempty"`
	Required    bool      `json:"required"`
	CreatedAt   time.Time `json:"created_at"`
}

type RuntimeState struct {
	SessionID       string               `json:"session_id"`
	Status          SessionStatus        `json:"status"`
	PendingTool     *PendingToolInvocation `json:"pending_tool,omitempty"`
	PendingApproval *ApprovalRequest     `json:"pending_approval,omitempty"`
	PendingQuestion *UserQuestionRequest `json:"pending_question,omitempty"`

	// conversation rewind 用
	VisibleUntilSeq int64     `json:"visible_until_seq"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type RuntimeStateStore interface {
	Load(ctx context.Context, sessionID string) (*RuntimeState, error)
	Save(ctx context.Context, state *RuntimeState) error
	Delete(ctx context.Context, sessionID string) error
}

func DefaultRuntimeState(sessionID string, now time.Time) *RuntimeState {
	return &RuntimeState{
		SessionID:       sessionID,
		Status:          SessionIdle,
		VisibleUntilSeq: 0,
		UpdatedAt:       now,
	}
}
```

---

# 2) `internal/runtime/chat/actor.go`

这是你后面所有未完成功能的“胶水层”。

### 这版 actor 解决的核心问题

* 一个 session 不能并发跑多个 turn
* tool ask 可以暂停
* `AskUserQuestion` 可以暂停
* 用户批准 / 回答后可以继续
* rewind 进入 session 生命周期，而不是 session 外乱改数据

---

```go
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrSessionBusy        = errors.New("session is busy")
	ErrTurnPaused         = errors.New("turn paused")
	ErrNoPendingApproval  = errors.New("no pending approval")
	ErrApprovalMismatch   = errors.New("approval request mismatch")
	ErrNoPendingQuestion  = errors.New("no pending question")
	ErrQuestionMismatch   = errors.New("question request mismatch")
	ErrToolDenied         = errors.New("tool denied")
	ErrInvalidCommand     = errors.New("invalid command")
)

type Command interface{ isCommand() }

type SubmitPrompt struct {
	Text string
}
func (SubmitPrompt) isCommand() {}

type ApproveTool struct {
	RequestID   string
	Allow       bool
	PatchedArgs json.RawMessage
}
func (ApproveTool) isCommand() {}

type AnswerQuestion struct {
	QuestionID string
	Answer     string
}
func (AnswerQuestion) isCommand() {}

type RewindTo struct {
	CheckpointID string
	Mode         string // code|conversation|both
}
func (RewindTo) isCommand() {}

type Interrupt struct{}
func (Interrupt) isCommand() {}

type EventType string

const (
	EventAssistantDelta    EventType = "assistant_delta"
	EventToolStarted       EventType = "tool_started"
	EventToolFinished      EventType = "tool_finished"
	EventApprovalRequested EventType = "approval_requested"
	EventQuestionAsked     EventType = "question_asked"
	EventCheckpointCreated EventType = "checkpoint_created"
	EventRewindStarted     EventType = "rewind_started"
	EventRewindFinished    EventType = "rewind_finished"
	EventTurnCompleted     EventType = "turn_completed"
	EventTurnFailed        EventType = "turn_failed"
)

type Event struct {
	SessionID string    `json:"session_id"`
	Seq       int64     `json:"seq"`
	Type      EventType `json:"type"`
	Payload   []byte    `json:"payload"`
	At        time.Time `json:"at"`
}

type EventHub interface {
	Publish(ctx context.Context, evt Event) error
}

type RunRequest struct {
	SessionID    string
	Input        string
	ContinueOnly bool
}

type ToolCall struct {
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	ArgsJSON   json.RawMessage `json:"args_json"`
}

type ToolResult struct {
	Success bool           `json:"success"`
	Output  string         `json:"output"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type TurnCallbacks struct {
	OnAssistantDelta func(text string) error
	OnToolCall       func(call ToolCall) error
	OnDone           func() error
}

type TurnRunner interface {
	Run(ctx context.Context, req RunRequest, cb TurnCallbacks) error
}

type DecisionType string

const (
	DecisionAllow DecisionType = "allow"
	DecisionDeny  DecisionType = "deny"
	DecisionAsk   DecisionType = "ask"
)

type Decision struct {
	Type    DecisionType   `json:"type"`
	Reason  string         `json:"reason,omitempty"`
	Risk    string         `json:"risk,omitempty"`
	Patched json.RawMessage `json:"patched,omitempty"`
}

type ToolExecutor interface {
	Authorize(ctx context.Context, sessionID string, call ToolCall) (Decision, error)
	Execute(ctx context.Context, sessionID string, call ToolCall) (ToolResult, error)
	AppendToolResult(ctx context.Context, sessionID, toolCallID string, result ToolResult) error
}

type Rewinder interface {
	Restore(ctx context.Context, sessionID, checkpointID, mode string) error
}

type Actor struct {
	ID         string
	CmdCh      chan Command
	Hub        EventHub
	StateStore RuntimeStateStore
	Runner     TurnRunner
	Tools      ToolExecutor
	Rewinder   Rewinder
	Now        func() time.Time
}

type AskUserQuestionArgs struct {
	Prompt      string   `json:"prompt"`
	Suggestions []string `json:"suggestions,omitempty"`
	Required    bool     `json:"required"`
}

func NewActor(
	id string,
	hub EventHub,
	stateStore RuntimeStateStore,
	runner TurnRunner,
	tools ToolExecutor,
	rewinder Rewinder,
	now func() time.Time,
) *Actor {
	if now == nil {
		now = time.Now
	}
	return &Actor{
		ID:         id,
		CmdCh:      make(chan Command, 32),
		Hub:        hub,
		StateStore: stateStore,
		Runner:     runner,
		Tools:      tools,
		Rewinder:   rewinder,
		Now:        now,
	}
}

func (a *Actor) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cmd := <-a.CmdCh:
			if err := a.handle(ctx, cmd); err != nil {
				return err
			}
		}
	}
}

func (a *Actor) handle(ctx context.Context, cmd Command) error {
	switch c := cmd.(type) {
	case SubmitPrompt:
		return a.handleSubmit(ctx, c)
	case ApproveTool:
		return a.handleApprove(ctx, c)
	case AnswerQuestion:
		return a.handleAnswer(ctx, c)
	case RewindTo:
		return a.handleRewind(ctx, c)
	case Interrupt:
		// 第一版先留空：后面可以接 runner cancel 或 background job interrupt
		return nil
	default:
		return ErrInvalidCommand
	}
}

func (a *Actor) handleSubmit(ctx context.Context, cmd SubmitPrompt) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}
	if state.Status != SessionIdle {
		return ErrSessionBusy
	}

	state.Status = SessionRunning
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	return a.runTurn(ctx, cmd.Text, false)
}

func (a *Actor) handleApprove(ctx context.Context, cmd ApproveTool) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}
	if state.Status != SessionWaitingApproval || state.PendingTool == nil || state.PendingApproval == nil {
		return ErrNoPendingApproval
	}
	if state.PendingApproval.ID != cmd.RequestID {
		return ErrApprovalMismatch
	}

	if !cmd.Allow {
		denied := ToolResult{
			Success: false,
			Output:  "user denied tool execution",
		}
		if err := a.Tools.AppendToolResult(ctx, a.ID, state.PendingTool.ToolCallID, denied); err != nil {
			return err
		}
	} else {
		args := state.PendingTool.ArgsJSON
		if len(cmd.PatchedArgs) > 0 {
			args = cmd.PatchedArgs
		}
		result, err := a.Tools.Execute(ctx, a.ID, ToolCall{
			ToolCallID: state.PendingTool.ToolCallID,
			ToolName:   state.PendingTool.ToolName,
			ArgsJSON:   args,
		})
		if err != nil {
			return err
		}
		if err := a.Tools.AppendToolResult(ctx, a.ID, state.PendingTool.ToolCallID, result); err != nil {
			return err
		}
	}

	state.Status = SessionRunning
	state.PendingTool = nil
	state.PendingApproval = nil
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	return a.runTurn(ctx, "", true)
}

func (a *Actor) handleAnswer(ctx context.Context, cmd AnswerQuestion) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}
	if state.Status != SessionWaitingInput || state.PendingQuestion == nil || state.PendingTool == nil {
		return ErrNoPendingQuestion
	}
	if state.PendingQuestion.ID != cmd.QuestionID {
		return ErrQuestionMismatch
	}

	result := ToolResult{
		Success: true,
		Output:  cmd.Answer,
	}
	if err := a.Tools.AppendToolResult(ctx, a.ID, state.PendingTool.ToolCallID, result); err != nil {
		return err
	}

	state.Status = SessionRunning
	state.PendingTool = nil
	state.PendingQuestion = nil
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	return a.runTurn(ctx, "", true)
}

func (a *Actor) handleRewind(ctx context.Context, cmd RewindTo) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}
	if state.Status != SessionIdle {
		return ErrSessionBusy
	}

	state.Status = SessionRewinding
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	if err := a.Hub.Publish(ctx, Event{
		SessionID: a.ID,
		Type:      EventRewindStarted,
		Payload:   mustJSON(cmd),
		At:        a.Now(),
	}); err != nil {
		return err
	}

	if err := a.Rewinder.Restore(ctx, a.ID, cmd.CheckpointID, cmd.Mode); err != nil {
		state.Status = SessionIdle
		state.UpdatedAt = a.Now()
		_ = a.StateStore.Save(ctx, state)
		return err
	}

	state.Status = SessionIdle
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	return a.Hub.Publish(ctx, Event{
		SessionID: a.ID,
		Type:      EventRewindFinished,
		Payload:   mustJSON(cmd),
		At:        a.Now(),
	})
}

func (a *Actor) runTurn(ctx context.Context, input string, continueOnly bool) error {
	err := a.Runner.Run(ctx, RunRequest{
		SessionID:    a.ID,
		Input:        input,
		ContinueOnly: continueOnly,
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
		_ = a.resetToIdle(ctx)
		_ = a.Hub.Publish(ctx, Event{
			SessionID: a.ID,
			Type:      EventTurnFailed,
			Payload:   mustJSON(map[string]string{"error": err.Error()}),
			At:        a.Now(),
		})
		return err
	}

	if err := a.resetToIdle(ctx); err != nil {
		return err
	}

	return a.Hub.Publish(ctx, Event{
		SessionID: a.ID,
		Type:      EventTurnCompleted,
		Payload:   mustJSON(map[string]any{}),
		At:        a.Now(),
	})
}

func (a *Actor) onToolCall(ctx context.Context, call ToolCall) error {
	if err := a.Hub.Publish(ctx, Event{
		SessionID: a.ID,
		Type:      EventToolStarted,
		Payload:   mustJSON(call),
		At:        a.Now(),
	}); err != nil {
		return err
	}

	// 内建挂起工具：AskUserQuestion
	if call.ToolName == "AskUserQuestion" {
		var args AskUserQuestionArgs
		if err := json.Unmarshal(call.ArgsJSON, &args); err != nil {
			return fmt.Errorf("parse AskUserQuestion args: %w", err)
		}
		return a.pauseForQuestion(ctx, call, args)
	}

	decision, err := a.Tools.Authorize(ctx, a.ID, call)
	if err != nil {
		return err
	}

	switch decision.Type {
	case DecisionAllow:
		args := call.ArgsJSON
		if len(decision.Patched) > 0 {
			args = decision.Patched
		}

		result, err := a.Tools.Execute(ctx, a.ID, ToolCall{
			ToolCallID: call.ToolCallID,
			ToolName:   call.ToolName,
			ArgsJSON:   args,
		})
		if err != nil {
			return err
		}
		if err := a.Tools.AppendToolResult(ctx, a.ID, call.ToolCallID, result); err != nil {
			return err
		}
		return a.Hub.Publish(ctx, Event{
			SessionID: a.ID,
			Type:      EventToolFinished,
			Payload:   mustJSON(map[string]any{"tool_call_id": call.ToolCallID, "success": result.Success}),
			At:        a.Now(),
		})

	case DecisionAsk:
		return a.pauseForApproval(ctx, call, decision)

	case DecisionDeny:
		denied := ToolResult{
			Success: false,
			Output:  "tool denied by permission engine",
		}
		if err := a.Tools.AppendToolResult(ctx, a.ID, call.ToolCallID, denied); err != nil {
			return err
		}
		return a.Hub.Publish(ctx, Event{
			SessionID: a.ID,
			Type:      EventToolFinished,
			Payload:   mustJSON(map[string]any{"tool_call_id": call.ToolCallID, "success": false}),
			At:        a.Now(),
		})

	default:
		return ErrToolDenied
	}
}

func (a *Actor) pauseForApproval(ctx context.Context, call ToolCall, decision Decision) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}

	req := &ApprovalRequest{
		ID:         newID(),
		SessionID:  a.ID,
		ToolName:   call.ToolName,
		ToolCallID: call.ToolCallID,
		ArgsJSON:   call.ArgsJSON,
		Reason:     decision.Reason,
		RiskLevel:  decision.Risk,
		CreatedAt:  a.Now(),
		ExpiresAt:  a.Now().Add(30 * time.Minute),
	}

	state.Status = SessionWaitingApproval
	state.PendingTool = &PendingToolInvocation{
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		ArgsJSON:   call.ArgsJSON,
	}
	state.PendingApproval = req
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	if err := a.Hub.Publish(ctx, Event{
		SessionID: a.ID,
		Type:      EventApprovalRequested,
		Payload:   mustJSON(req),
		At:        a.Now(),
	}); err != nil {
		return err
	}

	return ErrTurnPaused
}

func (a *Actor) pauseForQuestion(ctx context.Context, call ToolCall, args AskUserQuestionArgs) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}

	req := &UserQuestionRequest{
		ID:          newID(),
		SessionID:   a.ID,
		ToolCallID:  call.ToolCallID,
		Prompt:      args.Prompt,
		Suggestions: args.Suggestions,
		Required:    args.Required,
		CreatedAt:   a.Now(),
	}

	state.Status = SessionWaitingInput
	state.PendingTool = &PendingToolInvocation{
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		ArgsJSON:   call.ArgsJSON,
	}
	state.PendingQuestion = req
	state.UpdatedAt = a.Now()
	if err := a.StateStore.Save(ctx, state); err != nil {
		return err
	}

	if err := a.Hub.Publish(ctx, Event{
		SessionID: a.ID,
		Type:      EventQuestionAsked,
		Payload:   mustJSON(req),
		At:        a.Now(),
	}); err != nil {
		return err
	}

	return ErrTurnPaused
}

func (a *Actor) loadOrInitState(ctx context.Context) (*RuntimeState, error) {
	state, err := a.StateStore.Load(ctx, a.ID)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, ErrStateNotFound) {
		return nil, err
	}
	state = DefaultRuntimeState(a.ID, a.Now())
	if err := a.StateStore.Save(ctx, state); err != nil {
		return nil, err
	}
	return state, nil
}

func (a *Actor) resetToIdle(ctx context.Context) error {
	state, err := a.loadOrInitState(ctx)
	if err != nil {
		return err
	}
	state.Status = SessionIdle
	state.UpdatedAt = a.Now()
	return a.StateStore.Save(ctx, state)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// TODO: 换成你项目里的 uuid/ksuid 实现
func newID() string {
	return fmt.Sprintf("id_%d", time.Now().UnixNano())
}
```

---

## 这份 actor 有几个实现要点你要注意

第一，**暂停恢复不需要序列化 Go 调用栈**。
只要 tool call 已经进入 transcript，你把 pending tool 存起来，等审批/回答回来后补写 tool result，再继续 loop 即可。

第二，`AskUserQuestion` 被我当作**内建挂起工具**处理，不是普通外部 tool。
这正好符合你现在这套 runtime 的增量改造方式。

第三，`RewindTo` 已经进入 actor 生命周期。
后面 UI/CLI 只要发命令，不要直接去改 DB 或工作区。

---

# 3) `internal/runtime/checkpoint/manager.go`

这版 checkpoint manager 重点是三件事：

* `BeforeMutation`
* `AfterMutation`
* `Restore(code|conversation|both)`

它先不处理 bash 的完整可逆，只把**文件编辑工具**这条链先做扎实。

---

```go
package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidMode = errors.New("invalid rewind mode")
)

type RewindMode string

const (
	RewindCode         RewindMode = "code"
	RewindConversation RewindMode = "conversation"
	RewindBoth         RewindMode = "both"
)

type Checkpoint struct {
	ID              string    `json:"id"`
	SessionID       string    `json:"session_id"`
	TurnID          string    `json:"turn_id"`
	ParentID        *string   `json:"parent_id,omitempty"`
	Mode            string    `json:"mode"`
	Summary         string    `json:"summary,omitempty"`
	HistoryHash     string    `json:"history_hash,omitempty"`
	VisibleUntilSeq int64     `json:"visible_until_seq"`
	CreatedAt       time.Time `json:"created_at"`
}

type CheckpointFile struct {
	ID           string  `json:"id"`
	CheckpointID string  `json:"checkpoint_id"`
	Path         string  `json:"path"`
	Op           string  `json:"op"` // create|update|delete
	BeforeBlobID *string `json:"before_blob_id,omitempty"`
	AfterBlobID  *string `json:"after_blob_id,omitempty"`
	BeforeHash   *string `json:"before_hash,omitempty"`
	AfterHash    *string `json:"after_hash,omitempty"`
	DiffText     []byte  `json:"diff_text,omitempty"`
}

type Blob struct {
	ID       string `json:"id"`
	SHA256   string `json:"sha256"`
	Encoding string `json:"encoding"`
	Data     []byte `json:"data"`
}

type Repo interface {
	CreateCheckpoint(ctx context.Context, cp Checkpoint) error
	AddCheckpointFiles(ctx context.Context, files []CheckpointFile) error
	GetCheckpoint(ctx context.Context, sessionID, checkpointID string) (*Checkpoint, error)
	ListCheckpointsAfter(ctx context.Context, sessionID, checkpointID string) ([]Checkpoint, error)
	ListCheckpointFiles(ctx context.Context, checkpointID string) ([]CheckpointFile, error)

	SaveBlob(ctx context.Context, blob Blob) (string, error)
	LoadBlob(ctx context.Context, blobID string) ([]byte, error)

	GetConversationHead(ctx context.Context, sessionID string) (int64, error)
	SetConversationHead(ctx context.Context, sessionID string, visibleUntilSeq int64) error
	InvalidateSummariesAfter(ctx context.Context, sessionID string, seq int64) error
}

type Workspace interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	RemoveFile(path string) error
	Exists(path string) (bool, error)
}

type DiffEngine interface {
	Unified(path string, before, after []byte) (string, error)
}

type Manager struct {
	Repo      Repo
	Workspace Workspace
	Diff      DiffEngine
	Now       func() time.Time
}

type FileSnapshot struct {
	Exists bool
	BlobID *string
	Hash   *string
}

type CaptureHandle struct {
	CheckpointID string
	SessionID    string
	TurnID       string
	Paths        []string
	Before       map[string]*FileSnapshot
	VisibleSeq   int64
}

type PreviewFile struct {
	Path     string `json:"path"`
	Action   string `json:"action"`
	DiffText string `json:"diff_text"`
}

type RestorePreview struct {
	CheckpointID string        `json:"checkpoint_id"`
	Mode         string        `json:"mode"`
	Files        []PreviewFile `json:"files"`
}

func NewManager(repo Repo, ws Workspace, diff DiffEngine, now func() time.Time) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{
		Repo:      repo,
		Workspace: ws,
		Diff:      diff,
		Now:       now,
	}
}

func (m *Manager) BeforeMutation(
	ctx context.Context,
	sessionID, turnID string,
	paths []string,
	visibleSeq int64,
) (*CaptureHandle, error) {
	h := &CaptureHandle{
		CheckpointID: newID(),
		SessionID:    sessionID,
		TurnID:       turnID,
		Paths:        paths,
		Before:       map[string]*FileSnapshot{},
		VisibleSeq:   visibleSeq,
	}

	cp := Checkpoint{
		ID:              h.CheckpointID,
		SessionID:       sessionID,
		TurnID:          turnID,
		Mode:            string(RewindCode),
		VisibleUntilSeq: visibleSeq,
		CreatedAt:       m.Now(),
	}
	if err := m.Repo.CreateCheckpoint(ctx, cp); err != nil {
		return nil, err
	}

	for _, p := range paths {
		exists, err := m.Workspace.Exists(p)
		if err != nil {
			return nil, err
		}
		snap := &FileSnapshot{Exists: exists}
		if exists {
			b, err := m.Workspace.ReadFile(p)
			if err != nil {
				return nil, err
			}
			blobID, err := m.Repo.SaveBlob(ctx, BlobFromBytes(b))
			if err != nil {
				return nil, err
			}
			hash := HashBytes(b)
			snap.BlobID = &blobID
			snap.Hash = &hash
		}
		h.Before[p] = snap
	}

	return h, nil
}

func (m *Manager) AfterMutation(ctx context.Context, h *CaptureHandle) error {
	rows := make([]CheckpointFile, 0, len(h.Paths))

	for _, p := range h.Paths {
		before := h.Before[p]

		exists, err := m.Workspace.Exists(p)
		if err != nil {
			return err
		}

		var afterData []byte
		var afterBlobID *string
		var afterHash *string

		if exists {
			afterData, err = m.Workspace.ReadFile(p)
			if err != nil {
				return err
			}
			blobID, err := m.Repo.SaveBlob(ctx, BlobFromBytes(afterData))
			if err != nil {
				return err
			}
			hash := HashBytes(afterData)
			afterBlobID = &blobID
			afterHash = &hash
		}

		var beforeData []byte
		if before.BlobID != nil {
			beforeData, err = m.Repo.LoadBlob(ctx, *before.BlobID)
			if err != nil {
				return err
			}
		}

		diffText, err := m.Diff.Unified(p, beforeData, afterData)
		if err != nil {
			return err
		}

		rows = append(rows, CheckpointFile{
			ID:           newID(),
			CheckpointID: h.CheckpointID,
			Path:         p,
			Op:           detectOp(before.Exists, exists),
			BeforeBlobID: before.BlobID,
			AfterBlobID:  afterBlobID,
			BeforeHash:   before.Hash,
			AfterHash:    afterHash,
			DiffText:     []byte(diffText),
		})
	}

	return m.Repo.AddCheckpointFiles(ctx, rows)
}

func (m *Manager) Restore(ctx context.Context, sessionID, checkpointID string, mode string) error {
	switch RewindMode(mode) {
	case RewindCode:
		return m.RestoreCode(ctx, sessionID, checkpointID)
	case RewindConversation:
		return m.RestoreConversation(ctx, sessionID, checkpointID)
	case RewindBoth:
		if err := m.RestoreCode(ctx, sessionID, checkpointID); err != nil {
			return err
		}
		return m.RestoreConversation(ctx, sessionID, checkpointID)
	default:
		return ErrInvalidMode
	}
}

func (m *Manager) RestoreCode(ctx context.Context, sessionID, checkpointID string) error {
	// 返回目标 checkpoint 之后的所有 checkpoints，按时间正序
	cps, err := m.Repo.ListCheckpointsAfter(ctx, sessionID, checkpointID)
	if err != nil {
		return err
	}

	// 倒序回放，等价于“撤销目标点之后的所有 mutation”
	for i := len(cps) - 1; i >= 0; i-- {
		files, err := m.Repo.ListCheckpointFiles(ctx, cps[i].ID)
		if err != nil {
			return err
		}

		for _, f := range files {
			switch f.Op {
			case "create":
				// 这个文件是在后续步骤创建的，恢复时删除
				if err := m.Workspace.RemoveFile(f.Path); err != nil {
					return err
				}
			case "update", "delete":
				if f.BeforeBlobID == nil {
					// 原来不存在，恢复时删除
					if err := m.Workspace.RemoveFile(f.Path); err != nil {
						return err
					}
					continue
				}
				b, err := m.Repo.LoadBlob(ctx, *f.BeforeBlobID)
				if err != nil {
					return err
				}
				if err := m.Workspace.WriteFile(f.Path, b); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unknown checkpoint op: %s", f.Op)
			}
		}
	}

	return nil
}

func (m *Manager) RestoreConversation(ctx context.Context, sessionID, checkpointID string) error {
	cp, err := m.Repo.GetCheckpoint(ctx, sessionID, checkpointID)
	if err != nil {
		return err
	}

	if err := m.Repo.SetConversationHead(ctx, sessionID, cp.VisibleUntilSeq); err != nil {
		return err
	}
	return m.Repo.InvalidateSummariesAfter(ctx, sessionID, cp.VisibleUntilSeq)
}

func (m *Manager) PreviewRestore(ctx context.Context, sessionID, checkpointID, mode string) (*RestorePreview, error) {
	out := &RestorePreview{
		CheckpointID: checkpointID,
		Mode:         mode,
	}

	if RewindMode(mode) == RewindConversation {
		return out, nil
	}

	cps, err := m.Repo.ListCheckpointsAfter(ctx, sessionID, checkpointID)
	if err != nil {
		return nil, err
	}

	for i := len(cps) - 1; i >= 0; i-- {
		files, err := m.Repo.ListCheckpointFiles(ctx, cps[i].ID)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			out.Files = append(out.Files, PreviewFile{
				Path:     f.Path,
				Action:   "revert_" + f.Op,
				DiffText: string(f.DiffText),
			})
		}
	}
	return out, nil
}

func detectOp(beforeExists, afterExists bool) string {
	switch {
	case !beforeExists && afterExists:
		return "create"
	case beforeExists && !afterExists:
		return "delete"
	default:
		return "update"
	}
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func BlobFromBytes(b []byte) Blob {
	return Blob{
		ID:       newID(),
		SHA256:   HashBytes(b),
		Encoding: "raw",
		Data:     b,
	}
}

func newID() string {
	return fmt.Sprintf("id_%d", time.Now().UnixNano())
}
```

---

## 这份 checkpoint manager 的关键实现点

第一，`RestoreCode()` 是**反向回放**。
不是直接“把目标 checkpoint 覆盖回去”，而是把目标之后所有 mutation 逐个撤销。这种方式最适合你当前架构。

第二，`conversation rewind` 只改 `VisibleUntilSeq`。
不要删除 transcript，不要硬删历史消息。

第三，这一版默认只覆盖**文件编辑工具**。
bash 修改先不做完全可逆，这个是合理分阶段，不要第一版就把系统搞复杂。

---

# 4) `internal/team/repo.go`

这个 repo 接口是 Team 的真相源。
后面的 orchestrator、mailbox、path claim、lease 都从这里进。

---

```go
package team

import (
	"context"
	"time"
)

const OrchestratorAgentID = "__orchestrator__"

type TeamStatus string

const (
	TeamActive TeamStatus = "active"
	TeamPaused TeamStatus = "paused"
	TeamDone   TeamStatus = "done"
	TeamFailed TeamStatus = "failed"
)

type TeammateState string

const (
	TeammateIdle    TeammateState = "idle"
	TeammateBusy    TeammateState = "busy"
	TeammateBlocked TeammateState = "blocked"
	TeammateOffline TeammateState = "offline"
)

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskReady     TaskStatus = "ready"
	TaskRunning   TaskStatus = "running"
	TaskBlocked   TaskStatus = "blocked"
	TaskDone      TaskStatus = "done"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

type ClaimMode string

const (
	ClaimRead  ClaimMode = "read"
	ClaimWrite ClaimMode = "write"
)

type Team struct {
	ID            string
	WorkspaceID   string
	LeadSessionID string
	Status        TeamStatus
	Strategy      string
	MaxTeammates  int
	MaxWriters    int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Teammate struct {
	ID            string
	TeamID        string
	Name          string
	Profile       string
	SessionID     string
	State         TeammateState
	LastHeartbeat time.Time
	Capabilities  []string
	Metadata      map[string]any
}

type Task struct {
	ID           string
	TeamID       string
	ParentTaskID *string
	Title        string
	Goal         string
	Inputs       []string
	Status       TaskStatus
	Priority     int
	Assignee     *string
	LeaseUntil   *time.Time
	RetryCount   int

	ReadPaths    []string
	WritePaths   []string
	Deliverables []string

	Summary   string
	ResultRef *string
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type TaskDependency struct {
	TaskID          string
	DependsOnTaskID string
}

type PathClaim struct {
	ID           string
	TeamID       string
	TaskID       string
	OwnerAgentID string
	Path         string
	Mode         ClaimMode
	LeaseUntil   time.Time
}

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

type TeamEvent struct {
	TeamID    string
	Seq       int64
	Type      string
	Payload   []byte
	CreatedAt time.Time
}

type Repo interface {
	WithTx(ctx context.Context, fn func(TxRepo) error) error

	// Team
	CreateTeam(ctx context.Context, t Team) error
	GetTeam(ctx context.Context, teamID string) (*Team, error)
	UpdateTeamStatus(ctx context.Context, teamID string, status TeamStatus) error

	// Teammate
	UpsertTeammate(ctx context.Context, mate Teammate) error
	GetTeammate(ctx context.Context, teamID, teammateID string) (*Teammate, error)
	ListTeammates(ctx context.Context, teamID string) ([]Teammate, error)
	ListIdleTeammates(ctx context.Context, teamID string) ([]Teammate, error)
	UpdateTeammateState(ctx context.Context, teamID, teammateID string, state TeammateState) error
	HeartbeatTeammate(ctx context.Context, teamID, teammateID string, at time.Time) error

	// Task
	InsertTasks(ctx context.Context, tasks []Task, deps []TaskDependency) error
	GetTask(ctx context.Context, teamID, taskID string) (*Task, error)
	ListTasks(ctx context.Context, teamID string) ([]Task, error)
	ListReadyTasks(ctx context.Context, teamID string, limit int) ([]Task, error)
	ListRunningTasks(ctx context.Context, teamID string) ([]Task, error)
	ListBlockedTasks(ctx context.Context, teamID string) ([]Task, error)

	ClaimTask(ctx context.Context, teamID, taskID, assignee string, expectedVersion int64, leaseUntil time.Time) (bool, error)
	RenewLease(ctx context.Context, teamID, taskID string, leaseUntil time.Time) error
	CompleteTask(ctx context.Context, teamID, taskID string, summary string, resultRef *string) error
	FailTask(ctx context.Context, teamID, taskID string, reason string, retryable bool) error
	RequeueExpiredTasks(ctx context.Context, teamID string, now time.Time) ([]Task, error)
	UnblockReadyTasks(ctx context.Context, teamID string) ([]Task, error)

	// Dependencies
	AddDependencies(ctx context.Context, deps []TaskDependency) error
	ListDependents(ctx context.Context, teamID, taskID string) ([]Task, error)

	// Path claims
	ListPathClaims(ctx context.Context, teamID string) ([]PathClaim, error)
	AcquirePathClaims(ctx context.Context, claims []PathClaim) error
	ReleasePathClaimsByTask(ctx context.Context, teamID, taskID string) error
	DeleteExpiredPathClaims(ctx context.Context, teamID string, now time.Time) error

	// Mailbox
	InsertMail(ctx context.Context, msg MailMessage) error
	ListUnreadMail(ctx context.Context, teamID, agentID string, limit int) ([]MailMessage, error)
	AckMail(ctx context.Context, teamID, agentID string, msgIDs []string, at time.Time) error

	// Events
	AppendEvent(ctx context.Context, evt TeamEvent) error
	ListEventsAfter(ctx context.Context, teamID string, afterSeq int64, limit int) ([]TeamEvent, error)
}

type TxRepo interface {
	Repo
}
```

---

# 5) `internal/team/path_guard.go`

这块必须单独抽出来。
Agent Teams 第一版最容易出事故的地方，就是两个 agent 同时写同一个目录或同一组文件。

### 这版 path guard 的策略

* read/read 允许
* read/write 冲突
* write/write 冲突
* 目录前缀覆盖子路径

而且有一个很关键的点：

**`ClaimTask + AcquirePathClaims` 必须在同一个事务里做。**
不然两个 orchestrator tick 或两个 goroutine 之间会出现竞争窗口。

---

```go
package team

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PathGuard struct {
	WorkspaceRoot string
}

func (g *PathGuard) CanClaim(ctx context.Context, repo Repo, teamID string, readPaths, writePaths []string) (bool, []string, error) {
	readPaths = g.normalizePaths(readPaths)
	writePaths = g.normalizePaths(writePaths)

	claims, err := repo.ListPathClaims(ctx, teamID)
	if err != nil {
		return false, nil, err
	}

	var conflicts []string
	for _, c := range claims {
		for _, p := range readPaths {
			if claimConflicts(c, p, ClaimRead) {
				conflicts = append(conflicts, c.Path)
			}
		}
		for _, p := range writePaths {
			if claimConflicts(c, p, ClaimWrite) {
				conflicts = append(conflicts, c.Path)
			}
		}
	}

	return len(conflicts) == 0, conflicts, nil
}

func (g *PathGuard) Acquire(
	ctx context.Context,
	repo Repo,
	teamID, taskID, owner string,
	readPaths, writePaths []string,
	leaseUntil time.Time,
) error {
	readPaths = g.normalizePaths(readPaths)
	writePaths = g.normalizePaths(writePaths)

	claims := make([]PathClaim, 0, len(readPaths)+len(writePaths))
	for _, p := range readPaths {
		claims = append(claims, PathClaim{
			ID:           newID(),
			TeamID:       teamID,
			TaskID:       taskID,
			OwnerAgentID: owner,
			Path:         p,
			Mode:         ClaimRead,
			LeaseUntil:   leaseUntil,
		})
	}
	for _, p := range writePaths {
		claims = append(claims, PathClaim{
			ID:           newID(),
			TeamID:       teamID,
			TaskID:       taskID,
			OwnerAgentID: owner,
			Path:         p,
			Mode:         ClaimWrite,
			LeaseUntil:   leaseUntil,
		})
	}
	return repo.AcquirePathClaims(ctx, claims)
}

func (g *PathGuard) normalizePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}

	for _, p := range paths {
		if p == "" {
			continue
		}
		clean := filepath.Clean(p)
		if !filepath.IsAbs(clean) {
			clean = filepath.Join(g.WorkspaceRoot, clean)
		}
		clean = filepath.Clean(clean)

		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func claimConflicts(existing PathClaim, candidatePath string, candidateMode ClaimMode) bool {
	if !pathOverlap(existing.Path, candidatePath) {
		return false
	}

	// read/read 不冲突
	if existing.Mode == ClaimRead && candidateMode == ClaimRead {
		return false
	}
	return true
}

func pathOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)

	if a == b {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}
```

---

# 6) `internal/team/orchestrator.go`

这是 Team 模块的真正控制平面。

这份 orchestrator 解决的是：

* 初始化 plan
* tick 调度
* 过期 lease 回收
* 路径 claim + task claim 原子化
* task 完成 / 失败通过 mailbox 回报
* stuck 时触发 replan
* 全部 done 后收尾

---

```go
package team

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

type SessionFacade interface {
	SubmitPrompt(ctx context.Context, sessionID string, text string) error
}

type LeadPlanner interface {
	InitialPlan(ctx context.Context, team Team, goal string) ([]Task, []TaskDependency, error)
	ReplanOnFailure(ctx context.Context, team Team, failed Task) ([]Task, []TaskDependency, error)
	FinalSummary(ctx context.Context, team Team) (string, error)
}

type Orchestrator struct {
	Repo      Repo
	Sessions  SessionFacade
	Planner   LeadPlanner
	PathGuard *PathGuard

	LeaseDuration time.Duration
	TickInterval  time.Duration
	Now           func() time.Time

	mu          sync.Mutex
	leaseCancels map[string]context.CancelFunc // key: taskID
}

func NewOrchestrator(
	repo Repo,
	sessions SessionFacade,
	planner LeadPlanner,
	pathGuard *PathGuard,
	leaseDuration time.Duration,
	tickInterval time.Duration,
	now func() time.Time,
) *Orchestrator {
	if now == nil {
		now = time.Now
	}
	if leaseDuration <= 0 {
		leaseDuration = 20 * time.Second
	}
	if tickInterval <= 0 {
		tickInterval = 1 * time.Second
	}

	return &Orchestrator{
		Repo:          repo,
		Sessions:      sessions,
		Planner:       planner,
		PathGuard:     pathGuard,
		LeaseDuration: leaseDuration,
		TickInterval:  tickInterval,
		Now:           now,
		leaseCancels:  map[string]context.CancelFunc{},
	}
}

func (o *Orchestrator) Start(ctx context.Context, team Team, goal string) error {
	if err := o.Repo.CreateTeam(ctx, team); err != nil {
		return err
	}

	tasks, deps, err := o.Planner.InitialPlan(ctx, team, goal)
	if err != nil {
		return err
	}
	if err := o.Repo.InsertTasks(ctx, tasks, deps); err != nil {
		return err
	}

	return o.Repo.AppendEvent(ctx, TeamEvent{
		TeamID:    team.ID,
		Type:      "team_started",
		Payload:   mustJSON(map[string]any{"goal": goal}),
		CreatedAt: o.Now(),
	})
}

func (o *Orchestrator) Run(ctx context.Context, teamID string) error {
	ticker := time.NewTicker(o.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := o.tick(ctx, teamID); err != nil {
				return err
			}
		}
	}
}

func (o *Orchestrator) tick(ctx context.Context, teamID string) error {
	if err := o.reclaimExpired(ctx, teamID); err != nil {
		return err
	}
	if err := o.unblockReady(ctx, teamID); err != nil {
		return err
	}
	if err := o.dispatch(ctx, teamID); err != nil {
		return err
	}
	if err := o.collectMailboxReports(ctx, teamID); err != nil {
		return err
	}
	if err := o.replanIfStuck(ctx, teamID); err != nil {
		return err
	}
	return o.finalizeIfDone(ctx, teamID)
}

func (o *Orchestrator) reclaimExpired(ctx context.Context, teamID string) error {
	requeued, err := o.Repo.RequeueExpiredTasks(ctx, teamID, o.Now())
	if err != nil {
		return err
	}
	for _, t := range requeued {
		o.stopLeaseKeepalive(t.ID)

		msg := MailMessage{
			ID:        newID(),
			TeamID:    teamID,
			FromAgent: OrchestratorAgentID,
			ToAgent:   "*",
			TaskID:    &t.ID,
			Kind:      "warning",
			Body:      "task lease expired and has been re-queued",
			Metadata: map[string]any{
				"task_id": t.ID,
			},
			CreatedAt: o.Now(),
		}
		if err := o.Repo.InsertMail(ctx, msg); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) unblockReady(ctx context.Context, teamID string) error {
	_, err := o.Repo.UnblockReadyTasks(ctx, teamID)
	return err
}

func (o *Orchestrator) dispatch(ctx context.Context, teamID string) error {
	team, err := o.Repo.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}

	mates, err := o.Repo.ListIdleTeammates(ctx, teamID)
	if err != nil {
		return err
	}
	if len(mates) == 0 {
		return nil
	}

	tasks, err := o.Repo.ListReadyTasks(ctx, teamID, 100)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}

	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Priority == tasks[j].Priority {
			return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
		}
		return tasks[i].Priority > tasks[j].Priority
	})

	running, err := o.Repo.ListRunningTasks(ctx, teamID)
	if err != nil {
		return err
	}
	activeWriters := countActiveWriters(running)

	for _, mate := range mates {
		idx := o.pickTaskForMate(tasks, mate, team.MaxWriters, activeWriters)
		if idx < 0 {
			continue
		}

		task := tasks[idx]
		if len(task.WritePaths) > 0 {
			activeWriters++
		}

		if err := o.dispatchOne(ctx, *team, mate, task); err != nil {
			return err
		}

		// 从候选里移除
		tasks = append(tasks[:idx], tasks[idx+1:]...)
		if len(tasks) == 0 {
			break
		}
	}
	return nil
}

func (o *Orchestrator) pickTaskForMate(tasks []Task, mate Teammate, maxWriters int, activeWriters int) int {
	bestIdx := -1
	bestScore := -1 << 30

	for i, t := range tasks {
		if len(t.WritePaths) > 0 && activeWriters >= maxWriters {
			continue
		}
		score := scoreTaskForMate(mate, t)
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	return bestIdx
}

func scoreTaskForMate(m Teammate, t Task) int {
	score := t.Priority * 100
	switch m.Profile {
	case "explore":
		if len(t.WritePaths) == 0 {
			score += 30
		} else {
			score -= 1000
		}
	case "planner":
		if len(t.WritePaths) == 0 {
			score += 20
		}
	case "executor":
		if len(t.WritePaths) > 0 {
			score += 40
		}
	}
	score -= t.RetryCount * 10
	return score
}

func (o *Orchestrator) dispatchOne(ctx context.Context, team Team, mate Teammate, task Task) error {
	leaseUntil := o.Now().Add(o.LeaseDuration)

	err := o.Repo.WithTx(ctx, func(tx TxRepo) error {
		ok, _, err := o.PathGuard.CanClaim(ctx, tx, team.ID, task.ReadPaths, task.WritePaths)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		claimed, err := tx.ClaimTask(ctx, team.ID, task.ID, mate.ID, task.Version, leaseUntil)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}

		return o.PathGuard.Acquire(ctx, tx, team.ID, task.ID, mate.ID, task.ReadPaths, task.WritePaths, leaseUntil)
	})
	if err != nil {
		return err
	}

	// 任务可能因为 claim 失败或路径冲突未真正进入 running
	fresh, err := o.Repo.GetTask(ctx, team.ID, task.ID)
	if err != nil {
		return err
	}
	if fresh.Status != TaskRunning {
		return nil
	}

	prompt := BuildTeammatePrompt(team, mate, *fresh)

	if err := o.Sessions.SubmitPrompt(ctx, mate.SessionID, prompt); err != nil {
		_ = o.Repo.FailTask(ctx, team.ID, task.ID, err.Error(), true)
		_ = o.Repo.ReleasePathClaimsByTask(ctx, team.ID, task.ID)
		return err
	}

	o.startLeaseKeepalive(team.ID, task.ID)

	return o.Repo.AppendEvent(ctx, TeamEvent{
		TeamID:    team.ID,
		Type:      "task_dispatched",
		Payload:   mustJSON(map[string]any{"task_id": task.ID, "teammate_id": mate.ID}),
		CreatedAt: o.Now(),
	})
}

func (o *Orchestrator) collectMailboxReports(ctx context.Context, teamID string) error {
	msgs, err := o.Repo.ListUnreadMail(ctx, teamID, OrchestratorAgentID, 100)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}

	acked := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Kind {
		case "done":
			if err := o.handleDoneMail(ctx, msg); err != nil {
				return err
			}
		case "blocked":
			if err := o.handleBlockedMail(ctx, msg); err != nil {
				return err
			}
		case "failed":
			if err := o.handleFailedMail(ctx, msg); err != nil {
				return err
			}
		case "info", "warning", "question", "handoff":
			// 第一版先只记录事件，不影响 task 状态
			if err := o.Repo.AppendEvent(ctx, TeamEvent{
				TeamID:    teamID,
				Type:      "mail_received",
				Payload:   mustJSON(msg),
				CreatedAt: o.Now(),
			}); err != nil {
				return err
			}
		}
		acked = append(acked, msg.ID)
	}

	return o.Repo.AckMail(ctx, teamID, OrchestratorAgentID, acked, o.Now())
}

func (o *Orchestrator) handleDoneMail(ctx context.Context, msg MailMessage) error {
	taskID, _ := msg.Metadata["task_id"].(string)
	if taskID == "" && msg.TaskID != nil {
		taskID = *msg.TaskID
	}
	summary, _ := msg.Metadata["summary"].(string)

	var resultRef *string
	if s, ok := msg.Metadata["result_ref"].(string); ok && s != "" {
		resultRef = &s
	}

	o.stopLeaseKeepalive(taskID)

	if err := o.Repo.CompleteTask(ctx, msg.TeamID, taskID, summary, resultRef); err != nil {
		return err
	}
	return o.Repo.AppendEvent(ctx, TeamEvent{
		TeamID:    msg.TeamID,
		Type:      "task_completed",
		Payload:   mustJSON(msg.Metadata),
		CreatedAt: o.Now(),
	})
}

func (o *Orchestrator) handleBlockedMail(ctx context.Context, msg MailMessage) error {
	taskID, _ := msg.Metadata["task_id"].(string)
	reason, _ := msg.Metadata["reason"].(string)
	if reason == "" {
		reason = msg.Body
	}

	o.stopLeaseKeepalive(taskID)

	if err := o.Repo.FailTask(ctx, msg.TeamID, taskID, reason, true); err != nil {
		return err
	}
	return o.Repo.AppendEvent(ctx, TeamEvent{
		TeamID:    msg.TeamID,
		Type:      "task_blocked",
		Payload:   mustJSON(msg.Metadata),
		CreatedAt: o.Now(),
	})
}

func (o *Orchestrator) handleFailedMail(ctx context.Context, msg MailMessage) error {
	taskID, _ := msg.Metadata["task_id"].(string)
	reason, _ := msg.Metadata["reason"].(string)
	retryable, _ := msg.Metadata["retryable"].(bool)

	o.stopLeaseKeepalive(taskID)

	if err := o.Repo.FailTask(ctx, msg.TeamID, taskID, reason, retryable); err != nil {
		return err
	}
	return o.Repo.AppendEvent(ctx, TeamEvent{
		TeamID:    msg.TeamID,
		Type:      "task_failed",
		Payload:   mustJSON(msg.Metadata),
		CreatedAt: o.Now(),
	})
}

func (o *Orchestrator) replanIfStuck(ctx context.Context, teamID string) error {
	tasks, err := o.Repo.ListTasks(ctx, teamID)
	if err != nil {
		return err
	}

	var ready, running, blocked, failed int
	for _, t := range tasks {
		switch t.Status {
		case TaskReady:
			ready++
		case TaskRunning:
			running++
		case TaskBlocked:
			blocked++
		case TaskFailed:
			failed++
		}
	}

	if ready > 0 || running > 0 {
		return nil
	}
	if blocked == 0 && failed == 0 {
		return nil
	}

	team, err := o.Repo.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}

	for _, t := range tasks {
		if t.Status == TaskFailed || t.Status == TaskBlocked {
			newTasks, deps, err := o.Planner.ReplanOnFailure(ctx, *team, t)
			if err != nil {
				return err
			}
			if len(newTasks) == 0 {
				continue
			}
			if err := o.Repo.InsertTasks(ctx, newTasks, deps); err != nil {
				return err
			}
			return o.Repo.AppendEvent(ctx, TeamEvent{
				TeamID:    teamID,
				Type:      "team_replanned",
				Payload:   mustJSON(map[string]any{"from_task_id": t.ID, "new_task_count": len(newTasks)}),
				CreatedAt: o.Now(),
			})
		}
	}
	return nil
}

func (o *Orchestrator) finalizeIfDone(ctx context.Context, teamID string) error {
	tasks, err := o.Repo.ListTasks(ctx, teamID)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}

	for _, t := range tasks {
		if t.Status != TaskDone && t.Status != TaskCancelled {
			return nil
		}
	}

	team, err := o.Repo.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	summary, err := o.Planner.FinalSummary(ctx, *team)
	if err != nil {
		return err
	}

	if err := o.Repo.UpdateTeamStatus(ctx, teamID, TeamDone); err != nil {
		return err
	}
	return o.Repo.AppendEvent(ctx, TeamEvent{
		TeamID:    teamID,
		Type:      "team_completed",
		Payload:   mustJSON(map[string]any{"summary": summary}),
		CreatedAt: o.Now(),
	})
}

func (o *Orchestrator) startLeaseKeepalive(teamID, taskID string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if cancel, ok := o.leaseCancels[taskID]; ok {
		cancel()
		delete(o.leaseCancels, taskID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	o.leaseCancels[taskID] = cancel

	go func() {
		ticker := time.NewTicker(o.LeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = o.Repo.RenewLease(context.Background(), teamID, taskID, o.Now().Add(o.LeaseDuration))
			}
		}
	}()
}

func (o *Orchestrator) stopLeaseKeepalive(taskID string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if cancel, ok := o.leaseCancels[taskID]; ok {
		cancel()
		delete(o.leaseCancels, taskID)
	}
}

func BuildTeammatePrompt(team Team, mate Teammate, task Task) string {
	return fmt.Sprintf(`
You are teammate %s in team %s.

Current task:
- Title: %s
- Goal: %s

Constraints:
- Read paths: %s
- Write paths: %s
- Deliverables: %s

Rules:
- Do not write outside allowed write paths.
- If blocked, send a team message to %s with kind=blocked.
- If failed, send a team message to %s with kind=failed.
- When done, send a team message to %s with kind=done and include:
  - task_id
  - summary
  - result_ref (optional)
`,
		mate.Name,
		team.ID,
		task.Title,
		task.Goal,
		mustJSON(task.ReadPaths),
		mustJSON(task.WritePaths),
		mustJSON(task.Deliverables),
		OrchestratorAgentID,
		OrchestratorAgentID,
		OrchestratorAgentID,
	)
}

func countActiveWriters(tasks []Task) int {
	n := 0
	for _, t := range tasks {
		if len(t.WritePaths) > 0 {
			n++
		}
	}
	return n
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newID() string {
	return fmt.Sprintf("id_%d", time.Now().UnixNano())
}
```

---

# 这版 orchestrator 的几个关键点

## 1. `ClaimTask + PathClaim` 必须在同一个事务里

这个我已经在 `dispatchOne()` 里用 `Repo.WithTx()` 收口了。
这是第一版 Teams 稳不稳的关键。

## 2. `done / blocked / failed` 用 mailbox 回报

这样你暂时不需要再造一套 teammate-report 表，也不需要让 session 直接改 task 状态。
**会话负责执行，orchestrator 负责落状态**，边界很清楚。

## 3. lease keepalive 不能省

如果你只 claim 不续租，稍微长一点的任务都会被误判超时。
我这里已经把 keepalive goroutine 放进 orchestrator 了。

---

# 7) 这 6 个文件接到你现有代码里时，具体怎么连

下面是最关键的“接线点”。

---

## A. Tool Broker 接 Checkpoint

你现有 mutating tools 在执行前后插这一层：

```go
handle, err := checkpointMgr.BeforeMutation(ctx, sessionID, turnID, paths, visibleSeq)
if err != nil { return err }

result, err := actualTool.Execute(...)
if err != nil { return err }

if err := checkpointMgr.AfterMutation(ctx, handle); err != nil {
    return err
}
```

### 哪些 tool 算 mutating

第一版建议先只接：

* `Write`
* `Edit`
* `MultiEdit`
* `Delete`
* `Move/Rename`

---

## B. Permission Engine 接 Actor

你原本 tool 执行前如果只做静态 allow/deny，现在改成：

```go
decision, err := permissionEngine.Evaluate(...)
```

返回：

* `allow`
* `deny`
* `ask`

然后把 `ask` 交给 actor 的 `pauseForApproval()`。

---

## C. `AskUserQuestion` 接 Actor

不要把 `AskUserQuestion` 做成外部工具执行器。
直接在 `actor.onToolCall()` 里 special-case 即可，这样改动最小。

---

## D. Teams 接 Session

`Orchestrator` 只需要一个很小的 facade：

```go
type SessionFacade interface {
    SubmitPrompt(ctx context.Context, sessionID string, text string) error
}
```

这意味着你现有 `runtime/chat` / `runtime/agent` 基本不用重写，只要能往 session actor 塞一个 `SubmitPrompt{Text: ...}` 就够。

---

# 8) 还差的一个桥接工具：`SendTeamMessage`

上面的 Teams 闭环还有最后一个桥：

**teammate 怎么把 done/blocked/failed 发给 orchestrator？**

最小实现方式是加一个 team tool：

```go
type SendTeamMessageArgs struct {
	TeamID    string         `json:"team_id"`
	ToAgent   string         `json:"to_agent"`
	Kind      string         `json:"kind"`
	TaskID    string         `json:"task_id,omitempty"`
	Body      string         `json:"body"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}
```

执行器里直接：

```go
repo.InsertMail(ctx, team.MailMessage{
    ID:        newID(),
    TeamID:    args.TeamID,
    FromAgent: currentAgentID,
    ToAgent:   args.ToAgent,
    TaskID:    optionalTaskID,
    Kind:      args.Kind,
    Body:      args.Body,
    Metadata:  args.Metadata,
    CreatedAt: time.Now(),
})
```

### 这样就闭环了

* teammate session 执行任务
* 完成后调用 `SendTeamMessage(kind=done)`
* orchestrator 的 `collectMailboxReports()` 把它消费掉
* task 状态更新为 done

---

# 9) 第一轮落地顺序

这次我建议你按下面顺序真写：

### 第一步

`runtime_state_store.go` + `actor.go`

先把暂停恢复打通。

### 第二步

`checkpoint/manager.go`

先把文件编辑工具纳入 checkpoint。

### 第三步

`team/repo.go` + migration SQL

把 team/task/mailbox/path_claim/lease 表先定下来。

### 第四步

`path_guard.go` + `orchestrator.go`

先让 team 最小闭环跑起来。

### 第五步

`SendTeamMessage` tool

把 teammate -> orchestrator 的闭环补完。

---

# 10) 第一轮验收标准

如果你把上面这些文件接好了，至少应该能通过这几个场景：

## 场景 1：审批暂停恢复

* agent 发起高风险 tool
* session 进入 `waiting_approval`
* 用户批准后继续完成 turn

## 场景 2：提问暂停恢复

* agent 调 `AskUserQuestion`
* session 进入 `waiting_input`
* 用户回答后继续

## 场景 3：checkpoint 回退

* 连续两次 `Edit`
* 能 preview 第二次之前的状态
* 能 rewind 回第一次编辑后

## 场景 4：最小 Team

* lead 拆 2 个 ready tasks
* 2 个 teammate 并行执行
* 一个发 `done`
* orchestrator 标记完成
* 所有 tasks done 后 team 结束

## 场景 5：路径冲突阻止

* 两个 task 都写 `pkg/auth`
* 只能有一个先跑，另一个必须等待

---

如果你继续，我下一条直接给你：

**`sqlite_repo.go` 的关键事务实现 + migration SQL 草案 + `SendTeamMessage` tool 骨架**。
