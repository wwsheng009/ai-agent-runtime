package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"gopkg.in/yaml.v3"
)

func TestSkillsRuntimePolicyPersister_PersistsYAMLSectionOnly(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	snapshotPath := runtimeserver.ResolveAgentConfigSnapshotInfo(configPath).SnapshotPath
	initial := `
server:
  host: "${SERVER_HOST:-127.0.0.1}"
providers:
  items:
    openai:
      api_key: "${OPENAI_API_KEY:-secret}"
skills_runtime:
  enabled: true
  config_file: "configs/runtime.yaml"
  skill_dir: "./docs/skill_runtime/skills"
  reindex_cooldown: "30s"
  usage_tracking_enabled: true
  usage_ledger_enabled: false
  quota_enabled: false
  scope_resolver_enabled: true
  tenant_headers: ["X-Old-Tenant"]
  project_headers: ["X-Old-Project"]
  user_headers: ["X-Old-User"]
  role_headers: ["X-Old-Role"]
  jwt_claims_enabled: false
  tenant_claims: ["tenant_id"]
  project_claims: ["project_id"]
  user_claims: ["user_id"]
  role_claims: ["role"]
  admin_roles: []
  api_key_scopes: {}
  quota_policies:
    tenants: {}
    projects: {}
    users: {}
`
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o644))

	cfg, err := config.InitGlobalConfig(configPath)
	require.NoError(t, err)

	persister := runtimeserver.NewSkillsRuntimePolicyPersister(configPath, cfg)
	require.NotNil(t, persister)

	authPolicy := skillsapi.ScopeResolverConfig{
		Enabled:          true,
		JWTClaimsEnabled: true,
		TenantHeaders:    []string{"X-Skills-Tenant"},
		ProjectHeaders:   []string{"X-Skills-Project"},
		UserHeaders:      []string{"X-Skills-User"},
		RoleHeaders:      []string{"X-Skills-Role"},
		TenantClaims:     []string{"tenant", "tid"},
		ProjectClaims:    []string{"project", "pid"},
		UserClaims:       []string{"user", "uid"},
		RoleClaims:       []string{"role", "roles"},
		AdminRoles:       []string{"admin"},
		APIKeyScopes: map[string]skillsapi.UsageScope{
			"test-key": {
				TenantID:  "tenant-a",
				ProjectID: "project-a",
				UserID:    "alice",
			},
		},
	}
	require.NoError(t, persister.PersistAuthPolicy(authPolicy, "tester"))

	usagePolicy := skillsapi.UsagePolicy{
		TrackingEnabled:    true,
		QuotaEnabled:       true,
		DefaultMaxRequests: 10,
		DefaultMaxTokens:   500,
		TenantQuotas: map[string]skillsapi.UsageQuotaLimit{
			"tenant-a": {
				MaxRequests: intPtr(20),
				MaxTokens:   intPtr(1000),
			},
		},
	}
	require.NoError(t, persister.PersistUsagePolicy(usagePolicy, "tester"))

	mutationPolicy := skillsapi.MutationPolicy{
		ReadOnly:         true,
		DisableImport:    true,
		DisablePersist:   true,
		DisableReloadOps: true,
		DisableHotReload: true,
	}
	require.NoError(t, persister.PersistMutationPolicy(mutationPolicy, "tester"))

	baseRaw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(baseRaw), `${OPENAI_API_KEY:-secret}`)
	require.Contains(t, string(baseRaw), `${SERVER_HOST:-127.0.0.1}`)

	raw, err := os.ReadFile(snapshotPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), `${OPENAI_API_KEY:-secret}`)
	require.Contains(t, string(raw), `${SERVER_HOST:-127.0.0.1}`)

	var persisted struct {
		Server        map[string]interface{}     `yaml:"server"`
		Providers     map[string]interface{}     `yaml:"providers"`
		SkillsRuntime config.SkillsRuntimeConfig `yaml:"skills_runtime"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &persisted))
	require.NotNil(t, persisted.Server)
	require.NotNil(t, persisted.Providers)

	require.True(t, persisted.SkillsRuntime.JWTClaimsEnabled)
	require.Equal(t, []string{"X-Skills-Tenant"}, persisted.SkillsRuntime.TenantHeaders)
	require.Equal(t, []string{"admin"}, persisted.SkillsRuntime.AdminRoles)
	require.Equal(t, "tenant-a", persisted.SkillsRuntime.APIKeyScopes["test-key"].TenantID)
	require.True(t, persisted.SkillsRuntime.QuotaEnabled)
	require.Equal(t, 10, persisted.SkillsRuntime.DefaultMaxRequests)
	require.Equal(t, 500, persisted.SkillsRuntime.DefaultMaxTokens)
	require.NotNil(t, persisted.SkillsRuntime.QuotaPolicies.Tenants["tenant-a"].MaxRequests)
	require.Equal(t, 20, *persisted.SkillsRuntime.QuotaPolicies.Tenants["tenant-a"].MaxRequests)
	require.True(t, persisted.SkillsRuntime.ReadOnly)
	require.True(t, persisted.SkillsRuntime.DisableImport)
	require.True(t, persisted.SkillsRuntime.DisablePersist)
	require.True(t, persisted.SkillsRuntime.DisableReloadOps)
	require.True(t, persisted.SkillsRuntime.DisableHotReloadOps)
}

func intPtr(value int) *int {
	return &value
}
