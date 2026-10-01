package knowledge

import (
	"context"
	"errors"
	"testing"
)

// Phase 4 交付 1/2：SPI 能力声明、adapter 选择与降级、离线兜底。
// 全部测试不依赖任何外部进程（tree-sitter/LSP 缺席即是默认环境）。

func TestParseAdapterKind(t *testing.T) {
	cases := map[string]AdapterKind{
		"":            AdapterBuiltin,
		"builtin":     AdapterBuiltin,
		"BUILTIN":     AdapterBuiltin,
		"regex":       AdapterBuiltin,
		"treesitter":  AdapterTreeSitter,
		"tree-sitter": AdapterTreeSitter,
		"lsp":         AdapterLSP,
	}
	for input, want := range cases {
		got, err := ParseAdapterKind(input)
		if err != nil {
			t.Fatalf("ParseAdapterKind(%q) err = %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseAdapterKind(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := ParseAdapterKind("magic"); err == nil {
		t.Fatal("ParseAdapterKind(magic) 应当报错（fail closed）")
	}
}

func TestConfigAdapterAndLSPValidation(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.AdapterKind() != AdapterBuiltin {
		t.Fatalf("默认 adapter = %q, want builtin", cfg.AdapterKind())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置必须合法: %v", err)
	}

	bad := DefaultConfig()
	bad.Adapter = "magic"
	if err := bad.Validate(); err == nil {
		t.Fatal("非法 adapter 必须在加载期被拒绝")
	}

	badLSP := DefaultConfig()
	badLSP.LSP = LSPConfig{Enabled: true, Mode: "external"}
	if err := badLSP.Validate(); err == nil {
		t.Fatal("lsp.mode=external 在 v1 必须被拒绝（ADR-0002 §4.3 无 no-op 选项）")
	}

	lsp := LSPConfig{Enabled: true, Mode: LSPModeSelf}.Normalize()
	if lsp.MaxProcesses != DefaultLSPMaxProcesses || lsp.MemoryLimitMB != DefaultLSPMemoryLimitMB {
		t.Fatalf("LSP 默认上限未补齐: %+v", lsp)
	}
	if !lsp.SemanticEnabled() {
		t.Fatal("enabled+self 时语义通道必须开启")
	}
	if (LSPConfig{Enabled: true, Mode: LSPModeOff}).SemanticEnabled() {
		t.Fatal("mode=off 时语义通道必须关闭")
	}
}

func TestSelectIndexAdapter(t *testing.T) {
	base := DefaultConfig()

	builtin := SelectIndexAdapter(base)
	if builtin.Degraded || builtin.Requested != AdapterBuiltin {
		t.Fatalf("默认选择 = %+v, want builtin 无降级", builtin)
	}
	if _, ok := builtin.Adapter.(builtinAdapter); !ok {
		t.Fatalf("默认 adapter 类型 = %T, want builtinAdapter", builtin.Adapter)
	}

	ts := base
	ts.Adapter = string(AdapterTreeSitter)
	selected := SelectIndexAdapter(ts)
	if !selected.Degraded || selected.Reason == "" {
		t.Fatalf("tree-sitter 缺席时必须降级且给出原因: %+v", selected)
	}
	if _, ok := selected.Adapter.(builtinAdapter); !ok {
		t.Fatalf("降级目标必须是 builtin, got %T", selected.Adapter)
	}

	lspCfg := base
	lspCfg.Adapter = string(AdapterLSP)
	lspSel := SelectIndexAdapter(lspCfg)
	if !lspSel.Degraded || lspSel.Reason == "" {
		t.Fatalf("lsp 不承担索引抽取时必须降级: %+v", lspSel)
	}
}

func TestBuiltinAdapterSPI(t *testing.T) {
	adapter := builtinAdapter{}
	if adapter.Name() != SourceBuiltin {
		t.Fatalf("Name = %q, want %q", adapter.Name(), SourceBuiltin)
	}
	if adapter.Version() != AdapterVersion {
		t.Fatalf("Version = %q, want %q（builtin 版本必须等于身份常量）", adapter.Version(), AdapterVersion)
	}
	if lang, ok := adapter.Detect("a/b/c.go", nil); !ok || lang != "go" {
		t.Fatalf("Detect(.go) = (%q,%v), want (go,true)", lang, ok)
	}
	if _, ok := adapter.Detect("a/b/c.unknown", nil); ok {
		t.Fatal("Detect 未覆盖的后缀必须返回 false")
	}
	if caps := adapter.Capabilities(); caps.Any() {
		t.Fatalf("builtin 是启发式通道，能力面必须全 false: %+v", caps)
	}
}

func TestTreeSitterAdapterUnavailable(t *testing.T) {
	adapter := newTreeSitterAdapter()
	if adapter.Available() {
		t.Fatal("v1 未接入语法，Available 必须为 false")
	}
	if _, err := adapter.Extract(context.Background(), FileRecord{}, nil); !errors.Is(err, ErrAdapterUnavailable) {
		t.Fatalf("Extract err = %v, want ErrAdapterUnavailable", err)
	}
	if adapter.Capabilities().Any() {
		t.Fatal("未接入的通道能力面必须全 false（不得假装可用）")
	}
}

// 能力矩阵 / 离线模式（交付 5）：可选通道缺席时索引仍然可用且降级可观测。
func TestIndexWithUnavailableAdapterStillIndexes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Adapter = string(AdapterTreeSitter)

	result, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	if result.AdapterDegraded != true || result.AdapterDegradeReason == "" {
		t.Fatalf("降级必须可观测: %+v", result)
	}
	if result.Adapter != string(SourceBuiltin) {
		t.Fatalf("实际 adapter = %q, want %q", result.Adapter, SourceBuiltin)
	}
	if result.Symbols == 0 || result.Indexed == 0 {
		t.Fatalf("降级后索引仍必须产出: %+v", result)
	}
}

// Phase 4 交付 4：adapter 版本变化触发全量重建；关闭开关后不触发。
func TestFullRebuildOnAdapterChange(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)

	first, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(first): %v", err)
	}
	if first.FullRebuild {
		t.Fatal("首轮没有旧版本可比，不应标记 FullRebuild")
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	stored, err := store.WorkspaceAdapterVersion(ctx, wsID)
	if err != nil || stored != AdapterVersion {
		t.Fatalf("首轮后 adapter_version = %q (err=%v), want %q", stored, err, AdapterVersion)
	}

	// 模拟旧版本索引：内容未变，但版本不同 → 必须强制重解析。
	if err := store.SetWorkspaceAdapterVersion(ctx, wsID, "builtin/old"); err != nil {
		t.Fatalf("SetWorkspaceAdapterVersion: %v", err)
	}
	second, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(second): %v", err)
	}
	if !second.FullRebuild || second.Indexed != 1 || second.Skipped != 0 {
		t.Fatalf("版本变化必须全量重建: %+v", second)
	}
	if stored, _ = store.WorkspaceAdapterVersion(ctx, wsID); stored != AdapterVersion {
		t.Fatalf("重建后 adapter_version = %q, want %q", stored, AdapterVersion)
	}

	// 关闭开关：版本不同也不再重建（保留旧索引，差异留给诊断）。
	off := false
	cfg.Index.FullRebuildOnAdapterChange = &off
	if err := store.SetWorkspaceAdapterVersion(ctx, wsID, "builtin/old2"); err != nil {
		t.Fatalf("SetWorkspaceAdapterVersion: %v", err)
	}
	third, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(third): %v", err)
	}
	if third.FullRebuild || third.Skipped != 1 {
		t.Fatalf("关闭开关后不得重建: %+v", third)
	}
}

// WorkspaceVersion 必须对 adapter 版本敏感（版本向量口径）。
func TestWorkspaceVersionTracksAdapterVersion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	before, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion: %v", err)
	}
	if err := store.SetWorkspaceAdapterVersion(ctx, wsID, "builtin/other"); err != nil {
		t.Fatalf("SetWorkspaceAdapterVersion: %v", err)
	}
	after, err := WorkspaceVersion(ctx, store, wsID)
	if err != nil {
		t.Fatalf("WorkspaceVersion: %v", err)
	}
	if before == after {
		t.Fatal("adapter 版本变化必须改变工作区知识版本")
	}
}
