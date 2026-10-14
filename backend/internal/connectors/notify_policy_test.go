package connectors

import (
	"testing"
)

func notice(kind string, session SessionRef) AttentionNotice {
	return AttentionNotice{Kind: kind, Session: session, Text: "x"}
}

// TestAcceptsNoticeBoundSessionCommandLoopAlwaysDelivered：绑定会话的
// approval/question（命令回环）不受 notify 开关影响；completion 属回执类
// 提示，2026-10-14 起与 notify 总开关联动（默认静音，零配置零打扰——
// 消除每条回复尾随「✅ 回合已完成」的真机噪音反馈）。
func TestAcceptsNoticeBoundSessionCommandLoopAlwaysDelivered(t *testing.T) {
	policy := Policy{} // notify 零值（未配置）
	bound := func(session SessionRef) bool { return true }

	if !policy.AcceptsNotice(notice(AttentionKindApproval, SessionRef{SessionID: "s1"}), bound) {
		t.Fatal("绑定会话的 approval 提示应始终接收（命令回环，不受开关影响）")
	}
	if !policy.AcceptsNotice(notice(AttentionKindQuestion, SessionRef{SessionID: "s1"}), bound) {
		t.Fatal("绑定会话的 question 提示应始终接收（命令回环，不受开关影响）")
	}
	if policy.AcceptsNotice(notice(AttentionKindCompletion, SessionRef{SessionID: "s1"}), bound) {
		t.Fatal("notify 未配置时绑定会话的 completion 不应发送（零配置零打扰）")
	}
	enabled := Policy{Notify: NotifyPolicy{Enabled: true}}
	if !enabled.AcceptsNotice(notice(AttentionKindCompletion, SessionRef{SessionID: "s1"}), bound) {
		t.Fatal("notify.enabled=true 时绑定会话的 completion 应接收")
	}
}

// TestAcceptsNoticeUnboundRequiresExplicitOptIn：其它会话的事件默认不收，
// scope=all + enabled 才收，kinds 白名单生效。
func TestAcceptsNoticeUnboundRequiresExplicitOptIn(t *testing.T) {
	target := SessionRef{SessionID: "other-session"}
	unbound := func(SessionRef) bool { return false }

	cases := []struct {
		name   string
		policy Policy
		want   bool
	}{
		{"未配置 notify（零值）不收", Policy{}, false},
		{"enabled 但 scope=sessions 不收", Policy{Notify: NotifyPolicy{Enabled: true}}, false},
		{"enabled+all 收全部", Policy{Notify: NotifyPolicy{Enabled: true, Scope: NotifyScopeAll}}, true},
		{"enabled+all+kinds 未命中不收", Policy{Notify: NotifyPolicy{Enabled: true, Scope: NotifyScopeAll, Kinds: []string{AttentionKindApproval}}}, false},
		{"enabled+all+kinds 命中收", Policy{Notify: NotifyPolicy{Enabled: true, Scope: NotifyScopeAll, Kinds: []string{AttentionKindCompletion, AttentionKindError}}}, true},
	}
	for _, tc := range cases {
		if got := tc.policy.AcceptsNotice(notice(AttentionKindCompletion, target), unbound); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// TestAcceptsNoticeNilBoundHereTreatedAsUnbound：boundHere 为 nil 按"未绑定"处理
// （宿主接线缺失时保守不收跨会话事件）。
func TestAcceptsNoticeNilBoundHereTreatedAsUnbound(t *testing.T) {
	policy := Policy{Notify: NotifyPolicy{Enabled: true, Scope: NotifyScopeAll}}
	if !policy.AcceptsNotice(notice(AttentionKindError, SessionRef{SessionID: "x"}), nil) {
		t.Fatal("scope=all + enabled 在 boundHere=nil 时仍应收（广播模式不查绑定）")
	}
	minimal := Policy{}
	if minimal.AcceptsNotice(notice(AttentionKindError, SessionRef{SessionID: "x"}), nil) {
		t.Fatal("零值 policy 不得收任何通知")
	}
}
