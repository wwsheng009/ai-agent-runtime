// Package cacheanalytics 提供 LLM 请求缓存信息的统一采集、聚合与查询能力。
//
// 设计文档：docs/plan/llm-cache-analytics-unified-plan.md（cache.analytics.v1）。
// 核心原则：采集与聚合只做一次（Collector/Projector 挂在 runtime EventBus 上），
// HTTP 契约只定义一次（httpapi.Mount），前端（micro web client / runtime server
// frontend / TUI /usage 命令）各自消费同一 Source 接口。
//
// 本包不 import cmd 层与 internal/api 层；依赖方向单向（cmd → cacheanalytics）。
package cacheanalytics

import (
	"encoding/json"
	"errors"
	"time"
)

// SchemaVersion 是缓存分析契约的版本号。新增字段向后兼容；破坏性变更升 v2。
const SchemaVersion = "cache.analytics.v1"

// RecordPayload 把 CacheRequestRecord 投影为事件载荷 map（§6.3 SSE 增量）。
// 经 JSON 编解码保证字段名与 HTTP 契约完全一致；SSE 单帧 < 2KB（§10）。
func RecordPayload(record CacheRequestRecord) map[string]interface{} {
	raw, err := json.Marshal(record)
	if err != nil {
		return map[string]interface{}{"llm_request_id": record.LLMRequestID}
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string]interface{}{"llm_request_id": record.LLMRequestID}
	}
	return payload
}

// 请求终态与缓存状态枚举值。
const (
	// RequestStatusSuccess 请求成功（含 provider 返回业务错误但拿到 usage 的情况由调用方归类）。
	RequestStatusSuccess = "success"
	// RequestStatusError 请求失败（provider 调用返回错误）。
	RequestStatusError = "error"
	// RequestStatusInterrupted 进程/会话结束时仍未终态的 in-flight 请求（§16.1 边界 3）。
	RequestStatusInterrupted = "interrupted"

	// CacheStatusHit 命中缓存（cache_read > 0）。
	CacheStatusHit = "hit"
	// CacheStatusWrite 写入缓存（creation > 0 且 read == 0）。
	CacheStatusWrite = "write"
	// CacheStatusReportedZero provider 显式上报但命中为 0。
	CacheStatusReportedZero = "reported_zero"
	// CacheStatusNotReported provider 未上报缓存字段（不污染命中率）。
	CacheStatusNotReported = "not_reported"
	// CacheStatusError 请求失败，无缓存语义。
	CacheStatusError = "error"

	// CorrelationSourceHistory 标记 message_id 经会话历史推断回填（UI 显示"推断"徽标）。
	CorrelationSourceHistory = "history_inferred"

	// DataSourceLive 在线投影（内存 Projector）。
	DataSourceLive = "live"
	// DataSourceHistory 历史会话兜底推断。
	DataSourceHistory = "history"
	// DataSourceOffline 离线日志投影（Phase 3）。
	DataSourceOffline = "offline"
)

// 稳定错误码（HTTP envelope 使用，不回传原始 Go error）。
var (
	// ErrDisabled 缓存分析能力未启用（旧后端/未挂载）。
	ErrDisabled = errors.New("cache_analytics_disabled")
	// ErrInvalidRequest 请求参数非法。
	ErrInvalidRequest = errors.New("cache_invalid_request")
	// ErrSessionNotFound 会话不存在。
	ErrSessionNotFound = errors.New("cache_session_not_found")
	// ErrNotFound 请求或消息不存在。
	ErrNotFound = errors.New("cache_not_found")
	// ErrInternal 内部错误。
	ErrInternal = errors.New("cache_internal")
)

// CacheUsage 归一化后的 token 用量（字段与 internal/types/token.go 对齐）。
type CacheUsage struct {
	UsageSource           string `json:"usage_source,omitempty"`
	PromptTokens          int64  `json:"prompt_tokens"`
	CompletionTokens      int64  `json:"completion_tokens"`
	TotalTokens           int64  `json:"total_tokens"`
	CachedTokens          int64  `json:"cached_tokens"`
	CacheReadTokens       int64  `json:"cache_read_tokens"`
	CacheCreationTokens   int64  `json:"cache_creation_tokens"`
	CacheReadReported     bool   `json:"cache_read_reported"`
	CacheCreationReported bool   `json:"cache_creation_reported"`
	ReasoningTokens       int64  `json:"reasoning_tokens"`
}

// CacheRequestRecord 每条 LLM 请求一行（§4.1）。
// 记录终态后不可变（assistant/user message id 回填除外，且回填不改 usage/聚合）。
type CacheRequestRecord struct {
	SchemaVersion      string      `json:"schema_version"`
	LLMRequestID       string      `json:"llm_request_id"`
	SessionID          string      `json:"session_id"`
	TraceID            string      `json:"trace_id,omitempty"`
	TurnID             string      `json:"turn_id,omitempty"`
	Step               int         `json:"step,omitempty"`
	Provider           string      `json:"provider,omitempty"`
	Model              string      `json:"model,omitempty"`
	Stream             bool        `json:"stream,omitempty"`
	Status             string      `json:"status"`
	Attempt            int         `json:"attempt,omitempty"`
	StartedAt          time.Time   `json:"started_at"`
	FinishedAt         *time.Time  `json:"finished_at,omitempty"`
	DurationMS         int64       `json:"duration_ms,omitempty"`
	Usage              *CacheUsage `json:"usage,omitempty"`
	CacheHitRatio      *float64    `json:"cache_hit_ratio,omitempty"`
	CacheWriteRatio    *float64    `json:"cache_write_ratio,omitempty"`
	CacheStatus        string      `json:"cache_status"`
	CacheEpoch         int         `json:"cache_epoch,omitempty"`
	PromptCacheKey     string      `json:"prompt_cache_key,omitempty"`
	PromptFingerprint  string      `json:"prompt_fingerprint,omitempty"`
	UserMessageID      string      `json:"user_message_id,omitempty"`
	AssistantMessageID string      `json:"assistant_message_id,omitempty"`
	ProviderRequestID  string      `json:"provider_request_id,omitempty"`
	ErrorCategory      string      `json:"error_category,omitempty"`
	CorrelationSource  string      `json:"correlation_source,omitempty"`
}

// CacheStatusDistribution 缓存状态分布（overview 直接画饼）。
type CacheStatusDistribution struct {
	Hit          int `json:"hit"`
	Write        int `json:"write"`
	ReportedZero int `json:"reported_zero"`
	NotReported  int `json:"not_reported"`
	Error        int `json:"error"`
}

// CoverageInfo 数据覆盖情况（自报观测洞，UI 必须可见）。
type CoverageInfo struct {
	UsageRequestRate *float64 `json:"usage_request_rate,omitempty"`
	CacheReportRate  *float64 `json:"cache_report_rate,omitempty"`
	Partial          bool     `json:"partial"`
	PartialReasons   []string `json:"partial_reasons,omitempty"`
}

// CacheOverview 会话级总览（§4.2）。
type CacheOverview struct {
	SchemaVersion           string                  `json:"schema_version"`
	SessionID               string                  `json:"session_id"`
	GeneratedAt             time.Time               `json:"generated_at"`
	WindowFrom              *time.Time              `json:"window_from,omitempty"`
	WindowTo                *time.Time              `json:"window_to,omitempty"`
	RequestsTotal           int                     `json:"requests_total"`
	RequestsWithUsage       int                     `json:"requests_with_usage"`
	RequestsCacheReported   int                     `json:"requests_cache_reported"`
	Tokens                  CacheOverviewTokens     `json:"tokens"`
	CacheHitRatio           *float64                `json:"cache_hit_ratio,omitempty"`
	CacheWriteRatio         *float64                `json:"cache_write_ratio,omitempty"`
	CacheStatusDistribution CacheStatusDistribution `json:"cache_status_distribution"`
	Coverage                CoverageInfo            `json:"coverage"`
}

// CacheOverviewTokens 总览 token 聚合。
type CacheOverviewTokens struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
}

// ProducedBy 消息由哪个请求产出（§4.3）。
type ProducedBy struct {
	LLMRequestID  string      `json:"llm_request_id"`
	Usage         *CacheUsage `json:"usage,omitempty"`
	CacheHitRatio *float64    `json:"cache_hit_ratio,omitempty"`
	CacheStatus   string      `json:"cache_status,omitempty"`
	TraceID       string      `json:"trace_id,omitempty"`
	TurnID        string      `json:"turn_id,omitempty"`
	StartedAt     *time.Time  `json:"started_at,omitempty"`
}

// ConsumedBy 消息被哪些后续请求消费（§4.3）。
type ConsumedBy struct {
	LLMRequestID  string     `json:"llm_request_id"`
	Step          int        `json:"step,omitempty"`
	TurnID        string     `json:"turn_id,omitempty"`
	CacheHitRatio *float64   `json:"cache_hit_ratio,omitempty"`
	CacheStatus   string     `json:"cache_status,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
}

// MessageNeighbors 相邻消息浅引用（供 UI 定位上下文）。
type MessageNeighbors struct {
	PrevMessageID string `json:"prev_message_id,omitempty"`
	NextMessageID string `json:"next_message_id,omitempty"`
}

// MessageTrace 按消息 id 追溯（§4.3）。
type MessageTrace struct {
	SchemaVersion     string           `json:"schema_version"`
	SessionID         string           `json:"session_id"`
	MessageID         string           `json:"message_id"`
	MessageRole       string           `json:"message_role,omitempty"`
	TurnID            string           `json:"turn_id,omitempty"`
	ProducedBy        *ProducedBy      `json:"produced_by,omitempty"`
	ConsumedBy        []ConsumedBy     `json:"consumed_by,omitempty"`
	Neighbors         MessageNeighbors `json:"neighbors"`
	HistoryAvailable  bool             `json:"history_available"`
	CorrelationSource string           `json:"correlation_source,omitempty"`
}

// RequestQuery 明细列表查询/过滤条件（§4.4）。
type RequestQuery struct {
	TraceID     string
	TurnID      string
	MessageID   string
	Status      string
	CacheStatus string
	From        *time.Time
	To          *time.Time
	Limit       int
	Offset      int
}

// RequestListResponse 明细分页响应（按 started_at 倒序）。
type RequestListResponse struct {
	SchemaVersion string               `json:"schema_version"`
	SessionID     string               `json:"session_id"`
	Total         int                  `json:"total"`
	Limit         int                  `json:"limit"`
	Offset        int                  `json:"offset"`
	Requests      []CacheRequestRecord `json:"requests"`
}

// Capabilities 能力发现（§4.4 / §7.1：前端启动探测，不支持时优雅降级）。
type Capabilities struct {
	SchemaVersion         string   `json:"schema_version"`
	DataSource            string   `json:"data_source"`
	MaxRequestsPerSession int      `json:"max_requests_per_session"`
	SupportsSSE           bool     `json:"supports_sse"`
	SupportedEvents       []string `json:"supported_events,omitempty"`
}

// MessageContext 会话历史中消息的定位信息（HistoryLookup 返回）。
type MessageContext struct {
	MessageID     string
	Role          string
	TurnID        string
	PrevMessageID string
	NextMessageID string
}

// HistoryLookup 会话历史兜底查询接口（§5.2 兜底路径）。
// aicli/runtime server 挂载时用 SessionStorage 实现注入；为 nil 时
// message id 关联仅依赖事件流（assistant_message 载荷），trace 仍可用。
type HistoryLookup interface {
	// MessageContext 返回消息在历史中的角色/turn/相邻消息。
	MessageContext(sessionID, messageID string) (MessageContext, bool)
	// AssistantMessageIDByTurn 返回该 turn 最后一条 assistant 消息的 message_id。
	AssistantMessageIDByTurn(sessionID, turnID string) (string, bool)
	// UserMessageIDByTurn 返回触发该 turn 的 user 消息 message_id。
	UserMessageIDByTurn(sessionID, turnID string) (string, bool)
	// SessionExists 会话是否存在于历史存储。
	SessionExists(sessionID string) bool
}

// Source 缓存分析统一查询接口——"同一后端，不同前端"的落点。
// HTTP 层（httpapi.Mount）与 TUI（/usage 命令）都只依赖此接口。
type Source interface {
	// Capabilities 报告数据源能力。
	Capabilities() Capabilities
	// Overview 返回指定会话的总览。
	Overview(sessionID string) (CacheOverview, error)
	// Requests 返回明细分页（含过滤，按 started_at 倒序）。
	Requests(sessionID string, q RequestQuery) (RequestListResponse, error)
	// Request 返回单请求详情。
	Request(sessionID, llmRequestID string) (CacheRequestRecord, error)
	// MessageTrace 返回消息追溯。
	MessageTrace(sessionID, messageID string) (MessageTrace, error)
}
