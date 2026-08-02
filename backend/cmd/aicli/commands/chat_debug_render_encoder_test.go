package commands

import (
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestDebugDisplayRenderEncoderSection 验证 /debug display 中的
// "Unified Render Encoder:" 小节：bridge 存在时输出编码器统计与
// 模型快照；bridge 缺失时整节省略（不报错）。
func TestDebugDisplayRenderEncoderSection(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// bridge 缺失：小节不出现
	session := &ChatSession{}
	doc := buildChatDebugDisplayDocument(session)
	if strings.Contains(renderDocPlainText(doc), "Unified Render Encoder:") {
		t.Fatalf("bridge 缺失时不应输出 Unified Render Encoder 小节")
	}

	// bridge 存在：编码事件后输出统计与模型行
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	bridge.session = session
	session.RuntimeEventBridge = bridge
	bridge.renderEncoder.Encode(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
	}})
	bridge.renderEncoder.Encode(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1", "delta": "hi", "sequence": uint64(1),
	}})

	doc = buildChatDebugDisplayDocument(session)
	plain := renderDocPlainText(doc)
	for _, marker := range []string{
		"Unified Render Encoder:",
		"Encode Count:",
		"Append/Upsert/Remove: 1 / 1 / 0",
		"Out of Order:",
		"Unknown Types:",
		"item-1 #1", // Tail meta 行带对齐填充，按内容匹配
		"Model Items:",
		"#1 item-1 [assistant] hi",
	} {
		if !strings.Contains(plain, marker) {
			t.Fatalf("debug display 缺少锚点 %q\n---\n%s", marker, plain)
		}
	}
}

func renderDocPlainText(doc interface{ PlainText() string }) string {
	return doc.PlainText()
}
