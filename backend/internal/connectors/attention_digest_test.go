package connectors

// S4 端到端（docs/bots/13 §7.1）：digest 聚合——窗口内多个会话的 completion
// 合并为一条广播（带计数与会话标签）；交互类通知不聚合（即时投递）。

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

func TestSubscribeAttentionDigestAggregatesCompletions(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "ops", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-a", "sess-b", "sess-c"}}, Types: []string{"turn.*", "approval"}, Deliver: "digest"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-ops", Session: SessionRef{SessionID: "sess-bot"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	type broadcastCall struct {
		connectorIDs []string
		notice       AttentionNotice
	}
	broadcastCh := make(chan broadcastCall, 4)
	bus := runtimeevents.NewBusWithRetention(32)
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
		NotifyBroadcast: func(_ context.Context, connectorIDs []string, notice AttentionNotice) error {
			broadcastCh <- broadcastCall{connectorIDs: append([]string(nil), connectorIDs...), notice: notice}
			return nil
		},
		DigestWindow: 40 * time.Millisecond,
	})
	defer sub()

	for _, sessionID := range []string{"sess-a", "sess-b", "sess-c"} {
		bus.Publish(runtimeevents.Event{
			Type:      runtimeobserve.EventAgentTurnDone,
			SessionID: sessionID,
			Timestamp: time.Now(),
			Payload:   map[string]any{"turn_id": "turn-" + sessionID},
		})
	}

	var call broadcastCall
	select {
	case call = <-broadcastCh:
	case <-time.After(3 * time.Second):
		t.Fatal("digest flush did not fire")
	}
	if len(call.connectorIDs) != 1 || call.connectorIDs[0] != "c1" {
		t.Fatalf("broadcast connectors = %v", call.connectorIDs)
	}
	if !strings.Contains(call.notice.Text, "3 个会话回合已收尾") {
		t.Fatalf("digest text = %q", call.notice.Text)
	}
	for _, label := range []string{"sess-a", "sess-b", "sess-c"} {
		if !strings.Contains(call.notice.Text, label) {
			t.Fatalf("digest text missing %s: %q", label, call.notice.Text)
		}
	}
	// 聚合命中不得产生即时投递；也不得二次广播。
	if sent := provider.sentTexts(); len(sent) != 0 {
		t.Fatalf("digest must not send immediate notices, got %v", sent)
	}
	select {
	case extra := <-broadcastCh:
		t.Fatalf("unexpected extra broadcast: %+v", extra)
	case <-time.After(150 * time.Millisecond):
	}

	// 交互类（approval）：即便命中 digest 订阅也即时投递（安全优先）。
	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "sess-a",
		Timestamp: time.Now(),
		Payload:   map[string]any{"request_id": "apr-1", "tool_name": "shell"},
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, text := range provider.sentTexts() {
			if strings.Contains(text, "待审批") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("approval must deliver immediately; sent=%v", provider.sentTexts())
}
