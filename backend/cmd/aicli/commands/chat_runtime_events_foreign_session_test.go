package commands

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// recordingHeadlessBridge 复刻 ACP stdio / exec JSONL 的 headless 桥入口：
// 只记录收到的 runtime 事件，不产生任何输出。
type recordingHeadlessBridge struct {
	runtime []runtimeevents.Event
}

func (b *recordingHeadlessBridge) HandleChatCoreEvent(runtimechatcore.ChatEvent) {}

func (b *recordingHeadlessBridge) HandleRuntimeEvent(event runtimeevents.Event) {
	b.runtime = append(b.runtime, event)
}

// TestChatRuntimeEventBridgeDropsForeignSessionContent 锁定多代理（spawn_agent）
// 场景下的会话隔离：子代理与父代理共用一条 EventBus
// （internal/agent/child_factory.go: childAgent.SetEventBus(parent.GetEventBus())），
// 父桥订阅全总线，因此必须由桥保证子会话内容不进入父会话的输出面
// ——否则 ACP 客户端会把子代理的 agent_message_chunk / agent_thought_chunk
// 与父回合串在一起（thinking 未完成就出正文、调用工具后又继续 thinking）。
func TestChatRuntimeEventBridgeDropsForeignSessionContent(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "lead-session"}}
	headless := &recordingHeadlessBridge{}
	session.ExecEventBridge = headless
	bridge := newChatRuntimeEventBridge(session)

	const childSessionID = "child-session-1"
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: childSessionID,
		Payload:   map[string]interface{}{"delta": "child answer text"},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantReasoning,
		SessionID: childSessionID,
		Payload:   map[string]interface{}{"reasoning": map[string]interface{}{"summary": "child thought"}},
	})
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventToolStarted,
		SessionID: childSessionID,
		Payload: map[string]interface{}{
			"tool_call_id": "call-child-1",
			"tool_name":    "view",
		},
	})

	if len(headless.runtime) != 0 {
		t.Fatalf("child session content leaked into the headless (ACP/exec) bridge: %#v", headless.runtime)
	}
	if stats := bridge.renderEncoderStats(); stats.EncodeCount != 0 {
		t.Fatalf("child session content entered the parent render data plane: encode=%d", stats.EncodeCount)
	}

	// 主会话事件必须照常通过：守卫不得过宽。
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"delta": "parent answer text"},
	})
	if len(headless.runtime) != 1 || headless.runtime[0].SessionID != "lead-session" {
		t.Fatalf("primary session event was not forwarded: %#v", headless.runtime)
	}

	// 控制面投影继续放行：subagent.progress 镜像以父会话身份发布，
	// 是父侧唯一允许看到的子代理活动信号。
	bridge.handleEvent(runtimeevents.Event{
		Type:      "subagent.progress",
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"agent_id":   childSessionID,
			"session_id": childSessionID,
			"state":      "progress",
		},
	})
	if len(headless.runtime) != 2 {
		t.Fatalf("subagent progress mirror was dropped: %#v", headless.runtime)
	}

	// 子会话终态仍需抵达父桥（父侧 TitleNotifier 按会话清理工具行）。
	bridge.handleEvent(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: childSessionID,
		Payload:   map[string]interface{}{"status": "idle"},
	})
	if len(headless.runtime) != 3 {
		t.Fatalf("child session_end must stay deliverable to the bridge: %#v", headless.runtime)
	}
}

// TestChatRuntimeEventBridgeTerminalEventsStayScopedToPrimarySession 锁定残留串流
// 路径：子会话的 session_end / session_interrupted 会被 isForeignSessionContentEvent
// 放行（父侧 TitleNotifier 需要按会话清理工具行），但它们绝不能 finalize 父回合
// 正在进行的思考块 / 流式正文单元格——否则任一子代理结束都会把父会话的输出截断成
// 多段（thinking 被提前收尾、正文被拆成多个单元格）。
func TestChatRuntimeEventBridgeTerminalEventsStayScopedToPrimarySession(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "lead-session"}}
	bridge := newChatRuntimeEventBridge(session)

	// 父回合已有渲染中的思考增量与正文增量（均未终态化）。
	bridge.activeReasoningRequestKey = "req-lead-1"
	bridge.markReasoningDelta("req-lead-1")
	bridge.markAssistantDeltaRendered("parent partial answer")

	const childSessionID = "child-session-1"
	childTerminals := []runtimeevents.Event{
		{Type: runtimechat.EventSessionEnd, SessionID: childSessionID, Payload: map[string]interface{}{"status": "idle"}},
		{Type: runtimechat.EventSessionInterrupted, SessionID: childSessionID},
		// session_end 的会话身份也可能只出现在 payload 里（见 updateChatTitleForRuntimeEvent）。
		{Type: runtimechat.EventSessionEnd, Payload: map[string]interface{}{"session_id": childSessionID}},
	}
	for _, event := range childTerminals {
		if bridge.shouldFlushReasoningOnSessionEnd(event) {
			t.Fatalf("child terminal event %q flushed the parent reasoning block: %#v", event.Type, event)
		}
		if bridge.shouldFinalizeAssistantDeltaOnTerminalEvent(event) {
			t.Fatalf("child terminal event %q finalized the parent assistant delta cell: %#v", event.Type, event)
		}
	}
	if bridge.hasRenderedReasoningFinalFor("req-lead-1") || bridge.hasFinalizedAssistantDelta() {
		t.Fatal("parent stream state was mutated by a child terminal event")
	}

	// 守卫不得过宽：父会话自己的终态事件必须继续收口。
	parentEnd := runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: "lead-session"}
	if !bridge.shouldFlushReasoningOnSessionEnd(parentEnd) {
		t.Fatal("parent session_end must still flush the parent reasoning block")
	}
	if !bridge.shouldFinalizeAssistantDeltaOnTerminalEvent(parentEnd) {
		t.Fatal("parent session_end must still finalize the parent assistant delta cell")
	}
}
