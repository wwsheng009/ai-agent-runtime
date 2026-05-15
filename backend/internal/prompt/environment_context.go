package prompt

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// RenderEnvironmentContextBlock renders a compact model-visible environment block
// that captures the current execution context relevant to shell/tool planning.
func RenderEnvironmentContextBlock(cwd string) string {
	values := CurrentEnvironmentValues()
	lines := []string{"<environment_context>"}

	if cwd = strings.TrimSpace(cwd); cwd != "" {
		lines = append(lines, fmt.Sprintf("  <cwd>%s</cwd>", cwd))
	}

	lines = append(lines, fmt.Sprintf("  <os>%s</os>", values["os"]))
	lines = append(lines, fmt.Sprintf("  <shell>%s</shell>", values["shell"]))
	lines = append(lines, fmt.Sprintf("  <current_date>%s</current_date>", values["current_date"]))
	lines = append(lines, fmt.Sprintf("  <timezone>%s</timezone>", values["timezone"]))
	lines = append(lines, "</environment_context>")

	return strings.Join(lines, "\n")
}

// CurrentEnvironmentValues returns shell- and time-related runtime facts that
// are useful both for prompt injection and runtime summaries.
func CurrentEnvironmentValues() map[string]interface{} {
	shell := runtimeexecutor.DefaultUserShell()
	now := time.Now()
	return map[string]interface{}{
		"os":           runtime.GOOS,
		"shell":        detectedShellName(shell),
		"current_date": now.Format("2006-01-02"),
		"timezone":     detectedTimezoneLabel(now),
	}
}

// RenderShellExecutionGuidance renders shell-specific guardrails that help the
// model choose commands compatible with the detected runtime environment.
func RenderShellExecutionGuidance() string {
	shell := runtimeexecutor.DefaultUserShell()
	lines := []string{
		fmt.Sprintf("Detected operating system: %s.", runtime.GOOS),
		fmt.Sprintf("Detected user shell: %s.", detectedShellName(shell)),
	}

	switch {
	case runtime.GOOS == "windows" &&
		(shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh):
		lines = append(lines,
			"Treat the current shell as PowerShell-compatible, not bash-compatible.",
			"Do not use Unix-only commands such as `head` when a PowerShell-native form exists.",
			"To limit output, prefer `... | Select-Object -First 200` instead of `... | head -200`.",
			"To print the current directory, prefer `Get-Location` or `pwd` on PowerShell.",
		)
	case runtime.GOOS == "windows" && shell.Type == runtimeexecutor.ShellTypeCmd:
		lines = append(lines,
			"Treat the current shell as cmd.exe, not bash-compatible.",
			"Do not use Unix-only commands such as `head`.",
			"To print the current directory, prefer `cd` or `echo %cd%` on cmd.exe.",
		)
	}

	if len(lines) == 0 {
		return ""
	}

	rendered := make([]string, 0, len(lines)+1)
	rendered = append(rendered, "Shell guidance:")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rendered = append(rendered, "- "+line)
	}
	return strings.Join(rendered, "\n")
}

// RenderFileEditingGuidance renders guardrails that steer the model toward
// dedicated file-editing tools instead of shell-based file writes.
func RenderFileEditingGuidance() string {
	lines := []string{
		"Prefer the dedicated file tools for workspace mutations.",
		"Use `apply_patch` for code edits, multi-line replacements, and structured multi-hunk edits; use `edit` only for a small exact string that was just confirmed with view/grep.",
		"Use `write` for small full-file writes and `append_write` for long text chunking.",
		"Do not use shell redirection, here-strings, or inline `Set-Content`/`Out-File`/`Add-Content` commands for large file writes.",
		"For long content, prefer skeleton -> append_write chunk(s) -> apply_patch cleanup, instead of one huge full-file write or one huge shell command.",
		"If a client or runtime-side transport write API is available, prefer that over pushing oversized inline content through model-generated shell text.",
	}

	rendered := make([]string, 0, len(lines)+1)
	rendered = append(rendered, "File editing guidance:")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rendered = append(rendered, "- "+line)
	}
	return strings.Join(rendered, "\n")
}

// RenderParallelToolGuidance renders guidance that encourages batching
// independent read-only inspections into the same assistant turn.
func RenderParallelToolGuidance() string {
	lines := []string{
		"Use this only for independent read-only inspections.",
		"When a tool definition explicitly marks supports_parallel=true, prefer batching it with other independent calls in the same assistant turn.",
		"When several inspections do not depend on each other, request them in the same assistant turn so the runtime can batch them in parallel.",
		"Keep dependent tool calls serial and wait for the earlier result before planning the next dependent step.",
	}

	rendered := make([]string, 0, len(lines)+1)
	rendered = append(rendered, "Parallel tool guidance:")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rendered = append(rendered, "- "+line)
	}
	return strings.Join(rendered, "\n")
}

func detectedShellName(shell runtimeexecutor.Shell) string {
	if text := strings.TrimSpace(string(shell.Type)); text != "" {
		return text
	}
	if text := strings.TrimSpace(shell.Path); text != "" {
		return text
	}
	return "unknown"
}

func detectedTimezoneLabel(now time.Time) string {
	zoneName, offsetSeconds := now.Zone()
	zoneName = strings.TrimSpace(zoneName)
	if zoneName != "" && !strings.EqualFold(zoneName, "Local") {
		return zoneName
	}
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	return fmt.Sprintf("UTC%s%02d:%02d", sign, hours, minutes)
}
