//go:build win7compat

package manager

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// 本文件是 Windows 7 兼容构建（Go 1.20 + win7compat tag）下的 MCP 管理器
// stub。internal/mcp/client、transport、server 依赖
// github.com/modelcontextprotocol/go-sdk（其所有版本要求 go >= 1.23），
// 无法在 Go 1.20 下编译，因此 Win7 兼容构建整体禁用 MCP：
//
//  1. Manager 接口、生命周期类型保持与 manager.go 完全一致的形状，
//     调用方（aicli chat 工具注册等）无需任何改动；
//  2. 所有方法空实现或返回"不支持"错误，NewManager 返回禁用实现；
//  3. 主线（go 1.24，无 win7compat tag）构建不受影响——本文件被排除，
//     真实 manager.go 正常编译。

var errMCPDisabled = errors.New("MCP is not supported in the Windows 7 compatible build")

var statusOutput io.Writer = os.Stdout

// SetStatusOutput 设置管理器状态输出目标。传入 nil 表示静默。
func SetStatusOutput(w io.Writer) {
	statusOutput = w
}

// Manager MCP 管理器接口（与 manager.go 保持一致，供调用方编译）。
type Manager interface {
	// LoadConfig 加载配置
	LoadConfig(configPath string) error

	// Start 启动所有启用的 MCPs
	Start(ctx context.Context) error

	// Stop 停止所有 MCPs
	Stop() error

	// ListTools 列出所有工具
	ListTools() []*registry.ToolInfo

	// CallTool 调用工具
	CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error)

	// FindTool 查找工具（通过工具名称）
	FindTool(toolName string) (*registry.ToolInfo, error)

	// ListResources 列出资源
	ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error)

	// SetMCPEnabled 启用/禁用 MCP
	SetMCPEnabled(name string, enabled bool) error

	// GetMCPStatus 获取 MCP 状态
	GetMCPStatus(name string) (*config.MCPStatus, error)

	// ListMCPs 列出所有 MCP
	ListMCPs() []*config.MCPStatus

	// ReloadConfig 重新加载配置
	ReloadConfig() error
}

// LifecycleEvent 描述 MCP manager 的生命周期事件。
type LifecycleEvent struct {
	Type      string
	TraceID   string
	MCPName   string
	Payload   map[string]interface{}
	Timestamp time.Time
}

// LifecycleObserver 订阅 MCP manager 生命周期事件。
type LifecycleObserver func(LifecycleEvent)

// ObservableManager 暴露可选的生命周期事件能力。
type ObservableManager interface {
	AddLifecycleObserver(LifecycleObserver)
}

// QuarantineReporter exposes invalid MCP definitions without adding them to
// the callable tool inventory.
type QuarantineReporter interface {
	ListQuarantinedTools() []registry.QuarantinedToolInfo
}

// disabledManager 是 Win7 兼容构建中的空实现：不加载、不启动任何 MCP。
type disabledManager struct{}

func (disabledManager) LoadConfig(configPath string) error { return nil }

func (disabledManager) Start(ctx context.Context) error { return nil }

func (disabledManager) Stop() error { return nil }

func (disabledManager) ListTools() []*registry.ToolInfo { return nil }

func (disabledManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, errMCPDisabled
}

func (disabledManager) FindTool(toolName string) (*registry.ToolInfo, error) {
	return nil, nil
}

func (disabledManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	return nil, errMCPDisabled
}

func (disabledManager) SetMCPEnabled(name string, enabled bool) error {
	return errMCPDisabled
}

func (disabledManager) GetMCPStatus(name string) (*config.MCPStatus, error) {
	return nil, errMCPDisabled
}

func (disabledManager) ListMCPs() []*config.MCPStatus { return nil }

func (disabledManager) ReloadConfig() error { return errMCPDisabled }

// NewManager 创建管理器（Win7 兼容构建返回禁用实现）。
func NewManager() Manager { return disabledManager{} }

// WithTraceID 将 trace_id 绑定到 manager 上下文，便于生命周期事件透传。
// 禁用实现直接透传原上下文。
func WithTraceID(ctx context.Context, traceID string) context.Context { return ctx }

// TraceIDFromContext 读取 manager 上下文中的 trace_id。禁用实现恒为空。
func TraceIDFromContext(ctx context.Context) string { return "" }