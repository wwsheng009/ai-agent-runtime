package chat

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextreconcile"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func (a *SessionActor) reconcileCompactResult(ctx context.Context, session *Session, result *compactruntime.Result) {
	if a == nil || session == nil || result == nil || len(result.ReplacementHistory) == 0 {
		return
	}
	snapshot := a.compactCanonicalSnapshot(ctx, session)
	replacement, report := contextreconcile.Reconcile(result.ReplacementHistory, snapshot)
	result.ReplacementHistory = replacement
	result.Reconciliation = &report
	if a.llmRuntime != nil {
		result.TokenAfter = a.llmRuntime.CountMessagesTokens(replacement)
	}
}

func (a *SessionActor) compactCanonicalSnapshot(ctx context.Context, session *Session) contextreconcile.Snapshot {
	snapshot := CanonicalContextSnapshot(session)
	state := a.State()
	if state != nil {
		if state.Status != SessionIdle && state.Status != SessionStopped {
			snapshot.Run.Status = string(state.Status)
			snapshot.Run.TurnID = strings.TrimSpace(state.CurrentTurnID)
		}
		snapshot.Run.PendingApproval = state.PendingApproval != nil
		snapshot.Run.PendingQuestion = state.PendingQuestion != nil
		snapshot.Run.PendingTool = state.PendingTool != nil
		runMeta := state.CurrentRunMeta
		if runMeta == nil {
			runMeta = state.AmbientRunMeta
		}
		if runMeta != nil && runMeta.Team != nil {
			snapshot.Run.TeamID = strings.TrimSpace(runMeta.Team.TeamID)
			snapshot.Run.TaskID = strings.TrimSpace(runMeta.Team.CurrentTaskID)
		}
		snapshot.Jobs = a.backgroundJobSnapshots(ctx, state.ActiveJobIDs)
		for _, job := range snapshot.Jobs {
			snapshot.EvidenceRefs = append(snapshot.EvidenceRefs, "job:"+job.JobID)
			if job.ArtifactRef != "" {
				snapshot.EvidenceRefs = append(snapshot.EvidenceRefs, job.ArtifactRef)
			}
		}
	}
	return snapshot
}

// CanonicalContextSnapshot extracts goal/todo state available directly from a
// durable chat session. Actor callers enrich it with run and job projections.
func CanonicalContextSnapshot(session *Session) contextreconcile.Snapshot {
	if session == nil {
		return contextreconcile.Snapshot{}
	}
	snapshot := contextreconcile.Snapshot{SessionID: strings.TrimSpace(session.ID)}
	if raw, ok := session.GetContext("aicli.goal"); ok && raw != nil {
		payload, _ := json.Marshal(raw)
		var goal contextreconcile.GoalSnapshot
		if json.Unmarshal(payload, &goal) == nil && strings.TrimSpace(goal.GoalID) != "" {
			snapshot.Goal = &goal
			snapshot.EvidenceRefs = append(snapshot.EvidenceRefs, "session:"+session.ID+"#goal:"+goal.GoalID)
		}
	}
	goalID := ""
	if snapshot.Goal != nil {
		goalID = snapshot.Goal.GoalID
	}
	snapshot.Todos = latestTodoSnapshot(session.GetMessages(), goalID)
	return snapshot
}

func latestTodoSnapshot(messages []runtimetypes.Message, goalID string) []contextreconcile.TodoSnapshot {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		owner, _ := message.Metadata["goal_id"].(string)
		if strings.TrimSpace(owner) != strings.TrimSpace(goalID) {
			continue
		}
		raw, ok := message.Metadata["todos"]
		if !ok {
			continue
		}
		payload, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var todos []contextreconcile.TodoSnapshot
		if json.Unmarshal(payload, &todos) == nil {
			return todos
		}
	}
	return nil
}

func (a *SessionActor) backgroundJobSnapshots(ctx context.Context, jobIDs []string) []contextreconcile.JobSnapshot {
	if a == nil || a.agent == nil || len(jobIDs) == 0 {
		return nil
	}
	broker := a.agent.GetToolBroker()
	if broker == nil || broker.Background == nil {
		return nil
	}
	jobs := make([]contextreconcile.JobSnapshot, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			continue
		}
		job, err := broker.Background.GetJob(ctx, jobID)
		if err != nil || job == nil {
			jobs = append(jobs, contextreconcile.JobSnapshot{JobID: jobID, Status: "unknown", ErrorCode: "JOB_NOT_FOUND"})
			continue
		}
		snapshot := contextreconcile.JobSnapshot{JobID: job.ID, Status: string(job.Status)}
		if value, ok := job.Metadata["error_code"].(string); ok {
			snapshot.ErrorCode = strings.TrimSpace(value)
		}
		if value, ok := job.Metadata["retryable"].(bool); ok {
			snapshot.Retryable = value
		}
		for _, key := range []string{"artifact_ref", "result_ref"} {
			if value, ok := job.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				snapshot.ArtifactRef = strings.TrimSpace(value)
				break
			}
		}
		jobs = append(jobs, snapshot)
	}
	return jobs
}

func (a *SessionActor) publishCompactReconciliation(traceID string, result *compactruntime.Result) {
	if a == nil || result == nil || result.Reconciliation == nil {
		return
	}
	report := result.Reconciliation
	a.publish(runtimeevents.Event{
		Type: EventContextReconciled, SessionID: a.id, TraceID: traceID,
		Payload: map[string]interface{}{
			"drift_count": report.DriftCount, "correction_made": report.CorrectionMade,
			"corrections": report.Corrections, "evidence_refs": report.EvidenceRefs,
		},
	})
}
