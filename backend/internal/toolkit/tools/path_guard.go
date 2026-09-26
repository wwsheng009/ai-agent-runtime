package tools

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

// This file implements the shared special-path guard for the file tools.
//
// Design rationale (docs/analysis/commandcode-read-tool-design-borrowing-20260926.md §3.2):
// a read tool must reject device / FIFO paths *before any I/O*. Opening a FIFO
// blocks until a writer appears, so a guard placed after os.Open is useless;
// /dev/zero and friends stream meaningless bytes into the read pipeline; and
// Windows device namespaces (NUL, CON, COM1, \\.\PhysicalDrive0,
// \\?\GLOBALROOT) have semantics that ordinary stat/open calls do not reflect.
// unsupportedPathNameReason therefore works purely on the requested path
// string (safe before stat/open), while unsupportedFileModeReason covers the
// type layer once a stat result already exists (stat is safe; open is not).

// unixDeviceBaseNames lists the Unix pseudo-file/device names whose reads
// yield no meaningful regular-file content. /dev/null is intentionally refused
// as well: reading it is a legal EOF, but it carries no information and a
// uniform refusal keeps the contract simple.
var unixDeviceBaseNames = map[string]struct{}{
	"zero":    {},
	"random":  {},
	"urandom": {},
	"full":    {},
	"stdin":   {},
	"stdout":  {},
	"stderr":  {},
	"null":    {},
	"tty":     {},
	"console": {},
}

// windowsReservedDeviceNames is the legacy DOS device namespace, which stays
// reserved on Windows in every directory and with any extension
// (NUL.txt is still NUL). COM1..COM9 / LPT1..LPT9 only: COM10 and LPT10 are
// ordinary names.
var windowsReservedDeviceNames = func() map[string]struct{} {
	names := map[string]struct{}{
		"CON": {}, "PRN": {}, "AUX": {}, "NUL": {},
		"CONIN$": {}, "CONOUT$": {},
	}
	for i := 1; i <= 9; i++ {
		names[fmt.Sprintf("COM%d", i)] = struct{}{}
		names[fmt.Sprintf("LPT%d", i)] = struct{}{}
	}
	return names
}()

// unsupportedPathNameReason reports whether a requested path name must be
// refused before any stat/open call. It returns "" when the name is usable,
// otherwise a short English reason suitable for a model-visible error.
//
// Checks, in order:
//   - Unix device/pseudo-file namespace: /dev/zero, /dev/random, /dev/urandom,
//     /dev/full, /dev/stdin, /dev/stdout, /dev/stderr, /dev/null, /dev/std*,
//     /dev/fd/* and /proc/<pid>/fd/* (including /proc/self/fd/*).
//   - Windows device namespace prefixes: \\.\ and \\?\GLOBALROOT.
//   - Windows reserved device names (CON, PRN, AUX, NUL, COM1..COM9,
//     LPT1..LPT9), checked case-insensitively per path segment, ignoring the
//     extension and tolerating trailing dots/spaces ("NUL ", "nul.txt").
//
// Path separators and case follow the host platform's habits: Windows matches
// case-insensitively, Unix stays case-sensitive. Windows-style segments are
// still detected on Unix so a path such as C:\src\nul is refused everywhere.
func unsupportedPathNameReason(path string) string {
	if path == "" {
		return ""
	}
	if reason := unixDeviceNameReason(path); reason != "" {
		return reason
	}
	if reason := windowsDeviceNamespaceReason(path); reason != "" {
		return reason
	}
	if name := windowsReservedDeviceNameInPath(path); name != "" {
		return fmt.Sprintf("Windows reserved device name %q is not a readable file", name)
	}
	return ""
}

// unsupportedFileModeReason reports whether a stat result describes a file
// type the read/write tools cannot handle. Regular files and directories are
// accepted (""); every other type returns its kind name.
func unsupportedFileModeReason(mode os.FileMode) string {
	switch {
	case mode.IsRegular() || mode.IsDir():
		return ""
	case mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "device"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	case mode&os.ModeIrregular != 0:
		return "irregular file"
	default:
		return ""
	}
}

// unixDeviceNameReason matches the Unix device/pseudo-file namespace on the
// literal path string. Backslashes are folded to slashes first so Windows
// style spellings are still caught when the check runs on Unix.
func unixDeviceNameReason(path string) string {
	compare := strings.ReplaceAll(path, `\`, "/")
	if runtime.GOOS == "windows" {
		compare = strings.ToLower(compare)
	}

	if compare == "/dev/fd" || strings.HasPrefix(compare, "/dev/fd/") {
		return `unsupported device path "/dev/fd/*"`
	}
	if strings.HasPrefix(compare, "/dev/") {
		rest := strings.TrimPrefix(compare, "/dev/")
		name := rest
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			name = rest[:idx]
		}
		// /dev/std* covers stdin/stdout/stderr and any alias in that family.
		if strings.HasPrefix(name, "std") {
			return fmt.Sprintf("unsupported device path %q", "/dev/"+name)
		}
		if _, ok := unixDeviceBaseNames[name]; ok {
			return fmt.Sprintf("unsupported device path %q", "/dev/"+name)
		}
	}
	if isProcFDPath(compare) {
		return `unsupported process file-descriptor path "/proc/<pid>/fd/*"`
	}
	return ""
}

// isProcFDPath recognizes /proc/<pid>/fd[/...] (including /proc/self/fd/*)
// after separator normalization. The middle segment is not validated so both
// numeric pids and the "self" alias match.
func isProcFDPath(compare string) bool {
	if !strings.HasPrefix(compare, "/proc/") {
		return false
	}
	segments := strings.Split(strings.Trim(compare, "/"), "/")
	if len(segments) < 3 {
		return false
	}
	return segments[0] == "proc" && segments[2] == "fd"
}

// windowsDeviceNamespaceReason rejects the NT device namespace prefixes
// (\\.\ and \\?\GLOBALROOT). Ordinary extended-length paths such as
// \\?\C:\dir\file.txt are intentionally not rejected.
func windowsDeviceNamespaceReason(path string) string {
	const (
		devicePrefix     = `\\.\`
		globalRootPrefix = `\\?\GLOBALROOT`
		ntPrefix         = `\??\`
	)
	for _, candidate := range []string{path, strings.ReplaceAll(path, "/", `\`)} {
		if strings.HasPrefix(candidate, devicePrefix) {
			return `Windows device namespace path (\\.\) is not supported`
		}
		if len(candidate) >= len(globalRootPrefix) &&
			strings.EqualFold(candidate[:len(globalRootPrefix)], globalRootPrefix) {
			return `Windows global-root device namespace path (\\?\GLOBALROOT) is not supported`
		}
		if strings.HasPrefix(candidate, ntPrefix) {
			return `Windows NT device namespace path (\??\) is not supported`
		}
	}
	return ""
}

// errDevicePath marks a refusal of a device/stream path by the file tools. It
// is wrapped into a runtime error so the deny carries a machine-readable code
// and metadata (policy/failure_class/next_action) instead of a bare message.
var errDevicePath = errors.New("device or stream path is not a regular file")

// devicePathRefusalError turns a special-path refusal into a structured deny.
// The message matches the view tool's long-standing wording so every file tool
// refuses the same path the same way; the metadata gives the recovery route.
func devicePathRefusalError(targetPath, reason string) error {
	return runtimeerrors.WrapWithContext(
		runtimeerrors.ErrToolInvalidArgs,
		fmt.Sprintf("不支持的特殊文件（%s）: %s；请改用 ls/glob 选择普通文件后重试。", reason, targetPath),
		errDevicePath,
		map[string]interface{}{
			"policy":         "device_path",
			"failure_class":  "device_path",
			"target_path":    targetPath,
			"refusal_reason": reason,
			"path_refused":   true,
			"next_action":    "这是设备/流路径而不是常规文件；请用 ls/glob 定位项目内的常规文件，再用 view/grep 读取。不要原样重试同一路径。",
		},
	)
}

// windowsReservedDeviceNameInPath scans every path segment (split on both '/'
// and '\\' so Windows-style paths are checked on any host) and returns the
// canonical reserved name when one is found. Trailing dots/spaces are ignored,
// as is the extension, matching how Windows resolves these names.
func windowsReservedDeviceNameInPath(path string) string {
	segments := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, segment := range segments {
		name := strings.Trim(segment, " .")
		if name == "" {
			continue
		}
		if idx := strings.IndexByte(name, '.'); idx >= 0 {
			name = name[:idx]
		}
		if name == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if _, ok := windowsReservedDeviceNames[upper]; ok {
			return upper
		}
	}
	return ""
}
