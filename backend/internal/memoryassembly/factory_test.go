package memoryassembly

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
)

func TestBuildFromRuntimeConfigDisabledCreatesNothing(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "off"
	rt, err := BuildFromRuntimeConfig(BuildOptions{RuntimeConfig: cfg})
	require.NoError(t, err)
	require.Nil(t, rt)
}

func TestBuildFromRuntimeConfigWiresSharedMemoryRuntime(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "on"
	cfg.Memory.StorePath = filepath.Join(t.TempDir(), "memory.db")
	cfg.Memory.Injection.Enabled = true
	cfg.Memory.Manual.Enabled = true
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model"})
	workspace := t.TempDir()

	rt, err := BuildFromRuntimeConfig(BuildOptions{
		RuntimeConfig:  cfg,
		Workspace:      workspace,
		SessionManager: manager,
		LLMRuntime:     llmRuntime,
		UserID:         "user-1",
		WorkerID:       "worker-test",
	})
	require.NoError(t, err)
	require.NotNil(t, rt)
	defer rt.Store.Close()
	require.Equal(t, "worker-test", rt.WorkerID)
	require.Equal(t, workspace, rt.Config.Workspace)
	require.NotNil(t, rt.WindowSummarizer)
	source, ok := rt.Source.(*memoryruntime.ChatSessionSource)
	require.True(t, ok)
	require.Equal(t, "user-1", source.UserID)
}

func TestBuildFromRuntimeConfigRequiresHosts(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "on"
	_, err := BuildFromRuntimeConfig(BuildOptions{RuntimeConfig: cfg})
	require.ErrorContains(t, err, "session manager")

	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	_, err = BuildFromRuntimeConfig(BuildOptions{RuntimeConfig: cfg, SessionManager: manager})
	require.ErrorContains(t, err, "LLM runtime")
}
