package qqbot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// TestProactiveQPSLimit 锁定主动消息的每秒预检：超限返回可识别的本地错误、
// 不发出请求；窗口推进后恢复。
func TestProactiveQPSLimit(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	fixedNow := time.Now()
	provider.now = func() time.Time { return fixedNow }

	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["proactive_qps"] = "2"
	cfg.Settings["proactive_daily_limit"] = "100"

	for i := 0; i < 2; i++ {
		if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}})
	var quota *proactiveQuotaError
	if !errors.As(err, &quota) || quota.scope != "qps" || !strings.Contains(err.Error(), "每秒") {
		t.Fatalf("third send err = %v, want qps quota error", err)
	}
	if got := len(fake.sentMessages()); got != 2 {
		t.Fatalf("sent = %d, want 2（超限不应发出请求）", got)
	}

	fixedNow = fixedNow.Add(proactiveQPSWindow)
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("after qps window: %v", err)
	}
}

// TestProactiveDailyLimitPerChat 锁定每会话每日上限（不同会话独立计数）。
func TestProactiveDailyLimitPerChat(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	fixedNow := time.Now()
	provider.now = func() time.Time { return fixedNow }

	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["proactive_qps"] = "100"
	cfg.Settings["proactive_daily_limit"] = "2"

	for i := 0; i < 2; i++ {
		if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}})
	var quota *proactiveQuotaError
	if !errors.As(err, &quota) || quota.scope != "daily" || !strings.Contains(err.Error(), "今日") {
		t.Fatalf("third send err = %v, want daily quota error", err)
	}
	// 另一个会话不受影响。
	if err := provider.Send(context.Background(), cfg, "user:u2", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("other chat should not be limited: %v", err)
	}
}

// TestProactiveLimitSkipsPassiveAndTyping 锁定被动回复与输入状态不占用主动配额。
func TestProactiveLimitSkipsPassiveAndTyping(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))

	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["proactive_qps"] = "100"
	cfg.Settings["proactive_daily_limit"] = "1"

	// u1 有最近入站 msg_id：回复走被动路径，不计入主动配额。
	provider.rememberInboundMsgID("user:u1", "m1")
	for i := 0; i < 3; i++ {
		if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatalf("passive send %d: %v", i, err)
		}
	}
	session, err := provider.StartTyping(context.Background(), cfg, "user:u1")
	if err != nil || session == nil {
		t.Fatalf("StartTyping = (%v, %v)", session, err)
	}
	if err := session.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// u2 无 msg_id：主动消息按 1 条上限，第 2 条被拒。
	if err := provider.Send(context.Background(), cfg, "user:u2", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("first proactive: %v", err)
	}
	err = provider.Send(context.Background(), cfg, "user:u2", []connectors.OutboundMessage{{Text: "hi"}})
	var quota *proactiveQuotaError
	if !errors.As(err, &quota) || quota.scope != "daily" {
		t.Fatalf("second proactive err = %v, want daily quota error", err)
	}
}

// TestSchemaDeclaresProactiveLimits 锁定两个预检开关进入 Schema（三端渲染）。
func TestSchemaDeclaresProactiveLimits(t *testing.T) {
	schema := New().Schema()
	keys := make(map[string]bool, len(schema.Fields))
	for _, field := range schema.Fields {
		keys[field.Key] = true
	}
	for _, key := range []string{"proactive_qps", "proactive_daily_limit"} {
		if !keys[key] {
			t.Fatalf("schema missing field %q", key)
		}
	}
}
