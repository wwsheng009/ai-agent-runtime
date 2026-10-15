package connectors

// S4（docs/bots/13 §7.4）：注意力通知成功投递审计——按目标逐连接器记录命中
// 订阅 id（events 订阅 / 恒开 / legacy 路径各自 id）；投递失败不审计。

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

func TestSubscribeAttentionAuditsDeliveries(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "watch", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"turn.*"}, Deliver: "notice"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	type auditCall struct{ connectorID, kind, sessionID, subscriptionID string }
	auditCh := make(chan auditCall, 8)
	bus := runtimeevents.NewBusWithRetention(16)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			unsubscribes := []func(){
				bus.SubscribeCancelable(runtimeobserve.EventAgentTurnDone, handler),
				bus.SubscribeCancelable(runtimechat.EventApprovalRequested, handler),
			}
			return func() {
				for _, unsubscribe := range unsubscribes {
					unsubscribe()
				}
			}
		},
		Store:        store,
		NotifyScoped: service.NotifyAttentionScoped,
		Audit: func(connectorID, kind, sessionID, subscriptionID string) {
			auditCh <- auditCall{connectorID, kind, sessionID, subscriptionID}
		},
	})
	defer sub()

	// events notice 订阅命中（completion）。
	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "sess-1", Timestamp: time.Now(), Payload: map[string]any{"turn_id": "t1"}})
	// 绑定会话 approval 恒开（合成 id "bound-command-loop"）。
	bus.Publish(runtimeevents.Event{Type: runtimechat.EventApprovalRequested, SessionID: "sess-1", Timestamp: time.Now(), Payload: map[string]any{"request_id": "apr-1"}})

	got := map[string]auditCall{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(got) < 2 {
		select {
		case call := <-auditCh:
			got[call.kind+"/"+call.subscriptionID] = call
		case <-time.After(20 * time.Millisecond):
		}
	}
	if len(got) != 2 {
		t.Fatalf("audit calls = %+v", got)
	}
	if turn := got["completion/watch"]; turn.connectorID != "c1" || turn.sessionID != "sess-1" {
		t.Fatalf("turn audit mismatch: %+v", turn)
	}
	if approval := got["approval/bound-command-loop"]; approval.connectorID != "c1" || approval.sessionID != "sess-1" {
		t.Fatalf("approval audit mismatch: %+v", approval)
	}
}

func TestSubscribeAttentionAuditSkippedOnDeliveryFailure(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "watch", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"turn.*"}, Deliver: "notice"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	auditCh := make(chan struct{}, 1)
	bus := runtimeevents.NewBusWithRetention(8)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimeobserve.EventAgentTurnDone, handler)
		},
		Store: store,
		NotifyScoped: func(context.Context, []string, AttentionNotice) error {
			return errors.New("boom")
		},
		Audit: func(string, string, string, string) {
			auditCh <- struct{}{}
		},
	})
	defer sub()

	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "sess-1", Timestamp: time.Now(), Payload: map[string]any{"turn_id": "t1"}})
	select {
	case <-auditCh:
		t.Fatal("failed delivery must not audit")
	case <-time.After(300 * time.Millisecond):
	}
}
