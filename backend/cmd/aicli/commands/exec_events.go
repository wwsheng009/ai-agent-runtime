package commands

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	EventTypeThreadStarted = "thread.started"
	EventTypeTurnStarted   = "turn.started"
	EventTypeTurnCompleted = "turn.completed"
	EventTypeTurnFailed    = "turn.failed"
	EventTypeItemStarted   = "item.started"
	EventTypeItemUpdated   = "item.updated"
	EventTypeItemCompleted = "item.completed"
	EventTypeWarning       = "warning"
	EventTypeError         = "error"
)

type ThreadEvent struct {
	Version   int             `json:"version"`
	Sequence  int64           `json:"sequence"`
	Timestamp string          `json:"timestamp"`
	ThreadID  string          `json:"thread_id,omitempty"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type ThreadStartedEvent struct {
	ThreadID  string `json:"thread_id"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Ephemeral bool   `json:"ephemeral,omitempty"`
	// Profile 是本次运行生效的 profile 元数据（D9/FR-10）。未启用 profile 时为
	// nil，字段整体缺席——保证无 profile 的 JSONL 输出形状逐字节不变。
	Profile *ExecProfileMetadata `json:"profile,omitempty"`
}

type TurnStartedEvent struct {
	TurnID string `json:"turn_id"`
	Prompt string `json:"prompt,omitempty"`
}

type TurnCompletedEvent struct {
	TurnID     string     `json:"turn_id"`
	Status     string     `json:"status"`
	Usage      TokenUsage `json:"usage"`
	DurationMs int64      `json:"duration_ms,omitempty"`
}

type TurnFailedEvent struct {
	TurnID string `json:"turn_id"`
	Error  string `json:"error"`
	Code   string `json:"code,omitempty"`
}

type TokenUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens,omitempty"`
	OutputTokens      int `json:"output_tokens"`
	ReasoningTokens   int `json:"reasoning_tokens,omitempty"`
	TotalTokens       int `json:"total_tokens"`
}

type ItemStartedEvent struct {
	ItemID   string      `json:"item_id"`
	ItemType string      `json:"item_type"`
	Details  interface{} `json:"details,omitempty"`
}

type ItemUpdatedEvent struct {
	ItemID   string      `json:"item_id"`
	ItemType string      `json:"item_type"`
	Details  interface{} `json:"details,omitempty"`
}

type ItemCompletedEvent struct {
	ItemID   string      `json:"item_id"`
	ItemType string      `json:"item_type"`
	Status   string      `json:"status"`
	Details  interface{} `json:"details,omitempty"`
}

type ErrorEvent struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type ToolCallDetails struct {
	ToolName   string      `json:"tool_name"`
	Args       interface{} `json:"args,omitempty"`
	Result     interface{} `json:"result,omitempty"`
	DurationMs int64       `json:"duration_ms,omitempty"`
}

type MessageDetails struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type FileChangeDetails struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Diff   string `json:"diff,omitempty"`
}

type ExecFinalResult struct {
	Status     string     `json:"status"`
	Message    string     `json:"message"`
	SessionID  string     `json:"session_id,omitempty"`
	Model      string     `json:"model,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	Usage      TokenUsage `json:"usage,omitempty"`
	DurationMs int64      `json:"duration_ms,omitempty"`
	// Profile 供 CI 断言实际生效的裁剪（D9/FR-10）；无 profile 时省略。
	Profile *ExecProfileMetadata `json:"profile,omitempty"`
}

// ExecProfileMetadata 描述一次 exec 运行实际生效的 profile 面（D9/FR-10）。
// 字段是稳定契约：ref/name/agent + 裁剪计数，供 CI 断言"工具面确实被收窄"。
type ExecProfileMetadata struct {
	Reference string `json:"ref"`
	Name      string `json:"name,omitempty"`
	Agent     string `json:"agent,omitempty"`
	// ToolCount 是 profile allowlist 生效后的工具条目数；0 表示未声明
	// allowlist（不限制工具面，即全量）。
	ToolCount int `json:"tool_count"`
	// SkillCount 是会话实际可见的 skill 数（exposure 绑定计数）。
	SkillCount int `json:"skill_count"`
}

func generateThreadID() string {
	return "thread_" + uuid.New().String()[:8]
}

func generateTurnID() string {
	return "turn_" + uuid.New().String()[:8]
}

func generateItemID() string {
	return "item_" + uuid.New().String()[:8]
}

func newThreadEvent(sequence int64, threadID, eventType string, data interface{}) ThreadEvent {
	var raw json.RawMessage
	if data != nil {
		if encoded, err := json.Marshal(data); err == nil {
			raw = encoded
		}
	}
	return ThreadEvent{
		Version:   1,
		Sequence:  sequence,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		ThreadID:  threadID,
		Type:      eventType,
		Data:      raw,
	}
}
