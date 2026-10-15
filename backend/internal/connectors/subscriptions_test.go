package connectors

// 契约（docs/bots/13 §3.1/§3.4/§3.5 S2）：声明式订阅求解层——
// 事件词表/族通配、会话选择器、legacy 回退与双读单写语义。

import (
	"testing"
)

func TestSubscriptionEventVocabularyAndWildcards(t *testing.T) {
	assistant, ok := subscriptionEventFor("assistant_message")
	if !ok || assistant.Family != "conversation" {
		t.Fatalf("assistant_message lookup = %+v ok=%v", assistant, ok)
	}
	for _, pattern := range []string{"assistant_message", "assistant.message", "conversation", "conversation.*", "*"} {
		if !assistant.matchesPattern(pattern) {
			t.Fatalf("pattern %q must match assistant_message", pattern)
		}
	}
	for _, pattern := range []string{"turn.*", "approval", "interaction.*"} {
		if assistant.matchesPattern(pattern) {
			t.Fatalf("pattern %q must NOT match assistant_message", pattern)
		}
	}

	// 点分隔别名归一化到同一事件（事件源侧两种写法都要能订阅）。
	alias, ok := subscriptionEventFor("assistant.message")
	if !ok || alias.Canonical != "assistant_message" {
		t.Fatalf("assistant.message alias = %+v ok=%v", alias, ok)
	}

	// 回合完成：旧 kind "completion" 与族通配 "turn.*" 都命中。
	turnDone, ok := subscriptionEventFor("agent.turn.finished")
	if !ok || turnDone.Kind != AttentionKindCompletion {
		t.Fatalf("agent.turn.finished lookup = %+v ok=%v", turnDone, ok)
	}
	for _, pattern := range []string{"agent.turn.finished", "completion", "turn", "turn.*"} {
		if !turnDone.matchesPattern(pattern) {
			t.Fatalf("pattern %q must match agent.turn.finished", pattern)
		}
	}
	if turnDone.matchesPattern("conversation.*") {
		t.Fatal("turn event must not match conversation family")
	}

	approval, ok := subscriptionEventFor("approval_requested")
	if !ok {
		t.Fatal("approval_requested must be registered")
	}
	if !approval.matchesPattern("approval") || !approval.matchesPattern("interaction.*") {
		t.Fatalf("approval aliases must match: %+v", approval)
	}
	if approval.matchesPattern("completion") {
		t.Fatal("approval must not match completion")
	}
}

func TestSubscriptionSessionMatchingComparisonIsSessionIDOnly(t *testing.T) {
	// 绑定带 NodeID（R2a 回写后的形态）也必须能被 SessionID 命中（13 会话选择器口径）。
	bindings := []Binding{
		{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{NodeID: "aicli:node-1", SessionID: "sess-1"}},
	}
	boundHere := sessionBoundLookup(bindings)
	if !boundHere(SessionRef{SessionID: "sess-1"}) {
		t.Fatal("SessionID-only lookup must match even when binding carries NodeID")
	}

	cases := []struct {
		selector SessionSelector
		want     bool
	}{
		{SessionSelector{}, true},                                      // 缺省 = bound
		{SessionSelector{Mode: "bound"}, true},                         // 显式 bound
		{SessionSelector{Mode: "ids", IDs: []string{"sess-1"}}, true},  // ids 命中
		{SessionSelector{Mode: "ids", IDs: []string{"sess-2"}}, false}, // ids 未命中
		{SessionSelector{Mode: "all"}, true},                           // 广播
		{SessionSelector{Mode: "bogus"}, false},                        // 未知模式安全拒绝
	}
	for _, item := range cases {
		got := subscriptionSessionMatches(item.selector, "sess-1", boundHere)
		if got != item.want {
			t.Fatalf("selector %+v match = %v, want %v", item.selector, got, item.want)
		}
	}
	if subscriptionSessionMatches(SessionSelector{Mode: "all"}, "", boundHere) {
		t.Fatal("empty session must never match")
	}
}

func TestNormalizeEventsSubscriptionsFiltersInvalidEntries(t *testing.T) {
	subs := normalizeEventsSubscriptions([]EventSubscription{
		{Types: []string{"turn.*"}},                                      // id 自动生成 + bound 缺省 + notice 缺省
		{Sessions: SessionSelector{Mode: "ids"}},                         // ids 无列表 → 忽略
		{Types: []string{"  "}},                                          // 空 types → 忽略
		{Sessions: SessionSelector{Mode: "bogus"}, Types: []string{"x"}}, // 未知模式 → 忽略
		{ID: "ops", Sessions: SessionSelector{Mode: "ids", IDs: []string{" a ", "b"}}, Types: []string{"approval"}, Deliver: "CONTENT"},
	})
	if len(subs) != 2 {
		t.Fatalf("normalized = %+v, want 2 entries", subs)
	}
	if subs[0].ID != "sub-1" || subs[0].Sessions.Mode != "bound" || subs[0].Deliver != "notice" {
		t.Fatalf("defaults not applied: %+v", subs[0])
	}
	if subs[1].ID != "ops" || subs[1].Deliver != "content" || len(subs[1].Sessions.IDs) != 2 || subs[1].Sessions.IDs[0] != "a" {
		t.Fatalf("normalization mismatch: %+v", subs[1])
	}
}

func TestSubscriptionNoticeAcceptedSemantics(t *testing.T) {
	boundHere := sessionBoundLookup([]Binding{{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}})
	approval, _ := subscriptionEventFor("approval_requested")
	turnDone, _ := subscriptionEventFor("agent.turn.finished")

	// 恒开（13 §6.2）：events 已配置且无 approval 订阅，绑定会话仍接收 approval。
	configured := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{Sessions: SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "content"},
	}}}
	if !subscriptionNoticeAccepted(configured, approval, AttentionKindApproval, "sess-1", boundHere) {
		t.Fatal("bound approval must stay always-on for events-configured bots")
	}
	// 未订阅的 completion：events 模型下绑定会话也不收（配置即契约）。
	if subscriptionNoticeAccepted(configured, turnDone, AttentionKindCompletion, "sess-1", boundHere) {
		t.Fatal("completion must not be delivered without a matching subscription")
	}
	// ids 选择器：跨会话 completion 命中。
	ops := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "ops", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-other"}}, Types: []string{"turn.*"}, Deliver: "notice"},
	}}}
	if !subscriptionNoticeAccepted(ops, turnDone, AttentionKindCompletion, "sess-other", boundHere) {
		t.Fatal("ids-selector completion must be accepted cross-session")
	}
	if subscriptionNoticeAccepted(ops, turnDone, AttentionKindCompletion, "sess-nope", boundHere) {
		t.Fatal("unlisted session must not be accepted")
	}

	// legacy 回退：未配置 events 时逐字保留 D5 语义。
	legacyDisabled := Policy{}
	if subscriptionNoticeAccepted(legacyDisabled, turnDone, AttentionKindCompletion, "sess-1", boundHere) {
		t.Fatal("legacy: bound completion requires notify.enabled")
	}
	legacyEnabled := Policy{Notify: NotifyPolicy{Enabled: true}}
	if !subscriptionNoticeAccepted(legacyEnabled, turnDone, AttentionKindCompletion, "sess-1", boundHere) {
		t.Fatal("legacy: notify.enabled must accept bound completion")
	}
	// legacy bound approval 恒开。
	if !subscriptionNoticeAccepted(legacyDisabled, approval, AttentionKindApproval, "sess-1", boundHere) {
		t.Fatal("legacy: bound approval must stay always-on")
	}
}

// TestSubscriptionNoticeLegacySkipsWorkFamily 锁定 2026-10-15 真机修复：
// legacy 回退不得逐条直投工作类（tool/subagent）——legacy 无 digest 聚合通道，
// 逐条 notice 违反「不放大：禁止逐 tool 事件直投」（docs/bots/13 §3.7#2）。
// 修复前：未配 events 的 bot 因「绑定会话非 completion 恒开」收到每个工具事件的
// `subscription=legacy-notify` 投递（微信 bot 实例）。
func TestSubscriptionNoticeLegacySkipsWorkFamily(t *testing.T) {
	boundHere := sessionBoundLookup([]Binding{{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}})
	toolDone, _ := subscriptionEventFor("tool.completed")
	toolStarted, _ := subscriptionEventFor("tool.requested")
	subagentDone, _ := subscriptionEventFor("subagent.completed")
	approval, _ := subscriptionEventFor("approval_requested")

	// 零配置 legacy：工作类三类事件（tool/subagent）均不投递。
	legacy := Policy{}
	for name, tc := range map[string]struct {
		ev   subscriptionEvent
		kind string
	}{
		"tool.completed":     {toolDone, AttentionKindTool},
		"tool.requested":     {toolStarted, AttentionKindTool},
		"subagent.completed": {subagentDone, AttentionKindSubagent},
	} {
		if delivery, sub := subscriptionNoticeDelivery(legacy, tc.ev, tc.kind, "sess-1", boundHere); delivery != "" {
			t.Fatalf("legacy %s delivery = %q sub=%+v, want none", name, delivery, sub)
		}
	}
	// 非工作类不受影响：绑定 approval 恒开（命令回环）。
	if delivery, _ := subscriptionNoticeDelivery(legacy, approval, AttentionKindApproval, "sess-1", boundHere); delivery != "notice" {
		t.Fatalf("legacy bound approval delivery = %q, want notice", delivery)
	}
	// 新模型不受影响：工作类仅 digest 订阅命中时投递。
	digest := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "work", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"work.*"}, Deliver: "digest"},
	}}}
	if delivery, sub := subscriptionNoticeDelivery(digest, toolDone, AttentionKindTool, "sess-1", boundHere); delivery != "digest" || sub.ID != "work" {
		t.Fatalf("events digest work delivery = %q sub=%+v", delivery, sub)
	}
	// 回归保护：新模型 notice 订阅对工作类不生效（已有语义）。
	noticeOnly := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "n", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"work.*"}, Deliver: "notice"},
	}}}
	if delivery, _ := subscriptionNoticeDelivery(noticeOnly, toolDone, AttentionKindTool, "sess-1", boundHere); delivery != "" {
		t.Fatalf("work family via notice subscription delivery = %q, want none", delivery)
	}
}

func TestSubscriptionContentAllowedSemantics(t *testing.T) {
	boundHere := sessionBoundLookup([]Binding{{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}})
	assistant, _ := subscriptionEventFor("assistant_message")

	// events 配置后 content_mirror 被替代（双读单写：新模型优先）。
	eventsWithoutContent := Policy{ContentMirror: true, Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{Sessions: SessionSelector{Mode: "bound"}, Types: []string{"turn.*"}, Deliver: "notice"},
	}}}
	if subscriptionContentAllowed(eventsWithoutContent, assistant, "sess-1", boundHere) {
		t.Fatal("events configured must not fall back to legacy content_mirror")
	}
	eventsWithContent := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{Sessions: SessionSelector{Mode: "bound"}, Types: []string{"conversation.*"}, Deliver: "content"},
	}}}
	if !subscriptionContentAllowed(eventsWithContent, assistant, "sess-1", boundHere) {
		t.Fatal("declarative content subscription must allow mirror")
	}
	// legacy：开关直通。
	if !subscriptionContentAllowed(Policy{ContentMirror: true}, assistant, "sess-1", boundHere) {
		t.Fatal("legacy content_mirror must allow mirror")
	}
	if subscriptionContentAllowed(Policy{}, assistant, "sess-1", boundHere) {
		t.Fatal("default-off policy must not mirror")
	}
}

func TestSubscriptionSessionLabel(t *testing.T) {
	if got := subscriptionSessionLabel("session_20261015171200_z7m74wJq"); got != "z7m74wJq" {
		t.Fatalf("label = %q", got)
	}
	if got := subscriptionSessionLabel("sess-other"); got != "sess-other" {
		t.Fatalf("short label = %q", got)
	}
	if got := subscriptionSessionLabel(""); got != "" {
		t.Fatalf("empty label = %q", got)
	}
}

// S4：单条订阅 enabled——显式停用跳过求解；缺省启用；命中返回订阅 id（审计）。
func TestSubscriptionDisabledEntriesAreSkipped(t *testing.T) {
	boundHere := sessionBoundLookup([]Binding{{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}})
	assistant, _ := subscriptionEventFor("assistant_message")
	turnDone, _ := subscriptionEventFor("agent.turn.finished")
	disabled := false

	contentOff := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "off", Enabled: &disabled, Sessions: SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "content"},
	}}}
	if subscriptionContentAllowed(contentOff, assistant, "sess-1", boundHere) {
		t.Fatal("disabled subscription must not match content")
	}
	noticeOff := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "off", Enabled: &disabled, Sessions: SessionSelector{Mode: "bound"}, Types: []string{"turn.*"}, Deliver: "notice"},
	}}}
	if subscriptionNoticeAccepted(noticeOff, turnDone, AttentionKindCompletion, "sess-1", boundHere) {
		t.Fatal("disabled subscription must not match notice")
	}

	// nil/true = 启用；匹配返回命中订阅 id（内容审计用）。
	enabled := true
	contentOn := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "on", Enabled: &enabled, Sessions: SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "content"},
	}}}
	sub, ok := subscriptionContentMatch(contentOn, assistant, "sess-1", boundHere)
	if !ok || sub.ID != "on" {
		t.Fatalf("match = %+v ok=%v", sub, ok)
	}
	legacy, ok := subscriptionContentMatch(Policy{ContentMirror: true}, assistant, "sess-1", boundHere)
	if !ok || legacy.ID != "legacy-content" {
		t.Fatalf("legacy match = %+v ok=%v", legacy, ok)
	}
}

// S4：digest 形态的投递决策——completion 聚合；交互类即便配 digest 也即时；
// notice 订阅优先于 digest（同一事件两形态并存时）。
func TestSubscriptionNoticeDeliveryDigest(t *testing.T) {
	boundHere := sessionBoundLookup([]Binding{{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}})
	turnDone, _ := subscriptionEventFor("agent.turn.finished")
	approval, _ := subscriptionEventFor("approval_requested")

	digestPolicy := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "ops", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-other"}}, Types: []string{"turn.*", "approval"}, Deliver: "digest"},
	}}}
	delivery, sub := subscriptionNoticeDelivery(digestPolicy, turnDone, AttentionKindCompletion, "sess-other", boundHere)
	if delivery != "digest" || sub.ID != "ops" {
		t.Fatalf("completion delivery = %q sub=%+v", delivery, sub)
	}
	// 交互类即便命中 digest 订阅也即时投递（安全优先，告警不因聚合迟到）。
	delivery, sub = subscriptionNoticeDelivery(digestPolicy, approval, AttentionKindApproval, "sess-other", boundHere)
	if delivery != "notice" || sub.ID != "ops" {
		t.Fatalf("approval delivery = %q sub=%+v", delivery, sub)
	}

	both := Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
		{ID: "digest", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-other"}}, Types: []string{"turn.*"}, Deliver: "digest"},
		{ID: "notice", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-other"}}, Types: []string{"turn.*"}, Deliver: "notice"},
	}}}
	delivery, sub = subscriptionNoticeDelivery(both, turnDone, AttentionKindCompletion, "sess-other", boundHere)
	if delivery != "notice" || sub.ID != "notice" {
		t.Fatalf("precedence delivery = %q sub=%+v", delivery, sub)
	}
}
