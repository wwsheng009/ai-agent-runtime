//go:build !win7compat

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

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
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

// QuarantineReporter exposes invalid MCP definitions without adding them to
// the callable tool inventory.
type QuarantineReporter interface {
	ListQuarantinedTools() []registry.QuarantinedToolInfo
}

// AsyncManager 暴露 MCP 客户端的异步启动能力：
//   - StartAsync 触发全部启用 MCP 的后台并行建连后立即返回；
//   - WaitReady 等待首轮建连（含工具加载）全部结束，便于需要完整工具面的调用方。
//
// Manager 基础接口保持不变，调用方可按需断言该能力；未实现时回退同步 Start。
type AsyncManager interface {
	Manager
	StartAsync(ctx context.Context) error
	WaitReady(ctx context.Context) error
}

// ScopedManager 暴露「内存配置快照」加载能力。
//
// 会话级 manager（例如 ACP 客户端下发的 server）需要在不为每个会话写临时
// 配置文件的前提下复用 Manager 的建连/回收/事件机制；文件加载路径
// （LoadConfig）保持不变，本接口是并行的可选入口。
type ScopedManager interface {
	Manager
	// LoadConfigFromConfig 用内存快照替换待加载配置，语义与 LoadConfig 一致：
	// 深拷贝 + 补齐默认值，且只允许在 Start 之前调用。
	LoadConfigFromConfig(cfg *config.Config) error
}

const (
	// maxConcurrentMCPStarts 限制后台并行建连的 MCP 数量，避免一次性拉起过多子进程。
	maxConcurrentMCPStarts = 8
	// mcpConnectShutdownGrace 限制 Stop/Reload 等待后台建连退出的时间；
	// 超时后由 generation 守卫丢弃迟到结果，避免关闭路径被慢握手拖住。
	mcpConnectShutdownGrace = 5 * time.Second
)

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

	// 后台建连生命周期：generation 每次 Start/Stop/Reload 递增，
	// 迟到的建连结果据此丢弃，避免向已停止或已重载的管理器回填客户端。
	startCancel context.CancelFunc
	connDone    chan struct{}
	readyCh     chan struct{}
	generation  uint64
	connecting  map[string]struct{}
	pending     map[string]client.Client
	lastStderr  map[string]string
	mu          sync.RWMutex

	// OAuth 会话：令牌存储全局共享，会话按 server 名缓存（仅在 Start/Reload 时重建）。
	authMu       sync.Mutex
	authStore    *auth.TokenStore
	authSessions map[string]*auth.Session

	// 分层配置来源（§4.5 Step 1）：说明每个 server 来自哪个配置文件、覆盖了谁。
	layered        bool
	explicitPath   string
	origins        map[string]config.ServerOrigin
	originWarnings []string
}

// NewManager 创建管理器
func NewManager() Manager {
	return &manager{
		registry:     registry.NewRegistry(),
		clients:      make(map[string]client.Client),
		status:       make(map[string]*config.MCPStatus),
		newClient:    client.NewClient,
		observers:    make([]LifecycleObserver, 0),
		connecting:   make(map[string]struct{}),
		pending:      make(map[string]client.Client),
		lastStderr:   make(map[string]string),
		authSessions: make(map[string]*auth.Session),
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
	// 运行时入口才展开环境变量：文件与内存配置保持 `${VAR}` 原始字面量，
	// 避免管理操作读改写回时把真实值（或空串）固化进配置文件。
	// 缺失项写入各 server 的 EnvError，StartAsync 会隔离这些 server。
	config.ExpandEnv(cfg)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return fmt.Errorf("管理器已经启动")
	}
	m.cfg = cfg
	m.layered = false
	m.explicitPath = configPath
	m.recordSingleFileOrigins(cfg, configPath)
	return nil
}

// LoadConfigFromConfig 加载内存配置快照（见 ScopedManager）。
func (m *manager) LoadConfigFromConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("配置为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return fmt.Errorf("管理器已经启动")
	}
	m.cfg = config.CloneWithDefaults(cfg)
	return nil
}

// Start 启动所有启用的 MCPs，并等待首轮并行建连完成后返回。
func (m *manager) Start(ctx context.Context) error {
	if err := m.StartAsync(ctx); err != nil {
		return err
	}
	return m.WaitReady(ctx)
}

// StartAsync 触发所有启用 MCP 的后台并行建连后立即返回，不阻塞应用启动。
// 连接进度可通过 ListMCPs/GetMCPStatus 与生命周期事件观测；
// 需要完整工具面的调用方可用 WaitReady 等待首轮建连结束。
func (m *manager) StartAsync(ctx context.Context) error {
	m.mu.Lock()
	if m.cfg == nil {
		m.mu.Unlock()
		return fmt.Errorf("配置未加载，请先调用 LoadConfig")
	}
	if m.started {
		m.mu.Unlock()
		return fmt.Errorf("管理器已经启动")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	startCtx, cancel := context.WithCancel(ctx)
	m.startCancel = cancel
	m.started = true
	m.generation++
	m.readyCh = make(chan struct{})
	m.connDone = make(chan struct{})

	type startEntry struct {
		name string
		cfg  config.MCPConfig
	}
	enabled := make([]startEntry, 0, len(m.cfg.MCPServers))
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
		if envErr := strings.TrimSpace(mcpCfg.EnvError); envErr != "" {
			// 环境变量插值缺失：只停掉该 server，并在状态里带出可行动的错误，
			// 不影响其它 server 与配置管理操作。
			status.LastError = envErr
			m.emitLifecycleEvent(ctx, "mcp.connect_failed", name, map[string]interface{}{
				"error":          envErr,
				"execution_mode": mcpCfg.ExecutionMode(),
				"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
			})
			printStatusf("[Manager] 启动 MCP 失败: %s - %s\n", name, envErr)
			continue
		}
		if mcpCfg.IsOAuth() {
			session, sessErr := m.oauthSessionFor(name, &mcpCfg)
			if sessErr != nil {
				status.RequiresAuth = true
				status.LastError = fmt.Sprintf("OAuth 初始化失败: %v", sessErr)
				m.emitLifecycleEvent(ctx, "mcp.auth.required", name, map[string]interface{}{"error": status.LastError})
				printStatusf("[Manager] 需要认证: %s - %s\n", name, status.LastError)
				continue
			}
			if session != nil && !session.Ready() {
				// 未登录：隔离该 server，并把可行动提示写进状态（CLI / `/mcp` / Web 共用）。
				status.RequiresAuth = true
				status.LastError = fmt.Sprintf("需要认证：运行 `aicli mcp auth %s` 完成 OAuth 授权", name)
				m.emitLifecycleEvent(ctx, "mcp.auth.required", name, map[string]interface{}{"target": mcpCfg.URL})
				printStatusf("[Manager] 需要认证: %s - %s\n", name, status.LastError)
				continue
			}
			mcpCfg.TokenSource = session
			status.RequiresAuth = false
		}
		m.emitLifecycleEvent(ctx, "mcp.starting", name, map[string]interface{}{
			"execution_mode": mcpCfg.ExecutionMode(),
			"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
		})
		enabled = append(enabled, startEntry{name: name, cfg: mcpCfg})
	}

	gen := m.generation
	// 在持锁期间登记计数：Stop/Reload 即使在建连 goroutine 启动前抢占，
	// 也能等待全部 goroutine 退出，避免迟到客户端回填到已清理的管理器。
	wg := &sync.WaitGroup{}
	wg.Add(len(enabled))
	m.startHealthCheckerLocked(ctx)
	readyCh := m.readyCh
	connDone := m.connDone
	m.mu.Unlock()

	go func() {
		wg.Wait()
		close(readyCh)
		close(connDone)
	}()

	sem := make(chan struct{}, maxConcurrentMCPStarts)
	for _, entry := range enabled {
		entry := entry
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-startCtx.Done():
				return
			}
			defer func() { <-sem }()
			m.connectMCP(startCtx, gen, entry.name, &entry.cfg)
		}()
	}
	return nil
}

// WaitReady 等待首轮建连（含工具加载）结束；管理器未启动时立即返回。
func (m *manager) WaitReady(ctx context.Context) error {
	m.mu.RLock()
	readyCh := m.readyCh
	m.mu.RUnlock()
	if readyCh == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// connectMCP 在后台完成单个 MCP 的建连、注册与工具加载。
func (m *manager) connectMCP(ctx context.Context, gen uint64, name string, mcpCfg *config.MCPConfig) {
	m.markConnecting(gen, name, true)
	defer m.markConnecting(gen, name, false)

	if ctx.Err() != nil {
		return
	}

	cli, err := m.createClient(name, mcpCfg)
	if err != nil {
		m.recordConnectFailure(ctx, name, mcpCfg, err)
		return
	}
	if !m.trackPending(gen, name, cli) {
		_ = cli.Close()
		return
	}
	connectErr := m.connectClient(ctx, cli, mcpCfg)
	m.untrackPending(name, cli)
	if connectErr != nil {
		// 客户端随后会被 Close，先把 stdio 诊断留存下来供 --show-stderr 读取。
		m.rememberStderrDiagnostics(name, cli)
		_ = cli.Close()
		m.recordConnectFailure(ctx, name, mcpCfg, connectErr)
		return
	}

	m.mu.Lock()
	if !m.isGenerationCurrentLocked(gen) || !m.isEnabledLocked(name) {
		m.mu.Unlock()
		_ = cli.Close()
		return
	}
	m.clients[name] = cli
	m.registry.RegisterClient(name, cli)
	status := m.ensureStatusLocked(name, mcpCfg)
	status.LastConnect = time.Now()
	status.LastError = ""
	status.RequiresAuth = false
	m.mu.Unlock()
	m.clearStderrDiagnostics(name)

	m.emitLifecycleEvent(ctx, "mcp.connected", name, map[string]interface{}{
		"execution_mode": mcpCfg.ExecutionMode(),
		"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
	})

	// 加载工具
	m.loadTools(ctx, cli, name, gen)

	printStatusf("[Manager] MCP 已启动: %s (工具: %d)\n", name, len(m.registry.ListToolsByMCP(name)))
}

// recordConnectFailure 记录连接失败；管理器停止/重载导致的取消不记为可见失败。
func (m *manager) recordConnectFailure(ctx context.Context, name string, mcpCfg *config.MCPConfig, err error) {
	if err == nil || ctx.Err() != nil {
		return
	}
	m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
		status.LastError = err.Error()
		// OAuth：401/403 或刷新失败时，把状态升级为「需认证」并给出可行动提示。
		if session := m.oauthSession(name); session != nil {
			if needs, reason := session.NeedsAuth(); needs {
				status.RequiresAuth = true
				if strings.TrimSpace(reason) != "" {
					status.LastError = reason
				}
			}
		}
	})
	m.emitLifecycleEvent(ctx, "mcp.connect_failed", name, map[string]interface{}{
		"error":          err.Error(),
		"execution_mode": mcpCfg.ExecutionMode(),
		"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
	})
	printStatusf("[Manager] 启动 MCP 失败: %s - %v\n", name, err)
}

// tokenStore 懒加载全局共享的令牌存储。
func (m *manager) tokenStore() (*auth.TokenStore, error) {
	m.authMu.Lock()
	defer m.authMu.Unlock()
	if m.authStore != nil {
		return m.authStore, nil
	}
	store, err := auth.NewTokenStore("")
	if err != nil {
		return nil, err
	}
	m.authStore = store
	return store, nil
}

// oauthSessionFor 重建并缓存指定 server 的 OAuth 会话；非 oauth server 返回 nil。
func (m *manager) oauthSessionFor(name string, mcpCfg *config.MCPConfig) (*auth.Session, error) {
	if mcpCfg == nil || !mcpCfg.IsOAuth() || mcpCfg.Auth == nil {
		return nil, nil
	}
	store, err := m.tokenStore()
	if err != nil {
		return nil, err
	}
	session, err := auth.NewSession(name, mcpCfg.URL, *mcpCfg.Auth, auth.SessionOptions{Store: store})
	if err != nil {
		return nil, err
	}
	m.authMu.Lock()
	if m.authSessions == nil {
		// 测试或外部构造的 manager 可能绕开 NewManager，这里兜底初始化。
		m.authSessions = make(map[string]*auth.Session)
	}
	m.authSessions[name] = session
	m.authMu.Unlock()
	return session, nil
}

// oauthSession 返回已缓存的会话（可能为 nil）。
func (m *manager) oauthSession(name string) *auth.Session {
	if m == nil {
		return nil
	}
	m.authMu.Lock()
	defer m.authMu.Unlock()
	return m.authSessions[name]
}

// trackPending 登记在建客户端，供 Stop/Reload 主动取消慢握手。
func (m *manager) trackPending(gen uint64, name string, cli client.Client) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isGenerationCurrentLocked(gen) {
		return false
	}
	if m.pending == nil {
		m.pending = make(map[string]client.Client)
	}
	m.pending[name] = cli
	return true
}

// untrackPending 按身份移除在建客户端，避免误删新一代同名连接。
func (m *manager) untrackPending(name string, cli client.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.pending[name]; ok && current == cli {
		delete(m.pending, name)
	}
}

func (m *manager) markConnecting(gen uint64, name string, connecting bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connecting == nil {
		m.connecting = make(map[string]struct{})
	}
	if connecting {
		if !m.isGenerationCurrentLocked(gen) {
			return
		}
		m.connecting[name] = struct{}{}
		return
	}
	// 只清理本代留下的标记，避免误删新一代同名 server 的建连状态。
	if m.generation == gen {
		delete(m.connecting, name)
	}
}

func (m *manager) isConnecting(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.connecting[name]
	return ok
}

func (m *manager) isGenerationCurrent(gen uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isGenerationCurrentLocked(gen)
}

func (m *manager) isGenerationCurrentLocked(gen uint64) bool {
	return m.started && m.generation == gen
}

func (m *manager) isEnabledLocked(name string) bool {
	if m.cfg == nil {
		return false
	}
	mcpCfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return false
	}
	return mcpCfg.IsEnabled()
}

func (m *manager) currentGeneration() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.generation
}

// waitForConnects 有界等待后台建连退出；超时仅打日志，迟到结果由 generation 丢弃。
func (m *manager) waitForConnects(done chan struct{}) {
	if done == nil {
		return
	}
	timer := time.NewTimer(mcpConnectShutdownGrace)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		printStatusf("[Manager] 等待后台 MCP 建连退出超时，转入后台清理\n")
	}
}

// createClient 构造客户端并桥接其生命周期事件。
func (m *manager) createClient(name string, mcpCfg *config.MCPConfig) (client.Client, error) {
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
	return cli, nil
}

// connectClient 在带超时的上下文中连接 MCP Server（超时只在连接阶段生效）。
func (m *manager) connectClient(ctx context.Context, cli client.Client, mcpCfg *config.MCPConfig) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, m.resolveConnectTimeout(mcpCfg))
	defer cancel()
	return cli.Connect(timeoutCtx)
}

// loadTools 加载 MCP 工具。gen 用于丢弃停止/重载后迟到的注册结果。
func (m *manager) loadTools(ctx context.Context, cli client.Client, mcpName string, gen uint64) {
	if !m.isGenerationCurrent(gen) {
		return
	}
	tools, err := cli.ListTools(ctx)
	if err != nil {
		if !m.isGenerationCurrent(gen) {
			return
		}
		m.emitLifecycleEvent(ctx, "mcp.tools_load_failed", mcpName, map[string]interface{}{
			"error": err.Error(),
		})
		printStatusf("[Manager] 加载工具失败: %s - %v\n", mcpName, err)
		return
	}

	loadedCount := 0
	quarantinedCount := 0
	for _, tool := range tools {
		if !m.isGenerationCurrent(gen) {
			return
		}
		if registerErr := m.registry.RegisterTool(mcpName, tool, true); registerErr != nil {
			quarantinedCount++
			toolName := ""
			if tool != nil {
				toolName = tool.Name
			}
			payload := map[string]interface{}{
				"tool_name":      toolName,
				"canonical_name": registry.CanonicalToolName(mcpName, toolName),
				"error":          registerErr.Error(),
			}
			for _, quarantined := range m.registry.ListQuarantinedTools() {
				if quarantined.MCPName == mcpName && quarantined.ToolName == strings.TrimSpace(toolName) {
					payload["schema_hash"] = quarantined.SchemaHash
					break
				}
			}
			m.emitLifecycleEvent(ctx, "mcp.tool.quarantined", mcpName, payload)
			continue
		}
		if tool != nil && !m.toolEnabledFromConfig(mcpName, tool.Name) {
			if cfgErr := m.registry.SetToolUserEnabled(mcpName, tool.Name, false); cfgErr != nil {
				m.emitLifecycleEvent(ctx, "mcp.tool.config_apply_failed", mcpName, map[string]interface{}{
					"tool_name": tool.Name,
					"error":     cfgErr.Error(),
				})
			}
		}
		loadedCount++
	}
	if !m.isGenerationCurrent(gen) {
		return
	}
	m.emitLifecycleEvent(ctx, "mcp.tools.loaded", mcpName, map[string]interface{}{
		"tool_count":        loadedCount,
		"advertised_count":  len(tools),
		"quarantined_count": quarantinedCount,
	})
}

// toolEnabledFromConfig 返回配置中的工具启用意图（缺省 true）。
func (m *manager) toolEnabledFromConfig(mcpName, toolName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cfg == nil {
		return true
	}
	mcpCfg, ok := m.cfg.MCPServers[mcpName]
	if !ok {
		return true
	}
	return mcpCfg.IsToolEnabled(toolName)
}

// Stop 停止所有 MCPs
func (m *manager) Stop() error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	clients := m.clients
	cancel := m.startCancel
	done := m.connDone
	pending := m.pending
	m.clients = make(map[string]client.Client)
	m.pending = make(map[string]client.Client)
	m.lastStderr = make(map[string]string)
	m.registry.Clear()
	m.started = false
	m.generation++
	m.startCancel = nil
	m.connDone = nil
	m.connecting = make(map[string]struct{})
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// 主动关闭在建客户端，让慢握手立即中断（SDK 自身取消可能滞后）。
	for _, cli := range pending {
		_ = cli.Close()
	}
	m.stopHealthChecker()
	// 有界等待后台建连退出；迟到客户端由 generation 守卫丢弃并自行关闭。
	m.waitForConnects(done)

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

// ListQuarantinedTools returns invalid definitions retained for diagnostics.
func (m *manager) ListQuarantinedTools() []registry.QuarantinedToolInfo {
	return m.registry.ListQuarantinedTools()
}

// CallTool 调用工具
func (m *manager) CallTool(ctx context.Context, mcpName, toolName string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	var (
		info *registry.ToolInfo
		err  error
	)
	if strings.TrimSpace(mcpName) == "" {
		info, err = m.registry.ResolveTool(toolName)
	} else {
		info, err = m.registry.ResolveToolForMCP(mcpName, toolName)
	}
	if err == nil {
		mcpName = info.MCPName
		toolName = info.Tool.Name
	} else {
		if strings.TrimSpace(mcpName) == "" || strings.HasPrefix(strings.TrimSpace(toolName), "mcp__") {
			return nil, err
		}
		if _, registeredErr := m.registry.GetTool(mcpName, toolName); registeredErr == nil {
			return nil, err
		}
	}

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
	return m.registry.ResolveTool(toolName)
}

// ListAllToolsForMCP 列出某个 MCP 的全部已注册工具（含被禁用者）。
func (m *manager) ListAllToolsForMCP(mcpName string) []*registry.ToolInfo {
	return m.registry.ListAllToolsForMCP(mcpName)
}

// SetToolEnabled 启停单个工具（用户意图）：只翻转注册表标志并同步内存配置，
// 不触发 MCP 重连。
func (m *manager) SetToolEnabled(mcpName, toolName string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	mcpName = strings.TrimSpace(mcpName)
	toolName = strings.TrimSpace(toolName)
	if mcpName == "" || toolName == "" {
		return fmt.Errorf("MCP 名称与工具名不能为空")
	}
	if err := m.registry.SetToolUserEnabled(mcpName, toolName, enabled); err != nil {
		return err
	}
	if m.cfg != nil {
		if mcpCfg, ok := m.cfg.MCPServers[mcpName]; ok {
			mcpCfg.SetToolEnabled(toolName, enabled)
			m.cfg.MCPServers[mcpName] = mcpCfg
		}
	}
	m.emitLifecycleEvent(context.Background(), "mcp.tool.state_changed", mcpName, map[string]interface{}{
		"tool_name": toolName,
		"enabled":   enabled,
	})
	return nil
}

// SetToolsEnabled 批量启停工具；tools 为空表示该 MCP 下全部工具。
func (m *manager) SetToolsEnabled(mcpName string, tools []string, enabled bool) error {
	names := make([]string, 0, len(tools))
	for _, toolName := range tools {
		if name := strings.TrimSpace(toolName); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		for _, info := range m.registry.ListAllToolsForMCP(strings.TrimSpace(mcpName)) {
			if info == nil || info.Tool == nil {
				continue
			}
			if name := strings.TrimSpace(info.Tool.Name); name != "" {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("MCP %s 没有可操作的工具", strings.TrimSpace(mcpName))
	}
	for _, name := range names {
		if err := m.SetToolEnabled(mcpName, name, enabled); err != nil {
			return err
		}
	}
	return nil
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
	return m.mcpStatusLocked(name)
}

// mcpStatusLocked 组装单个 MCP 状态；调用方需持有 m.mu 读锁。
func (m *manager) mcpStatusLocked(name string) (*config.MCPStatus, error) {
	if m.cfg == nil {
		return nil, fmt.Errorf("配置未加载")
	}

	mcpCfg, ok := m.cfg.MCPServers[name]
	if !ok {
		return nil, fmt.Errorf("MCP 不存在: %s", name)
	}

	status := &config.MCPStatus{
		Name:             name,
		Type:             mcpCfg.Type,
		TrustLevel:       mcpCfg.ResolvedTrustLevel(),
		ExecutionMode:    mcpCfg.ExecutionMode(),
		MaxParallelCalls: mcpCfg.MaxParallelCalls,
		Enabled:          mcpCfg.IsEnabled(),
	}
	if origin, ok := m.origins[name]; ok && strings.TrimSpace(origin.Path) != "" {
		status.ConfigSource = origin.Source
		status.ConfigPath = origin.Path
		status.ShadowedSources = append([]config.SourceRef(nil), origin.Shadowed...)
	}
	if runtimeStatus, ok := m.status[name]; ok && runtimeStatus != nil {
		status.LastError = runtimeStatus.LastError
		status.RequiresAuth = runtimeStatus.RequiresAuth
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

	statuses := make([]*config.MCPStatus, 0, len(m.cfg.MCPServers))
	for name := range m.cfg.MCPServers {
		status, _ := m.mcpStatusLocked(name)
		statuses = append(statuses, status)
	}

	return statuses
}

// ReloadConfig 重新加载配置
func (m *manager) ReloadConfig() error {
	m.mu.Lock()
	clients := m.clients
	cancel := m.startCancel
	done := m.connDone
	pending := m.pending
	m.clients = make(map[string]client.Client)
	m.pending = make(map[string]client.Client)
	m.lastStderr = make(map[string]string)
	m.registry.Clear()
	wasStarted := m.started
	m.started = false
	m.generation++
	m.startCancel = nil
	m.connDone = nil
	m.connecting = make(map[string]struct{})
	m.status = make(map[string]*config.MCPStatus)
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// 主动关闭在建客户端，让慢握手立即中断（SDK 自身取消可能滞后）。
	for _, cli := range pending {
		_ = cli.Close()
	}
	m.waitForConnects(done)
	if wasStarted {
		m.stopHealthChecker()
		for _, cli := range clients {
			cli.Close()
		}
	}

	// 重新加载配置
	if m.layered {
		result, err := config.LoadEffective(m.explicitPath)
		if err != nil {
			return err
		}
		config.ExpandEnv(result.Config)
		m.mu.Lock()
		m.cfg = result.Config
		m.origins = result.Origins
		m.originWarnings = append([]string(nil), result.Warnings...)
		m.mu.Unlock()
	} else if m.loader != nil {
		cfg, err := m.loader.Load()
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.cfg = cfg
		m.recordSingleFileOrigins(cfg, m.explicitPath)
		m.mu.Unlock()
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
		status.MaxParallelCalls = mcpCfg.MaxParallelCalls
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
	m.mu.RLock()
	globalTimeout := time.Duration(0)
	if m.cfg != nil {
		globalTimeout = m.cfg.Global.ConnectTimeout.Duration
	}
	m.mu.RUnlock()
	if globalTimeout > 0 {
		return globalTimeout
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
	// 首轮建连仍在进行时不重复触发健康检查/重连。
	if m.isConnecting(name) {
		return
	}
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
	if m.isConnecting(name) {
		return
	}
	gen := m.currentGeneration()

	reconnectCtx, cancel := context.WithTimeout(context.Background(), m.resolveConnectTimeout(mcpCfg))
	defer cancel()
	m.emitLifecycleEvent(reconnectCtx, "mcp.reconnect.started", name, map[string]interface{}{
		"execution_mode": mcpCfg.ExecutionMode(),
		"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
	})

	cli, err := m.createClient(name, mcpCfg)
	if err != nil {
		m.recordReconnectFailure(reconnectCtx, name, mcpCfg, err)
		return
	}
	if !m.trackPending(gen, name, cli) {
		_ = cli.Close()
		return
	}
	connectErr := m.connectClient(reconnectCtx, cli, mcpCfg)
	m.untrackPending(name, cli)
	if connectErr != nil {
		_ = cli.Close()
		m.recordReconnectFailure(reconnectCtx, name, mcpCfg, connectErr)
		return
	}

	m.mu.Lock()
	if !m.isGenerationCurrentLocked(gen) {
		m.mu.Unlock()
		_ = cli.Close()
		return
	}
	if oldCli, ok := m.clients[name]; ok {
		_ = oldCli.Close()
		m.registry.UnregisterClient(name)
	}
	m.clients[name] = cli
	m.registry.RegisterClient(name, cli)
	status := m.ensureStatusLocked(name, mcpCfg)
	status.LastConnect = time.Now()
	status.LastError = ""
	status.RequiresAuth = false
	m.mu.Unlock()
	m.emitLifecycleEvent(reconnectCtx, "mcp.reconnected", name, map[string]interface{}{
		"execution_mode": mcpCfg.ExecutionMode(),
		"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
	})

	m.loadTools(reconnectCtx, cli, name, gen)
}

func (m *manager) recordReconnectFailure(ctx context.Context, name string, mcpCfg *config.MCPConfig, err error) {
	m.setStatus(name, mcpCfg, func(status *config.MCPStatus) {
		status.LastError = err.Error()
	})
	m.emitLifecycleEvent(ctx, "mcp.reconnect.failed", name, map[string]interface{}{
		"error":          err.Error(),
		"execution_mode": mcpCfg.ExecutionMode(),
		"trust_level":    string(mcpCfg.ResolvedTrustLevel()),
	})
}

func (m *manager) getClient(name string) client.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[name]
}

// StderrDiagnostics 返回指定 MCP 最近一次连接尝试的 stdio 子进程诊断（无则空串）。
//
// 实现 StderrDiagnosticsProvider（可选能力，不进入 Manager 接口，避免测试替身
// 连锁改动）：优先读在线客户端，失败时回退到建连失败阶段留存的快照。
func (m *manager) StderrDiagnostics(name string) string {
	if m == nil {
		return ""
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if cli := m.getClient(name); cli != nil {
		if diag := client.StderrDiagnosticsOf(cli); diag != "" {
			return diag
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastStderr[name]
}

// rememberStderrDiagnostics 留存建连失败时的 stdio 诊断：客户端马上会被 Close，
// 之后无法再从它读取。诊断为空时清除旧值，避免展示过期内容。
func (m *manager) rememberStderrDiagnostics(name string, cli client.Client) {
	if m == nil {
		return
	}
	diag := client.StderrDiagnosticsOf(cli)
	m.mu.Lock()
	defer m.mu.Unlock()
	if diag == "" {
		delete(m.lastStderr, name)
		return
	}
	if m.lastStderr == nil {
		m.lastStderr = make(map[string]string)
	}
	m.lastStderr[name] = diag
}

// clearStderrDiagnostics 在建连成功后清除历史失败诊断。
func (m *manager) clearStderrDiagnostics(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.lastStderr, name)
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
