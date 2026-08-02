package commands

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestBridgeLateFinalMessageAfterFinalizeDoesNotRerenderBody 覆盖"delta 已
// 直播渲染 + 先行 finalize（携带不同快照内容，final 未标记）+ 随后才到达与
// 已渲染正文一致的终态消息"的时序：终态消息不得把同一正文整段重渲染
// （重复显示的回归防线，对应 handlePrimaryAssistantMessage 的
// hasFinalizedAssistantDelta 分支）。
func TestBridgeLateFinalMessageAfterFinalizeDoesNotRerenderBody(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-1"}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.session = session
	bridge.BeginRun()

	var rendered []string
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	body := "第一行\n第二行\n第三行\n"

	// 1) delta 已直播渲染（写屏发生在 coordinator，桥侧记录已渲染内容）。
	bridge.markAssistantDeltaRendered(body)
	if !bridge.HasRenderedAssistantDelta() {
		t.Fatalf("delta 应标记为已渲染")
	}

	// 2) 先行 finalize：携带与 delta 不同的快照内容 → 仅落 digest，
	//    不标记 final-rendered（HasRenderedAssistantFinal 保持 false）。
	if !bridge.finalizeAssistantDelta(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"content": "先行快照"},
	}) {
		t.Fatalf("finalizeAssistantDelta 应返回 true")
	}
	if !bridge.hasFinalizedAssistantDelta() {
		t.Fatalf("delta 应标记为已 finalize")
	}
	if bridge.HasRenderedAssistantFinal() {
		t.Fatalf("内容不匹配时 final 不应被标记为已渲染")
	}

	// 3) 迟到的终态消息：正文与已渲染内容一致，不得整段重渲染。
	if !bridge.handlePrimaryAssistantMessage(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"content": body},
	}) {
		t.Fatalf("handlePrimaryAssistantMessage 应返回 true")
	}
	if len(rendered) != 0 {
		t.Fatalf("迟到的终态消息不应重渲染正文，实际渲染了 %d 次: %q", len(rendered), rendered)
	}
	if !bridge.HasRenderedAssistantFinal() {
		t.Fatalf("终态应标记为已渲染（exactly-once ownership）")
	}
}

// TestBridgeLateFinalMessageWithNewContentStillRenders 反向保证：finalize 后
// 终态消息携带与已渲染内容不同的正文（delta 只覆盖了前缀）时，内容仍需整段
// 渲染，不能因防重而过早吞掉未显示的部分。
func TestBridgeLateFinalMessageWithNewContentStillRenders(t *testing.T) {
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session-1"}}
	bridge := newChatRuntimeEventBridge(session)
	bridge.session = session
	bridge.BeginRun()

	var rendered []string
	bridge.renderResponse = func(response string) {
		rendered = append(rendered, response)
	}

	// delta 只直播渲染了前缀；先行 finalize 快照也与正文不同。
	bridge.markAssistantDeltaRendered("第一行\n")
	bridge.finalizeAssistantDelta(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"content": "先行快照"},
	})

	full := "第一行\n第二行\n第三行\n"
	if !bridge.handlePrimaryAssistantMessage(runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "session-1",
		Payload:   map[string]interface{}{"content": full},
	}) {
		t.Fatalf("handlePrimaryAssistantMessage 应返回 true")
	}
	if len(rendered) != 1 || rendered[0] != full {
		t.Fatalf("终态内容未被渲染过时应整段渲染，实际: %q", rendered)
	}
}
