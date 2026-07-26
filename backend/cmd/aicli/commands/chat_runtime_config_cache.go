package commands

import (
	"os"
	"strings"
	"sync"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// Process-local cache for runtime YAML/JSON loads during a single aicli
// process. Chat startup currently reloads the same runtime config from disk in
// several independent helpers (persistence, tools, skills, actor host). Caching
// by absolute path + mtime keeps those reloads cheap without introducing a
// long-lived shared RuntimeManager.
type chatRuntimeConfigCacheEntry struct {
	modTime time.Time
	size    int64
	config  *runtimecfg.RuntimeConfig
	path    string
	err     error
}

var (
	chatRuntimeConfigCacheMu sync.Mutex
	chatRuntimeConfigCache   = map[string]chatRuntimeConfigCacheEntry{}
)

func loadCachedRuntimeConfig(configPath string) (*runtimecfg.RuntimeConfig, string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, "", nil
	}

	info, statErr := os.Stat(configPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, configPath, nil
		}
		return nil, configPath, statErr
	}

	chatRuntimeConfigCacheMu.Lock()
	if entry, ok := chatRuntimeConfigCache[configPath]; ok {
		if entry.err == nil && entry.config != nil &&
			entry.modTime.Equal(info.ModTime()) && entry.size == info.Size() {
			cloned := cloneRuntimeConfig(entry.config)
			loadedPath := entry.path
			chatRuntimeConfigCacheMu.Unlock()
			return cloned, loadedPath, nil
		}
	}
	chatRuntimeConfigCacheMu.Unlock()

	manager := runtimecfg.NewRuntimeManager(configPath)
	if err := manager.Load(); err != nil {
		chatRuntimeConfigCacheMu.Lock()
		chatRuntimeConfigCache[configPath] = chatRuntimeConfigCacheEntry{
			modTime: info.ModTime(),
			size:    info.Size(),
			path:    configPath,
			err:     err,
		}
		chatRuntimeConfigCacheMu.Unlock()
		return nil, configPath, err
	}
	config := manager.Get()
	loadedPath := manager.GetFilePath()
	if strings.TrimSpace(loadedPath) == "" {
		loadedPath = configPath
	}

	chatRuntimeConfigCacheMu.Lock()
	chatRuntimeConfigCache[configPath] = chatRuntimeConfigCacheEntry{
		modTime: info.ModTime(),
		size:    info.Size(),
		config:  cloneRuntimeConfig(config),
		path:    loadedPath,
	}
	chatRuntimeConfigCacheMu.Unlock()

	return cloneRuntimeConfig(config), loadedPath, nil
}

func cloneRuntimeConfig(cfg *runtimecfg.RuntimeConfig) *runtimecfg.RuntimeConfig {
	if cfg == nil {
		return nil
	}
	// RuntimeConfig is a large value type; shallow-copy the struct so callers
	// can mutate top-level fields (workspace root, default model, sandbox
	// overlay) without racing the cached baseline. Nested maps/slices remain
	// shared; current chat loaders only mutate scalar/top-level fields after load.
	cloned := *cfg
	return &cloned
}

func resetChatRuntimeConfigCacheForTest() {
	chatRuntimeConfigCacheMu.Lock()
	chatRuntimeConfigCache = map[string]chatRuntimeConfigCacheEntry{}
	chatRuntimeConfigCacheMu.Unlock()
}
