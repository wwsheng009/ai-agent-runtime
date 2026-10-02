//go:build live_semantic

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// live_two_managers_share_pool_test.go 证明**生产装配路径**上进程内共享成立。
//
// internal/lsp/live_process_sharing_test.go 已经证明"两个 Bridge 共用一个 gopls
// 进程"。但每个 ChatSession 拿到的是自己的 tools.Manager（chat_setup.go 的
// NewDefaultManagerWithRuntimeConfig），真正的多会话形态是"两个 Manager"而不是
// "两个 Bridge"。这里补的就是这一层：两个 Manager 用同一份配置装配，读同一个
// gopls 进程。
//
// 判据仍然只能用 pid：Manager 之间连指针都拿不到（池藏在 bridge 里），
// 而"两个进程碰巧同号"不成立，所以 pid 相同即单实例。

func TestLiveTwoManagersShareOneServerProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("live 实验")
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Skipf("找不到 backend/go.mod（仓库布局与实验假设不符）: %v", err)
	}
	specs := lsp.PresetServers()
	if len(specs) == 0 {
		t.Skip("gopls 预设缺失")
	}

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = moduleRoot
	cfg.LSP.Enabled = true
	// Prewarm 让池在装配时就起来：这里要观察的就是"装配是否共用"，
	// 懒启动会把两种情形都推迟到第一次编辑之后。
	cfg.LSP.Prewarm = true
	cfg.LSP.Servers = []lsp.ServerSpec{specs[0]}
	cfg.LSP.Diagnostics.StartWaitMS = 90000
	cfg.LSP.Diagnostics.ColdStartGraceMS = 60000

	sessionA := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	t.Cleanup(func() { _ = sessionA.Close() })
	sessionB := NewDefaultManagerWithRuntimeConfig(nil, cfg)
	t.Cleanup(func() { _ = sessionB.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	pidA := waitLiveManagerPID(t, ctx, "A", sessionA)
	t.Logf("session_A 装配后 gopls pid=%d", pidA)

	// 第二个会话装配**不得**产生第二个进程。
	pidB := waitLiveManagerPID(t, ctx, "B", sessionB)
	if pidB != pidA {
		t.Fatalf("两个会话各起了一个 gopls：pid=%d vs %d", pidA, pidB)
	}
	t.Logf("session_B 复用同一进程 pid=%d（未新增 gopls）", pidB)

	// 状态面必须自证，否则第二个会话看到同一个 pid 会被读成"重复实例"。
	statusB := serversOutput(t, ctx, sessionB)
	if !strings.Contains(statusB, "in_proc_sessions=2") {
		t.Fatalf("第二个会话的 lsp_servers 必须说明共用同一进程：\n%s", statusB)
	}

	// 关闭第一个会话不能带走第二个会话正在用的进程。
	if err := sessionA.Close(); err != nil {
		t.Fatalf("close session_A: %v", err)
	}
	if !processAlive(pidA) {
		t.Fatalf("关闭 A 后 pid=%d 已退出：B 仍在借用同一个进程", pidA)
	}
	t.Logf("关闭 session_A 后 pid=%d 仍存活，session_B 仍可借用", pidA)

	// B 仍能读到这个池（不要求 ready——它可能已空闲），只要求进程还在、归属没丢。
	final := serversOutput(t, ctx, sessionB)
	if got := parseLivePID(final); got != pidA {
		t.Fatalf("B 看到的 pid 变成了 %d，期望仍是 %d：\n%s", got, pidA, final)
	}
}

func serversOutput(t *testing.T, ctx context.Context, manager *Manager) string {
	t.Helper()
	out, err := manager.Execute(ctx, LSPServersToolName, map[string]interface{}{})
	if err != nil {
		t.Fatalf("lsp_servers failed: %v", err)
	}
	return out
}

func parseLivePID(output string) int {
	for _, field := range strings.Fields(output) {
		if !strings.HasPrefix(field, "pid=") {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimPrefix(field, "pid=")); err == nil {
			return pid
		}
	}
	return 0
}

// processAlive 探测进程是否还活着。与 internal/lsp 的 livePIDInTasklist 同一口径：
// 匹配带引号的 pid 字段（tasklist CSV 第一列），而不是匹配英文 "No tasks"——
// 本地化（非英文）Windows 上那句话根本不会出现，那个写法会恒判“存活”。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS != "windows" {
		_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
		return err == nil
	}
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf("\"%d\"", pid))
}

// waitLiveManagerPID 等该会话的池成员就绪并返回 pid。
func waitLiveManagerPID(t *testing.T, ctx context.Context, label string, manager *Manager) int {
	t.Helper()
	deadline := time.Now().Add(240 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			t.Fatalf("session_%s 等待超时，最后状态：\n%s", label, last)
		}
		last = serversOutput(t, ctx, manager)
		if strings.Contains(last, "state=ready") || strings.Contains(last, ": ready") {
			if pid := parseLivePID(last); pid > 0 {
				return pid
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("session_%s 未在超时内 ready，最后状态：\n%s", label, last)
	return 0
}
