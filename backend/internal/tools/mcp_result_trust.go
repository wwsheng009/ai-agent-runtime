package tools

import (
	"strings"

	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
)

// MCPResultContext is the locally trusted description of one MCP result.
// TrustLevel and ExecutionScope come from server configuration, never from the
// remote response itself.
type MCPResultContext struct {
	Server         string
	Tool           string
	TrustLevel     string
	ExecutionScope string
}

// Origin converts the context into the core result origin.
func (c *MCPResultContext) Origin() toolcore.ResultOrigin {
	if c == nil {
		return toolcore.ResultOrigin{}
	}
	scope := strings.ToLower(strings.TrimSpace(c.ExecutionScope))
	if scope == "" {
		scope = "remote"
	}
	trust := strings.ToLower(strings.TrimSpace(c.TrustLevel))
	if trust == "" {
		trust = "untrusted_remote"
	}
	return toolcore.ResultOrigin{
		Scope:      scope,
		Target:     strings.TrimSpace(c.Server),
		TrustLevel: trust,
	}
}

// mcpResultContext resolves trust from the MCP manager's server status. A
// missing status fails closed as an untrusted remote server.
func (m *Manager) mcpResultContext(info *mcpregistry.ToolInfo) *MCPResultContext {
	if info == nil || info.Tool == nil {
		return nil
	}
	ctx := &MCPResultContext{
		Server:         strings.TrimSpace(info.MCPName),
		Tool:           strings.TrimSpace(info.Tool.Name),
		ExecutionScope: "remote",
		TrustLevel:     "untrusted_remote",
	}
	if m != nil && m.mcp != nil {
		if status, err := m.mcp.GetMCPStatus(info.MCPName); err == nil && status != nil {
			if scope := strings.ToLower(strings.TrimSpace(string(status.ExecutionScope))); scope != "" {
				ctx.ExecutionScope = scope
			}
			if trust := strings.ToLower(strings.TrimSpace(string(status.TrustLevel))); trust != "" {
				ctx.TrustLevel = trust
			}
		}
	}
	return ctx
}

// sanitizeMCPResult applies the local trust projection to one remote result.
// Local servers are left byte-for-byte unchanged.
func sanitizeMCPResult(output string, metadata map[string]interface{}, context *MCPResultContext) (string, map[string]interface{}) {
	if context == nil {
		return output, metadata
	}
	origin := context.Origin()
	if !origin.IsRemote() {
		return output, metadata
	}
	sanitized := toolcoreoutput.SanitizeRemoteResult(origin, output, metadata, toolcoreoutput.TrustOptions{
		Tool: context.Tool,
	})
	return sanitized.Body, sanitized.Metadata
}
