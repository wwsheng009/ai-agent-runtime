package plugins_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
)

func TestLoadManifestAndPackage(t *testing.T) {
	root := writeSamplePlugin(t, t.TempDir(), "demo-plugin")

	manifest, path, err := plugins.LoadManifest(root)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if filepath.Base(path) != "plugin.yaml" {
		t.Fatalf("manifest path = %s", path)
	}
	if manifest.Name != "demo-plugin" {
		t.Fatalf("name = %q", manifest.Name)
	}
	if manifest.SkillsDir != "skills" {
		t.Fatalf("skills_dir = %q", manifest.SkillsDir)
	}

	pkg, err := plugins.Load(root, plugins.LoadOptions{Trust: plugins.TrustTrusted})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !pkg.IsActive() {
		t.Fatal("expected trusted package active")
	}
	if len(pkg.SkillDirs) != 1 {
		t.Fatalf("skill dirs = %#v", pkg.SkillDirs)
	}
	if len(pkg.AgentDirs) != 1 {
		t.Fatalf("agent dirs = %#v", pkg.AgentDirs)
	}
	if len(pkg.Hooks) != 1 {
		t.Fatalf("hooks = %#v", pkg.Hooks)
	}
	if pkg.MCP == nil || len(pkg.MCP.MCPServers) != 1 {
		t.Fatalf("mcp = %#v", pkg.MCP)
	}
}

func TestUntrustedPackageDoesNotContribute(t *testing.T) {
	root := writeSamplePlugin(t, t.TempDir(), "locked")
	pkg, err := plugins.Load(root, plugins.LoadOptions{Trust: plugins.TrustUntrusted})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pkg.IsActive() {
		t.Fatal("untrusted should not be active")
	}
	if c := pkg.ActiveContributions(); len(c.SkillDirs) != 0 || len(c.Hooks) != 0 {
		t.Fatalf("unexpected contributions: %#v", c)
	}
}

func TestInstallTrustDiscoverMerge(t *testing.T) {
	src := writeSamplePlugin(t, t.TempDir(), "pack-a")
	home := filepath.Join(t.TempDir(), "plugins-home")
	statePath := filepath.Join(home, "state.json")
	store := plugins.NewStateStore(statePath)

	result, err := plugins.Install(plugins.InstallOptions{
		Source:     src,
		TargetRoot: home,
		State:      store,
		Trust:      plugins.TrustUntrusted,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !result.Installed {
		t.Fatalf("expected installed: %#v", result)
	}

	catalog, err := plugins.Discover(plugins.DiscoverOptions{
		PluginsHome: home,
		ProjectRoot: t.TempDir(), // empty project plugins
		State:       store,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	pkg, ok := catalog.Get("pack-a")
	if !ok {
		t.Fatal("expected pack-a in catalog")
	}
	if pkg.IsActive() {
		t.Fatal("untrusted install must not be active")
	}
	if dirs := catalog.ActiveSkillDirs(); len(dirs) != 0 {
		t.Fatalf("active skill dirs should be empty, got %#v", dirs)
	}

	if _, err := store.SetTrust("pack-a", plugins.TrustTrusted, result.TargetDir); err != nil {
		t.Fatalf("SetTrust: %v", err)
	}
	catalog, err = plugins.Discover(plugins.DiscoverOptions{
		PluginsHome: home,
		ProjectRoot: filepath.Join(t.TempDir(), "proj"),
		State:       store,
	})
	if err != nil {
		t.Fatalf("Discover after trust: %v", err)
	}
	pkg, ok = catalog.Get("pack-a")
	if !ok || !pkg.IsActive() {
		t.Fatalf("expected active pack-a, ok=%v active=%v", ok, pkg != nil && pkg.IsActive())
	}
	skillDirs := catalog.ActiveSkillDirs()
	if len(skillDirs) != 1 {
		t.Fatalf("active skill dirs = %#v", skillDirs)
	}
	merged := plugins.MergeSkillDirs([]string{filepath.Join(t.TempDir(), "base-skills")}, catalog)
	if len(merged) < 2 {
		t.Fatalf("merged skill dirs = %#v", merged)
	}
	agentDirs := plugins.MergeAgentDirs(nil, catalog)
	if len(agentDirs) != 1 {
		t.Fatalf("agent dirs = %#v", agentDirs)
	}
	hooksMerged := plugins.MergeHooks([]hooks.HookConfig{{ID: "base", Event: hooks.EventStop}}, catalog)
	if len(hooksMerged) < 2 {
		t.Fatalf("hooks = %#v", hooksMerged)
	}
}

func TestDiscoverProjectPluginOverridesUser(t *testing.T) {
	userHome := t.TempDir()
	userPlugins := filepath.Join(userHome, ".aicli", "plugins")
	writeSamplePlugin(t, userPlugins, "shared")
	// bump description via rewrite
	_ = os.WriteFile(filepath.Join(userPlugins, "shared", "plugin.yaml"), []byte(`
name: shared
version: "1.0.0"
description: user
`), 0o644)

	project := t.TempDir()
	projectPlugins := filepath.Join(project, ".agents", "plugins")
	writeSamplePlugin(t, projectPlugins, "shared")
	_ = os.WriteFile(filepath.Join(projectPlugins, "shared", "plugin.yaml"), []byte(`
name: shared
version: "2.0.0"
description: project
`), 0o644)

	store := plugins.NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	_, _ = store.SetTrust("shared", plugins.TrustTrusted, "")

	catalog, err := plugins.Discover(plugins.DiscoverOptions{
		UserHome:    userHome,
		ProjectRoot: project,
		State:       store,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	pkg, ok := catalog.Get("shared")
	if !ok {
		t.Fatal("missing shared")
	}
	if pkg.Manifest.Description != "project" {
		t.Fatalf("expected project override, got %q from %s", pkg.Manifest.Description, pkg.Root)
	}
	if !strings.Contains(pkg.Root, ".agents") {
		t.Fatalf("expected project root, got %s", pkg.Root)
	}
}

func TestInstallRefusesWithoutForce(t *testing.T) {
	src := writeSamplePlugin(t, t.TempDir(), "once")
	home := filepath.Join(t.TempDir(), "home")
	if _, err := plugins.Install(plugins.InstallOptions{Source: src, TargetRoot: home, SkipState: true}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	_, err := plugins.Install(plugins.InstallOptions{Source: src, TargetRoot: home, SkipState: true})
	if err == nil {
		t.Fatal("expected already installed error")
	}
}

func writeSamplePlugin(t *testing.T, parent, name string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	mustWrite(t, filepath.Join(root, "plugin.yaml"), `name: `+name+`
version: "0.1.0"
description: sample plugin
skills_dir: skills
agents_dir: agents
hooks_file: hooks.yaml
mcp_file: mcp.yaml
`)
	mustWrite(t, filepath.Join(root, "skills", "demo", "SKILL.md"), `---
name: demo-skill
description: demo
---
# Demo
`)
	mustWrite(t, filepath.Join(root, "agents", "helper.md"), `---
name: helper
description: helper agent
permissionMode: plan
---
Be helpful.
`)
	mustWrite(t, filepath.Join(root, "hooks.yaml"), `hooks:
  - id: `+name+`-stop
    event: Stop
    exec:
      type: shell
      cmd: ["echo", "stop"]
`)
	mustWrite(t, filepath.Join(root, "mcp.yaml"), `mcpServers:
  echo:
    name: echo
    type: stdio
    command: echo
    enabled: true
`)
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
