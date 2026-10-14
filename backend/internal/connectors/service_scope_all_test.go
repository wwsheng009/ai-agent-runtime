package connectors

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// TestScopeAllDeliversToConnectorChats 钉死 D5 scope=all 的**投递**语义
// （docs/bots/09 §7.4.1）：bot 开启 notify{enabled, scope=all} 后，"其它会话"
// （未绑定到本 bot）的 turn 完成事件必须送达**本 bot 的已绑定 chat**——
// 而不是事件会话的绑定（后者对未绑定会话恒为空 = 静默丢失）。
func TestScopeAllDeliversToConnectorChats(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Notify = NotifyPolicy{Enabled: true, Scope: NotifyScopeAll, Kinds: []string{AttentionKindCompletion}}
	}))
	// bot 的绑定：chat-1 ↔ sess-bot（与将发生事件的 sess-other 无关）。
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-bot"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)
	// 挂上真实订阅（与宿主接线同构：NotifyScoped = service.NotifyAttentionScoped）。
	bus := runtimeevents.NewBusWithRetention(16)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimeobserve.EventAgentTurnDone, handler)
		},
		Store:        store,
		NotifyScoped: service.NotifyAttentionScoped,
	})
	defer sub()

	// 事件来自 sess-other（未绑定到 c1 的"其它会话"）。
	bus.Publish(runtimeevents.Event{
		Type:      runtimeobserve.EventAgentTurnDone,
		SessionID: "sess-other",
		Timestamp: time.Now(),
		Payload:   map[string]any{"turn_id": "turn-probe"},
	})

	// 期望：chat-1 收到完成通知（scope=all 的落点=本 bot 的绑定 chats）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, text := range provider.sentTexts() {
			if strings.Contains(text, "已完成") || strings.Contains(text, "回合") {
				return // 绿
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("scope=all notice not delivered to connector bindings; sent=%v", provider.sentTexts())
}

// TestBoundSessionDeliversPrecisely 防回归：绑定场景（bot 自己服务的会话）
// 必须**精确**投递给该会话的绑定 chat——不能因 scope=all 的回退广播误伤
// 同 bot 的其它 chats。
func TestBoundSessionDeliversPrecisely(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Notify = NotifyPolicy{Enabled: true, Scope: NotifyScopeAll, Kinds: []string{AttentionKindCompletion}}
	}))
	// 两个绑定：chat-A ↔ sess-1（事件源）；chat-B ↔ sess-2（旁观者，不应收到）。
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-A",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-B",
		Session: SessionRef{SessionID: "sess-2"}}); err != nil {
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
		SessionID: "sess-1",
		Timestamp: time.Now(),
		Payload:   map[string]any{"turn_id": "turn-bound"},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(provider.sentTexts()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sent := provider.sentTexts()
	if len(sent) != 1 || !strings.Contains(sent[0], "已完成") {
		t.Fatalf("bound-session delivery = %v, want exactly 1 completion to chat-A", sent)
	}
	sentForA := provider.sentFor("chat-A")
	sentForB := provider.sentFor("chat-B")
	if len(sentForA) != 1 {
		t.Fatalf("chat-A must receive exactly 1, got %d", len(sentForA))
	}
	if len(sentForB) != 0 {
		t.Fatalf("chat-B (bystander) must NOT receive bound-session notice, got %v", sentForB)
	}
}