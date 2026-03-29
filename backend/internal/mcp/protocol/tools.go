package protocol

// Tool MCP 工具定义
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// Resource MCP 资源定义
type Resource struct {
	URI         string                 `json:"uri"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	MIMEType    string                 `json:"mimeType,omitempty"`
	Annotations *ResourceAnnotations    `json:"annotations,omitempty"`
}

// ResourceAnnotations 资源注解
type ResourceAnnotations struct {
	// 资源类型标识符
	Audience []string `json:"audience,omitempty"`
	// 优先级
	Priority *float64 `json:"priority,omitempty"`
}

// Prompt MCP 提示定义
type Prompt struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Arguments   []PromptArgument  `json:"arguments,omitempty"`
}

// PromptArgument 提示参数
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}
