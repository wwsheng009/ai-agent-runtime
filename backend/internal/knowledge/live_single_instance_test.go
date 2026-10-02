//go:build live_semantic

package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// live_single_instance_test.go 是**单实例约束的真机判据**（收口轮十一）。
//
// 旧实现：诊断池与语义通道各自 spawn，一条链路上会留下两个 gopls。常驻内存
// 翻倍，而本仓库实测的 gopls 崩溃根因正是整机内存耗尽——所以这不是洁癖，
// 是稳定性约束。
//
// 判据选 PID 相等而不是"数 gopls 进程数"：
//   - 确定性：机器上可能同时有别的会话在跑 gopls，进程计数不可复现；
//   - 有鉴别力：若语义通道仍自建进程，它的 PID 必然与池的不同（同 pid 的两个
//     进程不可能并存），所以"PID 相等"只有在真的共用进程时才成立。
// 下面的 TestLiveSelfModePIDDiffers 进一步证明这个判据不是恒真的。

// TestLiveSharedInstanceIsSingleProcess 走完整链路：池起 gopls → 语义通道借用
// 同一进程 → 四个语义查询都走通 → PID 与状态自证共用。
func TestLiveSharedInstanceIsSingleProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("live 语义通道验证")
	}
	requireGopls(t)
	root := liveModuleWorkspace(t)
	moduleRoot := filepath.Join(root, "backend")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// ---- 宿主侧：诊断池（锚在 workspace 根，与生产一致） ----
	poolCfg := baselsp.DefaultConfig()
	poolCfg.Enabled = true
	poolCfg.Servers = baselsp.PresetServersNamed([]string{"gopls"})
	if len(poolCfg.Servers) == 0 {
		t.Skip("gopls 预设缺失")
	}
	pool := baselsp.NewRegistry(poolCfg, root, baselsp.RegistryOptions{})
	server := pool.ServerByName("gopls")
	if server == nil {
		t.Fatal("池里没有 gopls 成员")
	}
	t.Cleanup(func() { pool.Stop(context.Background()) })
	if err := pool.StartServer(ctx, "gopls"); err != nil {
		t.Fatalf("池启动 gopls 失败: %v", err)
	}
	poolClient := server.Client()
	if poolClient == nil {
		t.Fatal("池成员没有 client（启动后应就绪）")
	}
	poolPID := poolClient.PID()
	if poolPID <= 0 {
		t.Fatalf("池 gopls pid = %d, want >0", poolPID)
	}
	t.Logf("pool gopls pid=%d", poolPID)

	// ---- 语义通道：带借用缝构造（单实例） ----
	cfg := DefaultConfig()
	cfg.LSP.Enabled = true
	cfg.LSP.Mode = LSPModeSelf
	sharedCalls := 0
	adapter, reason := NewSemanticAdapterForWorkspaceWithOptions(cfg, root, SemanticAdapterOptions{
		SharedClient: func(inner context.Context, serverName string) *baselsp.Client {
			sharedCalls++
			if strings.ToLower(strings.TrimSpace(serverName)) != "gopls" {
				t.Errorf("借用的 server 名 = %q, want gopls", serverName)
			}
			if inner == nil {
				inner = context.Background()
			}
			budget, cancel := context.WithTimeout(inner, 60*time.Second)
			defer cancel()
			client, ok := pool.SharedClient(budget, serverName)
			if !ok {
				return nil
			}
			return client
		},
	})
	if adapter == nil {
		t.Fatalf("语义通道装配失败: reason=%q", reason)
	}
	t.Cleanup(func() {
		if closer, ok := adapter.(interface{ Close(context.Context) error }); ok {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer closeCancel()
			_ = closer.Close(closeCtx)
		}
	})

	callerRel := "backend/internal/demo/caller.go"
	targetRel := "backend/internal/demo/target.go"

	// ---- definition（走借来的进程） ----
	callCol := identifierByteColumn(t, filepath.Join(root, filepath.FromSlash(callerRel)), 2, "Target")
	defs, err := adapter.Definition(ctx, callerRel, 2, callCol)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(defs) == 0 {
		t.Fatal("借来的进程上 Definition 无结果——借用必须真的能干活，而不是只借到句柄")
	}
	if !strings.EqualFold(normalizeLivePath(defs[0].Path), targetRel) {
		t.Fatalf("Definition 路径 = %q, want %q", defs[0].Path, targetRel)
	}

	// ---- references ----
	refs, err := adapter.References(ctx, targetRel, 2, 5)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) == 0 {
		t.Fatal("借来的进程上 References 无结果")
	}

	// ---- documentSymbol ----
	syms, err := adapter.(DocumentSymbolAdapter).DocumentSymbols(ctx, targetRel)
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(syms) == 0 {
		t.Fatal("借来的进程上 DocumentSymbols 无结果")
	}

	// ---- workspace/symbol（跨模块作用域：池锚在 workspace 根） ----
	ws, err := adapter.(WorkspaceSymbolAdapter).WorkspaceSymbols(ctx, "Target", 20)
	if err != nil {
		t.Fatalf("WorkspaceSymbols: %v", err)
	}
	if len(ws) == 0 {
		t.Fatal("借来的进程上 WorkspaceSymbols 无结果（workspace 根锚定下模块符号仍应可查）")
	}
	t.Logf("workspace/symbol 命中 %d 条", len(ws))

	// ---- 状态自证 ----
	status := adapter.(SemanticStatusAdapter).SemanticStatus()
	if status.State != "ready" {
		t.Fatalf("state = %q (reason=%q), want ready", status.State, status.Reason)
	}
	if !status.Shared {
		t.Fatal("状态必须标记 Shared")
	}
	if status.PID != poolPID {
		t.Fatalf("语义通道 pid = %d，与池的 %d 不同 → 确实起了第二个 gopls", status.PID, poolPID)
	}
	if status.LockPath != "" {
		t.Fatalf("共享模式不得持锁（LockPath=%q）", status.LockPath)
	}
	if sharedCalls == 0 {
		t.Fatal("借用缝从未被调用——说明构造时没有把单实例选项传下去")
	}
	// 借来的进程必须仍然活着：语义通道无权关停宿主进程。
	select {
	case <-poolClient.Done():
		t.Fatal("语义查询把宿主进程弄死了——共享模式不得 Shutdown 借来的 client")
	default:
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, ".aicli", "knowledge", "lsp")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(moduleRoot, ".aicli", "knowledge", "lsp"))
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".lock") {
				t.Fatalf("共享模式留下了锁文件 %q——我们从未持有锁", entry.Name())
			}
		}
	}
}

// TestLiveSelfModePIDDiffers 证明"PID 相等"这个判据有鉴别力：自建模式下语义
// 通道会拿到**另一个** gopls 进程（PID 不同）。没有这条，上面的相等断言可能
// 因为"拿到的根本不是进程"而恒真。
func TestLiveSelfModePIDDiffers(t *testing.T) {
	if testing.Short() {
		t.Skip("live 语义通道验证")
	}
	requireGopls(t)
	root := liveModuleWorkspace(t)

	cfg := DefaultConfig()
	cfg.LSP.Enabled = true
	cfg.LSP.Mode = LSPModeSelf
	// 自建模式（无借用缝）——这正是要消除的旧行为。
	adapter, reason := NewSemanticAdapterForWorkspace(cfg, root)
	if adapter == nil {
		t.Fatalf("自建适配器装配失败: %q", reason)
	}
	t.Cleanup(func() {
		if closer, ok := adapter.(interface{ Close(context.Context) error }); ok {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer closeCancel()
			_ = closer.Close(closeCtx)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if _, err := adapter.Definition(ctx, "backend/internal/demo/caller.go", 2,
		identifierByteColumn(t, filepath.Join(root, "backend", "internal", "demo", "caller.go"), 2, "Target")); err != nil {
		t.Fatalf("Definition: %v", err)
	}
	status := adapter.(SemanticStatusAdapter).SemanticStatus()
	if status.Shared {
		t.Fatal("自建模式不得标记 Shared")
	}
	if status.PID <= 0 {
		t.Fatalf("自建模式 pid = %d, want >0", status.PID)
	}
	if status.LockPath == "" {
		t.Fatal("自建模式必须报告锁路径（ADR-0002 §4.4）")
	}
	t.Logf("自建模式 pid=%d lock=%s", status.PID, status.LockPath)
}