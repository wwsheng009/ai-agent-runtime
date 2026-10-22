//go:build !win7compat

package toolkit

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/server"
)

// ToolkitMCPServer 将 Toolkit 工具暴露为 MCP 服务
type ToolkitMCPServer struct {
	base     *server.BaseServer
	registry *Registry
	// decorate injects per-request state into the tool context. It is how a host
	// passes request-scoped facts the tool cannot see otherwise (the MCP session,
	// client roots) without teaching the toolkit about any particular host.
	decorate ToolContextDecorator
}

// ToolContextDecorator derives the context one tool call executes with.
//
// It receives the raw MCP request so a host can read client capabilities, roots or
// headers. Returning nil keeps the original context, so a decorator may decline.
type ToolContextDecorator func(ctx context.Context, req *mcp.CallToolRequest) context.Context

// SetToolContextDecorator installs the per-request context hook.
func (s *ToolkitMCPServer) SetToolContextDecorator(decorate ToolContextDecorator) {
	if s == nil {
		return
	}
	s.decorate = decorate
}

// NewToolkitMCPServer 创建 Toolkit MCP 服务器
func NewToolkitMCPServer(registry *Registry) *ToolkitMCPServer {
	base := server.NewBaseServer("toolkit-mcp", "1.0.0")
	s := &ToolkitMCPServer{
		base:     base,
		registry: registry,
	}

	s.registerTools()
	return s
}

// registerTools 注册所有 toolkit 工具到 MCP
func (s *ToolkitMCPServer) registerTools() {
	mcpServer := s.base.GetMCPServer()

	for _, tool := range s.registry.List() {
		// 为每个工具创建闭包捕获
		t := tool
		inputSchema, exists := s.registry.ParameterSchema(t.Name())
		if !exists {
			continue
		}

		// 构建 MCP Tool 定义
		mcpTool := &mcp.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: inputSchema,
		}

		// 注册到 MCP Server
		mcp.AddTool(mcpServer, mcpTool, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, any, error) {
			return s.handleToolCallWithRequest(ctx, req, t, args)
		})
	}
}

// handleToolCall 处理工具调用
func (s *ToolkitMCPServer) handleToolCall(ctx context.Context, tool Tool, args map[string]interface{}) (*mcp.CallToolResult, any, error) {
	return s.handleToolCallWithRequest(ctx, nil, tool, args)
}

// handleToolCallWithRequest runs one tool call with the request available to the
// context decorator.
func (s *ToolkitMCPServer) handleToolCallWithRequest(ctx context.Context, req *mcp.CallToolRequest, tool Tool, args map[string]interface{}) (*mcp.CallToolResult, any, error) {
	if s != nil && s.decorate != nil {
		if decorated := s.decorate(ctx, req); decorated != nil {
			ctx = decorated
		}
	}
	// 执行工具
	result, err := tool.Execute(ctx, args)
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("工具执行错误: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	return buildMCPCallToolResult(result), nil, nil
}

// buildMCPCallToolResult projects a toolkit result onto the MCP result contract.
//
// Metadata travels in CallToolResult.Meta only. It used to be appended to the
// text body as a "Metadata:" JSON block, which doubled the model-visible token
// cost, leaked internal fields (artifact ids, dedup flags) into the prompt, and
// diverged from the local formatToolkitResult* contract (review B5).
//
// Binary payloads become ImageContent; without this branch a remote `view` of a
// PNG returned empty text because only result.Content was ever read.
func buildMCPCallToolResult(result *ToolResult) *mcp.CallToolResult {
	if result == nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "工具执行失败"}},
			IsError: true,
		}
	}

	var contents []mcp.Content
	if result.Success {
		content := result.Content
		if content == "" && result.Error != nil {
			content = result.Error.Error()
		}
		if len(result.Data) > 0 {
			mimeType := strings.TrimSpace(result.MIMEType)
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			contents = append(contents, &mcp.ImageContent{
				Data:     result.Data,
				MIMEType: mimeType,
			})
		}
		if content != "" || len(contents) == 0 {
			contents = append(contents, &mcp.TextContent{Text: content})
		}
	} else {
		errMsg := "工具执行失败"
		if result.Error != nil {
			errMsg = result.Error.Error()
		}
		contents = append(contents, &mcp.TextContent{Text: errMsg})
	}

	return &mcp.CallToolResult{
		Content: contents,
		IsError: !result.Success,
		Meta:    result.MetadataWithOutputKind(),
	}
}

// Start 启动 MCP 服务器
func (s *ToolkitMCPServer) Start(ctx context.Context) error {
	return s.base.Start(ctx, &mcp.StdioTransport{})
}

// Stop 停止服务器
func (s *ToolkitMCPServer) Stop() error {
	return s.base.Stop()
}

// GetMCPServer 获取底层 MCP Server 实例
func (s *ToolkitMCPServer) GetMCPServer() *mcp.Server {
	return s.base.GetMCPServer()
}

// ListExposedTools 列出已暴露的工具
func (s *ToolkitMCPServer) ListExposedTools() []string {
	tools := s.registry.List()
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	return names
}
