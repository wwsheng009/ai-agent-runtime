package skill

import (
	"context"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
)

// MCPAdapter MCP 适配器
type MCPAdapter struct {
	manager manager.Manager
}

// NewMCPAdapter 创建适配器
func NewMCPAdapter(m manager.Manager) *MCPAdapter {
	return &MCPAdapter{manager: m}
}

// FindTool 查找工具
func (a *MCPAdapter) FindTool(toolName string) (ToolInfo, error) {
	info, err := a.manager.FindTool(toolName)
	if err != nil {
		return ToolInfo{}, err
	}

	return ToolInfo{
		Name:          info.Tool.Name,
		Description:   info.Tool.Description,
		InputSchema:   cloneInputSchema(info.Tool.InputSchema),
		MCPName:       info.MCPName,
		MCPTrustLevel: a.resolveTrustLevel(info.MCPName),
		ExecutionMode: a.resolveExecutionMode(info.MCPName),
		Enabled:       info.Enabled,
	}, nil
}

// CallTool 调用工具
func (a *MCPAdapter) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	output, _, err := a.CallToolWithMeta(ctx, mcpName, toolName, args)
	return output, err
}

// CallToolWithMeta 调用工具并保留结构化 metadata。
func (a *MCPAdapter) CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	callCtx, ok := ctx.(context.Context)
	if !ok || callCtx == nil {
		callCtx = context.Background()
	}

	// 使用 toolName 作为参数调用（mcpName 可以忽略或用于路由）
	result, err := a.manager.CallTool(callCtx, mcpName, toolName, args)
	if err != nil {
		return nil, nil, err
	}

	// 提取文本内容和 metadata
	return a.extractToolResult(result)
}

// extractToolResult 提取文本内容和 metadata。
func (a *MCPAdapter) extractToolResult(result *protocol.CallToolResult) (string, map[string]interface{}, error) {
	if result == nil {
		return "", nil, nil
	}

	var output string
	for _, content := range result.Content {
		if content.Type == "text" {
			output += content.Text
		}
	}

	return output, cloneMeta(result.Meta), nil
}

// ListTools 列出工具
func (a *MCPAdapter) ListTools() []ToolInfo {
	tools := a.manager.ListTools()
	result := make([]ToolInfo, len(tools))

	for i, t := range tools {
		result[i] = ToolInfo{
			Name:          t.Tool.Name,
			Description:   t.Tool.Description,
			InputSchema:   cloneInputSchema(t.Tool.InputSchema),
			MCPName:       t.MCPName,
			MCPTrustLevel: a.resolveTrustLevel(t.MCPName),
			ExecutionMode: a.resolveExecutionMode(t.MCPName),
			Enabled:       t.Enabled,
		}
	}

	return result
}

func cloneInputSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	clone := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		clone[key] = value
	}
	return clone
}

func cloneMeta(meta map[string]any) map[string]interface{} {
	if len(meta) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(meta))
	for key, value := range meta {
		cloned[key] = value
	}
	return cloned
}

// GetManager 获取底层 Manager
func (a *MCPAdapter) GetManager() manager.Manager {
	return a.manager
}

func (a *MCPAdapter) resolveTrustLevel(mcpName string) string {
	if a == nil || a.manager == nil || mcpName == "" {
		return ""
	}
	status, err := a.manager.GetMCPStatus(mcpName)
	if err != nil || status == nil {
		return ""
	}
	return string(status.TrustLevel)
}

func (a *MCPAdapter) resolveExecutionMode(mcpName string) string {
	if a == nil || a.manager == nil || mcpName == "" {
		return ""
	}
	status, err := a.manager.GetMCPStatus(mcpName)
	if err != nil || status == nil {
		return ""
	}
	return status.ExecutionMode
}
