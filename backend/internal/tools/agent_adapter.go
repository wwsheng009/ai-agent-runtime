package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
)

// AgentAdapter exposes runtime tools through the skill.MCPManager shape used by Agent.
type AgentAdapter struct {
	manager *Manager
}

// NewAgentAdapter wraps a runtime tools manager for the agent loop.
func NewAgentAdapter(manager *Manager) *AgentAdapter {
	return &AgentAdapter{manager: manager}
}

// ListTools returns runtime tools in agent-compatible metadata form.
func (a *AgentAdapter) ListTools() []skill.ToolInfo {
	if a == nil || a.manager == nil {
		return nil
	}
	descriptors := a.manager.ListTools()
	tools := make([]skill.ToolInfo, 0, len(descriptors))
	for _, descriptor := range descriptors {
		info := skill.ToolInfo{
			Name:        descriptor.Name,
			Description: descriptor.Description,
			InputSchema: normalizeParameters(descriptor.Parameters),
			Enabled:     true,
		}
		if mcpInfo, ok := a.lookupMCPTool(descriptor.Name); ok {
			info.MCPName = mcpInfo.MCPName
			info.MCPTrustLevel = mcpInfo.MCPTrustLevel
			info.ExecutionMode = mcpInfo.ExecutionMode
		}
		tools = append(tools, info)
	}
	return tools
}

// FindTool resolves a tool descriptor by name.
func (a *AgentAdapter) FindTool(toolName string) (skill.ToolInfo, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return skill.ToolInfo{}, fmt.Errorf("tool name is required")
	}
	for _, info := range a.ListTools() {
		if info.Name == toolName {
			return info, nil
		}
	}
	return skill.ToolInfo{}, fmt.Errorf("tool '%s' not found", toolName)
}

// CallTool delegates execution to the runtime tools manager.
func (a *AgentAdapter) CallTool(ctx interface{}, _ string, toolName string, args map[string]interface{}) (interface{}, error) {
	if a == nil || a.manager == nil {
		return nil, fmt.Errorf("runtime tool manager is not configured")
	}
	callCtx, ok := ctx.(context.Context)
	if !ok || callCtx == nil {
		callCtx = context.Background()
	}
	return a.manager.Execute(callCtx, toolName, args)
}

func (a *AgentAdapter) lookupMCPTool(toolName string) (skill.ToolInfo, bool) {
	if a == nil || a.manager == nil || a.manager.mcp == nil {
		return skill.ToolInfo{}, false
	}
	info, err := a.manager.mcp.FindTool(toolName)
	if err != nil || info == nil || info.Tool == nil {
		return skill.ToolInfo{}, false
	}
	if a.manager.shouldPreferLocalToolkit(info.MCPName, toolName) {
		return skill.ToolInfo{}, false
	}
	return skill.ToolInfo{
		Name:        info.Tool.Name,
		Description: info.Tool.Description,
		InputSchema: normalizeParameters(info.Tool.InputSchema),
		MCPName:     info.MCPName,
		Enabled:     info.Enabled,
	}, true
}
