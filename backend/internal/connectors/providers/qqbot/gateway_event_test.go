package qqbot

// 网关级事件接线：INTERACTION_CREATE 必须先 PUT 回应，再作为入站交给 agent；
// FRIEND_ADD 归一为入站但不发回应请求。

import (
	"context"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestGatewayInteractionAckThenInbound(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	event := map[string]any{
		"op": gatewayOpDispatch, "t": "INTERACTION_CREATE", "s": 2,
		"d": map[string]any{
			"id": "it-1", "type": 11, "scene": "c2c", "chat_type": 2,
			"user_openid": "u1",
			"data": map[string]any{
				"type": 11,
				"resolved": map[string]any{
					"button_data": "confirm:once", "button_id": "allow-once",
				},
			},
		},
	}
	fake.script = scriptIdentifyReady("session-1", 60000, event)
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, testConfig("qq-1", "app-1", "secret-1"),
		func(_ context.Context, msg connectors.InboundMessage) error {
			received <- msg
			return nil
		}, newMemState())

	select {
	case msg := <-received:
		if msg.Chat != "user:u1" || msg.Text != "[按钮] confirm:once" {
			t.Fatalf("inbound = %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("interaction was not delivered to handler")
	}

	waitFor(t, "interaction ack", func() bool {
		for _, sent := range fake.sentMessages() {
			if sent.path == "/interactions/it-1" {
				return true
			}
		}
		return false
	})
}

func TestGatewayFriendAddInboundWithoutAck(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	event := map[string]any{
		"op": gatewayOpDispatch, "t": "FRIEND_ADD", "s": 2,
		"d": map[string]any{
			"openid": "u2", "timestamp": 1784570523, "scene": 1001,
		},
	}
	fake.script = scriptIdentifyReady("session-1", 60000, event)
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL), WithRetryDelay(10*time.Millisecond))

	received := make(chan connectors.InboundMessage, 1)
	startPolling(t, provider, testConfig("qq-1", "app-1", "secret-1"),
		func(_ context.Context, msg connectors.InboundMessage) error {
			received <- msg
			return nil
		}, newMemState())

	select {
	case msg := <-received:
		if msg.Chat != "user:u2" || msg.ProviderData["qqbot_message_type"] != "friend_add" {
			t.Fatalf("inbound = %+v", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("friend add was not delivered to handler")
	}
	for _, sent := range fake.sentMessages() {
		if len(sent.path) >= 14 && sent.path[:14] == "/interactions/" {
			t.Fatalf("friend add must not ack interactions: %+v", sent)
		}
	}
}
