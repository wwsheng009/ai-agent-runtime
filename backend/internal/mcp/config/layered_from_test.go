package config

import (
	"os"
	"path/filepath"
	"testing"
)

func isolateLayeredHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeLayeredMCPFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadEffectiveFromMergesUserBaseWithProjectDelta(t *testing.T) {
	home := isolateLayeredHome(t)
	base := t.TempDir()

	writeLayeredMCPFile(t, filepath.Join(home, ".aicli", "mcp.yaml"), `
mcpServers:
  alpha:
    type: stdio
    command: user-alpha
  gamma:
    type: stdio
    command: user-gamma
`)
	writeLayeredMCPFile(t, filepath.Join(base, ".aicli", "mcp.yaml"), `
mcpServers:
  alpha:
    type: stdio
    command: project-alpha
  beta:
    type: stdio
    command: project-beta
`)

	result, err := LoadEffectiveFrom(base, "")
	if err != nil {
		t.Fatalf("LoadEffectiveFrom: %v", err)
	}

	if result.Primary.Source != "project" {
		t.Fatalf("expected project primary, got %q", result.Primary.Source)
	}
	if got := result.Config.MCPServers["alpha"].Command; got != "project-alpha" {
		t.Fatalf("alpha should come from the project layer, got command %q", got)
	}
	if got := result.Config.MCPServers["beta"].Command; got != "project-beta" {
		t.Fatalf("beta should come from the project layer, got command %q", got)
	}
	if got := result.Config.MCPServers["gamma"].Command; got != "user-gamma" {
		t.Fatalf("gamma should survive from the user layer, got command %q", got)
	}
	origin, ok := result.Origins["alpha"]
	if !ok || origin.Source != "project" {
		t.Fatalf("alpha origin should be project, got %#v", origin)
	}
	if len(origin.Shadowed) != 1 || origin.Shadowed[0].Source != "user" {
		t.Fatalf("alpha should shadow the user definition, got %#v", origin.Shadowed)
	}
}

func TestLoadEffectiveFromIsIndependentFromProcessCWD(t *testing.T) {
	_ = isolateLayeredHome(t)
	base := t.TempDir()
	writeLayeredMCPFile(t, filepath.Join(base, ".aicli", "mcp.yaml"), `
mcpServers:
  only-in-base:
    type: stdio
    command: base-only
`)

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	other := t.TempDir()
	if err := os.Chdir(other); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previous)
	})

	result, err := LoadEffectiveFrom(base, "")
	if err != nil {
		t.Fatalf("LoadEffectiveFrom: %v", err)
	}
	if _, ok := result.Config.MCPServers["only-in-base"]; !ok {
		t.Fatal("anchor must follow baseDir, not the process cwd")
	}

	// 对照：进程 cwd 锚定解析在 other 下不应命中 base 的配置。
	cwdResult, err := LoadEffective("")
	if err != nil {
		if err != ErrNoConfigFiles {
			t.Fatalf("cwd-resolved load: %v", err)
		}
		return
	}
	if _, ok := cwdResult.Config.MCPServers["only-in-base"]; ok {
		t.Fatal("cwd-anchored load must not see baseDir's config")
	}
}
