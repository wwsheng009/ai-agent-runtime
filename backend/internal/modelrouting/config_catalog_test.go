package modelrouting

import (
	"testing"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestConfigCatalogRejectsAmbiguousModelAliases(t *testing.T) {
	cfg := &agentconfig.Config{}
	cfg.Providers.Items = map[string]agentconfig.Provider{
		"alpha": {
			Enabled:         true,
			DefaultModel:    "shared-model",
			SupportedModels: []string{"shared-model", "alpha-only"},
		},
		"beta": {
			Enabled:         true,
			DefaultModel:    "shared-model",
			SupportedModels: []string{"shared-model", "beta-only"},
		},
	}

	catalog := NewConfigCatalog(cfg)

	require.Equal(t, "alpha", catalog.ResolveProviderName("ALPHA"))
	require.Equal(t, "alpha", catalog.ResolveProviderName("alpha-only"))
	require.Equal(t, "beta", catalog.ResolveProviderName("beta-only"))
	require.Empty(t, catalog.ResolveProviderName("shared-model"))
}

func TestConfigCatalogProviderNameWinsOverModelAlias(t *testing.T) {
	cfg := &agentconfig.Config{}
	cfg.Providers.Items = map[string]agentconfig.Provider{
		"alpha":   {Enabled: true, DefaultModel: "model-a"},
		"model-a": {Enabled: true, DefaultModel: "model-b"},
	}

	catalog := NewConfigCatalog(cfg)

	require.Equal(t, "model-a", catalog.ResolveProviderName("model-a"))
}

func TestConfigCatalogResolvesMappedCapabilities(t *testing.T) {
	cfg := &agentconfig.Config{}
	cfg.Providers.Items = map[string]agentconfig.Provider{
		"alpha": {
			Enabled:      true,
			DefaultModel: "friendly",
			ModelMappings: map[string]string{
				"friendly": "canonical",
			},
			ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
				"canonical": {
					ReasoningModel:   true,
					ReasoningEfforts: []string{"low", "high"},
				},
			},
		},
	}

	catalog := NewConfigCatalog(cfg)

	require.Equal(t, "canonical", catalog.DefaultModel("alpha"))
	supported, known := catalog.SupportsReasoningEffort("alpha", "friendly", "high")
	require.True(t, known)
	require.True(t, supported)
	supported, known = catalog.SupportsReasoningEffort("alpha", "friendly", "max")
	require.True(t, known)
	require.False(t, supported)
}
