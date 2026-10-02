//go:build live_semantic

package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// live_shared_endpoint_test.go 验证**跨进程共享的可行前提**（2026-10-01）。
//
// TestLiveSecondInstanceCostExperiment 已经量到：第二个实例要付 ~97% 的完整成本
// （525MB vs 541MB），因为 gopls 的索引/堆是进程私有的，没有任何东西被共享。
//
// 那么"共享"的可行性取决于两件事，本文件各验一条：
//
//  1. gopls 是否接受多客户端连接 → `gopls -listen=<addr>`（本文件验证）
//  2. 我们的客户端能否在**不拥有进程**的情况下用那条连接 → DialResult.Conn 是
//     io.ReadWriteCloser，Kill/Wait 可为 nil（client.go:20-37 的注释明确写了
//     "starts (or attaches to)" 与 "May be nil"），本文件也验证
//
// 判据不是"能连上"，而是：**两个各自 initialize 的客户端，对同一文件各自拿到
// 正确的 definition，且只有一个 gopls 进程**。前者证明会话隔离可用，后者证明
// 单实例目标达成。
func TestLiveSharedEndpointServesTwoClients(t *testing.T) {
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
	target := filepath.Join(moduleRoot, "internal", "toolkit", "tools", "code_common.go")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}

	// 监听端口由我们自己选：gopls 的 -listen 不会回填实际地址到 stdout，
	// 因此先占一个空闲端口再交出去（TOCTOU 风险可接受：实验串行执行）。
	addr := reserveLoopbackAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// 唯一的 spawn 点：进程归我们所有，因此带 Kill/Wait（进程树语义）。
	cmd := exec.CommandContext(ctx, spec[0].Command, "-listen="+addr)
	cmd.Dir = moduleRoot
	stderr, err := os.CreateTemp(t.TempDir(), "gopls-listen-*.log")
	if err != nil {
		t.Fatalf("stderr file: %v", err)
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("gopls -listen 不可用（跳过，不臆测）: %v", err)
	}
	t.Cleanup(func() {
		// 必须关掉自己的句柄，否则 TempDir 清理会因文件被占用而失败。
		_ = stderr.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	pid := cmd.Process.Pid
	t.Logf("gopls -listen=%s pid=%d", addr, pid)

	// 等端口就绪（gopls 可能要几十秒建索引才 accept）。
	waitTCP(t, addr, 60*time.Second)

	// attach：只给连接，不给 Kill/Wait —— 借来的进程不得被我们关停。
	attach := func(tag string) *Client {
		t.Helper()
		var dialErr error
		dial := func(context.Context, ServerSpec, string, Logger) (*DialResult, error) {
			if dialErr != nil {
				return nil, dialErr
			}
			conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
			if err != nil {
				dialErr = fmt.Errorf("%s dial: %w", tag, err)
				return nil, dialErr
			}
			return &DialResult{
				Conn:       conn,
				PID:        pid,
				StderrTail: func() string { return "" },
			}, nil
		}
		client, err := NewClient(ClientOptions{Spec: spec[0], Root: moduleRoot, Dial: dial, StartupTimeout: 120 * time.Second})
		if err != nil {
			t.Fatalf("%s NewClient: %v", tag, err)
		}
		if err := client.Start(ctx); err != nil {
			t.Fatalf("%s Start: %v", tag, err)
		}
		t.Cleanup(func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			// 注意：Shutdown 只发 LSP shutdown/exit，不碰进程——我们没有 Kill。
			client.Shutdown(shutdownCtx)
		})
		if _, err := client.OpenOrUpdate(ctx, target, content); err != nil {
			t.Fatalf("%s OpenOrUpdate: %v", tag, err)
		}
		return client
	}

	a := attach("client_A")
	b := attach("client_B")

	// 两个客户端各自拿到 definition：会话隔离成立（否则共享会把两个会话的
	// 文档状态搅在一起——这是本方案最大的正确性风险，必须实测而不是推断）。
	// 位置按符号定位而不是硬编码行列：硬编码的位置一旦文件演进就会静默返回
	// null，让"两个客户端一致"这个断言变成"两个 null 一致"的假通过。
	symbolLine, symbolChar := locateSymbol(t, target, string(content))
	t.Logf("定位符号位置 %s:%d:%d", filepath.Base(target), symbolLine, symbolChar)
	defA := definitionOf(t, ctx, a, target, symbolLine, symbolChar)
	defB := definitionOf(t, ctx, b, target, symbolLine, symbolChar)
	t.Logf("client_A definition = %s", defA)
	t.Logf("client_B definition = %s", defB)
	if !strings.HasPrefix(defA, "file://") {
		t.Fatalf("client_A 没有拿到 definition（拿到 %q）：共享连接不可用", defA)
	}
	if !strings.EqualFold(defA, defB) {
		t.Fatalf("两个客户端对同一位置的 definition 不一致: A=%q B=%q", defA, defB)
	}

	// 单实例判据：一个进程。gopls 自己 fork 的 telemetry 子进程不算。
	if a.PID() != b.PID() || a.PID() != pid {
		t.Fatalf("两个客户端应共用同一进程: A=%d B=%d spawn=%d", a.PID(), b.PID(), pid)
	}
	t.Logf("单实例达成：两个客户端共用 pid=%d，RSS=%s", pid, formatRSS(readRSSMB(t, pid)))
}

// locateSymbol 在文件里找一个真实的函数声明位置（返回 "func Name(" 里左括号
// 的列），用于发起一次必然有结果的 definition 查询。硬编码行列在文件演进后
// 会静默变成 null，让"两个客户端一致"变成"两个 null 一致"的假通过。
func locateSymbol(t *testing.T, file, content string) (int, int) {
	t.Helper()
	for i, line := range strings.Split(content, "\n") {
		idx := strings.Index(line, "func ")
		if idx < 0 {
			continue
		}
		name := strings.TrimLeft(line[idx+len("func "):], " \t*")
		rel := strings.Index(name, "(")
		if rel <= 0 {
			continue
		}
		return i, idx + len("func ") + rel
	}
	t.Fatalf("%s 里找不到可用于 definition 断言的符号位置", filepath.Base(file))
	return 0, 0
}

func definitionOf(t *testing.T, ctx context.Context, client *Client, file string, line, char int) string {
	t.Helper()
	raw, err := client.Call(ctx, "textDocument/definition", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": PathToURI(file)},
		"position":     map[string]interface{}{"line": line, "character": char},
	})
	if err != nil {
		t.Logf("definition 调用失败: %v", err)
		return ""
	}
	if target := decodeDefinitionTarget(t, raw); target != "" {
		return target
	}
	return fmt.Sprintf("(非空响应 %d 字节)", len(raw))
}

// decodeDefinitionTarget 从 textDocument/definition 响应里取第一个目标 URI
// （gopls 可能返回单对象、数组或 null）。
func decodeDefinitionTarget(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) == 0 || isNullOrEmpty(raw) {
		return ""
	}
	var single struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return single.URI
	}
	var many []struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &many); err == nil && len(many) > 0 {
		return many[0].URI
	}
	return ""
}

// reserveLoopbackAddr 占一个空闲的回环端口并立刻释放（供 gopls 监听）。
func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitTCP(t *testing.T, addr string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(time.Second)
	}
	t.Skipf("gopls 未在 %s 内监听 %s（跳过，不臆测）", budget, addr)
}
