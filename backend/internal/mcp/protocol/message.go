package protocol

// JSON-RPC 版本
const (
	JSONRPCVersion = "2.0"
)

// Request JSON-RPC 请求
type Request struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id"`
	Method  string        `json:"method"`
	Params  interface{}   `json:"params,omitempty"`
}

// Response JSON-RPC 响应
type Response struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *Error        `json:"error,omitempty"`
}

// Error JSON-RPC 错误
type Error struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Notification JSON-RPC 通知（无需响应）
type Notification struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  interface{}   `json:"params,omitempty"`
}

// MCP 方法常量
const (
	// 工具方法
	MethodListTools      = "tools/list"
	MethodCallTool       = "tools/call"

	// 资源方法
	MethodListResources  = "resources/list"
	MethodReadResource   = "resources/read"

	// 提示方法
	MethodListPrompts    = "prompts/list"
	MethodGetPrompt      = "prompts/get"

	// 初始化
	MethodInitialize     = "initialize"
	MethodInitialized    = "notifications/initialized"
)

// InitializeParams 初始化参数
type InitializeParams struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo        `json:"clientInfo"`
}

// ClientCapabilities 客户端能力
type ClientCapabilities struct {
	Experimental map[string]interface{} `json:"experimental,omitempty"`
}

// ClientInfo 客户端信息
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult 初始化结果
type InitializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo        `json:"serverInfo"`
}

// ServerCapabilities 服务器能力
type ServerCapabilities struct {
	Experimental   map[string]interface{} `json:"experimental,omitempty"`
	Resources      *struct{}               `json:"resources,omitempty"`
	Tools          *struct{}               `json:"tools,omitempty"`
	Prompts        *struct{}               `json:"prompts,omitempty"`
}

// ServerInfo 服务器信息
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ListToolsParams 列出工具参数
type ListToolsParams struct {
	Cursor *string `json:"cursor,omitempty"`
}

// ListToolsResult 列出工具结果
type ListToolsResult struct {
	Tools      []*Tool            `json:"tools"`
	NextCursor *string             `json:"nextCursor,omitempty"`
	Meta       map[string]any      `json:"meta,omitempty"`
}

// CallToolParams 调用工具参数
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// CallToolResult 调用工具结果
type CallToolResult struct {
	Content []Content          `json:"content"`
	IsError bool               `json:"isError,omitempty"`
	Meta    map[string]any     `json:"meta,omitempty"`
}

// ListResourcesParams 列出资源参数
type ListResourcesParams struct {
	Cursor *string `json:"cursor,omitempty"`
}

// ListResourcesResult 列出资源结果
type ListResourcesResult struct {
	Resources  []*Resource        `json:"resources"`
	NextCursor *string             `json:"nextCursor,omitempty"`
}

// ReadResourceParams 读取资源参数
type ReadResourceParams struct {
	URI string `json:"uri"`
}

// ReadResourceResult 读取资源结果
type ReadResourceResult struct {
	Contents []ResourceContents   `json:"contents"`
}

// Content 内容（工具结果或资源内容）
type Content struct {
	Type       string                 `json:"type"` // text | image | resource
	Text       string                 `json:"text,omitempty"`
	Data       string                 `json:"data,omitempty"`
	MIMEType   string                 `json:"mimeType,omitempty"`
	URI        string                 `json:"uri,omitempty"`
	Annotation map[string]interface{} `json:"annotation,omitempty"`
}

// ResourceContents 资源内容
type ResourceContents struct {
	URI      string                 `json:"uri"`
	MIMEType string                 `json:"mimeType,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Blob     string                 `json:"blob,omitempty"`
	Contents []Content              `json:"contents,omitempty"`
}
