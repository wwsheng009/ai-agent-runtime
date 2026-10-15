package connectors

// S4 指标面（docs/bots/13 §7.4 D10）：投递面进程指标——
// `connectors_deliveries_total{connector,form,outcome}`（content/notice/digest ×
// sent/failed）与 `connectors_digest_merged_total{connector}`（被聚合吸收的事件数）；
// 并锁定 digest 聚合广播的成功投递审计（`AuditBroadcast`，批内会话数进 ConnectorLog）。
//
// 隔离纪律（2026-10-15，-race 教训）：不替换 observability.GlobalMetrics——跨测试
// 残留的 digest 计时器 goroutine 会与替换写入竞争；改为每个用例使用**唯一 connector
// id**（label 维度隔离），计数器从 0 起算，断言不依赖全局重置。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

func deliveryCounter(connectorID, form, outcome string) float64 {
	return observability.GlobalMetrics.GetOrCreateCounter(observability.MetricConnectorsDeliveriesTotal, map[string]string{
		observability.LabelConnector: connectorID,
		observability.LabelForm:      form,
		observability.LabelOutcome:   outcome,
	}).Get()
}

func digestMergedCounter(connectorID string) float64 {
	return observability.GlobalMetrics.GetOrCreateCounter(observability.MetricConnectorsDigestMergedTotal, map[string]string{
		observability.LabelConnector: connectorID,
	}).Get()
}

func waitForMetric(t *testing.T, what string, read func() float64, want float64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if read() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s = %v (got %v)", what, want, read())
}

// TestRecordDeliveryMetricsCoverAllForms：三种形态的成功投递各计一次 sent；
// 空连接器不产生样本（指标不得出现空 label）；digest 审计落 ConnectorLog。
func TestRecordDeliveryMetricsCoverAllForms(t *testing.T) {
	const cid = "c-forms"
	service := newTestService(t, NewMemoryStore(testConfig(cid, "fake")), &fakeBackend{}, &fakeProvider{})

	service.RecordSubscriptionDelivery(cid, "chat-1", "main", "turn-1")
	service.RecordAttentionDelivery(cid, "approval", "sess-1", "bound-command-loop")
	service.RecordAttentionDigestDelivery(cid, "ops", 3)

	if got := deliveryCounter(cid, DeliveryFormContent, deliveryOutcomeSent); got != 1 {
		t.Fatalf("content sent = %v, want 1", got)
	}
	if got := deliveryCounter(cid, DeliveryFormNotice, deliveryOutcomeSent); got != 1 {
		t.Fatalf("notice sent = %v, want 1", got)
	}
	if got := deliveryCounter(cid, DeliveryFormDigest, deliveryOutcomeSent); got != 1 {
		t.Fatalf("digest sent = %v, want 1", got)
	}
	if got := deliveryCounter(cid, DeliveryFormContent, deliveryOutcomeFailed); got != 0 {
		t.Fatalf("content failed = %v, want 0", got)
	}

	// 空连接器：不产生样本（trim 后为空即跳过）。
	service.RecordSubscriptionDelivery("", "chat-1", "main", "turn-1")
	if got := deliveryCounter("", DeliveryFormContent, deliveryOutcomeSent); got != 0 {
		t.Fatalf("empty connector produced sample = %v", got)
	}

	// digest 审计（连接器日志）：批内计数 + 订阅 id 落 out 方向条目。
	entries, ok := service.Logs(cid, 10)
	if !ok {
		t.Fatal("logs missing for " + cid)
	}
	found := false
	for _, entry := range entries {
		if entry.Direction == LogDirectionOut &&
			strings.Contains(entry.Summary, "digest=3") &&
			strings.Contains(entry.Summary, "subscription=ops") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("digest audit entry missing: %+v", entries)
	}
}

// TestAttentionDeliveryFailureMetrics：通知投递失败按目标连接器计 failed，
// 且不产生 sent（审计同样只在成功路径）。
func TestAttentionDeliveryFailureMetrics(t *testing.T) {
	const cid = "c-attn-fail"
	store := NewMemoryStore(testConfig(cid, "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "watch", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"turn.*"}, Deliver: "notice"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: cid, Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	bus := runtimeevents.NewBusWithRetention(8)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimeobserve.EventAgentTurnDone, handler)
		},
		Store: store,
		NotifyScoped: func(context.Context, []string, AttentionNotice) error {
			return errors.New("boom")
		},
	})
	defer sub()

	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "sess-1", Timestamp: time.Now(), Payload: map[string]any{"turn_id": "t1"}})
	waitForMetric(t, "notice failed", func() float64 {
		return deliveryCounter(cid, DeliveryFormNotice, deliveryOutcomeFailed)
	}, 1)
	if got := deliveryCounter(cid, DeliveryFormNotice, deliveryOutcomeSent); got != 0 {
		t.Fatalf("failed delivery must not count sent, got %v", got)
	}
}

// TestDigestBroadcastMetricsAndAudit：聚合窗口吸收 3 个事件（merged=3），
// flush 成功计 digest sent 并回调 AuditBroadcast（connector/订阅/计数）。
func TestDigestBroadcastMetricsAndAudit(t *testing.T) {
	const cid = "c-digest-agg"
	store := NewMemoryStore(testConfig(cid, "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "ops", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-a", "sess-b", "sess-c"}}, Types: []string{"turn.*"}, Deliver: "digest"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: cid, Chat: "chat-ops", Session: SessionRef{SessionID: "sess-bot"}}); err != nil {
		t.Fatal(err)
	}

	type auditCall struct {
		connectorID    string
		subscriptionID string
		count          int
	}
	auditCh := make(chan auditCall, 4)
	bus := runtimeevents.NewBusWithRetention(16)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimeobserve.EventAgentTurnDone, handler)
		},
		Store:           store,
		NotifyScoped:    func(context.Context, []string, AttentionNotice) error { return nil },
		NotifyBroadcast: func(context.Context, []string, AttentionNotice) error { return nil },
		AuditBroadcast: func(connectorID, subscriptionID string, count int) {
			auditCh <- auditCall{connectorID, subscriptionID, count}
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

	select {
	case call := <-auditCh:
		if call.connectorID != cid || call.subscriptionID != "ops" || call.count != 3 {
			t.Fatalf("digest audit = %+v", call)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("digest audit not fired")
	}
	if got := deliveryCounter(cid, DeliveryFormDigest, deliveryOutcomeSent); got != 1 {
		t.Fatalf("digest sent = %v, want 1", got)
	}
	if got := digestMergedCounter(cid); got != 3 {
		t.Fatalf("digest merged = %v, want 3", got)
	}
	if got := deliveryCounter(cid, DeliveryFormDigest, deliveryOutcomeFailed); got != 0 {
		t.Fatalf("digest failed = %v, want 0", got)
	}
}

// TestDigestBroadcastFailureMetrics：广播失败计 digest failed、不审计；
// 吸收事件仍计 merged（聚合读数与投递结果独立）。
func TestDigestBroadcastFailureMetrics(t *testing.T) {
	const cid = "c-digest-fail"
	store := NewMemoryStore(testConfig(cid, "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "ops", Sessions: SessionSelector{Mode: "ids", IDs: []string{"sess-a"}}, Types: []string{"turn.*"}, Deliver: "digest"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: cid, Chat: "chat-ops", Session: SessionRef{SessionID: "sess-bot"}}); err != nil {
		t.Fatal(err)
	}
	auditCh := make(chan struct{}, 1)
	bus := runtimeevents.NewBusWithRetention(8)
	sub := SubscribeAttention(context.Background(), AttentionDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimeobserve.EventAgentTurnDone, handler)
		},
		Store:        store,
		NotifyScoped: func(context.Context, []string, AttentionNotice) error { return nil },
		NotifyBroadcast: func(context.Context, []string, AttentionNotice) error {
			return errors.New("broadcast down")
		},
		AuditBroadcast: func(string, string, int) { auditCh <- struct{}{} },
		DigestWindow:   40 * time.Millisecond,
	})
	defer sub()

	bus.Publish(runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "sess-a", Timestamp: time.Now(), Payload: map[string]any{"turn_id": "t1"}})
	waitForMetric(t, "digest failed", func() float64 {
		return deliveryCounter(cid, DeliveryFormDigest, deliveryOutcomeFailed)
	}, 1)
	if got := deliveryCounter(cid, DeliveryFormDigest, deliveryOutcomeSent); got != 0 {
		t.Fatalf("failed broadcast must not count sent, got %v", got)
	}
	select {
	case <-auditCh:
		t.Fatal("failed broadcast must not audit")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestContentMirrorFailureMetrics：内容副本入队失败计 content failed，
// 不产生 sent（认领释放后审计也只在成功路径）。
func TestContentMirrorFailureMetrics(t *testing.T) {
	const cid = "c-content-fail"
	store := NewMemoryStore(testConfig(cid, "fake", func(cfg *Config) {
		cfg.Policy.ContentMirror = true
	}))
	if err := store.PutBinding(Binding{ConnectorID: cid, Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	bus := runtimeevents.NewBusWithRetention(8)
	sub := SubscribeContentMirror(context.Background(), ContentMirrorDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			return bus.SubscribeCancelable(runtimechat.EventAssistantMessage, handler)
		},
		Store: store,
		Deliver: func(context.Context, string, string, string) error {
			return errors.New("queue full")
		},
	})
	defer sub()

	bus.Publish(runtimeevents.Event{Type: runtimechat.EventAssistantMessage, SessionID: "sess-1", Payload: map[string]any{"content": "hello", "turn_id": "t1"}})
	waitForMetric(t, "content failed", func() float64 {
		return deliveryCounter(cid, DeliveryFormContent, deliveryOutcomeFailed)
	}, 1)
	if got := deliveryCounter(cid, DeliveryFormContent, deliveryOutcomeSent); got != 0 {
		t.Fatalf("failed enqueue must not count sent, got %v", got)
	}
}
