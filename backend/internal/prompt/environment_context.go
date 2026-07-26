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
	return RenderEnvironmentContextBlockFromValues(cwd, CurrentEnvironmentValues())
}

// RenderEnvironmentContextBlockFromValues renders an environment block from a
// previously captured values map. Callers that freeze session-scoped snapshots
// should reuse those values instead of re-probing the host each turn.
func RenderEnvironmentContextBlockFromValues(cwd string, values map[string]interface{}) string {
	if values == nil {
		values = map[string]interface{}{}
	}
	lines := []string{"<environment_context>"}

	if cwd = strings.TrimSpace(cwd); cwd != "" {
		lines = append(lines, fmt.Sprintf("  <cwd>%s</cwd>", cwd))
	}

	lines = append(lines, fmt.Sprintf("  <os>%s</os>", environmentValueString(values["os"])))
	lines = append(lines, fmt.Sprintf("  <shell>%s</shell>", environmentValueString(values["shell"])))
	lines = append(lines, fmt.Sprintf("  <current_date>%s</current_date>", environmentValueString(values["current_date"])))
	lines = append(lines, fmt.Sprintf("  <timezone>%s</timezone>", environmentValueString(values["timezone"])))
	if available := environmentValueStringSlice(values["available_commands"]); len(available) > 0 {
		lines = append(lines, fmt.Sprintf("  <available_commands>%s</available_commands>", strings.Join(available, ",")))
	}
	if unavailable := environmentValueStringSlice(values["unavailable_commands"]); len(unavailable) > 0 {
		lines = append(lines, fmt.Sprintf("  <unavailable_commands>%s</unavailable_commands>", strings.Join(unavailable, ",")))
	}
	lines = append(lines, "</environment_context>")

	return strings.Join(lines, "\n")
}

// EnvironmentSnapshot holds session-frozen environment prompt fragments.
// Capture once at session create / first ensure, then reuse for later turns.
type EnvironmentSnapshot struct {
	ContextBlock       string
	CapabilityGuidance string
	ProbedAt           time.Time
	Values             map[string]interface{}
}

// CaptureEnvironmentSnapshot measures the host once and returns rendered prompt
// fragments suitable for session freeze/reuse.
//
// Capability probing happens exactly once per capture: context values and
// capability guidance are derived from the same report so startup does not
// re-scan PATH for each prompt fragment.
func CaptureEnvironmentSnapshot(cwd string) EnvironmentSnapshot {
	report := DetectEnvironmentCapabilities()
	values := currentEnvironmentValuesFromReport(report)
	probedAt := report.ProbedAt
	if probedAt.IsZero() {
		probedAt = time.Now().UTC()
	} else {
		probedAt = probedAt.UTC()
	}
	return EnvironmentSnapshot{
		ContextBlock:       RenderEnvironmentContextBlockFromValues(cwd, values),
		CapabilityGuidance: renderEnvironmentCapabilityGuidanceFromReport(report),
		ProbedAt:           probedAt,
		Values:             values,
	}
}

// CurrentEnvironmentValues returns shell- and time-related runtime facts that
// are useful both for prompt injection and runtime summaries.
func CurrentEnvironmentValues() map[string]interface{} {
	return currentEnvironmentValuesFromReport(DetectEnvironmentCapabilities())
}

func currentEnvironmentValuesFromReport(report EnvironmentCapabilityReport) map[string]interface{} {
	shell := runtimeexecutor.DefaultUserShell()
	now := time.Now()
	values := map[string]interface{}{
		"os":           runtime.GOOS,
		"shell":        detectedShellName(shell),
		"current_date": now.Format("2006-01-02"),
		"timezone":     detectedTimezoneLabel(now),
	}
	if available := availableEnvironmentCommandsFromReport(report); len(available) > 0 {
		values["available_commands"] = available
	}
	if unavailable := highValueUnavailableCommandsFromReport(report); len(unavailable) > 0 {
		values["unavailable_commands"] = unavailable
	}
	return values
}

// RenderShellExecutionGuidance renders shell-specific guardrails that help the
// model choose commands compatible with the detected runtime environment.
func RenderShellExecutionGuidance() string {
	return RenderShellExecutionGuidanceWithCapability(RenderEnvironmentCapabilityGuidance())
}

// RenderShellExecutionGuidanceWithCapability renders shell guidance using a
// precomputed (possibly session-frozen) capability section. Pass empty to omit
// the measured capability appendix.
func RenderShellExecutionGuidanceWithCapability(capabilityGuidance string) string {
	shell := runtimeexecutor.DefaultUserShell()
	lines := []string{
		fmt.Sprintf("Detected operating system: %s.", runtime.GOOS),
		fmt.Sprintf("Detected user shell: %s.", detectedShellName(shell)),
		"Prefer toolkit `grep` for code search instead of shell `rg`/`grep` (rg in shell uses exit 1 for no matches and is easy to break with quotes/regex escapes).",
		"Prefer toolkit `ls`/`glob`/`view` for filesystem inspection; use shell for builds, tests, git, and package managers.",
		"Never invoke toolkit tool names as shell commands (for example `view -path ...` or `grep -pattern ...` inside bash). Call those tools directly with structured args.",
		"When using shell for multiple independent checks, prefer `commands` batching so one tool call returns all results.",
		"When several inspections do not depend on each other, batch them in one turn: `view.files`, `grep` with patterns/paths, and shell `commands`.",
		"Do not treat empty search results as a crash; change the pattern/path or use the dedicated search tool instead of retrying the identical query.",
		"If shell search fails with regex/path errors, switch to toolkit `grep` (literal=true for fixed text) rather than repairing complex shell quoting.",
		"Do not put shell globs in the rg path argument (e.g. `rg pattern backend/**/*.go`); use `rg -g \"*.go\" pattern backend` or toolkit `grep` path+glob.",
		"Non-zero process exit codes from shell tools are content results (inspect Exit code/Output), not tool crashes; only launch/timeout/cancel/permission failures are hard tool errors.",
	}

	switch {
	case runtime.GOOS == "windows" &&
		(shell.Type == runtimeexecutor.ShellTypePowerShell || shell.Type == runtimeexecutor.ShellTypePwsh):
		lines = append(lines,
			"Treat the current shell as PowerShell-compatible, not bash-compatible.",
			"Do not use Unix-only commands such as `head` when a PowerShell-native form exists.",
			"To limit output, prefer `... | Select-Object -First 200` instead of `... | head -200`.",
			"To print the current directory, prefer `Get-Location` or `pwd` on PowerShell.",
			"Do not use bash heredoc (`python - <<'PY'`, `cat <<EOF`); PowerShell does not support that syntax. Prefer dedicated file tools, `python -c`, or write a temp script with `write`/`append_write` then execute it.",
			"Avoid chaining many search commands with `;` when one failed rg exit 1 can poison the whole command; use toolkit `grep` or shell `commands` with independent items.",
			"Avoid bash-only operators such as `&&`/`||` chains when PowerShell parsing is unreliable; prefer separate shell `commands` items or native PowerShell control flow.",
			"On Windows, unexpanded path globs often become os error 123; keep path as a real directory and put filters in `-g`/`--glob` or toolkit `grep`.",
		)
	case runtime.GOOS == "windows" && shell.Type == runtimeexecutor.ShellTypeCmd:
		lines = append(lines,
			"Treat the current shell as cmd.exe, not bash-compatible.",
			"Do not use Unix-only commands such as `head`.",
			"To print the current directory, prefer `cd` or `echo %cd%` on cmd.exe.",
			"Do not use bash heredoc or Unix pipelines that cmd.exe cannot parse; prefer dedicated file/search tools.",
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
	if capability := strings.TrimSpace(capabilityGuidance); capability != "" {
		rendered = append(rendered, "", capability)
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
		"Before editing, re-read the latest nearby context with view/grep; stale @@ context is a common apply_patch failure mode.",
		"After STALE_CONTEXT / old_string miss / hunk miss, re-view first and rebuild; never replay the same patch or old_string unchanged.",
		"Keep each apply_patch focused on one file or one nearby change region; split large or multi-file patches.",
		"For Add File hunks, every content line must start with `+`.",
		"When using `todos`, keep at most one task `in_progress`; mark the previous task completed/pending before starting another.",
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
		"Prefer view.files for multiple file ranges and grep.patterns plus grep.paths for related searches, so one tool result can answer the whole inspection question.",
		"Use shell.commands for checks or test targets that do not need model decisions between commands; set parallel=true only for independent read-only commands, otherwise keep the default ordered execution, and keep separate tool calls only for true data dependencies.",
		"Gather all predictable independent evidence in one assistant turn before deciding edits; do not discover unchanged files one at a time.",
		"For large files, always pass view offset/limit; if is_truncated=true, continue with suggested_next_offset instead of re-viewing the whole file from offset 0.",
		"Do not repeatedly full-file view the same path; reuse prior evidence or narrow with offset/limit/grep.",
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

// RenderTaskDifficultyGuidance renders the delegation policy used by runtimes
// that can map child task difficulty to local provider/model routes.
func RenderTaskDifficultyGuidance() string {
	lines := []string{
		"Task difficulty rating and subagent delegation policy:",
		"",
		"Before decomposing or delegating work, rate the overall user request difficulty as one of: easy, normal, hard, expert.",
		"Use easy for local, low-risk, single-step work. Use normal for regular multi-file or multi-step work. Use hard for complex implementation, broad investigation, or tasks requiring test verification. Use expert for high-risk architecture, security, permission, provider/protocol, migration, or cross-system consistency work.",
		"Do not spawn subagents for easy work unless explicitly requested or clearly beneficial. Prefer one or more subagents for hard/expert work when subtasks can be isolated.",
		"When spawning subagents, include difficulty and difficulty_rationale for every child task. Do not invent provider/model names; leave provider/model empty unless the user explicitly asked for a specific override. The runtime maps difficulty to local provider/model configuration.",
		"For multiple subtasks, use this structure: id, role, goal, difficulty, difficulty_rationale, depends_on, read_only, and tools_whitelist when needed.",
	}
	return strings.Join(lines, "\n")
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

func environmentValueString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		if value == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func environmentValueStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := environmentValueString(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func highValueUnavailableCommands() []string {
	return highValueUnavailableCommandsFromReport(DetectEnvironmentCapabilities())
}

func highValueUnavailableCommandsFromReport(report EnvironmentCapabilityReport) []string {
	unavailable := unavailableEnvironmentCommandsFromReport(report)
	if len(unavailable) == 0 {
		return nil
	}
	out := make([]string, 0, len(unavailable))
	for _, name := range unavailable {
		switch name {
		case "git", "rg", "gh", "go", "node", "python", "docker", "cargo":
			out = append(out, name)
		}
	}
	return out
}
