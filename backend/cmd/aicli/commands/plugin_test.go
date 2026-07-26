package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
)

func TestRunPluginInstallTrustListAndSkillMerge(t *testing.T) {
	ClearPluginCatalogCache()
	t.Cleanup(ClearPluginCatalogCache)

	home := filepath.Join(t.TempDir(), "aicli-home")
	t.Setenv("AICLI_HOME", home)
	pluginsHome := filepath.Join(home, "plugins")

	src := writeCLISamplePlugin(t, t.TempDir(), "demo-pack")

	installResult, _, err := runPluginInstallCommand(pluginInstallOptions{
		Source:     src,
		TargetRoot: pluginsHome,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !installResult.Installed || installResult.Name != "demo-pack" {
		t.Fatalf("unexpected install: %+v", installResult)
	}
	if installResult.Trust != plugins.TrustUntrusted {
		t.Fatalf("expected untrusted install, got %s", installResult.Trust)
	}

	// Untrusted must not merge skill dirs.
	baseSkill := t.TempDir()
	merged := mergeActivePluginSkillDirs([]string{baseSkill})
	if len(merged) != 1 || merged[0] != baseSkill {
		t.Fatalf("untrusted should not contribute skill dirs: %#v", merged)
	}

	trustResult, _, err := runPluginTrustCommand("demo-pack", true)
	if err != nil {
		t.Fatalf("trust: %v", err)
	}
	if trustResult.Trust != string(plugins.TrustTrusted) {
		t.Fatalf("expected trusted, got %+v", trustResult)
	}

	ClearPluginCatalogCache()
	merged = mergeActivePluginSkillDirs([]string{baseSkill})
	if len(merged) < 2 {
		t.Fatalf("trusted plugin should append skill dir, got %#v", merged)
	}
	found := false
	for _, dir := range merged {
		if strings.Contains(filepath.ToSlash(dir), "demo-pack") && strings.HasSuffix(filepath.ToSlash(dir), "/skills") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected demo-pack/skills in %#v", merged)
	}

	list, _, err := runPluginListCommand()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.Count != 1 || !list.Plugins[0].Active {
		t.Fatalf("expected one active plugin, got %+v", list)
	}

	_, _, err = runPluginEnableCommand("demo-pack", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	ClearPluginCatalogCache()
	merged = mergeActivePluginSkillDirs([]string{baseSkill})
	if len(merged) != 1 {
		t.Fatalf("disabled plugin must not contribute: %#v", merged)
	}
}

func TestPluginInstallCommandJSON(t *testing.T) {
	ClearPluginCatalogCache()
	t.Cleanup(ClearPluginCatalogCache)

	home := filepath.Join(t.TempDir(), "aicli-home")
	t.Setenv("AICLI_HOME", home)
	src := writeCLISamplePlugin(t, t.TempDir(), "json-pack")
	target := filepath.Join(home, "plugins")

	cmd := NewPluginCommand()
	cmd.SetArgs([]string{"install", src, "--target-dir", target, "--output", "json"})
	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute: %v", err)
		}
	})
	if !strings.Contains(output, `"name":"json-pack"`) {
		t.Fatalf("expected json install output, got %q", output)
	}
	if !strings.Contains(output, `"trust":"untrusted"`) {
		t.Fatalf("expected untrusted default, got %q", output)
	}
}

func TestResolveConfiguredSkillDirsMergesTrustedPlugins(t *testing.T) {
	ClearPluginCatalogCache()
	t.Cleanup(ClearPluginCatalogCache)

	home := filepath.Join(t.TempDir(), "aicli-home")
	t.Setenv("AICLI_HOME", home)
	pluginsHome := filepath.Join(home, "plugins")
	src := writeCLISamplePlugin(t, t.TempDir(), "skill-merge")
	if _, err := plugins.Install(plugins.InstallOptions{
		Source:     src,
		TargetRoot: pluginsHome,
		Trust:      plugins.TrustTrusted,
		State:      plugins.NewStateStore(filepath.Join(pluginsHome, plugins.StateFileName)),
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	ClearPluginCatalogCache()

	base := t.TempDir()
	resolved := resolveConfiguredSkillDirs(nil, []string{base})
	if len(resolved) < 2 {
		t.Fatalf("expected base + plugin skill dirs, got %#v", resolved)
	}
	if resolved[0] != base {
		t.Fatalf("expected base first, got %#v", resolved)
	}
	foundPlugin := false
	for _, dir := range resolved[1:] {
		if strings.Contains(filepath.ToSlash(dir), "skill-merge") {
			foundPlugin = true
			break
		}
	}
	if !foundPlugin {
		t.Fatalf("expected skill-merge plugin dir in %#v", resolved)
	}
}

func writeCLISamplePlugin(t *testing.T, parent, name string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	mustWritePluginFile(t, filepath.Join(root, "plugin.yaml"), "name: "+name+"\nversion: 0.1.0\ndescription: cli test plugin\n")
	mustWritePluginFile(t, filepath.Join(root, "skills", "demo", "SKILL.md"), "---\nname: demo\ndescription: demo skill\n---\n# Demo\n")
	mustWritePluginFile(t, filepath.Join(root, "agents", "helper.md"), "---\nname: helper\ndescription: helper agent\n---\n# Helper\n")
	mustWritePluginFile(t, filepath.Join(root, "hooks.yaml"), "hooks:\n  - id: "+name+"-stop\n    event: Stop\n    exec:\n      type: shell\n      cmd: [\"echo\", \"stop\"]\n")
	mustWritePluginFile(t, filepath.Join(root, "mcp.yaml"), "mcpServers:\n  echo:\n    name: echo\n    type: stdio\n    command: echo\n    enabled: true\n")
	return root
}

func mustWritePluginFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
