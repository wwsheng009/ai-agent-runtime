//go:build windows && !win7compat

package transport

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// minimalMCPServerJS 是一个最小可用的 MCP stdio server：
// 实现 initialize / tools/list，并把收到的 argv 落盘供断言。
const minimalMCPServerJS = `
const fs = require('fs');
const readline = require('readline');
if (process.env.AICLI_ARGV_DUMP) {
  fs.writeFileSync(process.env.AICLI_ARGV_DUMP, JSON.stringify(process.argv.slice(2)));
}
const rl = readline.createInterface({ input: process.stdin });
const send = (msg) => process.stdout.write(JSON.stringify(msg) + '\n');
rl.on('line', (line) => {
  if (!line.trim()) return;
  let req;
  try { req = JSON.parse(line); } catch (e) { return; }
  if (req.method === 'initialize') {
    send({ jsonrpc: '2.0', id: req.id, result: {
      protocolVersion: (req.params && req.params.protocolVersion) || '2025-06-18',
      capabilities: { tools: {} },
      serverInfo: { name: 'shim-e2e', version: '1.0.0' },
    }});
  } else if (req.method === 'tools/list') {
    send({ jsonrpc: '2.0', id: req.id, result: { tools: [
      { name: 'echo_ping', description: 'e2e probe', inputSchema: { type: 'object', properties: {} } },
    ]}});
  } else if (req.method === 'notifications/initialized') {
    // notification：无响应
  } else if (req.id !== undefined) {
    send({ jsonrpc: '2.0', id: req.id, error: { code: -32601, message: 'not found: ' + req.method } });
  }
});
`

// TestStdioShimFullHandshakeThroughSpacedPath 是最强的端到端验证：
// 真实 cmd.exe → 含空格目录里的 .cmd 垫片 → node MCP server → initialize → tools/list。
// 同时断言最终进程收到的 argv 与配置逐字节一致。
func TestStdioShimFullHandshakeThroughSpacedPath(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available on PATH")
	}
	base := t.TempDir()
	scriptDir := filepath.Join(base, "mcp server dir")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	serverJS := filepath.Join(scriptDir, "server.js")
	if err := os.WriteFile(serverJS, []byte(minimalMCPServerJS), 0o644); err != nil {
		t.Fatalf("write server: %v", err)
	}
	shim := filepath.Join(scriptDir, "launcher shim.cmd")
	shimSource := "@echo off\r\n" + `"` + node + `" "%~dp0server.js" %*` + "\r\n"
	if err := os.WriteFile(shim, []byte(shimSource), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	dumpPath := filepath.Join(base, "argv.json")
	t.Setenv("AICLI_ARGV_DUMP", dumpPath)

	wantArgs := []string{"--userDataDir", `C:\Users\demo user\Edge\User Data`}
	tr := NewStdioTransport(&Config{Type: "stdio", Command: shim, Args: wantArgs})
	observer := &recordingObserver{}
	tr.AddLifecycleObserver(observer.observe)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sdkTransport := tr.ToMCPSdkTransport(ctx)
	client := mcp.NewClient(&mcp.Implementation{Name: "transport-e2e", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, sdkTransport, nil)
	if err != nil {
		t.Fatalf("Connect 失败: %v\n%s", err, StderrDiagnosticsOf(tr))
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools 失败: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "echo_ping" {
		t.Fatalf("工具列表 = %+v, 期望 [echo_ping]", tools.Tools)
	}

	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("读取 argv dump 失败: %v", err)
	}
	var got []string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("解析 argv dump 失败: %v (raw=%s)", err, data)
	}
	if strings.Join(got, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("子进程 argv = %#v, 期望 %#v", got, wantArgs)
	}

	// 连接建立时进程树守卫必须真的绑定（Windows: Job Object）。
	if report := tr.treeGuardReport(); report.AttachErr != "" {
		t.Fatalf("Job Object 绑定失败: %s", report.AttachErr)
	}
}

// TestStdioShimFailureSurfacesStderrTail 验证 P1：启动即失败时，用户可见错误与
// 生命周期事件里都能看到子进程 stderr 尾部与退出码，而不是裸 EOF。
func TestStdioShimFailureSurfacesStderrTail(t *testing.T) {
	base := t.TempDir()
	scriptDir := filepath.Join(base, "broken shim dir")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shim := filepath.Join(scriptDir, "broken shim.cmd")
	shimSource := "@echo off\r\necho 'C:\\Program' is not recognized as an internal or external command 1>&2\r\nexit /b 3\r\n"
	if err := os.WriteFile(shim, []byte(shimSource), 0o644); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	tr := NewStdioTransport(&Config{Type: "stdio", Command: shim})
	observer := &recordingObserver{}
	tr.AddLifecycleObserver(observer.observe)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sdkTransport := tr.ToMCPSdkTransport(ctx)
	client := mcp.NewClient(&mcp.Implementation{Name: "transport-e2e", Version: "1.0.0"}, nil)
	_, err := client.Connect(ctx, sdkTransport, nil)
	if err == nil {
		t.Fatal("期望握手失败，实际连接成功")
	}

	enriched := EnrichConnectError(tr, err)
	text := enriched.Error()
	if !strings.Contains(text, "is not recognized") {
		t.Fatalf("错误信息缺少 stderr 尾部: %q", text)
	}
	if !strings.Contains(text, "exit code = 3") {
		t.Fatalf("错误信息缺少退出码: %q", text)
	}
	if !strings.Contains(text, "[stdio 子进程诊断]") {
		t.Fatalf("错误信息缺少诊断头: %q", text)
	}
	t.Logf("用户可见错误:\n%s", text)
}
