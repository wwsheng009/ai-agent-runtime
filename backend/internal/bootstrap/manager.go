package bootstrap

import (
	"fmt"
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/embedding"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const defaultGatewayProviderName = "gateway"

// Options Runtime Bootstrap 选项
type Options struct {
	Config              *runtimecfg.RuntimeConfig
	SkillDir            string
	SkillDirs           []string
	DiscoverOnly        bool
	MCPManager          skill.MCPManager
	ResourceManager     llm.ResourceManager
	GatewayProviderName string
	ProviderConfigs     map[string]*llm.ProviderConfig
}

// SkillsHandlerTarget 是 skills handler 所需的最小接线接口
type SkillsHandlerTarget interface {
	SetLLMRuntime(runtime *llm.LLMRuntime)
	SetSessionManager(manager *chat.SessionManager)
	SetHotReload(hotReload *skill.HotReload)
	SetEmbeddingRouter(router *skill.SemanticEmbeddingRouter)
}

// Manager 统一管理 Skills Runtime 相关组件的初始化与接线
type Manager struct {
	config          *runtimecfg.RuntimeConfig
	skillDir        string
	skillDirs       []string
	mcpManager      skill.MCPManager
	resourceManager llm.ResourceManager
	providerConfigs map[string]*llm.ProviderConfig

	llmRuntime      *llm.LLMRuntime
	registry        *skill.Registry
	loader          *skill.Loader
	sessionManager  *chat.SessionManager
	hotReload       *skill.HotReload
	embeddingRouter *skill.SemanticEmbeddingRouter
	teamStore       team.Store
}

// NewManager 创建 Runtime Bootstrap Manager
func NewManager(opts *Options) (*Manager, error) {
	if opts == nil {
		opts = &Options{}
	}

	config := opts.Config
	if config == nil {
		config = runtimecfg.DefaultRuntimeConfig()
	}

	sessionManager, err := newSessionManager(config)
	if err != nil {
		return nil, err
	}

	manager := &Manager{
		config:          config,
		skillDir:        strings.TrimSpace(opts.SkillDir),
		skillDirs:       resolveSkillDirs(opts.SkillDir, opts.SkillDirs),
		mcpManager:      opts.MCPManager,
		resourceManager: opts.ResourceManager,
		providerConfigs: opts.ProviderConfigs,
		registry:        skill.NewRegistry(opts.MCPManager),
		loader:          skill.NewLoader(opts.MCPManager),
		sessionManager:  sessionManager,
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{
		Path: strings.TrimSpace(config.Team.StorePath),
		DSN:  strings.TrimSpace(config.Team.StoreDSN),
	})
	if err != nil {
		return nil, err
	}
	manager.teamStore = teamStore

	if len(manager.skillDirs) > 0 {
		manager.skillDir = manager.skillDirs[0]
		manager.loader.SetSkillDirs(manager.skillDirs)
		if opts.DiscoverOnly {
			if err := manager.loader.DiscoverAllWithRegistry(manager.skillDirs, manager.registry); err != nil {
				return nil, err
			}
		} else {
			if err := manager.loader.LoadAllWithRegistry(manager.skillDirs, manager.registry); err != nil {
				return nil, err
			}
		}
	}

	llmRuntime, err := manager.buildLLMRuntime(opts.GatewayProviderName)
	if err != nil {
		return nil, err
	}
	manager.llmRuntime = llmRuntime

	embeddingRouter, err := manager.buildEmbeddingRouter()
	if err != nil {
		return nil, err
	}
	manager.embeddingRouter = embeddingRouter

	if !opts.DiscoverOnly && config.HotReload.Enabled && len(manager.skillDirs) > 0 {
		hotReload, err := skill.NewHotReload(manager.loader, manager.registry)
		if err != nil {
			return nil, err
		}
		manager.attachHotReloadEmbeddingSync(hotReload)
		if config.HotReload.DebounceDelay > 0 {
			hotReload.SetDebounceTime(config.HotReload.DebounceDelay)
		}
		if err := hotReload.StartMany(manager.skillDirs); err != nil {
			return nil, err
		}
		if err := hotReload.Reload(); err != nil {
			return nil, err
		}
		manager.hotReload = hotReload
	}

	return manager, nil
}

func newSessionManager(config *runtimecfg.RuntimeConfig) (*chat.SessionManager, error) {
	if config != nil {
		if dir := strings.TrimSpace(config.Sessions.Dir); dir != "" {
			storage, err := chat.NewFileStorage(dir)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize session file storage: %w", err)
			}
			return chat.NewSessionManager(storage, nil), nil
		}
	}

	return chat.NewSessionManager(chat.NewInMemoryStorage(), nil), nil
}

func (m *Manager) buildLLMRuntime(gatewayProviderName string) (*llm.LLMRuntime, error) {
	defaultModel := m.config.Agent.DefaultModel
	providerName := gatewayProviderName
	if providerName == "" {
		providerName = defaultGatewayProviderName
	}

	llmConfig := &llm.RuntimeConfig{
		DefaultProvider: m.config.Agent.DefaultProvider,
		DefaultModel:    defaultModel,
		DefaultTimeout:  m.config.Agent.Timeout,
	}
	llmConfig.MaxRetries, llmConfig.RetryTuning, llmConfig.RetryRules = runtimeRetryConfigFromProviderConfigs(m.providerConfigs)

	if m.resourceManager != nil && len(m.providerConfigs) == 0 {
		if strings.TrimSpace(llmConfig.DefaultProvider) == "" {
			llmConfig.DefaultProvider = providerName
		}
	}

	runtime := llm.NewLLMRuntime(llmConfig)
	if m.resourceManager != nil {
		if err := runtime.RegisterGatewayClient(providerName, m.resourceManager, defaultModel); err != nil {
			return nil, err
		}
	}
	if err := m.registerConfiguredProviders(runtime); err != nil {
		return nil, err
	}

	return runtime, nil
}

func (m *Manager) buildEmbeddingRouter() (*skill.SemanticEmbeddingRouter, error) {
	if m.config == nil || !m.config.Router.EnableEmbedding || !m.config.Embedding.Enabled {
		return nil, nil
	}

	vectorIndex, err := embedding.NewVectorIndex(
		embedding.NewLocalEmbeddingGenerator(m.config.Embedding.VectorDim),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding vector index: %w", err)
	}

	router, err := skill.NewSemanticEmbeddingRouter(vectorIndex, m.registry)
	if err != nil {
		return nil, fmt.Errorf("failed to create embedding router: %w", err)
	}

	if threshold := m.resolveEmbeddingThreshold(); threshold > 0 {
		router.SetThreshold(threshold)
	}
	if topK := m.resolveEmbeddingTopK(); topK > 0 {
		router.SetTopK(topK)
	}
	if err := router.IndexSkills(); err != nil {
		return nil, fmt.Errorf("failed to index skills for embedding router: %w", err)
	}

	return router, nil
}

func (m *Manager) resolveEmbeddingThreshold() float32 {
	if m.config == nil {
		return 0
	}
	if m.config.Embedding.Threshold > 0 {
		return m.config.Embedding.Threshold
	}
	return m.config.Router.Threshold
}

func (m *Manager) resolveEmbeddingTopK() int {
	if m.config == nil {
		return 0
	}
	if m.config.Embedding.TopK > 0 {
		return m.config.Embedding.TopK
	}
	return m.config.Router.TopK
}

func (m *Manager) attachHotReloadEmbeddingSync(hotReload *skill.HotReload) {
	if hotReload == nil || m.embeddingRouter == nil {
		return
	}

	hotReload.AddCallback(func(event *skill.ReloadEvent) {
		if event == nil {
			return
		}

		switch event.Type {
		case skill.ReloadEventSkillAdded, skill.ReloadEventSkillUpdated:
			if registeredSkill, ok := m.registry.Get(event.SkillName); ok {
				_ = m.embeddingRouter.IncrementalIndex(registeredSkill)
			}
		case skill.ReloadEventSkillRemoved:
			_ = m.embeddingRouter.RemoveIndex(&skill.Skill{Name: event.SkillName})
		case skill.ReloadEventReloadDone:
			_ = m.embeddingRouter.RebuildIndex()
		}
	})
}

func (m *Manager) registerConfiguredProviders(runtime *llm.LLMRuntime) error {
	for name, providerConfig := range m.providerConfigs {
		if providerConfig == nil {
			continue
		}

		provider, err := llm.NewProvider(providerConfig)
		if err != nil {
			return fmt.Errorf("failed to create provider %s: %w", name, err)
		}
		if err := runtime.RegisterProvider(name, provider); err != nil {
			return fmt.Errorf("failed to register provider %s: %w", name, err)
		}

		aliases := collectProviderAliases(providerConfig, provider)
		if err := runtime.RegisterProviderAliases(name, aliases...); err != nil {
			return fmt.Errorf("failed to register provider aliases for %s: %w", name, err)
		}
	}

	return nil
}

// ReloadProviderConfigs atomically updates the configured provider registrations on the live LLM runtime.
func (m *Manager) ReloadProviderConfigs(providerConfigs map[string]*llm.ProviderConfig) error {
	if m == nil {
		return fmt.Errorf("bootstrap manager is nil")
	}
	if m.llmRuntime == nil {
		return fmt.Errorf("llm runtime is nil")
	}

	nextConfigs := cloneProviderConfigMap(providerConfigs)
	currentNames := make(map[string]struct{}, len(m.providerConfigs))
	for name := range m.providerConfigs {
		currentNames[name] = struct{}{}
	}

	builtProviders := make(map[string]llm.LegacyChatProvider, len(nextConfigs))
	builtAliases := make(map[string][]string, len(nextConfigs))
	for name, providerConfig := range nextConfigs {
		if providerConfig == nil {
			continue
		}

		provider, err := llm.NewProvider(cloneProviderConfig(providerConfig))
		if err != nil {
			return fmt.Errorf("failed to create provider %s: %w", name, err)
		}
		builtProviders[name] = provider
		builtAliases[name] = collectProviderAliases(providerConfig, provider)
	}

	for name := range currentNames {
		if _, exists := nextConfigs[name]; exists {
			continue
		}
		m.llmRuntime.UnregisterProvider(name)
	}

	for name, provider := range builtProviders {
		if err := m.llmRuntime.ReplaceProviderRegistration(name, provider, builtAliases[name]...); err != nil {
			return fmt.Errorf("failed to replace provider %s: %w", name, err)
		}
	}

	m.providerConfigs = nextConfigs
	maxRetries, retryTuning, retryRules := runtimeRetryConfigFromProviderConfigs(nextConfigs)
	m.llmRuntime.UpdateRetryConfig(maxRetries, retryTuning, retryRules)
	return nil
}

func runtimeRetryConfigFromProviderConfigs(providerConfigs map[string]*llm.ProviderConfig) (int, llm.RetryTuning, []llm.RetryRule) {
	if len(providerConfigs) == 0 {
		return 3, llm.RetryTuning{}, nil
	}

	names := make([]string, 0, len(providerConfigs))
	for name := range providerConfigs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		providerConfig := providerConfigs[name]
		if providerConfig == nil {
			continue
		}
		maxRetries := providerConfig.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}
		return maxRetries, providerConfig.RetryTuning, cloneRetryRules(providerConfig.RetryRules)
	}
	return 3, llm.RetryTuning{}, nil
}

func collectProviderAliases(providerConfig *llm.ProviderConfig, provider llm.ModelCatalogProvider) []string {
	aliasSet := make(map[string]struct{})
	addAlias := func(alias string) {
		if alias == "" {
			return
		}
		aliasSet[alias] = struct{}{}
	}

	addAlias(providerConfig.DefaultModel)
	for _, model := range providerConfig.SupportedModels {
		addAlias(model)
	}
	for sourceModel, targetModel := range providerConfig.ModelMappings {
		addAlias(sourceModel)
		addAlias(targetModel)
	}
	for _, model := range provider.SupportedModels() {
		addAlias(model)
	}

	aliases := make([]string, 0, len(aliasSet))
	for alias := range aliasSet {
		aliases = append(aliases, alias)
	}
	return aliases
}

func cloneProviderConfigMap(input map[string]*llm.ProviderConfig) map[string]*llm.ProviderConfig {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]*llm.ProviderConfig, len(input))
	for key, value := range input {
		output[key] = cloneProviderConfig(value)
	}
	return output
}

func cloneProviderConfig(input *llm.ProviderConfig) *llm.ProviderConfig {
	if input == nil {
		return nil
	}
	cloned := *input
	if len(input.SupportedModels) > 0 {
		cloned.SupportedModels = append([]string(nil), input.SupportedModels...)
	}
	if len(input.ModelMappings) > 0 {
		cloned.ModelMappings = cloneStringMap(input.ModelMappings)
	}
	if len(input.ModelCapabilities) > 0 {
		cloned.ModelCapabilities = cloneRuntimeModelCapabilities(input.ModelCapabilities)
	}
	if len(input.Headers) > 0 {
		cloned.Headers = cloneStringMap(input.Headers)
	}
	if len(input.HeaderMappings) > 0 {
		cloned.HeaderMappings = cloneStringMap(input.HeaderMappings)
	}
	if len(input.HeaderMappingRules) > 0 {
		cloned.HeaderMappingRules = cloneHeaderMappingRules(input.HeaderMappingRules)
	}
	if len(input.RetryRules) > 0 {
		cloned.RetryRules = cloneRetryRules(input.RetryRules)
	}
	if input.Proxy != nil {
		cloned.Proxy = input.Proxy.Clone()
	}
	return &cloned
}

func cloneRetryRules(input []llm.RetryRule) []llm.RetryRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]llm.RetryRule, len(input))
	for i, rule := range input {
		cloned := rule
		cloned.Keyword.Values = append([]string(nil), rule.Keyword.Values...)
		cloned.Keyword.Patterns = append([]string(nil), rule.Keyword.Patterns...)
		cloned.ErrorCode.Codes = append([]string(nil), rule.ErrorCode.Codes...)
		cloned.StatusCode.Codes = append([]int(nil), rule.StatusCode.Codes...)
		output[i] = cloned
	}
	return output
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneHeaderMappingRules(input []llm.HeaderMappingRule) []llm.HeaderMappingRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]llm.HeaderMappingRule, len(input))
	copy(output, input)
	return output
}

func cloneRuntimeModelCapabilities(input map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]agentconfig.ModelCapabilitySpec, len(input))
	for key, value := range input {
		cloned := value
		if len(value.InputModalities) > 0 {
			cloned.InputModalities = append([]string(nil), value.InputModalities...)
		}
		output[key] = cloned
	}
	return output
}

// ApplyToSkillsHandler 将组件接线到 Skills Handler
func (m *Manager) ApplyToSkillsHandler(target SkillsHandlerTarget) {
	if target == nil {
		return
	}
	target.SetLLMRuntime(m.llmRuntime)
	target.SetSessionManager(m.sessionManager)
	if m.hotReload != nil {
		target.SetHotReload(m.hotReload)
	}
	if m.embeddingRouter != nil {
		target.SetEmbeddingRouter(m.embeddingRouter)
	}
	if m.teamStore != nil {
		if setter, ok := target.(interface{ SetTeamStore(team.Store) }); ok {
			setter.SetTeamStore(m.teamStore)
		}
		root := ""
		if m.config != nil {
			root = strings.TrimSpace(m.config.Workspace.Root)
		}
		claims := team.NewPathClaimManager(m.teamStore, root)
		if setter, ok := target.(interface{ SetTeamClaimsManager(*team.PathClaimManager) }); ok {
			setter.SetTeamClaimsManager(claims)
		}
		if setter, ok := target.(interface{ SetTeamOrchestrator(*team.Orchestrator) }); ok {
			setter.SetTeamOrchestrator(team.NewOrchestrator(m.teamStore, claims, nil))
		}
	}
}

// Stop 停止由 bootstrap manager 管理的后台组件
func (m *Manager) Stop() error {
	var stopErr error
	if m.hotReload != nil {
		if err := m.hotReload.Stop(); err != nil {
			stopErr = err
		}
	}
	if m.sessionManager != nil {
		m.sessionManager.Stop()
	}
	if m.teamStore != nil {
		if err := m.teamStore.Close(); err != nil && stopErr == nil {
			stopErr = err
		}
	}
	return stopErr
}

// Config 返回当前配置
func (m *Manager) Config() *runtimecfg.RuntimeConfig { return m.config }

// SkillDir 返回技能目录
func (m *Manager) SkillDir() string { return m.skillDir }

// SkillDirs 返回技能目录列表
func (m *Manager) SkillDirs() []string { return append([]string(nil), m.skillDirs...) }

// LLMRuntime 返回 LLM Runtime
func (m *Manager) LLMRuntime() *llm.LLMRuntime { return m.llmRuntime }

// Registry 返回 Skill Registry
func (m *Manager) Registry() *skill.Registry { return m.registry }

// Loader 返回 Skill Loader
func (m *Manager) Loader() *skill.Loader { return m.loader }

// SessionManager 返回 Session Manager
func (m *Manager) SessionManager() *chat.SessionManager { return m.sessionManager }

// HotReload 返回 HotReload 组件
func (m *Manager) HotReload() *skill.HotReload { return m.hotReload }

// EmbeddingRouter 返回 Embedding 路由器
func (m *Manager) EmbeddingRouter() *skill.SemanticEmbeddingRouter { return m.embeddingRouter }

// ResourceManager 返回 ResourceManager
func (m *Manager) ResourceManager() llm.ResourceManager { return m.resourceManager }

// TeamStore 返回 Team store。
func (m *Manager) TeamStore() team.Store { return m.teamStore }

// Validate 检查 Manager 关键组件是否已初始化
func (m *Manager) Validate() error {
	if m.registry == nil {
		return fmt.Errorf("registry is nil")
	}
	if m.loader == nil {
		return fmt.Errorf("loader is nil")
	}
	if m.llmRuntime == nil {
		return fmt.Errorf("llm runtime is nil")
	}
	if m.sessionManager == nil {
		return fmt.Errorf("session manager is nil")
	}
	return nil
}

func resolveSkillDirs(primary string, extras []string) []string {
	seen := make(map[string]struct{})
	resolved := make([]string, 0, 1+len(extras))

	addDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if _, exists := seen[dir]; exists {
			return
		}
		seen[dir] = struct{}{}
		resolved = append(resolved, dir)
	}

	addDir(primary)
	for _, dir := range extras {
		addDir(dir)
	}

	return resolved
}
