package types

// Message 消息
type Message struct {
	Role       string       `json:"role" yaml:"role"`
	Content    string       `json:"content" yaml:"content"`
	ToolCalls  []ToolCall   `json:"tool_calls,omitempty" yaml:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty" yaml:"tool_call_id,omitempty"`
	Metadata   Metadata     `json:"metadata,omitempty" yaml:"metadata,omitempty"`
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

// NewUserMessage 创建用户消息
func NewUserMessage(content string) *Message {
	return &Message{
		Role:    "user",
		Content: content,
		Metadata: NewMetadata(),
	}
}

// NewAssistantMessage 创建助手消息
func NewAssistantMessage(content string) *Message {
	return &Message{
		Role:    "assistant",
		Content: content,
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
		Role:    "system",
		Content: content,
		Metadata: NewMetadata(),
	}
}
