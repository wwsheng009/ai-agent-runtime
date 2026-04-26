package executor

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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

// DeriveExecArgs returns the argv slice needed to execute a command string
// through this shell, mirroring the logic from codex-rs/core/src/shell.rs
// derive_exec_args().
//
//	login=true  → login shell flags (-l / -NoProfile absent)
//	login=false → non-login shell flags
func (s Shell) DeriveExecArgs(command string, login bool) []string {
	switch s.Type {
	case ShellTypeBash, ShellTypeZsh, ShellTypeSh:
		if login {
			return []string{s.Path, "-lc", command}
		}
		return []string{s.Path, "-c", command}
	case ShellTypePowerShell, ShellTypePwsh:
		if login {
			return []string{s.Path, "-Command", command}
		}
		return []string{s.Path, "-NoProfile", "-Command", command}
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
