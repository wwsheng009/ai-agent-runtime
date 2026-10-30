package remotetools_test

// The process surface on envd. envd filters nothing, so these tests are really
// about the SHARED policy: the same CommandPolicy the ssh/http surface runs, with
// the same fail-closed default, and the jail applied to a command's cwd as well.

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// connectEnvDOps starts an ops-tier server with a caller-supplied policy tweak.
func connectEnvDOps(t *testing.T, stub *envdStub, policy func(*remotetools.Config)) (*mcp.ClientSession, context.Context) {
	t.Helper()
	cfg := newEnvDConfig(stub.server.URL)
	cfg.Profile = remotetools.ProfileOps
	if policy != nil {
		policy(cfg)
	}
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { server.Close() })
	session, ctx := connectEnvDServer(t, server)
	return session, ctx
}

func allowCommands(commands ...string) func(*remotetools.Config) {
	return func(cfg *remotetools.Config) {
		cfg.Shell.AllowedCommands = commands
	}
}

func TestEnvDShellRunsAllowedCommand(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, allowCommands("echo"))

	got := callTool(t, ctx, session, "remote_envd__shell", map[string]interface{}{"command": "echo hi"})
	if !strings.Contains(got, "ran: echo hi") {
		t.Fatalf("shell did not run the command: %q", got)
	}
	if !strings.Contains(got, "[exit 0]") {
		t.Fatalf("shell result lost the exit status: %q", got)
	}
}

func TestEnvDShellRefusesUnlistedCommandWithoutCallingEnvd(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, allowCommands("echo"))

	before := stub.processCalls.Load()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__shell", map[string]interface{}{"command": "id"})
	if !strings.Contains(refused, "command denied") {
		t.Fatalf("unlisted command refusal = %q", refused)
	}
	if after := stub.processCalls.Load(); after != before {
		t.Fatalf("a refused command still reached envd: %d -> %d", before, after)
	}
}

func TestEnvDShellRefusesMetacharacterChaining(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	// The gate is opt-in, so this test enables it explicitly: it covers the
	// refusal path itself, not the default policy.
	session, ctx := connectEnvDOps(t, stub, func(cfg *remotetools.Config) {
		cfg.Shell.AllowedCommands = []string{"echo"}
		cfg.Shell.DenyShellMetachars = true
	})

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__shell", map[string]interface{}{
		"command": "echo hi; id",
	})
	if !strings.Contains(refused, "command denied") || !strings.Contains(refused, "metachar") {
		t.Fatalf("metachar refusal = %q", refused)
	}
}

func TestEnvDShellRefusesDestructiveCommandEvenWhenListed(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, allowCommands("rm"))

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__shell", map[string]interface{}{
		"command": "rm -rf /",
	})
	if !strings.Contains(refused, "command denied") {
		t.Fatalf("destructive refusal = %q", refused)
	}
}

// An ops profile with no allowlist must refuse everything rather than default open.
func TestEnvDShellFailsClosedWithNoAllowlist(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, nil)

	before := stub.processCalls.Load()
	refused := callEnvDExpectError(t, ctx, session, "remote_envd__shell", map[string]interface{}{"command": "echo hi"})
	if !strings.Contains(refused, "command denied") {
		t.Fatalf("empty allowlist did not refuse: %q", refused)
	}
	if after := stub.processCalls.Load(); after != before {
		t.Fatalf("fail-closed violation: envd was contacted %d time(s)", after-before)
	}
}

// A command started outside the workspace can touch anything its user can, so the
// cwd is jailed like every other path.
func TestEnvDShellRefusesEscapeThroughCwd(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{"/etc/passwd": "root:x:0:0\n"}, nil)
	session, ctx := connectEnvDOps(t, stub, allowCommands("cat"))

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__shell", map[string]interface{}{
		"command": "cat passwd", "cwd": "../etc",
	})
	if !strings.Contains(refused, "outside the workspace") {
		t.Fatalf("cwd escape refusal = %q", refused)
	}
}

func TestEnvDWatchDirReturnsBoundedWindow(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, nil)

	got := callTool(t, ctx, session, "remote_envd__watch_dir", map[string]interface{}{
		"path": ".", "wait_ms": 1500, "max_events": 5,
	})
	if !strings.Contains(got, "1 event(s)") {
		t.Fatalf("watch result = %q", got)
	}
	if !strings.Contains(got, "EVENT_TYPE_CREATE") || !strings.Contains(got, "probe.txt") {
		t.Fatalf("watch result lost event detail: %q", got)
	}
}

func TestEnvDWatchDirStaysInsideJail(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{}, nil)
	session, ctx := connectEnvDOps(t, stub, nil)

	refused := callEnvDExpectError(t, ctx, session, "remote_envd__watch_dir", map[string]interface{}{
		"path": "../etc", "wait_ms": 200,
	})
	if !strings.Contains(refused, "outside the workspace") {
		t.Fatalf("watch escape refusal = %q", refused)
	}
}
