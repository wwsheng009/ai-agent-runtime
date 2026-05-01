package runtimeserver

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type runtimeConfigPathDisposition string

const (
	runtimeConfigPathHotReload runtimeConfigPathDisposition = "hot_reload"
	runtimeConfigPathRestart   runtimeConfigPathDisposition = "restart"
	runtimeConfigPathInactive  runtimeConfigPathDisposition = "inactive"
)

var (
	hotReloadSkillsRuntimePrefixes = []string{
		"skills_runtime.admin_token",
		"skills_runtime.reindex_cooldown",
		"skills_runtime.read_only",
		"skills_runtime.disable_import",
		"skills_runtime.disable_persist",
		"skills_runtime.disable_reload_ops",
		"skills_runtime.disable_hot_reload_ops",
		"skills_runtime.usage_tracking_enabled",
		"skills_runtime.quota_enabled",
		"skills_runtime.default_max_requests",
		"skills_runtime.default_max_tokens",
		"skills_runtime.quota_policies",
		"skills_runtime.scope_resolver_enabled",
		"skills_runtime.tenant_headers",
		"skills_runtime.project_headers",
		"skills_runtime.user_headers",
		"skills_runtime.role_headers",
		"skills_runtime.jwt_claims_enabled",
		"skills_runtime.jwt_secret",
		"skills_runtime.tenant_claims",
		"skills_runtime.project_claims",
		"skills_runtime.user_claims",
		"skills_runtime.role_claims",
		"skills_runtime.admin_roles",
		"skills_runtime.api_key_scopes",
	}
	restartRequiredSkillsRuntimePrefixes = []string{
		"skills_runtime.config_file",
		"skills_runtime.skill_dir",
		"skills_runtime.skill_dirs",
		"skills_runtime.extra_skill_dirs",
		"skills_runtime.usage_ledger_enabled",
	}
	inactiveSkillsRuntimePrefixes = []string{
		"skills_runtime.enabled",
		"skills_runtime.gateway_provider_name",
		"skills_runtime.aicli_skill_exposure_top_k",
		"skills_runtime.aicli_skill_exposure_mode",
	}
	hotReloadProfileSupportPrefixes = []string{
		"profiles",
		"aicli.mcp.config_file",
		"aicli.mcp.auto_connect",
	}
	hotReloadProviderPrefixes = []string{
		"providers.timeout",
		"providers.max_retries",
		"providers.backoff",
		"providers.proxy",
		"providers.items",
		"retry",
	}
)

type ConfigDocumentHotReloadResult struct {
	AppliedPaths []string
	Warnings     []string
}

type ConfigDocumentHotReloader interface {
	Apply(nextCfg *agentconfig.Config, hotPaths []string) ConfigDocumentHotReloadResult
}

type RuntimeConfigApplyTarget interface {
	SetAdminToken(token string)
	SetMutationPolicy(policy skillsapi.MutationPolicy)
	SetProfileSupport(cfg skillsapi.ProfileSupportConfig)
	SetRuntimeLogFilePath(path string)
	SetScopeResolverConfig(config skillsapi.ScopeResolverConfig)
	SetSearchReindexCooldown(cooldown time.Duration)
	SetUsagePolicy(policy skillsapi.UsagePolicy)
}

type RuntimeConfigHotReloader struct {
	target     RuntimeConfigApplyTarget
	bootstrap  *runtimebootstrap.Manager
	currentCfg *agentconfig.Config
}

func NewRuntimeConfigHotReloader(
	target RuntimeConfigApplyTarget,
	currentCfg *agentconfig.Config,
	bootstrap *runtimebootstrap.Manager,
) *RuntimeConfigHotReloader {
	if target == nil && currentCfg == nil && bootstrap == nil {
		return nil
	}
	return &RuntimeConfigHotReloader{
		target:     target,
		bootstrap:  bootstrap,
		currentCfg: currentCfg,
	}
}

func (r *RuntimeConfigHotReloader) Apply(
	nextCfg *agentconfig.Config,
	hotPaths []string,
) ConfigDocumentHotReloadResult {
	result := ConfigDocumentHotReloadResult{}
	if r == nil || nextCfg == nil {
		return result
	}

	hotPaths = uniqueSortedPaths(hotPaths)
	if len(hotPaths) == 0 {
		r.updateCurrentConfig(nextCfg)
		return result
	}

	if r.target == nil && r.bootstrap == nil {
		r.updateCurrentConfig(nextCfg)
		result.Warnings = append(result.Warnings,
			"runtime 配置已写入磁盘，但当前进程未接入热重载目标；可热重载变更尚未自动应用。")
		return result
	}

	appliedSet := make(map[string]struct{})

	if hasAnyConfigPathPrefix(hotPaths, "log") {
		if r.target == nil {
			result.Warnings = append(result.Warnings,
				"log 配置已保存，但当前进程未接入日志热重载目标。")
		} else if err := logger.InitLogger(&nextCfg.Log); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("log 配置已保存，但热重载日志器失败: %v", err))
		} else {
			r.target.SetRuntimeLogFilePath(strings.TrimSpace(nextCfg.Log.FilePath))
			addMatchingPaths(appliedSet, hotPaths, "log")
		}
	}

	if hasAnyPrefixInSet(hotPaths, hotReloadSkillsRuntimePrefixes) {
		if r.target == nil {
			result.Warnings = append(result.Warnings,
				"skills_runtime 治理策略已保存，但当前进程未接入热重载目标。")
		} else {
			skillsCfg := normalizeSkillsRuntimeConfigForHotReload(nextCfg)
			applySkillsRuntimePoliciesToTarget(r.target, skillsCfg)
			addMatchingPathsForPrefixes(appliedSet, hotPaths, hotReloadSkillsRuntimePrefixes)
		}
	}

	if hasAnyPrefixInSet(hotPaths, hotReloadProfileSupportPrefixes) {
		if r.target == nil {
			result.Warnings = append(result.Warnings,
				"profiles / aicli.mcp 配置已保存，但当前进程未接入热重载目标。")
		} else {
			r.target.SetProfileSupport(buildProfileSupportConfigForHotReload(nextCfg))
			addMatchingPathsForPrefixes(appliedSet, hotPaths, hotReloadProfileSupportPrefixes)
		}
	}

	if hasAnyPrefixInSet(hotPaths, hotReloadProviderPrefixes) {
		if r.bootstrap == nil {
			result.Warnings = append(result.Warnings,
				"provider 配置已保存，但当前进程未接入 provider 热替换目标。")
		} else if err := r.bootstrap.ReloadProviderConfigs(buildRuntimeProviderConfigs(nextCfg)); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("provider 配置已保存，但热替换 provider 注册表失败: %v", err))
		} else {
			addMatchingPathsForPrefixes(appliedSet, hotPaths, hotReloadProviderPrefixes)
		}
	}

	if pathWarnings := buildRuntimeConfigPathWarnings(nextCfg, hotPaths); len(pathWarnings) > 0 {
		result.Warnings = append(result.Warnings, pathWarnings...)
	}

	r.updateCurrentConfig(nextCfg)
	result.AppliedPaths = mapKeysSorted(appliedSet)
	return result
}

func (r *RuntimeConfigHotReloader) updateCurrentConfig(nextCfg *agentconfig.Config) {
	if r == nil || r.currentCfg == nil || nextCfg == nil {
		return
	}
	*r.currentCfg = *nextCfg
}

func analyzeConfigDocumentRuntimeImpact(
	current interface{},
	next interface{},
) *skillsapi.ConfigDocumentRuntimeImpact {
	changedSet := make(map[string]struct{})
	collectConfigDocumentChangedPaths("", current, next, changedSet)
	changedPaths := mapKeysSorted(changedSet)
	if len(changedPaths) == 0 {
		return nil
	}

	impact := &skillsapi.ConfigDocumentRuntimeImpact{
		ChangedPaths: changedPaths,
	}
	for _, path := range changedPaths {
		switch classifyConfigDocumentPath(path) {
		case runtimeConfigPathHotReload:
			impact.HotReloadPaths = append(impact.HotReloadPaths, path)
		case runtimeConfigPathRestart:
			impact.RestartRequiredPaths = append(impact.RestartRequiredPaths, path)
		default:
			impact.InactivePaths = append(impact.InactivePaths, path)
		}
	}
	return impact
}

func collectConfigDocumentChangedPaths(
	path string,
	current interface{},
	next interface{},
	changed map[string]struct{},
) {
	if reflect.DeepEqual(current, next) {
		return
	}

	currentMap, currentIsMap := current.(map[string]interface{})
	nextMap, nextIsMap := next.(map[string]interface{})
	if currentIsMap && nextIsMap {
		keys := make(map[string]struct{}, len(currentMap)+len(nextMap))
		for key := range currentMap {
			keys[key] = struct{}{}
		}
		for key := range nextMap {
			keys[key] = struct{}{}
		}
		for _, key := range mapKeysSorted(keys) {
			childPath := joinConfigPath(path, key)
			collectConfigDocumentChangedPaths(childPath, currentMap[key], nextMap[key], changed)
		}
		return
	}

	_, currentIsList := current.([]interface{})
	_, nextIsList := next.([]interface{})
	if currentIsList && nextIsList {
		recordChangedPath(path, changed)
		return
	}

	recordChangedPath(path, changed)
}

func recordChangedPath(path string, changed map[string]struct{}) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "$"
	}
	changed[path] = struct{}{}
}

func joinConfigPath(prefix, key string) string {
	if strings.TrimSpace(prefix) == "" {
		return key
	}
	return prefix + "." + key
}

func classifyConfigDocumentPath(path string) runtimeConfigPathDisposition {
	path = strings.TrimSpace(path)
	if path == "" || path == "$" {
		return runtimeConfigPathRestart
	}

	if hasAnyConfigPathPrefix([]string{path}, "log") {
		return runtimeConfigPathHotReload
	}
	if hasAnyPrefixInSet([]string{path}, hotReloadProfileSupportPrefixes) {
		return runtimeConfigPathHotReload
	}
	if hasAnyPrefixInSet([]string{path}, hotReloadSkillsRuntimePrefixes) {
		return runtimeConfigPathHotReload
	}
	if hasAnyPrefixInSet([]string{path}, hotReloadProviderPrefixes) {
		return runtimeConfigPathHotReload
	}
	if hasAnyPrefixInSet([]string{path}, inactiveSkillsRuntimePrefixes) {
		return runtimeConfigPathInactive
	}
	if hasAnyPrefixInSet([]string{path}, restartRequiredSkillsRuntimePrefixes) {
		return runtimeConfigPathRestart
	}

	if hasConfigPathPrefix(path, "server.host") || hasConfigPathPrefix(path, "server.port") {
		return runtimeConfigPathRestart
	}
	if hasConfigPathPrefix(path, "skills_runtime") {
		return runtimeConfigPathRestart
	}
	if classifyProviderItemPath(path) != runtimeConfigPathInactive {
		return classifyProviderItemPath(path)
	}

	switch topLevelConfigPath(path) {
	case "aicli",
		"auth",
		"circuit_breaker",
		"concurrency",
		"database",
		"monitor",
		"provider_groups",
		"provider_queue",
		"providers",
		"resource_manager",
		"retry",
		"routing",
		"transformer",
		"websocket":
		return runtimeConfigPathInactive
	default:
		return runtimeConfigPathInactive
	}
}

func classifyProviderItemPath(path string) runtimeConfigPathDisposition {
	parts := strings.Split(path, ".")
	if len(parts) < 3 || parts[0] != "providers" || parts[1] != "items" {
		return runtimeConfigPathInactive
	}
	if len(parts) == 3 {
		return runtimeConfigPathHotReload
	}

	fieldPath := strings.Join(parts[3:], ".")
	switch {
	case hasConfigPathPrefix(fieldPath, "enabled"),
		hasConfigPathPrefix(fieldPath, "type"),
		hasConfigPathPrefix(fieldPath, "protocol"),
		hasConfigPathPrefix(fieldPath, "base_url"),
		hasConfigPathPrefix(fieldPath, "api_key"),
		hasConfigPathPrefix(fieldPath, "api_keys"),
		hasConfigPathPrefix(fieldPath, "default_model"),
		hasConfigPathPrefix(fieldPath, "supported_models"),
		hasConfigPathPrefix(fieldPath, "headers"),
		hasConfigPathPrefix(fieldPath, "header_mappings"),
		hasConfigPathPrefix(fieldPath, "header_mapping_rules"),
		hasConfigPathPrefix(fieldPath, "model_mappings"),
		hasConfigPathPrefix(fieldPath, "timeout"):
		return runtimeConfigPathHotReload
	default:
		return runtimeConfigPathInactive
	}
}

func buildConfigDocumentWarnings(
	impact *skillsapi.ConfigDocumentRuntimeImpact,
	appliedPaths []string,
	applyWarnings []string,
) []string {
	warnings := make([]string, 0, 4+len(applyWarnings))
	if impact == nil {
		warnings = append(warnings,
			"保存时会按变更路径区分为即时热重载、需重启和当前进程未使用三类。")
		warnings = append(warnings,
			"providers.items.*、providers.timeout、providers.max_retries、providers.backoff、retry.* 会同步刷新运行中 provider 注册表。")
		warnings = append(warnings,
			"skills_runtime 的治理策略、log、profiles、aicli.mcp 相关配置也会在当前进程内即时应用。")
		return warnings
	}

	if len(appliedPaths) > 0 {
		warnings = append(warnings,
			fmt.Sprintf("以下变更已即时应用到当前 runtime-server 进程: %s",
				strings.Join(appliedPaths, ", ")))
	}
	if len(impact.RestartRequiredPaths) > 0 {
		warnings = append(warnings,
			fmt.Sprintf("以下变更仍需重启 runtime-server 才会生效: %s",
				strings.Join(impact.RestartRequiredPaths, ", ")))
	}
	if len(impact.InactivePaths) > 0 {
		warnings = append(warnings,
			fmt.Sprintf("以下变更当前不会影响 runtime-server 进程: %s",
				strings.Join(impact.InactivePaths, ", ")))
	}
	warnings = append(warnings, applyWarnings...)
	return warnings
}

func buildRuntimeConfigPathWarnings(cfg *agentconfig.Config, hotPaths []string) []string {
	if cfg == nil || len(hotPaths) == 0 {
		return nil
	}

	warnings := make([]string, 0, 4)
	addWarning := func(label, path string) {
		if len(warnings) >= 4 {
			return
		}
		if warning := buildRuntimeConfigPathWarning(label, path); warning != "" {
			warnings = append(warnings, warning)
		}
	}

	if hasAnyPrefixInSet(hotPaths, restartRequiredSkillsRuntimePrefixes) && cfg.SkillsRuntime != nil {
		addWarning("skills_runtime.config_file", cfg.SkillsRuntime.ConfigFile)
		addWarning("skills_runtime.skill_dir", cfg.SkillsRuntime.SkillDir)
		for index, path := range cfg.SkillsRuntime.SkillDirs {
			if len(warnings) >= 4 {
				break
			}
			addWarning(fmt.Sprintf("skills_runtime.skill_dirs[%d]", index), path)
		}
		for index, path := range cfg.SkillsRuntime.ExtraSkillDirs {
			if len(warnings) >= 4 {
				break
			}
			addWarning(fmt.Sprintf("skills_runtime.extra_skill_dirs[%d]", index), path)
		}
	}

	if hasAnyConfigPathPrefix(hotPaths, "aicli.mcp.config_file") && cfg.AICLI != nil && cfg.AICLI.MCP != nil {
		addWarning("aicli.mcp.config_file", cfg.AICLI.MCP.ConfigFile)
	}

	return warnings
}

func buildRuntimeConfigPathWarning(label, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	detail := ResolveUpwardPathDetail(path)
	if strings.TrimSpace(detail.Resolved) != "" {
		return ""
	}
	if len(detail.Candidates) == 0 {
		return fmt.Sprintf("%s 路径不存在: %s", label, path)
	}
	return fmt.Sprintf(
		"%s 路径不存在: %s，可能的候选路径: %s",
		label,
		path,
		strings.Join(detail.Candidates, ", "),
	)
}

func applySkillsRuntimePoliciesToTarget(
	target RuntimeConfigApplyTarget,
	cfg *agentconfig.SkillsRuntimeConfig,
) {
	if target == nil || cfg == nil {
		return
	}

	target.SetAdminToken(cfg.AdminToken)
	target.SetSearchReindexCooldown(cfg.ReindexCooldown)
	target.SetMutationPolicy(skillsapi.MutationPolicy{
		ReadOnly:         cfg.ReadOnly,
		DisableImport:    cfg.DisableImport,
		DisablePersist:   cfg.DisablePersist,
		DisableReloadOps: cfg.DisableReloadOps,
		DisableHotReload: cfg.DisableHotReloadOps,
	})
	target.SetUsagePolicy(skillsapi.UsagePolicy{
		TrackingEnabled:    cfg.UsageTrackingEnabled,
		QuotaEnabled:       cfg.QuotaEnabled,
		DefaultMaxRequests: cfg.DefaultMaxRequests,
		DefaultMaxTokens:   cfg.DefaultMaxTokens,
		TenantQuotas:       buildSkillsUsageQuotaLimitsForHotReload(cfg.QuotaPolicies.Tenants),
		ProjectQuotas:      buildSkillsUsageQuotaLimitsForHotReload(cfg.QuotaPolicies.Projects),
		UserQuotas:         buildSkillsUsageQuotaLimitsForHotReload(cfg.QuotaPolicies.Users),
	})
	target.SetScopeResolverConfig(skillsapi.ScopeResolverConfig{
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
		APIKeyScopes:     buildSkillsScopeBindingsForHotReload(cfg.APIKeyScopes),
	})
}

func buildProfileSupportConfigForHotReload(cfg *agentconfig.Config) skillsapi.ProfileSupportConfig {
	skillsCfg := normalizeSkillsRuntimeConfigForHotReload(cfg)
	return skillsapi.ProfileSupportConfig{
		Registry:          profilesys.NewRegistryFromProfilesConfig(cfg.Profiles),
		DefaultProfile:    defaultProfileForHotReload(cfg),
		GlobalRuntimePath: strings.TrimSpace(skillsCfg.ConfigFile),
		GlobalMCPPath:     configuredMCPConfigPathForHotReload(cfg),
		GlobalSkillDirs:   allConfiguredSkillDirsForHotReload(skillsCfg),
		MCPAutoConnect:    configuredMCPAutoConnectForHotReload(cfg),
	}
}

func normalizeSkillsRuntimeConfigForHotReload(cfg *agentconfig.Config) *agentconfig.SkillsRuntimeConfig {
	skillsCfg := &agentconfig.SkillsRuntimeConfig{}
	if cfg != nil && cfg.SkillsRuntime != nil {
		*skillsCfg = *cfg.SkillsRuntime
	}

	if strings.TrimSpace(skillsCfg.ConfigFile) == "" {
		skillsCfg.ConfigFile = "backend/configs/runtime.yaml"
	}
	if strings.TrimSpace(skillsCfg.SkillDir) == "" {
		skillsCfg.SkillDir = "./.agents/skills"
	}
	if strings.TrimSpace(skillsCfg.GatewayProviderName) == "" {
		skillsCfg.GatewayProviderName = "gateway"
	}
	if skillsCfg.ReindexCooldown <= 0 {
		skillsCfg.ReindexCooldown = 30 * time.Second
	}
	if len(skillsCfg.TenantHeaders) == 0 {
		skillsCfg.TenantHeaders = []string{"X-Skills-Tenant", "X-Skills-Auth-Tenant", "X-Tenant-ID", "X-Authenticated-Tenant"}
	}
	if len(skillsCfg.ProjectHeaders) == 0 {
		skillsCfg.ProjectHeaders = []string{"X-Skills-Project", "X-Skills-Auth-Project", "X-Project-ID", "X-Authenticated-Project"}
	}
	if len(skillsCfg.UserHeaders) == 0 {
		skillsCfg.UserHeaders = []string{"X-Skills-User", "X-Skills-Auth-User", "X-User-ID", "X-Authenticated-User"}
	}
	if len(skillsCfg.RoleHeaders) == 0 {
		skillsCfg.RoleHeaders = []string{"X-Skills-Role", "X-Skills-Auth-Role", "X-Role", "X-Authenticated-Role"}
	}
	if len(skillsCfg.TenantClaims) == 0 {
		skillsCfg.TenantClaims = []string{"tenant_id", "tenant", "tid"}
	}
	if len(skillsCfg.ProjectClaims) == 0 {
		skillsCfg.ProjectClaims = []string{"project_id", "project", "pid"}
	}
	if len(skillsCfg.UserClaims) == 0 {
		skillsCfg.UserClaims = []string{"user_id", "user", "uid", "sub"}
	}
	if len(skillsCfg.RoleClaims) == 0 {
		skillsCfg.RoleClaims = []string{"role", "roles"}
	}
	skillsCfg.ConfigFile = ResolveUpwardPath(skillsCfg.ConfigFile)
	skillsCfg.SkillDir = ResolveUpwardPath(skillsCfg.SkillDir)
	skillsCfg.SkillDirs = ResolveUpwardPaths(skillsCfg.SkillDirs)
	skillsCfg.ExtraSkillDirs = ResolveUpwardPaths(skillsCfg.ExtraSkillDirs)
	return skillsCfg
}

func configuredMCPConfigPathForHotReload(cfg *agentconfig.Config) string {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.MCP == nil {
		return ""
	}
	return strings.TrimSpace(ResolveUpwardPath(cfg.AICLI.MCP.ConfigFile))
}

func configuredMCPAutoConnectForHotReload(cfg *agentconfig.Config) bool {
	return cfg != nil && cfg.AICLI != nil && cfg.AICLI.MCP != nil && cfg.AICLI.MCP.AutoConnect
}

func defaultProfileForHotReload(cfg *agentconfig.Config) string {
	if cfg == nil || cfg.Profiles == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Profiles.DefaultProfile)
}

func allConfiguredSkillDirsForHotReload(cfg *agentconfig.SkillsRuntimeConfig) []string {
	if cfg == nil {
		return nil
	}
	result := make([]string, 0, 1+len(cfg.SkillDirs)+len(cfg.ExtraSkillDirs))
	if trimmed := strings.TrimSpace(cfg.SkillDir); trimmed != "" {
		result = append(result, trimmed)
	}
	result = append(result, resolvedExtraSkillDirsForHotReload(cfg)...)
	return result
}

func resolvedExtraSkillDirsForHotReload(cfg *agentconfig.SkillsRuntimeConfig) []string {
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
	if configFile := strings.TrimSpace(cfg.ConfigFile); configFile != "" {
		if resolvedConfigFile := ResolveUpwardPath(configFile); strings.TrimSpace(resolvedConfigFile) != "" {
			for _, dir := range skill.DiscoverCodexCompatibleSkillDirs(filepath.Dir(resolvedConfigFile), resolvedConfigFile) {
				addDir(dir)
			}
		}
	}
	return dirs
}

func buildSkillsUsageQuotaLimitsForHotReload(
	configured map[string]agentconfig.SkillsRuntimeQuotaLimit,
) map[string]skillsapi.UsageQuotaLimit {
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

func buildSkillsScopeBindingsForHotReload(
	configured map[string]agentconfig.SkillsRuntimeScopeBinding,
) map[string]skillsapi.UsageScope {
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

func hasAnyPrefixInSet(paths []string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if hasAnyConfigPathPrefix(paths, prefix) {
			return true
		}
	}
	return false
}

func hasAnyConfigPathPrefix(paths []string, prefix string) bool {
	for _, path := range paths {
		if hasConfigPathPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func hasConfigPathPrefix(path string, prefix string) bool {
	path = strings.TrimSpace(path)
	prefix = strings.TrimSpace(prefix)
	if path == "" || prefix == "" {
		return false
	}
	return path == prefix || strings.HasPrefix(path, prefix+".")
}

func addMatchingPaths(target map[string]struct{}, paths []string, prefix string) {
	for _, path := range paths {
		if hasConfigPathPrefix(path, prefix) {
			target[path] = struct{}{}
		}
	}
}

func addMatchingPathsForPrefixes(target map[string]struct{}, paths []string, prefixes []string) {
	for _, prefix := range prefixes {
		addMatchingPaths(target, paths, prefix)
	}
}

func topLevelConfigPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	index := strings.IndexByte(path, '.')
	if index < 0 {
		return path
	}
	return path[:index]
}

func uniqueSortedPaths(paths []string) []string {
	set := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		set[path] = struct{}{}
	}
	return mapKeysSorted(set)
}

func mapKeysSorted[T any](values map[string]T) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
