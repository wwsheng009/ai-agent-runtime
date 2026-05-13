package aiclitools

import (
	"context"
	"fmt"
	"strings"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type CapabilityMCPManager struct {
	Registry       *Registry
	Next           runtimeskill.MCPManager
	ContextFactory SessionContextFactory
	Path           ExposurePath
	MCPName        string
	Enabled        func() bool
}

func (m *CapabilityMCPManager) ListTools() []runtimeskill.ToolInfo {
	var tools []runtimeskill.ToolInfo
	if m != nil && m.Next != nil {
		tools = append(tools, m.Next.ListTools()...)
	}
	if m == nil || m.Registry == nil || !m.enabled() {
		return tools
	}
	for _, cap := range m.Registry.ListForPath(m.exposurePath()) {
		if containsToolInfo(tools, cap.Name) {
			continue
		}
		tools = append(tools, m.toolInfo(cap))
	}
	return tools
}

func (m *CapabilityMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	toolName = strings.TrimSpace(toolName)
	if m != nil && m.Registry != nil && m.enabled() {
		if cap, ok := m.Registry.Get(toolName); ok && cap.SupportsPath(m.exposurePath()) {
			return m.toolInfo(cap), nil
		}
	}
	if m != nil && m.Next != nil {
		return m.Next.FindTool(toolName)
	}
	return runtimeskill.ToolInfo{}, fmt.Errorf("tool '%s' not found", toolName)
}

func (m *CapabilityMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	toolName = strings.TrimSpace(toolName)
	if m != nil && m.Registry != nil && m.enabled() {
		if cap, ok := m.Registry.Get(toolName); ok && cap.SupportsPath(m.exposurePath()) {
			if cap.Execute == nil {
				return nil, fmt.Errorf("capability %q is not executable", toolName)
			}
			callCtx, ok := ctx.(context.Context)
			if !ok || callCtx == nil {
				callCtx = context.Background()
			}
			var session ToolSessionContext
			if m.ContextFactory != nil {
				session = m.ContextFactory(callCtx)
			}
			result, err := cap.Execute(callCtx, session, args)
			if err != nil {
				return nil, err
			}
			return result.Output, nil
		}
	}
	if m != nil && m.Next != nil {
		return m.Next.CallTool(ctx, mcpName, toolName, args)
	}
	return nil, fmt.Errorf("tool '%s' not found", toolName)
}

func (m *CapabilityMCPManager) toolInfo(cap Capability) runtimeskill.ToolInfo {
	return runtimeskill.ToolInfo{
		Name:        cap.Name,
		Description: cap.Description,
		InputSchema: cloneMap(cap.Parameters),
		Metadata:    cloneMap(cap.Metadata),
		MCPName:     firstNonEmpty(m.MCPName, string(m.exposurePath())),
		Enabled:     true,
	}
}

func (m *CapabilityMCPManager) exposurePath() ExposurePath {
	if m == nil || m.Path == "" {
		return ExposureActor
	}
	return m.Path
}

func (m *CapabilityMCPManager) enabled() bool {
	return m == nil || m.Enabled == nil || m.Enabled()
}

func containsToolInfo(tools []runtimeskill.ToolInfo, name string) bool {
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == name {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var _ runtimeskill.MCPManager = (*CapabilityMCPManager)(nil)
