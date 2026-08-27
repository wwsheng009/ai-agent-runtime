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
// flag 关闭且无 env 时返回空串；flag 开启默认随机端口；
// AICLI_PPROF 单独设置即可启用并覆盖地址。
func TestResolveRuntimeServerPprofAddr(t *testing.T) {
	t.Setenv("AICLI_PPROF", "")
	if got := resolveRuntimeServerPprofAddr(false); got != "" {
		t.Fatalf("resolveRuntimeServerPprofAddr(false) = %q, want empty", got)
	}
	if got := resolveRuntimeServerPprofAddr(true); got != "127.0.0.1:0" {
		t.Fatalf("resolveRuntimeServerPprofAddr(true) = %q, want 127.0.0.1:0", got)
	}
	t.Setenv("AICLI_PPROF", "127.0.0.1:6060")
	if got := resolveRuntimeServerPprofAddr(true); got != "127.0.0.1:6060" {
		t.Fatalf("resolveRuntimeServerPprofAddr(true) with env = %q, want 127.0.0.1:6060", got)
	}
	// env 单独启用（flag 关闭也生效），与 aicli 行为一致。
	if got := resolveRuntimeServerPprofAddr(false); got != "127.0.0.1:6060" {
		t.Fatalf("resolveRuntimeServerPprofAddr(false) with env = %q, want 127.0.0.1:6060 (env 单独启用)", got)
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
