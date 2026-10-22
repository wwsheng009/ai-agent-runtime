package runtimeapi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// supervisionItemDetail wires the *business* payload of a lifecycle row into the
// injected preflight text: what a background command actually printed, or what a
// child agent actually produced. The control-plane rendering
// (Digest.DiagnosticsText) keeps the machine handles; this hook only adds the
// content the parent must reason about.
//
// A host with neither a background manager nor a batch store returns nil, so the
// injected text stays free of detail lines.
func (h *Handler) supervisionItemDetail(sessionID, rootScopeID string) supervision.ItemDetailFunc {
	if h == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" {
		rootScopeID = sessionID
	}
	batchStore := h.getSubagentBatchStore()
	h.backgroundMu.Lock()
	backgroundManager := h.backgroundManager
	h.backgroundMu.Unlock()
	if batchStore == nil && backgroundManager == nil {
		return nil
	}
	return func(ctx context.Context, n supervision.Notification) string {
		subjectID := strings.TrimSpace(n.SubjectID)
		if subjectID == "" {
			return ""
		}
		switch n.SubjectKind {
		case supervision.SubjectJob:
			if backgroundManager == nil {
				return ""
			}
			tail, ok := backgroundManager.ReadOutputTail(ctx, subjectID, 0)
			if !ok {
				return ""
			}
			return "输出末尾：\n" + tail
		case supervision.SubjectAgentSession, supervision.SubjectAgentRun:
			if batchStore == nil || sessionID == "" {
				return ""
			}
			return agentResultDetailForSession(ctx, batchStore, sessionID, subjectID)
		default:
			return ""
		}
	}
}

// agentResultDetailForSession finds the durable task capsule that belongs to the
// child session and renders the child's own words. Scope is the parent session,
// so a notification can never pull another parent's result into the prompt.
// Best-effort: a read failure simply renders no detail line.
func agentResultDetailForSession(ctx context.Context, store subagentbatch.BatchStore, parentSessionID, childSessionID string) string {
	batches, err := store.ListBatches(ctx, subagentbatch.BatchFilter{
		ParentSessionID: strings.TrimSpace(parentSessionID),
		Limit:           maxDetailBatches,
	})
	if err != nil {
		return ""
	}
	for _, batch := range batches {
		tasks, err := store.ListTasks(ctx, batch.BatchID)
		if err != nil {
			continue
		}
		for _, task := range tasks {
			capsule := decodeTaskCapsule(task.ResultSummary)
			if !strings.EqualFold(strings.TrimSpace(task.ChildSessionID), childSessionID) &&
				!strings.EqualFold(capsule.SessionID, childSessionID) {
				continue
			}
			return formatTaskCapsuleDetail(capsule)
		}
	}
	return ""
}

// maxDetailBatches bounds the per-notification scan. A parent normally owns a
// handful of batches; the cap only protects a pathological history.
const maxDetailBatches = 32

// taskCapsule is the subset of a durable result capsule the injected text uses.
// Both the TaskResult shape and a projected agentresult.Result decode into it.
type taskCapsule struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Summary   string `json:"summary"`
	Error     string `json:"error"`
	Success   bool   `json:"success"`
}

func decodeTaskCapsule(raw []byte) taskCapsule {
	if len(raw) == 0 {
		return taskCapsule{}
	}
	var capsule taskCapsule
	if err := json.Unmarshal(raw, &capsule); err != nil {
		return taskCapsule{}
	}
	return capsule
}

// formatTaskCapsuleDetail renders the child's outcome. Machine fields (usage,
// artifact refs, error classes) stay behind subagent_inspect_task.
func formatTaskCapsuleDetail(capsule taskCapsule) string {
	var b strings.Builder
	if summary := strings.TrimSpace(capsule.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	}
	if message := strings.TrimSpace(capsule.Error); message != "" {
		b.WriteString("失败原因：" + message + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
