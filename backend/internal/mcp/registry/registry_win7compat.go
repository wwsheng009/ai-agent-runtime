//go:build win7compat

package registry

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
)

// 本文件是 Windows 7 兼容构建（Go 1.20 + win7compat tag）下的 MCP 注册表
// stub，与 manager_win7compat.go 配套。保留类型与纯函数逻辑（不依赖
// go-sdk），Registry 本体降级为空壳。注意：CanonicalToolName 的简化实现
// 未做 64 字符截断与哈希后缀（原实现见 registry.go），该路径在 Win7
// 兼容构建中不可达（MCP 始终为空），行为差异无实际影响。

const maxCanonicalToolNameLength = 64

// ToolInfo 工具信息
type ToolInfo struct {
	Tool     *protocol.Tool
	MCPName  string
	Enabled  bool
	Metadata map[string]interface{}
}

// QuarantinedToolInfo records an externally supplied tool that failed schema
// validation. It is retained for diagnostics but never exposed for execution.
type QuarantinedToolInfo struct {
	MCPName    string
	ToolName   string
	SchemaHash string
	Error      string
}

// AmbiguousToolError is returned when a short MCP tool name maps to more than
// one enabled server. Callers must choose a canonical name instead.
type AmbiguousToolError struct {
	Name       string
	Candidates []string
}

func (e *AmbiguousToolError) Error() string {
	if e == nil {
		return "ambiguous MCP tool"
	}
	return fmt.Sprintf("ambiguous MCP tool %q; use one of: %s", e.Name, strings.Join(e.Candidates, ", "))
}

// IsAmbiguousToolError reports whether err is the fail-closed short-name error.
func IsAmbiguousToolError(err error) bool {
	var target *AmbiguousToolError
	return errors.As(err, &target)
}

// Registry MCP 工具注册表（Win7 兼容构建空壳）。
type Registry struct{}

// NewRegistry 创建注册表。
func NewRegistry() *Registry { return &Registry{} }

// CanonicalToolName returns the provider-safe identity for an MCP tool.
// 简化实现：不做长名截断（见文件头注释）。
func CanonicalToolName(mcpName, toolName string) string {
	return "mcp__" + mcpName + "__" + toolName
}

// CallableToolNames returns names aligned with tools.
// 简化实现：直接使用原始工具名（见文件头注释）。
func CallableToolNames(tools []*ToolInfo) []string {
	names := make([]string, len(tools))
	for index, info := range tools {
		if info == nil || info.Tool == nil {
			continue
		}
		names[index] = info.Tool.Name
	}
	return names
}

// CallableToolName returns one tool's projected name for the given inventory.
func CallableToolName(info *ToolInfo, tools []*ToolInfo) string {
	if info == nil || info.Tool == nil {
		return ""
	}
	return info.Tool.Name
}

// ExecutionLookupName returns the name adapters should pass to a Manager that
// resolves both raw and canonical identities. 简化实现：直接返回原始工具名。
func ExecutionLookupName(info *ToolInfo, tools []*ToolInfo) string {
	if info == nil || info.Tool == nil {
		return ""
	}
	return strings.TrimSpace(info.Tool.Name)
}