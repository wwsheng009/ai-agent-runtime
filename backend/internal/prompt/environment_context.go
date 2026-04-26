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
