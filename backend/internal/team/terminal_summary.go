package team

import (
	"sort"
	"strings"
)

// TerminalTaskOutcome is the durable, retry-oriented task view attached to a
// terminal team summary.
type TerminalTaskOutcome struct {
	TaskID     string     `json:"task_id"`
	Title      string     `json:"title,omitempty"`
	Status     TaskStatus `json:"status"`
	Summary    string     `json:"summary,omitempty"`
	ResultRef  string     `json:"result_ref,omitempty"`
	RetryCount int        `json:"retry_count,omitempty"`
	Retryable  bool       `json:"retryable,omitempty"`
}

type terminalOutcomeSnapshot struct {
	Counts     map[string]int
	Successful []TerminalTaskOutcome
	Failed     []TerminalTaskOutcome
	Canceled   []TerminalTaskOutcome
	Retryable  []TerminalTaskOutcome
}

func terminalStatusForTasks(tasks []Task) TeamStatus {
	done, failed, canceled := 0, 0, 0
	for _, task := range tasks {
		switch task.Status {
		case TaskStatusDone:
			done++
		case TaskStatusFailed:
			failed++
		case TaskStatusCancelled:
			canceled++
		}
	}
	return terminalStatusFromCounts(done, failed, canceled, len(tasks))
}

func terminalStatusFromCounts(done, failed, canceled, total int) TeamStatus {
	switch {
	case total > 0 && done == total:
		return TeamStatusDone
	case done > 0:
		return TeamStatusPartiallyCompleted
	case failed > 0:
		return TeamStatusFailed
	case canceled > 0:
		return TeamStatusCanceled
	default:
		return TeamStatusFailed
	}
}

func buildTerminalOutcomeSnapshot(tasks []Task) terminalOutcomeSnapshot {
	snapshot := terminalOutcomeSnapshot{Counts: make(map[string]int)}
	ordered := append([]Task(nil), tasks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.TrimSpace(ordered[i].ID) < strings.TrimSpace(ordered[j].ID)
	})
	for _, task := range ordered {
		status := string(task.Status)
		snapshot.Counts[status]++
		outcome := TerminalTaskOutcome{
			TaskID:     strings.TrimSpace(task.ID),
			Title:      firstNonEmptyString(task.Title, task.Goal, task.ID),
			Status:     task.Status,
			Summary:    strings.TrimSpace(task.Summary),
			RetryCount: task.RetryCount,
			Retryable:  task.Status == TaskStatusFailed,
		}
		if task.ResultRef != nil {
			outcome.ResultRef = strings.TrimSpace(*task.ResultRef)
		}
		switch task.Status {
		case TaskStatusDone:
			snapshot.Successful = append(snapshot.Successful, outcome)
		case TaskStatusFailed:
			snapshot.Failed = append(snapshot.Failed, outcome)
			snapshot.Retryable = append(snapshot.Retryable, outcome)
		case TaskStatusCancelled:
			snapshot.Canceled = append(snapshot.Canceled, outcome)
		}
	}
	return snapshot
}

func appendTerminalOutcomePayload(payload map[string]interface{}, status TeamStatus, tasks []Task) {
	if payload == nil {
		return
	}
	snapshot := buildTerminalOutcomeSnapshot(tasks)
	payload["status"] = string(status)
	payload["task_counts"] = snapshot.Counts
	payload["successful_tasks"] = snapshot.Successful
	payload["failed_tasks"] = snapshot.Failed
	payload["canceled_tasks"] = snapshot.Canceled
	payload["retryable_tasks"] = snapshot.Retryable
	summary, _ := payload["summary"].(string)
	payload["result_contract"] = buildTeamResultContract(status, tasks, summary)
}
