package commands

import (
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestChatRuntimeEvents_LiveOrderAcrossFamiliesUnderSlowConsumer 固化现场
// session_20261005225426_UlODbztS 的实时错乱类别：UI 消费者被大段工具输出
// 拖慢时，reasoning 增量积压在合并车道；随后到达的 tool/llm 事件（critical）
// 不得越过积压。实时投递顺序必须等于事件到达顺序——回放/历史层正确不能替代
// 实时正确（该会话的回放、chat.json、事件流均已证实是正确顺序）。
//
// 现场形态：r9 → tool(view) → tool(git) → r12 → llm.finished → r16；
// 历史渲染里 reasoning 块被拆散、夹到工具结果之间，正是本用例要禁止的越队。
func TestChatRuntimeEvents_LiveOrderAcrossFamiliesUnderSlowConsumer(t *testing.T) {
	const sessionID = "live-order-slow-consumer"
	bridge := newDeferredQueueTestBridge(t, sessionID) // 队列容量 1 且预填：消费者停摆

	reasoning := func(stream string, seq int, text string) runtimeevents.Event {
		return runtimeevents.Event{Type: "assistant.reasoning", SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": stream, "sequence": seq, "mode": "append", "text": text,
		}}
	}
	toolDone := func(callID, toolName string) runtimeevents.Event {
		return runtimeevents.Event{Type: "tool.completed", SessionID: sessionID, Payload: map[string]interface{}{
			"tool_call_id": callID, "tool_name": toolName,
		}}
	}
	llmDone := func(stream string) runtimeevents.Event {
		return runtimeevents.Event{Type: "llm.request.finished", SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": stream, "success": true,
		}}
	}

	bridge.Handle(reasoning("s4", 1, "Let me read the last part "))
	bridge.Handle(reasoning("s4", 2, "of the file and then verify the actual implementation status."))
	bridge.Handle(toolDone("call-view", "view"))
	bridge.Handle(toolDone("call-git", "git"))
	bridge.Handle(reasoning("s5", 1, "The file header says v0.4. "))
	bridge.Handle(reasoning("s5", 2, "Let me verify the actual code state now."))
	bridge.Handle(llmDone("s5"))
	bridge.Handle(toolDone("call-status", "status"))
	bridge.Handle(reasoning("s6", 1, "Key findings so far:"))

	// 消费者恢复：逐条消费并按到达序记录。
	<-bridge.eventQueue // 预填的 filler
	type delivery struct {
		kind   string
		marker string
	}
	var got []delivery
	observe := func(e runtimeevents.Event) {
		d := delivery{kind: e.Type}
		switch e.Type {
		case "assistant.reasoning":
			d.marker, _ = e.Payload["text"].(string)
		case "tool.completed":
			d.marker, _ = e.Payload["tool_name"].(string)
		case "llm.request.finished":
			d.marker = "llm"
		}
		got = append(got, d)
	}

	// 首条必须是积压头的 r9（合并后的完整文本），而不是任何后到的 tool/llm 事件。
	select {
	case queued := <-bridge.eventQueue:
		observe(queued.event)
	case <-time.After(3 * time.Second):
		t.Fatal("消费者恢复后没有任何事件被投递：积压未被排空")
	}
	if got[0].kind != "assistant.reasoning" || !strings.Contains(got[0].marker, "Let me read the last part") {
		t.Fatalf("首个投递 = %s(%q)，期望积压中的 reasoning——后到的 tool/llm 越过了已有积压；delivered=%v",
			got[0].kind, got[0].marker, got)
	}

	// 其余按事件序到达（两段 reasoning 各合并为一条，共 7 条）。
	const want = 7
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < want && time.Now().Before(deadline) {
		select {
		case queued := <-bridge.eventQueue:
			observe(queued.event)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if len(got) != want {
		t.Fatalf("投递 %d 条，期望 %d 条（积压滞留或丢事件）：%v", len(got), want, got)
	}
	expect := []struct{ kind, marker string }{
		{"assistant.reasoning", "Let me read the last part of the file and then verify the actual implementation status."},
		{"tool.completed", "view"},
		{"tool.completed", "git"},
		{"assistant.reasoning", "The file header says v0.4. Let me verify the actual code state now."},
		{"llm.request.finished", "llm"},
		{"tool.completed", "status"},
		{"assistant.reasoning", "Key findings so far:"},
	}
	for index, want := range expect {
		if got[index].kind != want.kind || got[index].marker != want.marker {
			t.Fatalf("投递[%d] = %s(%q)，期望 %s(%q)；实际序列=%v",
				index, got[index].kind, got[index].marker, want.kind, want.marker, got)
		}
	}
}
