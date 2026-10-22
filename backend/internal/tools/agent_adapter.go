package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
)

// AgentAdapter exposes runtime tools through the skill.MCPManager shape used by Agent.
type AgentAdapter struct {
	manager *Manager
}

// NewAgentAdapter wraps a runtime tools manager for the agent loop.
func NewAgentAdapter(manager *Manager) *AgentAdapter {
	return &AgentAdapter{manager: manager}
}

// UnderlyingToolsManager exposes the wrapped tools manager for host-side
// wiring seams that are built after the tool surface (e.g. the runtime-server
// lazily-created background manager injecting the detach→managed-job submitter,
// 2026-10-11). Returns nil when the adapter has no manager.
func (a *AgentAdapter) UnderlyingToolsManager() *Manager {
	if a == nil {
		return nil
	}
	return a.manager
}

// SetLSPObserver 把 LSP 池事件观察者转发到底层工具 Manager（runtime-server
// 观测接线用；未启用 LSP 或未挂池时是安全 no-op）。
func (a *AgentAdapter) SetLSPObserver(observer lsp.Observer) {
	if a == nil || a.manager == nil {
		return
	}
	a.manager.SetLSPObserver(observer)
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
			Metadata:    cloneMetadataMap(descriptor.Metadata),
			Enabled:     true,
		}
		if mcpInfo, ok := a.lookupMCPTool(descriptor.Name); ok {
			info.MCPName = mcpInfo.MCPName
			info.MaxParallelCalls = mcpInfo.MaxParallelCalls
			info.MCPTrustLevel = mcpInfo.MCPTrustLevel
			info.ExecutionMode = mcpInfo.ExecutionMode
			info.ExecutionScope = mcpInfo.ExecutionScope
			// lookupMCPTool stamps fs_owner=tool_server (B2); the descriptor's
			// metadata comes from the raw MCP registry and lacks it. Merge the
			// stamped copy over the descriptor so the owner is present without
			// dropping the callable-name identity fields ListTools already set.
			info.Metadata = mergeMetadataMaps(info.Metadata, mcpInfo.Metadata)
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
	canonicalName := toolnames.CanonicalOpenAIImageGenerateToolName(toolName)
	if canonicalName == "" {
		canonicalName = toolName
	}
	for _, info := range a.ListTools() {
		if info.Name == canonicalName {
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
	output, _, err := a.manager.ExecuteWithMeta(callCtx, toolName, args)
	return output, err
}

// CallToolWithMeta delegates execution while preserving runtime metadata such as output_kind.
func (a *AgentAdapter) CallToolWithMeta(ctx interface{}, _ string, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if a == nil || a.manager == nil {
		return nil, nil, fmt.Errorf("runtime tool manager is not configured")
	}
	callCtx, ok := ctx.(context.Context)
	if !ok || callCtx == nil {
		callCtx = context.Background()
	}
	return a.manager.ExecuteWithMeta(callCtx, toolName, args)
}

// ResolveToolSource reports the runtime source category for the named tool.
func (a *AgentAdapter) ResolveToolSource(toolName string) string {
	if a == nil || a.manager == nil {
		return ""
	}
	return a.manager.resolveToolSource(toolName)
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
	// The MCP tool server resolves its own filesystem paths: stamp the owner so
	// the local preflight stops stat'ing remote paths and the sandbox stops
	// validating them against the local workspace (review B2).
	metadata := skill.WithRuntimeMCPMetadata(cloneMetadataMap(info.Metadata))
	trustLevel, executionMode, executionScope := a.resolveMCPGovernance(info.MCPName)
	return skill.ToolInfo{
		Name:             info.Tool.Name,
		Description:      info.Tool.Description,
		InputSchema:      normalizeParameters(info.Tool.InputSchema),
		Metadata:         metadata,
		MCPName:          info.MCPName,
		MaxParallelCalls: a.resolveMCPMaxParallelCalls(info.MCPName),
		MCPTrustLevel:    trustLevel,
		ExecutionMode:    executionMode,
		ExecutionScope:   executionScope,
		Enabled:          info.Enabled,
	}, true
}

// resolveMCPGovernance returns the trust level, execution mode and execution
// scope recorded for one MCP server.
//
// These three values are what ToolExecutionPolicy gates remote writes/shell on
// (policy/tool_policy.go AllowToolInfo), so dropping them here silently
// disabled BlockUntrustedMCP / BlockRemoteWrites for every host that projects
// tools through this adapter (aicli TUI and runtime-server; review B1).
func (a *AgentAdapter) resolveMCPGovernance(mcpName string) (trustLevel, executionMode, executionScope string) {
	if a == nil || a.manager == nil || a.manager.mcp == nil || strings.TrimSpace(mcpName) == "" {
		return "", "", ""
	}
	status, err := a.manager.mcp.GetMCPStatus(mcpName)
	if err != nil || status == nil {
		return "", "", ""
	}
	return string(status.TrustLevel), status.ExecutionMode, string(status.ExecutionScope)
}

func (a *AgentAdapter) resolveMCPMaxParallelCalls(mcpName string) int {
	if a == nil || a.manager == nil || a.manager.mcp == nil || strings.TrimSpace(mcpName) == "" {
		return 0
	}
	status, err := a.manager.mcp.GetMCPStatus(mcpName)
	if err != nil || status == nil {
		return 0
	}
	return status.MaxParallelCalls
}
