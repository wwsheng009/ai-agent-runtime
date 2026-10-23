package remotetools_test

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteViewSchemaSharesLocalContract pins the single-schema rule: the
// remote surface publishes the same view parameters as the local surface for
// every handler-level feature (files/compact/offset/limit), and omits only the
// parameter its runtime cannot honor (symbol needs a code index).
func TestRemoteViewSchemaSharesLocalContract(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var view *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "remote_dev__view" {
			view = tool
			break
		}
	}
	if view == nil {
		t.Fatalf("remote view tool missing: %v", tools.Tools)
	}
	schema, ok := view.InputSchema.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected schema type %T", view.InputSchema)
	}
	properties, _ := schema["properties"].(map[string]interface{})
	for _, want := range []string{"file_path", "files", "compact", "offset", "limit"} {
		if _, exists := properties[want]; !exists {
			t.Errorf("remote view schema missing %q: %#v", want, properties)
		}
	}
	if _, exists := properties["symbol"]; exists {
		t.Errorf("remote runtime has no code index; symbol must not be advertised: %#v", properties)
	}
}

// TestRemoteViewBatchFilesReadsEveryEntry proves the shared schema is honest:
// a files[] call accepted by the remote surface actually reads every entry.
func TestRemoteViewBatchFilesReadsEveryEntry(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	output := callTool(t, ctx, session, "remote_dev__view", map[string]interface{}{
		"files": []interface{}{
			map[string]interface{}{"file_path": "README.md"},
			map[string]interface{}{"file_path": "src/main.go", "limit": 1},
		},
		"compact": true,
	})
	for _, want := range []string{"README.md", "1: # demo", "src/main.go", "1: package main"} {
		if !strings.Contains(output, want) {
			t.Fatalf("remote batch view missing %q:\n%s", want, output)
		}
	}
}
