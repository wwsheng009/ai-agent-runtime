//go:build live_semantic

// live_semantic_channel_test.go 是语义通道的**真机**端到端验证（LSP live，
// 默认不参与 `go test ./...`）。
//
// 为什么单独一个 build tag：语义通道会真的 spawn gopls，而 CHANGELOG 记录过
// 「异常终止会遗留 gopls 进程（本轮实测残留 1.5GB + 0.4GB，导致后续 go test
// 编译期 OOM）」以及「live 测量需独占运行」。把它绑到显式 tag 上，默认的
// `go test ./...` 不会碰它，避免再次把资源拖垮。
//
// 跑法：
//   go test -tags=live_semantic ./internal/knowledge/ -run TestLiveSemantic -count=1 -v
//
// 覆盖的真实链路（此前没有任何自动化覆盖）：
//
//	workspace 根（不是模块根）→ 模块根探测 → 惰性 Ensure → initialize 握手
//	→ didOpen → definition / references / documentSymbol / workspace/symbol
//	→ canonical 位置转换 → 语义状态快照
package knowledge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// liveModuleWorkspace 构造一个 workspace 根 ≠ 模块根的最小 Go 模块，
// 复刻本仓库的形状（根是 repo，go.mod 在 backend/）。
func liveModuleWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	module := filepath.Join(root, "backend", "internal", "demo")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(path, content string) {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write("backend/go.mod", "module example.com/demo\n\ngo 1.21\n")
	write("backend/internal/demo/target.go", "package demo\n\nfunc Target() int { return 1 }\n")
	write("backend/internal/demo/caller.go", "package demo\n\nfunc Caller() int { return Target() }\n")

	// 模块需要可构建，否则 gopls 会因为编译错误把 Target 的定义解析成错误节点。
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = filepath.Join(root, "backend")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy 失败（环境不具备 live 前提）: %v\n%s", err, out)
	}
	return root
}

func requireGopls(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skipf("gopls 不在 PATH: %v", err)
	}
}

func TestLiveSemanticChannelDefinitionReferencesAndSymbols(t *testing.T) {
	if testing.Short() {
		t.Skip("live 语义通道验证")
	}
	requireGopls(t)
	root := liveModuleWorkspace(t)

	// 先锁住本条链路最关键的断言：workspace 根不是模块根时仍能装配。
	if got := SemanticModuleRoot(root); got != filepath.Join(root, "backend") {
		t.Fatalf("SemanticModuleRoot = %q, want <root>/backend", got)
	}

	cfg := DefaultConfig()
	cfg.LSP.Enabled = true
	cfg.LSP.Mode = LSPModeSelf
	adapter, reason := NewSemanticAdapterForWorkspace(cfg, root)
	if adapter == nil {
		t.Fatalf("语义通道装配失败: reason=%q", reason)
	}
	if closer, ok := adapter.(interface{ Close(context.Context) error }); ok {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = closer.Close(ctx)
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// ---- definition：caller.go 里的 Target() 调用应跳到 target.go 的定义 ----
	callerRel := "backend/internal/demo/caller.go"
	targetRel := "backend/internal/demo/target.go"
	// 列偏移从文件内容实算（canonical UTF-8 字节列），不写死：写死的列号在
	// 内容微调后会悄悄落到别的 token 上，测试仍然"通过"但已失去意义。
	callCol := identifierByteColumn(t, filepath.Join(root, filepath.FromSlash(callerRel)), 2, "Target")
	defs, err := adapter.Definition(ctx, callerRel, 2, callCol)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("Definition 未返回结果")
	}
	if !strings.EqualFold(normalizeLivePath(defs[0].Path), targetRel) {
		t.Fatalf("Definition 路径 = %q, want %q", defs[0].Path, targetRel)
	}
	// canonical 0-based：target.go 第 3 行（1-based）即 canonical 2。
	if defs[0].Line != 2 {
		t.Fatalf("Definition 行 = %d, want 2 (canonical 0-based)", defs[0].Line)
	}

	// ---- references：Target 的引用集合必须含 caller.go，且不含声明行 ----
	refs, err := adapter.References(ctx, targetRel, 2, 5)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("References 未返回结果")
	}
	sawCaller := false
	for _, ref := range refs {
		if strings.EqualFold(normalizeLivePath(ref.Path), callerRel) {
			sawCaller = true
		}
	}
	if !sawCaller {
		t.Fatalf("References 缺少 caller.go 的调用点: %+v", refs)
	}
	// 适配器固定 includeDeclaration=true：声明自身**会**出现，去除是工具侧
	// trySemanticRefsQuery 的责任（与索引通道口径对齐）。这里锁住这个分工，
	// 避免有人为了"干净"在适配器层改掉它、把职责搞反。
	sawDeclaration := false
	for _, ref := range refs {
		if strings.EqualFold(normalizeLivePath(ref.Path), targetRel) && ref.Line == 2 {
			sawDeclaration = true
		}
	}
	if !sawDeclaration {
		t.Fatalf("适配器层应保留声明（includeDeclaration=true），由工具侧剔除: %+v", refs)
	}

	// ---- documentSymbol：target.go 内应列出 Target（层级形状也必须展平） ----
	syms, err := adapter.(DocumentSymbolAdapter).DocumentSymbols(ctx, targetRel)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	foundTarget := false
	for _, sym := range syms {
		if strings.EqualFold(normalizeLivePath(sym.Path), targetRel) && sym.Name == "Target" {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Fatalf("DocumentSymbols 未列出 Target: %+v", syms)
	}

	// ---- workspace/symbol：精确同名查询必须命中，且路径是 workspace 相对 ----
	ws, err := adapter.(WorkspaceSymbolAdapter).WorkspaceSymbols(ctx, "Target", 20)
	if err != nil {
		t.Fatalf("WorkspaceSymbols: %v", err)
	}
	found := false
	for _, sym := range ws {
		if sym.Name == "Target" {
			found = true
			if sym.Path == "" || strings.HasPrefix(sym.Path, "/") {
				t.Fatalf("WorkspaceSymbols 返回非 workspace 相对路径: %q", sym.Path)
			}
		}
	}
	if !found {
		t.Fatalf("WorkspaceSymbols 未命中 Target: %+v", ws)
	}

	// ---- 状态快照：进程应已就绪，且模块根 ≠ workspace 根 ----
	status := adapter.(SemanticStatusAdapter).SemanticStatus()
	if status.State != "ready" {
		t.Fatalf("state = %q (reason=%q), want ready", status.State, status.Reason)
	}
	if status.PID <= 0 {
		t.Fatalf("pid = %d, want >0", status.PID)
	}
	if status.Root != filepath.Join(root, "backend") {
		t.Fatalf("manager root = %q, want 模块根", status.Root)
	}
}

// TestLiveSemanticChannelCreatesLock 验证 ADR-0002 §4.4 的重复防护落点：通道
// 启动后必须在模块根下留下锁记录，供第二个进程/会话判重。
//
// 不断言"同进程内第二条通道被拒"：锁语义是跨进程的（实现里 PID 相同视为
// 自己的锁），那属于实现细节而不是契约，锁死它反而妨碍后续改造。
func TestLiveSemanticChannelCreatesLock(t *testing.T) {
	if testing.Short() {
		t.Skip("live 语义通道验证")
	}
	requireGopls(t)
	root := liveModuleWorkspace(t)
	moduleRoot := filepath.Join(root, "backend")

	cfg := DefaultConfig()
	cfg.LSP.Enabled = true
	cfg.LSP.Mode = LSPModeSelf
	adapter, reason := NewSemanticAdapterForWorkspace(cfg, root)
	if adapter == nil {
		t.Fatalf("通道装配失败: %q", reason)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := adapter.Definition(ctx, "backend/internal/demo/caller.go", 2, 20); err != nil {
		t.Fatalf("Definition: %v", err)
	}
	status := adapter.(SemanticStatusAdapter).SemanticStatus()
	if status.LockPath == "" {
		t.Fatal("锁路径必须可观测")
	}
	if !strings.HasPrefix(filepath.ToSlash(status.LockPath), filepath.ToSlash(filepath.Join(moduleRoot, ".aicli", "knowledge", "lsp"))) {
		t.Fatalf("锁路径 = %q, want 位于模块根 .aicli/knowledge/lsp/ 下", status.LockPath)
	}
	if _, err := os.Stat(status.LockPath); err != nil {
		t.Fatalf("启动后锁文件应存在: %v", err)
	}
	if closer, ok := adapter.(interface{ Close(context.Context) error }); ok {
		t.Cleanup(func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer closeCancel()
			_ = closer.Close(closeCtx)
		})
	}
}

func normalizeLivePath(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(strings.TrimSpace(path)), "\\", "/")
}

// identifierByteColumn 返回 (0-based 行, 标识符名) 的 UTF-8 字节列（canonical）。
func identifierByteColumn(t *testing.T, path string, line int, name string) int {
	t.Helper()
raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
lines := strings.Split(string(raw), "\n")
	if line >= len(lines) {
		t.Fatalf("行 %d 超出 %s 的行数 %d", line, path, len(lines))
	}
idx := strings.Index(lines[line], name)
	if idx < 0 {
	t.Fatalf("%s 第 %d 行没有 %q: %q", path, line, name, lines[line])
	}
	return idx
}

var _ = baselsp.EncodingUTF16
