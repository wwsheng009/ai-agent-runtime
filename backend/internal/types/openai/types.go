package openai

import (
	"encoding/json"
)

const (
	Name    = "openai"
	Version = "2.0.0" // 版本升级
)

// ChatCompletionRequest OpenAI聊天完成请求
type ChatCompletionRequest struct {
	Model               string           `json:"model"`
	Messages            []Message        `json:"messages,omitempty"`
	Temperature         *float64         `json:"temperature,omitempty"`
	MaxTokens           *int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int             `json:"max_completion_tokens,omitempty"` // 推理模型使用
	TopP                *float64         `json:"top_p,omitempty"`
	TopK                *int             `json:"top_k,omitempty"` // Top-K 采样参数
	Stream              bool             `json:"stream,omitempty"`
	StreamOptions       *StreamOptions   `json:"stream_options,omitempty"` // 流式响应选项
	Stop                []string         `json:"stop,omitempty"`
	N                   *int             `json:"n,omitempty"`
	Tools               []Tool           `json:"tools,omitempty"`
	ToolChoice          interface{}      `json:"tool_choice,omitempty"` // string or object
	ParallelToolCalls   *bool            `json:"parallel_tool_calls,omitempty"` // 并行工具调用控制
	User                string           `json:"user,omitempty"` // 用户标识符
	FrequencyPenalty    *float64         `json:"frequency_penalty,omitempty"` // 频率惩罚（-2.0 到 2.0）
	PresencePenalty     *float64         `json:"presence_penalty,omitempty"` // 存在惩罚（-2.0 到 2.0）
	Seed                *float64         `json:"seed,omitempty"` // 随机种子，用于可重现的输出
	LogProbs            *bool            `json:"logprobs,omitempty"` // 是否返回对数概率
	TopLogProbs         *int             `json:"top_logprobs,omitempty"` // 返回顶部对数概率数量（0-5）
	EncodingFormat      json.RawMessage  `json:"encoding_format,omitempty"` // token 编码格式
	ReasoningEffort     string           `json:"reasoning_effort,omitempty"` // 推理力度（o1/gpt-5）
	Reasoning           *Reasoning       `json:"reasoning,omitempty"` // 推理参数
	WebSearchOptions    *WebSearchOptions `json:"web_search_options,omitempty"` // Web搜索选项
	ResponseFormat      *ResponseFormat  `json:"response_format,omitempty"` // 响应格式（JSON 模式、结构化输出）
	Audio               json.RawMessage  `json:"audio,omitempty"` // 音频选项（TTS/STT）
	Modalities          json.RawMessage  `json:"modalities,omitempty"` // 多模态配置
	THINKING            *json.RawMessage `json:"thinking,omitempty"` // Claude Thinking 格式（内部使用）
}

// Message 消息
type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"` // string or []MediaContent - 支持多模态
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // 工具调用（用于响应）
	ToolCallID string     `json:"tool_call_id,omitempty"` // 工具调用ID（用于role=tool的响应消息）
	Name       *string    `json:"name,omitempty"` // 消息名称（用于tool消息）

	// 内部缓存
	parsedContent []MediaContent `json:"-"` // 缓存解析后的多模态内容
}

// StringContent 返回字符串内容（向后兼容）
// 支持多种 Content 格式：
//   - string: 直接返回
//   - []MediaContent: 返回第一个 text 类型的内容
//   - []interface{}: JSON 反序列化后的格式，提取第一个 text
func (m *Message) StringContent() string {
	if m.Content == nil {
		return ""
	}

	switch v := m.Content.(type) {
	case string:
		return v
	case []MediaContent:
		for _, c := range v {
			if c.Type == "text" {
				return c.Text
			}
		}
	case []interface{}:
		// JSON 反序列化后的格式: [{"type": "text", "text": "..."}]
		for _, item := range v {
			if itemMap, ok := item.(map[string]interface{}); ok {
				if getTextFromMap(itemMap) != "" {
					return getTextFromMap(itemMap)
				}
			}
		}
	}
	return ""
}

// getTextFromMap 从 map 中提取 text 字段（仅当 type 为 text 时）
func getTextFromMap(m map[string]interface{}) string {
	if typ, ok := m["type"].(string); ok && typ == "text" {
		if text, ok := m["text"].(string); ok {
			return text
		}
	}
	return ""
}

// ParseContent 解析多模态内容
// 支持多种 Content 格式并缓存结果
func (m *Message) ParseContent() []MediaContent {
	if m.parsedContent != nil {
		return m.parsedContent
	}

	if m.Content == nil {
		m.parsedContent = []MediaContent{}
		return m.parsedContent
	}

	switch v := m.Content.(type) {
	case string:
		m.parsedContent = []MediaContent{{Type: "text", Text: v}}
		return m.parsedContent

	case []MediaContent:
		m.parsedContent = v
		return m.parsedContent

	case []interface{}:
		// JSON 反序列化后的格式
		m.parsedContent = parseMediaContentFromSlice(v)
		return m.parsedContent

	case []byte:
		// 原始 JSON 字节
		var contents []MediaContent
		if json.Unmarshal(v, &contents) == nil {
			m.parsedContent = contents
			return m.parsedContent
		}
	}

	m.parsedContent = []MediaContent{}
	return m.parsedContent
}

// parseMediaContentFromSlice 从 []interface{} 解析 MediaContent
func parseMediaContentFromSlice(items []interface{}) []MediaContent {
	result := make([]MediaContent, 0, len(items))
	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		mc := MediaContent{}
		if typ, ok := itemMap["type"].(string); ok {
			mc.Type = typ
		}
		if text, ok := itemMap["text"].(string); ok {
			mc.Text = text
		}

		// 解析 image_url
		if imageURL, ok := itemMap["image_url"].(map[string]interface{}); ok {
			mc.ImageUrl = parseMessageImageUrl(imageURL)
		}

		// 解析 input_audio
		if inputAudio, ok := itemMap["input_audio"].(map[string]interface{}); ok {
			mc.InputAudio = parseMessageInputAudio(inputAudio)
		}

		// 解析 file
		if file, ok := itemMap["file"].(map[string]interface{}); ok {
			mc.File = parseMessageFile(file)
		}

		// 解析 video_url
		if videoURL, ok := itemMap["video_url"].(map[string]interface{}); ok {
			mc.VideoUrl = parseMessageVideoUrl(videoURL)
		}

		result = append(result, mc)
	}
	return result
}

// parseMessageImageUrl 解析 MessageImageUrl
func parseMessageImageUrl(m map[string]interface{}) *MessageImageUrl {
	img := &MessageImageUrl{}
	if url, ok := m["url"].(string); ok {
		img.Url = url
	}
	if detail, ok := m["detail"].(string); ok {
		img.Detail = detail
	}
	if mimeType, ok := m["mime_type"].(string); ok {
		img.MimeType = mimeType
	}
	return img
}

// parseMessageInputAudio 解析 MessageInputAudio
func parseMessageInputAudio(m map[string]interface{}) *MessageInputAudio {
	audio := &MessageInputAudio{}
	if data, ok := m["data"].(string); ok {
		audio.Data = data
	}
	if format, ok := m["format"].(string); ok {
		audio.Format = format
	}
	return audio
}

// parseMessageFile 解析 MessageFile
func parseMessageFile(m map[string]interface{}) *MessageFile {
	file := &MessageFile{}
	if fileName, ok := m["filename"].(string); ok {
		file.FileName = fileName
	}
	if fileData, ok := m["file_data"].(string); ok {
		file.FileData = fileData
	}
	if fileId, ok := m["file_id"].(string); ok {
		file.FileId = fileId
	}
	return file
}

// parseMessageVideoUrl 解析 MessageVideoUrl
func parseMessageVideoUrl(m map[string]interface{}) *MessageVideoUrl {
	video := &MessageVideoUrl{}
	if url, ok := m["url"].(string); ok {
		video.Url = url
	}
	return video
}

// SetMediaContent 设置多模态内容
func (m *Message) SetMediaContent(content []MediaContent) {
	m.Content = content
	m.parsedContent = content
}

// IsToolCallMessage 检查是否为工具调用消息
func (m *Message) IsToolCallMessage() bool {
	return len(m.ToolCalls) > 0
}

// IsEmpty 检查消息是否为空
func (m *Message) IsEmpty() bool {
	content := m.StringContent()
	return len(content) == 0 && len(m.ToolCalls) == 0
}

// ToolCall 工具调用
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction 工具函数
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool 工具定义
type Tool struct {
	Type     string    `json:"type"`              // "function"
	Function Function  `json:"function"`
}

// Function 函数定义
type Function struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
	Strict      bool                   `json:"strict,omitempty"`
}

// GetModel 实现 ModelProvider 接口
func (r *ChatCompletionRequest) GetModel() string {
	return r.Model
}

// GetMessageCount 实现 MessageCountProvider 接口
func (r *ChatCompletionRequest) GetMessageCount() int {
	return len(r.Messages)
}

// GetSize 实现 SizeProvider 接口
func (r *ChatCompletionRequest) GetSize() int {
	size := len(r.Model)
	for _, msg := range r.Messages {
		size += len(msg.Role) + len(msg.StringContent())
	}
	if r.MaxTokens != nil {
		size += 4 // max_tokens 字段开销
	}
	return size
}

// GetModel 实现 ModelProvider 接口
func (r *ChatCompletionResponse) GetModel() string {
	return r.Model
}

// ChatCompletionResponse OpenAI聊天完成响应
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage,omitempty"`
}

// Choice 选择
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage 使用情况
type Usage struct {
	PromptTokens     int                `json:"prompt_tokens"`
	CompletionTokens int                `json:"completion_tokens"`
	TotalTokens      int                `json:"total_tokens"`

	// 缓存 tokens
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens,omitempty"`

	// 详细 token 统计
	PromptTokensDetails    *InputTokenDetails  `json:"prompt_tokens_details,omitempty"`
	CompletionTokenDetails *OutputTokenDetails `json:"completion_tokens_details,omitempty"`

	// 备用字段（某些提供商使用不同字段名）
	InputTokens            int                `json:"input_tokens,omitempty"`
	OutputTokens           int                `json:"output_tokens,omitempty"`
	InputTokensDetails     *InputTokenDetails `json:"input_tokens_details,omitempty"`
}

// InputTokenDetails 输入 token 详细统计
type InputTokenDetails struct {
	CachedTokens         int `json:"cached_tokens,omitempty"`         // 缓存的 token 数
	CachedCreationTokens int `json:"cached_creation_tokens,omitempty"` // 缓存创建的 token 数
	TextTokens           int `json:"text_tokens,omitempty"`           // 文本 token 数
	AudioTokens          int `json:"audio_tokens,omitempty"`          // 音频 token 数
	ImageTokens          int `json:"image_tokens,omitempty"`          // 图片 token 数
}

// OutputTokenDetails 输出 token 详细统计
type OutputTokenDetails struct {
	TextTokens      int `json:"text_tokens,omitempty"`      // 文本 token 数
	AudioTokens     int `json:"audio_tokens,omitempty"`     // 音频 token 数
	ReasoningTokens int `json:"reasoning_tokens,omitempty"` // 推理 token 数
}

// ErrorResponse 错误响应
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 错误详情
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// ChatCompletionChunk OpenAI流式响应块
type ChatCompletionChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	// Usage 令牌使用情况（仅在最终 chunk 中返回）
	Usage *Usage `json:"usage,omitempty"`
}

// ChunkChoice 块选择
type ChunkChoice struct {
	Index        int              `json:"index"`
	Delta        ChunkDelta       `json:"delta"`
	FinishReason *string          `json:"finish_reason,omitempty"`
}

// ChunkDelta 块增量
type ChunkDelta struct {
	Role            string      `json:"role,omitempty"`
	Content         *string     `json:"content,omitempty"`      // 改为指针
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Reasoning        *string     `json:"reasoning,omitempty"`        // 新增：推理内容（o1/gpt-5）
	ToolCalls       []ToolCallDelta `json:"tool_calls,omitempty"` // 流式工具调用
}

// MarshalJSON 自定义 JSON 序列化，避免输出 null 值
func (cd ChunkDelta) MarshalJSON() ([]byte, error) {
	type alias ChunkDelta // 避免递归调用
	tmp := struct {
		Role            *string           `json:"role,omitempty"`
		Content         *string           `json:"content,omitempty"`
		ReasoningContent string           `json:"reasoning_content,omitempty"`
		Reasoning       *string           `json:"reasoning,omitempty"`
		ToolCalls       []ToolCallDelta   `json:"tool_calls,omitempty"`
	}{}
	
	// 只设置非空字符串的 Role
	if cd.Role != "" {
		tmp.Role = &cd.Role
	}
	// Content 已经是指针，直接赋值（nil 会被 omitempty 忽略）
	tmp.Content = cd.Content
	// ReasoningContent 非空时才设置
	if cd.ReasoningContent != "" {
		tmp.ReasoningContent = cd.ReasoningContent
	}
	// Reasoning 已经是指针，直接赋值
	tmp.Reasoning = cd.Reasoning
	// ToolCalls 非空时才设置
	if len(cd.ToolCalls) > 0 {
		tmp.ToolCalls = cd.ToolCalls
	}
	
	return json.Marshal(tmp)
}

// ToolCallDelta 流式工具调用增量
type ToolCallDelta struct {
	Index    *int               `json:"index,omitempty"` // 改为指针，增加灵活性
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function *ToolFunctionDelta `json:"function,omitempty"`
}

// MarshalJSON 自定义 JSON 序列化，避免输出 null 值
func (tcd ToolCallDelta) MarshalJSON() ([]byte, error) {
	type alias ToolCallDelta // 避免递归调用
	tmp := struct {
		Index    *int               `json:"index,omitempty"`
		ID       string             `json:"id,omitempty"`
		Type     string             `json:"type,omitempty"`
		Function *ToolFunctionDelta `json:"function,omitempty"`
	}{}

	// Index 已经是指针，直接赋值
	tmp.Index = tcd.Index
	// ID 非空时才设置
	if tcd.ID != "" {
		tmp.ID = tcd.ID
	}
	// Type 非空时才设置
	if tcd.Type != "" {
		tmp.Type = tcd.Type
	}
	// Function 已经是指针，直接赋值
	tmp.Function = tcd.Function

	return json.Marshal(tmp)
}

// ToolFunctionDelta 流式工具函数增量
type ToolFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// MarshalJSON 自定义 JSON 序列化，避免输出空字符串为 null
// 注意：OpenAI 流式工具调用中，空字符串 "" 是有意义的（表示增量结束）
func (tfd ToolFunctionDelta) MarshalJSON() ([]byte, error) {
	type alias ToolFunctionDelta // 避免递归调用
	tmp := struct {
		Name      *string `json:"name,omitempty"`
		Arguments *string `json:"arguments,omitempty"`
	}{}

	// 只设置非空字符串的 Name
	if tfd.Name != "" {
		tmp.Name = &tfd.Name
	}
	// Arguments 可能为空字符串（表示增量结束），需要特殊处理
	// 如果 Arguments 非空，或者 Name 也为空，则设置 Arguments 字段
	if tfd.Arguments != "" || tfd.Name == "" {
		tmp.Arguments = &tfd.Arguments
	}

	return json.Marshal(tmp)
}

// MessageWithToolCall 带工具调用的消息（用于响应）
type MessageWithToolCall struct {
	Role      string     `json:"role"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Name      string     `json:"name,omitempty"` // 当 role=tool 时，工具名称
}

// ToolMessage 工具响应消息
type ToolMessage struct {
	Role       string `json:"role"` // "tool"
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
}

// Reasoning 推理参数（用于 o1, o3, gpt-5 等推理模型）
type Reasoning struct {
	MaxTokens int    `json:"max_tokens,omitempty"` // 最大推理 token 数
	Effort    string `json:"effort,omitempty"`    // 推理级别: "low", "medium", "high"
}

// Thinking Claude Thinking 格式（用于内部转换）
type Thinking struct {
	Type         string `json:"type"`                   // "enabled", "disabled", "adaptive"
	BudgetTokens *int   `json:"budget_tokens,omitempty"` // 最大推理 token 数
}

// WebSearchOptions Web搜索选项（用于 Web Search 工具）
type WebSearchOptions struct {
	UserLocation        *WebSearchUserLocation `json:"user_location,omitempty"`         // 用户位置
	SearchContextSize   string                 `json:"search_context_size,omitempty"`  // 搜索上下文大小: "low", "medium", "high"
}

// WebSearchUserLocation Web搜索用户位置
type WebSearchUserLocation struct {
	Type     string `json:"type"`                // "approximate"
	Timezone string `json:"timezone,omitempty"`  // 时区，如 "America/New_York"
	Country  string `json:"country,omitempty"`   // 国家代码，如 "US"
	Region   string `json:"region,omitempty"`    // 地区/州代码，如 "CA"
	City     string `json:"city,omitempty"`      // 城市名称，如 "San Francisco"
}

// MediaContent 多模态内容项（支持文本、图片、音频、视频、文件）
type MediaContent struct {
	Type       string             `json:"type"`                // text, image_url, input_audio, file, video_url
	Text       string             `json:"text,omitempty"`

	// 图片支持
	ImageUrl   *MessageImageUrl   `json:"image_url,omitempty"`

	// 音频支持
	InputAudio *MessageInputAudio  `json:"input_audio,omitempty"`

	// 文件支持
	File       *MessageFile        `json:"file,omitempty"`

	// 视频支持
	VideoUrl   *MessageVideoUrl    `json:"video_url,omitempty"`

	// 缓存控制（OpenRouter 特定）
	CacheControl json.RawMessage    `json:"cache_control,omitempty"`
}

// MessageImageUrl 图片 URL（支持 base64 和外部 URL）
type MessageImageUrl struct {
	Url      string `json:"url"`                     // 图片 URL 或 data URL
	Detail   string `json:"detail,omitempty"`    // low, high, auto
	MimeType string `json:"mime_type,omitempty"` // MIME 类型
}

// MessageInputAudio 音频输入（base64 编码）
type MessageInputAudio struct {
	Data   string `json:"data"`   // base64 编码的音频数据
	Format string `json:"format"` // 音频格式：wav, mp3, m4a, etc.
}

// MessageFile 文件附件
type MessageFile struct {
	FileName string `json:"filename,omitempty"` // 文件名
	FileData string `json:"file_data,omitempty"` // base64 编码的文件数据
	FileId   string `json:"file_id,omitempty"`   // 文件 ID
}

// MessageVideoUrl 视频 URL
type MessageVideoUrl struct {
	Url string `json:"url"` // 视频 URL
}

// ResponseFormat 响应格式配置（用于 JSON 模式和结构化输出）
type ResponseFormat struct {
	Type       string          `json:"type,omitempty"` // "text", "json_object", "json_schema"
	JsonSchema json.RawMessage `json:"json_schema,omitempty"` // 当 type="json_schema" 时的 schema 定义
}

// FormatJsonSchema JSON Schema 格式定义（用于 ResponseFormat）
type FormatJsonSchema struct {
	Description string          `json:"description,omitempty"` // 描述
	Name        string          `json:"name"`                   // 名称
	Schema      any             `json:"schema,omitempty"`       // JSON Schema
	Strict      json.RawMessage `json:"strict,omitempty"`       // 是否严格模式
}

// StreamOptions 流式响应选项
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"` // 是否在流式响应中包含 usage 信息
}
