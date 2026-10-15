package connectors

// 端到端（docs/bots/13 §3.5/§6.2 S2）：声明式事件的注意力投递——
// ids 跨会话选择器 + 跨会话文案标识 + 绑定会话 approval 恒开。

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

func TestSubscribeAttentionDeclarativeIdsSelectorCrossSession(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "ops", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-other"}}, Types: []string{"turn.*"}, Deliver: "notice"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-ops", Session: SessionRef{SessionID: "sess-bot"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)
	bus := runtimeevents.NewBusWithRetention(16)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimeobserve.EventAgentTurnDone, handler)
		},
		Store:        store,
		NotifyScoped: service.NotifyAttentionScoped,
	})
	defer sub()

	bus.Publish(runtimeevents.Event{
		Type:      runtimeobserve.EventAgentTurnDone,
		SessionID: "sess-other",
		Timestamp: time.Now(),
		Payload:   map[string]any{"turn_id": "turn-ops"},
	})

	deadline := time.Now().Add(3 * time.Second)
	var text string
	for time.Now().Before(deadline) {
		if texts := provider.sentTexts(); len(texts) > 0 {
			text = texts[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if text == "" {
		t.Fatalf("declarative ids subscription did not deliver; sent=%v", provider.sentTexts())
	}
	// 跨会话投递必须带会话标识（13 §7.1），否则运维无法区分来源。
	if !strings.Contains(text, "已完成") || !strings.Contains(text, "（会话 sess-other）") {
		t.Fatalf("cross-session notice must carry session label, got %q", text)
	}
}

func TestSubscribeAttentionBoundApprovalAlwaysOnWithEventsConfigured(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		// 配置了 events 模型但没有 approval 订阅：绑定会话的 approval 仍恒开（13 §6.2）。
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{Sessions: SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "content"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-bound", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)
	bus := runtimeevents.NewBusWithRetention(16)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimechat.EventApprovalRequested, handler)
		},
		Store:        store,
		NotifyScoped: service.NotifyAttentionScoped,
	})
	defer sub()

	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "sess-1",
		Timestamp: time.Now(),
		Payload:   map[string]any{"request_id": "apr-1", "tool_name": "shell"},
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if texts := provider.sentFor("chat-bound"); len(texts) > 0 {
			if strings.Contains(texts[0].Text, "（会话 ") {
				t.Fatalf("bound-session notice must NOT carry session label, got %q", texts[0].Text)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("bound approval must stay delivered; sent=%v", provider.sentTexts())
}
