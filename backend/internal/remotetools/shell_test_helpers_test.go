package remotetools_test

import (
	"path/filepath"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

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

// workdirProbeCommand returns a command that prints the current directory.
func workdirProbeCommand(shellType runtimeexecutor.ShellType) (string, []string) {
	switch shellType {
	case runtimeexecutor.ShellTypeCmd:
		return "cd", []string{"cd"}
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "Get-Location", []string{"get-location"}
	default:
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
