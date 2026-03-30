package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/spf13/pflag"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func main() {
	loadEnv()

	var configPath string
	var listenAddr string

	flags := pflag.NewFlagSet("runtime-server", pflag.ExitOnError)
	flags.StringVarP(&configPath, "config", "c", "./configs/config.yaml", "配置文件路径")
	flags.StringVar(&listenAddr, "listen", "", "监听地址，优先级高于配置文件，例如 127.0.0.1:8081")
	_ = flags.Parse(os.Args[1:])

	cfg, err := config.InitGlobalConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if err := logger.InitLogger(&cfg.Log); err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := newRuntimeServerApp(ctx, cfg, configPath)
	if err != nil {
		logger.Error("Failed to initialize runtime server", logger.Err(err))
		os.Exit(1)
	}
	defer app.close()

	addr := resolveListenAddr(cfg, listenAddr)
	server := &http.Server{
		Addr:              addr,
		Handler:           app.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Warn("Runtime server shutdown returned error", logger.Err(err))
		}
	}()

	logger.Info("AI agent runtime HTTP server started",
		logger.String("listen", addr),
		logger.String("config_file", strings.TrimSpace(configPath)),
		logger.String("runtime_config_file", strings.TrimSpace(app.runtimeManager.GetFilePath())),
		logger.String("skill_dir", strings.TrimSpace(app.skillsCfg.SkillDir)),
		logger.Int("extra_skill_dir_count", len(app.skillsCfg.ExtraSkillDirs)),
	)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("Runtime server exited with error", logger.Err(err))
		os.Exit(1)
	}
}

type runtimeServerApp struct {
	router         *mux.Router
	cfg            *config.Config
	skillsCfg      *config.SkillsRuntimeConfig
	runtimeManager *runtimecfg.RuntimeManager
	bootstrap      *runtimebootstrap.Manager
	mcpManager     mcpmanager.Manager
	ledgerStore    io.Closer
}

func newRuntimeServerApp(ctx context.Context, cfg *config.Config, configPath string) (*runtimeServerApp, error) {
	skillsCfg := normalizeSkillsRuntimeConfig(cfg)
	runtimeManager := runtimecfg.NewRuntimeManager(skillsCfg.ConfigFile)
	if err := runtimeManager.Load(); err != nil {
		return nil, fmt.Errorf("failed to load runtime config: %w", err)
	}

	mcpAdapter, manager, err := buildSkillsMCPManager(ctx, cfg)
	if err != nil {
		return nil, err
	}

	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config:              runtimeManager.Get(),
		SkillDir:            skillsCfg.SkillDir,
		SkillDirs:           resolvedExtraSkillDirs(skillsCfg),
		DiscoverOnly:        true,
		MCPManager:          mcpAdapter,
		GatewayProviderName: strings.TrimSpace(skillsCfg.GatewayProviderName),
		ProviderConfigs:     buildSkillsProviderConfigs(cfg),
	})
	if err != nil {
		if manager != nil {
			_ = manager.Stop()
		}
		return nil, fmt.Errorf("failed to initialize runtime bootstrap: %w", err)
	}
	if err := bootstrapManager.Validate(); err != nil {
		if manager != nil {
			_ = manager.Stop()
		}
		_ = bootstrapManager.Stop()
		return nil, fmt.Errorf("invalid runtime bootstrap: %w", err)
	}

	handler := skillsapi.NewHandler(bootstrapManager.Registry(), bootstrapManager.Loader(), mcpAdapter)
	bootstrapManager.ApplyToSkillsHandler(handler)
	handler.SetRuntimeConfig(runtimeManager.Get(), runtimeManager.GetFilePath())
	handler.SetRuntimeConfigResolver(func(scope skillsapi.UsageScope) *runtimecfg.RuntimeConfig {
		return runtimeManager.SelectConfigForScope(scope.ScopeKey)
	})
	handler.SetProfileSupport(skillsapi.ProfileSupportConfig{
		Registry:          profilesys.NewRegistryFromProfilesConfig(cfg.Profiles),
		DefaultProfile:    defaultProfile(cfg),
		GlobalRuntimePath: strings.TrimSpace(skillsCfg.ConfigFile),
		GlobalMCPPath:     configuredMCPConfigPath(cfg),
		GlobalSkillDirs:   allConfiguredSkillDirs(skillsCfg),
		MCPAutoConnect:    configuredMCPAutoConnect(cfg),
	})
	if persister := newSkillsRuntimePolicyPersister(configPath, cfg); persister != nil {
		handler.SetAuthPolicyPersister(persister.persistAuthPolicy)
		handler.SetUsagePolicyPersister(persister.persistUsagePolicy)
		handler.SetMutationPolicyPersister(persister.persistMutationPolicy)
	}
	applySkillsRuntimePolicies(handler, skillsCfg)
	ledgerStore, err := buildUsageLedgerStore(cfg)
	if err != nil {
		if manager != nil {
			_ = manager.Stop()
		}
		_ = bootstrapManager.Stop()
		return nil, err
	}
	if ledgerStore != nil {
		handler.SetUsageLedgerStore(ledgerStore)
	}

	router := mux.NewRouter()
	router.HandleFunc("/", runtimeInfoHandler).Methods(http.MethodGet)
	router.HandleFunc("/healthz", runtimeInfoHandler).Methods(http.MethodGet)
	handler.RegisterRoutes(router)

	return &runtimeServerApp{
		router:         router,
		cfg:            cfg,
		skillsCfg:      skillsCfg,
		runtimeManager: runtimeManager,
		bootstrap:      bootstrapManager,
		mcpManager:     manager,
		ledgerStore:    ledgerStore,
	}, nil
}

func (a *runtimeServerApp) close() {
	if a == nil {
		return
	}
	if a.bootstrap != nil {
		if err := a.bootstrap.Stop(); err != nil {
			logger.Warn("Failed to stop runtime bootstrap", logger.Err(err))
		}
	}
	if a.mcpManager != nil {
		if err := a.mcpManager.Stop(); err != nil {
			logger.Warn("Failed to stop MCP manager", logger.Err(err))
		}
	}
	if a.ledgerStore != nil {
		if err := a.ledgerStore.Close(); err != nil {
			logger.Warn("Failed to close usage ledger store", logger.Err(err))
		}
	}
}

func loadEnv() {
	for _, path := range []string{".env", "./configs/.env"} {
		if err := godotenv.Load(path); err == nil {
			return
		}
	}
}

func normalizeSkillsRuntimeConfig(cfg *config.Config) *config.SkillsRuntimeConfig {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if cfg.SkillsRuntime == nil {
		cfg.SkillsRuntime = &config.SkillsRuntimeConfig{}
	}
	if strings.TrimSpace(cfg.SkillsRuntime.ConfigFile) == "" {
		cfg.SkillsRuntime.ConfigFile = "configs/runtime.yaml"
	}
	if strings.TrimSpace(cfg.SkillsRuntime.SkillDir) == "" {
		cfg.SkillsRuntime.SkillDir = "./docs/skill_runtime/skills"
	}
	if strings.TrimSpace(cfg.SkillsRuntime.GatewayProviderName) == "" {
		cfg.SkillsRuntime.GatewayProviderName = "gateway"
	}
	if cfg.SkillsRuntime.ReindexCooldown <= 0 {
		cfg.SkillsRuntime.ReindexCooldown = 30 * time.Second
	}
	if len(cfg.SkillsRuntime.TenantHeaders) == 0 {
		cfg.SkillsRuntime.TenantHeaders = []string{"X-Skills-Tenant", "X-Skills-Auth-Tenant", "X-Tenant-ID", "X-Authenticated-Tenant"}
	}
	if len(cfg.SkillsRuntime.ProjectHeaders) == 0 {
		cfg.SkillsRuntime.ProjectHeaders = []string{"X-Skills-Project", "X-Skills-Auth-Project", "X-Project-ID", "X-Authenticated-Project"}
	}
	if len(cfg.SkillsRuntime.UserHeaders) == 0 {
		cfg.SkillsRuntime.UserHeaders = []string{"X-Skills-User", "X-Skills-Auth-User", "X-User-ID", "X-Authenticated-User"}
	}
	if len(cfg.SkillsRuntime.RoleHeaders) == 0 {
		cfg.SkillsRuntime.RoleHeaders = []string{"X-Skills-Role", "X-Skills-Auth-Role", "X-Role", "X-Authenticated-Role"}
	}
	if len(cfg.SkillsRuntime.TenantClaims) == 0 {
		cfg.SkillsRuntime.TenantClaims = []string{"tenant_id", "tenant", "tid"}
	}
	if len(cfg.SkillsRuntime.ProjectClaims) == 0 {
		cfg.SkillsRuntime.ProjectClaims = []string{"project_id", "project", "pid"}
	}
	if len(cfg.SkillsRuntime.UserClaims) == 0 {
		cfg.SkillsRuntime.UserClaims = []string{"user_id", "user", "uid", "sub"}
	}
	if len(cfg.SkillsRuntime.RoleClaims) == 0 {
		cfg.SkillsRuntime.RoleClaims = []string{"role", "roles"}
	}
	return cfg.SkillsRuntime
}

func resolveListenAddr(cfg *config.Config, override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}

	host := "127.0.0.1"
	port := 8081
	if cfg != nil {
		if trimmed := strings.TrimSpace(cfg.Server.Host); trimmed != "" {
			host = trimmed
		}
		if cfg.Server.Port > 0 {
			port = cfg.Server.Port
		}
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

func buildSkillsMCPManager(ctx context.Context, cfg *config.Config) (runtimeskill.MCPManager, mcpmanager.Manager, error) {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.MCP == nil || strings.TrimSpace(cfg.AICLI.MCP.ConfigFile) == "" {
		return nil, nil, nil
	}
	if !cfg.AICLI.MCP.AutoConnect {
		return nil, nil, nil
	}

	manager := mcpmanager.NewManager()
	if err := manager.LoadConfig(cfg.AICLI.MCP.ConfigFile); err != nil {
		return nil, nil, fmt.Errorf("failed to load MCP config: %w", err)
	}
	if err := manager.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("failed to start MCP manager: %w", err)
	}
	return runtimeskill.NewMCPAdapter(manager), manager, nil
}

func buildSkillsProviderConfigs(cfg *config.Config) map[string]*runtimellm.ProviderConfig {
	providerConfigs := make(map[string]*runtimellm.ProviderConfig)
	if cfg == nil {
		return providerConfigs
	}

	for name, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}

		providerType := provider.GetType()
		if providerType == "" {
			continue
		}

		timeout := provider.Timeout
		if timeout <= 0 {
			timeout = cfg.Providers.Timeout
		}
		maxRetries := cfg.Providers.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}

		providerConfigs[name] = &runtimellm.ProviderConfig{
			Type:               providerType,
			APIKey:             provider.GetAPIKey(),
			BaseURL:            provider.BaseURL,
			Timeout:            timeout,
			MaxRetries:         maxRetries,
			DefaultModel:       provider.DefaultModel,
			SupportedModels:    append([]string(nil), provider.SupportedModels...),
			ModelMappings:      cloneStringMap(provider.ModelMappings),
			Headers:            cloneStringMap(provider.Headers),
			HeaderMappings:     cloneStringMap(provider.HeaderMappings),
			HeaderMappingRules: cloneHeaderMappingRules(provider.HeaderMappingRules),
		}
	}

	return providerConfigs
}

func applySkillsRuntimePolicies(handler *skillsapi.Handler, cfg *config.SkillsRuntimeConfig) {
	if handler == nil || cfg == nil {
		return
	}
	handler.SetAdminToken(cfg.AdminToken)
	handler.SetSearchReindexCooldown(cfg.ReindexCooldown)
	handler.SetMutationPolicy(skillsapi.MutationPolicy{
		ReadOnly:         cfg.ReadOnly,
		DisableImport:    cfg.DisableImport,
		DisablePersist:   cfg.DisablePersist,
		DisableReloadOps: cfg.DisableReloadOps,
		DisableHotReload: cfg.DisableHotReloadOps,
	})
	handler.SetUsagePolicy(skillsapi.UsagePolicy{
		TrackingEnabled:    cfg.UsageTrackingEnabled,
		QuotaEnabled:       cfg.QuotaEnabled,
		DefaultMaxRequests: cfg.DefaultMaxRequests,
		DefaultMaxTokens:   cfg.DefaultMaxTokens,
		TenantQuotas:       buildSkillsUsageQuotaLimits(cfg.QuotaPolicies.Tenants),
		ProjectQuotas:      buildSkillsUsageQuotaLimits(cfg.QuotaPolicies.Projects),
		UserQuotas:         buildSkillsUsageQuotaLimits(cfg.QuotaPolicies.Users),
	})
	handler.SetScopeResolverConfig(buildSkillsScopeResolverConfig(cfg))
}

func buildSkillsScopeResolverConfig(cfg *config.SkillsRuntimeConfig) skillsapi.ScopeResolverConfig {
	if cfg == nil {
		return skillsapi.ScopeResolverConfig{}
	}
	return skillsapi.ScopeResolverConfig{
		Enabled:          cfg.ScopeResolverEnabled,
		TenantHeaders:    append([]string(nil), cfg.TenantHeaders...),
		ProjectHeaders:   append([]string(nil), cfg.ProjectHeaders...),
		UserHeaders:      append([]string(nil), cfg.UserHeaders...),
		RoleHeaders:      append([]string(nil), cfg.RoleHeaders...),
		JWTClaimsEnabled: cfg.JWTClaimsEnabled,
		JWTSecret:        strings.TrimSpace(cfg.JWTSecret),
		TenantClaims:     append([]string(nil), cfg.TenantClaims...),
		ProjectClaims:    append([]string(nil), cfg.ProjectClaims...),
		UserClaims:       append([]string(nil), cfg.UserClaims...),
		RoleClaims:       append([]string(nil), cfg.RoleClaims...),
		AdminRoles:       append([]string(nil), cfg.AdminRoles...),
		APIKeyScopes:     buildSkillsScopeBindings(cfg.APIKeyScopes),
	}
}

func configuredMCPConfigPath(cfg *config.Config) string {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.MCP == nil {
		return ""
	}
	return strings.TrimSpace(cfg.AICLI.MCP.ConfigFile)
}

func configuredMCPAutoConnect(cfg *config.Config) bool {
	return cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil && cfg.AICLI.MCP.AutoConnect
}

func defaultProfile(cfg *config.Config) string {
	if cfg == nil || cfg.Profiles == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Profiles.DefaultProfile)
}

func resolvedExtraSkillDirs(cfg *config.SkillsRuntimeConfig) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]struct{})
	dirs := make([]string, 0, len(cfg.SkillDirs)+len(cfg.ExtraSkillDirs))
	addDir := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == strings.TrimSpace(cfg.SkillDir) {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		dirs = append(dirs, value)
	}
	for _, dir := range cfg.SkillDirs {
		addDir(dir)
	}
	for _, dir := range cfg.ExtraSkillDirs {
		addDir(dir)
	}
	return dirs
}

func allConfiguredSkillDirs(cfg *config.SkillsRuntimeConfig) []string {
	if cfg == nil {
		return nil
	}
	result := make([]string, 0, 1+len(cfg.SkillDirs)+len(cfg.ExtraSkillDirs))
	if trimmed := strings.TrimSpace(cfg.SkillDir); trimmed != "" {
		result = append(result, trimmed)
	}
	result = append(result, resolvedExtraSkillDirs(cfg)...)
	return result
}

func runtimeInfoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"service": "ai-agent-runtime",
		"routes":  []string{"/api/agent/chat", "/api/skills"},
	})
}

func buildSkillsUsageQuotaLimits(configured map[string]config.SkillsRuntimeQuotaLimit) map[string]skillsapi.UsageQuotaLimit {
	if len(configured) == 0 {
		return nil
	}
	limits := make(map[string]skillsapi.UsageQuotaLimit, len(configured))
	for key, value := range configured {
		limits[key] = skillsapi.UsageQuotaLimit{
			MaxRequests: value.MaxRequests,
			MaxTokens:   value.MaxTokens,
		}
	}
	return limits
}

func buildSkillsScopeBindings(configured map[string]config.SkillsRuntimeScopeBinding) map[string]skillsapi.UsageScope {
	if len(configured) == 0 {
		return nil
	}
	bindings := make(map[string]skillsapi.UsageScope, len(configured))
	for key, value := range configured {
		bindings[key] = skillsapi.UsageScope{
			TenantID:  value.TenantID,
			ProjectID: value.ProjectID,
			UserID:    value.UserID,
		}
	}
	return bindings
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

func cloneHeaderMappingRules(input []config.HeaderMappingRule) []runtimellm.HeaderMappingRule {
	if len(input) == 0 {
		return nil
	}
	output := make([]runtimellm.HeaderMappingRule, len(input))
	for i, rule := range input {
		output[i] = runtimellm.HeaderMappingRule{
			Name:         rule.Name,
			Enabled:      rule.Enabled,
			Header:       rule.Header,
			TargetHeader: rule.TargetHeader,
			MatchType:    rule.MatchType,
			Match:        rule.Match,
			Value:        rule.Value,
		}
	}
	return output
}
