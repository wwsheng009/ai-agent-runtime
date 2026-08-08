package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// A tool-calling ReAct step has no intermediate assistant.message. The
// renderer must still close that step before mounting the tool cell, otherwise
// the first mutable cell remains a history barrier and later rounds disappear
// from native scrollback.
func TestReActToolRoundsKeepEarlierHistoryVisible(t *testing.T) {
	const width, height = 72, 10
	h := newNativeScrollbackRegressionHarness(t)
	h.post(t,
		Resize{Width: width, Height: height, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ShowPromptAction{Line: "> "},
	)

	encoder := encoding.NewEventEncoder()
	encoder.EnableReasoningOrderingBarrier(true)
	renderScene := scene.New()
	renderMapper := scene.NewChangeSetMapper(renderScene)
	apply := func(event runtimeevents.Event) {
		t.Helper()
		if _, _, err := renderMapper.Apply(encoder.Encode(event)); err != nil {
			t.Fatalf("apply %s: %v", event.Type, err)
		}
		if !h.controller.Post(ReplaceTranscriptAction{Snapshot: renderScene.Snapshot()}) {
			t.Fatalf("post snapshot after %s", event.Type)
		}
		h.controller.WaitIdle()
		h.flush()
	}

	const traceID = "trace-native-react-rounds"
	for step := 1; step <= 5; step++ {
		streamID := fmt.Sprintf("stream-%d", step)
		turnID := "turn-native-react-rounds"
		apply(runtimeevents.Event{Type: "llm.request.started", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": step,
		}})
		assistantMarker := fmt.Sprintf("REACT-ROUND-%02d-ASSISTANT", step)
		apply(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
			"step": step, "sequence": uint64(1), "delta": assistantMarker,
		}})
		apply(runtimeevents.Event{Type: "llm.request.finished", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": step, "success": true,
		}})
		callID := fmt.Sprintf("react-call-%d", step)
		apply(runtimeevents.Event{Type: "tool.requested", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "step": step,
			"tool_call_id": callID, "tool_name": "read_file", "arg_preview": assistantMarker,
		}})

		state := h.controller.State()
		if state.Active.Phase != ActiveCellMutable || state.Active.Kind != scene.KindToolChain {
			t.Fatalf("round %d did not hand active ownership to tool: %+v", step, state.Active)
		}
		toolMarker := fmt.Sprintf("REACT-ROUND-%02d-TOOL", step)
		apply(runtimeevents.Event{Type: "tool.completed", Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "step": step,
			"tool_call_id": callID, "output": toolMarker,
		}})
	}

	finalStream := "stream-final"
	apply(runtimeevents.Event{Type: "llm.request.started", Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-native-react-rounds", "stream_id": finalStream, "step": 6,
	}})
	finalMarker := "REACT-FINAL-ASSISTANT"
	apply(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-native-react-rounds", "stream_id": finalStream,
		"step": 6, "sequence": uint64(1), "delta": finalMarker,
	}})
	apply(runtimeevents.Event{Type: "llm.request.finished", Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-native-react-rounds", "stream_id": finalStream, "step": 6, "success": true,
	}})
	apply(runtimeevents.Event{Type: runtimechat.EventAssistantMessage, Payload: map[string]interface{}{
		"trace_id": traceID, "turn_id": "turn-native-react-rounds", "stream_id": finalStream, "content": finalMarker,
	}})

	state := h.controller.State()
	if state.Active.Phase != ActiveCellInactive || state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.HasPending() {
		t.Fatalf("ReAct rounds did not settle: active=%+v effects=%+v", state.Active, state.HistoryEffects)
	}
	for _, cell := range state.Transcript.Cells {
		if cell.Phase != scene.CellCommitted {
			t.Fatalf("transcript retained mutable cell after final: %+v", cell)
		}
	}

	markers := make([]string, 0, 11)
	for step := 1; step <= 5; step++ {
		markers = append(markers,
			boundary.FormatAssistantBlockChrome(fmt.Sprintf("REACT-ROUND-%02d-ASSISTANT", step)),
			fmt.Sprintf("REACT-ROUND-%02d-TOOL", step),
		)
	}
	markers = append(markers, boundary.FormatAssistantBlockChrome(finalMarker))
	assertPhysicalMarkersExactlyOnce(t, h.physical.String(), width, height, markers)
	if !strings.Contains(h.physical.String(), "REACT-ROUND-01-ASSISTANT") {
		t.Fatal("the first ReAct round never reached native history")
	}
}
