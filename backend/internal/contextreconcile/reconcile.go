package contextreconcile

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type GoalSnapshot struct {
	GoalID    string `json:"goal_id"`
	Objective string `json:"objective,omitempty"`
	Status    string `json:"status,omitempty"`
}

type TodoSnapshot struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type RunSnapshot struct {
	Status          string `json:"status,omitempty"`
	TurnID          string `json:"turn_id,omitempty"`
	PendingApproval bool   `json:"pending_approval,omitempty"`
	PendingQuestion bool   `json:"pending_question,omitempty"`
	PendingTool     bool   `json:"pending_tool,omitempty"`
	TeamID          string `json:"team_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
}

type JobSnapshot struct {
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	ErrorCode   string `json:"error_code,omitempty"`
	Retryable   bool   `json:"retryable,omitempty"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
}

type Snapshot struct {
	SessionID    string         `json:"session_id"`
	Goal         *GoalSnapshot  `json:"goal,omitempty"`
	Todos        []TodoSnapshot `json:"todos,omitempty"`
	Run          RunSnapshot    `json:"run"`
	Jobs         []JobSnapshot  `json:"jobs,omitempty"`
	EvidenceRefs []string       `json:"evidence_refs,omitempty"`
}

type Correction struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type Report struct {
	DriftCount     int          `json:"drift_count"`
	Corrections    []Correction `json:"corrections,omitempty"`
	CorrectionMade bool         `json:"correction_made"`
	EvidenceRefs   []string     `json:"evidence_refs,omitempty"`
}

// Reconcile appends an authoritative correction message when compacted prose
// no longer carries durable goal, todo, run, or job state.
func Reconcile(replacement []types.Message, snapshot Snapshot) ([]types.Message, Report) {
	result := cloneMessages(replacement)
	report := Report{EvidenceRefs: dedupe(snapshot.EvidenceRefs)}
	text, goalIDs := replacementIndex(result)

	if snapshot.Goal != nil && strings.TrimSpace(snapshot.Goal.GoalID) != "" {
		goalID := strings.TrimSpace(snapshot.Goal.GoalID)
		if !strings.Contains(text, strings.ToLower(goalID)) && !goalIDs[goalID] {
			report.Corrections = append(report.Corrections, Correction{Code: "ACTIVE_GOAL_MISSING", Detail: goalID})
		}
		for indexedGoalID := range goalIDs {
			if indexedGoalID != "" && indexedGoalID != goalID {
				report.Corrections = append(report.Corrections, Correction{Code: "STALE_GOAL_SCOPE", Detail: indexedGoalID})
			}
		}
	}
	if len(snapshot.Todos) > 0 && !replacementHasStage(result, "todo_state") {
		report.Corrections = append(report.Corrections, Correction{Code: "TODO_STATE_MISSING", Detail: fmt.Sprintf("count=%d", len(snapshot.Todos))})
	}
	if runHasState(snapshot.Run) && !replacementHasStage(result, "active_execution") {
		report.Corrections = append(report.Corrections, Correction{Code: "RUN_STATE_MISSING", Detail: snapshot.Run.Status})
	}
	for _, job := range snapshot.Jobs {
		if strings.TrimSpace(job.JobID) != "" && !strings.Contains(text, strings.ToLower(job.JobID)) {
			report.Corrections = append(report.Corrections, Correction{Code: "BACKGROUND_JOB_MISSING", Detail: job.JobID})
		}
	}
	report.Corrections = dedupeCorrections(report.Corrections)
	report.DriftCount = len(report.Corrections)
	if report.DriftCount == 0 {
		return result, report
	}

	payload, _ := json.Marshal(snapshot)
	message := types.NewAssistantMessage("Authoritative context correction:\n" + string(payload))
	message.Metadata["context_stage"] = "correction"
	message.Metadata["drift_count"] = report.DriftCount
	message.Metadata["corrections"] = report.Corrections
	message.Metadata["evidence_refs"] = report.EvidenceRefs
	result = appendCorrectionMessage(result, *message)
	report.CorrectionMade = true
	return result, report
}

// appendCorrectionMessage injects the correction as context, not as a turn
// boundary. When the replacement still ends with the active user turn (a real
// user message without a context stage), the correction is inserted before it:
// callers detect "prompt already in history" by the trailing user message
// (ReActLoop.RunWithSession), and a correction appended after it made the loop
// re-append the prompt of the open turn. Live 2026-10-10 (session
// session_20261010201220_MvgmioqR): pre-turn auto compact retained the active
// 50K-char prompt, RUN_STATE_MISSING appended a correction after it, and the
// provider request then carried that prompt twice (~95K prompt tokens).
func appendCorrectionMessage(messages []types.Message, correction types.Message) []types.Message {
	insertAt := len(messages)
	if insertAt > 0 {
		last := messages[insertAt-1]
		if strings.EqualFold(strings.TrimSpace(last.Role), "user") &&
			strings.TrimSpace(last.Metadata.GetString("context_stage", "")) == "" {
			insertAt--
		}
	}
	corrected := make([]types.Message, 0, len(messages)+1)
	corrected = append(corrected, messages[:insertAt]...)
	corrected = append(corrected, correction)
	corrected = append(corrected, messages[insertAt:]...)
	return corrected
}

func runHasState(run RunSnapshot) bool {
	return strings.TrimSpace(run.Status) != "" || strings.TrimSpace(run.TurnID) != "" ||
		run.PendingApproval || run.PendingQuestion || run.PendingTool ||
		strings.TrimSpace(run.TeamID) != "" || strings.TrimSpace(run.TaskID) != ""
}
