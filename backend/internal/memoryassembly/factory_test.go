package memoryassembly

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
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
	cfg.Memory.Injection.Budgets = runtimecfg.MemoryInjectionBudgets{
		Core: 0.25, Task: 0.15, Retrieved: 0.35, Procedure: 0.15, Reference: 0.10,
	}
	cfg.Memory.Core.Rebuild = runtimecfg.MemoryCoreRebuildConfig{
		MaxRecords: 24, MaxUsageRows: 120, TokenBudget: 900,
	}
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
	require.Equal(t, 24, rt.Config.CoreRebuildMaxRecords)
	require.Equal(t, 120, rt.Config.CoreRebuildMaxUsageRows)
	require.Equal(t, 900, rt.Config.CoreRebuildTokenBudget)
	for i, kind := range []string{"task", "procedure", "reference"} {
		candidate, err := rt.Store.InsertCandidate(context.Background(), memorystore.Candidate{
			SessionID: "factory-budget-test", Kind: kind, Text: "candidate " + kind,
			Confidence: 0.9, SourceHash: kind + "-hash", SourceRevision: int64(i + 1),
		})
		require.NoError(t, err)
		active := memorystore.StatusActive
		_, err = rt.Store.UpdateRecord(context.Background(), candidate.ID,
			memorystore.RecordPatch{Status: &active})
		require.NoError(t, err)
	}
	selection, err := rt.Store.SelectForInjectV2(memorystore.InjectOptions{TokenBudget: 100})
	require.NoError(t, err)
	require.Equal(t, map[string]int{
		memorystore.InjectionCategoryCore:      0,
		memorystore.InjectionCategoryTask:      15,
		memorystore.InjectionCategoryRetrieved: 60,
		memorystore.InjectionCategoryProcedure: 15,
		memorystore.InjectionCategoryReference: 10,
	}, selection.CategoryBudget)
	require.NotNil(t, rt.WindowSummarizer)
	source, ok := rt.Source.(*memoryruntime.ChatSessionSource)
	require.True(t, ok)
	require.Equal(t, "user-1", source.UserID)
}

func TestBuildFromRuntimeConfigWiresMemoryStageProviders(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "on"
	cfg.Memory.StorePath = filepath.Join(t.TempDir(), "memory.db")
	cfg.Memory.Generation.Provider = "extract-provider"
	cfg.Memory.Generation.Model = "extract-model"
	cfg.Memory.Consolidation.Provider = "summary-provider"
	cfg.Memory.Consolidation.Model = "summary-model"

	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "default-model"})

	rt, err := BuildFromRuntimeConfig(BuildOptions{
		RuntimeConfig:  cfg,
		Workspace:      t.TempDir(),
		SessionManager: manager,
		LLMRuntime:     llmRuntime,
		UserID:         "user-1",
		WorkerID:       "worker-test",
	})
	require.NoError(t, err)
	require.NotNil(t, rt)
	defer rt.Store.Close()

	require.Equal(t, "extract-provider", rt.Config.ExtractionProvider)
	require.Equal(t, "extract-model", rt.Config.ExtractionModel)
	require.Equal(t, "summary-provider", rt.Config.ConsolidationProvider)
	require.Equal(t, "summary-model", rt.Config.ConsolidationModel)

	extractor, ok := rt.Extractor.(*memoryruntime.LLMExtractor)
	require.True(t, ok)
	require.Equal(t, "extract-provider", extractor.Config.ExtractionProvider)
	require.Equal(t, "extract-model", extractor.Config.ExtractionModel)

	windowSummarizer, ok := rt.WindowSummarizer.(*memoryruntime.LLMWindowSummarizer)
	require.True(t, ok)
	require.Equal(t, "extract-provider", windowSummarizer.Config.ExtractionProvider)
	require.Equal(t, "extract-model", windowSummarizer.Config.ExtractionModel)

	projectSummarizer, ok := rt.Config.Summarizer.(*memoryruntime.LLMProjectSummarizer)
	require.True(t, ok)
	require.Equal(t, "summary-provider", projectSummarizer.Config.ConsolidationProvider)
	require.Equal(t, "summary-model", projectSummarizer.Config.ConsolidationModel)
}

func TestBuildFromRuntimeConfigConsolidationProviderFallsBackToExtraction(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "on"
	cfg.Memory.StorePath = filepath.Join(t.TempDir(), "memory.db")
	cfg.Memory.Generation.Provider = "extract-provider"
	cfg.Memory.Generation.Model = "extract-model"
	cfg.Memory.Consolidation.Provider = ""
	cfg.Memory.Consolidation.Model = ""

	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "default-model"})

	rt, err := BuildFromRuntimeConfig(BuildOptions{
		RuntimeConfig:  cfg,
		Workspace:      t.TempDir(),
		SessionManager: manager,
		LLMRuntime:     llmRuntime,
		UserID:         "user-1",
		WorkerID:       "worker-test",
	})
	require.NoError(t, err)
	require.NotNil(t, rt)
	defer rt.Store.Close()

	projectSummarizer, ok := rt.Config.Summarizer.(*memoryruntime.LLMProjectSummarizer)
	require.True(t, ok)
	require.Empty(t, projectSummarizer.Config.ConsolidationProvider)
	require.Empty(t, projectSummarizer.Config.ConsolidationModel)
	require.Equal(t, "extract-provider", projectSummarizer.Config.ExtractionProvider)
	require.Equal(t, "extract-model", projectSummarizer.Config.ExtractionModel)
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
