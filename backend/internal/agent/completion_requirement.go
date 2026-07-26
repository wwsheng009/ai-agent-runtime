package agent

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// CompletionRequirementNone does not require a terminal outcome tool.
	CompletionRequirementNone = string(agentdef.CompletionNone)
	// CompletionRequirementCompleteTask requires report_task_outcome (or block_current_task).
	CompletionRequirementCompleteTask = string(agentdef.CompletionCompleteTask)

	defaultCompletionRecoveryTurns = 1
)

// NormalizeCompletionRequirement canonicalizes none|complete_task (empty → none).
func NormalizeCompletionRequirement(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", CompletionRequirementNone:
		return CompletionRequirementNone
	case CompletionRequirementCompleteTask, "complete-task", "completetask":
		return CompletionRequirementCompleteTask
	default:
		return CompletionRequirementNone
	}
}

// RequiresCompleteTask reports whether the loop must observe a task-outcome tool.
func RequiresCompleteTask(requirement string) bool {
	return NormalizeCompletionRequirement(requirement) == CompletionRequirementCompleteTask
}

func normalizeCompletionRecoveryTurns(maxTurns int, requirement string) int {
	if !RequiresCompleteTask(requirement) {
		return 0
	}
	if maxTurns < 0 {
		return 0
	}
	if maxTurns == 0 {
		return defaultCompletionRecoveryTurns
	}
	return maxTurns
}

// HasSuccessfulTaskOutcomeObservation reports a successful report_task_outcome
// or block_current_task observation (aligned with team.teammate_runner).
func HasSuccessfulTaskOutcomeObservation(observations []types.Observation) bool {
	for i := len(observations) - 1; i >= 0; i-- {
		obs := observations[i]
		if !obs.Success {
			continue
		}
		switch normalizeTaskOutcomeToolName(obs.Tool) {
		case toolbroker.ToolReportTaskOutcome, toolbroker.ToolBlockCurrentTask:
			return true
		}
	}
	return false
}

func normalizeTaskOutcomeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func completionRequirementReminder(requirement string, attempt, maxTurns int) string {
	_ = requirement
	if maxTurns <= 0 {
		maxTurns = defaultCompletionRecoveryTurns
	}
	remaining := maxTurns - attempt + 1
	if remaining < 1 {
		remaining = 1
	}
	body := strings.TrimSpace(strings.Join([]string{
		"This worker run requires a structured task completion.",
		"Before finishing, call report_task_outcome with task_status done|failed|blocked|handoff and a short summary",
		"(block_current_task remains a compatibility alias for blocked/handoff).",
		"Do not end with text only; emit the completion tool call now.",
		"This is recovery attempt " + itoaMin1(attempt) + " of " + itoaMin1(maxTurns) +
			" (" + itoaMin1(remaining) + " remaining including this turn).",
	}, " "))
	// R3: unified <system-reminder> envelope (body kept plain for tests/hosts).
	return FormatSystemReminder(ReminderKindCompletionRequirement, body)
}

// newCompletionRequirementReminderMessage builds a durable recovery reminder.
func newCompletionRequirementReminderMessage(requirement string, attempt, maxTurns int) *types.Message {
	return NewSystemReminderMessage(SystemReminder{
		Kind:    ReminderKindCompletionRequirement,
		Body:    stripSystemReminderEnvelope(completionRequirementReminder(requirement, attempt, maxTurns)),
		Durable: true,
		Extra: types.Metadata{
			"completion_requirement_reminder": true,
			"completion_recovery_attempt":     attempt,
		},
	})
}

// stripSystemReminderEnvelope returns the inner body when content is already wrapped.
func stripSystemReminderEnvelope(content string) string {
	content = strings.TrimSpace(content)
	const openPrefix = "<system-reminder"
	const closeTag = "</system-reminder>"
	if !strings.HasPrefix(content, openPrefix) || !strings.HasSuffix(content, closeTag) {
		return content
	}
	start := strings.Index(content, ">")
	if start < 0 || start+1 >= len(content)-len(closeTag) {
		return content
	}
	return strings.TrimSpace(content[start+1 : len(content)-len(closeTag)])
}

func itoaMin1(n int) string {
	if n < 1 {
		n = 1
	}
	// tiny local formatter avoids strconv import noise for small counts
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

func missingCompletionRequirementMessage(requirement string) string {
	_ = requirement
	return "completion requirement complete_task was not satisfied: no successful report_task_outcome or block_current_task observation"
}

func applyCompletionResultFields(result *Result, requirement string, satisfied bool, recoveryTurns int) {
	if result == nil {
		return
	}
	result.CompletionRecoveryTurns = recoveryTurns
	if !RequiresCompleteTask(requirement) {
		result.CompletionSatisfied = nil
		return
	}
	value := satisfied
	result.CompletionSatisfied = &value
}
