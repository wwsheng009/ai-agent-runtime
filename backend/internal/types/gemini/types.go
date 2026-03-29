package gemini

const (
	Name    = "gemini"
	Version = "1.0.0"
)

// GenerateContentRequest Gemini生成内容请求
type GenerateContentRequest struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []SafetySetting   `json:"safetySettings,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
}

// Content 内容
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part 内容部分
type Part struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
	FileData         *FileData         `json:"fileData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

// InlineData 内联数据（如 base64 图片/音频）
type InlineData struct {
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// FileData 文件数据（URL）
type FileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileUri  string `json:"fileUri,omitempty"`
}

// FunctionCall 函数调用
type FunctionCall struct {
	Name string                 `json:"name,omitempty"`
	Args map[string]interface{} `json:"args,omitempty"`
	ID   string                 `json:"id,omitempty"`
}

// FunctionResponse 函数响应
type FunctionResponse struct {
	Name     string                 `json:"name,omitempty"`
	Response map[string]interface{} `json:"response,omitempty"`
	ID       string                 `json:"id,omitempty"`
}

// GetModel 实现 ModelProvider 接口 (Gemini 请求中不包含 model 字段，从 URL 或配置获取)
func (r *GenerateContentRequest) GetModel() string {
	return ""
}

// GetMessageCount 实现 MessageCountProvider 接口
func (r *GenerateContentRequest) GetMessageCount() int {
	return len(r.Contents)
}

// GetSize 实现 SizeProvider 接口
func (r *GenerateContentRequest) GetSize() int {
	size := 0

	// systemInstruction
	if r.SystemInstruction != nil {
		size += len(r.SystemInstruction.Role)
		for _, part := range r.SystemInstruction.Parts {
			size += len(part.Text)
			if part.InlineData != nil {
				size += len(part.InlineData.MimeType) + len(part.InlineData.Data)
			}
			if part.FileData != nil {
				size += len(part.FileData.MimeType) + len(part.FileData.FileUri)
			}
		}
	}

	for _, content := range r.Contents {
		size += len(content.Role)
		for _, part := range content.Parts {
			size += len(part.Text)
			if part.InlineData != nil {
				size += len(part.InlineData.MimeType) + len(part.InlineData.Data)
			}
			if part.FileData != nil {
				size += len(part.FileData.MimeType) + len(part.FileData.FileUri)
			}
		}
	}
	return size
}

// GenerationConfig 生成配置
type GenerationConfig struct {
	Temperature      *float64               `json:"temperature,omitempty"`
	TopP             *float64               `json:"topP,omitempty"`
	TopK             *int                   `json:"topK,omitempty"`
	MaxOutputTokens  *int                   `json:"maxOutputTokens,omitempty"`
	StopSequences    []string               `json:"stopSequences,omitempty"`
	CandidateCount   *int                   `json:"candidateCount,omitempty"`
	ResponseMimeType string                 `json:"responseMimeType,omitempty"`
	ResponseSchema   map[string]interface{} `json:"responseSchema,omitempty"`
	// ThinkingConfig 思考配置（Gemini 2.0+ 支持）
	ThinkingConfig *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

// ThinkingConfig Gemini 思考配置
// 用于 Gemini 2.0+ 的 Extended Thinking 功能
type ThinkingConfig struct {
	// ThinkingBudget 思考预算（token 数量）
	// Gemini 2.5 Pro: 128 - 32768
	// Gemini 2.5 Flash: 0 - 24576
	ThinkingBudget *int `json:"thinkingBudget,omitempty"`
	// IncludeThoughts 是否在响应中包含思考过程
	IncludeThoughts bool `json:"includeThoughts,omitempty"`
}

// SafetySetting 安全设置
type SafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// GenerateContentResponse Gemini生成内容响应
type GenerateContentResponse struct {
	Candidates     []Candidate     `json:"candidates"`
	PromptFeedback *PromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  *UsageMetadata  `json:"usageMetadata,omitempty"`
	ModelVersion   string          `json:"modelVersion,omitempty"`
	ResponseID     string          `json:"responseId,omitempty"`
}

// Candidate 候选结果
type Candidate struct {
	Content       Content        `json:"content"`
	FinishReason  string         `json:"finishReason"`
	Index         int            `json:"index"`
	SafetyRatings []SafetyRating `json:"safetyRatings"`
}

// SafetyRating 安全评分
type SafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

// PromptFeedback 提示词反馈
type PromptFeedback struct {
	SafetyRatings []SafetyRating `json:"safetyRatings"`
}

// UsageMetadata 使用情况
type UsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// Tool 工具定义
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
	CodeExecution        map[string]string     `json:"codeExecution,omitempty"`
	GoogleSearch         map[string]string     `json:"googleSearch,omitempty"`
	URLContext           map[string]string     `json:"urlContext,omitempty"`
}

// FunctionDeclaration 函数定义
type FunctionDeclaration struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

// ToolConfig 工具配置
type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// FunctionCallingConfig 函数调用配置
type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"` // AUTO/NONE/ANY
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}
