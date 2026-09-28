package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/projectscan"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// 启动期 LSP 自动装配（项目扫描 → 写工作区 runtime.yaml → 挂池）的单元测试。

func stubLSPLookPath(t *testing.T, available ...string) {
	t.Helper()
	restore := lspLookPath
	allowed := make(map[string]struct{}, len(available))
	for _, name := range available {
		allowed[name] = struct{}{}
	}
	lspLookPath = func(name string) (string, error) {
		if _, ok := allowed[name]; ok {
			return filepath.Join("stub", name), nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lspLookPath = restore })
}

// isolateLSPBootstrapHome 固定 userHomeDir 解析，隔离层栈里的 user 层
// （$HOME/.aicli/runtime.yaml），避免读到开发机真实配置。
func isolateLSPBootstrapHome(t *testing.T) {
	t.Helper()
	restore := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return t.TempDir(), nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(restore) })
}

func lspBootstrapBaseConfig(root string) *runtimecfg.RuntimeConfig {
	base := runtimecfg.DefaultRuntimeConfig()
	base.Workspace.Root = root
	base.LSP.Enabled = false
	return base
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLSPServerNamesForProjectTypes(t *testing.T) {
	tests := []struct {
		name  string
		types []projectscan.Type
		want  []string
	}{
		{"none", nil, nil},
		{"go", []projectscan.Type{projectscan.TypeGo}, []string{"gopls"}},
		{
			"polyglot",
			[]projectscan.Type{projectscan.TypeGo, projectscan.TypeNode, projectscan.TypeRust},
			[]string{"gopls", "typescript", "rust-analyzer"},
		},
		{
			"types without preset",
			[]projectscan.Type{projectscan.TypeJava, projectscan.TypeDotNet, projectscan.TypeRuby, projectscan.TypePHP},
			nil,
		},
		{"dedupe", []projectscan.Type{projectscan.TypeGo, projectscan.TypeGo}, []string{"gopls"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lspServerNamesForProjectTypes(tt.types)
			if !equalStrings(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterRunnableServerSpecs(t *testing.T) {
	stubLSPLookPath(t, "gopls")
	specs := []runtimelsp.ServerSpec{
		{Name: "gopls", Command: "gopls"},
		{Name: "typescript", Command: "typescript-language-server"},
	}
	runnable, missing := filterRunnableServerSpecs(specs)
	if len(runnable) != 1 || runnable[0].Name != "gopls" {
		t.Fatalf("runnable = %#v", runnable)
	}
	if len(missing) != 1 || missing[0] != "typescript" {
		t.Fatalf("missing = %#v", missing)
	}
}

func TestRunChatLSPBootstrapWritesWorkspaceConfig(t *testing.T) {
	isolateLSPBootstrapHome(t)
	stubLSPLookPath(t, "gopls")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n")

	runChatLSPBootstrap(context.Background(), nil, root, "", lspBootstrapBaseConfig(root))

	path := agentconfig.WorkspaceRuntimeConfigPath(root)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("workspace runtime config was not written: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "enabled: true") || !strings.Contains(text, "gopls") {
		t.Fatalf("workspace runtime config missing lsp enable:\n%s", text)
	}
	if strings.Contains(text, "typescript") {
		t.Fatalf("server that is not installed was persisted:\n%s", text)
	}
}

func TestRunChatLSPBootstrapRespectsExplicitDisable(t *testing.T) {
	isolateLSPBootstrapHome(t)
	stubLSPLookPath(t, "gopls")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n")
	path := agentconfig.WorkspaceRuntimeConfigPath(root)
	writeTestFile(t, path, "lsp:\n  enabled: false\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	runChatLSPBootstrap(context.Background(), nil, root, "", lspBootstrapBaseConfig(root))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("explicit lsp.enabled was modified:\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestRunChatLSPBootstrapSkipsUnknownProject(t *testing.T) {
	isolateLSPBootstrapHome(t)
	stubLSPLookPath(t, "gopls")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "README.md"), "nothing to detect\n")

	runChatLSPBootstrap(context.Background(), nil, root, "", lspBootstrapBaseConfig(root))

	if _, err := os.Stat(agentconfig.WorkspaceRuntimeConfigPath(root)); !os.IsNotExist(err) {
		t.Fatalf("workspace runtime config must not be written, stat err = %v", err)
	}
}

func TestRunChatLSPBootstrapSkipsTypesWithoutPreset(t *testing.T) {
	isolateLSPBootstrapHome(t)
	stubLSPLookPath(t, "gopls")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "pom.xml"), "<project/>\n")

	runChatLSPBootstrap(context.Background(), nil, root, "", lspBootstrapBaseConfig(root))

	if _, err := os.Stat(agentconfig.WorkspaceRuntimeConfigPath(root)); !os.IsNotExist(err) {
		t.Fatalf("workspace runtime config must not be written, stat err = %v", err)
	}
}

func TestRunChatLSPBootstrapSkipsMissingServers(t *testing.T) {
	isolateLSPBootstrapHome(t)
	stubLSPLookPath(t) // 本机没有任何服务器
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n")

	runChatLSPBootstrap(context.Background(), nil, root, "", lspBootstrapBaseConfig(root))

	if _, err := os.Stat(agentconfig.WorkspaceRuntimeConfigPath(root)); !os.IsNotExist(err) {
		t.Fatalf("workspace runtime config must not be written, stat err = %v", err)
	}
}

func TestPrepareLateLSPToolSurfaceNilSafe(t *testing.T) {
	prepareLateLSPToolSurface(nil)
	prepareLateLSPToolSurface(&ChatSession{})
}

func TestChatLSPLateEnableApplyWithoutCatalog(t *testing.T) {
	late := &chatLSPLateEnable{pending: true}
	if got := late.apply(&ChatSession{}); got != 0 {
		t.Fatalf("apply = %d, want 0", got)
	}
}

// 回归：工作区不在 .aicli 层栈内（CWD 与工作区不同）且启动时已存在层配置时，
// 扫描写入的工作区文件必须经「单文件叠加」生效并挂池——若被合并分支提前
// 返回，刚写入的 lsp 段进不了生效配置，本次会话永远挂不上池。
func TestRunChatLSPBootstrapReloadsOutsideLayerStackWithLayerConfig(t *testing.T) {
	home := t.TempDir()
	restore := agentconfig.UserHomeDirForTest()
	agentconfig.SetUserHomeDirForTest(func() (string, error) { return home, nil })
	t.Cleanup(func() { agentconfig.SetUserHomeDirForTest(restore) })
	userLayer := filepath.Join(home, ".aicli", "runtime.yaml")
	writeTestFile(t, userLayer, "workspace:\n  root: \".\"\n")
	if !chatRuntimeConfigLayerMatch(userLayer) {
		t.Fatalf("fixture: %s must be recognized as an .aicli layer", userLayer)
	}

	stubLSPLookPath(t, "gopls")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/demo\n")

	base := lspBootstrapBaseConfig(root)
	manager := runtimetools.NewDefaultManagerWithRuntimeConfig(nil, base)
	t.Cleanup(func() { _ = manager.Close() })
	late := &chatLSPLateEnable{toolManager: manager}

	runChatLSPBootstrap(context.Background(), late, root, userLayer, base)

	if !late.pending {
		t.Fatalf("LSP pool was not attached from the workspace config written by the scan")
	}
}
