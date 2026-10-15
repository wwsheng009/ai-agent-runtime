package connectors

// S4（docs/bots/13 §3.3/§7.1）：stream 形态——assistant 增量 → 占位消息 +
// 节流编辑（≥1s）→ 终态正文做最终编辑；provider 不支持流式时退化为一次性
// content 投递；L3 IM 回合跳过；失败不审计、计 stream/failed 指标。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ── 测试替身 ──────────────────────────────────────────────────────────────

type fakeStreamMirrorSession struct {
	mu       sync.Mutex
	updates  []string
	finals   []string
	finalErr error
}

func (f *fakeStreamMirrorSession) Update(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, text)
	return nil
}

func (f *fakeStreamMirrorSession) Final(_ context.Context, msgs []OutboundMessage) error {
	if f.finalErr != nil {
		return f.finalErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, msg := range msgs {
		f.finals = append(f.finals, msg.Text)
	}
	return nil
}

func (f *fakeStreamMirrorSession) Fail(context.Context, string) error { return nil }

func (f *fakeStreamMirrorSession) updatesSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.updates...)
}

func (f *fakeStreamMirrorSession) finalsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.finals...)
}

type streamAuditCall struct {
	connectorID    string
	chat           string
	subscriptionID string
	dedupKey       string
	degraded       bool
}

type streamMirrorOptions struct {
	StartErr   error
	FinalErr   error
	Interval   time.Duration
	TurnActive func(sessionID string) bool
}

type streamMirrorHarness struct {
	mu       sync.Mutex
	opts     streamMirrorOptions
	starts   int
	sessions []*fakeStreamMirrorSession
	delivers []string
	audits   []streamAuditCall
	bus      *runtimeevents.Bus
	stopFn   func()
}

func newStreamMirrorHarness(t *testing.T, store Store, opts streamMirrorOptions) *streamMirrorHarness {
	t.Helper()
	h := &streamMirrorHarness{opts: opts, bus: runtimeevents.NewBusWithRetention(64)}
	eventTypes := []string{
		runtimechat.EventAssistantDelta, "assistant.delta",
		runtimechat.EventAssistantMessage, "assistant.message",
	}
	h.stopFn = SubscribeStreamMirror(context.Background(), StreamMirrorDeps{
		Subscribe: func(handler func(runtimeevents.Event)) func() {
			unsubs := make([]func(), 0, len(eventTypes))
			for _, eventType := range eventTypes {
				if unsub := h.bus.SubscribeCancelable(eventType, handler); unsub != nil {
					unsubs = append(unsubs, unsub)
				}
			}
			return func() {
				for _, unsub := range unsubs {
					unsub()
				}
			}
		},
		Store:      store,
		TurnActive: opts.TurnActive,
		Start: func(_ context.Context, _, _ string) (StreamingSession, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.starts++
			if opts.StartErr != nil {
				return nil, opts.StartErr
			}
			session := &fakeStreamMirrorSession{finalErr: opts.FinalErr}
			h.sessions = append(h.sessions, session)
			return session, nil
		},
		Deliver: func(_ context.Context, _, _, text string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.delivers = append(h.delivers, text)
			return nil
		},
		Audit: func(connectorID, chat, subscriptionID, dedupKey string, degraded bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.audits = append(h.audits, streamAuditCall{connectorID, chat, subscriptionID, dedupKey, degraded})
		},
		Interval: opts.Interval,
		Logf:     func(string, ...any) {},
	})
	t.Cleanup(h.stop)
	return h
}

func (h *streamMirrorHarness) stop() {
	if h.stopFn != nil {
		h.stopFn()
	}
}

func (h *streamMirrorHarness) publishDelta(sessionID, text string) {
	h.bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: sessionID,
		Timestamp: time.Now(),
		Payload:   map[string]any{"delta": text, "turn_id": "turn-" + sessionID},
	})
}

func (h *streamMirrorHarness) publishMessage(sessionID, text string) {
	h.bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: sessionID,
		Timestamp: time.Now(),
		Payload:   map[string]any{"content": text, "turn_id": "turn-" + sessionID},
	})
}

func (h *streamMirrorHarness) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func (h *streamMirrorHarness) counts() (starts int, sessions, delivers, audits int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.starts, len(h.sessions), len(h.delivers), len(h.audits)
}

func (h *streamMirrorHarness) totalFinals() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, session := range h.sessions {
		total += len(session.finalsSnapshot())
	}
	return total
}

func (h *streamMirrorHarness) session(index int) *fakeStreamMirrorSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	if index >= len(h.sessions) {
		return nil
	}
	return h.sessions[index]
}

// streamMirrorTestStore 构造一条 deliver=stream 订阅 + 会话绑定（默认连接器 c1）。
func streamMirrorTestStore(t *testing.T, sessionID, chat string) *MemoryStore {
	return streamMirrorTestStoreFor(t, "c1", sessionID, chat)
}

// streamMirrorTestStoreFor 指定连接器 id（指标断言用唯一 id 隔离，避免替换全局注册表
// 与跨测试残留 goroutine 竞争；见 delivery_metrics_test.go 隔离纪律）。
func streamMirrorTestStoreFor(t *testing.T, connectorID, sessionID, chat string) *MemoryStore {
	t.Helper()
	store := NewMemoryStore(testConfig(connectorID, "fake", func(cfg *Config) {
		cfg.Policy.Events = EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "live", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"assistant_delta"}, Deliver: "stream"},
		}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: connectorID, Chat: chat, Session: SessionRef{SessionID: sessionID}}); err != nil {
		t.Fatal(err)
	}
	return store
}

// ── 用例 ──────────────────────────────────────────────────────────────────

// 占位只开一次；首个增量立即编辑（后续在节流窗口内被合并）；终态正文做最终编辑；
// 成功投递审计一次（命中订阅 id + 去重身份 + 非降级）。
func TestStreamMirrorPlaceholderEditsAndFinal(t *testing.T) {
	store := streamMirrorTestStore(t, "sess-1", "chat-1")
	h := newStreamMirrorHarness(t, store, streamMirrorOptions{Interval: 500 * time.Millisecond})

	h.publishDelta("sess-1", "你")
	h.publishDelta("sess-1", "好")
	h.publishDelta("sess-1", "！")
	h.publishMessage("sess-1", "你好！")

	h.waitFor(t, "final edit + audit", func() bool {
		_, _, _, audits := h.counts()
		return h.totalFinals() == 1 && audits == 1
	})
	if starts, sessions, _, audits := h.counts(); starts != 1 || sessions != 1 || audits != 1 {
		t.Fatalf("starts/sessions/audits = %d/%d/%d, want 1/1/1", starts, sessions, audits)
	}
	session := h.session(0)
	updates := session.updatesSnapshot()
	if len(updates) != 1 || updates[0] != "你" {
		t.Fatalf("updates = %v, want [你]（500ms 窗口内其余增量被合并）", updates)
	}
	if finals := session.finalsSnapshot(); len(finals) != 1 || finals[0] != "你好！" {
		t.Fatalf("finals = %v, want [你好！]（终态正文权威）", finals)
	}
	h.mu.Lock()
	audit := h.audits[0]
	h.mu.Unlock()
	if audit.connectorID != "c1" || audit.chat != "chat-1" || audit.subscriptionID != "live" || audit.degraded {
		t.Fatalf("audit = %+v", audit)
	}
	if audit.dedupKey != "turn-sess-1" {
		t.Fatalf("audit dedup = %q, want turn-sess-1", audit.dedupKey)
	}
}

// provider 不支持流式（Start 报错）→ 退化：终态一次性投递；审计标 degraded=content。
func TestStreamMirrorDegradesWhenStartFails(t *testing.T) {
	store := streamMirrorTestStore(t, "sess-1", "chat-1")
	h := newStreamMirrorHarness(t, store, streamMirrorOptions{StartErr: errors.New("no streaming"), Interval: time.Millisecond})

	h.publishDelta("sess-1", "x")
	h.publishMessage("sess-1", "完整回复")

	h.waitFor(t, "degraded delivery", func() bool {
		_, _, delivers, audits := h.counts()
		return delivers == 1 && audits == 1
	})
	h.mu.Lock()
	delivered := append([]string(nil), h.delivers...)
	audit := h.audits[0]
	h.mu.Unlock()
	if delivered[0] != "完整回复" {
		t.Fatalf("delivers = %v, want [完整回复]", delivered)
	}
	if !audit.degraded || audit.subscriptionID != "live" {
		t.Fatalf("degraded audit = %+v", audit)
	}
	if starts, _, _, _ := h.counts(); starts != 1 {
		t.Fatalf("starts = %d, want 1（占位只尝试一次，不重试轰炸）", starts)
	}
}

// L3：IM 来源回合整回合跳过（正文已由回复路径投递，含流式）。
func TestStreamMirrorSkipsIMTurn(t *testing.T) {
	store := streamMirrorTestStore(t, "sess-1", "chat-1")
	h := newStreamMirrorHarness(t, store, streamMirrorOptions{TurnActive: func(string) bool { return true }})

	h.publishDelta("sess-1", "x")
	h.publishMessage("sess-1", "x")
	time.Sleep(200 * time.Millisecond)
	if starts, sessions, delivers, audits := h.counts(); starts != 0 || sessions != 0 || delivers != 0 || audits != 0 {
		t.Fatalf("IM turn must be skipped: starts/sessions/delivers/audits = %d/%d/%d/%d", starts, sessions, delivers, audits)
	}
}

// 终态编辑失败：计 stream/failed 指标、不审计（审计只在成功路径）。
func TestStreamMirrorFinalFailureCountsFailedAndNoAudit(t *testing.T) {
	store := streamMirrorTestStoreFor(t, "c-stream-fail", "sess-1", "chat-1")
	h := newStreamMirrorHarness(t, store, streamMirrorOptions{FinalErr: errors.New("edit failed"), Interval: time.Millisecond})

	h.publishDelta("sess-1", "x")
	h.publishMessage("sess-1", "y")

	waitForMetric(t, "stream failed", func() float64 {
		return deliveryCounter("c-stream-fail", DeliveryFormStream, deliveryOutcomeFailed)
	}, 1)
	if _, _, _, audits := h.counts(); audits != 0 {
		t.Fatalf("failed edit must not audit, audits = %d", audits)
	}
	if got := deliveryCounter("c-stream-fail", DeliveryFormStream, deliveryOutcomeSent); got != 0 {
		t.Fatalf("stream sent = %v, want 0", got)
	}
}

// 同聊天重绑到新会话（/new 或重新绑定）：上一回合未收到终态时，新会话的首个
// 增量必须先收尾旧流（用累积文本），再开新流——避免旧会话的占位消息永久悬挂。
func TestStreamMirrorChatRebindFinalizesOldStream(t *testing.T) {
	store := streamMirrorTestStore(t, "sess-1", "chat-1")
	h := newStreamMirrorHarness(t, store, streamMirrorOptions{Interval: time.Millisecond})

	h.publishDelta("sess-1", "旧回合")
	h.waitFor(t, "stream A update", func() bool {
		session := h.session(0)
		return session != nil && len(session.updatesSnapshot()) == 1
	})
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-2"}}); err != nil {
		t.Fatal(err)
	}
	h.publishDelta("sess-2", "新回合")
	h.waitFor(t, "old stream finalized", func() bool {
		_, _, _, audits := h.counts()
		return h.totalFinals() == 1 && audits == 1
	})
	h.publishMessage("sess-2", "新回合完成")
	h.waitFor(t, "new stream finalized", func() bool {
		_, _, _, audits := h.counts()
		return h.totalFinals() == 2 && audits == 2
	})

	first := h.session(0)
	if finals := first.finalsSnapshot(); len(finals) != 1 || finals[0] != "旧回合" {
		t.Fatalf("old stream finals = %v, want [旧回合]", finals)
	}
	second := h.session(1)
	if finals := second.finalsSnapshot(); len(finals) != 1 || finals[0] != "新回合完成" {
		t.Fatalf("new stream finals = %v, want [新回合完成]", finals)
	}
	h.mu.Lock()
	audits := append([]streamAuditCall(nil), h.audits...)
	h.mu.Unlock()
	if len(audits) != 2 || audits[0].dedupKey != "turn-sess-1" || audits[1].dedupKey != "turn-sess-2" {
		t.Fatalf("audits = %+v", audits)
	}
}

// 订阅面必须显式声明（无 legacy 等价物）：未配置 events 时即使绑定了会话也不投递。
func TestStreamMirrorRequiresExplicitSubscription(t *testing.T) {
	store := NewMemoryStore(testConfig("c1", "fake"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	h := newStreamMirrorHarness(t, store, streamMirrorOptions{Interval: time.Millisecond})

	h.publishDelta("sess-1", "x")
	h.publishMessage("sess-1", "y")
	time.Sleep(200 * time.Millisecond)
	if starts, _, delivers, audits := h.counts(); starts != 0 || delivers != 0 || audits != 0 {
		t.Fatalf("stream must require explicit subscription: starts/delivers/audits = %d/%d/%d", starts, delivers, audits)
	}
}

// 服务层记账：RecordStreamDelivery 计 stream/sent 指标 + ConnectorLog（含降级标注）。
func TestRecordStreamDeliveryMetricAndLog(t *testing.T) {
	service := newTestService(t, NewMemoryStore(testConfig("c-stream-log", "fake")), &fakeBackend{}, &fakeProvider{})

	service.RecordStreamDelivery("c-stream-log", "chat-1", "live", "turn-1", false)
	service.RecordStreamDelivery("c-stream-log", "chat-2", "live", "turn-2", true)

	if got := deliveryCounter("c-stream-log", DeliveryFormStream, deliveryOutcomeSent); got != 2 {
		t.Fatalf("stream sent = %v, want 2", got)
	}
	entries, ok := service.Logs("c-stream-log", 10)
	if !ok {
		t.Fatal("logs missing for c1")
	}
	sawDegraded := false
	sawPlain := false
	for _, entry := range entries {
		if entry.Direction != LogDirectionOut || !strings.Contains(entry.Summary, "流式投递（subscription=live") {
			continue
		}
		if strings.Contains(entry.Summary, "degraded=content") {
			sawDegraded = true
		} else {
			sawPlain = true
		}
	}
	if !sawPlain || !sawDegraded {
		t.Fatalf("stream audit entries missing (plain=%v degraded=%v): %+v", sawPlain, sawDegraded, entries)
	}
}
