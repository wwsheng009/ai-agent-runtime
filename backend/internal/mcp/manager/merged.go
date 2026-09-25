package manager

import (
	"context"
	"errors"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// mergedManager 把两个 Manager 合并成一个工具面：primary 优先，secondary 兜底。
//
// 典型用途（ACP × MCP）：primary = 会话级 manager（客户端下发的 server），
// secondary = 进程级 manager（本地配置链）。按 §5-D4 的裁定，同名 server/工具
// 一律客户端优先；secondary 中与 primary 规范名冲突的条目会被丢弃，
// 保证 tools.Manager 看到的是一份无重复的工具清单。
type mergedManager struct {
	primary   Manager
	secondary Manager
}

// NewMergedManager 组合两个 Manager；任一为 nil 时退化为另一个。
// 返回值始终非 nil（两者都为 nil 时返回 nil）。
func NewMergedManager(primary, secondary Manager) Manager {
	switch {
	case primary == nil && secondary == nil:
		return nil
	case primary == nil:
		return secondary
	case secondary == nil:
		return primary
	default:
		return &mergedManager{primary: primary, secondary: secondary}
	}
}

// Start 启动两个 manager：primary 失败不阻断 secondary（会话级失败不得拖垮全局）。
func (m *mergedManager) Start(ctx context.Context) error {
	var errs []error
	if m.primary != nil {
		if err := m.primary.Start(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if m.secondary != nil {
		if err := m.secondary.Start(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// StartAsync 异步启动两个 manager。
func (m *mergedManager) StartAsync(ctx context.Context) error {
	var errs []error
	if m.primary != nil {
		if err := startManagerAsync(m.primary, ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if m.secondary != nil {
		if err := startManagerAsync(m.secondary, ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WaitReady 等待两个 manager 的首轮建连结束。
func (m *mergedManager) WaitReady(ctx context.Context) error {
	var errs []error
	if m.primary != nil {
		if err := waitManagerReady(m.primary, ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if m.secondary != nil {
		if err := waitManagerReady(m.secondary, ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Stop 停止两个 manager；两边都尝试，错误合并返回。
func (m *mergedManager) Stop() error {
	var errs []error
	if m.primary != nil {
		if err := m.primary.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if m.secondary != nil {
		if err := m.secondary.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ListTools 返回合并后的工具清单：primary 优先，secondary 丢弃同名冲突项。
func (m *mergedManager) ListTools() []*registry.ToolInfo {
	if m.primary == nil {
		return m.secondary.ListTools()
	}
	if m.secondary == nil {
		return m.primary.ListTools()
	}
	primaryTools := m.primary.ListTools()
	taken := make(map[string]struct{})
	for _, name := range registry.CallableToolNames(primaryTools) {
		taken[name] = struct{}{}
	}
	secondaryTools := m.secondary.ListTools()
	secondaryNames := registry.CallableToolNames(secondaryTools)
	merged := make([]*registry.ToolInfo, 0, len(primaryTools)+len(secondaryTools))
	merged = append(merged, primaryTools...)
	for index, info := range secondaryTools {
		if index < len(secondaryNames) {
			if _, exists := taken[secondaryNames[index]]; exists {
				continue
			}
			taken[secondaryNames[index]] = struct{}{}
		}
		merged = append(merged, info)
	}
	return merged
}

// FindTool 按名称查找工具：primary 优先。
func (m *mergedManager) FindTool(toolName string) (*registry.ToolInfo, error) {
	if m.primary != nil {
		info, err := m.primary.FindTool(toolName)
		if err == nil && info != nil {
			return info, nil
		}
		// 歧义（同名短名映射到多个 server）必须上抛：换 secondary 匹配会掩盖
		// 真实的命名冲突。其余错误（含「工具不存在」）视为本 manager 无此工具，
		// 继续到 secondary 查找。
		if registry.IsAmbiguousToolError(err) {
			return nil, err
		}
	}
	if m.secondary != nil {
		return m.secondary.FindTool(toolName)
	}
	return nil, errors.New("工具不存在")
}

// CallTool 调用工具：先按 mcpName 归属选择 manager，归属不明时 primary 优先。
func (m *mergedManager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if m.primary == nil {
		return m.secondary.CallTool(ctx, mcpName, toolName, args)
	}
	if m.secondary == nil {
		return m.primary.CallTool(ctx, mcpName, toolName, args)
	}
	if ownsTool(m.primary, mcpName, toolName) {
		return m.primary.CallTool(ctx, mcpName, toolName, args)
	}
	if ownsTool(m.secondary, mcpName, toolName) {
		return m.secondary.CallTool(ctx, mcpName, toolName, args)
	}
	// 归属无法判定（工具未注册/已掉线）：保持 primary 优先的调用语义，
	// 由 manager 自己返回其标准错误，避免在这里伪造调用结果。
	return m.primary.CallTool(ctx, mcpName, toolName, args)
}

// ListResources 资源查询：primary 优先。
func (m *mergedManager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	if m.primary == nil {
		return m.secondary.ListResources(ctx, mcpName, cursor)
	}
	if m.secondary == nil {
		return m.primary.ListResources(ctx, mcpName, cursor)
	}
	if ownsMCP(m.primary, mcpName) {
		return m.primary.ListResources(ctx, mcpName, cursor)
	}
	if ownsMCP(m.secondary, mcpName) {
		return m.secondary.ListResources(ctx, mcpName, cursor)
	}
	return m.primary.ListResources(ctx, mcpName, cursor)
}

// SetMCPEnabled 启停：按归属转发，未知时两边都尝试（幂等语义由 manager 保证）。
func (m *mergedManager) SetMCPEnabled(name string, enabled bool) error {
	if m.primary != nil && ownsMCP(m.primary, name) {
		return m.primary.SetMCPEnabled(name, enabled)
	}
	if m.secondary != nil && ownsMCP(m.secondary, name) {
		return m.secondary.SetMCPEnabled(name, enabled)
	}
	if m.primary != nil {
		return m.primary.SetMCPEnabled(name, enabled)
	}
	return m.secondary.SetMCPEnabled(name, enabled)
}

// GetMCPStatus 状态查询：primary 优先。
func (m *mergedManager) GetMCPStatus(name string) (*config.MCPStatus, error) {
	if m.primary != nil {
		status, err := m.primary.GetMCPStatus(name)
		if err == nil && status != nil {
			return status, nil
		}
	}
	if m.secondary != nil {
		return m.secondary.GetMCPStatus(name)
	}
	if m.primary != nil {
		return m.primary.GetMCPStatus(name)
	}
	return nil, errors.New("mcp manager is nil")
}

// StderrDiagnostics 诊断查询：primary 有内容优先，其次 secondary（可选能力）。
func (m *mergedManager) StderrDiagnostics(name string) string {
	if m.primary != nil {
		if provider, ok := m.primary.(StderrDiagnosticsProvider); ok && provider != nil {
			if diag := strings.TrimSpace(provider.StderrDiagnostics(name)); diag != "" {
				return diag
			}
		}
	}
	if m.secondary != nil {
		if provider, ok := m.secondary.(StderrDiagnosticsProvider); ok && provider != nil {
			return strings.TrimSpace(provider.StderrDiagnostics(name))
		}
	}
	return ""
}

// MCPConfigOrigins 来源查询：secondary（本地配置链）为基础，primary（客户端下发）覆盖同名项。
func (m *mergedManager) MCPConfigOrigins() map[string]config.ServerOrigin {
	out := make(map[string]config.ServerOrigin)
	if m.secondary != nil {
		if reporter, ok := m.secondary.(ConfigOriginReporter); ok && reporter != nil {
			for name, origin := range reporter.MCPConfigOrigins() {
				out[name] = origin
			}
		}
	}
	if m.primary != nil {
		if reporter, ok := m.primary.(ConfigOriginReporter); ok && reporter != nil {
			for name, origin := range reporter.MCPConfigOrigins() {
				out[name] = origin
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// MCPConfigWarnings 返回两侧的分层加载告警（primary 在前，便于定位来源）。
func (m *mergedManager) MCPConfigWarnings() []string {
	var out []string
	for _, mgr := range []Manager{m.primary, m.secondary} {
		if mgr == nil {
			continue
		}
		if reporter, ok := mgr.(ConfigOriginReporter); ok && reporter != nil {
			out = append(out, reporter.MCPConfigWarnings()...)
		}
	}
	return out
}

// ListMCPs 返回两边的状态并集（primary 优先，同名去重）。
func (m *mergedManager) ListMCPs() []*config.MCPStatus {
	if m.primary == nil {
		return m.secondary.ListMCPs()
	}
	if m.secondary == nil {
		return m.primary.ListMCPs()
	}
	primaryStatuses := m.primary.ListMCPs()
	seen := make(map[string]struct{}, len(primaryStatuses))
	out := make([]*config.MCPStatus, 0, len(primaryStatuses))
	for _, status := range primaryStatuses {
		if status != nil {
			seen[status.Name] = struct{}{}
		}
		out = append(out, status)
	}
	for _, status := range m.secondary.ListMCPs() {
		if status != nil {
			if _, exists := seen[status.Name]; exists {
				continue
			}
			seen[status.Name] = struct{}{}
		}
		out = append(out, status)
	}
	return out
}

// ReloadConfig 重载：两边都尝试。
func (m *mergedManager) ReloadConfig() error {
	var errs []error
	if m.primary != nil {
		if err := m.primary.ReloadConfig(); err != nil {
			errs = append(errs, err)
		}
	}
	if m.secondary != nil {
		if err := m.secondary.ReloadConfig(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// LoadConfig 文件加载入口：合并语义下无单一配置文件，按 primary 语义处理。
func (m *mergedManager) LoadConfig(configPath string) error {
	if m.primary != nil {
		return m.primary.LoadConfig(configPath)
	}
	return m.secondary.LoadConfig(configPath)
}

// LoadConfigEffective 分层加载入口（实现 LayeredConfigLoader）：优先 primary，
// primary 不支持该能力时回退 secondary（本地配置链通常是分层加载的一方）。
func (m *mergedManager) LoadConfigEffective(explicitPath string) error {
	if loader, ok := m.primary.(LayeredConfigLoader); ok && loader != nil {
		return loader.LoadConfigEffective(explicitPath)
	}
	if loader, ok := m.secondary.(LayeredConfigLoader); ok && loader != nil {
		return loader.LoadConfigEffective(explicitPath)
	}
	if m.primary != nil {
		return m.primary.LoadConfig(explicitPath)
	}
	return m.secondary.LoadConfig(explicitPath)
}

// AddLifecycleObserver 订阅两个 manager 的生命周期事件。
func (m *mergedManager) AddLifecycleObserver(observer LifecycleObserver) {
	if observer == nil {
		return
	}
	if observable, ok := m.primary.(ObservableManager); ok {
		observable.AddLifecycleObserver(observer)
	}
	if observable, ok := m.secondary.(ObservableManager); ok {
		observable.AddLifecycleObserver(observer)
	}
}

// ListQuarantinedTools 返回两边的隔离工具并集。
func (m *mergedManager) ListQuarantinedTools() []registry.QuarantinedToolInfo {
	var out []registry.QuarantinedToolInfo
	if reporter, ok := m.primary.(QuarantineReporter); ok {
		out = append(out, reporter.ListQuarantinedTools()...)
	}
	if reporter, ok := m.secondary.(QuarantineReporter); ok {
		out = append(out, reporter.ListQuarantinedTools()...)
	}
	return out
}

func startManagerAsync(mgr Manager, ctx context.Context) error {
	if async, ok := mgr.(AsyncManager); ok {
		return async.StartAsync(ctx)
	}
	return mgr.Start(ctx)
}

func waitManagerReady(mgr Manager, ctx context.Context) error {
	if async, ok := mgr.(AsyncManager); ok {
		return async.WaitReady(ctx)
	}
	return nil
}

func ownsTool(mgr Manager, mcpName, toolName string) bool {
	if mgr == nil {
		return false
	}
	info, err := mgr.FindTool(toolName)
	if err != nil || info == nil {
		return false
	}
	return info.MCPName == mcpName
}

func ownsMCP(mgr Manager, mcpName string) bool {
	if mgr == nil {
		return false
	}
	for _, status := range mgr.ListMCPs() {
		if status != nil && status.Name == mcpName {
			return true
		}
	}
	return false
}
