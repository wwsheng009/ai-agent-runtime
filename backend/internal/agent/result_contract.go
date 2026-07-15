package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func ensureAgentResultContract(result *Result, goal string) *agentresult.Result {
	if result == nil {
		return nil
	}
	if result.Contract != nil {
		return result.Contract
	}
	usage := agentresult.Usage{ToolCalls: len(result.Observations)}
	if result.Usage != nil {
		usage.InputTokens = result.Usage.PromptTokens
		usage.OutputTokens = result.Usage.CompletionTokens
		usage.TotalTokens = result.Usage.TotalTokens
	}
	usage.DurationMS = result.Duration.GetDuration().Milliseconds()
	contract := agentresult.FromLegacy(result.Success, result.Output, resultErrorCode(result.Error), result.Error, usage)
	applyContractErrorStatus(contract, result.Error)
	contract.TraceID = strings.TrimSpace(result.TraceID)

	for index, observation := range result.Observations {
		refs := observationEvidenceRefs(observation)
		agentresult.MergeEvidence(contract, refs...)
		if observation.Success {
			if summary := observationSummary(observation); summary != "" {
				contract.Findings = append(contract.Findings, agentresult.Finding{
					ID:           fmt.Sprintf("finding_%d", index+1),
					Summary:      summary,
					EvidenceRefs: append([]string(nil), refs...),
				})
			}
			continue
		}
		message := strings.TrimSpace(observation.Error)
		if message == "" {
			continue
		}
		contract.Errors = appendUniqueContractError(contract.Errors, agentresult.Error{
			Code:         observationErrorCode(observation),
			Message:      message,
			Retryable:    observationRetryable(observation),
			EvidenceRefs: append([]string(nil), refs...),
		})
	}
	if !result.Success && strings.TrimSpace(goal) != "" {
		contract.RemainingWork = []string{strings.TrimSpace(goal)}
	}
	if !result.Success && len(contract.Findings) > 0 {
		contract.Status = agentresult.StatusPartiallyCompleted
	}
	result.Contract = contract
	return contract
}

func ensureSubagentResultContract(report *SubagentResult, task SubagentTask) *agentresult.Result {
	if report == nil {
		return nil
	}
	contract := report.Contract
	if contract == nil {
		usage := agentresult.Usage{}
		if report.Usage != nil {
			usage.InputTokens = report.Usage.PromptTokens
			usage.OutputTokens = report.Usage.CompletionTokens
			usage.TotalTokens = report.Usage.TotalTokens
		}
		contract = agentresult.FromLegacy(report.Success, report.Summary, resultErrorCode(report.Error), report.Error, usage)
		applyContractErrorStatus(contract, report.Error)
	}
	for index, finding := range report.Findings {
		finding = strings.TrimSpace(finding)
		if finding == "" {
			continue
		}
		contract.Findings = appendUniqueContractFinding(contract.Findings, agentresult.Finding{
			ID:      fmt.Sprintf("finding_%d", index+1),
			Summary: finding,
		})
	}
	for _, patch := range report.Patches {
		contract.Changes = append(contract.Changes, agentresult.Change{
			Path:         strings.TrimSpace(patch.Path),
			Summary:      strings.TrimSpace(patch.Summary),
			Status:       strings.TrimSpace(patch.ApplyStatus),
			ArtifactRefs: append([]string(nil), patch.ArtifactRefs...),
			EvidenceRefs: append([]string(nil), patch.ArtifactRefs...),
		})
		agentresult.MergeEvidence(contract, patch.ArtifactRefs...)
	}
	if !report.Success && len(contract.RemainingWork) == 0 && strings.TrimSpace(task.Goal) != "" {
		contract.RemainingWork = []string{strings.TrimSpace(task.Goal)}
	}
	if !report.Success && (len(contract.Findings) > 0 || len(contract.Changes) > 0) {
		contract.Status = agentresult.StatusPartiallyCompleted
	}
	report.Contract = contract
	return contract
}

func appendUniqueContractFinding(items []agentresult.Finding, candidate agentresult.Finding) []agentresult.Finding {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Summary), strings.TrimSpace(candidate.Summary)) {
			return items
		}
	}
	return append(items, candidate)
}

func resultErrorCode(message string) string {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(lower, context.DeadlineExceeded.Error()), strings.Contains(lower, "timeout"):
		return "TOOL_TIMEOUT"
	case strings.Contains(lower, context.Canceled.Error()), strings.Contains(lower, "cancelled"):
		return "AGENT_RUN_CANCELED"
	default:
		return ""
	}
}

func applyContractErrorStatus(contract *agentresult.Result, message string) {
	if contract == nil || contract.Status == agentresult.StatusSucceeded {
		return
	}
	switch resultErrorCode(message) {
	case "TOOL_TIMEOUT":
		contract.Status = agentresult.StatusTimedOut
	case "AGENT_RUN_CANCELED":
		contract.Status = agentresult.StatusCanceled
	}
}

func observationSummary(observation types.Observation) string {
	text := strings.TrimSpace(contractValueText(observation.Output))
	if text == "" {
		text = strings.TrimSpace(observation.Tool)
	}
	return truncateContractText(text, 300)
}

func contractValueText(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		payload, err := json.Marshal(value)
		if err == nil {
			return string(payload)
		}
		return fmt.Sprintf("%v", value)
	}
}

func observationEvidenceRefs(observation types.Observation) []string {
	refs := make([]string, 0, 4)
	for _, key := range []string{"evidence_refs", "artifact_refs", "source_refs", "event_id"} {
		refs = append(refs, contractStringValues(observation.Metrics[key])...)
	}
	return dedupeContractStrings(refs)
}

func observationErrorCode(observation types.Observation) string {
	if value, ok := observation.Metrics["error_code"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return resultErrorCode(observation.Error)
}

func observationRetryable(observation types.Observation) bool {
	value, _ := observation.Metrics["retryable"].(bool)
	return value
}

func appendUniqueContractError(items []agentresult.Error, candidate agentresult.Error) []agentresult.Error {
	for _, item := range items {
		if item.Code == candidate.Code && item.Message == candidate.Message {
			return items
		}
	}
	return append(items, candidate)
}

func contractStringValues(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func dedupeContractStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func truncateContractText(value string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if limit <= 0 || len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-3]) + "..."
}
