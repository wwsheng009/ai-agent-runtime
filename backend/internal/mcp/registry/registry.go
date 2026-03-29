package registry

import (
	"fmt"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
)

// ToolInfo 工具信息
type ToolInfo struct {
	Tool       *protocol.Tool
	MCPName    string
	Enabled    bool
}

// Registry MCP 工具注册表
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*ToolInfo  // key: ${mcp_name}_${tool_name}
	mcps  map[string]client.Client
}

// NewRegistry 创建注册表
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]*ToolInfo),
		mcps:  make(map[string]client.Client),
	}
}

// RegisterClient 注册 MCP 客户端
func (r *Registry) RegisterClient(name string, cli client.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.mcps[name] = cli
}

// UnregisterClient 注销 MCP 客户端
func (r *Registry) UnregisterClient(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 移除客户端
	delete(r.mcps, name)

	// 移除相关工具
	for key, info := range r.tools {
		if info.MCPName == name {
			delete(r.tools, key)
		}
	}
}

// RegisterTool 注册工具
func (r *Registry) RegisterTool(mcpName string, tool *protocol.Tool, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, tool.Name)
	r.tools[key] = &ToolInfo{
		Tool:    tool,
		MCPName: mcpName,
		Enabled: enabled,
	}
}

// UnregisterTool 注销工具
func (r *Registry) UnregisterTool(mcpName, toolName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, toolName)
	delete(r.tools, key)
}

// ListTools 列出所有工具
func (r *Registry) ListTools() []*ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]*ToolInfo, 0, len(r.tools))
	for _, info := range r.tools {
		if info.Enabled {
			tools = append(tools, info)
		}
	}
	return tools
}

// ListToolsByMCP 列出指定 MCP 的所有工具
func (r *Registry) ListToolsByMCP(mcpName string) []*ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]*ToolInfo, 0)
	for _, info := range r.tools {
		if info.MCPName == mcpName && info.Enabled {
			tools = append(tools, info)
		}
	}
	return tools
}

// GetTool 获取工具信息
func (r *Registry) GetTool(mcpName, toolName string) (*ToolInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return nil, fmt.Errorf("工具不存在: %s", toolName)
	}

	return info, nil
}

// EnableTool 启用工具
func (r *Registry) EnableTool(mcpName, toolName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return fmt.Errorf("工具不存在: %s", toolName)
	}

	info.Enabled = true
	return nil
}

// DisableTool 禁用工具
func (r *Registry) DisableTool(mcpName, toolName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return fmt.Errorf("工具不存在: %s", toolName)
	}

	info.Enabled = false
	return nil
}

// ToolEnabled 检查工具是否启用
func (r *Registry) ToolEnabled(mcpName, toolName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := r.makeToolKey(mcpName, toolName)
	info, ok := r.tools[key]
	if !ok {
		return false
	}
	return info.Enabled
}

// GetClient 获取 MCP 客户端
func (r *Registry) GetClient(mcpName string) (client.Client, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cli, ok := r.mcps[mcpName]
	if !ok {
		return nil, fmt.Errorf("MCP 客户端不存在: %s", mcpName)
	}

	return cli, nil
}

// ListClients 列出所有客户端
func (r *Registry) ListClients() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clients := make([]string, 0, len(r.mcps))
	for name := range r.mcps {
		clients = append(clients, name)
	}
	return clients
}

// Clear 清空注册表
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tools = make(map[string]*ToolInfo)
	r.mcps = make(map[string]client.Client)
}

// makeToolKey 生成工具键
func (r *Registry) makeToolKey(mcpName, toolName string) string {
	return fmt.Sprintf("%s_%s", mcpName, toolName)
}
