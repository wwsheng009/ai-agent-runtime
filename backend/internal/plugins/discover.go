package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// DiscoverOptions configures multi-root plugin discovery.
type DiscoverOptions struct {
	// ProjectRoot defaults to cwd.
	ProjectRoot string
	// SkipProjectRoot skips .aicli/plugins and .agents/plugins under ProjectRoot.
	// Used by folder-trust gating when the workspace is untrusted.
	SkipProjectRoot bool
	// UserHome overrides home for ~/.aicli/plugins.
	UserHome string
	// PluginsHome overrides the user plugins install root.
	PluginsHome string
	// ExtraDirs are additional plugin roots (each entry is a plugin dir or a parent of plugins).
	ExtraDirs []string
	// State is optional trust store; when set, trust/enable overlay is applied.
	State *StateStore
	// IncludeUntrusted still lists untrusted packages (default true for list).
	// Active-only filtering is done via Catalog.Active*.
	IncludeDisabled bool
}

// Catalog is the merged set of plugins keyed by normalized name.
// Later discovery roots override earlier ones for the same name.
type Catalog struct {
	ByName map[string]*Package
	Order  []string
}

// Get returns a package by name.
func (c *Catalog) Get(name string) (*Package, bool) {
	if c == nil || c.ByName == nil {
		return nil, false
	}
	pkg, ok := c.ByName[normalizePluginID(name)]
	return pkg, ok
}

// List returns packages in sorted name order.
func (c *Catalog) List() []*Package {
	if c == nil {
		return nil
	}
	out := make([]*Package, 0, len(c.Order))
	for _, name := range c.Order {
		if pkg, ok := c.ByName[name]; ok {
			out = append(out, pkg)
		}
	}
	return out
}

// ActiveSkillDirs returns skill dirs from trusted+enabled plugins (stable order).
func (c *Catalog) ActiveSkillDirs() []string {
	return c.collectActive(func(p *Package) []string { return p.ActiveContributions().SkillDirs })
}

// ActiveAgentDirs returns agent def dirs from trusted+enabled plugins.
func (c *Catalog) ActiveAgentDirs() []string {
	return c.collectActive(func(p *Package) []string { return p.ActiveContributions().AgentDirs })
}

// ActiveHooks merges hooks from trusted+enabled plugins (later plugins append).
func (c *Catalog) ActiveHooks() []hooks.HookConfig {
	if c == nil {
		return nil
	}
	var out []hooks.HookConfig
	for _, pkg := range c.List() {
		if !pkg.IsActive() {
			continue
		}
		out = append(out, pkg.ActiveContributions().Hooks...)
	}
	return out
}

// ActiveMCPConfigs returns MCP configs from active plugins.
func (c *Catalog) ActiveMCPConfigs() []*mcpconfig.Config {
	if c == nil {
		return nil
	}
	var out []*mcpconfig.Config
	for _, pkg := range c.List() {
		if !pkg.IsActive() {
			continue
		}
		if cfg := pkg.ActiveContributions().MCP; cfg != nil {
			out = append(out, cfg)
		}
	}
	return out
}

func (c *Catalog) collectActive(fn func(*Package) []string) []string {
	if c == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, pkg := range c.List() {
		if !pkg.IsActive() {
			continue
		}
		for _, dir := range fn(pkg) {
			dir = filepath.Clean(strings.TrimSpace(dir))
			if dir == "" {
				continue
			}
			if _, ok := seen[dir]; ok {
				continue
			}
			seen[dir] = struct{}{}
			out = append(out, dir)
		}
	}
	return out
}

// Discover loads plugins from standard roots + extra dirs.
//
// Priority (later overrides earlier for same name):
//  1. ~/.aicli/plugins/* (or PluginsHome)
//  2. <project>/.aicli/plugins/*
//  3. <project>/.agents/plugins/*
//  4. ExtraDirs (each may be a plugin root or a parent containing plugin children)
func Discover(opts DiscoverOptions) (*Catalog, error) {
	catalog := &Catalog{ByName: make(map[string]*Package)}

	pluginsHome := strings.TrimSpace(opts.PluginsHome)
	if pluginsHome == "" {
		if home := strings.TrimSpace(opts.UserHome); home != "" {
			pluginsHome = filepath.Join(home, ".aicli", "plugins")
		} else {
			pluginsHome = DefaultPluginsHome()
		}
	}
	if err := loadPluginsParent(catalog, pluginsHome, opts); err != nil {
		return nil, err
	}

	projectRoot := strings.TrimSpace(opts.ProjectRoot)
	if projectRoot == "" && !opts.SkipProjectRoot {
		if cwd, err := os.Getwd(); err == nil {
			projectRoot = cwd
		}
	}
	if projectRoot != "" && !opts.SkipProjectRoot {
		for _, rel := range []string{
			filepath.Join(".aicli", "plugins"),
			filepath.Join(".agents", "plugins"),
		} {
			if err := loadPluginsParent(catalog, filepath.Join(projectRoot, rel), opts); err != nil {
				return nil, err
			}
		}
	}

	for _, dir := range opts.ExtraDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		dir = aiclipaths.ExpandUserPath(dir)
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		// ExtraDirs may be a single plugin root or a parent.
		if _, err := FindManifestPath(dir); err == nil {
			if err := putPackage(catalog, dir, opts); err != nil {
				return nil, err
			}
			continue
		}
		if err := loadPluginsParent(catalog, dir, opts); err != nil {
			return nil, err
		}
	}

	catalog.rebuildOrder()
	return catalog, nil
}

func loadPluginsParent(catalog *Catalog, parent string, opts DiscoverOptions) error {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return nil
	}
	info, err := os.Stat(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	// Parent itself may be a plugin.
	if _, err := FindManifestPath(parent); err == nil {
		return putPackage(catalog, parent, opts)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		child := filepath.Join(parent, name)
		if _, err := FindManifestPath(child); err != nil {
			continue
		}
		if err := putPackage(catalog, child, opts); err != nil {
			return err
		}
	}
	return nil
}

func putPackage(catalog *Catalog, root string, opts DiscoverOptions) error {
	pkg, err := Load(root, LoadOptions{RequireManifest: true})
	if err != nil {
		return fmt.Errorf("load plugin %s: %w", root, err)
	}
	if opts.State != nil {
		if err := opts.State.ApplyToPackage(pkg); err != nil {
			return err
		}
	}
	if !opts.IncludeDisabled && !pkg.Enabled {
		return nil
	}
	key := normalizePluginID(pkg.Manifest.Name)
	catalog.ByName[key] = pkg
	return nil
}

func (c *Catalog) rebuildOrder() {
	if c == nil {
		return
	}
	c.Order = make([]string, 0, len(c.ByName))
	for name := range c.ByName {
		c.Order = append(c.Order, name)
	}
	sort.Strings(c.Order)
}

// MergeSkillDirs appends active plugin skill dirs onto base dirs (deduped, stable).
func MergeSkillDirs(base []string, catalog *Catalog) []string {
	extra := catalog.ActiveSkillDirs()
	if len(extra) == 0 {
		return base
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	add := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" {
			return
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	for _, dir := range base {
		add(dir)
	}
	for _, dir := range extra {
		add(dir)
	}
	return out
}

// MergeAgentDirs appends active plugin agent dirs for agentdef ExtraDirs.
func MergeAgentDirs(base []string, catalog *Catalog) []string {
	extra := catalog.ActiveAgentDirs()
	if len(extra) == 0 {
		return base
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(base)+len(extra))
	add := func(dir string) {
		dir = filepath.Clean(strings.TrimSpace(dir))
		if dir == "" {
			return
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	for _, dir := range base {
		add(dir)
	}
	for _, dir := range extra {
		add(dir)
	}
	return out
}

// MergeHooks appends active plugin hooks after base hooks.
func MergeHooks(base []hooks.HookConfig, catalog *Catalog) []hooks.HookConfig {
	extra := catalog.ActiveHooks()
	if len(extra) == 0 {
		return base
	}
	out := make([]hooks.HookConfig, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}
