package remotetools_test

import (
	"path/filepath"
	"runtime"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// pinTestShell forces one candidate for tests that exercise a specific shell's
// syntax. The production default intentionally follows the shared auto-detect
// chain, which can be Git Bash on Windows.
func pinTestShell(t *testing.T, cfg *remotetools.Config, selector string) {
	t.Helper()
	cfg.Shell.Program = selector
	cfg.Shell.Path = ""
	cfg.Shell.Prefer = nil
	if err := cfg.Normalize(); err != nil {
		t.Skipf("shell %q is not available: %v", selector, err)
	}
}

// pinPowerShellTestShell selects whichever PowerShell build is probed
// successfully, so syntax-specific tests still run on hosts where pwsh exists
// but Windows PowerShell does not (or the reverse).
func pinPowerShellTestShell(t *testing.T, cfg *remotetools.Config) {
	t.Helper()
	var lastErr error
	for _, selector := range []string{"pwsh", "powershell"} {
		cfg.Shell.Program = selector
		cfg.Shell.Path = ""
		cfg.Shell.Prefer = nil
		if err := cfg.Normalize(); err != nil {
			lastErr = err
			continue
		}
		if cfg.ResolvedShell.Type == runtimeexecutor.ShellTypePwsh ||
			cfg.ResolvedShell.Type == runtimeexecutor.ShellTypePowerShell {
			return
		}
	}
	t.Skipf("PowerShell is not available: %v", lastErr)
}

// pathEchoCommand returns a shell-appropriate command that prints PATH and the
// literal spelling produced when PATH is absent.
func pathEchoCommand(shellType runtimeexecutor.ShellType) (command, literal string) {
	switch shellType {
	case runtimeexecutor.ShellTypeCmd:
		return "echo %PATH%", "%PATH%"
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "echo $env:PATH", "$env:PATH"
	default:
		return "echo $PATH", "$PATH"
	}
}

// noColorEchoCommand prints the neutralized NO_COLOR value with a marker that
// cannot be confused with job ids or status text.
func noColorEchoCommand(shellType runtimeexecutor.ShellType) string {
	switch shellType {
	case runtimeexecutor.ShellTypeCmd:
		return "echo NO_COLOR=%NO_COLOR%"
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "echo NO_COLOR=$env:NO_COLOR"
	default:
		return "echo NO_COLOR=$NO_COLOR"
	}
}

func computerNameEchoCommand(shellType runtimeexecutor.ShellType) string {
	switch shellType {
	case runtimeexecutor.ShellTypeCmd:
		return "echo %COMPUTERNAME%"
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "echo $env:COMPUTERNAME"
	default:
		return "echo $COMPUTERNAME"
	}
}

// envEchoCommand returns a shell-appropriate command for one environment
// variable. Quoting the POSIX form keeps backslashes in Windows paths from
// being interpreted as escapes.
func envEchoCommand(shellType runtimeexecutor.ShellType, name string) string {
	switch shellType {
	case runtimeexecutor.ShellTypeCmd:
		return "echo %" + name + "%"
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "echo $env:" + name
	default:
		return `echo "$` + name + `"`
	}
}

// workdirProbeCommand returns a command that prints the current directory.
func workdirProbeCommand(shellType runtimeexecutor.ShellType) (string, []string) {
	switch shellType {
	case runtimeexecutor.ShellTypeCmd:
		return "cd", []string{"cd"}
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "Get-Location", []string{"get-location"}
	default:
		if runtime.GOOS == "windows" {
			// MSYS bash prints its virtual /tmp/... view for pwd. -W asks for the
			// native Windows path, which is the value the server supplied as cwd.
			return "pwd -W", []string{"pwd"}
		}
		return "pwd", []string{"pwd"}
	}
}

// quotedProbeCommand invokes a script under a directory whose name contains a
// space, using the selected shell's call syntax.
func quotedProbeCommand(shellType runtimeexecutor.ShellType, script, arg string) string {
	if shellType == runtimeexecutor.ShellTypePwsh || shellType == runtimeexecutor.ShellTypePowerShell {
		return `& "` + script + `" "` + arg + `"`
	}
	return `"` + script + `" "` + arg + `"`
}

// emptySuccessCommand creates a file/directory without writing to stdout.
func emptySuccessCommand(shellType runtimeexecutor.ShellType, root string) string {
	target := filepath.Join(root, "empty-output-probe")
	switch shellType {
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "Set-Content -LiteralPath '" + target + "' -Value ok"
	case runtimeexecutor.ShellTypeCmd:
		return `mkdir "` + target + `"`
	default:
		return `mkdir "` + target + `"`
	}
}
