package toolkit

import (
	"context"

	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/protocol"
)

// MCPAdapter 将 Tool 适配为 MCP Tool 格式
type MCPAdapter struct {
	tool Tool
}

// NewMCPAdapter 创建 MCP 适配器
func NewMCPAdapter(tool Tool) MCPAdapter {
	return MCPAdapter{tool: tool}
}

// ToMCPTool 转换为 MCP Tool 格式
func (a *MCPAdapter) ToMCPTool() *protocol.Tool {
	return &protocol.Tool{
		Name:        a.tool.Name(),
		Description: a.tool.Description(),
		InputSchema: a.tool.Parameters(),
	}
}

// ExecuteAsMCP 转换 MCP 调用结果为 MCP 格式
func (a *MCPAdapter) ExecuteAsMCP(ctx context.Context, args map[string]interface{}) (*protocol.CallToolResult, error) {
	result, err := a.tool.Execute(ctx, args)
	if err != nil {
		return nil, err
	}

	// 转换为 MCP 格式
	content := protocol.Content{
		Type: "text",
		Text: result.Content,
	}

	if result.Data != nil {
		content.Type = "image"
		content.Data = string(result.Data)
		content.MIMEType = result.MIMEType
	}

	return &protocol.CallToolResult{
		Content: []protocol.Content{content},
		Meta:    result.Metadata,
	}, nil
}

// RegistryToMCPTools 将工具注册表转换为 MCP 工具列表
func RegistryToMCPTools(registry *Registry) []*protocol.Tool {
	tools := registry.List()
	mcpTools := make([]*protocol.Tool, 0, len(tools))

	for _, tool := range tools {
		adapter := NewMCPAdapter(tool)
		mcpTools = append(mcpTools, adapter.ToMCPTool())
	}

	return mcpTools
}
