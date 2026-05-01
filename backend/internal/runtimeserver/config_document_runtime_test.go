package runtimeserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

type fakeRuntimeConfigApplyTarget struct {
	adminToken     string
	logFilePath    string
	profileSupport skillsapi.ProfileSupportConfig
	mutationPolicy skillsapi.MutationPolicy
	scopeResolver  skillsapi.ScopeResolverConfig
	searchCooldown time.Duration
	usagePolicy    skillsapi.UsagePolicy
}

func (f *fakeRuntimeConfigApplyTarget) SetAdminToken(token string) {
	f.adminToken = token
}

func (f *fakeRuntimeConfigApplyTarget) SetMutationPolicy(policy skillsapi.MutationPolicy) {
	f.mutationPolicy = policy
}

func (f *fakeRuntimeConfigApplyTarget) SetProfileSupport(cfg skillsapi.ProfileSupportConfig) {
	f.profileSupport = cfg
}

func (f *fakeRuntimeConfigApplyTarget) SetRuntimeLogFilePath(path string) {
	f.logFilePath = path
}

func (f *fakeRuntimeConfigApplyTarget) SetScopeResolverConfig(config skillsapi.ScopeResolverConfig) {
	f.scopeResolver = config
}

func (f *fakeRuntimeConfigApplyTarget) SetSearchReindexCooldown(cooldown time.Duration) {
	f.searchCooldown = cooldown
}

func (f *fakeRuntimeConfigApplyTarget) SetUsagePolicy(policy skillsapi.UsagePolicy) {
	f.usagePolicy = policy
}

func TestAnalyzeConfigDocumentRuntimeImpactClassifiesChangedPaths(t *testing.T) {
	current := map[string]interface{}{
		"providers": map[string]interface{}{
			"backoff": map[string]interface{}{
				"initial_interval": "500ms",
			},
			"proxy": map[string]interface{}{
				"http": "http://old-proxy.example.com:8080",
			},
			"items": map[string]interface{}{
				"alpha": map[string]interface{}{
					"base_url": "https://old.example.com",
				},
			},
		},
		"retry": map[string]interface{}{
			"default_backoff_multiplier": 1.5,
		},
		"skills_runtime": map[string]interface{}{
			"admin_token": "old-token",
		},
		"auth": map[string]interface{}{
			"admin_token": "old-auth-token",
		},
	}
	next := map[string]interface{}{
		"providers": map[string]interface{}{
			"backoff": map[string]interface{}{
				"initial_interval": "750ms",
			},
			"proxy": map[string]interface{}{
				"http": "http://new-proxy.example.com:8080",
			},
			"items": map[string]interface{}{
				"alpha": map[string]interface{}{
					"base_url": "https://new.example.com",
				},
			},
		},
		"retry": map[string]interface{}{
			"default_backoff_multiplier": 2.0,
		},
		"skills_runtime": map[string]interface{}{
			"admin_token": "new-token",
		},
		"auth": map[string]interface{}{
			"admin_token": "new-auth-token",
		},
	}

	impact := analyzeConfigDocumentRuntimeImpact(current, next)
	require.NotNil(t, impact)
	require.ElementsMatch(t,
		[]string{
			"auth.admin_token",
			"providers.backoff.initial_interval",
			"providers.items.alpha.base_url",
			"providers.proxy.http",
			"retry.default_backoff_multiplier",
			"skills_runtime.admin_token",
		},
		impact.ChangedPaths,
	)
	require.ElementsMatch(t,
		[]string{
			"providers.backoff.initial_interval",
			"providers.items.alpha.base_url",
			"providers.proxy.http",
			"retry.default_backoff_multiplier",
			"skills_runtime.admin_token",
		},
		impact.HotReloadPaths,
	)
	require.Empty(t, impact.RestartRequiredPaths)
	require.ElementsMatch(t, []string{"auth.admin_token"}, impact.InactivePaths)
}

func TestClassifyConfigDocumentPathTreatsRetrySettingsAsHotReload(t *testing.T) {
	require.Equal(t, runtimeConfigPathHotReload, classifyConfigDocumentPath("providers.backoff.initial_interval"))
	require.Equal(t, runtimeConfigPathHotReload, classifyConfigDocumentPath("retry.default_backoff_multiplier"))
	require.Equal(t, runtimeConfigPathHotReload, classifyConfigDocumentPath("retry.rules.0.retry_delay_ms"))
}

func TestRuntimeConfigHotReloaderAppliesHotReloadableSettings(t *testing.T) {
	currentCfg := &agentconfig.Config{}
	target := &fakeRuntimeConfigApplyTarget{}
	reloader := NewRuntimeConfigHotReloader(target, currentCfg, nil)
	require.NotNil(t, reloader)

	nextCfg := &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{
			MCP: &agentconfig.AICLIMCPConfig{
				AutoConnect: true,
				ConfigFile:  "configs/mcp.yaml",
			},
		},
		Profiles: &agentconfig.ProfilesConfig{
			DefaultProfile: "dev",
		},
		SkillsRuntime: &agentconfig.SkillsRuntimeConfig{
			AdminToken:       "new-admin-token",
			ReindexCooldown:  15 * time.Second,
			QuotaEnabled:     true,
			DefaultMaxTokens: 2048,
		},
	}

	result := reloader.Apply(nextCfg, []string{
		"profiles.default_profile",
		"skills_runtime.admin_token",
	})

	require.ElementsMatch(t,
		[]string{"profiles.default_profile", "skills_runtime.admin_token"},
		result.AppliedPaths,
	)
	require.Empty(t, result.Warnings)
	require.Equal(t, "new-admin-token", target.adminToken)
	require.Equal(t, "dev", target.profileSupport.DefaultProfile)
	require.Equal(t, "dev", currentCfg.Profiles.DefaultProfile)
}

func TestRuntimeConfigHotReloaderReportsPathSuggestionsForMissingConfigFiles(t *testing.T) {
	root := t.TempDir()
	workdir := filepath.Join(root, "backend")
	require.NoError(t, os.MkdirAll(workdir, 0o755))

	suggested := filepath.Join(workdir, "frontend", "src", "pages", "settings", "runtime.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(suggested), 0o755))
	require.NoError(t, os.WriteFile(suggested, []byte("ok"), 0o644))

	originalWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(workdir))
	t.Cleanup(func() {
		require.NoError(t, os.Chdir(originalWD))
	})

	currentCfg := &agentconfig.Config{}
	target := &fakeRuntimeConfigApplyTarget{}
	reloader := NewRuntimeConfigHotReloader(target, currentCfg, nil)
	require.NotNil(t, reloader)

	nextCfg := &agentconfig.Config{
		SkillsRuntime: &agentconfig.SkillsRuntimeConfig{
			ConfigFile: "frontend/src/pages/setting/runtime.yaml",
		},
	}

	result := reloader.Apply(nextCfg, []string{"skills_runtime.config_file"})
	require.NotEmpty(t, result.Warnings)

	normalizedWarnings := strings.ReplaceAll(strings.Join(result.Warnings, "\n"), `\`, "/")
	require.Contains(t, normalizedWarnings, "skills_runtime.config_file")
	require.Contains(t, normalizedWarnings, "frontend/src/pages/settings/runtime.yaml")
}

func TestLocalConfigDocumentServiceSaveClassifiesAndAppliesChanges(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := `
providers:
  items:
    alpha:
      protocol: openai
      base_url: https://old.example.com
skills_runtime:
  admin_token: old-token
auth:
  admin_token: old-auth-token
`
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o644))

	target := &fakeRuntimeConfigApplyTarget{}
	service := NewLocalConfigDocumentService(configPath)
	require.NotNil(t, service)
	service.SetHotReloader(NewRuntimeConfigHotReloader(target, &agentconfig.Config{}, nil))

	updated := `
providers:
  items:
    alpha:
      protocol: openai
      base_url: https://new.example.com
skills_runtime:
  admin_token: new-token
auth:
  admin_token: new-auth-token
`
	document, err := service.SaveDocument(skillsapi.ConfigDocumentSaveRequest{
		Mode: "raw",
		Raw:  ptrToString(updated),
	})
	require.NoError(t, err)
	require.False(t, document.RestartRequired)
	require.NotNil(t, document.RuntimeImpact)
	require.ElementsMatch(t,
		[]string{"providers.items.alpha.base_url", "skills_runtime.admin_token"},
		document.RuntimeImpact.HotReloadPaths,
	)
	require.Empty(t, document.RuntimeImpact.RestartRequiredPaths)
	require.ElementsMatch(t, []string{"auth.admin_token"}, document.RuntimeImpact.InactivePaths)
	require.ElementsMatch(t, []string{"skills_runtime.admin_token"}, document.RuntimeImpact.AppliedPaths)
	require.Equal(t, "new-token", target.adminToken)
}
