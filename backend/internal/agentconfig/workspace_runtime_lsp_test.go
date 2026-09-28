package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// 工作区层 runtime.yaml 自动写入（项目扫描 → LSP 启用）的单元测试。
//
// 覆盖两条硬约束：显式决定优先（已声明 lsp.enabled 一律不动）、最小改写
// （只补缺失键，其余内容/注释保持原样）。

type workspaceRuntimeDoc struct {
	LSP struct {
		Enabled bool `yaml:"enabled"`
		Servers []struct {
			Name    string `yaml:"name"`
			Command string `yaml:"command"`
		} `yaml:"servers"`
	} `yaml:"lsp"`
}

func testRuntimeLSPSpecs(names ...string) []runtimelsp.ServerSpec {
	specs := make([]runtimelsp.ServerSpec, 0, len(names))
	for _, name := range names {
		specs = append(specs, runtimelsp.ServerSpec{Name: name, Command: name})
	}
	return specs
}

func readWorkspaceRuntimeDoc(t *testing.T, path string) workspaceRuntimeDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workspace runtime config: %v", err)
	}
	var doc workspaceRuntimeDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal workspace runtime config: %v", err)
	}
	return doc
}

func TestWorkspaceRuntimeConfigPath(t *testing.T) {
	if got := WorkspaceRuntimeConfigPath("   "); got != "" {
		t.Fatalf("blank root: got %q, want empty", got)
	}
	root := t.TempDir()
	want := filepath.Join(root, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	if got := WorkspaceRuntimeConfigPath(root); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEnsureWorkspaceRuntimeLSPCreatesFile(t *testing.T) {
	root := t.TempDir()
	path, changed, err := EnsureWorkspaceRuntimeLSP(root, testRuntimeLSPSpecs("gopls", "typescript"))
	if err != nil {
		t.Fatalf("EnsureWorkspaceRuntimeLSP: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if want := WorkspaceRuntimeConfigPath(root); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	doc := readWorkspaceRuntimeDoc(t, path)
	if !doc.LSP.Enabled {
		t.Fatal("lsp.enabled = false, want true")
	}
	if len(doc.LSP.Servers) != 2 || doc.LSP.Servers[0].Name != "gopls" || doc.LSP.Servers[1].Name != "typescript" {
		t.Fatalf("lsp.servers = %#v", doc.LSP.Servers)
	}
}

func TestEnsureWorkspaceRuntimeLSPPreservesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := WorkspaceRuntimeConfigPath(root)
	writeConfigLayerFile(t, path, "# keep this comment\nworkspace:\n  root: sub\n")

	_, changed, err := EnsureWorkspaceRuntimeLSP(root, testRuntimeLSPSpecs("gopls"))
	if err != nil {
		t.Fatalf("EnsureWorkspaceRuntimeLSP: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read workspace runtime config: %v", err)
	}
	if !strings.Contains(string(raw), "# keep this comment") {
		t.Fatalf("comment was dropped:\n%s", raw)
	}
	if !strings.Contains(string(raw), "root: sub") {
		t.Fatalf("unrelated key was dropped:\n%s", raw)
	}
	doc := readWorkspaceRuntimeDoc(t, path)
	if !doc.LSP.Enabled || len(doc.LSP.Servers) != 1 || doc.LSP.Servers[0].Name != "gopls" {
		t.Fatalf("lsp section = %#v", doc.LSP)
	}
}

func TestEnsureWorkspaceRuntimeLSPAddsEnabledToExistingServers(t *testing.T) {
	root := t.TempDir()
	path := WorkspaceRuntimeConfigPath(root)
	writeConfigLayerFile(t, path, "lsp:\n  servers:\n    - name: gopls\n      command: gopls\n")

	_, changed, err := EnsureWorkspaceRuntimeLSP(root, testRuntimeLSPSpecs("typescript"))
	if err != nil {
		t.Fatalf("EnsureWorkspaceRuntimeLSP: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	doc := readWorkspaceRuntimeDoc(t, path)
	if !doc.LSP.Enabled {
		t.Fatal("lsp.enabled = false, want true")
	}
	// 已显式声明 servers 时保持原样（检测结果不覆盖显式选择）。
	if len(doc.LSP.Servers) != 1 || doc.LSP.Servers[0].Name != "gopls" {
		t.Fatalf("explicit servers were overwritten: %#v", doc.LSP.Servers)
	}
}

func TestEnsureWorkspaceRuntimeLSPRespectsExplicitKeys(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"explicit disable", "lsp:\n  enabled: false\n"},
		{"explicit enable", "lsp:\n  enabled: true\n"},
		{"lsp is not a mapping", "lsp: false\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			path := WorkspaceRuntimeConfigPath(root)
			writeConfigLayerFile(t, path, tt.content)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read before: %v", err)
			}

			_, changed, err := EnsureWorkspaceRuntimeLSP(root, testRuntimeLSPSpecs("gopls"))
			if err != nil {
				t.Fatalf("EnsureWorkspaceRuntimeLSP: %v", err)
			}
			if changed {
				t.Fatal("changed = true, want false")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read after: %v", err)
			}
			if string(before) != string(after) {
				t.Fatalf("file was modified:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestEnsureWorkspaceRuntimeLSPNoServers(t *testing.T) {
	root := t.TempDir()
	path, changed, err := EnsureWorkspaceRuntimeLSP(root, nil)
	if err != nil {
		t.Fatalf("EnsureWorkspaceRuntimeLSP: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("no servers must not create the file, stat err = %v", err)
	}
}

func TestRuntimeConfigExplicitKey(t *testing.T) {
	dir := t.TempDir()
	if RuntimeConfigExplicitKey(filepath.Join(dir, "missing.yaml"), "lsp", "enabled") {
		t.Fatal("missing file must not report an explicit key")
	}
	if RuntimeConfigExplicitKey("", "lsp") {
		t.Fatal("blank path must not report an explicit key")
	}

	path := filepath.Join(dir, "runtime.yaml")
	writeConfigLayerFile(t, path, "lsp:\n  enabled: false\n")
	if !RuntimeConfigExplicitKey(path, "lsp", "enabled") {
		t.Fatal("lsp.enabled must be reported as explicit")
	}
	if RuntimeConfigExplicitKey(path, "lsp", "servers") {
		t.Fatal("missing key must not be reported as explicit")
	}

	bad := filepath.Join(dir, "bad.yaml")
	writeConfigLayerFile(t, bad, "\tnot yaml: [")
	if RuntimeConfigExplicitKey(bad, "lsp") {
		t.Fatal("unparsable file must not report an explicit key")
	}
}

func TestRuntimeConfigExplicitKeyInLayers(t *testing.T) {
	home := isolateConfigLayerHome(t)
	// Project 层是 CWD 相对路径（./.aicli/runtime.yaml）；测试运行在包目录下，
	// 该目录没有 .aicli，因此这里只由 user 层决定结果。
	if RuntimeConfigExplicitKeyInLayers("lsp", "enabled") {
		t.Fatal("no layer declares lsp.enabled")
	}
	writeConfigLayerFile(t, filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName), "lsp:\n  enabled: false\n")
	if !RuntimeConfigExplicitKeyInLayers("lsp", "enabled") {
		t.Fatal("user layer declares lsp.enabled but was not reported")
	}
}
