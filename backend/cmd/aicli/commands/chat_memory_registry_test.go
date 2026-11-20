package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestSharedMemoryRuntimeReusesWorkspaceRuntime(t *testing.T) {
	registry := sharedMemoryRuntimeRegistryInstance
	registry.mu.Lock()
	registry.entries = make(map[string]*sharedMemoryRuntimeEntry)
	registry.mu.Unlock()
	t.Cleanup(drainSharedMemoryRuntimeRegistryForTest)

	store, err := memorystore.New(memorystore.Config{Root: t.TempDir(), Mode: "on"})
	require.NoError(t, err)
	runtime := &memoryruntime.Runtime{Store: store}

	originalBuilder := sharedMemoryRuntimeBuilder
	originalStarter := sharedMemoryMaintenanceStarter
	t.Cleanup(func() {
		sharedMemoryRuntimeBuilder = originalBuilder
		sharedMemoryMaintenanceStarter = originalStarter
	})
	buildCalls := 0
	startCalls := 0
	stopCalls := 0
	sharedMemoryRuntimeBuilder = func(memoryassembly.BuildOptions) (*memoryruntime.Runtime, error) {
		buildCalls++
		return runtime, nil
	}
	sharedMemoryMaintenanceStarter = func(context.Context, memoryassembly.MaintenanceOptions) func() {
		startCalls++
		return func() { stopCalls++ }
	}

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "on"
	opts := memoryassembly.BuildOptions{
		RuntimeConfig:  cfg,
		Workspace:      t.TempDir(),
		SessionManager: &runtimechat.SessionManager{},
		LLMRuntime:     &llm.LLMRuntime{},
		UserID:         "u1",
		WorkerID:       "test-worker",
	}
	first, releaseFirst, err := acquireSharedMemoryRuntime(opts)
	require.NoError(t, err)
	require.Same(t, runtime, first)
	require.Equal(t, 1, buildCalls)
	require.Equal(t, 1, startCalls)

	second, releaseSecond, err := acquireSharedMemoryRuntime(opts)
	require.NoError(t, err)
	require.Same(t, runtime, second)
	require.Equal(t, 1, buildCalls)
	require.Equal(t, 1, startCalls)

	releaseFirst()
	require.Zero(t, stopCalls, "shared runtime must stay alive while a host still holds it")
	page, err := store.ListRecords(context.Background(), memorystore.RecordFilter{Limit: 1})
	require.NoError(t, err)
	require.Zero(t, page.Count)

	releaseSecond()
	require.Equal(t, 1, stopCalls)
	_, err = store.ListRecords(context.Background(), memorystore.RecordFilter{Limit: 1})
	require.Error(t, err, "last release must close the shared store")
}

func drainSharedMemoryRuntimeRegistryForTest() {
	registry := sharedMemoryRuntimeRegistryInstance
	registry.mu.Lock()
	entries := registry.entries
	registry.entries = make(map[string]*sharedMemoryRuntimeEntry)
	registry.mu.Unlock()
	for _, entry := range entries {
		if entry.stopMaintenance != nil {
			entry.stopMaintenance()
		}
		if entry.runtime != nil && entry.runtime.Store != nil {
			_ = entry.runtime.Store.Close()
		}
	}
}
