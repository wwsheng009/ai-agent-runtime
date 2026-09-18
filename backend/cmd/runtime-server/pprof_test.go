package main

import (
	"io"
	"net/http"
	"testing"
)

// TestStartPprofServerEndpoints 验证 pprof 服务器注册了常用诊断端点。
func TestStartPprofServerEndpoints(t *testing.T) {
	handle, err := startPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer handle.Close()

	endpoints := []string{
		"/debug/pprof/",
		"/debug/pprof/cmdline",
		"/debug/pprof/symbol",
		"/debug/pprof/allocs",
		"/debug/pprof/block",
		"/debug/pprof/goroutine",
		"/debug/pprof/heap",
		"/debug/pprof/mutex",
		"/debug/pprof/threadcreate",
	}
	for _, path := range endpoints {
		resp, err := http.Get("http://" + handle.Addr() + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
}

// TestStartPprofServerEmptyAddr 验证空地址回退到随机空闲端口。
func TestStartPprofServerEmptyAddr(t *testing.T) {
	handle, err := startPprofServer("")
	if err != nil {
		t.Fatalf("startPprofServer() error = %v", err)
	}
	defer handle.Close()
	if handle.Addr() == "" {
		t.Fatal("startPprofServer() returned empty addr")
	}
}

// TestParseServeOptionsPprofFlag 验证 --pprof 在 serve 子命令可解析。
func TestParseServeOptionsPprofFlag(t *testing.T) {
	opts, err := parseServeOptions([]string{"--config", "x.yaml", "--pprof"})
	if err != nil {
		t.Fatalf("parseServeOptions() error = %v", err)
	}
	if !opts.Pprof {
		t.Fatal("parseServeOptions(--pprof) = Pprof false, want true")
	}
}

// TestParseStartOptionsPprofFlag 验证 --pprof 在 start 子命令可解析。
func TestParseStartOptionsPprofFlag(t *testing.T) {
	opts, err := parseStartOptions([]string{"--pprof"})
	if err != nil {
		t.Fatalf("parseStartOptions() error = %v", err)
	}
	if !opts.Pprof {
		t.Fatal("parseStartOptions(--pprof) = Pprof false, want true")
	}
}

// TestResolveRuntimeServerPprofAddr 验证地址解析逻辑（与 aicli 一致）：
//   - flag/env 都未设置 → 空串（不启动）；
//   - --pprof → 随机空闲端口；AICLI_PPROF 单独设置即可启用并覆盖地址；
//   - --web-port 优先级最高，端口越界报错而非静默退化。
func TestResolveRuntimeServerPprofAddr(t *testing.T) {
	t.Setenv("AICLI_PPROF", "")
	if got, err := resolveRuntimeServerPprofAddr(false, 0, false); err != nil || got != "" {
		t.Fatalf("resolveRuntimeServerPprofAddr(off) = (%q, %v), want empty", got, err)
	}
	if got, err := resolveRuntimeServerPprofAddr(true, 0, false); err != nil || got != "127.0.0.1:0" {
		t.Fatalf("resolveRuntimeServerPprofAddr(--pprof) = (%q, %v), want 127.0.0.1:0", got, err)
	}
	t.Setenv("AICLI_PPROF", "127.0.0.1:6060")
	if got, err := resolveRuntimeServerPprofAddr(true, 0, false); err != nil || got != "127.0.0.1:6060" {
		t.Fatalf("resolveRuntimeServerPprofAddr(env) = (%q, %v), want 127.0.0.1:6060", got, err)
	}
	// env 单独启用（flag 关闭也生效），与 aicli 行为一致。
	if got, err := resolveRuntimeServerPprofAddr(false, 0, false); err != nil || got != "127.0.0.1:6060" {
		t.Fatalf("resolveRuntimeServerPprofAddr(env only) = (%q, %v), want 127.0.0.1:6060", got, err)
	}
	// --web-port 覆盖 env；单独设置也启用。
	if got, err := resolveRuntimeServerPprofAddr(false, 64562, true); err != nil || got != "127.0.0.1:64562" {
		t.Fatalf("resolveRuntimeServerPprofAddr(--web-port) = (%q, %v), want 127.0.0.1:64562", got, err)
	}
	if got, err := resolveRuntimeServerPprofAddr(true, 64562, true); err != nil || got != "127.0.0.1:64562" {
		t.Fatalf("resolveRuntimeServerPprofAddr(--pprof + --web-port) = (%q, %v), want 127.0.0.1:64562", got, err)
	}
	// 越界：0 / 70000 都必须报错。
	for _, bad := range []int{0, 70000, -1} {
		if got, err := resolveRuntimeServerPprofAddr(false, bad, true); err == nil {
			t.Fatalf("resolveRuntimeServerPprofAddr(--web-port %d) = (%q, nil), want error", bad, got)
		}
	}
}

// TestParseWebPortFlag 验证 --web-port 在 serve/start 子命令的解析与"显式设置"标记。
func TestParseWebPortFlag(t *testing.T) {
	serveOpts, err := parseServeOptions([]string{"--web-port", "64562"})
	if err != nil {
		t.Fatalf("parseServeOptions() error = %v", err)
	}
	if !serveOpts.WebPortSet || serveOpts.WebPort != 64562 {
		t.Fatalf("parseServeOptions(--web-port 64562) = (%v, %d), want (true, 64562)", serveOpts.WebPortSet, serveOpts.WebPort)
	}
	startOpts, err := parseStartOptions([]string{"--web-port", "64562", "--pprof"})
	if err != nil {
		t.Fatalf("parseStartOptions() error = %v", err)
	}
	if !startOpts.WebPortSet || startOpts.WebPort != 64562 || !startOpts.Pprof {
		t.Fatalf("parseStartOptions(--web-port/--pprof) = (%v, %d, %v), want (true, 64562, true)",
			startOpts.WebPortSet, startOpts.WebPort, startOpts.Pprof)
	}
	// 未显式设置时不带 WebPortSet，避免把默认 0 当成"指定了端口"。
	defaultOpts, err := parseServeOptions(nil)
	if err != nil {
		t.Fatalf("parseServeOptions(nil) error = %v", err)
	}
	if defaultOpts.WebPortSet {
		t.Fatal("parseServeOptions(nil) = WebPortSet true, want false")
	}
	// 非数字端口由 pflag 直接拒绝。
	if _, err := parseServeOptions([]string{"--web-port", "abc"}); err == nil {
		t.Fatal("parseServeOptions(--web-port abc) = nil error, want parse error")
	}
}

// TestIsLoopbackAddr 验证回环地址判定。
func TestIsLoopbackAddr(t *testing.T) {
	loopback := []string{"127.0.0.1:6060", "localhost:6060", "[::1]:6060"}
	for _, addr := range loopback {
		if !isLoopbackAddr(addr) {
			t.Errorf("isLoopbackAddr(%q) = false, want true", addr)
		}
	}
	nonLoopback := []string{"0.0.0.0:6060", "192.168.1.10:6060", ":", ""}
	for _, addr := range nonLoopback {
		if isLoopbackAddr(addr) {
			t.Errorf("isLoopbackAddr(%q) = true, want false", addr)
		}
	}
}
