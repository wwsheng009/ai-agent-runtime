package connectors

// 契约（docs/bots/13 §3.7#3）：订阅投递入口 EnqueueOutbound 经 per-chat 串行
// 队列投递（与入站/reply 同键），保证 reply 与订阅投递的先后顺序。

import (
	"context"
	"testing"
	"time"
)

func TestServiceEnqueueOutboundSerializesPerChat(t *testing.T) {
	store := NewMemoryStore(Config{ID: "c1", Provider: "fake", Enabled: true})
	provider := &fakeProvider{}
	service := New(Deps{Store: store})
	if err := service.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for i, text := range []string{"one", "two", "three"} {
		if err := service.EnqueueOutbound(context.Background(), "c1", "u1", []OutboundMessage{{Text: text}}); err != nil {
			t.Fatalf("EnqueueOutbound #%d: %v", i, err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		provider.mu.Lock()
		got := len(provider.sent)
		provider.mu.Unlock()
		if got >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.sent) != 3 {
		t.Fatalf("queued deliveries not all sent: %+v", provider.sent)
	}
	if provider.sent[0].Text != "one" || provider.sent[1].Text != "two" || provider.sent[2].Text != "three" {
		t.Fatalf("per-chat queue must preserve enqueue order: %+v", provider.sent)
	}
	if provider.sentChat != "u1" {
		t.Fatalf("sent chat mismatch: %q", provider.sentChat)
	}
}

func TestServiceEnqueueOutboundValidation(t *testing.T) {
	var nilService *Service
	if err := nilService.EnqueueOutbound(context.Background(), "c1", "u1", []OutboundMessage{{Text: "x"}}); err == nil {
		t.Fatal("nil service must fail with ErrServiceNotConfigured")
	}

	service := New(Deps{Store: NewMemoryStore()})
	if err := service.EnqueueOutbound(context.Background(), "c1", "u1", nil); err != nil {
		t.Fatalf("empty msgs must be no-op: %v", err)
	}
	if err := service.EnqueueOutbound(context.Background(), "", "u1", []OutboundMessage{{Text: "x"}}); err != nil {
		t.Fatalf("empty connector must be no-op: %v", err)
	}
}
