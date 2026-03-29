package team

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// TaskOutcomeStatus captures the structured terminal state emitted by a teammate.
type TaskOutcomeStatus string

const (
	TaskOutcomeDone    TaskOutcomeStatus = "done"
	TaskOutcomeBlocked TaskOutcomeStatus = "blocked"
	TaskOutcomeFailed  TaskOutcomeStatus = "failed"
	TaskOutcomeHandoff TaskOutcomeStatus = "handoff"
)

// TaskOutcomeContract defines the structured teammate completion contract.
type TaskOutcomeContract struct {
	Status    TaskOutcomeStatus `json:"task_status"`
	Summary   string            `json:"summary,omitempty"`
	Blocker   string            `json:"blocker,omitempty"`
	HandoffTo string            `json:"handoff_to,omitempty"`
}

// TaskOutcomeContractSchema returns the reusable JSON schema for teammate outcomes.
func TaskOutcomeContractSchema() map[string]interface{} {
	return TaskOutcomeContractSchemaFor()
}

// TaskOutcomeContractSchemaFor returns the reusable JSON schema for a restricted set of statuses.
func TaskOutcomeContractSchemaFor(allowed ...TaskOutcomeStatus) map[string]interface{} {
	enumValues := taskOutcomeStatusEnumValues(allowed...)
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_status": map[string]interface{}{
				"type":        "string",
				"enum":        enumValues,
				"description": fmt.Sprintf("Allowed values: %s.", joinTaskOutcomeStatuses(allowed...)),
			},
			"summary": map[string]interface{}{
				"type":        "string",
				"description": "Required for every task outcome.",
			},
			"blocker": map[string]interface{}{
				"type":        "string",
				"description": "Required for blocked, failed, and handoff outcomes.",
			},
			"handoff_to": map[string]interface{}{
				"type":        "string",
				"description": "Required only when task_status=handoff.",
			},
		},
		"required":             []string{"task_status", "summary"},
		"additionalProperties": false,
	}
}

// TaskOutcomePromptLines returns reusable prompt guidance for teammate outcome reporting.
func TaskOutcomePromptLines(allowed ...TaskOutcomeStatus) []string {
	statuses := taskOutcomeStatusEnumValues(allowed...)
	statusSummary := strings.Join(statuses, "|")
	statusDescription := joinTaskOutcomeStatuses(allowed...)
	return []string{
		"- Prefer calling report_task_outcome for terminal task state changes. If you do not call report_task_outcome or block_current_task, end your final response with a structured status block.",
		fmt.Sprintf("- Preferred format: ```json {\"task_status\":\"%s\",\"summary\":\"...\",\"blocker\":\"...\",\"handoff_to\":\"...\"} ```", statusSummary),
		"- Fallback line format: TASK_STATUS:, TASK_SUMMARY:, TASK_BLOCKER:, TASK_HANDOFF:.",
		fmt.Sprintf("- Allowed task_status values: %s.", statusDescription),
		"- Contract rules: summary is required for every status; blocker is required for blocked/failed/handoff; handoff_to is required only for handoff.",
		"- Missing or invalid structured status will be treated as a protocol error when no canonical task outcome tool result was recorded.",
	}
}

// ParseTaskOutcomeContract extracts and validates the teammate outcome contract from output.
func ParseTaskOutcomeContract(output string) (TaskOutcomeContract, error) {
	jsonOutcome, jsonFound, jsonErr := parseTaskOutcomeContractJSON(output)
	if jsonFound && jsonErr == nil {
		return jsonOutcome, nil
	}
	lineOutcome, lineFound, lineErr := parseTaskOutcomeContractLines(output)
	if lineFound && lineErr == nil {
		return lineOutcome, nil
	}
	if jsonFound {
		return TaskOutcomeContract{}, fmt.Errorf("invalid JSON status block: %w", jsonErr)
	}
	if lineFound {
		return TaskOutcomeContract{}, fmt.Errorf("invalid TASK_* status block: %w", lineErr)
	}
	return TaskOutcomeContract{}, fmt.Errorf("missing structured task outcome (expected JSON status block or TASK_* lines)")
}

// ValidateTaskOutcomeContract normalizes and validates a contract value.
func ValidateTaskOutcomeContract(outcome TaskOutcomeContract) (TaskOutcomeContract, error) {
	outcome.Status = TaskOutcomeStatus(strings.ToLower(strings.TrimSpace(string(outcome.Status))))
	outcome.Summary = strings.TrimSpace(outcome.Summary)
	outcome.Blocker = strings.TrimSpace(outcome.Blocker)
	outcome.HandoffTo = strings.TrimSpace(outcome.HandoffTo)

	if !validTaskOutcomeStatus(outcome.Status) {
		return TaskOutcomeContract{}, fmt.Errorf("task_status must be one of done, blocked, failed, handoff")
	}
	if outcome.Summary == "" {
		return TaskOutcomeContract{}, fmt.Errorf("summary is required")
	}
	switch outcome.Status {
	case TaskOutcomeDone:
		if outcome.Blocker != "" {
			return TaskOutcomeContract{}, fmt.Errorf("blocker is only allowed for blocked, failed, or handoff")
		}
		if outcome.HandoffTo != "" {
			return TaskOutcomeContract{}, fmt.Errorf("handoff_to is only allowed when task_status=handoff")
		}
	case TaskOutcomeBlocked, TaskOutcomeFailed:
		if outcome.Blocker == "" {
			return TaskOutcomeContract{}, fmt.Errorf("blocker is required when task_status=%s", outcome.Status)
		}
		if outcome.HandoffTo != "" {
			return TaskOutcomeContract{}, fmt.Errorf("handoff_to is only allowed when task_status=handoff")
		}
	case TaskOutcomeHandoff:
		if outcome.Blocker == "" {
			return TaskOutcomeContract{}, fmt.Errorf("blocker is required when task_status=handoff")
		}
		if outcome.HandoffTo == "" {
			return TaskOutcomeContract{}, fmt.Errorf("handoff_to is required when task_status=handoff")
		}
	}
	return outcome, nil
}

// NormalizeTaskOutcomeContract applies legacy summary-only compatibility or validates an explicit structured contract.
func NormalizeTaskOutcomeContract(defaultStatus TaskOutcomeStatus, outcome TaskOutcomeContract) (TaskOutcomeContract, bool, error) {
	outcome.Status = TaskOutcomeStatus(strings.ToLower(strings.TrimSpace(string(outcome.Status))))
	outcome.Summary = strings.TrimSpace(outcome.Summary)
	outcome.Blocker = strings.TrimSpace(outcome.Blocker)
	outcome.HandoffTo = strings.TrimSpace(outcome.HandoffTo)

	if outcome.Status == "" && outcome.Blocker == "" && outcome.HandoffTo == "" {
		outcome.Status = TaskOutcomeStatus(strings.ToLower(strings.TrimSpace(string(defaultStatus))))
		return outcome, false, nil
	}

	validated, err := ValidateTaskOutcomeContract(outcome)
	if err != nil {
		return TaskOutcomeContract{}, true, err
	}
	return validated, true, nil
}

// ValidateAllowedTaskOutcomeStatus ensures a structured contract only uses statuses valid for the current entrypoint.
func ValidateAllowedTaskOutcomeStatus(outcome TaskOutcomeContract, allowed ...TaskOutcomeStatus) error {
	for _, status := range allowed {
		if outcome.Status == status {
			return nil
		}
	}
	expected := make([]string, 0, len(allowed))
	for _, status := range allowed {
		expected = append(expected, string(status))
	}
	return fmt.Errorf("task_status %q is not allowed for this entrypoint (expected %s)", outcome.Status, strings.Join(expected, " or "))
}

func parseTaskOutcomeContractJSON(output string) (TaskOutcomeContract, bool, error) {
	var parseErr error
	for _, block := range extractTaskOutcomeJSONCodeBlocks(output) {
		if !looksLikeTaskOutcomeContractJSON(block) {
			continue
		}
		outcome, err := decodeTaskOutcomeContractJSON(block)
		if err != nil {
			parseErr = err
			continue
		}
		return outcome, true, nil
	}
	return TaskOutcomeContract{}, parseErr != nil, parseErr
}

func decodeTaskOutcomeContractJSON(block string) (TaskOutcomeContract, error) {
	var outcome TaskOutcomeContract
	decoder := json.NewDecoder(strings.NewReader(block))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&outcome); err != nil {
		return TaskOutcomeContract{}, err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return TaskOutcomeContract{}, fmt.Errorf("unexpected trailing JSON content")
		}
		return TaskOutcomeContract{}, err
	}
	return ValidateTaskOutcomeContract(outcome)
}

func parseTaskOutcomeContractLines(output string) (TaskOutcomeContract, bool, error) {
	var outcome TaskOutcomeContract
	found := false
	seen := map[string]bool{}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "task_") {
			continue
		}
		found = true
		switch {
		case strings.HasPrefix(lower, "task_status:"):
			if seen["task_status"] {
				return TaskOutcomeContract{}, true, fmt.Errorf("task_status may only appear once")
			}
			outcome.Status = TaskOutcomeStatus(strings.ToLower(strings.TrimSpace(trimmed[len("task_status:"):])))
			seen["task_status"] = true
		case strings.HasPrefix(lower, "task_summary:"):
			if seen["task_summary"] {
				return TaskOutcomeContract{}, true, fmt.Errorf("task_summary may only appear once")
			}
			outcome.Summary = strings.TrimSpace(trimmed[len("task_summary:"):])
			seen["task_summary"] = true
		case strings.HasPrefix(lower, "task_blocker:"):
			if seen["task_blocker"] {
				return TaskOutcomeContract{}, true, fmt.Errorf("task_blocker may only appear once")
			}
			outcome.Blocker = strings.TrimSpace(trimmed[len("task_blocker:"):])
			seen["task_blocker"] = true
		case strings.HasPrefix(lower, "task_handoff:"):
			if seen["task_handoff"] {
				return TaskOutcomeContract{}, true, fmt.Errorf("task_handoff may only appear once")
			}
			outcome.HandoffTo = strings.TrimSpace(trimmed[len("task_handoff:"):])
			seen["task_handoff"] = true
		default:
			return TaskOutcomeContract{}, true, fmt.Errorf("unknown TASK_* field %q", trimmed)
		}
	}
	if !found {
		return TaskOutcomeContract{}, false, nil
	}
	validated, err := ValidateTaskOutcomeContract(outcome)
	if err != nil {
		return TaskOutcomeContract{}, true, err
	}
	return validated, true, nil
}

func looksLikeTaskOutcomeContractJSON(block string) bool {
	lower := strings.ToLower(block)
	return strings.Contains(lower, "task_status") ||
		strings.Contains(lower, "blocker") ||
		strings.Contains(lower, "handoff_to")
}

func validTaskOutcomeStatus(status TaskOutcomeStatus) bool {
	switch status {
	case TaskOutcomeDone, TaskOutcomeBlocked, TaskOutcomeFailed, TaskOutcomeHandoff:
		return true
	default:
		return false
	}
}

func extractTaskOutcomeJSONCodeBlocks(output string) []string {
	blocks := make([]string, 0, 2)
	remaining := output
	remainingLower := strings.ToLower(output)
	for {
		start := strings.Index(remainingLower, "```json")
		if start < 0 {
			break
		}
		trimmed := remaining[start+7:]
		trimmedLower := remainingLower[start+7:]
		end := strings.Index(trimmedLower, "```")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(trimmed[:end])
		if block != "" {
			blocks = append(blocks, block)
		}
		remaining = trimmed[end+3:]
		remainingLower = trimmedLower[end+3:]
	}
	return blocks
}

func normalizeAllowedTaskOutcomeStatuses(allowed ...TaskOutcomeStatus) []TaskOutcomeStatus {
	if len(allowed) == 0 {
		return []TaskOutcomeStatus{
			TaskOutcomeDone,
			TaskOutcomeFailed,
			TaskOutcomeBlocked,
			TaskOutcomeHandoff,
		}
	}
	seen := make(map[TaskOutcomeStatus]bool, len(allowed))
	out := make([]TaskOutcomeStatus, 0, len(allowed))
	for _, status := range allowed {
		status = TaskOutcomeStatus(strings.ToLower(strings.TrimSpace(string(status))))
		if !validTaskOutcomeStatus(status) || seen[status] {
			continue
		}
		seen[status] = true
		out = append(out, status)
	}
	if len(out) == 0 {
		return normalizeAllowedTaskOutcomeStatuses()
	}
	return out
}

func taskOutcomeStatusEnumValues(allowed ...TaskOutcomeStatus) []string {
	statuses := normalizeAllowedTaskOutcomeStatuses(allowed...)
	values := make([]string, 0, len(statuses))
	for _, status := range statuses {
		values = append(values, string(status))
	}
	return values
}

func joinTaskOutcomeStatuses(allowed ...TaskOutcomeStatus) string {
	statuses := taskOutcomeStatusEnumValues(allowed...)
	switch len(statuses) {
	case 0:
		return ""
	case 1:
		return statuses[0]
	case 2:
		return statuses[0] + " or " + statuses[1]
	default:
		head := strings.Join(statuses[:len(statuses)-1], ", ")
		return head + ", or " + statuses[len(statuses)-1]
	}
}
