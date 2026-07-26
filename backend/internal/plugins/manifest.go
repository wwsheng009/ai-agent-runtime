package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ManifestFileNames are accepted plugin root manifests (first match wins).
var ManifestFileNames = []string{
	"plugin.yaml",
	"plugin.yml",
	"plugin.json",
}

// TrustLevel is the local trust mark for a plugin package.
type TrustLevel string

const (
	// TrustUntrusted is the default for newly discovered/installed plugins.
	TrustUntrusted TrustLevel = "untrusted"
	// TrustTrusted allows the plugin contributions to be applied at runtime.
	TrustTrusted TrustLevel = "trusted"
)

// Manifest is the on-disk plugin declaration.
type Manifest struct {
	Name        string            `yaml:"name" json:"name"`
	Version     string            `yaml:"version,omitempty" json:"version,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Author      string            `yaml:"author,omitempty" json:"author,omitempty"`
	Homepage    string            `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Tags        []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	Enabled     *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	SkillsDir   string            `yaml:"skills_dir,omitempty" json:"skills_dir,omitempty"`
	AgentsDir   string            `yaml:"agents_dir,omitempty" json:"agents_dir,omitempty"`
	HooksFile   string            `yaml:"hooks_file,omitempty" json:"hooks_file,omitempty"`
	MCPFile     string            `yaml:"mcp_file,omitempty" json:"mcp_file,omitempty"`
	Metadata    map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

// IsEnabled reports whether the plugin should contribute when trusted.
// Missing enabled field defaults to true.
func (m *Manifest) IsEnabled() bool {
	if m == nil {
		return false
	}
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

// Normalize fills defaults relative to the plugin root.
func (m *Manifest) Normalize(root string) {
	if m == nil {
		return
	}
	m.Name = strings.TrimSpace(m.Name)
	m.Version = strings.TrimSpace(m.Version)
	m.Description = strings.TrimSpace(m.Description)
	m.Author = strings.TrimSpace(m.Author)
	m.Homepage = strings.TrimSpace(m.Homepage)
	m.SkillsDir = strings.TrimSpace(m.SkillsDir)
	m.AgentsDir = strings.TrimSpace(m.AgentsDir)
	m.HooksFile = strings.TrimSpace(m.HooksFile)
	m.MCPFile = strings.TrimSpace(m.MCPFile)
	if m.Name == "" && root != "" {
		m.Name = filepath.Base(filepath.Clean(root))
	}
	if m.SkillsDir == "" {
		if dirExists(filepath.Join(root, "skills")) {
			m.SkillsDir = "skills"
		}
	}
	if m.AgentsDir == "" {
		if dirExists(filepath.Join(root, "agents")) {
			m.AgentsDir = "agents"
		}
	}
	if m.HooksFile == "" {
		for _, candidate := range []string{"hooks.yaml", "hooks.yml", "hooks.json", filepath.Join("hooks", "hooks.yaml")} {
			if fileExists(filepath.Join(root, candidate)) {
				m.HooksFile = candidate
				break
			}
		}
	}
	if m.MCPFile == "" {
		for _, candidate := range []string{"mcp.yaml", "mcp.yml", "mcp.json"} {
			if fileExists(filepath.Join(root, candidate)) {
				m.MCPFile = candidate
				break
			}
		}
	}
}

// Validate checks required manifest fields after Normalize.
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("plugin manifest is nil")
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("plugin name is required")
	}
	if err := validatePluginName(m.Name); err != nil {
		return err
	}
	return nil
}

func validatePluginName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("plugin name is required")
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("plugin name must not contain path separators: %q", name)
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("plugin name contains invalid character %q: %s", r, name)
	}
	return nil
}

// FindManifestPath locates a plugin manifest under root.
func FindManifestPath(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("plugin root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("plugin root is not a directory: %s", root)
	}
	for _, name := range ManifestFileNames {
		path := filepath.Join(root, name)
		if fileExists(path) {
			return path, nil
		}
	}
	return "", fmt.Errorf("plugin manifest not found under %s (expected plugin.yaml)", root)
}

// LoadManifest reads and normalizes a plugin manifest from root.
func LoadManifest(root string) (*Manifest, string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	manifestPath, err := FindManifestPath(root)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, "", fmt.Errorf("read plugin manifest: %w", err)
	}
	var manifest Manifest
	switch strings.ToLower(filepath.Ext(manifestPath)) {
	case ".json":
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, "", fmt.Errorf("parse plugin.json: %w", err)
		}
	default:
		if err := yaml.Unmarshal(data, &manifest); err != nil {
			return nil, "", fmt.Errorf("parse plugin.yaml: %w", err)
		}
	}
	manifest.Normalize(root)
	if err := manifest.Validate(); err != nil {
		return nil, "", err
	}
	return &manifest, manifestPath, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
