package toolkit

import (
	"context"
	"fmt"
	"sync"
)

// Registry 工具注册表
type Registry struct {
	tools map[string]Tool
	mu    sync.RWMutex
}

// NewRegistry 创建新的注册表
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register 注册工具
func (r *Registry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, exists := r.tools[tool.Name()]; exists {
		return fmt.Errorf("tool '%s' already registered (existing: %s)", tool.Name(), existing.Name())
	}

	r.tools[tool.Name()] = tool
	return nil
}

// Unregister 注销工具
func (r *Registry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; !exists {
		return fmt.Errorf("tool '%s' not found", name)
	}

	delete(r.tools, name)
	return nil
}

// Get 获取工具
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	return tool, exists
}

// List 列出所有工具
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// Execute 执行工具
func (r *Registry) Execute(ctx context.Context, name string, params map[string]interface{}) (*ToolResult, error) {
	tool, exists := r.Get(name)
	if !exists {
		return nil, fmt.Errorf("tool '%s' not found", name)
	}

	// 优化：如果工具支持直接调用，直接执行
	if tool.CanDirectCall() {
		return tool.Execute(ctx, params)
	}

	// 否则通过 MCP 执行（如果有 MCP 适配器）
	return tool.Execute(ctx, params)
}

// GetToolSchemas 获取所有工具的 Schema（用于发送给 AI）
func (r *Registry) GetToolSchemas() []map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	schemas := make([]map[string]interface{}, 0, len(r.tools))
	for _, tool := range r.tools {
		schema := map[string]interface{}{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  tool.Parameters(),
		}
		schemas = append(schemas, schema)
	}
	return schemas
}

// FilterByPrefix 根据前缀过滤工具
func (r *Registry) FilterByPrefix(prefix string) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	filtered := make([]Tool, 0)
	for _, tool := range r.tools {
		if len(prefix) == 0 || len(tool.Name()) >= len(prefix) &&
			tool.Name()[:len(prefix)] == prefix {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
