package toolctl

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// servePipe runs `toolctl serve --mcp stdio` against an in-memory MCP client.
// The real process transport is covered separately by the HTTP smoke test; this
// keeps the tool-list/tool-call contract fast and deterministic.
func servePipe(t *testing.T, workspace string) (*mcp.ClientSession, context.Context) {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	// Build the same surface serve would expose, then run the SDK server on the
	// pipe transport. This exercises the real registry/schema path without
	// spawning a process or binding stdin/stdout.
	cli := &CLI{Stdout: io.Discard, Stderr: io.Discard}
	surface, err := cli.buildSurface(&commonOptions{
		workspace:  workspace,
		profile:    "ops",
		allowWrite: true,
		allowCmd:   true,
	})
	if err != nil {
		t.Fatalf("build surface: %v", err)
	}
	server := newSurfaceMCPServer(surface)
	go func() { _ = server.GetMCPServer().Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "toolctl-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

func TestServeStdioListsToolSurface(t *testing.T) {
	workspace := t.TempDir()
	session, ctx := servePipe(t, workspace)
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"view", "grep", "write", "shell"} {
		if !names[want] {
			t.Errorf("serve surface missing %q: %v", want, names)
		}
	}
}

func TestServeStdioCallsTool(t *testing.T) {
	workspace := t.TempDir()
	if err := writeFile(workspace, "hello.txt", "hello-from-serve\n"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	session, ctx := servePipe(t, workspace)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "view",
		Arguments: map[string]interface{}{"file_path": "hello.txt"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.IsError {
		t.Fatalf("view failed: %s", toolText(result))
	}
	if !strings.Contains(toolText(result), "hello-from-serve") {
		t.Fatalf("unexpected output: %s", toolText(result))
	}
}

func TestServeStdioPreservesJSONRPCErrors(t *testing.T) {
	workspace := t.TempDir()
	session, ctx := servePipe(t, workspace)
	// The SDK reports an unknown tool as a call error, not as a successful
	// empty result. Fail closed is the contract.
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "definitely_missing_tool",
		Arguments: map[string]interface{}{},
	})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatal("unknown tool must fail")
	}
}

// toolText concatenates text content from an MCP result.
func toolText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var buffer bytes.Buffer
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			buffer.WriteString(text.Text)
		}
	}
	return buffer.String()
}

// writeFile is a tiny helper kept local so the test does not depend on another
// package's fixture.
func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
}
