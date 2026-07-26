package commands

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
)

// ClearPluginCatalogCache is retained for tests/CLI that previously expected a
// process-local discovery cache. Discovery is intentionally uncached so
// install/trust/enable changes are visible in the same process and tests do not
// leak plugin roots across cases.
func ClearPluginCatalogCache() {}

// discoverRuntimePluginCatalog loads the local plugin catalog with trust state applied.
// Failures return (nil, err); callers should fail open (keep base dirs) so missing
// plugins never break default multi-agent/skills behavior.
//
// When folder trust blocks project scope, project plugin roots (.aicli/plugins,
// .agents/plugins) are skipped; user-home plugins remain available.
func discoverRuntimePluginCatalog() (*plugins.Catalog, error) {
	opts := plugins.DiscoverOptions{
		State: plugins.NewStateStore(""),
	}
	if projectScopeAllowed() {
		if cwd, err := os.Getwd(); err == nil {
			opts.ProjectRoot = cwd
		}
	} else {
		opts.SkipProjectRoot = true
	}
	return plugins.Discover(opts)
}

// mergeActivePluginSkillDirs appends skill dirs from trusted+enabled plugins.
func mergeActivePluginSkillDirs(base []string) []string {
	catalog, err := discoverRuntimePluginCatalog()
	if err != nil || catalog == nil {
		return base
	}
	return plugins.MergeSkillDirs(base, catalog)
}

// mergeActivePluginAgentDirs appends agent definition dirs from active plugins.
func mergeActivePluginAgentDirs(base []string) []string {
	catalog, err := discoverRuntimePluginCatalog()
	if err != nil || catalog == nil {
		return base
	}
	return plugins.MergeAgentDirs(base, catalog)
}

// mergeActivePluginHooks appends hooks from active plugins after base hooks.
func mergeActivePluginHooks(base []hooks.HookConfig) []hooks.HookConfig {
	catalog, err := discoverRuntimePluginCatalog()
	if err != nil || catalog == nil {
		return base
	}
	return plugins.MergeHooks(base, catalog)
}

// defaultPluginCLIHome resolves plugins home for CLI commands (respects AICLI_HOME).
func defaultPluginCLIHome() string {
	return plugins.DefaultPluginsHome()
}

// defaultPluginStateStore opens the durable trust store under plugins home.
func defaultPluginStateStore(pluginsHome string) *plugins.StateStore {
	home := strings.TrimSpace(pluginsHome)
	if home == "" {
		home = defaultPluginCLIHome()
	}
	return plugins.NewStateStore(filepath.Join(home, plugins.StateFileName))
}
