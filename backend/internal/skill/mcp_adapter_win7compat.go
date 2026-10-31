//go:build win7compat

package skill

import (
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// 本文件是 Windows 7 兼容构建（Go 1.21.4 + win7compat tag）下的 MCP 适配器
// stub。MCP 在 Win7 兼容构建中整体禁用，MCPAdapter 保持与原实现相同的
// 导出面（FindTool/CallTool/CallToolWithMeta/ListTools/ResolveToolSource/
// GetManager/WithRuntimeMCPMetadata），全部返回空或"不支持"错误，保证调用
// 方（skills 集成、chat 工具桥、tools manager 适配器）无需改动即可编译。

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
	return ToolInfo{}, fmt.Errorf("MCP is not supported in the Windows 7 compatible build")
}

// CallTool 调用工具
func (a *MCPAdapter) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("MCP is not supported in the Windows 7 compatible build")
}

// CallToolWithMeta 调用工具并保留结构化 metadata。
func (a *MCPAdapter) CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	return nil, nil, fmt.Errorf("MCP is not supported in the Windows 7 compatible build")
}

// ListTools 列出工具
func (a *MCPAdapter) ListTools() []ToolInfo {
	return nil
}

// ResolveToolSource reports the source category for the named tool.
func (a *MCPAdapter) ResolveToolSource(toolName string) string {
	return ""
}

// GetManager 获取底层 Manager
func (a *MCPAdapter) GetManager() manager.Manager {
	return a.manager
}

// WithRuntimeMCPMetadata is the win7compat counterpart of the standard build's
// exported stamp. MCP is disabled in this build, so no tool server can own
// filesystem paths and the stamp is inert; keeping the same body keeps the
// tools manager adapter compiling unchanged and the metadata shape identical.
func WithRuntimeMCPMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata[types.ToolMetadataFSOwnerKey] = types.ToolFSOwnerToolServer
	return metadata
}
