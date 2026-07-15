package team

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
)

func ensureFinalSummaryContract(result *FinalSummaryResult) *agentresult.Result {
	if result == nil {
		return nil
	}
	if result.Contract != nil {
		return result.Contract
	}
	success := !result.HasSessionError()
	contract := agentresult.FromLegacy(success, result.Summary, result.ErrorType, sessionErrorText(result.SessionError), agentresult.Usage{})
	contract.TraceID = strings.TrimSpace(result.TraceID)
	if result.UsedFallback && !success {
		contract.Status = agentresult.StatusPartiallyCompleted
	}
	result.Contract = contract
	return contract
}

func buildTeamResultContract(status TeamStatus, tasks []Task, summary string) *agentresult.Result {
	contract := &agentresult.Result{Status: teamContractStatus(status), Summary: strings.TrimSpace(summary)}
	if contract.Summary == "" {
		contract.Summary = "Team reached terminal state " + string(status) + "."
	}
	for _, task := range tasks {
		ref := ""
		if task.ResultRef != nil {
			ref = strings.TrimSpace(*task.ResultRef)
		}
		evidenceRefs := []string(nil)
		if ref != "" {
			evidenceRefs = []string{ref}
			agentresult.MergeEvidence(contract, ref)
			contract.Artifacts = append(contract.Artifacts, agentresult.Artifact{ID: ref, Kind: "team_task_result"})
		}
		summary := strings.TrimSpace(task.Summary)
		if summary == "" {
			summary = strings.TrimSpace(firstNonEmptyString(task.Title, task.Goal, task.ID))
		}
		switch task.Status {
		case TaskStatusDone:
			contract.Findings = append(contract.Findings, agentresult.Finding{ID: task.ID, Summary: summary, EvidenceRefs: evidenceRefs})
		case TaskStatusFailed:
			contract.Errors = append(contract.Errors, agentresult.Error{Code: "TEAM_TASK_FAILED", Message: summary, Retryable: true, EvidenceRefs: evidenceRefs})
			contract.RemainingWork = append(contract.RemainingWork, strings.TrimSpace(firstNonEmptyString(task.Title, task.Goal, task.ID)))
		case TaskStatusCancelled, TaskStatusBlocked, TaskStatusPending, TaskStatusReady, TaskStatusRunning:
			contract.RemainingWork = append(contract.RemainingWork, strings.TrimSpace(firstNonEmptyString(task.Title, task.Goal, task.ID)))
		}
	}
	return contract
}

func ensureTaskOutcomeResultContract(outcome TaskOutcomeContract) *agentresult.Result {
	if outcome.ResultContract != nil {
		return outcome.ResultContract
	}
	status := agentresult.StatusSucceeded
	switch outcome.Status {
	case TaskOutcomeBlocked:
		status = agentresult.StatusBlocked
	case TaskOutcomeFailed:
		status = agentresult.StatusFailed
	case TaskOutcomeHandoff:
		status = agentresult.StatusPartiallyCompleted
	}
	contract := &agentresult.Result{Status: status, Summary: strings.TrimSpace(outcome.Summary)}
	if contract.Summary == "" {
		contract.Summary = strings.TrimSpace(outcome.Blocker)
	}
	if contract.Summary == "" {
		contract.Summary = "Team task reached " + string(outcome.Status) + "."
	}
	if blocker := strings.TrimSpace(outcome.Blocker); blocker != "" {
		contract.Errors = []agentresult.Error{{Code: "TEAM_TASK_" + strings.ToUpper(string(outcome.Status)), Message: blocker, Retryable: outcome.Status != TaskOutcomeFailed}}
		contract.RemainingWork = []string{blocker}
	}
	if handoff := strings.TrimSpace(outcome.HandoffTo); handoff != "" {
		contract.RemainingWork = append(contract.RemainingWork, "Handoff to "+handoff)
	}
	return contract
}

func teamContractStatus(status TeamStatus) agentresult.Status {
	switch status {
	case TeamStatusDone:
		return agentresult.StatusSucceeded
	case TeamStatusPartiallyCompleted:
		return agentresult.StatusPartiallyCompleted
	case TeamStatusCanceled:
		return agentresult.StatusCanceled
	default:
		return agentresult.StatusFailed
	}
}

func sessionErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
