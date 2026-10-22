package remotetools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// newTestWorkspace builds a workspace with a few files to search.
func newTestWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"),
		[]byte("# demo\nTODO: fix me\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	return root
}

func newTestConfig(t *testing.T, root string) *remotetools.Config {
	t.Helper()
	cfg := remotetools.DefaultConfig()
	cfg.Workspace = root
	cfg.ToolPrefix = "remote_dev"
	cfg.TargetName = "dev"
	cfg.Profile = remotetools.ProfileReadonly
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return cfg
}

// TestWorkspaceResolveRejectsEscape locks the jail: relative traversal, absolute
// paths outside the roots, and symlink escapes must all be refused.
func TestWorkspaceResolveRejectsEscape(t *testing.T) {
	root := newTestWorkspace(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	workspace, err := remotetools.NewWorkspace(root, nil)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	if _, err := workspace.Resolve("../../etc/passwd"); err == nil {
		t.Fatal("expected relative traversal to be denied")
	}
	if _, err := workspace.Resolve(filepath.Join(outside, "secret.txt")); err == nil {
		t.Fatal("expected absolute outside path to be denied")
	}

	// Symlink escape: the lexical path is inside, the resolved one is not.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := workspace.Resolve("escape/secret.txt"); err == nil {
		t.Fatal("expected symlink escape to be denied")
	}

	// A legitimate nested path still resolves.
	resolved, err := workspace.Resolve("src/main.go")
	if err != nil {
		t.Fatalf("expected nested path to resolve: %v", err)
	}
	if !strings.HasSuffix(resolved, filepath.Join("src", "main.go")) {
		t.Fatalf("unexpected resolution: %s", resolved)
	}
}

// TestToolSurfaceIsPrefixed locks G4: every exposed name carries the prefix so a
// remote tool can never shadow a local builtin.
func TestToolSurfaceIsPrefixed(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	names := server.ToolNames()
	if len(names) != 4 {
		t.Fatalf("expected 4 tools, got %v", names)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "remote_dev__") {
			t.Fatalf("tool %q missing prefix", name)
		}
	}
}

// TestStdioTransportEndToEnd runs the real MCP server over an in-memory pipe and
// exercises view/ls/glob/grep, proving the stdio path emits valid JSON-RPC.
func TestStdioTransportEndToEnd(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ServeStdioOn(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("expected 4 tools, got %d", len(tools.Tools))
	}

	// view
	viewResult := callTool(t, ctx, session, "remote_dev__view", map[string]interface{}{
		"file_path": "src/main.go",
	})
	if !strings.Contains(viewResult, "func main()") {
		t.Fatalf("view output missing content: %s", viewResult)
	}
	if strings.Contains(viewResult, "Metadata:") {
		t.Fatal("metadata must not be appended to the model-visible body (review B5)")
	}

	// ls
	lsResult := callTool(t, ctx, session, "remote_dev__ls", map[string]interface{}{"path": "src"})
	if !strings.Contains(lsResult, "main.go") {
		t.Fatalf("ls output missing file: %s", lsResult)
	}

	// glob
	globResult := callTool(t, ctx, session, "remote_dev__glob", map[string]interface{}{
		"pattern": "**/*.go",
	})
	if !strings.Contains(globResult, "src/main.go") {
		t.Fatalf("glob output missing match: %s", globResult)
	}

	// grep
	grepResult := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "TODO",
	})
	if !strings.Contains(grepResult, "README.md:2") {
		t.Fatalf("grep output missing match: %s", grepResult)
	}

	select {
	case err := <-serverErr:
		if err != nil && err != context.Canceled {
			t.Fatalf("server exited: %v", err)
		}
	case <-time.After(2 * time.Second):
		// The session stays open until Close; a still-running server is correct.
	}
}

// TestToolErrorIsClassified locks the stable error-code contract.
func TestToolErrorIsClassified(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.ServeStdioOn(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__view",
		Arguments: map[string]interface{}{"file_path": "../../etc/passwd"},
	})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected escape to be a tool error")
	}
	text := textContent(result)
	if !strings.Contains(text, "PATH_DENIED") {
		t.Fatalf("expected PATH_DENIED, got %q", text)
	}
}

// TestProfileCustomNarrowsSurface locks the allowlist filter.
func TestProfileCustomNarrowsSurface(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newTestConfig(t, root)
	cfg.Profile = remotetools.ProfileCustom
	cfg.Tools = []string{"view", "grep"}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	names := server.ToolNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 tools, got %v", names)
	}
	for _, name := range names {
		if name != "remote_dev__view" && name != "remote_dev__grep" {
			t.Fatalf("unexpected tool %q", name)
		}
	}
}

// TestHTTPAuthFailsClosed locks the token requirement for non-loopback binds.
func TestHTTPAuthFailsClosed(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newTestConfig(t, root)
	cfg.HTTP.Listen = "0.0.0.0:0"
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = server.RunHTTP(ctx)
	if err == nil {
		t.Fatal("expected non-loopback bind without token to fail")
	}
	if !strings.Contains(err.Error(), "requires") && !strings.Contains(err.Error(), "token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestHTTPEndToEnd runs the Streamable HTTP transport on loopback.
func TestHTTPEndToEnd(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newTestConfig(t, root)
	cfg.HTTP.Listen = "127.0.0.1:0"
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan string, 1)
	go func() {
		_ = server.RunHTTPWithListener(ctx, func(addr string) { addrCh <- addr })
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("http server did not report a listener")
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: "http://" + addr + "/mcp",
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	result := callTool(t, ctx, session, "remote_dev__ls", map[string]interface{}{"path": "."})
	if !strings.Contains(result, "README.md") {
		t.Fatalf("unexpected ls output: %s", result)
	}
}

// callTool invokes one tool and returns its concatenated text.
func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]interface{}) string {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if result.IsError {
		t.Fatalf("tool %s failed: %s", name, textContent(result))
	}
	return textContent(result)
}

func textContent(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var builder strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

// TestMetadataTravelsInMetaNotBody locks the B5 contract at the wire level.
func TestMetadataTravelsInMetaNotBody(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.ServeStdioOn(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__view",
		Arguments: map[string]interface{}{"file_path": "README.md"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	body := textContent(result)
	if strings.Contains(body, "Metadata:") {
		t.Fatalf("metadata leaked into body: %s", body)
	}
	if result.Meta == nil {
		t.Fatal("expected metadata in _meta")
	}
	encoded, _ := json.Marshal(result.Meta)
	if !strings.Contains(string(encoded), "output_kind") {
		t.Fatalf("expected output_kind in meta, got %s", encoded)
	}
}