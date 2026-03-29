// Package codex 提供对 OpenAI Codex/Responses API 的支持
//
// Codex API（也称 Responses API）是 OpenAI 的新一代 API 格式，与 Chat Completions API 有以下主要差异：
//   - 使用 input/output 替代 messages
//   - 工具调用使用独立的 function_call/function_call_output 对象
//   - 支持 reasoning（推理）和 thinking（思考）
//   - 工具名称长度限制为 64 字符
//
// 本包提供 Codex API 的请求和响应数据结构定义。
// 参考: https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/sse/responses.rs
package codex

import "encoding/json"

const (
	Name    = "codex"
	Version = "1.1.0"
)

// ResponsesRequest Codex/Responses API 请求
// 参考: https://platform.openai.com/docs/api-reference/responses
type ResponsesRequest struct {
	Model                string          `json:"model"`
	Stream               bool            `json:"stream,omitempty"`
	Input                []InputItem     `json:"input,omitempty"`
	Tools                []Tool          `json:"tools,omitempty"`
	ToolChoice           interface{}     `json:"tool_choice,omitempty"` // string or ToolChoiceObject
	ParallelToolCalls    *bool           `json:"parallel_tool_calls,omitempty"`
	PromptCacheKey       string          `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention json.RawMessage `json:"prompt_cache_retention,omitempty"`

	// 推理配置
	Reasoning *Reasoning `json:"reasoning,omitempty"`

	// 文本输出配置
	Text *TextConfig `json:"text,omitempty"`

	// 输出 token 限制
	MaxOutputTokens *int `json:"max_output_tokens,omitempty"`

	// 其他配置
	Instructions string   `json:"instructions,omitempty"`
	Include      []string `json:"include,omitempty"`
	Store        *bool    `json:"store,omitempty"`

	// 元数据
	// 仅允许作为网关内部中间态存在，用于 prompt cache 等派生逻辑。
	// Codex 上游协议不接受该字段，发往上游前必须由 pipeline 剥离。
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// InputItem 输入项（可以是消息或工具调用）
type InputItem struct {
	// 通用字段
	Type string `json:"type"` // message, function_call, function_call_output

	// message 类型字段
	Role    string        `json:"role,omitempty"` // user, assistant, developer
	Content []ContentPart `json:"content,omitempty"`

	// function_call 类型字段
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// function_call_output 类型字段
	Output string `json:"output,omitempty"`
}

// ContentPart 内容部分
type ContentPart struct {
	Type string `json:"type"` // input_text, output_text, input_image

	// 文本内容
	Text string `json:"text,omitempty"`

	// 图片内容
	ImageUrl string `json:"image_url,omitempty"`
}

// Tool 工具定义
type Tool struct {
	Type string `json:"type"` // function, web_search, etc.

	// function 类型字段
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Strict      *bool                  `json:"strict,omitempty"`
}

// ToolChoiceObject 工具选择对象
type ToolChoiceObject struct {
	Type string `json:"type"` // function
	Name string `json:"name,omitempty"`
}

// Reasoning 推理配置
type Reasoning struct {
	Effort  string `json:"effort,omitempty"`  // low, medium, high
	Summary string `json:"summary,omitempty"` // auto, concise, detailed
}

// TextConfig 文本输出配置
type TextConfig struct {
	Format    *TextFormat `json:"format,omitempty"`
	Verbosity string      `json:"verbosity,omitempty"` // low, medium, high
}

// TextFormat 文本格式配置
type TextFormat struct {
	Type string `json:"type"` // text, json_schema

	// json_schema 类型字段
	Name   string      `json:"name,omitempty"`
	Schema interface{} `json:"schema,omitempty"`
	Strict *bool       `json:"strict,omitempty"`
}

// ResponsesResponse Codex/Responses API 响应
type ResponsesResponse struct {
	ID                   string          `json:"id"`
	Object               string          `json:"object"`
	CreatedAt            int64           `json:"created_at"`
	Model                string          `json:"model"`
	Status               string          `json:"status"` // in_progress, completed, failed
	Output               []OutputItem    `json:"output,omitempty"`
	Usage                *Usage          `json:"usage,omitempty"`
	PromptCacheKey       string          `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention json.RawMessage `json:"prompt_cache_retention,omitempty"`

	// 错误信息
	Error *ResponseError `json:"error,omitempty"`

	// 停止原因
	StopReason string `json:"stop_reason,omitempty"`

	// 不完整详情
	IncompleteDetails *IncompleteDetails `json:"incomplete_details,omitempty"`
}

// OutputItem 输出项
type OutputItem struct {
	Type string `json:"type"` // message, function_call, reasoning

	// message 类型字段
	Role    string        `json:"role,omitempty"`
	Content []ContentPart `json:"content,omitempty"`

	// function_call 类型字段
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// reasoning 类型字段
	Summary []ReasoningSummary `json:"summary,omitempty"`
}

// ReasoningSummary 推理摘要
type ReasoningSummary struct {
	Type string `json:"type"` // summary_text
	Text string `json:"text,omitempty"`
}

// Usage 使用情况
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	// 详细统计
	InputTokensDetails  *InputTokenDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *OutputTokenDetails `json:"output_tokens_details,omitempty"`
}

// InputTokenDetails 输入 token 详细统计
type InputTokenDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	TextTokens   int `json:"text_tokens,omitempty"`
	ImageTokens  int `json:"image_tokens,omitempty"`
}

// OutputTokenDetails 输出 token 详细统计
type OutputTokenDetails struct {
	TextTokens      int `json:"text_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// ResponseError 响应错误
type ResponseError struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
	PlanType string `json:"plan_type,omitempty"`
	ResetsAt *int64 `json:"resets_at,omitempty"`
}

// IncompleteDetails 不完整响应详情
type IncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

// GetModel 实现 ModelProvider 接口
func (r *ResponsesRequest) GetModel() string {
	return r.Model
}

// GetModel 实现 ModelProvider 接口
func (r *ResponsesResponse) GetModel() string {
	return r.Model
}

// --- 流式响应事件类型 ---

// StreamEvent 流式事件基础结构
// 注意：不同事件类型使用不同的索引字段
// - reasoning_summary_* 事件使用 summary_index
// - reasoning_text.* 事件使用 content_index
// - 其他事件使用 index
type StreamEvent struct {
	Type string `json:"type"`

	// 响应级别事件
	Response *ResponseData `json:"response,omitempty"`

	// 内容级别事件
	Item  *OutputItem `json:"item,omitempty"`
	Delta string      `json:"delta,omitempty"`

	// 索引字段（根据事件类型使用不同字段）
	Index        int  `json:"index,omitempty"`
	SummaryIndex *int `json:"summary_index,omitempty"` // reasoning_summary 事件专用
	ContentIndex *int `json:"content_index,omitempty"` // reasoning_text 事件专用

	// 使用量
	Usage *Usage `json:"usage,omitempty"`

	// Headers（用于提取 server model）
	Headers interface{} `json:"headers,omitempty"`
}

// ResponseData 响应数据
type ResponseData struct {
	ID                   string          `json:"id"`
	Model                string          `json:"model"`
	CreatedAt            int64           `json:"created_at"`
	Status               string          `json:"status"`
	Output               []OutputItem    `json:"output,omitempty"`
	Usage                *Usage          `json:"usage,omitempty"`
	StopReason           string          `json:"stop_reason,omitempty"`
	PromptCacheKey       string          `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention json.RawMessage `json:"prompt_cache_retention,omitempty"`

	// 错误信息
	Error *ResponseError `json:"error,omitempty"`

	// 不完整详情
	IncompleteDetails *IncompleteDetails `json:"incomplete_details,omitempty"`

	// Headers（包含 server model 信息）
	Headers map[string]interface{} `json:"headers,omitempty"`
}

// 事件类型常量
const (
	// 响应级别事件
	EventTypeResponseCreated    = "response.created"
	EventTypeResponseCompleted  = "response.completed"
	EventTypeResponseInProgress = "response.in_progress"
	EventTypeResponseDone       = "response.done"       // 增量完成
	EventTypeResponseFailed     = "response.failed"     // 响应失败
	EventTypeResponseIncomplete = "response.incomplete" // 响应不完整

	// 输出项事件
	EventTypeResponseOutputItemAdded = "response.output_item.added"
	EventTypeResponseOutputItemDone  = "response.output_item.done"

	// 内容部分事件（注意：Codex 客户端不处理这些事件）
	EventTypeResponseContentPartAdded = "response.content_part.added"
	EventTypeResponseContentPartDone  = "response.content_part.done"

	// 文本输出事件
	EventTypeResponseOutputTextDelta = "response.output_text.delta"

	// 推理/思考事件 - 使用 summary_index
	EventTypeResponseReasoningSummaryPartAdded = "response.reasoning_summary_part.added"
	EventTypeResponseReasoningSummaryPartDone  = "response.reasoning_summary_part.done"
	EventTypeResponseReasoningSummaryTextDelta = "response.reasoning_summary_text.delta"
	EventTypeResponseReasoningSummaryTextDone  = "response.reasoning_summary_text.done"

	// 推理文本事件 - 使用 content_index
	EventTypeResponseReasoningTextDelta = "response.reasoning_text.delta"

	// 工具调用事件
	EventTypeResponseFunctionCallArgumentsDelta = "response.function_call_arguments.delta"
)

// 错误码常量
const (
	ErrorCodeContextLengthExceeded = "context_length_exceeded"
	ErrorCodeInsufficientQuota     = "insufficient_quota"
	ErrorCodeUsageNotIncluded      = "usage_not_included"
	ErrorCodeInvalidPrompt         = "invalid_prompt"
	ErrorCodeServerOverloaded      = "server_is_overloaded"
	ErrorCodeSlowDown              = "slow_down"
	ErrorCodeRateLimitExceeded     = "rate_limit_exceeded"
)

// GetEffectiveIndex 获取有效索引（根据事件类型返回正确的索引）
func (e *StreamEvent) GetEffectiveIndex() int {
	if e.SummaryIndex != nil {
		return *e.SummaryIndex
	}
	if e.ContentIndex != nil {
		return *e.ContentIndex
	}
	return e.Index
}

// IsReasoningSummaryEvent 判断是否为 reasoning summary 事件
func (e *StreamEvent) IsReasoningSummaryEvent() bool {
	return e.SummaryIndex != nil
}

// IsReasoningTextEvent 判断是否为 reasoning text 事件
func (e *StreamEvent) IsReasoningTextEvent() bool {
	return e.ContentIndex != nil
}
