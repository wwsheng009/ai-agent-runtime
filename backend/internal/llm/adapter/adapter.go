package adapter

import (
	"io"
	"time"

	anthropictypes "github.com/ai-gateway/ai-agent-runtime/internal/types/anthropic"
)

// ToolCall 工具调用定义（简化版本，避免依赖 functions 包）
type ToolCall struct {
	ID       string           `json:"id"`
	Function ToolCallFunction `json:"function"`
	Type     string           `json:"type"`
}

// ToolCallFunction 工具调用函数
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// RequestConfig 请求配置
type RequestConfig struct {
	Model           string
	Messages        []map[string]interface{} // 支持复杂消息格式（Function Call 等）
	Stream          bool
	MaxTokens       int
	ReasoningEffort string
	Thinking        *anthropictypes.Thinking
	Temperature     float64
	Timeout         time.Duration
	Functions       interface{} // Function Call 配置（tools, tools 等）
}

// AdapterConfig 适配器配置
type AdapterConfig struct {
	Type        string                 // provider 类型
	APIKey      string                 // API key
	Timeout     time.Duration          // 请求超时
	Model       string                 // 最终发送到上游的模型
	RequestBody map[string]interface{} // 已构建的请求体，用于 header 派生逻辑
	Headers     map[string]string      // 调用方附加的自定义 headers
}

// ProtocolAdapter 协议适配器接口
type ProtocolAdapter interface {
	// Name 返回适配器名称
	Name() string

	// BuildRequest 构建请求体
	BuildRequest(config RequestConfig) map[string]interface{}

	// BuildHeaders 构建请求头
	BuildHeaders(adapterConfig AdapterConfig) map[string]string

	// ExtractResponse 从响应中提取内容
	ExtractResponse(result map[string]interface{}) string

	// ExtractReasoning 从响应中提取推理内容（thinking）
	ExtractReasoning(result map[string]interface{}) string

	// ExtractStreamContent 从流式响应中提取内容
	ExtractStreamContent(result map[string]interface{}) string

	// ExtractStreamReasoning 从流式响应中提取推理内容（推理模型）
	ExtractStreamReasoning(result map[string]interface{}) string

	// BuildAssistantMessage 构建 assistant 消息（用于添加到对话历史）
	// content: 文本内容
	// toolCalls: ToolCalls 数据（从响应中提取的原始格式）
	// reasoning: 推理内容
	BuildAssistantMessage(content string, toolCalls []map[string]interface{}, reasoning string) map[string]interface{}

	// ExtractToolCallsFromRawCalls 从已解析的 tool_calls 数组直接构造 ToolCall 列表
	// 用于流式响应，避免重新序列化/解析可能造成的问题
	ExtractToolCallsFromRawCalls(rawCalls []map[string]interface{}) []ToolCall

	// HandleResponse 处理完整响应（流式或非流式）
	// isStream: 是否为流式请求
	// respBody: 响应体
	// onContent: 回调函数，流式模式下实时返回 content 用于打印
	// 返回: assistant 消息（可直接添加到 messages），包含 role, content, tool_calls, reasoning_content 等
	HandleResponse(isStream bool, respBody io.Reader, onContent func(string)) (map[string]interface{}, error)

	// ProcessResponse 统一处理响应，提取 reasoning、content 和 tool_calls
	ProcessResponse(result map[string]interface{}) ProcessResult

	// IsReasoningModel 判断是否为推理模型
	IsReasoningModel(model string) bool

	// GetAPIPath 获取默认 API 路径
	GetAPIPath() string
}

// ProcessResult 响应处理结果
type ProcessResult struct {
	Reasoning    string                   // 推理/thinking 内容
	Content      string                   // 普通内容
	HasToolCalls bool                     // 是否包含 Function Call
	ToolCalls    []map[string]interface{} // Function Call 原始数据
}
