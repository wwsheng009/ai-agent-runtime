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
			return []string{s.Path, "-lc", command}
		}
		return []string{s.Path, "-c", command}
	case ShellTypePowerShell, ShellTypePwsh:
		longScript := len(command) > PowerShellEncodedCommandThreshold
		if login {
			if longScript {
				return []string{s.Path, "-EncodedCommand", EncodePowerShellCommand(command)}
			}
			return []string{s.Path, "-Command", command}
		}
		args := []string{s.Path, "-NoProfile", "-NonInteractive"}
		if longScript {
			return append(args, "-EncodedCommand", EncodePowerShellCommand(command))
		}
		return append(args, "-Command", command)
	case ShellTypeCmd:
		return []string{s.Path, "/c", command}
	default:
		// Fallback: treat as sh-like
		if login {
			return []string{s.Path, "-lc", command}
		}
		return []string{s.Path, "-c", command}
	}
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
