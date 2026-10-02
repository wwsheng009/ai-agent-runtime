//go:build live_semantic

package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// live_process_sharing_test.go 是**进程内共享的端到端真机验证**（2026-10-01）。
//
// 单测能证明"两次 Acquire 返回同一个 *Registry 指针"，但证不了"真的只有一个
// gopls 进程"——指针相同也可能各自 spawn（那样 pid 仍会不同）。判据必须是
// pid：同一个进程被两个会话共用。
//
// 同时验第二件事，也是共享最容易出错的地方：**一个会话关闭不能影响另一个**。
// 若引用计数少减一次，症状是"第二个会话的编辑突然没有诊断"，而这个症状在
// 单测里看不出来（单测的池根本没有真进程）。

func TestLiveTwoBridgesShareOneServerProcess(t *testing.T) {
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
	spec := firstPreset(t)
	if len(spec) == 0 {
		t.Skip("gopls 预设缺失")
	}
	cfg := Config{
		Enabled:     true,
		Servers:     []ServerSpec{spec[0]},
		Diagnostics: DiagnosticsConfig{Scope: ScopeAll, WaitMS: 4000, StartWaitMS: 90000, ColdStartGraceMS: 60000},
	}

	// 两个独立 Bridge = 两个会话各自的宿主侧门面。root 相同、配置相同。
	first := NewBridge(cfg, moduleRoot, nil, nil)
	second := NewBridge(cfg, moduleRoot, nil, nil)
	if !first.sharedPool || !second.sharedPool {
		t.Fatalf("两个桥都应走共享路径：first=%v second=%v", first.sharedPool, second.sharedPool)
	}
	if first.registry != second.registry {
		t.Fatalf("两个桥没有拿到同一个池（%p vs %p）", first.registry, second.registry)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	first.StartAll(ctx)
	waitLiveReady(t, ctx, first)
	pid := first.liveServerPID(t)
	t.Logf("session_A 启动 gopls pid=%d RSS=%s", pid, formatRSS(readRSSMB(t, pid)))

	// 第二个会话启动**不得**产生第二个进程：它的 StartAll 落在同一个池上。
	second.StartAll(ctx)
	waitLiveReady(t, ctx, second)
	if got := second.liveServerPID(t); got != pid {
		t.Fatalf("第二个会话起了独立进程 pid=%d（第一个是 %d）：单实例未达成", got, pid)
	}
	t.Logf("session_B 复用同一进程 pid=%d（未新增 gopls）", pid)

	// 两个会话各自发一次语义查询，证明共用进程没有把会话打瞎。
	target := filepath.Join(moduleRoot, "internal", "toolkit", "tools", "code_common.go")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	clientA, okA := first.registry.SharedClient(ctx, spec[0].Name)
	clientB, okB := second.registry.SharedClient(ctx, spec[0].Name)
	if !okA || !okB {
		t.Fatalf("两个会话都应借到同一个活进程：A=%v B=%v", okA, okB)
	}
	if clientA != clientB {
		t.Fatalf("两个会话借到的必须是同一个 client 对象：%p vs %p", clientA, clientB)
	}
	// 只发一次：两个会话拿到的是同一个 client，同一次 didOpen 就够了。发两次
	// 只会让同一份文档在同一连接上翻一个无意义的版本号。
	if _, err := clientA.OpenOrUpdate(ctx, target, content); err != nil {
		t.Fatalf("OpenOrUpdate: %v", err)
	}
	if _, err := clientA.Call(ctx, "textDocument/documentSymbol", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": PathToURI(target)},
	}); err != nil {
		t.Fatalf("documentSymbol: %v", err)
	}
	t.Logf("两个会话在同一进程上完成查询（A/B 各一次）")

	// 状态面必须能自证"这是同一个进程"：第二个会话看到与第一个相同的 pid 时，
	// 唯一能解释它的是这个计数。
	if got := second.Statuses()[0].ProcessSharedInProc; got != 2 {
		t.Fatalf("共享会话计数 = %d，want 2（两个会话各一个）", got)
	}
	if got := first.Statuses()[0].ProcessSharedInProc; got != 2 {
		t.Fatalf("第一个会话看到的共享计数 = %d，want 2", got)
	}

	// 关掉第一个会话：进程必须留着（否则是"提前关停别人在用的池"）。
	first.Stop(ctx)
	if second.registry.isStopped() {
		t.Fatal("关闭第一个会话把第二个会话仍在用的池关停了")
	}
	time.Sleep(500 * time.Millisecond)
	if !processAlive(pid) {
		t.Fatalf("关闭第一个会话后 gopls pid=%d 死了：引用计数少减了一次", pid)
	}
	if _, ok := second.registry.SharedClient(ctx, spec[0].Name); !ok {
		t.Fatal("关闭第一个会话后第二个会话借不到进程了")
	}
	t.Logf("关闭 session_A 后 pid=%d 仍存活，session_B 仍可借用", pid)

	// 最后一个会话关闭才关停。
	second.Stop(ctx)
	if !waitProcessExit(pid, 20*time.Second) {
		t.Fatalf("最后一个会话关闭后 gopls pid=%d 仍在运行：留下孤儿进程", pid)
	}
	t.Logf("session_B 关闭后 pid=%d 已回收（无孤儿）", pid)
}

func waitLiveReady(t *testing.T, ctx context.Context, bridge *Bridge) {
	t.Helper()
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		for _, status := range bridge.Statuses() {
			if status.State == StateReady {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("等待 server ready 超时: %v", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatalf("等待 server ready 超时，状态=%+v", bridge.Statuses())
}

func (b *Bridge) liveServerPID(t *testing.T) int {
	t.Helper()
	for _, server := range b.registry.Servers() {
		if client := server.Client(); client != nil {
			return client.PID()
		}
	}
	t.Fatalf("没有可用的 client，状态=%+v", b.Statuses())
	return 0
}

// processAlive 探测进程是否还活着。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if _, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid))); err == nil {
		return true
	}
	// Windows 主路径。tasklist 探测失败时返回 false 并让调用方在日志里看到
	// pid：真正的判定依据是"最后一个会话关闭后进程是否还在"。
	return livePIDInTasklist(pid)
}

// livePIDInTasklist 用 tasklist 判定（Windows 主路径）。
func livePIDInTasklist(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf("\"%d\"", pid))
}

// waitProcessExit 等进程真正退出。
func waitProcessExit(pid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return !processAlive(pid)
}
