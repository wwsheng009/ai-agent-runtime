package toolkit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolkitMCPServer 将 Toolkit 工具暴露为 MCP 服务
type ToolkitMCPServer struct {
	base     *server.BaseServer
	registry *Registry
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

		// 构建 MCP Tool 定义
		mcpTool := &mcp.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.Parameters(),
		}

		// 注册到 MCP Server
		mcp.AddTool(mcpServer, mcpTool, func(ctx context.Context, req *mcp.CallToolRequest, args map[string]interface{}) (*mcp.CallToolResult, any, error) {
			return s.handleToolCall(ctx, t, args)
		})
	}
}

// handleToolCall 处理工具调用
func (s *ToolkitMCPServer) handleToolCall(ctx context.Context, tool Tool, args map[string]interface{}) (*mcp.CallToolResult, any, error) {
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

	// 构建返回内容
	var contents []mcp.Content

	if result.Success {
		// 成功结果
		content := result.Content
		if content == "" && result.Error != nil {
			content = result.Error.Error()
		}

		// 如果有 metadata，附加到内容
		if len(result.Metadata) > 0 {
			metadataJSON, _ := json.MarshalIndent(result.Metadata, "", "  ")
			content += fmt.Sprintf("\n\nMetadata:\n%s", string(metadataJSON))
		}

		contents = append(contents, &mcp.TextContent{Text: content})
	} else {
		// 错误结果
		errMsg := "工具执行失败"
		if result.Error != nil {
			errMsg = result.Error.Error()
		}
		contents = append(contents, &mcp.TextContent{Text: errMsg})
	}

	return &mcp.CallToolResult{
		Content: contents,
		IsError: !result.Success,
		Meta:    result.Metadata,
	}, nil, nil
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
