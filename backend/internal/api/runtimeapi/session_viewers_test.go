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
// —— R1：sessionViewers 作为 notify.Presence 权威（docs/bots/12 §4 R1）——

// TestSessionPresenceSatisfiesKernelContract 锁定 R1 契约：SessionPresence()
// 返回的实现在内核 Dispatcher 语义下可用——SSE 订阅者在看时报告在场（门抑制），
// 无订阅者时放行；nodeID 维度按单宿主忽略（任意非空 nodeID 皆可）。
func TestSessionPresenceSatisfiesKernelContract(t *testing.T) {
	h := &Handler{}
	v := h.SessionPresence()
	if v == nil {
		t.Fatal("SessionPresence 不应为 nil")
	}

	// 无订阅者：不在场。
	if v.Active("node-x", "s1") {
		t.Fatal("无订阅者不应报告在场")
	}

	// 挂一个订阅者：在场（内核门将抑制推送）。
	h.sessionViewers.Add("s1")
	if !v.Active("node-x", "s1") {
		t.Fatal("SSE 订阅者在看应报告在场")
	}
	// nodeID 维度不参与判定（单宿主）。
	if !v.Active("any-node", "s1") {
		t.Fatal("单宿主应忽略 nodeID 维度")
	}

	// 断开后回落不在场。
	h.sessionViewers.Remove("s1")
	if v.Active("node-x", "s1") {
		t.Fatal("订阅者断开后不应报告在场")
	}
}
