package connectors

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// replayProvider 是「支持失败回复重放」的最小测试通道：不实现 typing/streaming，
// 回复直接走 Provider.Send；failNext 控制接下来 N 次 Send 失败。
type replayProvider struct {
	mu       sync.Mutex
	failNext int
	sent     [][]OutboundMessage
	intents  int
}

func (p *replayProvider) Name() string { return "replay" }

func (p *replayProvider) Schema() api.ProviderSchema {
	return api.ProviderSchema{Provider: "replay", Label: "Replay"}
}

func (p *replayProvider) Activate(context.Context, Config) (Activation, error) {
	return Activation{}, nil
}

func (p *replayProvider) Deactivate(context.Context, Config) error { return nil }

func (p *replayProvider) Send(_ context.Context, _ Config, _ string, msgs []OutboundMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failNext > 0 {
		p.failNext--
		return errors.New("send exploded")
	}
	p.sent = append(p.sent, cloneOutboundMessages(msgs))
	return nil
}

func (p *replayProvider) IsReplyReplayIntent(msg InboundMessage) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.intents++
	return strings.Contains(msg.Text, "重发")
}

func (p *replayProvider) sentBatches() [][]OutboundMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]OutboundMessage, len(p.sent))
	for index, batch := range p.sent {
		out[index] = cloneOutboundMessages(batch)
	}
	return out
}

func TestMemoryStoreFailedReplyRoundTripAndCascade(t *testing.T) {
	store := NewMemoryStore()
	if err := store.PutFailedReply(FailedReply{Reply: []OutboundMessage{{Text: "x"}}}); err == nil {
		t.Fatalf("missing connector id/chat should fail")
	}

	original := FailedReply{
		ConnectorID: "c1",
		Chat:        "u1",
		MessageID:   "m1",
		Error:       "boom",
		Reply: []OutboundMessage{{
			Text:  "你好",
			Media: []Media{{Kind: "image", FileID: "f1"}},
		}},
	}
	if err := store.PutFailedReply(original); err != nil {
		t.Fatalf("PutFailedReply: %v", err)
	}
	original.Reply[0].Text = "mutated"

	got, ok := store.GetFailedReply("c1", "u1")
	if !ok {
		t.Fatalf("snapshot missing")
	}
	if got.MessageID != "m1" || got.Error != "boom" || got.FailedAt.IsZero() {
		t.Fatalf("snapshot fields = %+v", got)
	}
	if got.Reply[0].Text != "你好" || got.Reply[0].Media[0].FileID != "f1" {
		t.Fatalf("snapshot reply = %+v（应深拷贝存储）", got.Reply[0])
	}
	got.Reply[0].Text = "mutated-again"
	again, _ := store.GetFailedReply("c1", "u1")
	if again.Reply[0].Text != "你好" {
		t.Fatalf("store leaked internal state: %+v", again.Reply[0])
	}

	if err := store.ClearFailedReply("c1", "u1"); err != nil {
		t.Fatalf("ClearFailedReply: %v", err)
	}
	if _, ok := store.GetFailedReply("c1", "u1"); ok {
		t.Fatalf("snapshot should be cleared")
	}
	if err := store.ClearFailedReply("c1", "u1"); err != nil {
		t.Fatalf("clear should be idempotent: %v", err)
	}

	// DeleteConfig 级联清理。
	if err := store.PutFailedReply(original); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	if err := store.DeleteConfig("c1"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if _, ok := store.GetFailedReply("c1", "u1"); ok {
		t.Fatalf("DeleteConfig should cascade failed replies")
	}
}

func TestServiceReplaysFailedReplyOnIntent(t *testing.T) {
	provider := &replayProvider{failNext: 1}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "原始回答"}}}
	store := NewMemoryStore(testConfig("c1", "replay"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "问题")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed reply recorded", func() bool {
		_, ok := store.GetFailedReply("c1", "chat-1")
		return ok
	})
	failed, _ := store.GetFailedReply("c1", "chat-1")
	if failed.MessageID != "m1" || len(failed.Reply) != 1 || failed.Reply[0].Text != "原始回答" || failed.Error == "" {
		t.Fatalf("failed snapshot = %+v", failed)
	}

	// 用户发重发意图：应重放快照并消费该条入站（不进入 backend）。
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "重发")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "snapshot cleared after replay", func() bool {
		_, ok := store.GetFailedReply("c1", "chat-1")
		return !ok
	})
	if got := len(backend.requests()); got != 1 {
		t.Fatalf("backend handled %d time(s), want 1（重发意图被重放消费）", got)
	}
	batches := provider.sentBatches()
	if len(batches) != 1 || len(batches[0]) != 1 || batches[0][0].Text != "原始回答" {
		t.Fatalf("replayed batches = %+v", batches)
	}
}

func TestServiceKeepsSnapshotWithoutIntentThenClearsOnSuccess(t *testing.T) {
	provider := &replayProvider{failNext: 1}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "第一次回答"}}}
	store := NewMemoryStore(testConfig("c1", "replay"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "问题")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed reply recorded", func() bool {
		_, ok := store.GetFailedReply("c1", "chat-1")
		return ok
	})

	backend.mu.Lock()
	backend.reply = []OutboundMessage{{Text: "第二次回答"}}
	backend.mu.Unlock()

	// 普通消息不触发重放：进入 backend，成功的投递顺带清理残留快照。
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "你好")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second turn handled", func() bool { return len(backend.requests()) == 2 })
	waitFor(t, "stale snapshot cleared after success", func() bool {
		_, ok := store.GetFailedReply("c1", "chat-1")
		return !ok
	})
	batches := provider.sentBatches()
	if len(batches) != 1 || batches[0][0].Text != "第二次回答" {
		t.Fatalf("sent batches = %+v", batches)
	}
}

func TestServiceReplayFailureRefreshesSnapshotAndNotifies(t *testing.T) {
	provider := &replayProvider{failNext: 2}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}, reply: []OutboundMessage{{Text: "原始回答"}}}
	store := NewMemoryStore(testConfig("c1", "replay"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "问题")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failed reply recorded", func() bool {
		_, ok := store.GetFailedReply("c1", "chat-1")
		return ok
	})

	// 重放也失败：快照保留并刷新，尽力发提示。
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "重发")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replay failure notice", func() bool { return len(provider.sentBatches()) == 1 })
	refreshed, ok := store.GetFailedReply("c1", "chat-1")
	if !ok || refreshed.Error == "" {
		t.Fatalf("snapshot should be refreshed after replay failure: ok=%v record=%+v", ok, refreshed)
	}
	batches := provider.sentBatches()
	if !strings.Contains(batches[0][0].Text, "重发失败") {
		t.Fatalf("notice batch = %+v", batches[0])
	}
	if got := len(backend.requests()); got != 1 {
		t.Fatalf("backend handled %d time(s), want 1", got)
	}
}
