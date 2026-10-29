package remotetools_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// writeQuotedCmdProbe creates a .cmd file under a directory whose name contains
// a space. Running it with a quoted path is the case os/exec's normal argv
// encoding corrupts for cmd.exe; PowerShell needs the & call operator.
func writeQuotedCmdProbe(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "probe dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir probe dir: %v", err)
	}
	script := filepath.Join(dir, "probe.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho %~1\r\n"), 0o600); err != nil {
		t.Fatalf("write probe script: %v", err)
	}
	return script
}

// TestRemoteShellPreservesQuotedPathOnWindows proves the shell tool preserves
// the caller's quoting for the resolved shell. cmd.exe gets a verbatim
// CreateProcess command line instead of Go's MSVCRT-style argv encoding; a
// PowerShell target uses the & call operator.
func TestRemoteShellPreservesQuotedPathOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows shell quoting")
	}
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"*"})
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	script := writeQuotedCmdProbe(t)
	command := quotedProbeCommand(cfg.ResolvedShell.Type, script, "hello world")
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": command,
	})
	if !strings.Contains(body, "hello world") {
		t.Fatalf("quoted path did not reach the probe: %s", body)
	}
	if strings.Contains(body, `\"`) {
		t.Fatalf("command line still contains escaped quotes: %s", body)
	}
}

// TestRemoteShellPreservesQuotedPathWithCmdOnWindows keeps the cmd.exe fallback
// covered even though the default on a modern Windows host is PowerShell.
func TestRemoteShellPreservesQuotedPathWithCmdOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows cmd.exe quoting")
	}
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"*"})
	cfg.Shell.Program = "cmd"
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	script := writeQuotedCmdProbe(t)
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": quotedProbeCommand(runtimeexecutor.ShellTypeCmd, script, "cmd hello"),
	})
	if !strings.Contains(body, "cmd hello") {
		t.Fatalf("quoted cmd path did not reach the probe: %s", body)
	}
}

// TestRemoteShellPowerShellCallOperatorHonorsAllowlistOnWindows proves the
// `&` syntax advertised to the model does not hide the script from a narrow
// allowedCommands entry.
func TestRemoteShellPowerShellCallOperatorHonorsAllowlistOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows shell quoting")
	}
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"probe"})
	if cfg.ResolvedShell.Type != runtimeexecutor.ShellTypePwsh &&
		cfg.ResolvedShell.Type != runtimeexecutor.ShellTypePowerShell {
		t.Skip("PowerShell is not the resolved Windows shell")
	}
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	script := writeQuotedCmdProbe(t)
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": quotedProbeCommand(cfg.ResolvedShell.Type, script, "allowlist hello"),
	})
	if !strings.Contains(body, "allowlist hello") {
		t.Fatalf("call-operator command was denied or lost its argument: %s", body)
	}
}

// TestRemoteShellBareAndQuotedPowerShellOnWindows covers the two failures from
// the live report: a bare command needs PATH from the platform baseline, and a
// quoted -Command argument must reach PowerShell intact instead of being
// printed back as the literal text "whoami".
func TestRemoteShellBareAndQuotedPowerShellOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows shell and powershell")
	}
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("powershell.exe is not installed")
	}
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"whoami", "powershell"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	bare := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "whoami",
	})
	if !strings.Contains(bare, `\`) {
		t.Fatalf("bare whoami did not resolve through PATH: %s", bare)
	}

	quoted := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": `powershell.exe -NoProfile -Command "whoami"`,
	})
	if strings.TrimSpace(quoted) == "whoami" || !strings.Contains(quoted, `\`) {
		t.Fatalf("powershell -Command received literal text: %s", quoted)
	}
}
