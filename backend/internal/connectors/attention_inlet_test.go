package connectors

import (
	"strings"
	"testing"
)

// TestInletAttentionDeliversToAcceptedConnectorChats 钉死跨宿主接收面
// （P2 切片 D 的 InletAttention）与本地订阅链路同一个投递语义
// （docs/bots/09 §7.4.1 D5；2026-10-14 修复补全）：对端转发来的
// "其它会话"（本机未绑定）完成事件，被本机 bot（notify enabled +
// scope=all）接受后必须送达**该 bot 的绑定 chat**——修复前 inlet 经
// NotifyAttention 按事件会话反查绑定，跨宿主未绑定会话同样恒 0 投递。
func TestInletAttentionDeliversToAcceptedConnectorChats(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(cfg *Config) {
		cfg.Policy.Notify = NotifyPolicy{Enabled: true, Scope: NotifyScopeAll, Kinds: []string{AttentionKindCompletion}}
	}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-bot"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)
	// 与宿主接线同构的出站：scoped 投递（先精确、未命中回退广播）。
	inlet := InletAttention(store, service.NotifyAttentionScoped)
	if inlet == nil {
		t.Fatal("inlet must not be nil with store+notify")
	}
	// 对端转发来的通知：事件会话属于对端（本机无绑定）。
	if err := inlet(AttentionNotice{
		Kind:    AttentionKindCompletion,
		Session: SessionRef{SessionID: "sess-remote-B"},
		Text:    "会话已完成",
	}); err != nil {
		t.Fatalf("inlet: %v", err)
	}
	sent := provider.sentFor("chat-1")
	texts := provider.sentTexts()
	if len(sent) == 0 || len(texts) == 0 || !strings.Contains(texts[0], "已完成") {
		t.Fatalf("cross-host notice must reach c1's bound chat; sent=%v", texts)
	}
}

// TestInletAttentionSkipsWhenNoConnectorAccepts 防回归：无连接器接受
// （notify 零值）时不得投递（静默成功，广播尽力而为）。
func TestInletAttentionSkipsWhenNoConnectorAccepts(t *testing.T) {
	provider := &fakeProvider{}
	backend := &fakeBackend{}
	store := NewMemoryStore(testConfig("c1", "fake", func(*Config) {}))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-bot"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)
	inlet := InletAttention(store, service.NotifyAttentionScoped)
	if err := inlet(AttentionNotice{Kind: AttentionKindCompletion,
		Session: SessionRef{SessionID: "sess-remote-B"}}); err != nil {
		t.Fatalf("inlet: %v", err)
	}
	if sent := provider.sentTexts(); len(sent) != 0 {
		t.Fatalf("no connector accepts → no delivery, got %v", sent)
	}
}