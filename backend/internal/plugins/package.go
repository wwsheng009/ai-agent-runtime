package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"gopkg.in/yaml.v3"
)

// Package is a loaded plugin with resolved contribution paths.
type Package struct {
	Root         string
	ManifestPath string
	Manifest     Manifest
	Trust        TrustLevel
	// Enabled is the effective enable flag (manifest + trust state override).
	Enabled bool
	// SkillDirs are absolute skill tree roots contributed by this plugin.
	SkillDirs []string
	// AgentDirs are absolute agent definition directories.
	AgentDirs []string
	// Hooks are hook configs loaded from the plugin (empty when untrusted/disabled).
	Hooks []hooks.HookConfig
	// MCP is optional MCP config contributed by the plugin.
	MCP *mcpconfig.Config
	// Warnings collect non-fatal load issues.
	Warnings []string
}

// Contributions summarizes what a package exposes when active.
type Contributions struct {
	SkillDirs []string
	AgentDirs []string
	Hooks     []hooks.HookConfig
	MCP       *mcpconfig.Config
}

// IsActive reports whether the package should contribute at runtime.
// Requires local trust + enabled.
func (p *Package) IsActive() bool {
	if p == nil {
		return false
	}
	return p.Enabled && p.Trust == TrustTrusted
}

// ActiveContributions returns skill/agent/hook/mcp surfaces when active.
func (p *Package) ActiveContributions() Contributions {
	if !p.IsActive() {
		return Contributions{}
	}
	return Contributions{
		SkillDirs: append([]string(nil), p.SkillDirs...),
		AgentDirs: append([]string(nil), p.AgentDirs...),
		Hooks:     append([]hooks.HookConfig(nil), p.Hooks...),
		MCP:       p.MCP,
	}
}

// LoadOptions controls package load behavior.
type LoadOptions struct {
	// Trust defaults to TrustUntrusted when empty.
	Trust TrustLevel
	// EnabledOverride, when non-nil, overrides manifest enabled.
	EnabledOverride *bool
	// RequireManifest fails when plugin.yaml is missing (default true for Load).
	// Discover tolerates missing manifests by skipping.
	RequireManifest bool
}

// Load reads a plugin package from root without requiring trust state.
func Load(root string, opts LoadOptions) (*Package, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return nil, fmt.Errorf("plugin root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plugin root is not a directory: %s", root)
	}

	manifest, manifestPath, err := LoadManifest(root)
	if err != nil {
		if opts.RequireManifest || !os.IsNotExist(err) {
			// FindManifestPath returns a non-IsNotExist error; always require for Load.
			if opts.RequireManifest || true {
				return nil, err
			}
		}
		return nil, err
	}

	pkg := &Package{
		Root:         root,
		ManifestPath: manifestPath,
		Manifest:     *manifest,
		Trust:        normalizeTrust(opts.Trust),
		Enabled:      manifest.IsEnabled(),
	}
	if opts.EnabledOverride != nil {
		pkg.Enabled = *opts.EnabledOverride
	}

	if err := pkg.resolveContributions(); err != nil {
		return nil, err
	}
	return pkg, nil
}

func (p *Package) resolveContributions() error {
	if p == nil {
		return fmt.Errorf("plugin package is nil")
	}
	p.SkillDirs = nil
	p.AgentDirs = nil
	p.Hooks = nil
	p.MCP = nil
	p.Warnings = nil

	if dir := strings.TrimSpace(p.Manifest.SkillsDir); dir != "" {
		abs := resolvePluginPath(p.Root, dir)
		if dirExists(abs) {
			p.SkillDirs = append(p.SkillDirs, abs)
		} else {
			p.Warnings = append(p.Warnings, fmt.Sprintf("skills_dir not found: %s", dir))
		}
	}
	if dir := strings.TrimSpace(p.Manifest.AgentsDir); dir != "" {
		abs := resolvePluginPath(p.Root, dir)
		if dirExists(abs) {
			p.AgentDirs = append(p.AgentDirs, abs)
		} else {
			p.Warnings = append(p.Warnings, fmt.Sprintf("agents_dir not found: %s", dir))
		}
	}

	// Always try to parse hooks/mcp when present so install/list can show them;
	// runtime apply still gated by IsActive.
	if file := strings.TrimSpace(p.Manifest.HooksFile); file != "" {
		abs := resolvePluginPath(p.Root, file)
		hooksList, err := loadHooksFile(abs)
		if err != nil {
			p.Warnings = append(p.Warnings, fmt.Sprintf("hooks: %v", err))
		} else {
			p.Hooks = hooksList
		}
	}
	if file := strings.TrimSpace(p.Manifest.MCPFile); file != "" {
		abs := resolvePluginPath(p.Root, file)
		mcpCfg, err := loadMCPFile(abs)
		if err != nil {
			p.Warnings = append(p.Warnings, fmt.Sprintf("mcp: %v", err))
		} else {
			p.MCP = mcpCfg
		}
	}
	return nil
}

func resolvePluginPath(root, rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Clean(filepath.Join(root, rel))
}

func normalizeTrust(level TrustLevel) TrustLevel {
	switch TrustLevel(strings.ToLower(strings.TrimSpace(string(level)))) {
	case TrustTrusted:
		return TrustTrusted
	default:
		return TrustUntrusted
	}
}

type hooksFileDocument struct {
	Hooks []hooks.HookConfig `yaml:"hooks" json:"hooks"`
}

func loadHooksFile(path string) ([]hooks.HookConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Prefer { hooks: [...] }; also accept bare array.
	var doc hooksFileDocument
	if err := unmarshalConfig(path, data, &doc); err == nil && len(doc.Hooks) > 0 {
		return doc.Hooks, nil
	}
	var list []hooks.HookConfig
	if err := unmarshalConfig(path, data, &list); err != nil {
		return nil, fmt.Errorf("parse hooks file %s: %w", path, err)
	}
	return list, nil
}

func loadMCPFile(path string) (*mcpconfig.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg mcpconfig.Config
	if err := unmarshalConfig(path, data, &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp file %s: %w", path, err)
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]mcpconfig.MCPConfig{}
	}
	return &cfg, nil
}

func unmarshalConfig(path string, data []byte, dest interface{}) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return json.Unmarshal(data, dest)
	default:
		return yaml.Unmarshal(data, dest)
	}
}
