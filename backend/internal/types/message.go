package types

// ContentPartType identifies the kind of a structured content part.
type ContentPartType string

const (
	ContentPartText  ContentPartType = "text"
	ContentPartImage ContentPartType = "image"
)

// ContentPart represents one element in a multimodal message. When present,
// ContentParts on Message takes precedence over the flat Content string for
// protocol serialization.
type ContentPart struct {
	Type     ContentPartType `json:"type" yaml:"type"`
	Text     string          `json:"text,omitempty" yaml:"text,omitempty"`
	ImageURL string          `json:"image_url,omitempty" yaml:"image_url,omitempty"` // data: or https: URL
	MimeType string          `json:"mime_type,omitempty" yaml:"mime_type,omitempty"` // e.g. "image/png"
	Path     string          `json:"path,omitempty" yaml:"path,omitempty"`           // original file path (for display)
	Source   string          `json:"source,omitempty" yaml:"source,omitempty"`       // "explicit" | "prompt" | ...
}

// Message 消息
type Message struct {
	Role         string        `json:"role" yaml:"role"`
	Content      string        `json:"content" yaml:"content"`
	ContentParts []ContentPart `json:"content_parts,omitempty" yaml:"content_parts,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"`
	Metadata     Metadata      `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// ToolCall 工具调用
type ToolCall struct {
	ID   string                 `json:"id" yaml:"id"`
	Name string                 `json:"name" yaml:"name"`
	Args map[string]interface{} `json:"arguments" yaml:"arguments"`
}

// ToolDefinition 工具定义
type ToolDefinition struct {
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description" yaml:"description"`
	Parameters  map[string]interface{} `json:"parameters" yaml:"parameters"`
	Metadata    map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// GetToolCall 获取指定 ID 的工具调用
func (m *Message) GetToolCall(id string) (*ToolCall, bool) {
	for _, tc := range m.ToolCalls {
		if tc.ID == id {
			return &tc, true
		}
	}
	return nil, false
}

// HasToolCalls 检查是否有工具调用
func (m *Message) HasToolCalls() bool {
	return len(m.ToolCalls) > 0
}

// HasContentParts reports whether the message carries structured multimodal
// content parts.
func (m *Message) HasContentParts() bool {
	return len(m.ContentParts) > 0
}

// ImageContentParts returns all image-type content parts.
func (m *Message) ImageContentParts() []ContentPart {
	if m == nil {
		return nil
	}
	var result []ContentPart
	for _, part := range m.ContentParts {
		if part.Type == ContentPartImage {
			result = append(result, part)
		}
	}
	return result
}

// Clone 克隆消息
func (m *Message) Clone() *Message {
	if m == nil {
		return nil
	}

	clone := &Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		Metadata:   m.Metadata.Clone(),
	}

	// 克隆 ContentParts
	if len(m.ContentParts) > 0 {
		clone.ContentParts = make([]ContentPart, len(m.ContentParts))
		copy(clone.ContentParts, m.ContentParts)
	}

	// 克隆 ToolCalls
	if len(m.ToolCalls) > 0 {
		clone.ToolCalls = make([]ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			clone.ToolCalls[i] = ToolCall{
				ID:   tc.ID,
				Name: tc.Name,
			}
			// 克隆 Args
			if len(tc.Args) > 0 {
				clone.ToolCalls[i].Args = make(map[string]interface{}, len(tc.Args))
				for k, v := range tc.Args {
					clone.ToolCalls[i].Args[k] = v
				}
			}
		}
	}

	return clone
}

// WithMetadataWith 添加元数据（返回新实例）
func (m *Message) WithMetadata(key string, value interface{}) *Message {
	clone := m.Clone()
	clone.Metadata.Set(key, value)
	return clone
}

// GetInputImagesMetadata returns the raw metadata map for use with
// llm.ExtractLocalInputImages. This is a bridge accessor for the
// metadata-based image sideband until all consumers migrate to
// ContentParts.
func (m *Message) GetInputImagesMetadata() map[string]interface{} {
	if m == nil || len(m.Metadata) == 0 {
		return nil
	}
	return map[string]interface{}(m.Metadata)
}

// NewUserMessage 创建用户消息
func NewUserMessage(content string) *Message {
	return &Message{
		Role:     "user",
		Content:  content,
		Metadata: NewMetadata(),
	}
}

// NewAssistantMessage 创建助手消息
func NewAssistantMessage(content string) *Message {
	return &Message{
		Role:     "assistant",
		Content:  content,
		Metadata: NewMetadata(),
	}
}

// NewDeveloperMessage 创建开发者消息。
func NewDeveloperMessage(content string) *Message {
	return &Message{
		Role:     "developer",
		Content:  content,
		Metadata: NewMetadata(),
	}
}

// NewToolMessage 创建工具消息
func NewToolMessage(toolCallID, content string) *Message {
	return &Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: toolCallID,
		Metadata:   NewMetadata(),
	}
}

// NewSystemMessage 创建系统消息
func NewSystemMessage(content string) *Message {
	return &Message{
		Role:     "system",
		Content:  content,
		Metadata: NewMetadata(),
	}
}
