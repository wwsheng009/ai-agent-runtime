package commands

import (
	"strings"
	"testing"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// stderrProviderManager 在 fakeMCPManager（mcp_run_test.go）之上实现可选的
// stdio 诊断能力，用于验证 `mcp test-server --show-stderr` 的接线。
type stderrProviderManager struct {
	*fakeMCPManager
	stderrTail string
}

func (m *stderrProviderManager) StderrDiagnostics(name string) string { return m.stderrTail }

func TestMCPStderrDiagnosticsReadsOptionalProvider(t *testing.T) {
	provider := &stderrProviderManager{fakeMCPManager: &fakeMCPManager{}, stderrTail: "  boom tail  "}
	if got := mcpStderrDiagnostics(provider, "svc"); got != "boom tail" {
		t.Fatalf("mcpStderrDiagnostics = %q, want %q", got, "boom tail")
	}
	// 不支持该能力的 manager（历史测试替身、Win7 兼容构建）必须安全降级为空串。
	if got := mcpStderrDiagnostics(&fakeMCPManager{}, "svc"); got != "" {
		t.Fatalf("不支持的 manager 应返回空串，实际 %q", got)
	}
}

func TestRenderMCPTestServerShowsStderrTail(t *testing.T) {
	out := captureStdout(t, func() {
		renderMCPTestServerResult("svc", &mcpServerCommandResult{
			Config:     mcpconfig.MCPConfig{Name: "svc", Type: "stdio", Command: "npx"},
			Status:     &mcpconfig.MCPStatus{Name: "svc", Connected: true, ToolCount: 2},
			Tools:      []mcpToolOutput{{Name: "ping", Description: "ping tool"}},
			Success:    true,
			StderrTail: "[stdio 子进程诊断]\nDevTools listening on ws://127.0.0.1:9222",
		}, mcpCommandOptions{OutputFormat: "text"})
	})
	if !strings.Contains(out, "stderr 诊断") || !strings.Contains(out, "DevTools listening") {
		t.Fatalf("text 输出缺少 stderr 诊断: %q", out)
	}
}

func TestRenderMCPTestServerJSONIncludesStderrTail(t *testing.T) {
	out := captureStdout(t, func() {
		renderMCPTestServerResult("svc", &mcpServerCommandResult{
			Config:     mcpconfig.MCPConfig{Name: "svc", Type: "stdio", Command: "npx"},
			Status:     &mcpconfig.MCPStatus{Name: "svc", Connected: false, LastError: "calling \"initialize\": EOF"},
			StderrTail: "exit code = 3",
		}, mcpCommandOptions{OutputFormat: "json"})
	})
	if !strings.Contains(out, `"stderr_tail":"exit code = 3"`) {
		t.Fatalf("json 输出缺少 stderr_tail: %q", out)
	}
}

func TestRenderMCPTestServerShowsLastErrorOnFailure(t *testing.T) {
	out := captureStdout(t, func() {
		renderMCPTestServerResult("svc", &mcpServerCommandResult{
			Config: mcpconfig.MCPConfig{Name: "svc", Type: "stdio", Command: "npx"},
			Status: &mcpconfig.MCPStatus{Name: "svc", Connected: false, LastError: "calling \"initialize\": EOF"},
		}, mcpCommandOptions{OutputFormat: "text"})
	})
	if !strings.Contains(out, "连接失败") || !strings.Contains(out, "initialize") {
		t.Fatalf("失败输出缺少 LastError: %q", out)
	}
}
