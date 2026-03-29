package anthropic

import (
	"encoding/json"
	"fmt"
)

const (
	Name    = "anthropic"
	Version = "1.0.0"
)

// MessageChunkType 消息块类型常量
type MessageChunkType string

const (
	MessageChunkTypeMessageStart      MessageChunkType = "message_start"
	MessageChunkTypeContentBlockStart MessageChunkType = "content_block_start"
	MessageChunkTypeContentBlockDelta MessageChunkType = "content_block_delta"
	MessageChunkTypeContentBlockStop  MessageChunkType = "content_block_stop"
	MessageChunkTypeMessageDelta      MessageChunkType = "message_delta"
	MessageChunkTypeMessageStop       MessageChunkType = "message_stop"
)

// MessageRequest Anthropic消息请求
type MessageRequest struct {
	Model         string                 `json:"model"`
	Messages      []Message              `json:"messages,omitempty"`
	System        []ContentBlock         `json:"system,omitempty"`
	MaxTokens     int                    `json:"max_tokens,omitempty"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	Stream        bool                   `json:"stream,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Tools         []Tool                 `json:"tools,omitempty"`
	ToolChoice    *ToolChoice            `json:"tool_choice,omitempty"`
	Thinking      *Thinking              `json:"thinking,omitempty"` // Thinking 参数
	Metadata      map[string]interface{} `json:"metadata,omitempty"` // 元数据（用于传递供应商特定参数）
}

// Message 消息
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// UnmarshalJSON 兼容 content 字段为 string 或 []ContentBlock
func (m *Message) UnmarshalJSON(data []byte) error {
	type messageAlias struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	var aux messageAlias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	m.Role = aux.Role
	if len(aux.Content) == 0 {
		m.Content = nil
		return nil
	}

	// 优先解析为 []ContentBlock
	var blocks []ContentBlock
	if err := json.Unmarshal(aux.Content, &blocks); err == nil {
		m.Content = blocks
		return nil
	}

	// 回退解析为 string
	var text string
	if err := json.Unmarshal(aux.Content, &text); err == nil {
		m.Content = []ContentBlock{{Type: "text", Text: text}}
		return nil
	}

	return fmt.Errorf("invalid message content format")
}

// GetModel 实现 ModelProvider 接口
func (r *MessageRequest) GetModel() string {
	return r.Model
}

// GetMessageCount 实现 MessageCountProvider 接口
func (r *MessageRequest) GetMessageCount() int {
	return len(r.Messages)
}

// GetSize 实现 SizeProvider 接口
func (r *MessageRequest) GetSize() int {
	size := len(r.Model)
	for _, msg := range r.Messages {
		size += len(msg.Role)
		for _, block := range msg.Content {
			size += len(block.Type) + len(block.Text)
		}
	}
	for _, sys := range r.System {
		size += len(sys.Type) + len(sys.Text)
	}
	return size
}

// MessageResponse Anthropic消息响应
type MessageResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason,omitempty"`
	Usage      Usage          `json:"usage,omitempty"`
}

// ContentBlock 内容块
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// tool_result 类型的字段（content 用于工具结果）
	Content   interface{} `json:"content,omitempty"`     // 当 type=tool_result 时使用（可能是 string 或 []ContentBlock）
	ToolUseID string      `json:"tool_use_id,omitempty"` // 当 type=tool_result 时使用
	IsError   bool        `json:"is_error,omitempty"`    // 当 type=tool_result 时使用

	// tool_use 类型的字段
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	// cache_control 缓存控制（Claude 特有，OpenAI 不支持）
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// Usage 使用情况
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ErrorResponse 错误响应
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 错误详情
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Tool 工具定义
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
	// WebSearch 工具特定字段（用于内部存储）
	WebSearch *WebSearchTool `json:"-"` // 不序列化到 JSON，通过 MarshalJSON 处理
}

// MarshalJSON 自定义 JSON 序列化，处理 WebSearch 工具
func (t Tool) MarshalJSON() ([]byte, error) {
	// 如果是 WebSearch 工具，使用特殊的序列化格式
	if t.WebSearch != nil && t.WebSearch.Type == "web_search_20250305" {
		// 创建一个符合 Anthropic API 格式的 web_search 工具对象
		webSearchObj := map[string]interface{}{
			"name":         t.WebSearch.Name,
			"type":         t.WebSearch.Type,
			"display_name": t.Description, // Anthropic 使用 display_name
		}

		// 添加可选字段
		if t.WebSearch.UserLocation != nil {
			webSearchObj["user_location"] = t.WebSearch.UserLocation
		}
		if t.WebSearch.MaxUses > 0 {
			webSearchObj["max_uses"] = t.WebSearch.MaxUses
		}

		return json.Marshal(webSearchObj)
	}

	// 标准工具序列化
	type ToolAlias Tool
	return json.Marshal(struct {
		ToolAlias
	}{
		ToolAlias: ToolAlias(t),
	})
}

// WebSearchTool Web搜索工具（Anthropic web_search_20250305）
type WebSearchTool struct {
	Name         string                 `json:"name"`                    // "web_search"
	Type         string                 `json:"type"`                    // "web_search_20250305"
	DisplayName  string                 `json:"display_name,omitempty"`  // 显示名称
	UserLocation *WebSearchUserLocation `json:"user_location,omitempty"` // 用户位置
	MaxUses      int                    `json:"max_uses,omitempty"`      // 最大使用次数
}

// WebSearchUserLocation Web搜索用户位置
type WebSearchUserLocation struct {
	Type     string `json:"type"`               // "approximate"
	Timezone string `json:"timezone,omitempty"` // 时区，如 "America/New_York"
	Country  string `json:"country,omitempty"`  // 国家代码，如 "US"
	Region   string `json:"region,omitempty"`   // 地区/州代码，如 "CA"
	City     string `json:"city,omitempty"`     // 城市名称，如 "San Francisco"
}

// ToolChoice 工具选择
type ToolChoice struct {
	Type                   string  `json:"type"`                      // "auto", "any", "tool"
	Name                   *string `json:"name,omitempty"`            // tool name when type="tool"
	DisableParallelToolUse bool    `json:"disable_parallel_tool_use"` // 默认 false
}

// ToolUse 工具调用
type ToolUse struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// CacheControl 缓存控制
type CacheControl struct {
	Type string `json:"type"`
}

// MessageChunk Anthropic流式消息块
type MessageChunk struct {
	Type MessageChunkType `json:"type"`

	// message_start
	Message *MessageResponse `json:"message,omitempty"`

	// content_block_start
	Index        int           `json:"index,omitempty"`
	ContentBlock *ContentBlock `json:"content_block,omitempty"`

	// content_block_delta - Delta 字段用于 content_block_delta 事件
	Delta *ContentBlockDelta `json:"delta,omitempty"`

	// content_block_stop
	ContentBlockStopIndex int `json:"index,omitempty"`

	// message_delta - 使用不同的 JSON 字段名避免冲突
	MessageDelta *MessageDeltaData `json:"message_delta,omitempty"`

	// message_delta 的 usage 在外层
	Usage *ChunkUsage `json:"usage,omitempty"`

	// message_stop
	MessageStop *MessageStop `json:"message_stop,omitempty"`
}

// MessageDeltaData message_delta 事件的 delta 数据
type MessageDeltaData struct {
	Type         string  `json:"type"`
	StopReason   *string `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// UnmarshalJSON 自定义 JSON 解析，处理 message_delta 事件的特殊情况
func (m *MessageChunk) UnmarshalJSON(data []byte) error {
	// 首先尝试标准解析
	type Alias MessageChunk
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// 对于 message_delta 类型，需要从嵌套的 delta 字段中提取数据
	if m.Type == MessageChunkTypeMessageDelta {
		// 定义临时结构来解析嵌套的 delta
		temp := struct {
			Type  string            `json:"type"`
			Delta *MessageDeltaData `json:"delta,omitempty"`
			Usage *ChunkUsage       `json:"usage,omitempty"`
		}{}
		if err := json.Unmarshal(data, &temp); err != nil {
			return err
		}
		m.MessageDelta = temp.Delta
		m.Usage = temp.Usage
	}

	return nil
}

// ContentBlockDelta 内容块增量
type ContentBlockDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`

	// tool_use 类型的字段
	ToolUseID   string                 `json:"tool_use_id,omitempty"`
	Name        string                 `json:"name,omitempty"`
	Input       map[string]interface{} `json:"input,omitempty"`
	PartialJSON string                 `json:"partial_json,omitempty"`
}

// MessageDelta 消息增量
type MessageDelta struct {
	Type         string      `json:"type"`
	StopReason   *string     `json:"stop_reason,omitempty"`
	StopSequence *string     `json:"stop_sequence,omitempty"`
	Usage        *ChunkUsage `json:"usage,omitempty"`
}

// ChunkUsage 块使用情况（用于 MessageDelta 等块级事件）
type ChunkUsage struct {
	InputTokens  int `json:"input_tokens"`  // 输入token数（message_delta 中提供）
	OutputTokens int `json:"output_tokens"` // 输出token数
}

// MessageStop 消息停止
type MessageStop struct {
	Type string `json:"type"`
}

// ToolResult 工具结果（用于工具调用后的响应）
type ToolResult struct {
	Type      string `json:"type"` // "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ContentBlockWithToolUse 带工具调用的内容块
type ContentBlockWithToolUse struct {
	Type  string                 `json:"type"` // "tool_use"
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// ContentBlockWithToolResult 带工具结果的内容块
type ContentBlockWithToolResult struct {
	Type      string `json:"type"` // "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// Thinking Claude Thinking 推理参数
type Thinking struct {
	Type         string `json:"type"`                    // "enabled", "disabled", "adaptive", "low", "medium", "high"
	BudgetTokens *int   `json:"budget_tokens,omitempty"` // 最大推理 token 数
	Effort       string `json:"effort,omitempty"`        // adaptive 模式下的力度提示，如 low/medium/high/max
}
