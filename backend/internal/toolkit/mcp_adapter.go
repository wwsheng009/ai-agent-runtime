package toolkit

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
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
	tool := &protocol.Tool{
		Name:        a.tool.Name(),
		Description: a.tool.Description(),
		InputSchema: a.tool.Parameters(),
	}
	if provider, ok := a.tool.(ToolDefinitionMetadataProvider); ok {
		if metadata := provider.DefinitionMetadata(); len(metadata) > 0 {
			tool.Metadata = cloneMetadataMap(metadata)
		}
	}
	return tool
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
		Meta:    result.MetadataWithOutputKind(),
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

func cloneMetadataMap(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
