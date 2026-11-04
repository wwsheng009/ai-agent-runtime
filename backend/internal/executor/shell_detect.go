package executor

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"
)

// ShellType represents the kind of shell detected on the system.
type ShellType string

const (
	ShellTypeBash       ShellType = "bash"
	ShellTypeZsh        ShellType = "zsh"
	ShellTypeSh         ShellType = "sh"
	ShellTypePowerShell ShellType = "powershell"
	ShellTypePwsh       ShellType = "pwsh"
	ShellTypeCmd        ShellType = "cmd"
)

// Shell holds the resolved shell binary path and type.
type Shell struct {
	Path string
	Type ShellType
	// Prefix is inserted between the binary and its flags. It exists for
	// multi-applet binaries: busybox.exe needs `sh` first, and probing /
	// execution must agree on the same argv shape.
	Prefix []string
	// ID is the stable candidate identifier (git-bash, busybox, pwsh, ...).
	// Empty for shells resolved outside the candidate chain.
	ID string
	// Dialect is the command language the model must write for this shell.
	Dialect ShellDialect
	// Version is the shell's self-reported version, when probed.
	Version string
	// Source records how the shell was chosen: config-path, prefer,
	// autodetect or fallback.
	Source string
}

// String returns a human-readable shell label suitable for prompts, logs,
// and debugging metadata.
func (s Shell) String() string {
	typeName := strings.TrimSpace(string(s.Type))
	path := strings.TrimSpace(s.Path)
	switch {
	case typeName != "" && path != "":
		return fmt.Sprintf("%s (%s)", typeName, path)
	case typeName != "":
		return typeName
	case path != "":
		return path
	default:
		return "unknown"
	}
}

// Metadata returns a stable metadata map describing the selected shell.
func (s Shell) Metadata() map[string]interface{} {
	typeName := strings.TrimSpace(string(s.Type))
	path := strings.TrimSpace(s.Path)
	if typeName == "" && path == "" {
		return nil
	}
	metadata := map[string]interface{}{
		"shell_display": s.String(),
	}
	if typeName != "" {
		metadata["shell_type"] = typeName
	}
	if path != "" {
		metadata["shell_path"] = path
	}
	if s.ID != "" {
		metadata["shell_id"] = s.ID
	}
	if s.Dialect != "" {
		metadata["shell_dialect"] = string(s.Dialect)
	}
	if s.Source != "" {
		metadata["shell_source"] = s.Source
	}
	if s.Version != "" {
		metadata["shell_version"] = s.Version
	}
	return metadata
}

// PowerShellCommandPrefix makes PowerShell emit UTF-8 regardless of the host's
// active code page. It is embedded into -EncodedCommand payloads and prefixed
// onto -Command payloads by the callers.
const PowerShellCommandPrefix = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; "

// PowerShellEncodedCommandThreshold is the script length above which
// DeriveExecArgs passes the script as -EncodedCommand (base64 UTF-16LE) instead
// of -Command. Long scripts otherwise have to survive the Windows command line
// limit and every intermediate quoting layer (cmd.exe parsing, powershellQuote,
// CreateProcess re-quoting), which is where "it worked in my shell" failures
// come from.
const PowerShellEncodedCommandThreshold = 7000

// DeriveExecArgs returns the argv slice needed to execute a command string
// through this shell, mirroring the logic from codex-rs/core/src/shell.rs
// derive_exec_args().
//
//	login=true  → login shell flags (-l / -NoProfile absent)
//	login=false → non-login, hardened agent flags (PowerShell: -NoProfile
//	              -NonInteractive so a profile or an interactive prompt can
//	              never block or pollute a tool call)
//
// PowerShell payloads longer than PowerShellEncodedCommandThreshold switch to
// -EncodedCommand in both modes; the caller must then not prefix the script
// again (the UTF-8 directive is already inside the encoded payload).
func (s Shell) DeriveExecArgs(command string, login bool) []string {
	switch s.Type {
	case ShellTypeBash, ShellTypeZsh, ShellTypeSh:
		if login {
			return s.withPrefix("-lc", command)
		}
		return s.withPrefix("-c", command)
	case ShellTypePowerShell, ShellTypePwsh:
		longScript := len(command) > PowerShellEncodedCommandThreshold
		if login {
			if longScript {
				return s.withPrefix("-EncodedCommand", EncodePowerShellCommand(command))
			}
			return s.withPrefix("-Command", command)
		}
		if longScript {
			return s.withPrefix("-NoProfile", "-NonInteractive", "-EncodedCommand", EncodePowerShellCommand(command))
		}
		return s.withPrefix("-NoProfile", "-NonInteractive", "-Command", command)
	case ShellTypeCmd:
		// `/c`, not MSYS's `//c`: DeriveExecArgs feeds CreateProcess directly,
		// where no MSYS argument rewriting happens. `//c` is only a workaround
		// for a POSIX shell line that forwards to cmd.exe, and that shape is
		// built separately (see background.buildShellCommandLine).
		return s.withPrefix("/c", command)
	default:
		// Fallback: treat as sh-like
		if login {
			return s.withPrefix("-lc", command)
		}
		return s.withPrefix("-c", command)
	}
}

// withPrefix builds argv as <path> <prefix...> <flags...>.
func (s Shell) withPrefix(tail ...string) []string {
	argv := make([]string, 0, 1+len(s.Prefix)+len(tail))
	argv = append(argv, s.Path)
	argv = append(argv, s.Prefix...)
	argv = append(argv, tail...)
	return argv
}

// DialectOrDerived returns the dialect recorded at detection time, falling back
// to one derived from Type for shells that never went through the resolver
// (DefaultUserShell, test fixtures, callers that build Shell{} by hand).
//
// Without the fallback the zero value "" would read as "not POSIX", so a
// PowerShell-only gate would wrongly reject a real bash.
func (s Shell) DialectOrDerived() ShellDialect {
	if s.Dialect != "" {
		return s.Dialect
	}
	return DialectFor(s.Type, s.ID)
}

// SpeaksPosix reports whether this shell takes sh-style scripts. It is the one
// predicate callers should use to decide between a .sh runner and a PowerShell
// runner, or between `/bin/sh`-style and PowerShell quoting.
func (s Shell) SpeaksPosix() bool {
	return s.DialectOrDerived().IsPosixDialect()
}

// SupportsStdinScript reports whether the shell can read its script from stdin.
// Only POSIX shells are wired today; PowerShell's `-Command -` behaviour is
// still unverified (plan appendix B item 6), so it stays on -EncodedCommand.
func (s Shell) SupportsStdinScript() bool {
	switch s.Type {
	case ShellTypeBash, ShellTypeZsh, ShellTypeSh:
		return true
	default:
		return false
	}
}

// DeriveExecInvocation returns the argv plus an optional stdin payload for one
// command string.
//
// Short commands always go through argv (`-c`): that keeps their stdin free, so
// `cat`, `git commit -F -` and other stdin consumers behave as before. Long
// scripts switch to stdin delivery, which removes the Windows command-line
// length limit entirely — no more 7000-char heuristic deciding between -Command
// and -EncodedCommand for POSIX shells.
func (s Shell) DeriveExecInvocation(command string, delivery ScriptDelivery) ([]string, string) {
	if delivery != ScriptDeliveryStdin || !s.SupportsStdinScript() {
		return s.DeriveExecArgs(command, false), ""
	}
	if len(command) <= ShellStdinScriptThreshold {
		return s.DeriveExecArgs(command, false), ""
	}
	return s.withPrefix(s.stdinFlags()...), command
}

// ShellStdinScriptThreshold is the script length above which POSIX shells read
// the script from stdin instead of argv. It matches the historical PowerShell
// switch-over point so both transports change at the same size.
const ShellStdinScriptThreshold = PowerShellEncodedCommandThreshold

// stdinFlags returns the non-interactive flags for stdin script delivery.
func (s Shell) stdinFlags() []string {
	if s.Type == ShellTypeBash {
		return []string{"--noprofile", "--norc", "-s"}
	}
	// busybox ash / dash do not know --noprofile; -s alone is POSIX.
	return []string{"-s"}
}

// WrapPowerShellExitCode appends an epilogue that preserves the command's real
// process status. PowerShell folds a failed native command into its own generic
// failure when invoked with -Command: `pwsh -Command "cmd /c exit 3"` exits 1,
// not 3. The epilogue records both $? (cmdlet/pipeline success) and
// $LASTEXITCODE (native command status), then uses $host.SetShouldExit so object
// formatting (for example Get-Location) is not lost the way a raw `exit` can
// lose it.
//
// The wrapper is a no-op for non-PowerShell shells. Callers that already wrap
// the same command must not call it twice.
func WrapPowerShellExitCode(shell Shell, command string) string {
	switch shell.Type {
	case ShellTypePowerShell, ShellTypePwsh:
	default:
		return command
	}
	return command + `
$__aicli_ok = $?
$__aicli_code = $LASTEXITCODE
if (-not $__aicli_ok) {
    if ($null -ne $__aicli_code -and [int]$__aicli_code -ne 0) { $host.SetShouldExit([int]$__aicli_code) }
    else { $host.SetShouldExit(1) }
} elseif ($null -ne $__aicli_code) {
    $host.SetShouldExit([int]$__aicli_code)
} else {
    $host.SetShouldExit(0)
}
`
}

// EncodePowerShellCommand base64-encodes a script as UTF-16LE, the encoding
// -EncodedCommand expects, and prepends PowerShellCommandPrefix so the
// encoded payload keeps the UTF-8 output contract on its own.
func EncodePowerShellCommand(command string) string {
	script := PowerShellCommandPrefix + command
	units := utf16.Encode([]rune(script))
	payload := make([]byte, 0, len(units)*2)
	for _, unit := range units {
		payload = append(payload, byte(unit), byte(unit>>8))
	}
	return base64.StdEncoding.EncodeToString(payload)
}

// ShellArgsUseEncodedCommand reports whether argv carries a PowerShell script
// through -EncodedCommand, i.e. the script argument is already base64 and must
// not be text-prefixed (doing so would corrupt the payload).
func ShellArgsUseEncodedCommand(args []string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), "-EncodedCommand") {
			return true
		}
	}
	return false
}

// DetectShellType maps a shell binary name (e.g. "bash", "pwsh") to a
// ShellType. Returns the zero value if the name is not recognised.
func DetectShellType(binaryName string) ShellType {
	switch strings.ToLower(binaryName) {
	case "bash":
		return ShellTypeBash
	case "zsh":
		return ShellTypeZsh
	case "sh":
		return ShellTypeSh
	case "powershell", "powershell.exe":
		return ShellTypePowerShell
	case "pwsh", "pwsh.exe":
		return ShellTypePwsh
	case "cmd", "cmd.exe":
		return ShellTypeCmd
	default:
		return ShellType("")
	}
}

// DefaultUserShell attempts to find the best available shell for the current
// user, following the same priority order as codex-rs:
//
//	Windows:  pwsh → powershell → cmd
//	Unix:     $SHELL → zsh → bash → /bin/sh
func DefaultUserShell() Shell {
	if runtime.GOOS == "windows" {
		return defaultWindowsShell()
	}
	return defaultUnixShell()
}

func defaultWindowsShell() Shell {
	// Prefer PowerShell Core (pwsh) → Windows PowerShell → cmd
	for _, candidate := range []struct {
		name string
		typ  ShellType
	}{
		{"pwsh", ShellTypePwsh},
		{"powershell", ShellTypePowerShell},
	} {
		if path, err := exec.LookPath(candidate.name); err == nil {
			return Shell{Path: path, Type: candidate.typ}
		}
	}
	// Fallback to cmd (always available on Windows)
	if path, err := exec.LookPath("cmd"); err == nil {
		return Shell{Path: path, Type: ShellTypeCmd}
	}
	// Absolute fallback
	return Shell{Path: "cmd", Type: ShellTypeCmd}
}

func defaultUnixShell() Shell {
	// 1. Check $SHELL environment variable
	if shellPath := os.Getenv("SHELL"); shellPath != "" {
		base := shellBaseName(shellPath)
		if st := DetectShellType(base); st != "" {
			return Shell{Path: shellPath, Type: st}
		}
	}

	// 2. Try common shells in priority order
	for _, candidate := range []struct {
		path string
		typ  ShellType
	}{
		{"/bin/zsh", ShellTypeZsh},
		{"/bin/bash", ShellTypeBash},
		{"/bin/sh", ShellTypeSh},
	} {
		if info, err := os.Stat(candidate.path); err == nil && !info.IsDir() {
			return Shell{Path: candidate.path, Type: candidate.typ}
		}
	}

	// 3. Try LookPath as last resort
	for _, name := range []string{"zsh", "bash", "sh"} {
		if path, err := exec.LookPath(name); err == nil {
			return Shell{Path: path, Type: DetectShellType(name)}
		}
	}

	return Shell{Path: "/bin/sh", Type: ShellTypeSh}
}

// ShellByPath resolves a Shell from an explicit path provided by the caller
// (e.g. from a tool parameter). Falls back to DefaultUserShell if the path
// cannot be resolved.
func ShellByPath(path string) Shell {
	if path == "" {
		return DefaultUserShell()
	}
	base := shellBaseName(path)
	if st := DetectShellType(base); st != "" {
		return Shell{Path: path, Type: st}
	}
	// Unrecognised binary – still return it as a sh-like shell
	return Shell{Path: path, Type: ShellTypeSh}
}

func shellBaseName(path string) string {
	// Handle both / and \ separators for Windows compatibility
	path = strings.ReplaceAll(path, `\`, "/")
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}
