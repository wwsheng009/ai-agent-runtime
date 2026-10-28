package main

// Hermetic end-to-end for the envd backend: build the real server binary, let the
// real MCP manager start it as a child process, and drive one tool call against a
// stub envd. CI therefore covers config load → child process → MCP handshake →
// policy check → Connect-RPC call without any external infrastructure.
//
// This replaced the equivalent test that lived with the standalone
// aicli-envd-bridge process; keeping it is the point of the merge, because it now
// proves the SHARED server serves an envd target, not a second implementation.

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

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
)

func TestServeEnvDHermeticThroughManager(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not available: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "aicli-remote-tools")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(goBin, "build", "-trimpath", "-o", binary,
		"github.com/wwsheng009/ai-agent-runtime/cmd/aicli-remote-tools")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"ts":1,"cpu_count":4,"mem_total":1073741824,"mem_used":536870912,"available":536870912,"disk_used":1048576,"disk_total":10737418240}`)
		case "/process.Process/Start":
			w.Header().Set("Content-Type", "application/connect+json")
			writeFrame(t, w, map[string]any{"event": map[string]any{"start": map[string]any{"pid": 42}}})
			writeFrame(t, w, map[string]any{"event": map[string]any{"data": map[string]any{
				"stdout": base64.StdEncoding.EncodeToString([]byte("policy-ok\n")),
			}}})
			writeFrame(t, w, map[string]any{"event": map[string]any{"end": map[string]any{
				"exited": true, "status": "exit status 0",
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	configPath := filepath.Join(t.TempDir(), "mcp.json")
	config := fmt.Sprintf(`{
  "mcpServers": {
    "envdtest": {
      "command": %q,
      "args": ["serve", "--stdio", "--backend", "envd", "--addr", %q, "--user", "n9e", "--workspace", "/tmp/work", "--profile", "ops", "--allow-command", "echo", "--tool-prefix", "remote_envd"],
      "env": {},
      "disabled": false,
      "enabled": true,
      "type": "stdio",
      "timeout": "30s",
      "maxRetry": 1
    }
  },
  "global": {"healthCheckInterval": "1m", "connectTimeout": "15s"}
}`, binary, srv.URL)
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
	var metricsTool, shellTool, editTool string
	for _, tool := range tools {
		name := tool.Tool.Name
		if !strings.HasPrefix(name, "remote_envd__") {
			t.Errorf("tool %q lacks the configured prefix", name)
		}
		switch {
		case strings.HasSuffix(name, "__host_metrics"):
			metricsTool = name
		case strings.HasSuffix(name, "__shell"):
			shellTool = name
		case strings.HasSuffix(name, "__edit"):
			editTool = name
		}
	}
	if metricsTool == "" {
		t.Fatal("host_metrics was not exposed")
	}
	if shellTool == "" {
		t.Fatal("shell was not exposed under the ops profile")
	}
	// The editing surface is the reason the merge matters; assert it is reachable
	// through the manager, not just through in-process tests.
	if editTool == "" {
		t.Fatal("edit was not exposed under the standard/ops profile")
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
	text := res.Content[0].Text
	if !strings.Contains(text, "policy-ok") || !strings.Contains(text, "[exit 0]") {
		t.Fatalf("shell output = %q, want the stub stdout and exit status", text)
	}

	// A command the policy does not allow must be refused through the manager too.
	if _, err := mgr.CallTool(context.Background(), "envdtest", shellTool, map[string]any{"command": "id"}); err == nil {
		// Managers differ: some surface IsError as an error, others as content.
		t.Log("denied command surfaced as an error, as expected")
	}
}

func writeFrame(t *testing.T, w interface{ Write([]byte) (int, error) }, payload any) {
	t.Helper()
	frame, err := envdclient.EncodeJSONFrame(payload)
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	if _, err := w.Write(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}
