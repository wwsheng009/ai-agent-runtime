package commands

import (
	"context"
	"encoding/json"
	"strings"

	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func reconcileGoalCompletionFromToolMessages(session *ChatSession) (bool, error) {
	if session == nil || session.RuntimeSession == nil {
		return false, nil
	}
	// P2-1（docs/plan/aicli-stream-pipeline-performance-audit-and-optimization-plan-20261011.md §4）：
	// 全量扫描对每条 tool 消息做 json.Unmarshal，成本随会话历史线性增长且
	// 每个 turn 收尾都付一遍。改为增量：append-only 前缀的扫描结果由游标 +
	// lastCompleted 携带，fingerprint 失配（compact/restore/backtrack 整体
	// 替换列表）才退化为全量重扫——last-wins 语义与全量扫描严格等价。
	fingerprint := chatGoalReconcileFingerprint{length: len(session.Messages)}
	if n := len(session.Messages); n > 0 {
		last := session.Messages[n-1]
		fingerprint.lastRole = strings.TrimSpace(last.Role)
		fingerprint.lastContentSize = len(last.Content)
	}
	var completed *runtimegoal.SessionGoal
	scanFrom := 0
	if fingerprint == session.goalReconcileFingerprint {
		completed = session.goalReconcileLastCompleted
		scanFrom = session.goalReconcileScanned
		if scanFrom > len(session.Messages) {
			scanFrom = 0
			completed = nil
		}
	}
	for i := scanFrom; i < len(session.Messages); i++ {
		message := session.Messages[i]
		if strings.TrimSpace(message.Role) != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		var payload struct {
			Goal *runtimegoal.SessionGoal `json:"goal"`
		}
		if err := json.Unmarshal([]byte(message.Content), &payload); err != nil {
			continue
		}
		if payload.Goal == nil || payload.Goal.Status != runtimegoal.StatusComplete {
			continue
		}
		if strings.TrimSpace(payload.Goal.CompletedBy) != "model" {
			continue
		}
		completed = payload.Goal
	}
	session.goalReconcileScanned = len(session.Messages)
	session.goalReconcileFingerprint = fingerprint
	session.goalReconcileLastCompleted = completed
	if completed == nil {
		return false, nil
	}
	store := runtimegoal.NewMetadataStore()
	if session.SessionManager != nil && session.SessionManager.GetStorage() != nil {
		updated, err := store.PutPersistent(context.Background(), session.SessionManager.GetStorage(), session.RuntimeSession.ID, *completed, runtimegoal.MutationModel)
		if err != nil {
			return false, err
		}
		if err := restoreChatStateFromRuntimeSession(session, updated); err != nil {
			return false, err
		}
	} else if err := store.Put(session.RuntimeSession, *completed); err != nil {
		return false, err
	}
	ensureChatSystemPromptMessage(session)
	return true, nil
}
