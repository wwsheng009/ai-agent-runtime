package connectors

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestOutboundRuntimeSkipsPollingAndSends 锁定网关复制副本的运行语义：
// RuntimeMode=outbound 不启动轮询，但 SendOutbound 仍可用。
func TestOutboundRuntimeSkipsPollingAndSends(t *testing.T) {
	store := NewMemoryStore(Config{
		ID:          "c1",
		Provider:    "fake",
		Name:        "outbound copy",
		Enabled:     true,
		Mode:        "polling",
		RuntimeMode: "outbound",
	})
	provider := &fakeProvider{polling: true}
	service := New(Deps{Store: store, Backend: &fakeBackend{}, Logf: func(string, ...any) {}})
	if err := service.Register(provider); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&provider.pollStarts); got != 0 {
		t.Fatalf("outbound runtime must not start polling, got %d", got)
	}
	if err := service.SendOutbound(ctx, "c1", "chat-1", []OutboundMessage{{Text: "hello"}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.sent) != 1 || provider.sent[0].Text != "hello" {
		t.Fatalf("sent = %+v", provider.sent)
	}
}
