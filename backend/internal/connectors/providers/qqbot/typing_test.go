package qqbot

import (
	"context"
	"testing"
)

// TestStartTypingSendsInputNotifyForC2C 锁定单聊输入状态：开始发 input_type=1
// （带 input_second、无 msg_id 的主动消息），Stop 发 input_type=2 且幂等。
func TestStartTypingSendsInputNotifyForC2C(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	session, err := provider.StartTyping(context.Background(), cfg, "user:user-openid-1")
	if err != nil {
		t.Fatalf("StartTyping: %v", err)
	}
	if session == nil {
		t.Fatalf("expected typing session for C2C")
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].path != "/v2/users/user-openid-1/messages" {
		t.Fatalf("sent = %+v, want one C2C message", sent)
	}
	if sent[0].payload["msg_type"] != float64(qqTypingMessageType) {
		t.Fatalf("msg_type = %v, want %d", sent[0].payload["msg_type"], qqTypingMessageType)
	}
	if _, hasMsgID := sent[0].payload["msg_id"]; hasMsgID {
		t.Fatalf("typing should be a proactive message (no msg_id): %+v", sent[0].payload)
	}
	notify, ok := sent[0].payload["input_notify"].(map[string]any)
	if !ok || notify["input_type"] != float64(qqTypingInputTyping) ||
		notify["input_second"] != float64(qqTypingInputSeconds) {
		t.Fatalf("input_notify = %+v", sent[0].payload["input_notify"])
	}

	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop should be a no-op: %v", err)
	}
	sent = fake.sentMessages()
	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want typing + cancel", sent)
	}
	cancel, _ := sent[1].payload["input_notify"].(map[string]any)
	if cancel["input_type"] != float64(qqTypingInputCancel) {
		t.Fatalf("cancel input_notify = %+v", sent[1].payload["input_notify"])
	}
	if _, hasSecond := cancel["input_second"]; hasSecond {
		t.Fatalf("cancel should not carry input_second: %+v", cancel)
	}
}

// TestStartTypingSkipsGroupAndChannel 锁定群聊/频道不发送输入状态（无 typing 能力）。
func TestStartTypingSkipsGroupAndChannel(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	for _, chat := range []string{"group:group-openid-1", "channel:channel-1"} {
		session, err := provider.StartTyping(context.Background(), cfg, chat)
		if err != nil || session != nil {
			t.Fatalf("StartTyping(%s) = (%v, %v), want (nil, nil)", chat, session, err)
		}
	}
	if sent := fake.sentMessages(); len(sent) != 0 {
		t.Fatalf("no message should be sent for group/channel typing: %+v", sent)
	}
}
