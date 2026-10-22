package commands

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// newLocalSupervisionItemDetail projects the *business* payload of a lifecycle
// row for the injected preflight text: what a background command actually
// printed, or what a child agent actually produced. Control-plane handles stay
// out of it — they belong to the diagnostics rendering and the tools.
//
// A nil host/background/results plane returns nil, which keeps the injected
// text free of detail lines (the pre-existing behavior).
func (h *localChatRuntimeHost) newLocalSupervisionItemDetail(sessionID, rootScopeID string) supervision.ItemDetailFunc {
	if h == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	rootScopeID = strings.TrimSpace(rootScopeID)
	if sessionID == "" {
		return nil
	}
	var results supervision.ResultSource
	if h.SubagentBatches != nil {
		results = runtimeserver.NewSupervisionResultSource(h.SubagentBatches, localCompletionMailboxReader(h))
	}
	background := h.Background
	if background == nil && results == nil {
		return nil
	}
	return func(ctx context.Context, n supervision.Notification) string {
		subjectID := strings.TrimSpace(n.SubjectID)
		if subjectID == "" {
			return ""
		}
		switch n.SubjectKind {
		case supervision.SubjectJob:
			if background == nil {
				return ""
			}
			tail, ok := background.ReadOutputTail(ctx, subjectID, 0)
			if !ok {
				return ""
			}
			return "输出末尾：\n" + tail
		case supervision.SubjectAgentSession, supervision.SubjectAgentRun:
			if results == nil {
				return ""
			}
			record, found, err := results.LoadAgentResult(ctx, supervision.Scope{RootSessionID: rootScopeID}, subjectID, "")
			if err != nil || !found {
				return ""
			}
			return formatLocalAgentResultDetail(record)
		default:
			return ""
		}
	}
}

// formatLocalAgentResultDetail renders the child's own words, not the ledger:
// its result summary plus the failure reason when it did not succeed.
// Remaining machine fields (usage, refs, error classes) stay behind
// subagent_inspect_task.
func formatLocalAgentResultDetail(record supervision.AgentResultRecord) string {
	var b strings.Builder
	status := strings.TrimSpace(record.Status)
	if status != "" {
		b.WriteString("结果状态：" + status + "\n")
	}
	if summary := strings.TrimSpace(record.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	}
	for _, item := range record.Errors {
		message := strings.TrimSpace(item.Message)
		if message == "" {
			continue
		}
		b.WriteString("失败原因：" + message + "\n")
		break
	}
	if record.PartialProduct != nil {
		if summary := strings.TrimSpace(record.PartialProduct.Summary); summary != "" {
			b.WriteString("中断时已完成：\n" + summary + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
