package policy

import (
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
)

// This file implements the accept-edits safe-file-command fast lane of
// docs/analysis/commandcode-permissions-design-borrowing-20260926.md §4.11.
//
// accept_edits may auto-accept only the narrow class of file commands whose
// effect is verifiable: mkdir/touch/cp/mv/rmdir and non-recursive rm (plus the
// PowerShell equivalents). Recursive deletes, find -delete, targets outside the
// workspace, sensitive targets, and commands that cannot be resolved
// statically keep the normal ask behavior.

// Safe-file assessment reasons (stable contracts for tests and diagnostics).
const (
	ShellSafeFileReasonNotSafeCommand  = "safe_file:not_safe_command"
	ShellSafeFileReasonRecursive       = "safe_file:recursive_delete"
	ShellSafeFileReasonUnsafeTarget    = "safe_file:target_outside_workspace"
	ShellSafeFileReasonSensitiveTarget = "safe_file:sensitive_target"
	ShellSafeFileReasonDynamicSyntax   = "safe_file:dynamic_syntax"
	ShellSafeFileReasonUnparsable      = "safe_file:unparsable_command"
	ShellSafeFileReasonNoTarget        = "safe_file:no_target"
)

// ShellSafeFileAssessment is the result of the accept-edits fast-lane check.
type ShellSafeFileAssessment struct {
	Allowed bool
	Reason  string
}

// AssessShellSafeFileCommand reports whether an accept-edits fast lane may
// auto-allow one concrete shell command. Every segment must be a safe file
// command and every target must resolve inside workspaceRoot and not match the
// sensitive-path classifier. A missing workspace root never allows anything.
func AssessShellSafeFileCommand(command, workspaceRoot string) ShellSafeFileAssessment {
	command = strings.TrimSpace(command)
	if command == "" {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonNotSafeCommand}
	}
	for _, bad := range []string{">", "<", "`", "$"} {
		if strings.Contains(command, bad) {
			return ShellSafeFileAssessment{Reason: ShellSafeFileReasonDynamicSyntax}
		}
	}
	if hasPairedShellExpansion(command, '%') || hasPairedShellExpansion(command, '!') {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonDynamicSyntax}
	}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonUnsafeTarget}
	}
	segments, parsed := shellrisk.Segments(command)
	if !parsed {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonUnparsable}
	}
	if len(segments) == 0 {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonNotSafeCommand}
	}
	for _, segment := range segments {
		if assessment := assessShellSafeFileSegment(segment, root); !assessment.Allowed {
			return assessment
		}
	}
	return ShellSafeFileAssessment{Allowed: true}
}

func assessShellSafeFileSegment(segment, root string) ShellSafeFileAssessment {
	fields, ok := shellrisk.ResolveSegment(segment)
	if !ok || len(fields) == 0 {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonNotSafeCommand}
	}
	argv0 := normalizeShellCommandName(fields[0])
	args := fields[1:]

	if argv0 == "find" && hasFlagToken(args, "-delete") {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonRecursive}
	}

	targets := make([]string, 0, len(args))
	switch argv0 {
	case "rm", "del", "erase", "remove-item", "ri", "rd", "rmdir":
		if hasRecursiveRemoveFlag(args) {
			return ShellSafeFileAssessment{Reason: ShellSafeFileReasonRecursive}
		}
		targets = safeFileTargets(args)
	case "mkdir", "md", "new-item", "new-directory", "touch":
		targets = safeFileTargets(args)
	case "cp", "copy", "copy-item", "cpi", "mv", "move", "move-item", "mi":
		targets = safeFileTargets(args)
	default:
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonNotSafeCommand}
	}
	if len(targets) == 0 {
		return ShellSafeFileAssessment{Reason: ShellSafeFileReasonNoTarget}
	}
	for _, target := range targets {
		if !pathInsideWorkspace(root, target) {
			return ShellSafeFileAssessment{Reason: ShellSafeFileReasonUnsafeTarget}
		}
		if _, sensitive := ClassifySensitivePath(resolveSensitiveTarget(root, target)); sensitive {
			return ShellSafeFileAssessment{Reason: ShellSafeFileReasonSensitiveTarget}
		}
	}
	return ShellSafeFileAssessment{Allowed: true}
}

// hasRecursiveRemoveFlag covers POSIX short/long flags (including combined
// forms such as -rf/-fr), PowerShell's -Recurse, and cmd.exe's /s.
func hasRecursiveRemoveFlag(args []string) bool {
	for _, arg := range args {
		lower := strings.ToLower(strings.TrimSpace(arg))
		switch {
		case lower == "--recursive", lower == "-recurse", lower == "/s":
			return true
		case strings.HasPrefix(lower, "-") && !strings.HasPrefix(lower, "--"):
			if strings.ContainsAny(lower[1:], "rR") {
				return true
			}
		}
	}
	return false
}

// hasFlagToken reports whether one of the tokens equals the flag exactly.
func hasFlagToken(args []string, flag string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), flag) {
			return true
		}
	}
	return false
}

// safeFileTargets returns the non-flag arguments of a file command: POSIX
// `-x`/`--long` flags and cmd.exe's single-letter `/x` switches are skipped.
func safeFileTargets(args []string) []string {
	targets := make([]string, 0, len(args))
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		if arg == "" || arg == "--" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if len(arg) == 2 && arg[0] == '/' && isASCIILetter(arg[1]) {
			continue
		}
		targets = append(targets, arg)
	}
	return targets
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// pathInsideWorkspace resolves a command target against the workspace root and
// reports whether it stays inside. Glob characters and home references are not
// verifiable and therefore never allowed.
func pathInsideWorkspace(root, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" || strings.ContainsAny(target, "*?[") || strings.Contains(target, "~") ||
		strings.Contains(target, "$") || strings.Contains(target, "%") {
		return false
	}
	absRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(absRoot, target)
	}
	rel, err := filepath.Rel(absRoot, filepath.Clean(target))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// acceptEditsSafeFileAllowed applies the fast lane to every command of a
// request (single command or structured batch).
func acceptEditsSafeFileAllowed(commands []string, workspaceRoot string) (bool, string) {
	if len(commands) == 0 {
		return false, ""
	}
	for _, command := range commands {
		if assessment := AssessShellSafeFileCommand(command, workspaceRoot); !assessment.Allowed {
			return false, ""
		}
	}
	return true, strings.Join(commands, " ; ")
}
