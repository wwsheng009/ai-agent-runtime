package runtimeobserve

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 版本化 schema 常量（Phase 0 契约）。
const (
	SchemaVersionResponse  = "runtime.observe.v1"
	SchemaVersionEvent     = "runtime.observe.event.v1"
	SchemaVersionSSE       = "runtime.observe.sse.v1"
	SchemaVersionCursor    = "runtime.observe.cursor.v1"
	RedactionProfileSafeDefault = "safe_default"

	// 错误码（错误 envelope 只使用稳定错误码，不回传原始 Go error）。
	ErrCodeDisabled         = "observe_disabled"
	ErrCodeUnauthorized     = "observe_unauthorized"
	ErrCodeInvalidRequest   = "observe_invalid_request"
	ErrCodeSessionNotFound  = "observe_session_not_found"
	ErrCodeCursorExpired    = "observe_cursor_expired"   // 对应 HTTP 410 / resync_required
	ErrCodeCursorInvalid    = "observe_cursor_invalid"
	ErrCodeTooManyClients   = "observe_too_many_clients"
	ErrCodeInternal         = "observe_internal"
	ErrCodeResourceExceeded = "observe_resource_exceeded"

	// 事件白名单事件类型（方案 §6.3；v1 仅允许这些类型，其余丢弃并按
	// unknown_event_dropped 计数，绝不透传原始 Payload）。
	EventRuntimeStarted   = "runtime.started"
	EventRuntimeReady     = "runtime.ready"
	EventRuntimeShutdown  = "runtime.shutdown"
	EventSessionStarted   = "session.started"
	EventSessionState     = "session.state_changed"
	EventSessionFinished  = "session.finished"
	EventAgentTurnStarted = "agent.turn.started"
	EventAgentTurnDone    = "agent.turn.finished"
	EventLLMRequestStart  = "llm.request.started"
	EventLLMRequestDone   = "llm.request.finished"
	EventLLMAttemptStart  = "llm.attempt.started"
	EventLLMAttemptDone   = "llm.attempt.finished"
	EventLLMRetry         = "llm.retry"
	EventLLMStreamSummary = "llm.stream.summary"
	EventUsageUpdated     = "usage.updated"
	EventToolStarted      = "tool.started"
	EventToolFinished     = "tool.finished"
	EventToolFailed       = "tool.failed"
	EventToolProgress     = "tool.progress.summary"
	EventRendererChanged  = "renderer.snapshot.changed"
	EventObservationGap   = "observation.gap"
	EventResyncRequired   = "observation.resync_required"
)

// Correlation 是所有观测事件的关联上下文（方案 §4.3/§6.1）。
type Correlation struct {
	SessionID      string `json:"session_id,omitempty"`
	TraceID        string `json:"trace_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	ToolCallID     string `json:"tool_call_id,omitempty"`
	LLMRequestID   string `json:"llm_request_id,omitempty"`
	RetryAttemptID string `json:"retry_attempt_id,omitempty"`
	StreamID       string `json:"stream_id,omitempty"`
	ProviderRequestID string `json:"provider_request_id,omitempty"`
}

// Event 是观测平面上一条已投影、已脱敏的事件记录。
type Event struct {
	ObservationSeq int64        `json:"observation_seq"`
	Timestamp      time.Time    `json:"timestamp"`
	Type           string       `json:"type"`
	Source         string       `json:"source,omitempty"`
	SchemaVersion  string       `json:"schema_version"`
	Correlation    Correlation `json:"correlation,omitempty"`
	Payload        map[string]interface{} `json:"payload,omitempty"`
}

// CursorInfo 携带事件序列边界与实例 epoch。
type CursorInfo struct {
	ObservationSeq    int64  `json:"observation_seq"`
	OldestAvailableSeq int64 `json:"oldest_available_seq,omitempty"`
	InstanceEpoch     string `json:"instance_epoch,omitempty"`
}

// ComponentMeta 描述快照中单个数据域的 revision 与采集时间。
type ComponentMeta struct {
	Revision   int64     `json:"revision,omitempty"`
	CapturedAt time.Time `json:"captured_at,omitempty"`
}

// Consistency 描述快照跨组件一致性情况。
type Consistency struct {
	Kind            string   `json:"kind"`
	Partial         bool     `json:"partial"`
	StaleComponents []string `json:"stale_components,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

// ProcessSummary 是低成本的进程摘要，不能替代 pprof。
type ProcessSummary struct {
	InstanceID          string `json:"instance_id"`
	PID                 int    `json:"pid"`
	UptimeMS            int64  `json:"uptime_ms"`
	Goroutines          int    `json:"goroutines"`
	HeapBytes           uint64 `json:"heap_bytes"`
	ObservationEnabled  bool   `json:"observation_enabled"`
}

// RuntimeSummary 是 runtime 维度的聚合计数。
type RuntimeSummary struct {
	ActiveSessions    int       `json:"active_sessions,omitempty"`
	RunningTurns      int       `json:"running_turns,omitempty"`
	ActiveLLMRequests int       `json:"active_llm_requests,omitempty"`
	ActiveTools       int       `json:"active_tools,omitempty"`
	PendingApprovals  int       `json:"pending_approvals,omitempty"`
	EventIngressDropped uint64  `json:"event_ingress_dropped"`
	UnknownEventsDropped uint64 `json:"unknown_events_dropped"`
	ProjectionErrors  uint64    `json:"projection_errors"`
	GapCount          uint64    `json:"gap_count"`
	LastGapAt         *time.Time `json:"last_gap_at,omitempty"`
	LastEventAt       *time.Time `json:"last_event_at,omitempty"`
	RingCurrentBytes  int64     `json:"ring_current_bytes"`
	RingOldestSeq     int64     `json:"ring_oldest_seq"`
	RingLatestSeq     int64     `json:"ring_latest_seq"`
}

// UsageSummary 汇总 token usage（仅计数，不重复求和）。
type UsageSummary struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`
}

// ProviderSummary 按 provider 聚合要求/错误数。
type ProviderSummary struct {
	Requests  int64 `json:"requests"`
	Errors    int64 `json:"errors"`
	Retries   int64 `json:"retries"`
}

// LLMSummary 是 LLM 维度的聚合摘要。
type LLMSummary struct {
	RequestsTotal    int64                      `json:"requests_total"`
	RequestsInFlight int64                      `json:"requests_in_flight"`
	RetriesTotal     int64                      `json:"retries_total"`
	StreamCount      int64                      `json:"stream_count"`
	Usage            UsageSummary               `json:"usage"`
	ByProvider       map[string]ProviderSummary `json:"by_provider,omitempty"`
	ByModel          map[string]int64            `json:"by_model,omitempty"`
}

// RendererLink 是 renderer 语义快照的低敏 link（Phase 4 预留）。
type RendererLink struct {
	RendererID    string `json:"renderer_id,omitempty"`
	SceneRevision int64  `json:"scene_revision,omitempty"`
	Fresh         bool   `json:"fresh,omitempty"`
	Unavailable   bool   `json:"unavailable,omitempty"`
}

// SessionSummary 是单 session 状态的低敏摘要。
type SessionSummary struct {
	SessionID            string        `json:"session_id"`
	State                string        `json:"state,omitempty"`
	TurnID               string        `json:"turn_id,omitempty"`
	TraceID              string        `json:"trace_id,omitempty"`
	RuntimeStateRevision int64         `json:"runtime_state_revision,omitempty"`
	LastEventSeq         int64         `json:"last_event_seq,omitempty"`
	ActiveRequestID      string        `json:"active_request_id,omitempty"`
	Renderer             *RendererLink `json:"renderer,omitempty"`
	UpdatedAt            *time.Time    `json:"updated_at,omitempty"`
}

// Snapshot 是观测平面的复合只读快照。
type Snapshot struct {
	SchemaVersion   string            `json:"schema_version"`
	SnapshotRevision int64            `json:"snapshot_revision"`
	CapturedAt      time.Time         `json:"captured_at"`
	FreshnessMS     int64             `json:"freshness_ms"`
	Consistency     Consistency       `json:"consistency"`
	Process         ProcessSummary    `json:"process"`
	Runtime         RuntimeSummary    `json:"runtime"`
	LLM             LLMSummary        `json:"llm"`
	Sessions        SessionCollection `json:"sessions"`
	Cursor          CursorInfo        `json:"cursor"`
	Components      map[string]ComponentMeta `json:"components,omitempty"`
}

// SessionCollection 是快照内的 session 摘要集合。
type SessionCollection struct {
	Items []SessionSummary `json:"items,omitempty"`
	Count int              `json:"count"`
	Partial bool           `json:"partial,omitempty"`
}

// Capabilities 描述观测平面的能力与生效限额。
type Capabilities struct {
	SchemaVersion  string            `json:"schema_version"`
	Enabled        bool              `json:"enabled"`
	InstanceEpoch  string            `json:"instance_epoch"`
	Retention      map[string]int64  `json:"retention"`
	Limits         map[string]int64  `json:"limits"`
	Redaction      RedactionCapability `json:"redaction"`
	Query          QueryCapability   `json:"query"`
	Stream         StreamCapability  `json:"stream"`
	Renderer       RendererCapability `json:"renderer"`
	EventAllowlist []string          `json:"event_allowlist"`
}

// RedactionCapability 披露脱敏契约摘要。
type RedactionCapability struct {
	Profile       string   `json:"profile"`
	OmittedFields []string `json:"omitted_fields"`
	HMACScheme    string   `json:"hmac_scheme"`
	HMACKeySet    bool     `json:"hmac_key_set"`
	KeyVersion    string   `json:"key_version,omitempty"`
}

// QueryCapability 披露事件查询能力。
type QueryCapability struct {
	DefaultLimit       int   `json:"default_limit"`
	MaxLimit           int   `json:"max_limit"`
	RetentionEvents    int   `json:"retention_events"`
	OldestAvailableSeq int64 `json:"oldest_available_seq"`
	LatestSeq          int64 `json:"latest_seq"`
}

// StreamCapability 披露 SSE 能力（Phase 3 前为不可用）。
type StreamCapability struct {
	Enabled   bool `json:"enabled"`
	HeartbeatMS int `json:"heartbeat_ms"`
	MaxClients  int  `json:"max_clients"`
	Protocol    string `json:"protocol,omitempty"`
}

// RendererCapability 披露 renderer 能力（Phase 4 前为不可用）。
type RendererCapability struct {
	Available       bool   `json:"available"`
	PublisherLinked bool   `json:"publisher_linked"`
	Reason          string `json:"reason,omitempty"`
}

// Envelope 是普通 JSON 响应的统一 envelope。
type Envelope struct {
	OK            bool          `json:"ok"`
	SchemaVersion string        `json:"schema_version"`
	RequestID     string        `json:"request_id"`
	Data          interface{}   `json:"data,omitempty"`
	Warnings      []string      `json:"warnings,omitempty"`
	Redaction     *RedactionCapability `json:"redaction,omitempty"`
	Error         *ErrorBody    `json:"error,omitempty"`
}

// ErrorBody 是错误 envelope 的最小字段（不带原始 provider body / Go error）。
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// EventQuery 是 /events 查询条件。
type EventQuery struct {
	SessionID string
	TraceID   string
	AgentID   string
	TurnID    string
	Provider  string
	Model     string
	EventType string
	Source    string
	AfterSeq  int64 // inclusive lower bound（0 表示从 oldest 开始）
	BeforeSeq int64 // exclusive upper bound（0 表示无上限）
	Since     *time.Time
	Until     *time.Time
	Limit     int
}

// QueryResult 是 /events 的响应数据域。
type QueryResult struct {
	Events             []Event `json:"events"`
	AfterSeq           int64   `json:"after_seq"`
	LatestSeq          int64   `json:"latest_seq"`
	OldestAvailableSeq int64   `json:"oldest_available_seq"`
	NextCursor         *string `json:"next_cursor"`
	Partial            bool    `json:"partial"`
	Count               int     `json:"count"`
}

// Cursor 是客户端可恢复的观测位置（opaque，绑定 instance epoch/schema）。
// 编码：base64url( schema_version|epoch|seq )
type Cursor struct {
	SchemaVersion string
	InstanceEpoch string
	Seq           int64
}

// Encode 把 cursor 编码为 opaque 字符串。
func (c Cursor) Encode() string {
	if c.Seq <= 0 {
		return ""
	}
	raw := strings.Join([]string{c.SchemaVersion, c.InstanceEpoch, strconv.FormatInt(c.Seq, 10)}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor 解析并校验 opaque cursor；校验失败的返回 ErrCodeCursorInvalid。
func DecodeCursor(value, expectedSchema, expectedEpoch string) (Cursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Cursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, fmt.Errorf("%s: malformed cursor encoding", ErrCodeCursorInvalid)
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 3 {
		return Cursor{}, fmt.Errorf("%s: cursor arity mismatch", ErrCodeCursorInvalid)
	}
	schema, epoch, seqRaw := parts[0], parts[1], parts[2]
	if schema != expectedSchema {
		return Cursor{}, fmt.Errorf("%s: cursor schema mismatch", ErrCodeCursorInvalid)
	}
	if epoch != "" && expectedEpoch != "" && epoch != expectedEpoch {
		return Cursor{}, fmt.Errorf("%s: cursor belongs to a different instance epoch", ErrCodeCursorInvalid)
	}
	seq, err := strconv.ParseInt(seqRaw, 10, 64)
	if err != nil || seq <= 0 {
		return Cursor{}, fmt.Errorf("%s: cursor seq invalid", ErrCodeCursorInvalid)
	}
	return Cursor{SchemaVersion: schema, InstanceEpoch: epoch, Seq: seq}, nil
}

// JSONSize 返回 value 的 JSON 编码字节数（用于 projection 后检查）。
func JSONSize(value interface{}) (int, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}
