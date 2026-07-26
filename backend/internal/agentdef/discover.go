package agentdef

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// DiscoverOptions configures multi-root agent definition discovery.
type DiscoverOptions struct {
	// ProjectRoot is the project working directory (defaults to cwd when empty).
	ProjectRoot string
	// SkipProjectRoot skips .agents/agents and .aicli/agents under ProjectRoot/cwd.
	// Used by folder-trust gating when the workspace is untrusted; user-home and
	// builtin agents remain available.
	SkipProjectRoot bool
	// UserHome overrides the home directory used for ~/.aicli/agents.
	UserHome string
	// ProfileRoot, when set, also loads profile agents/<id>/agent.yaml via adapter.
	ProfileRoot string
	// IncludeBuiltin includes built-in explore/plan/general stubs (default true when Discover is used).
	IncludeBuiltin *bool
	// ExtraDirs are additional agent definition directories (later dirs override earlier).
	ExtraDirs []string
}

// Catalog is the merged set of agent definitions keyed by normalized name.
// Later discovery roots override earlier ones for the same name.
type Catalog struct {
	ByName map[string]*Definition
	Order  []string // stable sorted names
}

// Get returns a definition by name (case-insensitive).
func (c *Catalog) Get(name string) (*Definition, bool) {
	if c == nil || c.ByName == nil {
		return nil, false
	}
	def, ok := c.ByName[normalizeAgentName(name)]
	return def, ok
}

// List returns definitions in sorted name order.
func (c *Catalog) List() []*Definition {
	if c == nil {
		return nil
	}
	out := make([]*Definition, 0, len(c.Order))
	for _, name := range c.Order {
		if def, ok := c.ByName[name]; ok {
			out = append(out, def)
		}
	}
	return out
}

// Discover loads agent definitions with fixed priority (later overrides earlier):
//
//  1. built-in
//  2. ~/.aicli/agents/*
//  3. <project>/.agents/agents/* and <project>/.aicli/agents/*
//  4. profile agents/*/agent.yaml (when ProfileRoot set)
//  5. ExtraDirs (highest priority among filesystem roots)
func Discover(opts DiscoverOptions) (*Catalog, error) {
	catalog := &Catalog{ByName: make(map[string]*Definition)}

	includeBuiltin := true
	if opts.IncludeBuiltin != nil {
		includeBuiltin = *opts.IncludeBuiltin
	}
	if includeBuiltin {
		for _, def := range BuiltinDefinitions() {
			clone := *def
			clone.Normalize()
			_ = catalog.put(&clone)
		}
	}

	userAgents := userAgentsDir(opts.UserHome)
	if err := loadDirIntoCatalog(catalog, userAgents, SourceUser); err != nil {
		return nil, err
	}

	if !opts.SkipProjectRoot {
		projectRoot := strings.TrimSpace(opts.ProjectRoot)
		if projectRoot == "" {
			if cwd, err := os.Getwd(); err == nil {
				projectRoot = cwd
			}
		}
		if projectRoot != "" {
			for _, rel := range []string{
				filepath.Join(".agents", "agents"),
				filepath.Join(".aicli", "agents"),
			} {
				dir := filepath.Join(projectRoot, rel)
				if err := loadDirIntoCatalog(catalog, dir, SourceProject); err != nil {
					return nil, err
				}
			}
		}
	}

	if root := strings.TrimSpace(opts.ProfileRoot); root != "" {
		if err := loadProfileAgents(catalog, root); err != nil {
			return nil, err
		}
	}

	for _, dir := range opts.ExtraDirs {
		if err := loadDirIntoCatalog(catalog, dir, SourceProject); err != nil {
			return nil, err
		}
	}

	catalog.rebuildOrder()
	return catalog, nil
}

// Resolve looks up a single agent by name using Discover options.
func Resolve(name string, opts DiscoverOptions) (*Definition, error) {
	name = normalizeAgentName(name)
	if name == "" {
		return nil, fmt.Errorf("agentdef: agent name is required")
	}
	catalog, err := Discover(opts)
	if err != nil {
		return nil, err
	}
	def, ok := catalog.Get(name)
	if !ok {
		return nil, fmt.Errorf("agentdef: agent %q not found", name)
	}
	clone := *def
	return &clone, nil
}

func (c *Catalog) put(def *Definition) error {
	if def == nil {
		return nil
	}
	def.Normalize()
	if err := Validate(def); err != nil {
		return err
	}
	if c.ByName == nil {
		c.ByName = make(map[string]*Definition)
	}
	clone := *def
	c.ByName[clone.Name] = &clone
	return nil
}

func (c *Catalog) rebuildOrder() {
	names := make([]string, 0, len(c.ByName))
	for name := range c.ByName {
		names = append(names, name)
	}
	sort.Strings(names)
	c.Order = names
}

func loadDirIntoCatalog(catalog *Catalog, dir string, source Source) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" || dir == "." {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("agentdef: stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("agentdef: read dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		if entry.IsDir() {
			// Support agents/<id>/agent.yaml layout inside a flat agents root.
			for _, candidate := range []string{
				filepath.Join(path, "agent.yaml"),
				filepath.Join(path, "agent.yml"),
				filepath.Join(path, "agent.md"),
			} {
				if _, err := os.Stat(candidate); err == nil {
					if err := loadFileIntoCatalog(catalog, candidate, source); err != nil {
						return err
					}
					break
				}
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".md", ".markdown", ".yaml", ".yml":
			if err := loadFileIntoCatalog(catalog, path, source); err != nil {
				return err
			}
		}
	}
	return nil
}

func loadFileIntoCatalog(catalog *Catalog, path string, source Source) error {
	def, err := ParseFile(path)
	if err != nil {
		return err
	}
	def.Source = source
	def.SourcePath = path
	return catalog.put(def)
}

func loadProfileAgents(catalog *Catalog, profileRoot string) error {
	profileRoot = filepath.Clean(strings.TrimSpace(profileRoot))
	if profileRoot == "" {
		return nil
	}
	agentsDir := profilesys.ResolveProfilePaths(profileRoot).AgentsDir
	info, err := os.Stat(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("agentdef: stat profile agents %s: %w", agentsDir, err)
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return fmt.Errorf("agentdef: read profile agents %s: %w", agentsDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		agentID := entry.Name()
		paths := profilesys.ResolveAgentPaths(profileRoot, agentID)
		def, err := AdaptProfileAgent(profileRoot, agentID, paths.ConfigFile)
		if err != nil {
			// Missing agent.yaml is not fatal; skip empty agent dirs.
			if os.IsNotExist(err) {
				continue
			}
			// Soft-skip unreadable agents to avoid breaking whole discovery.
			if _, statErr := os.Stat(paths.ConfigFile); statErr != nil && os.IsNotExist(statErr) {
				continue
			}
			return err
		}
		if def == nil {
			continue
		}
		def.Source = SourceProfile
		if err := catalog.put(def); err != nil {
			return err
		}
	}
	return nil
}

func userAgentsDir(userHome string) string {
	home := strings.TrimSpace(userHome)
	if home == "" {
		expanded := aiclipaths.ExpandUserPath("~")
		if expanded != "" && expanded != "~" {
			home = expanded
		}
	}
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".aicli", "agents")
}
