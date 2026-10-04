package aiclipaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateMCPHome 把用户主目录指向临时目录，避免真实的 ~/.aicli/mcp.yaml 影响断言。
func isolateMCPHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func writeMCPFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveMCPConfigPathDetailedFromAnchorsProjectAtBase(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	project := filepath.Join(base, ".aicli", "mcp.yaml")
	writeMCPFile(t, project, "mcpServers: {}\n")

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "project" {
		t.Fatalf("expected project source, got %q (%s)", resolution.Source, resolution.Path)
	}
	if filepath.Clean(resolution.Path) != filepath.Clean(project) {
		t.Fatalf("expected %s, got %s", project, resolution.Path)
	}

	wantLocal, err := LocalMCPConfigPath(base)
	if err != nil {
		t.Fatalf("LocalMCPConfigPath: %v", err)
	}
	foundLocal := false
	for _, candidate := range resolution.Candidates {
		if candidate.Source != "local" {
			continue
		}
		foundLocal = true
		if filepath.Clean(candidate.Path) != filepath.Clean(wantLocal) {
			t.Fatalf("local candidate anchored wrong: got %s want %s", candidate.Path, wantLocal)
		}
	}
	if !foundLocal {
		t.Fatal("expected a local candidate in the candidate list")
	}
}

func TestResolveMCPConfigPathDetailedFromWalksUpFromBase(t *testing.T) {
	isolateMCPHome(t)
	root := t.TempDir()
	base := filepath.Join(root, "workspace", "nested")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	upward := filepath.Join(root, "configs", "mcp.yaml")
	writeMCPFile(t, upward, "mcpServers: {}\n")

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	if resolution.Source != "upward" {
		t.Fatalf("expected upward source, got %q (%s)", resolution.Source, resolution.Path)
	}
	if filepath.Clean(resolution.Path) != filepath.Clean(upward) {
		t.Fatalf("expected %s, got %s", upward, resolution.Path)
	}

	// default 候选必须锚定在 base（显式基准目录时不允许相对进程 cwd）。
	defaultPath := filepath.Join(base, DefaultMCPConfigRelativePath)
	for _, candidate := range resolution.Candidates {
		if candidate.Source != "default" {
			continue
		}
		if filepath.Clean(candidate.Path) != filepath.Clean(defaultPath) {
			t.Fatalf("default candidate anchored wrong: got %s want %s", candidate.Path, defaultPath)
		}
	}
}

func TestResolveMCPConfigPathDetailedFromKeepsExplicitOverride(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()
	override := filepath.Join(t.TempDir(), "custom", "servers.yaml")
	writeMCPFile(t, override, "mcpServers: {}\n")

	resolution := ResolveMCPConfigPathDetailedFrom(base, override)

	if resolution.Source != "explicit" {
		t.Fatalf("expected explicit source, got %q (%s)", resolution.Source, resolution.Path)
	}
	if filepath.Clean(resolution.Path) != filepath.Clean(override) {
		t.Fatalf("expected override %s, got %s", override, resolution.Path)
	}
}

func TestResolveMCPConfigPathDetailedFromDoesNotFallBackIntoBaseWhenEmpty(t *testing.T) {
	isolateMCPHome(t)
	base := t.TempDir()

	resolution := ResolveMCPConfigPathDetailedFrom(base, "")

	// 空工作区：不得命中 base 下的 project/upward/default 候选。
	if resolution.Path != "" {
		cleanPath := filepath.Clean(resolution.Path)
		if strings.HasPrefix(strings.ToLower(cleanPath), strings.ToLower(filepath.Clean(base))) {
			t.Fatalf("empty workspace must not resolve into base, got %q (%s)", resolution.Path, resolution.Source)
		}
	}
}
