package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestChatRuntimeEventBridge_WhitespaceAssistantBeforeToolBoundaryDropped
// 复现用户报告：reasoning 之后、tool 调用之前，模型输出了一段"纯空白"
// assistant content（如 " \n\n\n"），旧版编码器会把它落成一个空 assistant
// cell，渲染层再画成 "• " + 三行空白的幽灵命令输出。断言 encoder → Scene
// 链路不再留下任何无可见内容的 assistant cell。
func TestChatRuntimeEventBridge_WhitespaceAssistantBeforeToolBoundaryDropped(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	traceID := "ghost-repro"
	post := func(typ string, payload map[string]interface{}) {
		t.Helper()
		bridge.encodeRenderModelEvent(runtimeevents.Event{Type: typ, Payload: payload})
	}
	post(runtimechat.EventLLMRequestStarted, map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-g", "stream_id": "stream-g", "step": 1,
	})
	post(runtimechat.EventLLMRequestFinished, map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-g", "stream_id": "stream-g", "step": 1, "success": true,
	})
	post(runtimechat.EventAssistantReasoning, map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-g", "step": 1,
		"reasoning": map[string]interface{}{"format": "summary", "summary": "inspect before tool"},
	})
	// 模型在结束思考后、发出工具调用前，输出纯空白 content 块
	// （" " + 三行换行 => 旧版渲染为 "• " 幽灵行 + 三个空行）。
	post(runtimechat.EventAssistantDelta, map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-g", "stream_id": "stream-g",
		"step": 1, "sequence": uint64(1), "delta": " \n\n\n",
	})
	post(runtimechat.EventToolStarted, map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-g", "step": 1,
		"tool_call_id": "g-call-1", "tool_name": "grep", "arg_preview": "pattern=foo",
	})
	post(runtimechat.EventToolFinished, map[string]interface{}{
		"trace_id": traceID, "step": 1, "tool_call_id": "g-call-1", "output": "config.yaml:465\n",
	})

	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 2 {
		for i, c := range cells {
			t.Logf("cell[%d] kind=%s phase=%s source=%q", i, c.Kind, c.Phase, c.Source)
		}
		t.Fatalf("scene cells=%d, want [reasoning, toolchain]", len(cells))
	}
	if cells[0].Kind != scene.KindReasoning || cells[1].Kind != scene.KindToolChain {
		t.Fatalf("scene kinds=%s,%s want reasoning,toolchain", cells[0].Kind, cells[1].Kind)
	}
	for i, c := range cells {
		trimmed := strings.TrimSpace(c.Source)
		if trimmed == "" || trimmed == "•" {
			t.Fatalf("cell[%d] is a ghost cell (no visible content): kind=%s phase=%s source=%q",
				i, c.Kind, c.Phase, c.Source)
		}
	}
	if cells[0].Source != "inspect before tool" {
		t.Fatalf("reasoning source=%q, want provider body only", cells[0].Source)
	}
	if strings.Contains(cells[0].Source, " reasoning ") || strings.Contains(cells[0].Source, " end reasoning ") {
		t.Fatalf("reasoning source contains presentation chrome: %q", cells[0].Source)
	}
	if !strings.Contains(cells[1].Source, "Completed grep") || !strings.Contains(cells[1].Source, "config.yaml:465") {
		t.Fatalf("tool cell missing completion/output: %q", cells[1].Source)
	}
}

// TestChatInteractionCoordinator_WhitespaceAssistantBufferProducesNoGhostLine
// 复现 legacy 终端路径：reasoning 进行中缓冲了纯空白 assistant delta
// （" \n\n\n"），finalize reasoning 时旧代码会把整个缓冲连同 "• " 前缀写
// 出，产生用户看到的幽灵行。断言 legacy writer 只写出 end reasoning
// 分隔线，不产出任何 "• " 幽灵行。
func TestChatInteractionCoordinator_WhitespaceAssistantBufferProducesNoGhostLine(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.liveStreamFn = func() bool { return true }
	coord.streamRuneDelay = 0
	var output bytes.Buffer
	coord.SetWriter(&output)

	coord.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
		Provider:   "nvidia",
		Format:     "stream_delta",
		Summary:    "inspect first",
		Streamable: true,
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	})
	coord.RenderAssistantDelta(" \n\n\n") // 模型在工具调用前的空白 content 块
	coord.FinalizeReasoningDelta()

	rendered := output.String()
	if !strings.Contains(rendered, chatToolDivider("end reasoning")) {
		t.Fatalf("expected end reasoning divider, got %q", rendered)
	}
	if strings.Contains(rendered, ui.AssistantStreamMarker()) {
		t.Fatalf("whitespace-only assistant buffer painted ghost marker row: %q", rendered)
	}
	lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "•" {
			t.Fatalf("ghost bullet line[%d]: %q (full %q)", i, line, rendered)
		}
	}
}

func TestChatInteractionCoordinator_ReasoningAndAssistantStayAdjacentWithoutEventBullet(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	reasoning := &runtimetypes.ReasoningBlock{
		Format:     "stream_delta",
		Summary:    "inspect first",
		Streamable: true,
		Visibility: runtimetypes.ReasoningVisibilitySummary,
	}

	t.Run("live stream", func(t *testing.T) {
		coord := newChatInteractionCoordinator(&ChatSession{})
		coord.liveStreamFn = func() bool { return true }
		coord.streamRuneDelay = 0
		var output bytes.Buffer
		coord.SetWriter(&output)

		coord.RenderReasoningDelta(reasoning)
		coord.FinalizeReasoningDelta()
		coord.RenderAssistantDelta("Hello")
		if !coord.CompleteAssistantResponse("Hello") {
			t.Fatal("assistant response was not completed")
		}

		assertReasoningAssistantTerminalJoin(t, output.String(), "Hello")
	})

	t.Run("one shot final", func(t *testing.T) {
		coord := newChatInteractionCoordinator(&ChatSession{})
		coord.liveStreamFn = func() bool { return true }
		coord.streamRuneDelay = 0
		var output bytes.Buffer
		coord.SetWriter(&output)

		coord.RenderReasoningDelta(reasoning)
		coord.FinalizeReasoningDelta()
		coord.RenderAssistant("Hello")

		assertReasoningAssistantTerminalJoin(t, output.String(), "Hello")
	})
}

func assertReasoningAssistantTerminalJoin(t *testing.T, rendered, answer string) {
	t.Helper()
	join := chatToolDivider("end reasoning") + "\n" + answer
	if !strings.Contains(rendered, join) {
		t.Fatalf("reasoning and assistant were not adjacent; want %q in %q", join, rendered)
	}
	if strings.Contains(rendered, chatToolDivider("end reasoning")+"\n\n"+answer) {
		t.Fatalf("reasoning/assistant boundary contains a ghost blank row: %q", rendered)
	}
	if strings.Contains(rendered, ui.AssistantStreamMarker()+answer) {
		t.Fatalf("ordinary assistant body was rendered as an event: %q", rendered)
	}
}

func TestChatRuntimeEventBridge_SameRequestReasoningAssistantLayoutIsDense(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	post := func(typ string, payload map[string]interface{}) {
		t.Helper()
		bridge.encodeRenderModelEvent(runtimeevents.Event{Type: typ, Payload: payload})
	}
	identity := map[string]interface{}{
		"trace_id": "dense-trace", "turn_id": "dense-turn", "stream_id": "dense-stream", "step": 1,
	}
	post("llm.request.started", identity)
	post("assistant.reasoning", map[string]interface{}{
		"trace_id": "dense-trace", "turn_id": "dense-turn", "step": 1,
		"reasoning": map[string]interface{}{"format": "summary", "summary": "inspect first"},
	})
	post("assistant.delta", map[string]interface{}{
		"trace_id": "dense-trace", "turn_id": "dense-turn", "stream_id": "dense-stream", "step": 1,
		"sequence": uint64(1), "delta": "Hello",
	})
	post("assistant.message", map[string]interface{}{
		"trace_id": "dense-trace", "turn_id": "dense-turn", "stream_id": "dense-stream", "step": 1,
		"content": "Hello",
	})

	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 2 || cells[0].Kind != scene.KindReasoning || cells[1].Kind != scene.KindAssistant {
		t.Fatalf("scene cells = %+v, want reasoning then assistant", cells)
	}
	if cells[0].BoundaryGroupKey == "" || cells[0].BoundaryGroupKey != cells[1].BoundaryGroupKey {
		t.Fatalf("same request lost boundary group: %+v", cells)
	}
	if cells[0].ChainKey != "" || cells[1].ChainKey != "" {
		t.Fatalf("request boundary identity leaked into tool chain: %+v", cells)
	}
	if cells[1].Source != "Hello" || strings.HasPrefix(cells[1].Source, ui.AssistantStreamMarker()) {
		t.Fatalf("assistant Scene source is not semantic text: %q", cells[1].Source)
	}
	rows := scene.LayoutTranscript(cells, 1)
	for _, row := range rows {
		if row.Boundary != nil && row.Boundary.PrevCellID == cells[0].ID && row.Boundary.NextCellID == cells[1].ID {
			t.Fatalf("same-request reasoning/assistant gained a ghost gap row: %+v", rows)
		}
	}
}
