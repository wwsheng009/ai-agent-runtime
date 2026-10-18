package qqbot

// 撤回（DELETE /v2/{users|groups}/{openid}/messages/{id}）与互动召回
// （is_wakeup=true，单聊，每周期 1 条）的官方契约回归。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestRetractMessageEndpoints(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	cases := []struct {
		chat string
		want string
	}{
		{"user:u1", "/v2/users/u1/messages/msg-1"},
		{"group:g1", "/v2/groups/g1/messages/msg-1"},
	}
	for _, tc := range cases {
		if err := provider.RetractMessage(context.Background(), cfg, tc.chat, "msg-1"); err != nil {
			t.Fatalf("retract %s: %v", tc.chat, err)
		}
	}
	sent := fake.sentMessages()
	if len(sent) != 2 || sent[0].path != cases[0].want || sent[1].path != cases[1].want {
		t.Fatalf("sent = %+v", sent)
	}

	if err := provider.RetractMessage(context.Background(), cfg, "channel:c1", "msg-1"); err == nil {
		t.Fatal("channel retract should be rejected")
	}
	if err := provider.RetractMessage(context.Background(), cfg, "user:u1", " "); err == nil {
		t.Fatal("empty message id should be rejected")
	}
}

func TestWakeupMessageIsWakeupAndQuotaPerPeriod(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	fixedNow := time.Now()
	provider.now = func() time.Time { return fixedNow }
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if err := provider.WakeupMessage(context.Background(), cfg, "user:u1",
		connectors.OutboundMessage{Text: "回来看看吧"}, fixedNow.Add(-2*time.Hour)); err != nil {
		t.Fatalf("wakeup: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].payload["is_wakeup"] != true {
		t.Fatalf("sent = %+v", sent)
	}
	if _, hasMsgID := sent[0].payload["msg_id"]; hasMsgID {
		t.Fatalf("wakeup must not carry msg_id: %+v", sent[0].payload)
	}

	// 同一周期（当天）第二条被本地拦截。
	err := provider.WakeupMessage(context.Background(), cfg, "user:u1",
		connectors.OutboundMessage{Text: "再来一条"}, fixedNow.Add(-2*time.Hour))
	var quota *wakeupQuotaError
	if !errors.As(err, &quota) {
		t.Fatalf("second wakeup err = %v, want wakeup quota error", err)
	}

	// 进入下一周期（1-3 天）后可再发一条。
	fixedNow = fixedNow.Add(48 * time.Hour)
	if err := provider.WakeupMessage(context.Background(), cfg, "user:u1",
		connectors.OutboundMessage{Text: "第二阶段召回"}, fixedNow.Add(-48*time.Hour)); err != nil {
		t.Fatalf("second period wakeup: %v", err)
	}
}

func TestWakeupRejectsGroupAndExpired(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	now := time.Now()
	provider.now = func() time.Time { return now }
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if err := provider.WakeupMessage(context.Background(), cfg, "group:g1",
		connectors.OutboundMessage{Text: "x"}, now); err == nil {
		t.Fatal("group wakeup should be rejected")
	}
	if err := provider.WakeupMessage(context.Background(), cfg, "user:u2",
		connectors.OutboundMessage{Text: "x"}, now.Add(-31*24*time.Hour)); err == nil {
		t.Fatal("expired wakeup should be rejected")
	}
}
