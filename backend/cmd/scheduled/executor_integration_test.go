package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// TestBuildHeadlessExecutorAssemblesAndRuns 是 E3.3 的**全栈冒烟**：
// 用最小 runtime 配置（全部落盘路径指向临时目录，避免污染用户环境）验证
// buildHeadlessExecutor 能真正装配出执行核（bootstrap.Manager + Handler），
// 且适配器可调用真实执行核（未知会话返回领域错误而非 panic）。
//
// 注意：Handler 懒加载的 `session_runtime.sqlite` 在进程内没有关闭路径
// （长驻 daemon 场景可接受），因此这里用 best-effort 清理，避免 Windows 上
// 文件占用导致测试失败。
func TestBuildHeadlessExecutorAssemblesAndRuns(t *testing.T) {
	dir, err := os.MkdirTemp("", "sched-headless-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = filepath.Join(dir, "sessions")
	cfg.Team.StorePath = filepath.Join(dir, "team.sqlite")
	cfg.SessionRuntime.StorePath = filepath.Join(dir, "session_runtime.sqlite")
	// 关闭热加载 watcher（daemon 不需要目录监听，也避免测试期 goroutine）。
	cfg.HotReload.Enabled = false

	headless, err := buildHeadlessExecutor(cfg, filepath.Join(dir, "runtime.yaml"))
	if err != nil {
		t.Fatalf("buildHeadlessExecutor: %v", err)
	}
	defer headless.close()
	if headless.executor == nil {
		t.Fatal("headless executor must not be nil")
	}

	// 真实执行核可被调用：未知会话应返回领域错误（不 panic、不静默成功）。
	err = (scheduleExecutorAdapter{exec: headless.executor, profile: "scheduled"}).
		ExecuteTurn(context.Background(), "session-smoke", "noop")
	if err == nil {
		t.Fatal("unknown session must return an error (never silently succeed)")
	}
	t.Logf("ExecuteTurn(unknown session) err=%v", err)
}

// TestBuildHeadlessExecutorRequiresConfig 验证缺配置时 fail-closed。
func TestBuildHeadlessExecutorRequiresConfig(t *testing.T) {
	if _, err := buildHeadlessExecutor(nil, ""); err == nil {
		t.Fatal("nil config must error")
	}
}
