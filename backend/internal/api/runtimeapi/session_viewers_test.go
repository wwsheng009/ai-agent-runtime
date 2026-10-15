package runtimeapi

import "testing"

// TestSessionViewersTransitions 锁定订阅者计数的跃迁语义（docs/bots/10 §4）：
// 仅 0→1 与 1→0 报跃迁（调用方据此刷新网关摘要），中间值静默。
func TestSessionViewersTransitions(t *testing.T) {
	var v sessionViewers
	if v.Active("s1") {
		t.Fatal("空计数应为不活跃")
	}
	if !v.Add("s1") {
		t.Fatal("0→1 跃迁应报 true")
	}
	if v.Add("s1") {
		t.Fatal("1→2 不应报跃迁")
	}
	if !v.Active("s1") {
		t.Fatal("应有活跃订阅者")
	}
	if v.Remove("s1") {
		t.Fatal("2→1 不应报跃迁")
	}
	if !v.Remove("s1") {
		t.Fatal("1→0 应报跃迁")
	}
	if v.Active("s1") {
		t.Fatal("移除最后一个订阅者后应为不活跃")
	}
}

// TestGatewaySessionsCarryViewing：有 web 订阅者时会话摘要携带 Viewing=true。
func TestGatewaySessionsCarryViewing(t *testing.T) {
	handler, session := newTailTestHandler(t, &fakeGatewayController{})
	handler.sessionViewers.Add(session.ID)
	summaries := handler.GatewaySessions()
	if len(summaries) != 1 || !summaries[0].Viewing {
		t.Fatalf("summaries = %+v", summaries)
	}
	handler.sessionViewers.Remove(session.ID)
	if s := handler.GatewaySessions(); len(s) == 1 && s[0].Viewing {
		t.Fatal("移除订阅者后不得再报 Viewing")
	}
}