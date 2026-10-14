package connectors

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// TestAttentionNoticeForEventMapping 锁定事件→通知映射与去重键形状
// （文案变更须三宿主同步评审：approval/question/completion 三类）。
func TestAttentionNoticeForEventMapping(t *testing.T) {
	approval := runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "sess-1",
		Payload:   map[string]any{"request_id": "apr-1", "tool_name": "shell_exec", "reason": "writes outside workspace"},
	}
	notice, key, ok := AttentionNoticeForEvent(approval, nil)
	if !ok {
		t.Fatal("approval event should map")
	}
	if notice.Kind != AttentionKindApproval {
		t.Fatalf("kind = %s, want %s", notice.Kind, AttentionKindApproval)
	}
	if notice.Session != (SessionRef{SessionID: "sess-1"}) {
		t.Fatalf("session = %+v", notice.Session)
	}
	for _, want := range []string{"shell_exec", "writes outside workspace", "/approve apr-1"} {
		if !strings.Contains(notice.Text, want) {
			t.Fatalf("approval text missing %q: %s", want, notice.Text)
		}
	}
	if key != "approval|sess-1|apr-1" {
		t.Fatalf("key = %s", key)
	}

	question := runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "sess-1",
		Payload:   map[string]any{"question_id": "q-1", "prompt": "which env?"},
	}
	notice, key, ok = AttentionNoticeForEvent(question, nil)
	if !ok {
		t.Fatal("question event should map")
	}
	if notice.Kind != AttentionKindQuestion {
		t.Fatalf("kind = %s", notice.Kind)
	}
	if !strings.Contains(notice.Text, "which env?") || !strings.Contains(notice.Text, "/answer 1") {
		t.Fatalf("question text: %s", notice.Text)
	}
	if key != "question|sess-1|q-1" {
		t.Fatalf("key = %s", key)
	}

	done := runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "sess-1", Timestamp: time.Now()}
	notice, key, ok = AttentionNoticeForEvent(done, func(string) bool { return false })
	if !ok {
		t.Fatal("turn done should map")
	}
	if notice.Kind != AttentionKindCompletion {
		t.Fatalf("kind = %s", notice.Kind)
	}
	if !strings.Contains(notice.Text, "已完成") {
		t.Fatalf("completion text: %s", notice.Text)
	}
	if !strings.HasPrefix(key, "completion|sess-1|") {
		t.Fatalf("key = %s", key)
	}

	// 托管挂起：run 收尾不等于 turn 完成（与 gateway_attention 同口径）。
	notice, _, ok = AttentionNoticeForEvent(done, func(string) bool { return true })
	if !ok || !strings.Contains(notice.Text, "等待子任务") {
		t.Fatalf("parked text: %s (ok=%v)", notice.Text, ok)
	}

	// 空 session / 未订阅类型：不产生通知。
	if _, _, ok := AttentionNoticeForEvent(runtimeevents.Event{Type: runtimechat.EventApprovalRequested}, nil); ok {
		t.Fatal("empty session should not map")
	}
	if _, _, ok := AttentionNoticeForEvent(runtimeevents.Event{Type: "assistant_delta", SessionID: "sess-1"}, nil); ok {
		t.Fatal("unrelated type should not map")
	}
}

func TestAttentionDeduperEvictsOldest(t *testing.T) {
	dedupe := newAttentionDeduper(2)
	if !dedupe.first("a") || dedupe.first("a") {
		t.Fatal("first/a contract")
	}
	if !dedupe.first("b") || !dedupe.first("c") {
		t.Fatal("fill window")
	}
	// c 触发淘汰 a；a 重新可见。
	if !dedupe.first("a") {
		t.Fatal("a should be evicted and re-accepted")
	}
}

// TestAttentionNoticeForProjection 锁定 gateway 投影入口（节点心跳 attention
// 词表）的映射与跳过规则：known kind 出通知，waiting/未知 kind 跳过。
func TestAttentionNoticeForProjection(t *testing.T) {
	cases := []struct {
		attention string
		wantOK    bool
		wantKind  string
	}{
		{"approval", true, AttentionKindApproval},
		{"question", true, AttentionKindQuestion},
		{"error", true, "error"},
		{"completed", true, AttentionKindCompletion},
		{"waiting", false, ""},   // 托管挂起不推（避免与 completed 双推）
		{"unknown", false, ""},   // 未知 kind 保守跳过
		{"", false, ""},          // 空 kind 不推
		{"  approval  ", true, AttentionKindApproval},
	}
	for _, tc := range cases {
		notice, key, ok := AttentionNoticeForProjection("sess-1", tc.attention)
		if ok != tc.wantOK {
			t.Fatalf("attention=%q ok=%v want %v", tc.attention, ok, tc.wantOK)
		}
		if !ok {
			continue
		}
		if notice.Kind != tc.wantKind {
			t.Fatalf("attention=%q kind=%s want %s", tc.attention, notice.Kind, tc.wantKind)
		}
		if notice.Session.SessionID != "sess-1" {
			t.Fatalf("session = %+v", notice.Session)
		}
		if !strings.HasPrefix(key, "projection|sess-1|") {
			t.Fatalf("key = %s", key)
		}
	}
	// 空会话不推。
	if _, _, ok := AttentionNoticeForProjection("", "approval"); ok {
		t.Fatal("empty session should not notify")
	}
}

// TestSubscribeAttentionForwardWanted 锁定跨宿主扇出意愿判定（切片 D）：
// 任一 bot notify.enabled=true → Forward 被调（即使本宿主 scope/kinds 不命中
// ——本地出站判定与转发判定是两条链）；全部 disabled → Forward 不被调。
func TestSubscribeAttentionForwardWanted(t *testing.T) {
	notifyCalls := make(chan AttentionNotice, 4)
	forwardCalls := make(chan AttentionNotice, 4)
	bus := runtimeevents.NewBusWithRetention(16)
	unsubscribe := bus.SubscribeCancelable(runtimechat.EventApprovalRequested, func(e runtimeevents.Event) {})
	unsubscribe() // 仅确认 bus 可用

	// bot1：notify disabled（本宿主不出站也不转发意愿）；bot2：enabled（转发意愿）。
	store := NewMemoryStore(
		Config{ID: "c1", Provider: "fake", Enabled: true},
		Config{ID: "c2", Provider: "fake", Enabled: true, Policy: Policy{Notify: NotifyPolicy{Enabled: true}}},
	)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimechat.EventApprovalRequested, handler)
		},
		Store: store,
		Notify: func(_ context.Context, notice AttentionNotice) error {
			notifyCalls <- notice
			return nil
		},
		Forward: func(notice AttentionNotice) {
			forwardCalls <- notice
		},
	})
	defer sub()

	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "sess-elsewhere",
		Timestamp: time.Now(),
		Payload:   map[string]any{"request_id": "apr-fwd"},
	})

	select {
	case n := <-forwardCalls:
		if n.Session.SessionID != "sess-elsewhere" {
			t.Fatalf("forwarded notice = %+v", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("enabled bot must trigger forward")
	}
	// 本宿主出站：bot1 绑定空 + notify disabled；bot2 enabled 但默认 scope=sessions
	// 且 sess-elsewhere 未绑定到 c2 → 本地不出站。
	select {
	case n := <-notifyCalls:
		t.Fatalf("local notify must be suppressed for unbound+sessions scope, got %+v", n)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestRouteOverrideFromPolicy 锁定 D6 路由归一化（两宿主共用）：零策略/全空白
// 返回 nil（宿主走原路径），非空策略按项裁剪空白。
func TestRouteOverrideFromPolicy(t *testing.T) {
	if got := RouteOverrideFromPolicy(RoutePolicy{}); got != nil {
		t.Fatalf("zero policy should be nil, got %+v", got)
	}
	if got := RouteOverrideFromPolicy(RoutePolicy{Provider: "  ", Model: "\t"}); got != nil {
		t.Fatalf("whitespace-only policy should be nil, got %+v", got)
	}
	got := RouteOverrideFromPolicy(RoutePolicy{Provider: " workbuddy ", Model: "gpt-5.4", ReasoningEffort: " high "})
	if got == nil {
		t.Fatal("non-empty policy should map")
	}
	if got.Provider != "workbuddy" || got.Model != "gpt-5.4" || got.ReasoningEffort != "high" {
		t.Fatalf("trimmed = %+v", got)
	}
	// 仅单项也生效（独立省略语义）。
	got = RouteOverrideFromPolicy(RoutePolicy{ReasoningEffort: "low"})
	if got == nil || got.ReasoningEffort != "low" || got.Provider != "" || got.Model != "" {
		t.Fatalf("single-item = %+v", got)
	}
}
