package skill

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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
		Name:             mcpregistry.CallableToolName(info, a.manager.ListTools()),
		Description:      info.Tool.Description,
		InputSchema:      cloneInputSchema(info.Tool.InputSchema),
		Metadata:         cloneMeta(info.Metadata),
		MCPName:          info.MCPName,
		MaxParallelCalls: a.resolveMaxParallelCalls(info.MCPName),
		MCPTrustLevel:    a.resolveTrustLevel(info.MCPName),
		ExecutionMode:    a.resolveExecutionMode(info.MCPName),
		Enabled:          info.Enabled,
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
	mcpName = strings.TrimSpace(mcpName)
	toolName = strings.TrimSpace(toolName)

	var info *mcpregistry.ToolInfo
	if mcpName != "" {
		tools := a.manager.ListTools()
		info = findExplicitMCPTool(tools, mcpName, toolName)
		if info == nil {
			candidate, findErr := a.manager.FindTool(toolName)
			if findErr == nil && matchesExplicitMCPTool(candidate, mcpName, toolName) {
				info = candidate
			}
		}
	} else {
		var resolveErr error
		info, resolveErr = a.manager.FindTool(toolName)
		if resolveErr != nil {
			return nil, nil, resolveErr
		}
	}
	callMCPName, callToolName := mcpName, toolName
	if info != nil && info.Tool != nil {
		callMCPName = info.MCPName
		callToolName = mcpregistry.ExecutionLookupName(info, a.manager.ListTools())
	}
	result, err := a.manager.CallTool(callCtx, callMCPName, callToolName, args)
	if err != nil {
		return nil, nil, err
	}

	// 提取文本内容和 metadata
	output, metadata, extractErr := a.extractToolResult(result)
	if info != nil {
		metadata = withMCPToolIdentity(metadata, info, toolName)
	} else {
		metadata = withRequestedMCPToolIdentity(metadata, mcpName, toolName)
	}
	return output, metadata, extractErr
}

func findExplicitMCPTool(tools []*mcpregistry.ToolInfo, mcpName, toolName string) *mcpregistry.ToolInfo {
	for _, info := range tools {
		if info != nil && info.Tool != nil && info.MCPName == mcpName &&
			mcpregistry.CanonicalToolName(info.MCPName, info.Tool.Name) == toolName {
			return info
		}
	}
	for _, info := range tools {
		if info != nil && info.Tool != nil && info.MCPName == mcpName && info.Tool.Name == toolName {
			return info
		}
	}
	return nil
}

func matchesExplicitMCPTool(info *mcpregistry.ToolInfo, mcpName, toolName string) bool {
	return info != nil && info.Tool != nil && info.MCPName == mcpName &&
		(info.Tool.Name == toolName || mcpregistry.CanonicalToolName(info.MCPName, info.Tool.Name) == toolName)
}

// extractToolResult 提取文本内容和 metadata。
// Align with tools/manager formatMCPResult: IsError becomes a non-nil error so
// circuit/outcome classification and model envelopes see failures consistently.
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
	metadata := cloneMeta(result.Meta)
	if result.IsError {
		if strings.TrimSpace(output) == "" {
			return "", metadata, fmt.Errorf("tool execution failed")
		}
		return output, metadata, fmt.Errorf("%s", output)
	}
	return output, metadata, nil
}

// ListTools 列出工具
func (a *MCPAdapter) ListTools() []ToolInfo {
	tools := a.manager.ListTools()
	callableNames := mcpregistry.CallableToolNames(tools)
	result := make([]ToolInfo, len(tools))

	for i, t := range tools {
		result[i] = ToolInfo{
			Name:             callableNames[i],
			Description:      t.Tool.Description,
			InputSchema:      cloneInputSchema(t.Tool.InputSchema),
			Metadata:         withMCPToolIdentity(cloneMeta(t.Metadata), t, callableNames[i]),
			MCPName:          t.MCPName,
			MaxParallelCalls: a.resolveMaxParallelCalls(t.MCPName),
			MCPTrustLevel:    a.resolveTrustLevel(t.MCPName),
			ExecutionMode:    a.resolveExecutionMode(t.MCPName),
			Enabled:          t.Enabled,
		}
	}

	return result
}

// ResolveToolSource reports the source category for the named tool.
func (a *MCPAdapter) ResolveToolSource(toolName string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || a == nil || a.manager == nil {
		return ""
	}
	info, err := a.manager.FindTool(toolName)
	if err != nil || info == nil {
		return ""
	}
	if strings.TrimSpace(info.MCPName) != "" {
		return toolresult.SourceMCP
	}
	return ""
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

func withMCPToolIdentity(metadata map[string]interface{}, info *mcpregistry.ToolInfo, callableName string) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if info == nil || info.Tool == nil {
		return metadata
	}
	metadata["mcp_name"] = info.MCPName
	metadata["mcp_raw_tool_name"] = info.Tool.Name
	metadata["mcp_canonical_name"] = mcpregistry.CanonicalToolName(info.MCPName, info.Tool.Name)
	metadata["tool_callable_name"] = strings.TrimSpace(callableName)
	return metadata
}

func withRequestedMCPToolIdentity(metadata map[string]interface{}, mcpName, toolName string) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["mcp_name"] = mcpName
	metadata["mcp_raw_tool_name"] = toolName
	metadata["mcp_canonical_name"] = mcpregistry.CanonicalToolName(mcpName, toolName)
	metadata["tool_callable_name"] = toolName
	return metadata
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

func (a *MCPAdapter) resolveMaxParallelCalls(mcpName string) int {
	if a == nil || a.manager == nil || mcpName == "" {
		return 0
	}
	status, err := a.manager.GetMCPStatus(mcpName)
	if err != nil || status == nil {
		return 0
	}
	return status.MaxParallelCalls
}
