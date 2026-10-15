package qqbot

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// TestPassiveReplyCountFallsBackToProactive 锁定被动回复次数预检：达到单条消息
// 的回复上限后，后续消息改走主动（不带 msg_id），避免注定失败的被动调用。
func TestPassiveReplyCountFallsBackToProactive(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithPassiveReplyLimits(2, 2))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	provider.rememberInboundMsgID("user:u1", "m1")

	for i := 0; i < 3; i++ {
		if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	sent := fake.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("sent = %d, want 3", len(sent))
	}
	for i := 0; i < 2; i++ {
		if _, ok := sent[i].payload["msg_id"]; !ok {
			t.Fatalf("send %d should be passive: %+v", i, sent[i].payload)
		}
	}
	if _, ok := sent[2].payload["msg_id"]; ok {
		t.Fatalf("third send should be proactive after count limit: %+v", sent[2].payload)
	}
}

// TestPassiveReplyCountResetsOnNewInbound 锁定新入站消息（新 msg_id）重新计数。
func TestPassiveReplyCountResetsOnNewInbound(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithPassiveReplyLimits(1, 1))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	provider.rememberInboundMsgID("user:u1", "m1")
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "b"}}); err != nil {
		t.Fatal(err)
	}
	provider.rememberInboundMsgID("user:u1", "m2")
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "c"}}); err != nil {
		t.Fatal(err)
	}

	sent := fake.sentMessages()
	if len(sent) != 3 {
		t.Fatalf("sent = %d, want 3", len(sent))
	}
	if got := sent[0].payload["msg_id"]; got != "m1" {
		t.Fatalf("send 0 msg_id = %v, want m1", got)
	}
	if _, ok := sent[1].payload["msg_id"]; ok {
		t.Fatalf("send 1 should be proactive (m1 limit reached): %+v", sent[1].payload)
	}
	if got := sent[2].payload["msg_id"]; got != "m2" {
		t.Fatalf("send 2 msg_id = %v, want m2（新消息重新计数）", got)
	}
}

// TestPassiveReplyLimitPerChatKind 锁定单聊/群各自的上限。
func TestPassiveReplyLimitPerChatKind(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithPassiveReplyLimits(1, 2))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	provider.rememberInboundMsgID("user:u1", "mu")
	provider.rememberInboundMsgID("group:g1", "mg")

	for i := 0; i < 2; i++ {
		if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := provider.Send(context.Background(), cfg, "group:g1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatal(err)
		}
	}

	sent := fake.sentMessages()
	if len(sent) != 5 {
		t.Fatalf("sent = %d, want 5", len(sent))
	}
	if _, ok := sent[0].payload["msg_id"]; !ok {
		t.Fatalf("user send 0 should be passive: %+v", sent[0].payload)
	}
	if _, ok := sent[1].payload["msg_id"]; ok {
		t.Fatalf("user send 1 should be proactive (limit 1): %+v", sent[1].payload)
	}
	for i := 2; i < 4; i++ {
		if _, ok := sent[i].payload["msg_id"]; !ok {
			t.Fatalf("group send %d should be passive (limit 2): %+v", i-2, sent[i].payload)
		}
	}
	if _, ok := sent[4].payload["msg_id"]; ok {
		t.Fatalf("group send 2 should be proactive: %+v", sent[4].payload)
	}
}

// TestFailedPassiveReplyNotCounted 锁定失败的被动回复不占用次数（平台 4xx 时
// 降级主动成功，下一次仍可走被动）。
func TestFailedPassiveReplyNotCounted(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithPassiveReplyLimits(1, 1))
	cfg := testConfig("qq-1", "app-1", "secret-1")
	provider.rememberInboundMsgID("user:u1", "m1")

	fake.mu.Lock()
	fake.passiveStatus = 400
	fake.passiveBody = `{"message":"reply limit reached"}`
	fake.mu.Unlock()

	// 被动被平台拒绝 → 降级主动成功（失败的被动请求不记账）。
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "a"}}); err != nil {
		t.Fatalf("send with passive rejection: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent = %d, want 1（失败的被动请求不落记录）", len(sent))
	}
	if _, ok := sent[0].payload["msg_id"]; ok {
		t.Fatalf("fallback send should be proactive: %+v", sent[0].payload)
	}

	fake.mu.Lock()
	fake.passiveStatus = 0
	fake.mu.Unlock()

	// 次数未被占用：仍可走被动。
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "b"}}); err != nil {
		t.Fatal(err)
	}
	sent = fake.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("sent = %d, want 2", len(sent))
	}
	if got := sent[1].payload["msg_id"]; got != "m1" {
		t.Fatalf("second send msg_id = %v, want m1（失败不计数）", got)
	}
}
