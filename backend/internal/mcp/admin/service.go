package admin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

// Option 配置 Service。
type Option func(*Service)

// WithManager 注入已存在的 manager（如 runtime-server 启动时创建的管理器）。
func WithManager(mgr manager.Manager) Option {
	return func(s *Service) {
		if mgr != nil {
			s.manager = mgr
		}
	}
}

// WithManagerFactory 覆盖惰性创建 manager 的工厂（默认 manager.NewManager）。
func WithManagerFactory(factory func() manager.Manager) Option {
	return func(s *Service) {
		if factory != nil {
			s.newManager = factory
		}
	}
}

// WithRefresh 注册变更后的刷新回调（如 runtime tool catalog refresh）。
func WithRefresh(refresh func()) Option {
	return func(s *Service) {
		s.refresh = refresh
	}
}

// WithApplyOnMutate 控制写操作后是否立即热重载并重连（默认 true）。
//
// 一次性 CLI 关闭该开关时只做持久化，由调用方自行决定是否探测/重连。
func WithApplyOnMutate(apply bool) Option {
	return func(s *Service) {
		s.applyOnMutate = apply
	}
}

// Item MCP 管理视图：静态配置 + 运行时状态。
type Item struct {
	Config config.MCPConfig  `json:"config"`
	Status *config.MCPStatus `json:"status,omitempty"`
}

// AdminService MCP 管理能力接口（供 HTTP handler 等解耦依赖，便于测试替身）。
type AdminService interface {
	List(ctx context.Context) ([]Item, error)
	Get(ctx context.Context, name string) (*config.MCPConfig, error)
	Add(ctx context.Context, req UpsertRequest) (*config.MCPConfig, error)
	Update(ctx context.Context, name string, req UpsertRequest) (*config.MCPConfig, error)
	Remove(ctx context.Context, name string) error
	SetEnabled(ctx context.Context, name string, enabled bool) (*config.MCPConfig, error)
	Reload(ctx context.Context) error
}

// Service MCP 配置管理服务（CLI / HTTP API / 微型 Web 客户端共用）。
type Service struct {
	mu         sync.Mutex
	configPath string
	manager    manager.Manager
	newManager func() manager.Manager
	refresh    func()

	applyOnMutate bool
	diagnostics   *ConfigDiagnostics
}

var _ AdminService = (*Service)(nil)

// NewService 创建管理服务。
func NewService(configPath string, opts ...Option) *Service {
	service := &Service{
		configPath:    strings.TrimSpace(configPath),
		newManager:    manager.NewManager,
		applyOnMutate: true,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// ConfigPath 返回管理的配置文件路径。
func (s *Service) ConfigPath() string {
	if s == nil {
		return ""
	}
	return s.configPath
}

// List 列出全部 MCP（配置 + 状态），按名称排序。
func (s *Service) List(ctx context.Context) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	mgr, err := s.ensureManagerLocked()
	if err != nil {
		return nil, err
	}
	cfg, err := LoadFile(s.configPath)
	if err != nil {
		return nil, err
	}

	statuses := map[string]*config.MCPStatus{}
	for _, status := range mgr.ListMCPs() {
		if status != nil {
			statuses[status.Name] = status
		}
	}

	names := make([]string, 0, len(cfg.MCPServers))
	for name := range cfg.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]Item, 0, len(names))
	for _, name := range names {
		mcpCfg := cfg.MCPServers[name]
		item := Item{Config: mcpCfg}
		if status, ok := statuses[name]; ok {
			item.Status = status
		} else {
			item.Status = &config.MCPStatus{
				Name:          name,
				Type:          mcpCfg.Type,
				TrustLevel:    mcpCfg.ResolvedTrustLevel(),
				ExecutionMode: mcpCfg.ExecutionMode(),
				Enabled:       mcpCfg.IsEnabled(),
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// Get 返回单个 MCP 配置。
func (s *Service) Get(_ context.Context, name string) (*config.MCPConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := LoadFile(s.configPath)
	if err != nil {
		return nil, err
	}
	mcpCfg, ok := cfg.MCPServers[strings.TrimSpace(name)]
	if !ok {
		return nil, notFoundf("MCP '%s' 不存在", name)
	}
	return &mcpCfg, nil
}

// Add 新增 MCP：写配置文件 + 热重载生效。
func (s *Service) Add(ctx context.Context, req UpsertRequest) (*config.MCPConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := LoadFile(s.configPath)
	if err != nil {
		return nil, err
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]config.MCPConfig)
	}
	name := strings.TrimSpace(req.Name)
	if _, exists := cfg.MCPServers[name]; exists {
		return nil, invalidf("MCP '%s' 已存在", name)
	}

	mcpCfg, err := BuildConfig(req, nil)
	if err != nil {
		return nil, err
	}
	cfg.MCPServers[name] = mcpCfg
	if err := SaveFile(s.configPath, cfg); err != nil {
		return nil, err
	}
	if !s.applyOnMutate {
		return &mcpCfg, nil
	}
	if err := s.applyLocked(ctx); err != nil {
		return nil, err
	}
	return &mcpCfg, nil
}

// Update 更新 MCP：写配置文件 + 热重载生效。
func (s *Service) Update(ctx context.Context, name string, req UpsertRequest) (*config.MCPConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := LoadFile(s.configPath)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	existing, ok := cfg.MCPServers[name]
	if !ok {
		return nil, notFoundf("MCP '%s' 不存在", name)
	}

	req.Name = name
	mcpCfg, err := BuildConfig(req, &existing)
	if err != nil {
		return nil, err
	}
	cfg.MCPServers[name] = mcpCfg
	if err := SaveFile(s.configPath, cfg); err != nil {
		return nil, err
	}
	if !s.applyOnMutate {
		return &mcpCfg, nil
	}
	if err := s.applyLocked(ctx); err != nil {
		return nil, err
	}
	return &mcpCfg, nil
}

// Remove 删除 MCP：写配置文件 + 热重载生效。
func (s *Service) Remove(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := LoadFile(s.configPath)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if _, ok := cfg.MCPServers[name]; !ok {
		return notFoundf("MCP '%s' 不存在", name)
	}
	delete(cfg.MCPServers, name)
	if err := SaveFile(s.configPath, cfg); err != nil {
		return err
	}
	if !s.applyOnMutate {
		return nil
	}
	return s.applyLocked(ctx)
}

// SetEnabled 启用/停用 MCP：持久化到配置文件 + 热重载生效。
func (s *Service) SetEnabled(ctx context.Context, name string, enabled bool) (*config.MCPConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := LoadFile(s.configPath)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	mcpCfg, ok := cfg.MCPServers[name]
	if !ok {
		return nil, notFoundf("MCP '%s' 不存在", name)
	}
	mcpCfg.Enabled = enabled
	mcpCfg.Disabled = !enabled
	cfg.MCPServers[name] = mcpCfg
	if err := SaveFile(s.configPath, cfg); err != nil {
		return nil, err
	}
	if !s.applyOnMutate {
		return &mcpCfg, nil
	}
	if err := s.applyLocked(ctx); err != nil {
		return nil, err
	}
	return &mcpCfg, nil
}

// Reload 重新读取配置文件并重连全部 MCP。
func (s *Service) Reload(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ctx)
}

// SetToolEnabled 启停单个工具：写回配置文件 + 运行时生效（不重连 MCP 服务）。
func (s *Service) SetToolEnabled(ctx context.Context, name, tool string, enabled bool) (*config.MCPConfig, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return nil, invalidf("工具名不能为空")
	}
	return s.setToolsEnabled(ctx, name, []string{tool}, enabled)
}

// SetToolsEnabled 批量启停工具；tools 为空表示该 MCP 下全部工具。
func (s *Service) SetToolsEnabled(ctx context.Context, name string, tools []string, enabled bool) (*config.MCPConfig, error) {
	normalized := make([]string, 0, len(tools))
	for _, tool := range tools {
		if key := strings.TrimSpace(tool); key != "" {
			normalized = append(normalized, key)
		}
	}
	return s.setToolsEnabled(ctx, name, normalized, enabled)
}

// setToolsEnabled 工具级启停共用的写回 + 应用路径（调用方无需持有 s.mu）。
func (s *Service) setToolsEnabled(ctx context.Context, name string, tools []string, enabled bool) (*config.MCPConfig, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mgr, err := s.ensureManagerLocked()
	if err != nil {
		return nil, err
	}
	cfg, err := LoadFile(s.configPath)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	mcpCfg, ok := cfg.MCPServers[name]
	if !ok {
		return nil, notFoundf("MCP '%s' 不存在", name)
	}

	resolved := append([]string(nil), tools...)
	if len(resolved) == 0 {
		resolved = append(resolved, listAllToolNames(mgr, name)...)
	}
	if len(resolved) == 0 {
		return nil, invalidf("MCP '%s' 没有可操作的工具", name)
	}
	for _, toolName := range resolved {
		mcpCfg.SetToolEnabled(toolName, enabled)
	}
	cfg.MCPServers[name] = mcpCfg
	if err := SaveFile(s.configPath, cfg); err != nil {
		return nil, err
	}
	if s.applyOnMutate {
		if toggler, ok := mgr.(interface {
			SetToolsEnabled(string, []string, bool) error
		}); ok {
			if err := toggler.SetToolsEnabled(name, resolved, enabled); err != nil {
				return nil, fmt.Errorf("应用工具启停失败: %w", err)
			}
		} else if err := s.applyLocked(ctx); err != nil {
			return nil, err
		}
		if s.refresh != nil {
			s.refresh()
		}
	}
	out := mcpCfg
	return &out, nil
}

// listAllToolNames 返回某个 MCP 的全部工具名；管理器支持全量清单时包含被禁用工具。
func listAllToolNames(mgr manager.Manager, name string) []string {
	names := make([]string, 0)
	appendInfo := func(info *registry.ToolInfo) {
		if info == nil || info.Tool == nil {
			return
		}
		if toolName := strings.TrimSpace(info.Tool.Name); toolName != "" {
			names = append(names, toolName)
		}
	}
	if lister, ok := mgr.(interface {
		ListAllToolsForMCP(string) []*registry.ToolInfo
	}); ok {
		for _, info := range lister.ListAllToolsForMCP(name) {
			appendInfo(info)
		}
		return names
	}
	for _, info := range mgr.ListTools() {
		if info == nil || info.MCPName != name {
			continue
		}
		appendInfo(info)
	}
	return names
}

// applyLocked 重新加载配置并重连（调用方需持有 s.mu）。
func (s *Service) applyLocked(ctx context.Context) error {
	mgr, err := s.ensureManagerLocked()
	if err != nil {
		return err
	}
	if err := mgr.ReloadConfig(); err != nil {
		return fmt.Errorf("重新加载 MCP 配置失败: %w", err)
	}
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("重连 MCP 失败: %w", err)
	}
	if s.refresh != nil {
		s.refresh()
	}
	return nil
}

// ensureManagerLocked 返回可用的 manager；未注入时按配置文件惰性创建（调用方需持有 s.mu）。
func (s *Service) ensureManagerLocked() (manager.Manager, error) {
	if s.manager != nil {
		return s.manager, nil
	}
	if strings.TrimSpace(s.configPath) == "" {
		return nil, fmt.Errorf("MCP 配置文件路径为空")
	}
	if err := EnsureFile(s.configPath); err != nil {
		return nil, err
	}
	mgr := s.newManager()
	if mgr == nil {
		return nil, fmt.Errorf("MCP manager 创建失败")
	}
	if err := mgr.LoadConfig(s.configPath); err != nil {
		return nil, fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	s.manager = mgr
	return mgr, nil
}
