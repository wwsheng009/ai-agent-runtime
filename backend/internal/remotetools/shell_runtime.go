package remotetools

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// resolveShell selects the shell used by shell and job_start.
//
// Selection is delegated to executor.ResolveShell, the same resolver used by
// aicli/TUI and runtime-server. That gives the remote server the identical
// Windows candidate chain (git-bash -> msys-bash -> busybox -> pwsh ->
// powershell -> cmd), the same sentinel probe and the same WSL-stub guard
// instead of a second, weaker detector.
//
// This is an explicit startup resolution, not CurrentShell: a remote-tools
// process must resolve against the target machine's PATH/config and must not
// inherit an unrelated aicli process-wide resolver.
func resolveShell(cfg ShellConfig) (runtimeexecutor.Shell, error) {
	resolution, err := resolveShellResolution(cfg)
	if err != nil {
		return runtimeexecutor.Shell{}, err
	}
	if resolution == nil || strings.TrimSpace(resolution.Shell.Path) == "" ||
		resolution.Shell.Type == "" {
		return runtimeexecutor.Shell{}, fmt.Errorf("no usable shell found; set shell.prefer/path/program or --shell")
	}
	return resolution.Shell, nil
}

// resolveShellResolution resolves the selector and keeps the candidate matrix
// for doctor/startup diagnostics.
func resolveShellResolution(cfg ShellConfig) (*runtimeexecutor.ShellResolution, error) {
	selection, err := cfg.selectionConfig()
	if err != nil {
		return nil, err
	}
	resolution, err := runtimeexecutor.ResolveShell(selection)
	if err != nil {
		return resolution, fmt.Errorf("resolve shell selection: %w", err)
	}
	if resolution != nil {
		// executor.ResolveShell memoizes the resolution pointer. Clone before
		// filling a platform-specific ID so two servers cannot race on the same
		// cached object.
		cloned := *resolution
		cloned.Candidates = append([]runtimeexecutor.ShellCandidateReport(nil), resolution.Candidates...)
		ensureResolvedShellIdentity(&cloned.Shell)
		resolution = &cloned
	}
	return resolution, nil
}

// ensureResolvedShellIdentity fills the stable candidate ID for platforms where
// the legacy zero-config path in executor.ResolveShell returns a shell without
// one (notably Unix). Doctor and metadata stay uniform across platforms.
func ensureResolvedShellIdentity(shell *runtimeexecutor.Shell) {
	if shell == nil || strings.TrimSpace(shell.ID) != "" {
		return
	}
	base := strings.ToLower(strings.TrimSuffix(
		filepath.Base(strings.ReplaceAll(strings.TrimSpace(shell.Path), `\`, "/")), ".exe"))
	switch base {
	case "bash":
		shell.ID = runtimeexecutor.ShellIDBash
	case "zsh":
		shell.ID = runtimeexecutor.ShellIDZsh
	case "sh":
		shell.ID = runtimeexecutor.ShellIDSh
	case "busybox":
		shell.ID = runtimeexecutor.ShellIDBusybox
	case "pwsh":
		shell.ID = runtimeexecutor.ShellIDPwsh
	case "powershell":
		shell.ID = runtimeexecutor.ShellIDPowerShell
	case "cmd":
		shell.ID = runtimeexecutor.ShellIDCmd
	}
}

// selectionConfig translates the remote-tools shell block to the shared
// executor selector. Program is kept as a compatibility alias:
//   - a stable candidate ID -> Prefer with exactly that candidate
//   - any other value       -> Path (the historical --shell behavior)
func (c ShellConfig) selectionConfig() (runtimeexecutor.ShellConfig, error) {
	path := strings.TrimSpace(c.Path)
	program := strings.TrimSpace(c.Program)
	prefer := normalizedShellPrefer(c.Prefer)
	if err := validateShellPrefer(prefer); err != nil {
		return runtimeexecutor.ShellConfig{}, err
	}
	var probeTimeout time.Duration
	if c.ProbeTimeout != nil {
		probeTimeout = c.ProbeTimeout.Duration
	}
	if probeTimeout < 0 {
		return runtimeexecutor.ShellConfig{}, fmt.Errorf("shell.probeTimeout must not be negative")
	}

	out := runtimeexecutor.ShellConfig{
		AutoDetect:   c.AutoDetect,
		Prefer:       nil,
		Path:         "",
		ProbeTimeout: probeTimeout,
		StrictProbe:  c.StrictProbe,
	}

	if path != "" {
		if program != "" && !strings.EqualFold(program, path) {
			return runtimeexecutor.ShellConfig{}, fmt.Errorf(
				"shell.path and shell.program are mutually exclusive")
		}
		if len(prefer) > 0 {
			return runtimeexecutor.ShellConfig{}, fmt.Errorf(
				"shell.path and shell.prefer are mutually exclusive")
		}
		out.Path = path
		return out, nil
	}

	if program != "" {
		if len(prefer) > 0 {
			return runtimeexecutor.ShellConfig{}, fmt.Errorf(
				"shell.program and shell.prefer are mutually exclusive")
		}
		if runtimeexecutor.IsKnownShellCandidateID(program) {
			out.Prefer = []string{strings.ToLower(program)}
			return out, nil
		}
		out.Path = program
		return out, nil
	}

	out.Prefer = prefer
	return out, nil
}

func normalizedShellPrefer(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func validateShellPrefer(prefer []string) error {
	for _, id := range prefer {
		if !runtimeexecutor.IsKnownShellCandidateID(id) {
			return fmt.Errorf("shell.prefer: unknown candidate %q (want %s)",
				id, strings.Join(runtimeexecutor.ShellCandidateIDs(), ", "))
		}
	}
	return nil
}

// CandidateChain returns the ordered candidate IDs the shared resolver will
// consider. It performs no location or probing; doctor uses it to show the
// configured/default intent even when a candidate is absent.
func (c ShellConfig) CandidateChain() ([]string, error) {
	selection, err := c.selectionConfig()
	if err != nil {
		return nil, err
	}
	return runtimeexecutor.ShellCandidateChain(selection), nil
}

// resolvedShellForConfig returns the shell chosen by Normalize, falling back to
// the platform detector for callers that construct a tool without NewServer.
func resolvedShellForConfig(cfg *Config) runtimeexecutor.Shell {
	if cfg != nil && strings.TrimSpace(cfg.ResolvedShell.Path) != "" {
		return cfg.ResolvedShell
	}
	if cfg != nil {
		if shell, err := resolveShell(cfg.Shell); err == nil {
			return shell
		}
	}
	if shell, err := resolveShell(ShellConfig{}); err == nil {
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
	id := strings.TrimSpace(shell.ID)
	shellType := strings.TrimSpace(string(shell.Type))
	switch {
	case id != "" && shellType != "":
		return fmt.Sprintf("%s [%s] (%s)", name, id, shellType)
	case id != "":
		return fmt.Sprintf("%s [%s]", name, id)
	case shellType == "":
		return name
	}
	return fmt.Sprintf("%s (%s)", name, shellType)
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
