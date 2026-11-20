package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryassembly"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// A single process can host several ChatSessions in the same workspace. Keep
// one memory runtime/store/ticker per workspace+memory-config identity instead
// of opening a new SQLite connection for every session host.
var (
	sharedMemoryRuntimeBuilder          = memoryassembly.BuildFromRuntimeConfig
	sharedMemoryMaintenanceStarter      = memoryassembly.StartMaintenance
	sharedMemoryRuntimeRegistryInstance = &sharedMemoryRuntimeRegistry{
		entries: make(map[string]*sharedMemoryRuntimeEntry),
	}
)

type sharedMemoryRuntimeEntry struct {
	runtime         *memoryruntime.Runtime
	stopMaintenance func()
	refs            int
}

type sharedMemoryRuntimeRegistry struct {
	mu      sync.Mutex
	entries map[string]*sharedMemoryRuntimeEntry
}

type sharedMemoryMaintenanceHost struct {
	runtime *memoryruntime.Runtime
}

func (h sharedMemoryMaintenanceHost) RunConsolidation(ctx context.Context) error {
	if h.runtime == nil || h.runtime.Store == nil {
		return nil
	}
	_, err := memoryruntime.NewConsolidator(h.runtime.Store, h.runtime.Config).Run(ctx)
	return err
}

func acquireSharedMemoryRuntime(opts memoryassembly.BuildOptions) (*memoryruntime.Runtime, func(), error) {
	if opts.RuntimeConfig == nil {
		return nil, nil, nil
	}
	mode := strings.ToLower(strings.TrimSpace(opts.RuntimeConfig.Memory.Mode))
	if mode != "shadow" && mode != "on" {
		return nil, nil, nil
	}
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	workspace = filepath.Clean(workspace)
	opts.Workspace = workspace

	key, err := sharedMemoryRuntimeKey(opts)
	if err != nil {
		return nil, nil, err
	}
	registry := sharedMemoryRuntimeRegistryInstance
	registry.mu.Lock()
	if entry := registry.entries[key]; entry != nil {
		entry.refs++
		runtime := entry.runtime
		registry.mu.Unlock()
		return runtime, func() { releaseSharedMemoryRuntime(key) }, nil
	}

	// Build under the registry lock: construction only opens local stores and
	// never performs provider work, and this prevents two hosts from racing two
	// runtimes against the same SQLite file.
	runtime, buildErr := sharedMemoryRuntimeBuilder(opts)
	if buildErr != nil {
		registry.mu.Unlock()
		return nil, nil, buildErr
	}
	if runtime == nil {
		registry.mu.Unlock()
		return nil, nil, nil
	}
	maintenanceCtx, cancel := context.WithCancel(context.Background())
	stopMaintenance := sharedMemoryMaintenanceStarter(maintenanceCtx, memoryassembly.MaintenanceOptions{
		Runtime: runtime,
		Config:  opts.RuntimeConfig,
		Host:    sharedMemoryMaintenanceHost{runtime: runtime},
		OnError: func(stage string, err error) {
			logpkg.Warnf("memory maintenance failed: stage=%s: %v", stage, err)
		},
	})
	entry := &sharedMemoryRuntimeEntry{
		runtime: runtime,
		stopMaintenance: func() {
			cancel()
			if stopMaintenance != nil {
				stopMaintenance()
			}
		},
		refs: 1,
	}
	registry.entries[key] = entry
	registry.mu.Unlock()
	return runtime, func() { releaseSharedMemoryRuntime(key) }, nil
}

func releaseSharedMemoryRuntime(key string) {
	registry := sharedMemoryRuntimeRegistryInstance
	registry.mu.Lock()
	entry := registry.entries[key]
	if entry == nil {
		registry.mu.Unlock()
		return
	}
	entry.refs--
	if entry.refs > 0 {
		registry.mu.Unlock()
		return
	}
	delete(registry.entries, key)
	registry.mu.Unlock()
	entry.stopMaintenance()
	if entry.runtime.Store != nil {
		_ = entry.runtime.Store.Close()
	}
}

func sharedMemoryRuntimeKey(opts memoryassembly.BuildOptions) (string, error) {
	memoryJSON, err := json.Marshal(opts.RuntimeConfig.Memory)
	if err != nil {
		return "", fmt.Errorf("memory runtime key: %w", err)
	}
	return fmt.Sprintf("%s|%s|%s|%p|%p",
		filepath.Clean(opts.Workspace), string(memoryJSON), strings.TrimSpace(opts.UserID),
		opts.SessionManager, opts.LLMRuntime), nil
}
