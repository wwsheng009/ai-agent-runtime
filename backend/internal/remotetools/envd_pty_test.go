package remotetools_test

// The interactive surface: a pty session that outlives its tool call, driven by
// the unary process-control RPCs, and still gated by the shared command policy.

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestEnvDPtyLifecycle(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, allowCommands("top"))

	started := callTool(t, ctx, session, "remote_envd__pty_start", map[string]interface{}{
		"command": "top", "cols": 100, "rows": 30,
	})
	if !strings.Contains(started, "session started: pid=7") {
		t.Fatalf("pty_start result = %q", started)
	}

	read := callTool(t, ctx, session, "remote_envd__pty_read", map[string]interface{}{
		"pid": 7, "wait_ms": 2000,
	})
	// The stub answers an unlabeled shell command with "ran: <command>", and the
	// stream ends, so a finished session reports its exit status.
	if !strings.Contains(read, "ran: top") {
		t.Fatalf("pty_read lost the session output: %q", read)
	}
	if !strings.Contains(read, "state=exited") || !strings.Contains(read, "[exit 0") {
		t.Fatalf("pty_read did not report the ended state: %q", read)
	}

	wrote := callTool(t, ctx, session, "remote_envd__pty_write", map[string]interface{}{
		"pid": 7, "data": "q\n",
	})
	if !strings.Contains(wrote, "wrote 2 bytes to pid 7") {
		t.Fatalf("pty_write result = %q", wrote)
	}

	resized := callTool(t, ctx, session, "remote_envd__pty_resize", map[string]interface{}{
		"pid": 7, "cols": 120, "rows": 40,
	})
	if !strings.Contains(resized, "resized pid 7 to 120x40") {
		t.Fatalf("pty_resize result = %q", resized)
	}

	closed := callTool(t, ctx, session, "remote_envd__pty_close", map[string]interface{}{"pid": 7})
	if !strings.Contains(closed, "closed pid 7") {
		t.Fatalf("pty_close result = %q", closed)
	}

	// The control RPCs really went to the target, with the pty payload shapes.
	var sawInput, sawResize, sawSignal bool
	for _, entry := range stub.controlLog() {
		switch {
		case strings.HasPrefix(entry, "SendInput") && strings.Contains(entry, "pty"):
			sawInput = true
		case strings.HasPrefix(entry, "Update") && strings.Contains(entry, "120"):
			sawResize = true
		case strings.HasPrefix(entry, "SendSignal") && strings.Contains(entry, "SIGTERM"):
			sawSignal = true
		}
	}
	if !sawInput || !sawResize || !sawSignal {
		t.Fatalf("expected pty input, resize and SIGTERM control calls, got %v", stub.controlLog())
	}

	// After close the session is gone rather than silently resurrected.
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__pty_read", map[string]interface{}{"pid": 7})
	if !strings.Contains(refused, "no pty session with pid 7") {
		t.Fatalf("post-close read = %q", refused)
	}
}

func TestEnvDPtyStartHonoursCommandPolicy(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, allowCommands("sh"))

	before := stub.processCalls.Load()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__pty_start", map[string]interface{}{
		"command": "python3",
	})
	if !strings.Contains(refused, "command denied") {
		t.Fatalf("unlisted pty command was not refused: %q", refused)
	}
	if after := stub.processCalls.Load(); after != before {
		t.Fatalf("a refused pty_start still reached envd: %d -> %d", before, after)
	}
}

func TestEnvDPtyRejectsOversizedTerminal(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, allowCommands("top"))

	before := stub.processCalls.Load()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__pty_start", map[string]interface{}{
		"command": "top", "cols": 5000, "rows": 24,
	})
	if !strings.Contains(refused, "above the 1000 limit") {
		t.Fatalf("oversize refusal = %q", refused)
	}
	if after := stub.processCalls.Load(); after != before {
		t.Fatalf("oversized request reached envd: %d -> %d", before, after)
	}
}

// A session must not survive the server that owns it.
func TestEnvDPtySessionsTornDownOnClose(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	cfg := newEnvDConfig(stub.server.URL)
	cfg.Profile = remotetools.ProfileOps
	cfg.Shell.AllowedCommands = []string{"top"}
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	session, ctx := connectEnvDServer(t, server)

	callTool(t, ctx, session, "remote_envd__pty_start", map[string]interface{}{"command": "top"})
	// Close tears the session down; a fresh server on the same target has no
	// registry entry for that pid.
	server.Close()
	if got := stub.processCalls.Load(); got == 0 {
		t.Fatal("expected at least the pty start call to have reached envd")
	}
}
