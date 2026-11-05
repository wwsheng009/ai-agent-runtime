package remotetools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// resolveShell selects the shell used by shell and job_start.
//
// Empty configured means the same detection order the built-in shell tool uses:
// Windows pwsh -> powershell -> cmd.exe; Unix $SHELL -> zsh -> bash -> sh.
// A configured value is resolved once at startup so a typo is a startup error,
// not a surprising failure on the first model tool call.
func resolveShell(configured string) (runtimeexecutor.Shell, error) {
	trimmed := strings.TrimSpace(configured)
	if trimmed == "" {
		shell := runtimeexecutor.DefaultUserShell()
		if strings.TrimSpace(shell.Path) == "" || shell.Type == "" {
			return runtimeexecutor.Shell{}, fmt.Errorf(
				"no supported shell found; set shell.program or --shell (want pwsh, powershell, cmd, bash, zsh or sh)")
		}
		resolved, lookErr := exec.LookPath(shell.Path)
		if lookErr == nil {
			shell.Path = resolved
			return shell, nil
		}
		// DefaultUserShell's Windows fallback is the bare name "cmd". If PATH
		// is unusually empty, cmd.exe still lives in the system directory.
		if shell.Type == runtimeexecutor.ShellTypeCmd && runtime.GOOS == "windows" {
			if root := strings.TrimSpace(os.Getenv("SystemRoot")); root != "" {
				candidate := filepath.Join(root, "System32", "cmd.exe")
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					shell.Path = candidate
					return shell, nil
				}
			}
		}
		return runtimeexecutor.Shell{}, fmt.Errorf(
			"shell %q is not executable: %w", shell.Path, lookErr)
	}

	path, err := exec.LookPath(trimmed)
	if err != nil {
		return runtimeexecutor.Shell{}, fmt.Errorf(
			"shell %q not found: %w (set shell.program/--shell to an installed shell)", trimmed, err)
	}
	shellType := runtimeexecutor.DetectShellType(filepath.Base(path))
	if shellType == "" {
		return runtimeexecutor.Shell{}, fmt.Errorf(
			"shell %q is not supported (want pwsh, powershell, cmd, bash, zsh or sh)", trimmed)
	}
	if shellType == runtimeexecutor.ShellTypeCmd && runtime.GOOS != "windows" {
		return runtimeexecutor.Shell{}, fmt.Errorf(
			"shell %q is cmd.exe, which is only available on Windows", trimmed)
	}
	return runtimeexecutor.Shell{Path: path, Type: shellType}, nil
}

// resolvedShellForConfig returns the shell chosen by Normalize, falling back to
// the platform detector for callers that construct a tool without NewServer.
func resolvedShellForConfig(cfg *Config) runtimeexecutor.Shell {
	if cfg != nil && strings.TrimSpace(cfg.ResolvedShell.Path) != "" {
		return cfg.ResolvedShell
	}
	if shell, err := resolveShell(""); err == nil {
		return shell
	}
	return runtimeexecutor.DefaultUserShell()
}

// shellInvocation returns the argv used to run one command. cmd.exe keeps the
// /d /s /c form and the raw CreateProcess command line; other shells use the
// built-in DeriveExecArgs contract (-c, or PowerShell -NoProfile -NonInteractive
// -Command / -EncodedCommand).
func shellInvocation(shell runtimeexecutor.Shell, command string) (string, []string) {
	if shell.Type == runtimeexecutor.ShellTypeCmd {
		path := strings.TrimSpace(shell.Path)
		if path == "" {
			path = "cmd.exe"
		}
		return path, []string{"/d", "/s", "/c", command}
	}
	if shell.Type == runtimeexecutor.ShellTypePwsh || shell.Type == runtimeexecutor.ShellTypePowerShell {
		command = wrapPowerShellExitCode(command)
	}
	argv := shell.DeriveExecArgs(command, false)
	if len(argv) == 0 {
		return "", nil
	}
	return argv[0], append([]string(nil), argv[1:]...)
}

// wrapPowerShellExitCode preserves the command's real process status.
//
// `pwsh -Command "cmd /c exit 3"` exits 1, not 3, because PowerShell folds a
// failed native command into its own generic failure. The epilogue records both
// $? (cmdlet/pipeline success) and $LASTEXITCODE (native command status), then
// exits with the most specific status available.
func wrapPowerShellExitCode(command string) string {
	return runtimeexecutor.WrapPowerShellExitCode(
		runtimeexecutor.Shell{Type: runtimeexecutor.ShellTypePwsh},
		command,
	)
}

// shellRawCmdLine returns the verbatim Windows command line for cmd.exe, or
// empty for shells whose argv is passed directly.
func shellRawCmdLine(shell runtimeexecutor.Shell, command string) string {
	if shell.Type != runtimeexecutor.ShellTypeCmd {
		return ""
	}
	return windowsShellRawCmdLine(shell.Path, command)
}

// shellLabel is the short, model-facing identity of the selected shell.
func shellLabel(shell runtimeexecutor.Shell) string {
	name := filepath.Base(strings.TrimSpace(shell.Path))
	if name == "" {
		name = strings.TrimSpace(string(shell.Type))
	}
	if name == "" {
		return "unknown shell"
	}
	if shell.Type == "" {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, shell.Type)
}

// shellSyntaxHint tells the model which command language it is writing. It is
// appended to the MCP tool description at startup, so the hint cannot drift
// from the shell that will actually execute the command.
func shellSyntaxHint(shell runtimeexecutor.Shell) string {
	switch shell.Type {
	case runtimeexecutor.ShellTypePwsh, runtimeexecutor.ShellTypePowerShell:
		return "Use PowerShell syntax; `$env:VAR` reads environment variables; prefix an external executable path with `&` when it contains spaces; prefer `Get-Location` for the current directory and `Select-Object -First N` instead of `head`."
	case runtimeexecutor.ShellTypeCmd:
		return "Use cmd.exe syntax; quote paths containing spaces; `%VAR%` expands environment variables; `cd` or `echo %cd%` shows the current directory."
	case runtimeexecutor.ShellTypeBash, runtimeexecutor.ShellTypeZsh, runtimeexecutor.ShellTypeSh:
		return "Use POSIX shell syntax; `$VAR` expands environment variables; `pwd` shows the current directory; quote paths containing spaces."
	default:
		// envd always executes through /bin/sh -c and deliberately clears
		// ResolvedShell (Type == ""). Treat the zero value as POSIX rather than
		// falling back to an ambiguous hint.
		return "Use POSIX shell syntax; `$VAR` expands environment variables; `pwd` shows the current directory; quote paths containing spaces."
	}
}

func shellPromptHint(shell runtimeexecutor.Shell) string {
	return "Shell: " + shellLabel(shell) + ". " + shellSyntaxHint(shell)
}

func shellToolDescription(base string, shell runtimeexecutor.Shell) string {
	return strings.TrimSpace(base + " " + shellPromptHint(shell))
}

func shellCommandParameterDescription(shell runtimeexecutor.Shell) string {
	return "Command line for " + shellLabel(shell) + ". " + shellSyntaxHint(shell) +
		" Subject to the target's command policy; pipes, redirection and substitution are refused when shell.denyShellMetachars is true."
}
