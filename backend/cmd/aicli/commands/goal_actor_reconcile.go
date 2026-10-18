package commands

import (
	"context"
	"encoding/json"
	"strings"
	"time"

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
	requestGoalTerminalWrapUp(session, completed)
	return true, nil
}

// requestGoalTerminalWrapUp 在模型把 goal 置为终态（complete/blocked）后暂存
// 收尾轮提示词（G3，DSH goal wrapup 借鉴）。CLI 自动续跑循环在下一轮决策前
// 消费；仅模型终态触发（用户 /goal complete 有自己的确认输出，不需要收尾轮）。
// prompt 为空表示无需收尾（防御：未知终态）。
func requestGoalTerminalWrapUp(session *ChatSession, goal *runtimegoal.SessionGoal) {
	if session == nil || goal == nil {
		return
	}
	var prompt string
	switch goal.Status {
	case runtimegoal.StatusComplete:
		prompt = runtimegoal.CompleteWrapUpPrompt
	case runtimegoal.StatusBlocked:
		prompt = runtimegoal.BlockedWrapUpPrompt
	default:
		return
	}
	// 终态去重（2026-10-18 真机）：reconcile 的增量扫描在 goal 保持终态期间
	// 每轮都会重新命中同一条 complete/blocked 记录；旧实现每次都重新 Store，
	// 使下一个前台轮收尾的决策路径反复消费并补跑收尾轮（每发一条消息多一个
	// 隐藏轮）。按“终态标识”只触发一次；resume 后再次终态（updated_at 前移）
	// 视为新的终态，允许再次收尾。
	key := goalTerminalWrapUpKey(goal)
	if key != "" {
		if existing, _ := session.goalTerminalWrapUpKey.Load().(string); existing == key {
			return
		}
		session.goalTerminalWrapUpKey.Store(key)
	}
	session.goalTerminalWrapUpPrompt.Store(prompt)
	writeSessionDebugInfo(session, "[goal] terminal wrap-up requested status="+string(goal.Status), false)
}

// goalTerminalWrapUpKey 构造终态去重标识：goal_id | status | updated_at。
// updated_at 优先取 CompletedAt（较晚者），保证同一 goal 经 resume 重新
// 终态后 key 变化、可再次触发收尾。
func goalTerminalWrapUpKey(goal *runtimegoal.SessionGoal) string {
	if goal == nil {
		return ""
	}
	updated := goal.UpdatedAt
	if goal.CompletedAt != nil && goal.CompletedAt.After(updated) {
		updated = *goal.CompletedAt
	}
	return strings.TrimSpace(goal.GoalID) + "|" + string(goal.Status) + "|" + updated.UTC().Format(time.RFC3339Nano)
}

// consumeGoalTerminalWrapUp 取走暂存的收尾轮提示词（一次性）；空串表示无。
func consumeGoalTerminalWrapUp(session *ChatSession) string {
	if session == nil {
		return ""
	}
	prompt, _ := session.goalTerminalWrapUpPrompt.Load().(string)
	if prompt != "" {
		session.goalTerminalWrapUpPrompt.Store("")
	}
	return prompt
}
