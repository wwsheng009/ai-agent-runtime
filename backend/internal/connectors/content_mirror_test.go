package connectors

// 契约/回归（docs/bots/13 §8 S1a）：会话内容镜像（content 形态）共享实现。
// 覆盖：逐 bot 开关（policy.content_mirror）、绑定会话匹配、L3（IM 回合跳过）、
// L1+L2 去重（同事件同目标一条）、空正文跳过、点分隔别名、入队失败释放认领。

import (
	"context"
	"errors"
	"sync"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

type contentMirrorCall struct {
	connectorID string
	chat        string
	text        string
}

type contentMirrorRecorder struct {
	mu       sync.Mutex
	calls    []contentMirrorCall
	failNext error
}

func (r *contentMirrorRecorder) deliver(_ context.Context, connectorID, chat, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	r.calls = append(r.calls, contentMirrorCall{connectorID: connectorID, chat: chat, text: text})
	return nil
}

func (r *contentMirrorRecorder) snapshot() []contentMirrorCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contentMirrorCall(nil), r.calls...)
}

func mirrorEvent(sessionID, turnID, content string) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"turn_id": turnID, "content": content},
	}
}

// captureMirrorHandler 装配订阅并返回可手动触发的 handler 与退订函数。
func captureMirrorHandler(t *testing.T, deps ContentMirrorDeps) (func(runtimeevents.Event), func()) {
	t.Helper()
	var handler func(runtimeevents.Event)
	deps.Subscribe = func(h func(runtimeevents.Event)) func() {
		handler = h
		return func() {}
	}
	unsubscribe := SubscribeContentMirror(context.Background(), deps)
	if handler == nil {
		t.Fatal("subscribe handler not captured")
	}
	return handler, unsubscribe
}

func TestContentMirrorDeliversToBoundChatsOfEnabledBots(t *testing.T) {
	store := NewMemoryStore(
		Config{ID: "c1", Provider: "fake", Enabled: true, Policy: Policy{ContentMirror: true}},
		Config{ID: "c2", Provider: "fake", Enabled: true},                                       // 开关缺省关
		Config{ID: "c3", Provider: "fake", Enabled: false, Policy: Policy{ContentMirror: true}}, // 停用
	)
	for _, binding := range []Binding{
		{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}},
		{ConnectorID: "c2", Chat: "chat-2", Session: SessionRef{SessionID: "sess-1"}},
		{ConnectorID: "c3", Chat: "chat-3", Session: SessionRef{SessionID: "sess-1"}},
		{ConnectorID: "c1", Chat: "chat-other", Session: SessionRef{SessionID: "sess-2"}},
	} {
		if err := store.PutBinding(binding); err != nil {
			t.Fatalf("PutBinding: %v", err)
		}
	}

	recorder := &contentMirrorRecorder{}
	handler, unsubscribe := captureMirrorHandler(t, ContentMirrorDeps{
		Store:   store,
		Deliver: recorder.deliver,
	})
	defer unsubscribe()

	handler(mirrorEvent("sess-1", "turn-1", "答案一"))
	calls := recorder.snapshot()
	if len(calls) != 1 || calls[0].connectorID != "c1" || calls[0].chat != "chat-1" || calls[0].text != "答案一" {
		t.Fatalf("mirror delivery mismatch: %+v", calls)
	}

	// 同一事件重复投递（总线重放/重复订阅）→ L1+L2 去重。
	handler(mirrorEvent("sess-1", "turn-1", "答案一"))
	if got := recorder.snapshot(); len(got) != 1 {
		t.Fatalf("duplicate event must be deduped, got %+v", got)
	}

	// 新回合（新 turn_id）→ 再投。
	handler(mirrorEvent("sess-1", "turn-2", "答案二"))
	if got := recorder.snapshot(); len(got) != 2 || got[1].text != "答案二" {
		t.Fatalf("second turn must deliver, got %+v", got)
	}

	// 另一绑定会话（sess-2 → c1/chat-other）独立投递：会话不串（按 SessionID 匹配）。
	handler(mirrorEvent("sess-2", "turn-9", "别处"))
	if got := recorder.snapshot(); len(got) != 3 || got[2].chat != "chat-other" || got[2].text != "别处" {
		t.Fatalf("sess-2 must mirror to its own bound chat, got %+v", got)
	}

	// 未绑定会话 → 不投。
	handler(mirrorEvent("sess-3", "turn-10", "无绑定"))
	if got := recorder.snapshot(); len(got) != 3 {
		t.Fatalf("unbound session must not mirror, got %+v", got)
	}
}

func TestContentMirrorSkipsTurnActiveEmptyContentAndAlias(t *testing.T) {
	store := NewMemoryStore(Config{ID: "c1", Provider: "fake", Enabled: true, Policy: Policy{ContentMirror: true}})
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatalf("PutBinding: %v", err)
	}

	active := true
	recorder := &contentMirrorRecorder{}
	handler, unsubscribe := captureMirrorHandler(t, ContentMirrorDeps{
		Store:      store,
		Deliver:    recorder.deliver,
		TurnActive: func(string) bool { return active },
	})
	defer unsubscribe()

	// L3（13 §4.2）：IM 来源回合进行中 → 整回合跳过。
	handler(mirrorEvent("sess-1", "turn-im", "IM 回合的答案"))
	if got := recorder.snapshot(); len(got) != 0 {
		t.Fatalf("IM turn must be skipped (L3), got %+v", got)
	}

	active = false
	// 空正文（reasoning-only 回合）→ 不投。
	handler(mirrorEvent("sess-1", "turn-empty", ""))
	// 点分隔别名（assistant.message）同样被接受，且恰好一条。
	alias := mirrorEvent("sess-1", "turn-alias", "别名正文")
	alias.Type = "assistant.message"
	handler(alias)
	got := recorder.snapshot()
	if len(got) != 1 || got[0].text != "别名正文" {
		t.Fatalf("alias event must deliver exactly once, got %+v", got)
	}
}

func TestContentMirrorReleasesClaimOnEnqueueFailure(t *testing.T) {
	store := NewMemoryStore(Config{ID: "c1", Provider: "fake", Enabled: true, Policy: Policy{ContentMirror: true}})
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatalf("PutBinding: %v", err)
	}

	recorder := &contentMirrorRecorder{failNext: errors.New("queue full")}
	var logMu sync.Mutex
	var logs []string
	handler, unsubscribe := captureMirrorHandler(t, ContentMirrorDeps{
		Store:   store,
		Deliver: recorder.deliver,
		Logf: func(format string, args ...any) {
			logMu.Lock()
			logs = append(logs, format)
			logMu.Unlock()
		},
	})
	defer unsubscribe()

	event := mirrorEvent("sess-1", "turn-1", "内容")
	handler(event) // 第一次入队失败 → 释放认领。
	if got := recorder.snapshot(); len(got) != 0 {
		t.Fatalf("failed enqueue must not deliver, got %+v", got)
	}
	handler(event) // 重试成功。
	got := recorder.snapshot()
	if len(got) != 1 || got[0].text != "内容" {
		t.Fatalf("retry after failed enqueue must deliver, got %+v", got)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if len(logs) != 1 {
		t.Fatalf("failure must be logged once, got %v", logs)
	}
}

// S2（docs/bots/13 §3.5/§4.1）：声明式 events 订阅驱动正文镜像——
// 族通配命中、形态过滤（notice 不投正文）、events 配置后不回退 legacy 开关。
func TestContentMirrorHonorsDeclarativeSubscriptions(t *testing.T) {
	store := NewMemoryStore(
		// c1：声明式 content 订阅（conversation.* 族通配）。
		Config{ID: "c1", Provider: "fake", Enabled: true, Policy: Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "main", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"conversation.*"}, Deliver: "content"},
		}}}},
		// c2：legacy content_mirror（未配置 events，继续生效）。
		Config{ID: "c2", Provider: "fake", Enabled: true, Policy: Policy{ContentMirror: true}},
		// c3：events 配置但形态是 notice → 不投正文。
		Config{ID: "c3", Provider: "fake", Enabled: true, Policy: Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "watch", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "notice"},
		}}}},
		// c4：events 配置后 content_mirror 被替代（不再回退 legacy 开关，双读单写）。
		Config{ID: "c4", Provider: "fake", Enabled: true, Policy: Policy{ContentMirror: true, Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "turn-only", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"turn.*"}, Deliver: "notice"},
		}}}},
	)
	for _, binding := range []Binding{
		{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}},
		{ConnectorID: "c2", Chat: "chat-2", Session: SessionRef{SessionID: "sess-1"}},
		{ConnectorID: "c3", Chat: "chat-3", Session: SessionRef{SessionID: "sess-1"}},
		{ConnectorID: "c4", Chat: "chat-4", Session: SessionRef{SessionID: "sess-1"}},
	} {
		if err := store.PutBinding(binding); err != nil {
			t.Fatalf("PutBinding: %v", err)
		}
	}

	recorder := &contentMirrorRecorder{}
	handler, unsubscribe := captureMirrorHandler(t, ContentMirrorDeps{Store: store, Deliver: recorder.deliver})
	defer unsubscribe()

	handler(mirrorEvent("sess-1", "turn-1", "声明式正文"))
	got := recorder.snapshot()
	if len(got) != 2 {
		t.Fatalf("declarative+legacy content must deliver to c1+c2 only, got %+v", got)
	}
	targets := map[string]string{}
	for _, call := range got {
		targets[call.connectorID] = call.chat
	}
	if targets["c1"] != "chat-1" || targets["c2"] != "chat-2" {
		t.Fatalf("delivery targets mismatch: %+v", got)
	}
	if _, ok := targets["c3"]; ok {
		t.Fatalf("notice-only subscription must not receive content: %+v", got)
	}
	if _, ok := targets["c4"]; ok {
		t.Fatalf("events-configured bot must not fall back to legacy content_mirror: %+v", got)
	}
}

// S4（docs/bots/13 §7.4）：投递审计——成功入队才记（含命中订阅 id 与去重身份）；
// 入队失败释放认领且不审计。
func TestContentMirrorAuditsDeliveries(t *testing.T) {
	store := NewMemoryStore(
		Config{ID: "c1", Provider: "fake", Enabled: true, Policy: Policy{Events: EventsPolicy{Enabled: true, Subscriptions: []EventSubscription{
			{ID: "main", Sessions: SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "content"},
		}}}},
		Config{ID: "c2", Provider: "fake", Enabled: true, Policy: Policy{ContentMirror: true}},
	)
	for _, binding := range []Binding{
		{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-1"}},
		{ConnectorID: "c2", Chat: "chat-2", Session: SessionRef{SessionID: "sess-1"}},
	} {
		if err := store.PutBinding(binding); err != nil {
			t.Fatalf("PutBinding: %v", err)
		}
	}

	type auditEntry struct{ connectorID, chat, subscriptionID, dedupKey string }
	var audits []auditEntry
	recorder := &contentMirrorRecorder{}
	handler, unsubscribe := captureMirrorHandler(t, ContentMirrorDeps{
		Store:   store,
		Deliver: recorder.deliver,
		Audit: func(connectorID, chat, subscriptionID, dedupKey string) {
			audits = append(audits, auditEntry{connectorID, chat, subscriptionID, dedupKey})
		},
	})
	defer unsubscribe()

	handler(mirrorEvent("sess-1", "turn-audit", "审计"))
	if len(audits) != 2 {
		t.Fatalf("audits = %+v", audits)
	}
	byConnector := map[string]auditEntry{}
	for _, entry := range audits {
		byConnector[entry.connectorID] = entry
		if entry.dedupKey != "turn-audit" {
			t.Fatalf("dedup key mismatch: %+v", entry)
		}
	}
	if byConnector["c1"].subscriptionID != "main" || byConnector["c1"].chat != "chat-1" {
		t.Fatalf("declarative audit mismatch: %+v", byConnector["c1"])
	}
	if byConnector["c2"].subscriptionID != "legacy-content" || byConnector["c2"].chat != "chat-2" {
		t.Fatalf("legacy audit mismatch: %+v", byConnector["c2"])
	}

	// 入队失败：释放认领、不审计（同一事件后续重试仍可投递）。
	recorder.failNext = errors.New("queue full")
	audits = audits[:0]
	handler(mirrorEvent("sess-1", "turn-audit-2", "审计二"))
	if len(audits) != 1 {
		t.Fatalf("failed delivery must not audit, got %+v", audits)
	}
}
