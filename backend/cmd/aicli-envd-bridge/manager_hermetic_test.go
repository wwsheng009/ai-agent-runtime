package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
)

// TestManagerHermeticEnvD drives the built bridge through the real MCP manager
// against a stub envd, so CI covers config load → child process → MCP handshake
// → tool call without external infrastructure. It uses the ops profile so the
// generated --allow-command flag is exercised as well.
func TestManagerHermeticEnvD(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}

	bridge := filepath.Join(t.TempDir(), "aicli-envd-bridge")
	if runtime.GOOS == "windows" {
		bridge += ".exe"
	}
	build := exec.Command(goBin, "build", "-trimpath", "-o", bridge,
		"github.com/wwsheng009/ai-agent-runtime/cmd/aicli-envd-bridge")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build bridge: %v\n%s", err, out)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"ts":1,"cpu_count":4,"mem_total":1073741824,"mem_used":536870912,"available":536870912,"disk_used":1048576,"disk_total":10737418240}`)
		case "/process.Process/Start":
			w.Header().Set("Content-Type", "application/connect+json")
			writeConnectFrame(t, w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 42}}})
			writeConnectFrame(t, w, map[string]any{"event": map[string]any{"data": map[string]any{
				"stdout": base64.StdEncoding.EncodeToString([]byte("policy-ok\n")),
			}}})
			writeConnectFrame(t, w, map[string]any{"event": map[string]any{"end": map[string]any{
				"exited": true, "status": "exit status 0",
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	configPath := filepath.Join(t.TempDir(), "mcp.json")
	config := fmt.Sprintf(`{
  "mcpServers": {
    "envdtest": {
      "command": %q,
      "args": ["--addr", %q, "--user", "n9e", "--workspace", "/tmp/work", "--profile", "ops", "--allow-command", "echo", "--tool-prefix", "remote_envd"],
      "env": {},
      "disabled": false,
      "enabled": true,
      "type": "stdio",
      "timeout": "30s",
      "maxRetry": 1
    }
  },
  "global": {"healthCheckInterval": "1m", "connectTimeout": "15s"}
}`, bridge, srv.URL)
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	mgr := manager.NewManager()
	if err := mgr.LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })

	tools := mgr.ListTools()
	if len(tools) == 0 {
		t.Fatal("manager exposed no tools")
	}
	var metricsTool, shellTool string
	for _, tool := range tools {
		name := tool.Tool.Name
		if !strings.HasPrefix(name, "remote_envd__") {
			t.Errorf("tool %q lacks the configured prefix", name)
		}
		if strings.HasSuffix(name, "__host_metrics") {
			metricsTool = name
		}
		if strings.HasSuffix(name, "__shell") {
			shellTool = name
		}
	}
	if metricsTool == "" {
		t.Fatal("host_metrics was not exposed")
	}
	if shellTool == "" {
		t.Fatal("shell was not exposed under the ops profile")
	}

	res, err := mgr.CallTool(context.Background(), "envdtest", metricsTool, map[string]any{})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatal("CallTool returned no content")
	}
	if text := res.Content[0].Text; !strings.Contains(text, "cpus: 4") {
		t.Fatalf("output = %q, want the stub CPU count", text)
	}

	res, err = mgr.CallTool(context.Background(), "envdtest", shellTool, map[string]any{"command": "echo policy-ok"})
	if err != nil {
		t.Fatalf("CallTool shell: %v", err)
	}
	if res == nil || len(res.Content) == 0 {
		t.Fatal("shell returned no content")
	}
	if text := res.Content[0].Text; !strings.Contains(text, "policy-ok") || !strings.Contains(text, "[exit 0]") {
		t.Fatalf("shell output = %q, want the stub stdout and exit status", text)
	}
}
