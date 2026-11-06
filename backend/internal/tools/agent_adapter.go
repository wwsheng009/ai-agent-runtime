package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// AgentAdapter exposes runtime tools through the skill.MCPManager shape used by Agent.
type AgentAdapter struct {
	manager *Manager
}

// mcpAdapterStatus is the small governance projection the adapter needs from
// one MCP server. Keeping it local avoids carrying the full MCPStatus through
// every descriptor while still fetching each server only once per list call.
type mcpAdapterStatus struct {
	trustLevel       string
	executionMode    string
	executionScope   string
	maxParallelCalls int
}

// NewAgentAdapter wraps a runtime tools manager for the agent loop.
func NewAgentAdapter(manager *Manager) *AgentAdapter {
	return &AgentAdapter{manager: manager}
}

// UnderlyingToolsManager exposes the wrapped tools manager for host-side
// wiring that is built after the tool surface (for example the runtime-server
// lazily-created background manager injecting the shell job runtime). Returns
// nil when the adapter has no manager.
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
	statusByMCP := make(map[string]mcpAdapterStatus)
	for _, descriptor := range descriptors {
		info := skill.ToolInfo{
			Name:        descriptor.Name,
			Description: descriptor.Description,
			InputSchema: normalizeParameters(descriptor.Parameters),
			Metadata:    cloneMetadataMap(descriptor.Metadata),
			Enabled:     true,
		}
		// The manager has already resolved the callable MCP identity and put it
		// into descriptor.Metadata. Reusing that projection avoids a per-tool
		// FindTool scan and another JSON deep copy of the schema; the adapter only
		// needs server governance fields plus the fs_owner stamp (B1/B2).
		if mcpName, ok := descriptorMCPName(descriptor.Metadata); ok && a.manager.mcp != nil && !a.manager.shouldPreferLocalToolkit(mcpName, descriptor.Name) {
			status := a.mcpStatusFor(mcpName, statusByMCP)
			info.MCPName = mcpName
			info.MaxParallelCalls = status.maxParallelCalls
			info.MCPTrustLevel = status.trustLevel
			info.ExecutionMode = status.executionMode
			info.ExecutionScope = status.executionScope
			info.Metadata = skill.WithRuntimeMCPMetadata(info.Metadata)
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
	canonicalName := canonicalManagedToolName(toolName)
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

func descriptorMCPName(metadata map[string]interface{}) (string, bool) {
	raw, ok := metadata["mcp_name"]
	if !ok {
		return "", false
	}
	name, ok := raw.(string)
	if !ok {
		return "", false
	}
	name = strings.TrimSpace(name)
	return name, name != ""
}

// mcpStatusFor fetches one MCP server's governance projection at most once per
// ListTools call.
//
// The trust level, execution mode and execution scope are what
// ToolExecutionPolicy gates remote writes/shell on
// (policy/tool_policy.go AllowToolInfo), so dropping them here silently
// disabled BlockUntrustedMCP / BlockRemoteWrites for every host that projects
// tools through this adapter (aicli TUI and runtime-server; review B1).
func (a *AgentAdapter) mcpStatusFor(mcpName string, cache map[string]mcpAdapterStatus) mcpAdapterStatus {
	if a == nil || a.manager == nil || a.manager.mcp == nil || strings.TrimSpace(mcpName) == "" {
		return mcpAdapterStatus{}
	}
	name := strings.TrimSpace(mcpName)
	if cache != nil {
		if status, ok := cache[name]; ok {
			return status
		}
	}
	networkStatus, err := a.manager.mcp.GetMCPStatus(name)
	status := mcpAdapterStatus{}
	if err == nil && networkStatus != nil {
		status = mcpAdapterStatus{
			trustLevel:       string(networkStatus.TrustLevel),
			executionMode:    networkStatus.ExecutionMode,
			executionScope:   string(networkStatus.ExecutionScope),
			maxParallelCalls: networkStatus.MaxParallelCalls,
		}
	}
	if cache != nil {
		cache[name] = status
	}
	return status
}
