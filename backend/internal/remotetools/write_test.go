package remotetools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// newOpsConfig builds an ops profile config with a narrow command allowlist.
func newOpsConfig(t *testing.T, root string, allowed []string) *remotetools.Config {
	t.Helper()
	cfg := remotetools.DefaultConfig()
	cfg.Workspace = root
	cfg.ToolPrefix = "remote_dev"
	cfg.TargetName = "dev"
	cfg.Profile = remotetools.ProfileOps
	cfg.Shell.AllowedCommands = allowed
	cfg.Shell.DenyShellMetachars = true
	cfg.Limits.CommandTimeout = remotetools.Duration{Duration: 10 * time.Second}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return cfg
}

// startTestSession serves the given server over an in-memory transport.
func startTestSession(t *testing.T, server *remotetools.Server) (*mcp.ClientSession, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	go func() { _ = server.ServeStdioOn(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, ctx
}

// TestWriteEditAppendRoundTrip covers the P2 mutation surface end to end.
func TestWriteEditAppendRoundTrip(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"cat"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	callTool(t, ctx, session, "remote_dev__write", map[string]interface{}{
		"file_path": "new.txt",
		"content":   "hello\nworld\n",
	})
	assertFile(t, filepath.Join(root, "new.txt"), "hello\nworld\n")

	callTool(t, ctx, session, "remote_dev__append_write", map[string]interface{}{
		"file_path": "new.txt",
		"content":   "tail\n",
	})
	assertFile(t, filepath.Join(root, "new.txt"), "hello\nworld\ntail\n")

	callTool(t, ctx, session, "remote_dev__edit", map[string]interface{}{
		"file_path":  "new.txt",
		"old_string": "world",
		"new_string": "there",
	})
	assertFile(t, filepath.Join(root, "new.txt"), "hello\nthere\ntail\n")
}

// TestEditRejectsAmbiguousMatch locks the exact-match contract: a non-unique
// old_string must not silently replace the first occurrence.
func TestEditRejectsAmbiguousMatch(t *testing.T) {
	root := newTestWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "dup.txt"), []byte("x\nx\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"cat"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remote_dev__edit",
		Arguments: map[string]interface{}{
			"file_path":  "dup.txt",
			"old_string": "x",
			"new_string": "y",
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected ambiguous edit to fail")
	}
	if !strings.Contains(textContent(result), "occurs 2 times") {
		t.Fatalf("unexpected error: %s", textContent(result))
	}
	assertFile(t, filepath.Join(root, "dup.txt"), "x\nx\n")
}

// TestApplyPatchAddUpdateDelete covers the three patch directives.
func TestApplyPatchAddUpdateDelete(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"cat"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: added.txt",
		"+first",
		"+second",
		"*** Update File: README.md",
		"@@",
		" # demo",
		"-TODO: fix me",
		"+DONE",
		"*** Delete File: src/main.go",
		"*** End Patch",
	}, "\n")

	callTool(t, ctx, session, "remote_dev__apply_patch", map[string]interface{}{"patch": patch})

	assertFile(t, filepath.Join(root, "added.txt"), "first\nsecond\n")
	assertFile(t, filepath.Join(root, "README.md"), "# demo\nDONE\n")
	if _, err := os.Stat(filepath.Join(root, "src", "main.go")); !os.IsNotExist(err) {
		t.Fatalf("expected main.go to be deleted, got %v", err)
	}
}

// TestApplyPatchRejectsMismatchedHunk locks the exact-match rule.
func TestApplyPatchRejectsMismatchedHunk(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"cat"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: README.md",
		"@@",
		"-this text does not exist",
		"+replacement",
		"*** End Patch",
	}, "\n")

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__apply_patch",
		Arguments: map[string]interface{}{"patch": patch},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected mismatched hunk to fail")
	}
	body := textContent(result)
	if !strings.Contains(body, "NOT_FOUND") {
		t.Fatalf("expected NOT_FOUND, got %q", body)
	}
	// The file must be untouched.
	assertFile(t, filepath.Join(root, "README.md"), "# demo\nTODO: fix me\n")
}

// TestShellAllowedCommandRuns proves an allowlisted command executes.
func TestShellAllowedCommandRuns(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "echo hello-from-remote",
	})
	if !strings.Contains(body, "hello-from-remote") {
		t.Fatalf("unexpected output: %s", body)
	}
}

// TestShellEnvAllowlistForwardsConfiguredVariables locks the whitelist contract:
// FilterEnv must receive the server environment, otherwise PATH/HOME/LANG are
// silently absent even though the operator listed them.
func TestShellEnvAllowlistForwardsConfiguredVariables(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Shell.EnvAllowlist = []string{"PATH"}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	command := "echo $PATH"
	if runtime.GOOS == "windows" {
		command = "echo %PATH%"
	}
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": command,
	})
	got := strings.TrimSpace(body)
	if got == "" || got == "$PATH" || got == "%PATH%" {
		t.Fatalf("env allowlist did not forward PATH: %q", body)
	}
}

// TestShellFailsClosedWithoutAllowlist locks the remote default: unlike the local
// sandbox, an empty allowlist denies every command instead of allowing all.
func TestShellFailsClosedWithoutAllowlist(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, nil), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": "echo hi"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected empty allowlist to deny the command")
	}
	if !strings.Contains(textContent(result), "COMMAND_DENIED") {
		t.Fatalf("unexpected error: %s", textContent(result))
	}
}

// TestShellRejectsMetachars proves one allowed binary cannot be chained.
func TestShellRejectsMetachars(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"echo"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	for _, command := range []string{
		"echo hi; rm -rf /",
		"echo hi && whoami",
		"echo $(whoami)",
		"echo hi > /tmp/x",
		"echo hi | sh",
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "remote_dev__shell",
			Arguments: map[string]interface{}{"command": command},
		})
		if err != nil {
			t.Fatalf("call %q: %v", command, err)
		}
		if !result.IsError {
			t.Fatalf("expected %q to be refused", command)
		}
		if !strings.Contains(textContent(result), "COMMAND_DENIED") {
			t.Fatalf("command %q: unexpected error %s", command, textContent(result))
		}
	}
}

// TestShellRejectsDestructiveCommand proves the shared shellrisk classifier runs.
func TestShellRejectsDestructiveCommand(t *testing.T) {
	root := newTestWorkspace(t)
	// Allow rm so the refusal can only come from the classifier, not the allowlist.
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"rm"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": "rm -rf /"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected destructive command to be refused")
	}
	body := textContent(result)
	// Metachar check or the destructive classifier may catch it; either way the
	// command must not run.
	if !strings.Contains(body, "COMMAND_DENIED") {
		t.Fatalf("unexpected error: %s", body)
	}
}

// TestProfileStandardHidesShell locks profile separation.
func TestProfileStandardHidesShell(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Profile = remotetools.ProfileStandard
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	for _, name := range server.ToolNames() {
		if strings.Contains(name, "shell") {
			t.Fatalf("standard profile must not expose shell, got %q", name)
		}
	}
}

// TestReadOnlyOverridesOpsProfile locks the hard read-only override.
func TestReadOnlyOverridesOpsProfile(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.ReadOnly = true
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	names := server.ToolNames()
	if len(names) != 4 {
		t.Fatalf("expected only the 4 read tools, got %v", names)
	}
	for _, name := range names {
		if !strings.Contains(name, "view") && !strings.Contains(name, "ls") &&
			!strings.Contains(name, "glob") && !strings.Contains(name, "grep") {
			t.Fatalf("unexpected tool under readOnly: %q", name)
		}
	}
}

// TestWriteDeniedOutsideWorkspace locks the jail on the mutation path.
func TestWriteDeniedOutsideWorkspace(t *testing.T) {
	root := newTestWorkspace(t)
	outside := t.TempDir()
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"cat"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remote_dev__write",
		Arguments: map[string]interface{}{
			"file_path": filepath.Join(outside, "escape.txt"),
			"content":   "nope",
		},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected write outside the workspace to be denied")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatal("file must not be created outside the workspace")
	}
}

// TestAuditRecordsEveryCall proves the audit wrapper is wired and never leaks
// file contents.
func TestAuditRecordsEveryCall(t *testing.T) {
	root := newTestWorkspace(t)
	auditPath := filepath.Join(t.TempDir(), "audit.ndjson")
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Audit.Path = auditPath
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	session, ctx := startTestSession(t, server)

	callTool(t, ctx, session, "remote_dev__view", map[string]interface{}{"file_path": "README.md"})
	_ = server.Close()

	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 audit record, got %d: %s", len(lines), raw)
	}
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode audit record: %v", err)
	}
	if record["tool"] != "remote_dev__view" {
		t.Fatalf("unexpected tool: %v", record["tool"])
	}
	if record["decision"] != "allow" {
		t.Fatalf("unexpected decision: %v", record["decision"])
	}
	if record["target"] != "dev" {
		t.Fatalf("unexpected target: %v", record["target"])
	}
	// File contents must never appear in the audit log.
	if strings.Contains(string(raw), "TODO: fix me") {
		t.Fatal("audit log leaked file contents")
	}
}

// assertFile asserts exact file contents.
func assertFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(raw) != want {
		t.Fatalf("%s = %q, want %q", path, raw, want)
	}
}
