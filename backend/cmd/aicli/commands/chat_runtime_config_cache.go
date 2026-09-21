package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// Process-local cache for runtime YAML/JSON loads during a single aicli
// process. Chat startup currently reloads the same runtime config from disk in
// several independent helpers (persistence, tools, skills, actor host). Caching
// by absolute path + mtime keeps those reloads cheap without introducing a
// long-lived shared RuntimeManager. Merged loads key the cache on the full
// layer fingerprint instead of a single path.
type chatRuntimeConfigCacheEntry struct {
	modTime time.Time
	size    int64
	// fingerprint identifies a merged-layer input set. Empty for single-file
	// entries, which are validated by modTime+size instead.
	fingerprint string
	config      *runtimecfg.RuntimeConfig
	path        string
	err         error
}

var (
	chatRuntimeConfigCacheMu sync.Mutex
	chatRuntimeConfigCache   = map[string]chatRuntimeConfigCacheEntry{}
)

// loadCachedRuntimeConfig loads the runtime config selected by configPath.
//
// The .aicli layers (./.aicli/runtime.yaml, ~/.aicli/runtime.yaml) are merged:
// the project layer overrides only the keys it explicitly writes, so a project
// file that sets one field no longer discards the user's remaining settings.
// Any other path is an explicit single-file selection and is loaded as-is.
func loadCachedRuntimeConfig(configPath string) (*runtimecfg.RuntimeConfig, string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, "", nil
	}
	if chatRuntimeConfigLayerMatch(configPath) {
		return loadMergedChatRuntimeConfig()
	}
	return loadSingleFileChatRuntimeConfig(configPath)
}

// loadMergedChatRuntimeConfig merges every present .aicli runtime.yaml layer
// (user < project) and decodes the result with the same validation the
// single-file loader applies. The returned path is the effective source: the
// highest present layer, which is also the file a write belongs to.
func loadMergedChatRuntimeConfig() (*runtimecfg.RuntimeConfig, string, error) {
	document, err := agentconfig.LoadMergedRuntimeConfigDocument()
	if err != nil {
		return nil, "", err
	}
	if document == nil || len(document.PresentLayers) == 0 {
		// No layer on disk: soft miss, callers use the built-in defaults.
		return nil, "", nil
	}
	sourcePath := strings.TrimSpace(document.SourcePath)
	fingerprint := chatRuntimeConfigLayersFingerprint(document.PresentLayers)

	chatRuntimeConfigCacheMu.Lock()
	if entry, ok := chatRuntimeConfigCache[fingerprint]; ok && entry.err == nil && entry.config != nil {
		cloned := cloneRuntimeConfig(entry.config)
		loadedPath := entry.path
		chatRuntimeConfigCacheMu.Unlock()
		return cloned, loadedPath, nil
	}
	chatRuntimeConfigCacheMu.Unlock()

	manager := runtimecfg.NewRuntimeManager(sourcePath)
	if err := manager.LoadDocument(document.MergedYAML, sourcePath); err != nil {
		chatRuntimeConfigCacheMu.Lock()
		chatRuntimeConfigCache[fingerprint] = chatRuntimeConfigCacheEntry{
			fingerprint: fingerprint,
			path:        sourcePath,
			err:         err,
		}
		chatRuntimeConfigCacheMu.Unlock()
		return nil, sourcePath, err
	}
	config := manager.Get()
	if loadedPath := strings.TrimSpace(manager.GetFilePath()); loadedPath != "" {
		sourcePath = loadedPath
	}

	chatRuntimeConfigCacheMu.Lock()
	chatRuntimeConfigCache[fingerprint] = chatRuntimeConfigCacheEntry{
		fingerprint: fingerprint,
		config:      cloneRuntimeConfig(config),
		path:        sourcePath,
	}
	chatRuntimeConfigCacheMu.Unlock()

	return cloneRuntimeConfig(config), sourcePath, nil
}

// chatRuntimeConfigLayerMatch reports whether configPath is one of the .aicli
// runtime.yaml layers that must be merged. Session/profile paths pointing at
// other files (or at a stale workspace layer) stay single-file selections.
func chatRuntimeConfigLayerMatch(configPath string) bool {
	target := normalizeChatPathForCompare(configPath)
	if target == "" {
		return false
	}
	for _, layer := range agentconfig.RuntimeConfigLayerStack() {
		if normalizeChatPathForCompare(layer.Path) == target {
			return true
		}
	}
	return false
}

// normalizeChatPathForCompare makes a config path comparable across the
// absolute/relative spellings callers use. Windows paths compare
// case-insensitively.
func normalizeChatPathForCompare(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil && absolute != "" {
		path = absolute
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// chatRuntimeConfigLayersFingerprint identifies the exact input set of a merged
// load: path, mtime and size of every present layer.
func chatRuntimeConfigLayersFingerprint(layers []agentconfig.ConfigLayer) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		info, err := os.Stat(layer.Path)
		if err != nil || info == nil {
			parts = append(parts, layer.Path+"|missing")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s|%d|%d", layer.Path, info.ModTime().UnixNano(), info.Size()))
	}
	return "merge:" + strings.Join(parts, ";")
}

// loadSingleFileChatRuntimeConfig loads exactly one runtime.yaml/JSON file.
func loadSingleFileChatRuntimeConfig(configPath string) (*runtimecfg.RuntimeConfig, string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, "", nil
	}

	info, statErr := os.Stat(configPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			// Soft miss: callers may fall back to DefaultRuntimeConfig without
			// treating a missing optional runtime.yaml as a hard error.
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

// formatRuntimeConfigLoadFallback explains why a runtime config load fell back
// to defaults. loadCachedRuntimeConfig returns (nil, path, nil) when the file
// is missing, so callers must not print bare %v of a nil error ("<nil>").
func formatRuntimeConfigLoadFallback(configPath string, err error) string {
	path := strings.TrimSpace(configPath)
	if err != nil {
		if path != "" {
			return fmt.Sprintf("%s: %v", path, err)
		}
		return err.Error()
	}
	if path != "" {
		return fmt.Sprintf("未找到配置文件: %s", path)
	}
	return "配置为空"
}
