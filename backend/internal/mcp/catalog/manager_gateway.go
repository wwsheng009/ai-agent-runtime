package catalog

import (
	mcpmanager "github.com/ai-gateway/ai-agent-runtime/internal/mcp/manager"
	"github.com/ai-gateway/ai-agent-runtime/internal/skill"
)

type managerToolSource struct {
	manager mcpmanager.Manager
}

func (s *managerToolSource) ListTools() []skill.ToolInfo {
	if s == nil || s.manager == nil {
		return nil
	}
	tools := s.manager.ListTools()
	out := make([]skill.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool == nil || tool.Tool == nil {
			continue
		}
		out = append(out, skill.ToolInfo{
			Name:        tool.Tool.Name,
			Description: tool.Tool.Description,
			InputSchema: cloneSchema(tool.Tool.InputSchema),
			MCPName:     tool.MCPName,
			Enabled:     tool.Enabled,
		})
		if status, err := s.manager.GetMCPStatus(tool.MCPName); err == nil && status != nil {
			out[len(out)-1].MCPTrustLevel = string(status.TrustLevel)
			out[len(out)-1].ExecutionMode = status.ExecutionMode
		}
	}
	return out
}

// NewManagerGateway 使用 MCP manager 创建一个可刷新的 catalog gateway。
func NewManagerGateway(manager mcpmanager.Manager) *Gateway {
	return NewGateway(&managerToolSource{manager: manager}, nil)
}

// NewManagerGatewayWithStore 使用 MCP manager 创建一个带可选 snapshot store 的 catalog gateway。
func NewManagerGatewayWithStore(manager mcpmanager.Manager, store SnapshotStore) *Gateway {
	return NewGatewayWithStore(&managerToolSource{manager: manager}, store)
}

func cloneSchema(schema map[string]interface{}) map[string]interface{} {
	if len(schema) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		cloned[key] = value
	}
	return cloned
}
