//go:build live_semantic

package lsp

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
)

// live_hard_exit_orphan_test.go 验收 ADR-0005 D2：
// **宿主被强杀后不得残留 LSP 孤儿**。
//
// ## 为什么这个验收不能只拿 gopls 跑
//
// LSP over stdio 的现实退出机制是"客户端关闭 stdin → 服务端读到 EOF → 自行退出"。
// gopls 遵守这个约定，所以用 gopls 验出来的"无孤儿"**不能证明任何保证**——
// 就算生产路径根本没绑定 Job Object，这个实验照样会通过，因为 gopls 自己够乖。
//
// 2026-10-01 的第一版正是这么写的，结果它证明的只是 gopls 的礼貌。
//
// ## 这里验的是什么
//
// 用 `testdata/stubborn_lsp`：完成 initialize 握手后**再也不读 stdin**，睡死。
// 宿主死亡对它毫无影响，它会一直活着。于是：
//
//	宿主被强杀 → stub 消失	⇒ 只能来自 OS 级生命周期绑定
//	宿主被强杀 → stub 存活	⇒ 绑定没生效，真孤儿
//
// ## 这个实验踩过的坑，留作判据
//
// 1. `taskkill /T` 会连子进程树一起杀，stub 被 taskkill 直接干掉 → 实验自证。
//    必须**不带 /T**：要验的正是"只死父进程"。
// 2. 必须有"杀之前它还活着"的对照组，否则"事后查不到"可能只是探针坏了或
//    helper 报了个不存在的 pid。
// 3. 探针不能用英文 "No tasks" 判定（本地化 Windows 上那句话不存在，
//    会恒判存活）。匹配带引号的 pid 字段。

func TestLiveHardExitLeavesNoOrphanServer(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER") == "1" {
		runHardExitHelper()
		return
	}
	if testing.Short() {
		t.Skip("live 实验")
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Skipf("找不到 backend/go.mod: %v", err)
	}
	stub := buildStubbornServer(t)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=TestLiveHardExitLeavesNoOrphanServer", "-test.v")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER=1",
		"LSP_HELPER_ROOT="+moduleRoot,
		"LSP_HELPER_STUB="+stub,
	)
	cmd.Dir = moduleRoot
	out := &strings.Builder{}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 helper: %v", err)
	}
	pid := waitHelperPID(t, out, 90*time.Second)
	t.Logf("helper 停住时 stubborn server pid=%d", pid)

	// 对照组：强杀前必须活着。
	if !processAlive(pid) {
		t.Fatalf("对照组失败：强杀前 pid=%d 就不在了，探针或 helper 输出不可信", pid)
	}

	// 只杀父进程。绝不能带 /T。
	kill := "taskkill"
	if runtime.GOOS != "windows" {
		kill = "kill"
	}
	var killErr error
	if runtime.GOOS == "windows" {
		killErr = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	} else {
		killErr = cmd.Process.Kill()
	}
	if killErr != nil {
		t.Fatalf("强杀 helper 失败: %v", killErr)
	}
	_ = cmd.Wait()
	_ = kill

	if alive := waitProcessGone(pid, 30*time.Second); alive {
		reapPID(t, pid)
		t.Fatalf("宿主被强杀后 stubborn server pid=%d 仍存活：进程树绑定没生效，留下了真孤儿", pid)
	}
	t.Logf("宿主被强杀后 stubborn server pid=%d 已消失：绑定生效（不是靠服务端读 EOF）", pid)
}

// TestLiveGracefulStopReapsStubbornServer 覆盖另一侧：有序关停也必须收掉
// 不合规的服务端。这里若只杀直接子进程，stub 会活下来——收树
// （TerminateJobObject / 进程组）才是关键。
func TestLiveGracefulStopReapsStubbornServer(t *testing.T) {
	if testing.Short() {
		t.Skip("live 实验")
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs module root: %v", err)
	}
	stub := buildStubbornServer(t)
	spec := ServerSpec{
		Name:       "stubborn",
		Command:    stub,
		Extensions: []string{".go"},
		Languages:  []string{"go"},
	}
	cfg := Config{
		Enabled:     true,
		Servers:     []ServerSpec{spec},
		Diagnostics: DiagnosticsConfig{Scope: ScopeAll, WaitMS: 2000, StartWaitMS: 20000, ColdStartGraceMS: 5000},
	}
	bridge := NewBridge(cfg, moduleRoot, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	bridge.StartAll(ctx)
	waitLiveReady(t, ctx, bridge)
	pid := bridge.liveServerPID(t)
	t.Logf("stubborn server 就绪 pid=%d", pid)

	bridge.Stop(ctx)
	if alive := waitProcessGone(pid, 30*time.Second); alive {
		reapPID(t, pid)
		t.Fatalf("有序关停后 stubborn server pid=%d 仍存活：收树没生效", pid)
	}
	t.Logf("有序关停后 stubborn server pid=%d 已回收", pid)
}

// runHardExitHelper 起一个 stubborn server，然后停住等父进程强杀。
// 刻意不做任何清理：这才是被强杀的形态。
func runHardExitHelper() {
	root := os.Getenv("LSP_HELPER_ROOT")
	stub := os.Getenv("LSP_HELPER_STUB")
	spec := ServerSpec{
		Name:       "stubborn",
		Command:    stub,
		Extensions: []string{".go"},
		Languages:  []string{"go"},
	}
	cfg := Config{
		Enabled:     true,
		Servers:     []ServerSpec{spec},
		Diagnostics: DiagnosticsConfig{Scope: ScopeAll, WaitMS: 2000, StartWaitMS: 20000, ColdStartGraceMS: 5000},
	}
	bridge := NewBridge(cfg, root, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	bridge.StartAll(ctx)
	waitLiveReady(&testing.T{}, ctx, bridge)
	fmt.Printf("HELPER_STUB_PID=%d\n", bridge.liveServerPID(&testing.T{}))
	select {}
}

func buildStubbornServer(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs module root: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "stubborn_lsp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./internal/lsp/testdata/stubborn_lsp")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("构建 stubborn_lsp 失败（跳过，不臆测）: %v\n%s", err, out)
	}
	return bin
}

// waitHelperPID 等 helper 报出 stubborn server 的 pid。
func waitHelperPID(t *testing.T, out *strings.Builder, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid := parseHelperPID(out.String()); pid > 0 {
			return pid
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("helper 未在超时内报出 pid，输出：\n%s", out.String())
	return 0
}

func parseHelperPID(out string) int {
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.Index(line, "HELPER_STUB_PID="); idx >= 0 {
			if pid, err := strconv.Atoi(strings.TrimSpace(line[idx+len("HELPER_STUB_PID="):])); err == nil {
				return pid
			}
		}
	}
	return 0
}

// waitProcessGone 等进程消失；返回 true 表示超时后仍然存活。
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return false
		}
		time.Sleep(500 * time.Millisecond)
	}
	return processAlive(pid)
}

// reapPID 收掉实验残留，别把孤儿留在机器上。
func reapPID(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
		return
	}
	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Kill()
	}
}
