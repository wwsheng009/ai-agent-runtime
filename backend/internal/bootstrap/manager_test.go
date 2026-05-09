package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type bootstrapResourceManager struct{}

func (r *bootstrapResourceManager) SelectResource(retryInfo llm.RetryInfo) (*llm.SelectedResource, error) {
	return &llm.SelectedResource{
		Provider: &llm.ProviderResource{Name: "gateway", Type: "openai", BaseURL: "http://127.0.0.1"},
	}, nil
}

func (r *bootstrapResourceManager) RecordResult(selected *llm.SelectedResource, success bool, err error, statusCode int, latencyMs int64) {
}

type bootstrapMCPManager struct{}

func (m *bootstrapMCPManager) FindTool(toolName string) (skill.ToolInfo, error) {
	return skill.ToolInfo{Name: toolName, Description: toolName, MCPName: "test-mcp", Enabled: true}, nil
}

func (m *bootstrapMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return fmt.Sprintf("%s", toolName), nil
}

func (m *bootstrapMCPManager) ListTools() []skill.ToolInfo {
	return []skill.ToolInfo{{Name: "echo_tool", Description: "echo", MCPName: "test-mcp", Enabled: true}}
}

type bootstrapTarget struct {
	runtimeSet   bool
	sessionSet   bool
	hotSet       bool
	embeddingSet bool
}

func (t *bootstrapTarget) SetLLMRuntime(runtime *llm.LLMRuntime) { t.runtimeSet = runtime != nil }
func (t *bootstrapTarget) SetSessionManager(manager *chat.SessionManager) {
	t.sessionSet = manager != nil
}
func (t *bootstrapTarget) SetHotReload(hotReload *skill.HotReload) { t.hotSet = hotReload != nil }
func (t *bootstrapTarget) SetEmbeddingRouter(router *skill.SemanticEmbeddingRouter) {
	t.embeddingSet = router != nil
}

func TestManager_NewManager_WiresRuntimeComponents(t *testing.T) {
	mcpManager := &bootstrapMCPManager{}
	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: bootstrap-skill
description: bootstrap test
version: 1.0.0
triggers:
  - type: keyword
    values: ["boot"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agent.DefaultModel = "gpt-4o"
	cfg.Agent.Timeout = 12 * time.Second
	cfg.HotReload.Enabled = true
	cfg.HotReload.DebounceDelay = 50 * time.Millisecond

	manager, err := NewManager(&Options{
		Config:          cfg,
		SkillDir:        skillDir,
		MCPManager:      mcpManager,
		ResourceManager: &bootstrapResourceManager{},
	})
	require.NoError(t, err)
	require.NoError(t, manager.Validate())
	t.Cleanup(func() { _ = manager.Stop() })

	assert.Equal(t, 1, manager.Registry().Count())
	assert.NotNil(t, manager.HotReload())
	assert.NotNil(t, manager.SessionManager())
	assert.NotNil(t, manager.EmbeddingRouter())
	assert.Equal(t, skillDir, manager.Loader().GetSkillDir())

	provider, err := manager.LLMRuntime().GetProvider("gateway")
	require.NoError(t, err)
	_, ok := provider.(*llm.GatewayClient)
	assert.True(t, ok)

	stats := manager.HotReload().GetStats()
	assert.Equal(t, true, stats["watching"])
	assert.Equal(t, 1, stats["skillCount"])

	target := &bootstrapTarget{}
	manager.ApplyToSkillsHandler(target)
	assert.True(t, target.runtimeSet)
	assert.True(t, target.sessionSet)
	assert.True(t, target.hotSet)
	assert.True(t, target.embeddingSet)

	stopErr := manager.Stop()
	require.NoError(t, stopErr)
	assert.Equal(t, false, manager.HotReload().GetStats()["watching"])
}

func TestManager_NewManager_RegistersConfiguredProvidersAndAliases(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"real-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello from bootstrap provider"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer upstream.Close()

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agent.DefaultModel = "real-model"
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config: cfg,
		ProviderConfigs: map[string]*llm.ProviderConfig{
			"openai-test": {
				Type:            "openai",
				BaseURL:         upstream.URL,
				DefaultModel:    "real-model",
				SupportedModels: []string{"real-model"},
				Timeout:         2 * time.Second,
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	provider, err := manager.LLMRuntime().GetProvider("real-model")
	require.NoError(t, err)
	require.NotNil(t, provider)

	resp, err := manager.LLMRuntime().Call(context.Background(), &llm.LLMRequest{
		Model: "real-model",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello from bootstrap provider", resp.Content)
}

func TestManager_NewManager_WiresDefaultProvider(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agent.DefaultProvider = "openai-test"
	cfg.Agent.DefaultModel = "real-model"
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config: cfg,
		ProviderConfigs: map[string]*llm.ProviderConfig{
			"openai-test": {
				Type:            "openai",
				BaseURL:         "http://127.0.0.1",
				DefaultModel:    "real-model",
				SupportedModels: []string{"real-model"},
				Timeout:         2 * time.Second,
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	assert.Equal(t, "openai-test", manager.LLMRuntime().DefaultProvider())
	assert.Equal(t, "real-model", manager.LLMRuntime().DefaultModel())
}

func TestManager_NewManager_WiresRuntimeRetryConfigFromProviderConfig(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agent.DefaultProvider = "openai-test"
	cfg.Agent.DefaultModel = "real-model"
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config: cfg,
		ProviderConfigs: map[string]*llm.ProviderConfig{
			"openai-test": {
				Type:         "openai",
				BaseURL:      "http://127.0.0.1",
				DefaultModel: "real-model",
				MaxRetries:   6,
				RetryTuning: llm.RetryTuning{
					BaseDelay:  150 * time.Millisecond,
					MaxDelay:   2 * time.Second,
					Multiplier: 1.7,
				},
				RetryRules: []llm.RetryRule{
					{
						Name:       "http_5xx_retry",
						Enabled:    true,
						MaxRetries: 4,
						StatusCode: llm.RetryStatusCodeMatcher{Range: "500-504"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	maxRetries, tuning, rules := manager.LLMRuntime().RetryConfigSnapshot()
	assert.Equal(t, 6, maxRetries)
	assert.Equal(t, 150*time.Millisecond, tuning.BaseDelay)
	assert.Equal(t, 2*time.Second, tuning.MaxDelay)
	assert.Equal(t, 1.7, tuning.Multiplier)
	require.Len(t, rules, 1)
	assert.Equal(t, "http_5xx_retry", rules[0].Name)
	assert.Equal(t, 4, rules[0].MaxRetries)
	assert.Equal(t, "500-504", rules[0].StatusCode.Range)
}

func TestManager_ReloadProviderConfigsReplacesRuntimeProviders(t *testing.T) {
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"model-v1","choices":[{"index":0,"message":{"role":"assistant","content":"hello from v1"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer firstUpstream.Close()

	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"model-v2","choices":[{"index":0,"message":{"role":"assistant","content":"hello from v2"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer secondUpstream.Close()

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agent.DefaultModel = "model-v1"
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config: cfg,
		ProviderConfigs: map[string]*llm.ProviderConfig{
			"openai-test": {
				Type:            "openai",
				BaseURL:         firstUpstream.URL,
				DefaultModel:    "model-v1",
				SupportedModels: []string{"model-v1"},
				Timeout:         2 * time.Second,
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	require.NoError(t, manager.ReloadProviderConfigs(map[string]*llm.ProviderConfig{
		"openai-test": {
			Type:            "openai",
			BaseURL:         secondUpstream.URL,
			DefaultModel:    "model-v2",
			SupportedModels: []string{"model-v2"},
			Timeout:         2 * time.Second,
		},
	}))

	resp, err := manager.LLMRuntime().Call(context.Background(), &llm.LLMRequest{
		Model: "model-v2",
		Messages: []types.Message{{
			Role:    "user",
			Content: "hello",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello from v2", resp.Content)

	_, err = manager.LLMRuntime().GetProvider("model-v1")
	require.Error(t, err)
}

func TestManager_ReloadProviderConfigsUpdatesRuntimeRetryConfig(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agent.DefaultProvider = "openai-test"
	cfg.Agent.DefaultModel = "model-v1"
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config: cfg,
		ProviderConfigs: map[string]*llm.ProviderConfig{
			"openai-test": {
				Type:         "openai",
				BaseURL:      "http://127.0.0.1",
				DefaultModel: "model-v1",
				MaxRetries:   1,
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	require.NoError(t, manager.ReloadProviderConfigs(map[string]*llm.ProviderConfig{
		"openai-test": {
			Type:         "openai",
			BaseURL:      "http://127.0.0.1",
			DefaultModel: "model-v2",
			MaxRetries:   5,
			RetryTuning: llm.RetryTuning{
				BaseDelay: 250 * time.Millisecond,
			},
			RetryRules: []llm.RetryRule{
				{
					Name:       "rate_limit_retry",
					Enabled:    true,
					MaxRetries: 8,
					Keyword: llm.RetryKeywordMatcher{
						Values: []string{"rate limit"},
					},
				},
			},
		},
	}))

	maxRetries, tuning, rules := manager.LLMRuntime().RetryConfigSnapshot()
	assert.Equal(t, 5, maxRetries)
	assert.Equal(t, 250*time.Millisecond, tuning.BaseDelay)
	require.Len(t, rules, 1)
	assert.Equal(t, "rate_limit_retry", rules[0].Name)
	assert.Equal(t, 8, rules[0].MaxRetries)
	assert.Equal(t, []string{"rate limit"}, rules[0].Keyword.Values)
}

func TestManager_NewManager_BuildsLocalEmbeddingRouter(t *testing.T) {
	mcpManager := &bootstrapMCPManager{}
	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: embedding-skill
description: search customer orders in sap
version: 1.0.0
triggers:
  - type: embedding
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.HotReload.Enabled = false
	cfg.Router.EnableEmbedding = true
	cfg.Embedding.Enabled = true

	manager, err := NewManager(&Options{
		Config:     cfg,
		SkillDir:   skillDir,
		MCPManager: mcpManager,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	require.NotNil(t, manager.EmbeddingRouter())
	results, err := manager.EmbeddingRouter().Route(context.Background(), "search customer orders in sap")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	assert.Equal(t, "embedding-skill", results[0].Skill.Name)
	assert.Equal(t, "embedding", results[0].MatchedBy)
}

func TestManager_NewManager_LoadsMultipleSkillDirs(t *testing.T) {
	mcpManager := &bootstrapMCPManager{}
	systemDir := t.TempDir()
	extraDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(systemDir, "system.yaml"), []byte(`name: system-skill
description: system
version: 1.0.0
triggers:
  - type: keyword
    values: ["system"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(extraDir, "extra.yaml"), []byte(`name: extra-skill
description: extra
version: 1.0.0
triggers:
  - type: keyword
    values: ["extra"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config:     cfg,
		SkillDir:   systemDir,
		SkillDirs:  []string{extraDir},
		MCPManager: mcpManager,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	assert.Equal(t, 2, manager.Registry().Count())
	assert.Equal(t, []string{systemDir, extraDir}, manager.Loader().GetSkillDirs())
	assert.Equal(t, []string{systemDir, extraDir}, manager.SkillDirs())

	systemSkill, ok := manager.Registry().Get("system-skill")
	require.True(t, ok)
	require.NotNil(t, systemSkill.Source)
	assert.Equal(t, skill.SkillSourceLayerSystem, systemSkill.Source.Layer)
	assert.Equal(t, systemDir, systemSkill.Source.Dir)

	extraSkill, ok := manager.Registry().Get("extra-skill")
	require.True(t, ok)
	require.NotNil(t, extraSkill.Source)
	assert.Equal(t, skill.SkillSourceLayerExternal, extraSkill.Source.Layer)
	assert.Equal(t, extraDir, extraSkill.Source.Dir)
}

func TestManager_NewManager_AutoLoadsCodexSkillMdOnStartup(t *testing.T) {
	mcpManager := &bootstrapMCPManager{}
	skillDir := t.TempDir()
	codexSkillDir := filepath.Join(skillDir, "codex-auto-skill")
	metadataDir := filepath.Join(codexSkillDir, "agents")

	require.NoError(t, os.MkdirAll(metadataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(codexSkillDir, "SKILL.md"), []byte(`---
name: codex-auto-skill
description: codex auto load test
metadata:
  short-description: auto load summary
---
You are an auto-loaded Codex skill.
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(metadataDir, "openai.yaml"), []byte(`interface:
  display_name: Codex Auto Skill
  default_prompt: Use the Codex auto skill.
`), 0o644))

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config:     cfg,
		SkillDir:   skillDir,
		MCPManager: mcpManager,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	assert.Nil(t, manager.HotReload())
	assert.Equal(t, 1, manager.Registry().Count())

	loadedSkill, ok := manager.Registry().Get("codex-auto-skill")
	require.True(t, ok)
	require.NotNil(t, loadedSkill)
	require.NotNil(t, loadedSkill.Source)
	assert.Equal(t, skill.SkillSourceFormatCodex, loadedSkill.Source.Format)
	assert.Equal(t, filepath.Join(codexSkillDir, "SKILL.md"), loadedSkill.Source.Path)
	assert.Equal(t, filepath.Join(codexSkillDir, "agents", "openai.yaml"), loadedSkill.Source.MetadataPath)
	assert.Equal(t, "You are an auto-loaded Codex skill.", strings.TrimSpace(loadedSkill.SystemPrompt))
	assert.Equal(t, "auto load summary", loadedSkill.ShortDescription)
	assert.Equal(t, "Codex Auto Skill", loadedSkill.Codex.Interface.DisplayName)
}

func TestManager_NewManager_PersistsSessionsWhenSessionsDirConfigured(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.HotReload.Enabled = false
	cfg.Sessions.Dir = t.TempDir()

	firstManager, err := NewManager(&Options{
		Config: cfg,
	})
	require.NoError(t, err)

	ctx := context.Background()
	session, err := firstManager.SessionManager().CreateSession(ctx, "persist-user")
	require.NoError(t, err)
	require.NoError(t, firstManager.SessionManager().AddMessage(ctx, session.ID, *types.NewUserMessage("persist me")))
	require.NoError(t, firstManager.Stop())

	secondManager, err := NewManager(&Options{
		Config: cfg,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondManager.Stop() })

	restored, err := secondManager.SessionManager().GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, session.ID, restored.ID)
	require.Len(t, restored.History, 1)
	require.Equal(t, "persist me", restored.History[0].Content)
}

func TestManager_NewManager_PrefersFirstSkillDirOnDuplicateNames(t *testing.T) {
	mcpManager := &bootstrapMCPManager{}
	systemDir := t.TempDir()
	extraDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(systemDir, "duplicate.yaml"), []byte(`name: duplicate-skill
description: system duplicate
version: 1.0.0
triggers:
  - type: keyword
    values: ["system"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(extraDir, "duplicate.yaml"), []byte(`name: duplicate-skill
description: external duplicate
version: 1.0.0
triggers:
  - type: keyword
    values: ["external"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.HotReload.Enabled = false

	manager, err := NewManager(&Options{
		Config:     cfg,
		SkillDir:   systemDir,
		SkillDirs:  []string{extraDir},
		MCPManager: mcpManager,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	assert.Equal(t, 1, manager.Registry().Count())
	loadedSkill, ok := manager.Registry().Get("duplicate-skill")
	require.True(t, ok)
	assert.Equal(t, "system duplicate", loadedSkill.Description)
	assert.Equal(t, skill.SkillSourceLayerSystem, loadedSkill.Source.Layer)
}

func TestManager_NewManager_DiscoverOnlyRegistersPromptLazyStubs(t *testing.T) {
	mcpManager := &bootstrapMCPManager{}
	skillDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: lazy-bootstrap-skill
description: lazy bootstrap test
version: 1.0.0
triggers:
  - type: keyword
    values: ["lazy"]
    weight: 1
tools: ["echo_tool"]
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are lazily discovered."), 0o644))

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.HotReload.Enabled = true
	cfg.Router.EnableEmbedding = true
	cfg.Embedding.Enabled = true

	manager, err := NewManager(&Options{
		Config:       cfg,
		SkillDir:     skillDir,
		DiscoverOnly: true,
		MCPManager:   mcpManager,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Stop() })

	assert.Nil(t, manager.HotReload())
	item, ok := manager.Registry().Get("lazy-bootstrap-skill")
	require.True(t, ok)
	require.NotNil(t, item)
	assert.Equal(t, "", item.SystemPrompt)
	assert.Equal(t, "", item.UserPrompt)
	require.NotNil(t, item.Source)
	assert.Equal(t, filepath.Join(skillDir, "prompt.md"), item.Source.PromptPath)
	require.NotNil(t, manager.EmbeddingRouter())
}
