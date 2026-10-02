package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSemanticModuleRootFindsNestedGoMod 是本次修复的核心回归：本仓库的
// workspace 根是 repo 根，而 go.mod 在 backend/ 下。原实现只认 <root>/go.mod，
// 于是语义通道恒定降级为 no_supported_language——配置写对也不生效。
func TestSemanticModuleRootFindsNestedGoMod(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "backend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := SemanticModuleRoot(root); got != nested {
		t.Fatalf("SemanticModuleRoot = %q, want %q", got, nested)
	}
	if got := SemanticWorkspaceLanguage(root); got != "go" {
		t.Fatalf("SemanticWorkspaceLanguage = %q, want go", got)
	}
}

func TestSemanticModuleRootPrefersSelf(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module root\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module sub\n"), 0o644); err != nil {
		t.Fatalf("write nested go.mod: %v", err)
	}
	// workspace 根本身就是模块时必须直接用它：结果路径相对化以 workspace 根
	// 为锚，向下选子模块会让所有语义结果路径对不上索引。
	if got := SemanticModuleRoot(root); got != root {
		t.Fatalf("SemanticModuleRoot = %q, want %q", got, root)
	}
}

func TestSemanticModuleRootReturnsShallowest(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deep, "go.mod"), []byte("module deep\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// 深度恰好等于上限：必须命中。
	if got := SemanticModuleRoot(root); got != deep {
		t.Fatalf("SemanticModuleRoot(depth=3) = %q, want %q", got, deep)
	}
	tooDeep := filepath.Join(root, "x", "y", "z", "w")
	if err := os.MkdirAll(tooDeep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tooDeep, "go.mod"), []byte("module deeper\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	// 更深的那个不得命中：命中它意味着探测能扫穿整个仓库。
	if got := SemanticModuleRoot(root); got != deep {
		t.Fatalf("SemanticModuleRoot must stop at the shallowest module, got %q", got)
	}
}

func TestSemanticModuleRootAmbiguousSameDepthDegrades(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"modA", "modB"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
	}
	// 同深度多模块：v1 单通道只能锚一个根，猜一个必然给半数查询错误的
	// 相对路径。降级（空串）比猜更诚实。
	if got := SemanticModuleRoot(root); got != "" {
		t.Fatalf("SemanticModuleRoot = %q, want \"\" (ambiguous)", got)
	}
}

func TestSemanticModuleRootSkipsNoiseDirs(t *testing.T) {
	root := t.TempDir()
	// node_modules 与隐藏目录里的 go.mod 都不得被当成模块根。
	for _, name := range []string{"node_modules", ".cache", "vendor", "worktrees"} {
		dir := filepath.Join(root, name, "pkg")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module noise\n"), 0o644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
	}
	if got := SemanticModuleRoot(root); got != "" {
		t.Fatalf("SemanticModuleRoot = %q, want \"\" (only noise dirs)", got)
	}
}

func TestNewSemanticAdapterModuleRootGate(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.LSP = LSPConfig{Enabled: true, Mode: LSPModeSelf}

	// 无 go.mod → 降级（且 reason 仍是稳定的 no_supported_language）。
	if _, reason := NewSemanticAdapterForWorkspace(cfg, root); reason != SemanticReasonNoLanguage {
		t.Fatalf("reason = %q, want %q", reason, SemanticReasonNoLanguage)
	}

	nested := filepath.Join(root, "backend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	adapter, reason := NewSemanticAdapterForWorkspace(cfg, root)
	if adapter == nil {
		t.Fatalf("nested go.mod must assemble the semantic channel, reason=%q", reason)
	}
	// 构造不启动进程：查询才 Ensure（受 startup_timeout 约束）。
	if adapter.Available() {
		t.Fatal("construction must not start the language server")
	}
	status, ok := adapter.(SemanticStatusAdapter)
	if !ok {
		t.Fatal("lspSemanticAdapter must implement SemanticStatusAdapter")
	}
	st := status.SemanticStatus()
	if st.State != "idle" {
		t.Fatalf("state = %q, want idle (lazy, not started)", st.State)
	}
	// manager Root 必须是模块根（gopls 的类型信息锚点），不是 workspace 根。
	if st.Root != nested {
		t.Fatalf("manager root = %q, want module root %q", st.Root, nested)
	}
	if reason := st.Reason; reason == "" {
		t.Fatal("idle 状态必须带可读原因（否则用户无法区分 idle 与故障）")
	}
	if closer, ok := adapter.(interface{ Close(context.Context) error }); ok {
		_ = closer.Close(context.Background())
	}
	if st.LockPath == "" {
		t.Fatal("锁路径必须可观测（ADR-0002 §4.4 排障需要）")
	}
}

func TestNewSemanticAdapterDisabledReasonsStable(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultConfig()
	cfg.LSP = LSPConfig{Enabled: false, Mode: LSPModeSelf}
	if _, reason := NewSemanticAdapterForWorkspace(cfg, root); reason != SemanticReasonDisabled {
		t.Fatalf("reason = %q, want %q", reason, SemanticReasonDisabled)
	}
	cfg.LSP = LSPConfig{Enabled: true, Mode: "external"}
	if _, reason := NewSemanticAdapterForWorkspace(cfg, root); reason != SemanticReasonModeNotSelf {
		t.Fatalf("reason = %q, want %q", reason, SemanticReasonModeNotSelf)
	}
	cfg.LSP = LSPConfig{Enabled: true, Mode: LSPModeSelf}
	if _, reason := NewSemanticAdapterForWorkspace(cfg, "  "); reason != SemanticReasonWorkspace {
		t.Fatalf("reason = %q, want %q", reason, SemanticReasonWorkspace)
	}
}