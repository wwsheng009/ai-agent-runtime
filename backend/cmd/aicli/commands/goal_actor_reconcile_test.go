package commands

import (
	"encoding/json"
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P2-1：goal 完成度 reconciler 的增量扫描语义钉子——last-wins 与全量扫描
// 等价；fingerprint 失配（列表整体替换）退化为全量重扫。
func newGoalReconcileTestSession() *ChatSession {
	return &ChatSession{
		RuntimeSession: &runtimechat.Session{},
	}
}

func goalCompletedToolMessage(objective string) runtimetypes.Message {
	content, _ := json.Marshal(map[string]interface{}{
		"goal": map[string]interface{}{
			"objective":    objective,
			"status":       string(runtimegoal.StatusComplete),
			"completed_by": "model",
		},
	})
	return runtimetypes.Message{Role: "tool", Content: string(content)}
}

// TestGoalReconcileIncrementalScanFindsLateCompletion 验证游标推进后新追加的
// completed goal 仍被扫描到，且重复调用幂等。
func TestGoalReconcileIncrementalScanFindsLateCompletion(t *testing.T) {
	session := newGoalReconcileTestSession()
	session.Messages = []runtimetypes.Message{{Role: "user", Content: "hi"}}

	changed, err := reconcileGoalCompletionFromToolMessages(session)
	if err != nil || changed {
		t.Fatalf("no goal yet: changed=%v err=%v", changed, err)
	}
	if session.goalReconcileScanned != len(session.Messages) {
		t.Fatalf("cursor must advance: %d", session.goalReconcileScanned)
	}

	session.Messages = append(session.Messages, goalCompletedToolMessage("ship the release"))
	changed, err = reconcileGoalCompletionFromToolMessages(session)
	if err != nil || !changed {
		t.Fatalf("late completion must be found: changed=%v err=%v", changed, err)
	}
	if got := persistedGoalObjective(t, session); got != "ship the release" {
		t.Fatalf("goal not persisted: %q", got)
	}
}

// TestGoalReconcileLastWinsAcrossIncrementalScans 验证跨增量扫描的
// last-wins 语义与全量扫描一致。
func TestGoalReconcileLastWinsAcrossIncrementalScans(t *testing.T) {
	session := newGoalReconcileTestSession()
	session.Messages = []runtimetypes.Message{goalCompletedToolMessage("first goal")}
	if _, err := reconcileGoalCompletionFromToolMessages(session); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	session.Messages = append(session.Messages, goalCompletedToolMessage("second goal"))
	if _, err := reconcileGoalCompletionFromToolMessages(session); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if got := persistedGoalObjective(t, session); got != "second goal" {
		t.Fatalf("last-wins violated: %q", got)
	}
}

// TestGoalReconcileFingerprintInvalidationRescansAll 验证列表整体替换
// （fingerprint 失配）后即使新 completed goal 位于旧游标之前也能被重扫到。
func TestGoalReconcileFingerprintInvalidationRescansAll(t *testing.T) {
	session := newGoalReconcileTestSession()
	session.Messages = []runtimetypes.Message{{Role: "user", Content: "hi"}}
	if _, err := reconcileGoalCompletionFromToolMessages(session); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}

	// 模拟 restore/compact：整列表替换，completed goal 出现在旧游标之前，
	// 且列表长度不变、末条消息不同（触发 fingerprint 失配）。
	replacement := []runtimetypes.Message{
		{Role: "user", Content: "restored"},
		goalCompletedToolMessage("restored goal"),
	}
	session.Messages = replacement
	changed, err := reconcileGoalCompletionFromToolMessages(session)
	if err != nil || !changed {
		t.Fatalf("replaced list must trigger full rescan: changed=%v err=%v", changed, err)
	}
	if got := persistedGoalObjective(t, session); got != "restored goal" {
		t.Fatalf("restored goal not found: %q", got)
	}
}

// persistedGoalObjective 读取 MetadataStore 写入 session metadata 的当前目标。
func persistedGoalObjective(t *testing.T, session *ChatSession) string {
	t.Helper()
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session.RuntimeSession)
	if err != nil || !ok || goal == nil {
		return ""
	}
	return strings.TrimSpace(goal.Objective)
}
