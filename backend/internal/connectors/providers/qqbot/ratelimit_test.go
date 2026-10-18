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
	cfg.Settings["proactive_relation_qpm"] = "0"
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
	cfg.Settings["proactive_relation_qpm"] = "0"
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
	cfg.Settings["proactive_relation_qpm"] = "0"
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
	for _, key := range []string{
		"proactive_qps", "proactive_group_qpm", "proactive_relation_qpm", "proactive_daily_limit",
	} {
		if !keys[key] {
			t.Fatalf("schema missing field %q", key)
		}
	}
}

// TestProactiveRelationQPMPerChat 锁定单关系维度（每用户/每群 20/qpm）：
// 同一会话第 21 条被本地拦截，另一会话不受影响。
func TestProactiveRelationQPMPerChat(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	fixedNow := time.Now()
	provider.now = func() time.Time { return fixedNow }

	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["proactive_qps"] = "100"
	cfg.Settings["proactive_relation_qpm"] = "2"
	cfg.Settings["proactive_daily_limit"] = "100"

	for i := 0; i < 2; i++ {
		if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}})
	var quota *proactiveQuotaError
	if !errors.As(err, &quota) || quota.scope != "relation" {
		t.Fatalf("third send err = %v, want relation quota error", err)
	}
	if err := provider.Send(context.Background(), cfg, "user:u2", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("other relation should not be limited: %v", err)
	}
}

// TestProactiveGroupUsesQPMM 锁定群聊 Bot 维度走每分钟窗口（官方 60/qpm），
// 而不是单聊的每秒窗口。
func TestProactiveGroupUsesQPMM(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	fixedNow := time.Now()
	provider.now = func() time.Time { return fixedNow }

	cfg := testConfig("qq-1", "app-1", "secret-1")
	cfg.Settings["proactive_qps"] = "1"       // 单聊窗口：不应作用于群聊
	cfg.Settings["proactive_group_qpm"] = "2" // 群聊 Bot 维度
	cfg.Settings["proactive_relation_qpm"] = "0"
	cfg.Settings["proactive_daily_limit"] = "100"

	for i := 0; i < 2; i++ {
		if err := provider.Send(context.Background(), cfg, "group:g1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
			t.Fatalf("group send %d: %v", i, err)
		}
	}
	err := provider.Send(context.Background(), cfg, "group:g1", []connectors.OutboundMessage{{Text: "hi"}})
	var quota *proactiveQuotaError
	if !errors.As(err, &quota) || quota.scope != "qpm" {
		t.Fatalf("third group send err = %v, want qpm quota error", err)
	}
	// 单聊仍受 qps 限制，与群聊窗口独立。
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("first user send: %v", err)
	}
	if err := provider.Send(context.Background(), cfg, "user:u1", []connectors.OutboundMessage{{Text: "hi"}}); err == nil {
		t.Fatal("second user send should hit qps limit")
	}
}
