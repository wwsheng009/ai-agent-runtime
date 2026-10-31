//go:build !win7compat

package tools

// mcp_stack_e2e_test.go closes the last automated gap in the envd/remote chain: a REAL
// MCP stdio child process - this test binary re-exec'd as a minimal MCP server, so no
// external binary or live target is needed - driven through the whole stack:
//
//	config -> mcp manager -> tools.Manager -> CallTool
//
// in the same shape the real deployment uses: the child publishes a prefixed tool name
// (remote_dev__echo, as aicli-remote-tools does with --tool-prefix), the manager registers
// it, and tools.Manager must both list it and call it while carrying MCP identity metadata.
// The transport-level handshake already has its own e2e (internal/mcp/transport); this file
// pins the two layers above it, which until now were only covered with stubbed managers.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	mcpregistry "github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
)

const (
	mcpStackHelperEnv  = "AICLI_MCP_STACK_HELPER"
	mcpStackToolName   = "remote_dev__echo"
	mcpStackServerName = "devbox"
)

// TestMCPStackStdioHelper is not an independent test: it is the MCP stdio server the
// stacking test spawns (a re-exec of this test binary). Without the helper marker it
// skips, so a normal `go test` run only sees a skip.
func TestMCPStackStdioHelper(t *testing.T) {
	if os.Getenv(mcpStackHelperEnv) != "1" {
		t.Skip("helper target: only runs when re-exec'd by TestToolsManagerRunsOverRealMCPStdioChild")
	}
	serveMCPStdio(os.Stdin, os.Stdout)
	os.Exit(0) // do not let the testing framework print PASS into the MCP stream
}

// serveMCPStdio answers the small slice of MCP this stack needs: initialize, tools/list
// and tools/call. Unknown requests get a JSON-RPC method-not-found; unknown notifications
// are ignored. Messages are newline-delimited JSON, the framing the stdio transport uses.
func serveMCPStdio(in *os.File, out *os.File) {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	writer := bufio.NewWriter(out)
	defer writer.Flush()

	reply := func(payload map[string]interface{}) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		_, _ = writer.Write(append(data, '\n'))
		_ = writer.Flush()
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     json.RawMessage        `json:"id"`
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		hasID := len(req.ID) > 0 && string(req.ID) != "null"
		switch req.Method {
		case "initialize":
			version := "2025-06-18"
			if v, ok := req.Params["protocolVersion"].(string); ok && v != "" {
				version = v
			}
			reply(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]interface{}{
					"protocolVersion": version,
					"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
					"serverInfo":      map[string]interface{}{"name": "mcp-stack-helper", "version": "1.0.0"},
				},
			})
		case "tools/list":
			reply(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]interface{}{"tools": []map[string]interface{}{{
					"name":        mcpStackToolName,
					"description": "stacking e2e echo",
					"inputSchema": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}},
					},
				}}},
			})
		case "tools/call":
			text := "echo:"
			if args, ok := req.Params["arguments"].(map[string]interface{}); ok {
				if v, ok := args["text"].(string); ok {
					text += v
				}
			}
			reply(map[string]interface{}{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]interface{}{
					"content": []map[string]interface{}{{"type": "text", "text": text}},
					"isError": false,
				},
			})
		default:
			if hasID {
				reply(map[string]interface{}{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]interface{}{"code": -32601, "message": "not found: " + req.Method},
				})
			}
		}
	}
}

// TestToolsManagerRunsOverRealMCPStdioChild pins the stacking itself: the in-memory config
// (the same entry point the chat wiring uses) spawns a real MCP child, the manager connects
// it, and tools.Manager must list the prefixed tool and route one call to it with the MCP
// identity metadata callers rely on (policy reads mcp_raw_tool_name).
func TestToolsManagerRunsOverRealMCPStdioChild(t *testing.T) {
	cfg := &mcpconfig.Config{
		MCPServers: map[string]mcpconfig.MCPConfig{
			mcpStackServerName: {
				Name:    mcpStackServerName,
				Type:    "stdio",
				Command: os.Args[0],
				Args:    []string{"-test.run=^TestMCPStackStdioHelper$"},
				Env:     map[string]string{mcpStackHelperEnv: "1"},
				Enabled: true,
				Timeout: mcpconfig.Duration{Duration: 20 * time.Second},
			},
		},
		Global: mcpconfig.GlobalConfig{ConnectTimeout: mcpconfig.Duration{Duration: 15 * time.Second}},
	}

	mgr := mcpmanager.NewManager()
	scoped, ok := mgr.(mcpmanager.ScopedManager)
	if !ok {
		t.Fatal("the manager must expose the in-memory config entry point")
	}
	if err := scoped.LoadConfigFromConfig(cfg); err != nil {
		t.Fatalf("LoadConfigFromConfig: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("manager Start: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })

	tm := NewDefaultManager(mgr)
	if !tm.MCPAvailable() {
		t.Fatal("tools.Manager must see the MCP surface")
	}

	found := false
	for _, desc := range tm.ListTools() {
		if desc.Name != mcpStackToolName {
			continue
		}
		found = true
		if got := desc.Metadata["mcp_name"]; got != mcpStackServerName {
			t.Fatalf("descriptor metadata mcp_name = %#v, want %q", got, mcpStackServerName)
		}
		if got := desc.Metadata["mcp_raw_tool_name"]; got != mcpStackToolName {
			t.Fatalf("descriptor metadata mcp_raw_tool_name = %#v, want %q", got, mcpStackToolName)
		}
	}
	if !found {
		t.Fatalf("tools.Manager did not list the child's %q tool", mcpStackToolName)
	}

	out, meta, err := tm.ExecuteWithMeta(ctx, mcpStackToolName, map[string]interface{}{"text": "stacking"})
	if err != nil {
		t.Fatalf("ExecuteWithMeta: %v", err)
	}
	if !strings.Contains(out, "echo:stacking") {
		t.Fatalf("output = %q, want the child's echo", out)
	}
	if got := meta["mcp_name"]; got != mcpStackServerName {
		t.Fatalf("meta mcp_name = %#v, want %q", got, mcpStackServerName)
	}
	if got := meta["mcp_raw_tool_name"]; got != mcpStackToolName {
		t.Fatalf("meta mcp_raw_tool_name = %#v, want %q", got, mcpStackToolName)
	}
	if got := meta["tool_callable_name"]; got != mcpStackToolName {
		t.Fatalf("meta tool_callable_name = %#v, want %q", got, mcpStackToolName)
	}
	wantCanonical := mcpregistry.CanonicalToolName(mcpStackServerName, mcpStackToolName)
	if got := meta["mcp_canonical_name"]; got != wantCanonical {
		t.Fatalf("meta mcp_canonical_name = %#v, want %q", got, wantCanonical)
	}
}
