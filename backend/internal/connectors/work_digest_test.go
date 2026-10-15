package connectors

// S4（docs/bots/13 §3.3/§7.1）：工作类聚合订阅——工具/子代理事件仅 digest
// 形态投递（notice 订阅不生效）；窗口内与 completion 同批聚合，文案按 kind
// 分列（回合/工具/子代理）；事件身份去重（tool_call_id/subagent_id）。

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// 词表：工作类别名/族通配归一化。
func TestWorkClassEventTableNormalizes(t *testing.T) {
	for _, tc := range []struct{ in, canonical string }{
		{"tool_finished", "tool.completed"},
		{"tool.finished", "tool.completed"},
		{"tool.started", "tool.requested"},
		{"tool.denied", "tool.denied"},
		{"subagent.completed", "subagent.completed"},
		{"subagent.batch.completed", "subagent.batch.completed"},
	} {
		ev, ok := subscriptionEventFor(tc.in)
		if !ok || ev.Canonical != tc.canonical {
			t.Fatalf("subscriptionEventFor(%q) = %+v ok=%v, want %q", tc.in, ev, ok, tc.canonical)
		}
	}
	for _, name := range []string{"tool.completed", "subagent.completed", "subagent.batch.completed"} {
		ev, _ := subscriptionEventFor(name)
		if !ev.matchesPattern("work.*") || ev.Family != "work" {
			t.Fatalf("%s family/pattern = %q/%v", name, ev.Family, ev.matchesPattern("work.*"))
		}
	}
}

// 仅聚合形态：notice 订阅列出工作类类型也不投递（不 immediate、不广播、不审计）。
func TestWorkClassRejectsNoticeForm(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "w", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"tool.completed"}, Deliver: "notice"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	var scoped, broadcast atomic.Int32
	bus := runtimeevents.NewBusWithRetention(16)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable("tool.completed", handler)
		},
		Store: store,
		NotifyScoped: func(context.Context, []string, AttentionNotice) error {
			scoped.Add(1)
			return nil
		},
		NotifyBroadcast: func(context.Context, []string, AttentionNotice) error {
			broadcast.Add(1)
			return nil
		},
		DigestWindow: 30 * time.Millisecond,
	})
	defer sub()

	bus.Publish(runtimeevents.Event{
		Type: "tool.completed", SessionID: "sess-1", Timestamp: time.Now(),
		Payload: map[string]any{"tool_call_id": "call-1", "tool_name": "shell"},
	})
	time.Sleep(200 * time.Millisecond)
	if scoped.Load() != 0 || broadcast.Load() != 0 {
		t.Fatalf("work class must reject notice form: scoped=%d broadcast=%d", scoped.Load(), broadcast.Load())
	}
}

// digest 订阅：completion 与工作类同批聚合为一条广播，文案按 kind 分列；
// 事件身份去重；审计回调收到批内计数。
func TestWorkClassDigestAggregatesMixedEvents(t *testing.T) {
	const cid = "c-work-agg"
	store := NewMemoryStore(testConfig(cid, "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "ops", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"turn.*", "work.*"}, Deliver: "digest"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: cid, Chat: "chat-ops", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}

	type broadcastCall struct {
		connectorIDs []string
		notice       AttentionNotice
	}
	broadcastCh := make(chan broadcastCall, 4)
	type auditCall struct {
		connectorID    string
		subscriptionID string
		count          int
	}
	auditCh := make(chan auditCall, 4)
	var scoped atomic.Int32

	bus := runtimeevents.NewBusWithRetention(32)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			eventTypes := []string{
				runtimeobserve.EventAgentTurnDone,
				"tool.completed", "tool.denied", "subagent.completed", "subagent.batch.completed",
			}
			unsubs := make([]func(), 0, len(eventTypes))
			for _, eventType := range eventTypes {
				if unsub := bus.SubscribeCancelable(eventType, handler); unsub != nil {
					unsubs = append(unsubs, unsub)
				}
			}
			return func() {
				for _, unsub := range unsubs {
					unsub()
				}
			}
		},
		Store: store,
		NotifyScoped: func(context.Context, []string, AttentionNotice) error {
			scoped.Add(1)
			return nil
		},
		NotifyBroadcast: func(_ context.Context, connectorIDs []string, notice AttentionNotice) error {
			broadcastCh <- broadcastCall{connectorIDs: append([]string(nil), connectorIDs...), notice: notice}
			return nil
		},
		AuditBroadcast: func(connectorID, subscriptionID string, count int) {
			auditCh <- auditCall{connectorID, subscriptionID, count}
		},
		DigestWindow: 40 * time.Millisecond,
	})
	defer sub()

	publish := func(eventType, sessionID string, payload map[string]any) {
		bus.Publish(runtimeevents.Event{Type: eventType, SessionID: sessionID, Timestamp: time.Now(), Payload: payload})
	}
	publish(runtimeobserve.EventAgentTurnDone, "sess-1", map[string]any{"turn_id": "t1"})
	publish("tool.completed", "sess-1", map[string]any{"tool_call_id": "call-a", "tool_name": "shell"})
	publish("tool.completed", "sess-1", map[string]any{"tool_call_id": "call-b"})
	publish("tool.denied", "sess-1", map[string]any{"tool_call_id": "call-c"})
	publish("subagent.completed", "sess-1", map[string]any{"subagent_id": "sub-1"})
	// 同身份重复投递：必须被事件级去重吞掉（不重复计数）。
	publish("tool.completed", "sess-1", map[string]any{"tool_call_id": "call-b"})

	var call broadcastCall
	select {
	case call = <-broadcastCh:
	case <-time.After(3 * time.Second):
		t.Fatal("work digest flush did not fire")
	}
	if len(call.connectorIDs) != 1 || call.connectorIDs[0] != cid {
		t.Fatalf("broadcast connectors = %v", call.connectorIDs)
	}
	for _, want := range []string{"5 个事件已收尾", "回合 ×1", "工具 ×3", "子代理 ×1"} {
		if !strings.Contains(call.notice.Text, want) {
			t.Fatalf("digest text missing %q: %q", want, call.notice.Text)
		}
	}
	if strings.Contains(call.notice.Text, "会话回合已收尾") {
		t.Fatalf("mixed batch must not use completion-only wording: %q", call.notice.Text)
	}
	if scoped.Load() != 0 {
		t.Fatalf("digest hits must not deliver immediately, scoped=%d", scoped.Load())
	}
	select {
	case audit := <-auditCh:
		if audit.connectorID != cid || audit.subscriptionID != "ops" || audit.count != 5 {
			t.Fatalf("audit = %+v", audit)
		}
	case <-time.After(time.Second):
		t.Fatal("digest audit not fired")
	}
	if got := digestMergedCounter(cid); got != 5 {
		t.Fatalf("digest merged = %v, want 5", got)
	}
	if got := deliveryCounter(cid, DeliveryFormDigest, deliveryOutcomeSent); got != 1 {
		t.Fatalf("digest sent = %v, want 1", got)
	}
	// 去重后的重复投递不得触发二次广播。
	select {
	case extra := <-broadcastCh:
		t.Fatalf("unexpected extra broadcast: %+v", extra)
	case <-time.After(150 * time.Millisecond):
	}
}
