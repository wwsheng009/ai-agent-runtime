package manager

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/client"
	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/config"
	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/protocol"
	"github.com/ai-gateway/ai-agent-runtime/internal/mcp/registry"
	runtimeerrors "github.com/ai-gateway/ai-agent-runtime/internal/errors"
)

var statusOutput io.Writer = os.Stdout

// SetStatusOutput 设置管理器状态输出目标。传入 nil 表示静默。
func SetStatusOutput(w io.Writer) {
	statusOutput = w
}

func printStatusf(format string, args ...interface{}) {
	if statusOutput == nil {
		return
	}
	_, _ = fmt.Fprintf(statusOutput, format, args...)
}

// Manager MCP 管理器接口
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

// manager MCP 管理器实现
type manager struct {
	cfg       *config.Config
	loader    *config.Loader
	registry  *registry.Registry
	clients   map[string]client.Client
	started   bool
	status    map[string]*config.MCPStatus
	newClient func(name string, cfg *config.MCPConfig) (client.Client, error)

	healthCtx    context.Context
	healthCancel context.CancelFunc
	healthDone   chan struct{}
	observers    []LifecycleObserver
	observerMu   sync.RWMutex
	mu           sync.RWMutex
}

// NewManager 创建管理器
func NewManager() Manager {
	return &manager{
		registry:  registry.NewRegistry(),
		clients:   make(map[string]client.Client),
		status:    make(map[string]*config.MCPStatus),
		newClient: client.NewClient,
		observers: make([]LifecycleObserver, 0),
	}
}

// WithTraceID 将 trace_id 绑定到 manager 上下文，便于生命周期事件透传。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return client.WithTraceID(ctx, traceID)
}

// TraceIDFromContext 读取 manager 上下文中的 trace_id。
func TraceIDFromContext(ctx context.Context) string {
	return client.TraceIDFromContext(ctx)
}

// AddLifecycleObserver 注册 MCP manager 生命周期观察者。
func (m *manager) AddLifecycleObserver(observer LifecycleObserver) {
	if m == nil || observer == nil {
		return
	}
	m.observerMu.Lock()
	defer m.observerMu.Unlock()
	m.observers = append(m.observers, observer)
}

// LoadConfig 加载配置
func (m *manager) LoadConfig(configPath string) error {
	m.loader = config.NewLoader(configPath)

	cfg, err := m.loader.Load()
	if err != nil {
		return fmt.Errorf("加载配置失败: %w", err)
	}

	m.cfg = cfg
	return nil
}

// Start 启动所有启用的 MCPs
func (m *manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("配置未加载，请先调用 LoadConfig")
	}

	if m.started {
		return fmt.Errorf("管理器已经启动")
	}

	// 启动所有启用的 MCP
	for name, mcpCfg := range m.cfg.MCPServers {
		status := m.ensureStatusLocked(name, &mcpCfg)
		if !mcpCfg.IsEnabled() {
			status.Enabled = false
			m.emitLifecycleEvent(ctx, "mcp.disabled", name, map[string]interface{}{
				"execution_mode": mcpCfg.ExecutionMode(),
				"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
			})
			continue
		}
		status.Enabled = true
		m.emitLifecycleEvent(ctx, "mcp.starting", name, map[string]interface{}{
			"execution_mode": mcpCfg.ExecutionMode(),
			"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
		})

		cli, err := m.createAndConnectClient(ctx, name, &mcpCfg)
		if err != nil {
			status.LastError = err.Error()
			m.emitLifecycleEvent(ctx, "mcp.connect_failed", name, map[string]interface{}{
				"error":          err.Error(),
				"execution_mode": mcpCfg.ExecutionMode(),
				"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
			})
			printStatusf("[Manager] 启动 MCP 失败: %s - %v\n", name, err)
			continue
		}

		m.clients[name] = cli
		m.registry.RegisterClient(name, cli)
		status.LastConnect = time.Now()
		status.LastError = ""
		m.emitLifecycleEvent(ctx, "mcp.connected", name, map[string]interface{}{
			"execution_mode": mcpCfg.ExecutionMode(),
			"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
		})

		// 加载工具
		m.loadTools(ctx, cli, name)

		printStatusf("[Manager] MCP 已启动: %s (工具: %d)\n", name, len(m.registry.ListToolsByMCP(name)))
	}

	m.started = true
	m.startHealthCheckerLocked(ctx)
	return nil
}

// createAndConnectClient 创建并连接客户端
func (m *manager) createAndConnectClient(ctx context.Context, name string, mcpCfg *config.MCPConfig) (client.Client, error) {
	cli, err := m.newClient(name, mcpCfg)
	if err != nil {
		return nil, err
	}
	if observable, ok := cli.(client.ObservableClient); ok && observable != nil {
		observable.AddLifecycleObserver(func(event client.LifecycleEvent) {
			payload := cloneLifecyclePayload(event.Payload)
			if payload == nil {
				payload = map[string]interface{}{}
			}
			if event.ClientName != "" {
				payload["client_name"] = event.ClientName
			}
			if event.TransportType != "" {
				payload["transport_type"] = event.TransportType
			}
			if event.SessionID != "" {
				payload["session_id"] = event.SessionID
			}
			m.emitLifecycleEvent(client.WithTraceID(context.Background(), event.TraceID), event.Type, name, payload)
		})
	}

	// 连接到 MCP Server
	// 只在连接阶段使用带超时的 context
	timeoutCtx, cancel := context.WithTimeout(ctx, m.resolveConnectTimeout(mcpCfg))
	defer cancel()

	if err := cli.Connect(timeoutCtx); err != nil {
		return nil, err
	}

	return cli, nil
}

// loadTools 加载 MCP 工具
func (m *manager) loadTools(ctx context.Context, cli client.Client, mcpName string) {
	tools, err := cli.ListTools(ctx)
	if err != nil {
		m.emitLifecycleEvent(ctx, "mcp.tools_load_failed", mcpName, map[string]interface{}{
			"error": err.Error(),
		})
		printStatusf("[Manager] 加载工具失败: %s - %v\n", mcpName, err)
		return
	}

	for _, tool := range tools {
		m.registry.RegisterTool(mcpName, tool, true)
	}
	m.emitLifecycleEvent(ctx, "mcp.tools.loaded", mcpName, map[string]interface{}{
		"tool_count": len(tools),
	})
}

// Stop 停止所有 MCPs
func (m *manager) Stop() error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	clients := m.clients
	m.clients = make(map[string]client.Client)
	m.registry.Clear()
	m.started = false
	m.mu.Unlock()

	m.stopHealthChecker()

	// 停止所有客户端
	for name, cli := range clients {
		m.emitLifecycleEvent(context.Background(), "mcp.stopped", name, map[string]interface{}{
			"tool_count": len(m.registry.ListToolsByMCP(name)),
		})
		if err := cli.Close(); err != nil {
			printStatusf("[Manager] 停止 MCP 失败: %s - %v\n", name, err)
		}
	}

	return nil
}

// ListTools 列出所有工具
func (m *manager) ListTools() []*registry.ToolInfo {
	return m.registry.ListTools()
}

// CallTool 调用工具
func (m *manager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	// 获取客户端
	cli, err := m.registry.GetClient(mcpName)
	if err != nil {
		return nil, err
	}

	// 调用工具
	result, err := cli.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, m.wrapToolCallError(mcpName, toolName, err)
	}

	return result, nil
}

// ListResources 列出资源
func (m *manager) ListResources(ctx context.Context, mcpName string, cursor *string) (*protocol.ListResourcesResult, error) {
	cli, err := m.registry.GetClient(mcpName)
	if err != nil {
		return nil, err
	}

	result, err := cli.ListResources(ctx, cursor)
	if err != nil {
		return nil, fmt.Errorf("列出资源失败: %w", err)
	}

	return result, nil
}

// FindTool 查找工具
func (m *manager) FindTool(toolName string) (*registry.ToolInfo, error) {
	// 遍历所有 MCP 查找工具
	for _, info := range m.registry.ListTools() {
		if info.Tool.Name == toolName {
			return info, nil
		}
	}
	return nil, fmt.Errorf("工具不存在: %s", toolName)
}

// SetMCPEnabled 启用/禁用 MCP
func (m *manager) SetMCPEnabled(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return fmt.Errorf("配置未加载")
	}

	mcpCfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return fmt.Errorf("MCP 不存在: %s", name)
	}

	// 更新配置
	mcpCfg.Enabled = enabled
	mcpCfg.Disabled = !enabled
	m.cfg.MCPServers[name] = mcpCfg
	status := m.ensureStatusLocked(name, &mcpCfg)
	status.Enabled = enabled

	// 如果禁用，停止客户端
	if !enabled {
		if cli, ok := m.clients[name]; ok {
			cli.Close()
			delete(m.clients, name)
			m.registry.UnregisterClient(name)
		}
		status.Connected = false
		m.emitLifecycleEvent(context.Background(), "mcp.disabled", name, map[string]interface{}{
			"enabled": false,
		})
	}

	return nil
}

// GetMCPStatus 获取 MCP 状态
func (m *manager) GetMCPStatus(name string) (*config.MCPStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cfg == nil {
		return nil, fmt.Errorf("配置未加载")
	}

	mcpCfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return nil, fmt.Errorf("MCP 不存在: %s", name)
	}

	status := &config.MCPStatus{
		Name:          name,
		Type:          mcpCfg.Type,
		TrustLevel:    mcpCfg.ResolvedTrustLevel(),
		ExecutionMode: mcpCfg.ExecutionMode(),
		Enabled:       mcpCfg.IsEnabled(),
	}
	if runtimeStatus, ok := m.status[name]; ok && runtimeStatus != nil {
		status.LastError = runtimeStatus.LastError
		status.LastConnect = runtimeStatus.LastConnect
		status.HealthCheck = runtimeStatus.HealthCheck
	}

	// 检查连接状态
	if cli, ok := m.clients[name]; ok {
		status.Connected = cli.IsConnected()
		status.ToolCount = len(m.registry.ListToolsByMCP(name))
	}

	return status, nil
}

// ListMCPs 列出所有 MCP
func (m *manager) ListMCPs() []*config.MCPStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cfg == nil {
		return nil
	}

	statuses := make([]*config.MCPStatus, 0)
	for name := range m.cfg.MCPServers {
		status, _ := m.GetMCPStatus(name)
		statuses = append(statuses, status)
	}

	return statuses
}

// ReloadConfig 重新加载配置
func (m *manager) ReloadConfig() error {
	m.mu.Lock()
	clients := m.clients
	m.clients = make(map[string]client.Client)
	m.registry.Clear()
	wasStarted := m.started
	m.started = false
	m.status = make(map[string]*config.MCPStatus)
	m.mu.Unlock()

	if wasStarted {
		m.stopHealthChecker()
		for _, cli := range clients {
			cli.Close()
		}
	}

	// 重新加载配置
	if m.loader != nil {
		cfg, err := m.loader.Load()
		if err != nil {
			return err
		}
		m.cfg = cfg
	}

	return nil
}

func (m *manager) ensureStatusLocked(name string, mcpCfg *config.MCPConfig) *config.MCPStatus {
	status := m.status[name]
	if status == nil {
		status = &config.MCPStatus{Name: name}
		m.status[name] = status
	}
	if mcpCfg != nil {
		status.Name = name
		status.Type = mcpCfg.Type
		status.TrustLevel = mcpCfg.ResolvedTrustLevel()
		status.ExecutionMode = mcpCfg.ExecutionMode()
		status.Enabled = mcpCfg.IsEnabled()
	}
	return status
}

func (m *manager) wrapToolCallError(mcpName, toolName string, err error) error {
	mcpCfg := m.lookupMCPConfig(mcpName)
	ctx := map[string]interface{}{
		"governance_scope": "mcp",
		"mcp_name":         mcpName,
		"tool":             toolName,
	}
	if mcpCfg != nil {
		ctx["mcp_trust_level"] = string(mcpCfg.ResolvedTrustLevel())
		ctx["execution_mode"] = mcpCfg.ExecutionMode()
	}

	var runtimeErr *runtimeerrors.RuntimeError
	if stderrors.As(err, &runtimeErr) {
		enriched := runtimeErr
		for key, value := range ctx {
			enriched = enriched.WithContext(key, value)
		}
		return enriched.Prepend("mcp tool call failed: ")
	}

	return runtimeerrors.WrapWithContext(
		runtimeerrors.ErrToolExecution,
		fmt.Sprintf("mcp tool %q failed on server %q", toolName, mcpName),
		err,
		ctx,
	)
}

func (m *manager) lookupMCPConfig(name string) *config.MCPConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return nil
	}
	mcpCfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return nil
	}
	cloned := mcpCfg
	return &cloned
}

func (m *manager) setStatus(name string, mcpCfg *config.MCPConfig, update func(*config.MCPStatus)) {
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.ensureStatusLocked(name, mcpCfg)
	if update != nil {
		update(status)
	}
}

func (m *manager) resolveConnectTimeout(mcpCfg *config.MCPConfig) time.Duration {
	if mcpCfg != nil && mcpCfg.Timeout.Duration > 0 {
		return mcpCfg.Timeout.Duration
	}
	if m.cfg != nil && m.cfg.Global.ConnectTimeout.Duration > 0 {
		return m.cfg.Global.ConnectTimeout.Duration
	}
	return 10 * time.Second
}

func (m *manager) startHealthCheckerLocked(ctx context.Context) {
	if m.cfg == nil {
		return
	}
	interval := m.cfg.Global.HealthCheckInterval.Duration
	if interval <= 0 {
		return
	}
	if m.healthCancel != nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.healthCtx, m.healthCancel = context.WithCancel(ctx)
	m.healthDone = make(chan struct{})
	go m.healthLoop(m.healthCtx, interval, m.healthDone)
}

func (m *manager) stopHealthChecker() {
	m.mu.Lock()
	cancel := m.healthCancel
	done := m.healthDone
	m.healthCancel = nil
	m.healthDone = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (m *manager) healthLoop(ctx context.Context, interval time.Duration, done chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer func() {
		if done != nil {
			close(done)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.healthCheckOnce()
		}
	}
}

func (m *manager) healthCheckOnce() {
	m.mu.RLock()
	if m.cfg == nil {
		m.mu.RUnlock()
		return
	}

	entries := make([]struct {
		name string
		cfg  config.MCPConfig
	}, 0, len(m.cfg.MCPServers))
	for name, cfg := range m.cfg.MCPServers {
		entries = append(entries, struct {
			name string
			cfg  config.MCPConfig
		}{name: name, cfg: cfg})
	}
	m.mu.RUnlock()

	for _, entry := range entries {
		if !entry.cfg.IsEnabled() {
			continue
		}
		m.checkMCPHealth(entry.name, &entry.cfg)
	}
}

func (m *manager) checkMCPHealth(name string, mcpCfg *config.MCPConfig) {
	cli := m.getClient(name)
	if cli == nil || !cli.IsConnected() {
		m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
			status.HealthCheck = time.Now()
			status.LastError = "mcp client not connected"
		})
		m.emitLifecycleEvent(context.Background(), "mcp.health.failed", name, map[string]interface{}{
			"error": "mcp client not connected",
		})
		m.reconnectMCP(name, mcpCfg)
		return
	}

	checkCtx, cancel := context.WithTimeout(context.Background(), m.resolveConnectTimeout(mcpCfg))
	defer cancel()

	_, err := cli.ListTools(checkCtx)
	m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
		status.HealthCheck = time.Now()
		if err != nil {
			status.LastError = err.Error()
		} else {
			status.LastError = ""
		}
	})

	if err != nil {
		m.emitLifecycleEvent(context.Background(), "mcp.health.failed", name, map[string]interface{}{
			"error": err.Error(),
		})
		m.reconnectMCP(name, mcpCfg)
		return
	}

	probeCfg := m.resolveHealthCheckConfig(mcpCfg)
	if probeCfg == nil {
		m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
			status.LastError = ""
		})
		return
	}

	probeErrors := m.runHealthProbes(name, cli, probeCfg)
	if len(probeErrors) > 0 {
		m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
			status.LastError = fmt.Sprintf("probe failed: %s", strings.Join(probeErrors, "; "))
		})
		m.emitLifecycleEvent(context.Background(), "mcp.health.failed", name, map[string]interface{}{
			"error": strings.Join(probeErrors, "; "),
		})
		return
	}

	m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
		status.LastError = ""
	})
}

func (m *manager) reconnectMCP(name string, mcpCfg *config.MCPConfig) {
	if mcpCfg == nil || !mcpCfg.IsEnabled() {
		return
	}

	reconnectCtx, cancel := context.WithTimeout(context.Background(), m.resolveConnectTimeout(mcpCfg))
	defer cancel()
	m.emitLifecycleEvent(reconnectCtx, "mcp.reconnect.started", name, map[string]interface{}{
		"execution_mode": mcpCfg.ExecutionMode(),
		"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
	})

	cli, err := m.createAndConnectClient(reconnectCtx, name, mcpCfg)
	if err != nil {
		m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
			status.LastError = err.Error()
		})
		m.emitLifecycleEvent(reconnectCtx, "mcp.reconnect.failed", name, map[string]interface{}{
			"error":          err.Error(),
			"execution_mode": mcpCfg.ExecutionMode(),
			"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
		})
		return
	}

	m.mu.Lock()
	if oldCli, ok := m.clients[name]; ok {
		_ = oldCli.Close()
		m.registry.UnregisterClient(name)
	}
	m.clients[name] = cli
	m.registry.RegisterClient(name, cli)
	status := m.ensureStatusLocked(name, mcpCfg)
	status.LastConnect = time.Now()
	status.LastError = ""
	m.mu.Unlock()
	m.emitLifecycleEvent(reconnectCtx, "mcp.reconnected", name, map[string]interface{}{
		"execution_mode": mcpCfg.ExecutionMode(),
		"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
	})

	m.loadTools(reconnectCtx, cli, name)
}

func (m *manager) getClient(name string) client.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[name]
}

func (m *manager) resolveHealthCheckConfig(mcpCfg *config.MCPConfig) *config.MCPHealthCheckConfig {
	if mcpCfg != nil && mcpCfg.HealthCheck != nil && !isEmptyHealthCheck(mcpCfg.HealthCheck) {
		return mcpCfg.HealthCheck
	}
	if m.cfg != nil && !isEmptyHealthCheck(&m.cfg.Global.HealthCheck) {
		return &m.cfg.Global.HealthCheck
	}
	return nil
}

func isEmptyHealthCheck(cfg *config.MCPHealthCheckConfig) bool {
	if cfg == nil {
		return true
	}
	if len(cfg.Tools) > 0 {
		return false
	}
	if len(cfg.Resources) > 0 {
		return false
	}
	if len(cfg.ToolArgs) > 0 {
		return false
	}
	return true
}

func (m *manager) runHealthProbes(mcpName string, cli client.Client, cfg *config.MCPHealthCheckConfig) []string {
	if cli == nil || cfg == nil {
		return nil
	}

	errors := make([]string, 0)

	for _, toolName := range cfg.Tools {
		if toolName == "" {
			continue
		}
		args := map[string]interface{}{}
		if cfg.ToolArgs != nil {
			if toolArgs, ok := cfg.ToolArgs[toolName]; ok && toolArgs != nil {
				args = toolArgs
			}
		}
		_, err := cli.CallTool(context.Background(), toolName, args)
		if err != nil {
			errors = append(errors, fmt.Sprintf("tool %s: %v", toolName, err))
			_ = m.registry.DisableTool(mcpName, toolName)
		} else {
			_ = m.registry.EnableTool(mcpName, toolName)
		}
	}

	for _, resource := range cfg.Resources {
		if resource == "" {
			continue
		}
		_, err := cli.ReadResource(context.Background(), resource)
		if err != nil {
			errors = append(errors, fmt.Sprintf("resource %s: %v", resource, err))
		}
	}

	return errors
}

func (m *manager) emitLifecycleEvent(ctx context.Context, eventType, mcpName string, payload map[string]interface{}) {
	if m == nil {
		return
	}
	m.observerMu.RLock()
	observers := append([]LifecycleObserver(nil), m.observers...)
	m.observerMu.RUnlock()
	if len(observers) == 0 {
		return
	}

	event := LifecycleEvent{
		Type:      eventType,
		TraceID:   TraceIDFromContext(ctx),
		MCPName:   mcpName,
		Payload:   cloneLifecyclePayload(payload),
		Timestamp: time.Now().UTC(),
	}
	for _, observer := range observers {
		observer(event)
	}
}

func cloneLifecyclePayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}
