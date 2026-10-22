package remotetools

import (
	"os"
	"path/filepath"
	"strings"
)

// Platform describes the target's operating system semantics.
//
// The remote server may run on Windows, where three assumptions in the POSIX path
// break:
//
//  1. paths are case-insensitive and may carry a volume name (C:\...);
//  2. `/` and `\` are both accepted, and the shell is cmd.exe or PowerShell;
//  3. "inside a directory" cannot be decided by a case-sensitive prefix test.
//
// All three are handled here rather than sprinkled through the tools, so a future
// Linux-only regression cannot silently reintroduce one of them.
type Platform struct {
	// Windows reports Windows semantics.
	Windows bool
}

// currentPlatform returns the platform of the running process.
//
// The remote server always judges paths for its own host, never for the client, so
// runtime.GOOS is the correct source: there is no cross-platform path translation
// in this design (design §6.3).
func currentPlatform() Platform {
	return Platform{Windows: os.PathSeparator == '\\'}
}

// normalizePath converts a client-supplied path into the target's native form.
//
// On Windows a client may send either separator; folding to the native one keeps
// the later prefix test and the filesystem calls consistent.
func (p Platform) normalizePath(value string) string {
	trimmed := strings.TrimSpace(value)
	if !p.Windows {
		return trimmed
	}
	unified := strings.ReplaceAll(trimmed, "/", `\`)
	// Strip the extended-length prefix: the jail comparison must see the same
	// spelling the filesystem reports, and `\\?\C:\x` never equals `C:\x`.
	isExtended := strings.HasPrefix(unified, `\\?\`)
	if isExtended {
		unified = strings.TrimPrefix(unified, `\\?\`)
		// `\\?\UNC\server\share` → `\\server\share`
		if strings.HasPrefix(strings.ToUpper(unified), `UNC\`) {
			unified = `\\` + unified[len(`UNC\`):]
		}
	}
	return unified
}

// pathEqual compares two cleaned absolute paths using the target's case rules.
func (p Platform) pathEqual(a, b string) bool {
	if p.Windows {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// pathWithinPlatform reports whether target sits under root on this platform.
//
// filepath.Rel already handles volume names and case-insensitivity at the OS
// level, but its behavior differs across Go versions for mixed-case Windows paths;
// the explicit EqualFold prefix check makes the Windows branch deterministic.
func (p Platform) pathWithinPlatform(target, root string) bool {
	if target == "" || root == "" {
		return false
	}
	target = filepath.Clean(p.normalizePath(target))
	root = filepath.Clean(p.normalizePath(root))
	if p.pathEqual(target, root) {
		return true
	}
	if p.Windows {
		// A volume mismatch (C: vs D:) can never be "within".
		if !strings.EqualFold(filepath.VolumeName(target), filepath.VolumeName(root)) {
			return false
		}
		prefix := root
		if !strings.HasSuffix(prefix, `\`) {
			prefix += `\`
		}
		return len(target) > len(prefix) && strings.EqualFold(target[:len(prefix)], prefix)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

// rejectWindowsDevicePath refuses the NT device namespace and reserved DOS names.
//
// `\\?\GLOBALROOT\...` and `\\.\PhysicalDrive0` bypass normal directory semantics,
// and reading a device such as NUL or COM1 either yields nothing or blocks. The
// local toolkit's path guard refuses the same shapes; a remote target needs its own
// copy because it does not import that guard (it is in package tools, which pulls
// the whole local tool set).
func rejectWindowsDevicePath(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	normalized := strings.ReplaceAll(raw, "/", `\`)
	upper := strings.ToUpper(normalized)
	switch {
	case strings.HasPrefix(upper, `\\.\`):
		return newToolError(CodeInvalidArgument, "use a normal file path", "Windows device namespace paths are not supported")
	case strings.HasPrefix(upper, `\\?\GLOBALROOT`):
		return newToolError(CodeInvalidArgument, "use a normal file path", "Windows global-root device paths are not supported")
	case strings.HasPrefix(upper, `\??\`):
		return newToolError(CodeInvalidArgument, "use a normal file path", "Windows NT device namespace paths are not supported")
	}
	// Reserved DOS device names apply in every directory and with any extension.
	for _, segment := range strings.Split(normalized, `\`) {
		if segment == "" {
			continue
		}
		name := segment
		if idx := strings.IndexByte(name, '.'); idx >= 0 {
			name = name[:idx]
		}
		if isWindowsReservedDeviceName(name) {
			return newToolError(CodeInvalidArgument, "use a normal file path", "Windows reserved device name %q is not a file", segment)
		}
	}
	return nil
}

// isWindowsReservedDeviceName reports the legacy DOS device namespace.
func isWindowsReservedDeviceName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(upper) == 4 {
		prefix := upper[:3]
		if (prefix == "COM" || prefix == "LPT") && upper[3] >= '1' && upper[3] <= '9' {
			return true
		}
	}
	return false
}

// windowsShellInvocation returns the shell command for one command string.
//
// cmd.exe is the default because it exists on every Windows target. A target that
// needs PowerShell can set a custom invocation through the shell policy; P4 keeps
// the default predictable rather than guessing from the command text.
func windowsShellInvocation(command string) (string, []string) {
	return "cmd.exe", []string{"/d", "/s", "/c", command}
}