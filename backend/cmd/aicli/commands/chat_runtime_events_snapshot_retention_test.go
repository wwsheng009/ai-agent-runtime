package commands

import (
	"testing"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestClassifyLLMRequestFinishedIsCritical 锁定修复 3 的分类契约：
// llm.request.finished 现在携带 assistant_snapshot（每次模型响应的权威全文），
// 丢失它等于丢弃已经超越流式 delta 的内容，必须走 critical 通道。
func TestClassifyLLMRequestFinishedIsCritical(t *testing.T) {
	for _, typ := range []string{"llm.request.finished", runtimechat.EventLLMRequestFinished} {
		if got := classifyChatRuntimeEvent(typ); got != eventClassCritical {
			t.Fatalf("classify(%q) = %v, want critical", typ, got)
		}
	}
}

// TestFlushPendingStreamBoundedRetainsAssistantBacklog 锁定修复 3 的保留语义：
// flush 预算耗尽时不再整段丢弃积压——丢弃 assistant 文本增量会让中间步骤
// （无 run 级终稿）在屏幕上永久截断。积压保留，消费端追赶后自动排空。
func TestFlushPendingStreamBoundedRetainsAssistantBacklog(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{RuntimeSession: &runtimechat.Session{ID: "flush-retain"}})
	bridge.eventQueue = make(chan chatRuntimeQueuedEvent, 1)
	bridge.eventQueue <- chatRuntimeQueuedEvent{event: runtimeevents.Event{Type: "fill"}, size: 1}
	t.Cleanup(func() {
		bridge.streamMu.Lock()
		bridge.pendingStreams = nil
		bridge.pendingStreamsBytes = 0
		bridge.streamMu.Unlock()
	})

	bridge.streamMu.Lock()
	bridge.pendingStreams = []chatRuntimeQueuedEvent{{
		event: runtimeevents.Event{
			Type:    runtimechat.EventAssistantDelta,
			Payload: map[string]interface{}{"delta": "tail"},
		},
		size: 8,
	}}
	bridge.pendingStreamsBytes = 8
	bridge.flushPendingStreamBoundedLocked(time.Nanosecond)
	remaining := len(bridge.pendingStreams)
	bridge.streamMu.Unlock()
	if remaining != 1 {
		t.Fatalf("pending backlog = %d, want 1 retained (assistant text must never be dropped)", remaining)
	}
}
