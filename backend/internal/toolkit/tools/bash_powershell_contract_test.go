package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// TestCoreShellPowerShellExitAndOutputContracts pins the shared PowerShell
// behavior the core shell had in common with remote-tools: native commands must
// keep their real exit code, while cmdlet formatting and stderr still reach the
// model. Both the default executor path (sandbox nil) and the sandboxed direct
// shellexec path are covered.
func TestCoreShellPowerShellExitAndOutputContracts(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific PowerShell contract")
	}
	shell := runtimeexecutor.DefaultUserShell()
	if shell.Type != runtimeexecutor.ShellTypePwsh && shell.Type != runtimeexecutor.ShellTypePowerShell {
		t.Skip("test requires PowerShell or pwsh")
	}

	for _, tc := range []struct {
		name        string
		withSandbox bool
	}{
		{name: "default_executor"},
		{name: "sandboxed_direct_exec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewShellTool()
			if tc.withSandbox {
				tool.SetSandbox(runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{}))
			}

			// Native command status must survive the PowerShell wrapper.
			result := executeCoreShell(t, tool, "cmd /c exit 3")
			assertExitCode(t, result, 3)
			if !strings.Contains(result.Content, "Exit code: 3") {
				t.Fatalf("expected real exit code in content, got %q", result.Content)
			}

			// $host.SetShouldExit must not discard object formatting.
			result = executeCoreShell(t, tool, "Get-Location")
			assertExitCode(t, result, 0)
			if !strings.Contains(result.Content, "Path") || !strings.Contains(result.Content, "----") {
				t.Fatalf("expected Get-Location table output after exit wrapper, got %q", result.Content)
			}

			// A cmdlet failure is content success with stderr and exit code 1,
			// not a bare "exit status 1" tool failure.
			missing := filepath.Join(t.TempDir(), "missing.txt")
			result = executeCoreShell(t, tool, "Get-Item '"+missing+"'")
			assertExitCode(t, result, 1)
			if !strings.Contains(result.Content, missing) {
				t.Fatalf("expected stderr path in content, got %q", result.Content)
			}

			// A successful cmdlet with no output still reports exit code 0.
			emptyFile := filepath.Join(t.TempDir(), "empty.txt")
			result = executeCoreShell(t, tool, fmt.Sprintf("Set-Content -LiteralPath '%s' -Value 'x'", emptyFile))
			assertExitCode(t, result, 0)
			if !strings.Contains(result.Content, "Exit code: 0") {
				t.Fatalf("expected empty success to remain success, got %q", result.Content)
			}

			// Backslash paths are data, not JSON escapes, in the core surface.
			result = executeCoreShell(t, tool, `Write-Output 'common\tenantctx'`)
			assertExitCode(t, result, 0)
			if !strings.Contains(result.Content, `common\tenantctx`) {
				t.Fatalf("expected backslash path to be preserved, got %q", result.Content)
			}

			// The core shell inherits the full parent environment; COMPUTERNAME
			// is not a remote-tools-only baseline entry.
			if computerName := strings.TrimSpace(os.Getenv("COMPUTERNAME")); computerName != "" {
				result = executeCoreShell(t, tool, "$env:COMPUTERNAME")
				assertExitCode(t, result, 0)
				if !strings.Contains(result.Content, computerName) {
					t.Fatalf("expected COMPUTERNAME %q in content, got %q", computerName, result.Content)
				}
			}
		})
	}
}

func executeCoreShell(t *testing.T, tool *ShellTool, command string) *toolkit.ToolResult {
	t.Helper()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"command":     command,
		"timeout_sec": 10,
	})
	if err != nil {
		t.Fatalf("execute %q: %v", command, err)
	}
	if result == nil {
		t.Fatalf("execute %q returned nil result", command)
	}
	if !result.Success {
		t.Fatalf("execute %q returned a hard failure: %v", command, result.Error)
	}
	return result
}

func assertExitCode(t *testing.T, result *toolkit.ToolResult, want int) {
	t.Helper()
	got, ok := result.Metadata["exit_code"]
	if !ok {
		t.Fatalf("missing exit_code metadata: %#v", result.Metadata)
	}
	if asInt, ok := asIntish(got); !ok || asInt != want {
		t.Fatalf("exit_code = %#v, want %d (metadata %#v)", got, want, result.Metadata)
	}
}
