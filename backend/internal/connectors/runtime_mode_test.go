package connectors

import (
	"context"
	"strings"
	"testing"
)

// TestNormalizeRuntimeMode 锁定运行形态取值的规整与拒绝语义：未知值必须判为
// 非法（由管理面回 400），不做静默降级——静默降级会让「看起来配好了」与
// 「实际仍在抢入站」并存。
func TestNormalizeRuntimeMode(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"", RuntimeModeDefault, true},
		{"   ", RuntimeModeDefault, true},
		{"outbound", RuntimeModeOutbound, true},
		{" OUTBOUND ", RuntimeModeOutbound, true},
		{"Gateway", RuntimeModeGateway, true},
		{" polling ", "", false},
		{"local", "", false},
		{"self", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeRuntimeMode(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("NormalizeRuntimeMode(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// TestRuntimeModePredicates 锁定三种形态的互斥判定与入站策略解析。
func TestRuntimeModePredicates(t *testing.T) {
	outbound := Config{RuntimeMode: "outbound"}
	if !outbound.IsOutboundOnly() || outbound.IsSelfHosted() {
		t.Fatalf("outbound predicates mismatch: %+v", outbound)
	}

	selfHosted := Config{RuntimeMode: "gateway", Settings: map[string]string{InboundModeSettingKey: "notify-only"}}
	if selfHosted.IsOutboundOnly() || !selfHosted.IsSelfHosted() {
		t.Fatalf("gateway predicates mismatch: %+v", selfHosted)
	}
	if !selfHosted.InboundNotifyOnly() || selfHosted.InboundMode() != InboundModeNotifyOnly {
		t.Fatalf("gateway + notify-only must resolve to notify-only")
	}

	// 非自持连接器即使配了 notify-only 也不生效：避免节点侧误配后静默吞消息。
	// 注意 InboundMode() 只报告配置值本身；「是否真的跳过 agent」由
	// InboundNotifyOnly()（含 is-self-hosted 闸）决定。
	nodeSide := Config{RuntimeMode: "", Settings: map[string]string{InboundModeSettingKey: InboundModeNotifyOnly}}
	if nodeSide.InboundNotifyOnly() {
		t.Fatalf("notify-only must be ignored unless runtime_mode=gateway: %+v", nodeSide)
	}

	if (Config{}).InboundMode() != InboundModeAgent {
		t.Fatalf("default inbound mode must be agent")
	}
	// 未知入站策略回落 agent（宁可回失败文案，也不静默吞消息）。
	weird := Config{RuntimeMode: "gateway", Settings: map[string]string{InboundModeSettingKey: "notify"}}
	if weird.InboundNotifyOnly() {
		t.Fatalf("unknown inbound_mode must fall back to agent")
	}
}

// TestSelfHostedRuntimeStartsPolling 锁定网关自持形态会真的在本机轮询入站
// ——这是「通知不依赖节点」的前提：会话上下文（微信 context_token）由本机
// 产出，而不是靠节点同步。
func TestSelfHostedRuntimeStartsPolling(t *testing.T) {
	cfg := testConfig("c1", "fake")
	cfg.Mode = "polling"
	cfg.RuntimeMode = RuntimeModeGateway
	store := NewMemoryStore(cfg)
	provider := &fakeProvider{polling: true}
	service := newTestService(t, store, &fakeBackend{}, provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, "self-hosted polling start", func() bool {
		starts, _ := provider.pollCounts()
		return starts == 1
	})
	if _, exits := provider.pollCounts(); exits != 0 {
		t.Fatalf("self-hosted polling must stay running")
	}
}

// TestOutboundRuntimeStillSkipsPolling 对照用例：只出站副本仍然跳过轮询
// （自持形态的改动不得把 outbound 语义一并放宽）。
func TestOutboundRuntimeStillSkipsPolling(t *testing.T) {
	cfg := testConfig("c1", "fake")
	cfg.Mode = "polling"
	cfg.RuntimeMode = RuntimeModeOutbound
	store := NewMemoryStore(cfg)
	provider := &fakeProvider{polling: true}
	service := newTestService(t, store, &fakeBackend{}, provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if starts, _ := provider.pollCounts(); starts != 0 {
		t.Fatalf("outbound-only runtime must not poll, got %d", starts)
	}
}

// TestInboundNotifyOnlySkipsBackend 锁定纯通知形态：入站被消费（用于刷新会话
// 上下文，由 provider 在调用 HandleInbound 之前完成）但不投递 agent，也不回
// 「本次处理失败」文案。
func TestInboundNotifyOnlySkipsBackend(t *testing.T) {
	cfg := testConfig("c1", "fake")
	cfg.RuntimeMode = RuntimeModeGateway
	cfg.Settings = map[string]string{InboundModeSettingKey: InboundModeNotifyOnly}
	store := NewMemoryStore(cfg)
	backend := &fakeBackend{session: SessionRef{SessionID: "sess-1"}, reply: []OutboundMessage{{Text: "不该出现"}}}
	provider := &fakeProvider{}
	service := newTestService(t, store, backend, provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	// 正向信号：日志环里必须有 notify-only 记录（证明入站确实被处理过，
	// 而不是「什么都没发生」被误当成通过）。
	waitFor(t, "notify-only handled", func() bool {
		entries, ok := service.Logs("c1", 50)
		if !ok {
			return false
		}
		for _, entry := range entries {
			if strings.Contains(entry.Summary, "notify-only") {
				return true
			}
		}
		return false
	})
	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("notify-only must not dispatch to agent, got %+v", requests)
	}
	if texts := provider.sentTexts(); len(texts) != 0 {
		t.Fatalf("notify-only must not send failure notices, got %v", texts)
	}
}

// TestInboundSelfHostedAgentStillDispatches 对照用例：自持 + 默认 agent 策略
// 仍必须投递（自持只改变「谁收」，不改变「收来干什么」）。
func TestInboundSelfHostedAgentStillDispatches(t *testing.T) {
	cfg := testConfig("c1", "fake")
	cfg.RuntimeMode = RuntimeModeGateway
	store := NewMemoryStore(cfg)
	backend := &fakeBackend{session: SessionRef{SessionID: "sess-1"}, reply: []OutboundMessage{{Text: "你好"}}}
	provider := &fakeProvider{}
	service := newTestService(t, store, backend, provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agent dispatch", func() bool { return len(backend.requests()) == 1 })
}