package connectors

// S4（docs/bots/13 §7.1）：NotifyBroadcast / NotifyAttentionBroadcast——
// 多连接器广播、重复连接器去重、空参 no-op、单点失败聚合。

import (
	"context"
	"testing"
)

func TestServiceNotifyBroadcast(t *testing.T) {
	store := NewMemoryStore(
		Config{ID: "c1", Provider: "fake", Enabled: true},
		Config{ID: "c2", Provider: "fake", Enabled: true},
	)
	for _, binding := range []Binding{
		{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-a"}},
		{ConnectorID: "c1", Chat: "chat-9", Session: SessionRef{SessionID: "sess-a"}},
		{ConnectorID: "c2", Chat: "chat-2", Session: SessionRef{SessionID: "sess-b"}},
	} {
		if err := store.PutBinding(binding); err != nil {
			t.Fatalf("PutBinding: %v", err)
		}
	}
	provider := &fakeProvider{}
	service := New(Deps{Store: store})
	if err := service.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 重复连接器 id 与空串：同 chat 只投一次。
	delivered, err := service.NotifyBroadcast(context.Background(), []string{"c1", "c1", "c2", ""}, []OutboundMessage{{Text: "digest"}})
	if err != nil {
		t.Fatalf("NotifyBroadcast: %v", err)
	}
	if delivered != 3 {
		t.Fatalf("delivered = %d, want 3", delivered)
	}
	if got := len(provider.sentFor("chat-1")); got != 1 {
		t.Fatalf("chat-1 deliveries = %d, want 1", got)
	}

	// 空参 no-op。
	if delivered, err := service.NotifyBroadcast(context.Background(), nil, []OutboundMessage{{Text: "x"}}); delivered != 0 || err != nil {
		t.Fatalf("empty connectors = (%d, %v)", delivered, err)
	}
	if delivered, err := service.NotifyBroadcast(context.Background(), []string{"c1"}, nil); delivered != 0 || err != nil {
		t.Fatalf("empty msgs = (%d, %v)", delivered, err)
	}

	// 注意力封装：空文本拒绝。
	if err := service.NotifyAttentionBroadcast(context.Background(), []string{"c1"}, AttentionNotice{Text: "  "}); err == nil {
		t.Fatal("empty notice text must fail")
	}

	// 单点失败聚合：缺失 provider 的连接器失败，另一连接器仍投递。
	store2 := NewMemoryStore(
		Config{ID: "c1", Provider: "fake", Enabled: true},
		Config{ID: "c2", Provider: "missing", Enabled: true},
	)
	for _, binding := range []Binding{
		{ConnectorID: "c1", Chat: "chat-1", Session: SessionRef{SessionID: "sess-a"}},
		{ConnectorID: "c2", Chat: "chat-2", Session: SessionRef{SessionID: "sess-b"}},
	} {
		if err := store2.PutBinding(binding); err != nil {
			t.Fatalf("PutBinding: %v", err)
		}
	}
	service2 := New(Deps{Store: store2})
	if err := service2.Register(&fakeProvider{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	delivered, err = service2.NotifyBroadcast(context.Background(), []string{"c1", "c2"}, []OutboundMessage{{Text: "x"}})
	if delivered != 1 || err == nil {
		t.Fatalf("partial failure = (%d, %v), want (1, error)", delivered, err)
	}
}
