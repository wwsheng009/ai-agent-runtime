package remotetools_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRootsNarrowTheLiveSession proves the transport wiring: a client that declares
// a subdirectory as its root cannot read a file outside that declaration, while the
// same server still serves the full workspace to a client that declares nothing.
func TestRootsNarrowTheLiveSession(t *testing.T) {
	root := newTestWorkspace(t)
	// A file outside the declared root but inside the server workspace.
	if err := os.WriteFile(filepath.Join(root, "top-secret.txt"), []byte("outside-declaration"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Client A declares only src/ as its root.
	narrowedSession := connectWithRoots(t, ctx, server, root, "src")
	body := callTool(t, ctx, narrowedSession, "remote_dev__view", map[string]interface{}{
		"file_path": "main.go", // resolves inside the declared src/ root
	})
	if !strings.Contains(body, "func main()") {
		t.Fatalf("narrowed read inside the declared root failed: %s", body)
	}

	// Escaping the declaration must be refused even though the server workspace allows
	// it. The path is absolute so it names the file directly instead of being
	// re-anchored under the narrowed root (a relative path would resolve *inside*
	// src/, which is the documented behavior of a narrowed workspace).
	outsideDeclaration := filepath.Join(root, "top-secret.txt")
	result, err := narrowedSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__view",
		Arguments: map[string]interface{}{"file_path": outsideDeclaration},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected a read outside the declared root to be refused, got: %s", textContent(result))
	}
	if !strings.Contains(textContent(result), "PATH_DENIED") {
		t.Fatalf("unexpected error: %s", textContent(result))
	}

	// A client that declares no roots keeps the full workspace.
	fullSession := connectWithRoots(t, ctx, server, root, "")
	fullBody := callTool(t, ctx, fullSession, "remote_dev__view", map[string]interface{}{
		"file_path": "top-secret.txt",
	})
	if !strings.Contains(fullBody, "outside-declaration") {
		t.Fatalf("undeclared-root client should see the full workspace: %s", fullBody)
	}
}

// TestRootsOutsideWorkspaceAreIgnored proves a malicious declaration cannot widen
// access: an outside root is dropped, and the session keeps the (empty) intersection.
func TestRootsOutsideWorkspaceAreIgnored(t *testing.T) {
	root := newTestWorkspace(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("do-not-read"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	session := connectWithRoots(t, ctx, server, root, outside)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__view",
		Arguments: map[string]interface{}{"file_path": "secret.txt"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected an outside declaration to grant nothing, got: %s", textContent(result))
	}
}

// TestShellDefaultWorkdirFollowsNarrowedRoots locks the default cwd to the
// session's effective workspace: a client that narrows to src/ must not have a
// bare cwd command report the server's base root.
func TestShellDefaultWorkdirFollowsNarrowedRoots(t *testing.T) {
	root := newTestWorkspace(t)

	// `pwd` is a POSIX shell builtin; cmd.exe prints the current directory when
	// `cd` runs with no arguments.
	command, allowed := "pwd", []string{"pwd"}
	if runtime.GOOS == "windows" {
		command, allowed = "cd", []string{"cd"}
	}

	server, err := remotetools.NewServer(newOpsConfig(t, root, allowed), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	session := connectWithRoots(t, ctx, server, root, "src")
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": command,
	})

	// The tool returns the command output verbatim; compare the last line so a
	// future header cannot turn this into a substring test that always passes.
	lines := strings.Split(strings.TrimSpace(body), "\n")
	got := strings.TrimSpace(lines[len(lines)-1])

	want := filepath.Join(root, "src")
	// macOS reports the resolved /private/var form for a TempDir under /var.
	if resolved, resolveErr := filepath.EvalSymlinks(want); resolveErr == nil {
		want = resolved
	}
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(got, want) {
			t.Fatalf("default workdir = %q, want %q", got, want)
		}
		return
	}
	if got != want {
		t.Fatalf("default workdir = %q, want %q", got, want)
	}
}

// TestJobStartDefaultWorkdirFollowsNarrowedRoots is the job_start twin of the
// shell test: a roots-narrowed session must not launch in the base workspace.
func TestJobStartDefaultWorkdirFollowsNarrowedRoots(t *testing.T) {
	root := newTestWorkspace(t)
	command, allowed := "pwd", []string{"pwd"}
	if runtime.GOOS == "windows" {
		command, allowed = "cd", []string{"cd"}
	}

	server, err := remotetools.NewServer(newJobsConfig(t, root, allowed), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	session := connectWithRoots(t, ctx, server, root, "src")
	body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": command,
	})
	jobID := extractJobID(t, body)
	if jobID == "" {
		t.Fatalf("no job id in response: %s", body)
	}

	deadline := time.Now().Add(10 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
			"job_id": jobID,
		})
		if strings.Contains(output, "exited") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(output, "exited") {
		t.Fatalf("job did not reach a terminal state: %s", output)
	}

	want := filepath.Join(root, "src")
	if resolved, resolveErr := filepath.EvalSymlinks(want); resolveErr == nil {
		want = resolved
	}
	found := strings.Contains(output, want)
	if runtime.GOOS == "windows" {
		found = strings.Contains(strings.ToLower(output), strings.ToLower(want))
	}
	if !found {
		t.Fatalf("job default workdir = %s, want %q", output, want)
	}
}

// connectWithRoots connects a client that declares one root (or none).
func connectWithRoots(t *testing.T, ctx context.Context, server *remotetools.Server, workspace, declared string) *mcp.ClientSession {
	t.Helper()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.ServeStdioOn(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "roots-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// AddRoots must run after Connect: the client binds its roots store to the
	// session during the handshake, and a pre-connect call is dropped.
	if strings.TrimSpace(declared) != "" {
		path := declared
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		client.AddRoots(&mcp.Root{
			URI:  "file://" + filepath.ToSlash(path),
			Name: filepath.Base(path),
		})
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
