package commands

import "testing"

// TestProductionUIActorEnablesAsyncTranscriptPlan 锁定 P1.2 Stage B3 的生产切换：
// 真实会话 actor 必须配置 plan worker，否则大会话 resume 仍会在 actor 锁内跑
// O(entire history) 的 screening（P12 冻结的根因之一）。
func TestProductionUIActorEnablesAsyncTranscriptPlan(t *testing.T) {
	coordinator := newTestChatInteractionCoordinator(t, &ChatSession{})
	t.Cleanup(coordinator.Shutdown)
	actor := coordinator.ensureUIActor()
	if actor == nil {
		t.Fatal("coordinator 没有创建 UI actor")
	}
	if !actor.AsyncTranscriptPlanEnabled() {
		t.Fatal("生产 actor 必须启用 AsyncTranscriptPlan（P1.2 Stage B3）")
	}
}
