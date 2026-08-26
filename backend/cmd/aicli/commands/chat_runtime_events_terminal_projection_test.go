package commands

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// This is the production ownership chain, not an encoder-only fixture:
// runtime event bridge -> Scene -> AppState -> HistoryCommit -> terminal bytes.
func TestDottedLifecycleProjectsMarkdownExactlyOnceIntoNativeHistory(t *testing.T) {
	const width, height = 58, 12
	markers := make([]string, 32)
	items := make([]string, len(markers))
	for index := range markers {
		markers[index] = fmt.Sprintf("BRIDGE-MARKDOWN-%03d", index+1)
		items[index] = fmt.Sprintf("- **%s** with `code-%03d`", markers[index], index+1)
	}
	answer := "# Bridge projection\n\n" + strings.Join(items, "\n")
	traceID, turnID, streamID := "trace-bridge", "turn-bridge", "stream-bridge"

	bridge := newChatRuntimeEventBridge(&ChatSession{})
	for _, event := range []runtimeevents.Event{
		{Type: "llm.request.started", TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1,
		}},
		{Type: "assistant.reasoning", TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "step": 1,
			"reasoning": map[string]interface{}{"format": "stream_delta", "summary": "BRIDGE-REASONING-SENTINEL"},
		}},
		{Type: runtimechat.EventAssistantDelta, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
			"step": 1, "sequence": uint64(1), "delta": answer,
		}},
		{Type: "llm.request.finished", TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		}},
		// Production authoritative finals retain turn/stream but omit step.
		{Type: runtimechat.EventAssistantMessage, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "content": answer,
		}},
	} {
		bridge.encodeRenderModelEvent(event)
	}

	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 2 {
		t.Fatalf("bridge scene cells=%d, want reasoning + one assistant", len(snapshot.Cells))
	}
	if snapshot.Cells[0].Kind != scene.KindReasoning || !strings.Contains(snapshot.Cells[0].Source, "BRIDGE-REASONING-SENTINEL") ||
		snapshot.Cells[1].Kind != scene.KindAssistant || snapshot.Cells[1].Source != answer ||
		snapshot.Cells[1].Phase != scene.CellCommitted {
		t.Fatalf("bridge scene did not preserve canonical reasoning/assistant order: %+v", snapshot.Cells)
	}
	for _, cell := range snapshot.Cells {
		if strings.Contains(cell.Source, "assistant.reasoning") || strings.Contains(cell.Source, "llm.request.") {
			t.Fatalf("raw lifecycle label entered Scene: %+v", cell)
		}
	}

	controller := ui.NewUIController(ui.UIControllerConfig{}, nil, nil)
	go controller.Run()
	var physical bytes.Buffer
	executor := ui.NewTerminalSessionExecutor(controller, ui.NewTerminalSession(&physical))
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})
	for _, action := range []ui.UIAction{
		ui.Resize{Width: width, Height: height, Generation: 1},
		ui.ShowPromptAction{Line: "> "},
		ui.ReplaceTranscriptAction{Snapshot: snapshot},
	} {
		if !controller.Post(action) {
			t.Fatalf("post %T", action)
		}
	}
	controller.WaitIdle()
	executor.Request()
	executor.WaitIdle()
	controller.WaitIdle()

	entries := controller.State().HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("committed Scene produced no HistoryCommit effects")
	}
	for _, entry := range entries {
		if entry.State != ui.HistoryCommitAcked {
			t.Fatalf("history effect not acknowledged: %#v", entry)
		}
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	for _, marker := range append([]string{"BRIDGE-REASONING-SENTINEL"}, markers...) {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("physical marker %q count=%d, want exactly one\n%s", marker, count, screen.Dump())
		}
	}
	for _, raw := range []string{"assistant.reasoning", "llm.request.started", "llm.request.finished"} {
		if strings.Contains(projected, raw) {
			t.Fatalf("raw lifecycle label %q reached physical terminal", raw)
		}
	}
}

func TestUnifiedReasoningDeltasAndFinalSnapshotProjectExactlyOnce(t *testing.T) {
	const width, height = 100, 22
	const (
		sessionID = "session-reasoning-exactly-once"
		traceID   = "trace-reasoning-exactly-once"
		turnID    = "turn-reasoning-exactly-once"
		streamID  = "stream-reasoning-exactly-once"
		answer    = "ASSISTANT-EXACTLY-ONCE"
	)
	chunks := []string{
		"REASONING-FIRST-LINE\n\n",
		"# REASONING-LITERAL-HEADING\n",
		"- **REASONING-LITERAL-MARKDOWN**\n",
		"`REASONING-LAST-LINE`",
	}
	finalBody := strings.Join(chunks, "")

	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}, Stream: true}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var physical bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&physical) {
		t.Fatal("unified renderer did not attach")
	}
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	bridge.BeginRun()

	events := []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: sessionID, TraceID: traceID, Payload: map[string]interface{}{
			"turn_id": turnID,
		}},
		{Type: runtimechat.EventLLMRequestStarted, SessionID: sessionID, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1,
		}},
	}
	for index, chunk := range chunks {
		events = append(events, runtimeevents.Event{
			Type: runtimechat.EventAssistantReasoning, SessionID: sessionID, TraceID: traceID,
			Payload: map[string]interface{}{
				"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
				"step": 1, "sequence": uint64(index + 1), "mode": "append",
				"reasoning": map[string]interface{}{
					"format": "stream_delta", "streamable": true, "summary": chunk,
				},
			},
		})
	}
	events = append(events,
		runtimeevents.Event{Type: runtimechat.EventAssistantDelta, SessionID: sessionID, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
			"step": 1, "sequence": uint64(1), "mode": "append", "delta": answer,
		}},
		runtimeevents.Event{Type: runtimechat.EventLLMRequestFinished, SessionID: sessionID, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		}},
		runtimeevents.Event{Type: runtimechat.EventAssistantMessage, SessionID: sessionID, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "mode": "snapshot",
			"content": answer,
			"reasoning": map[string]interface{}{
				"format": "summary", "streamable": true, "summary": finalBody,
			},
		}},
		runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: sessionID, TraceID: traceID, Payload: map[string]interface{}{
			"turn_id": turnID, "success": true,
		}},
	)
	for _, event := range events {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("final reasoning flow retained mutable active: %+v", state.Active)
	}
	if len(state.Transcript.Cells) != 2 {
		t.Fatalf("transcript cells=%d, want reasoning + assistant: %+v", len(state.Transcript.Cells), state.Transcript.Cells)
	}
	reasoning, assistant := state.Transcript.Cells[0], state.Transcript.Cells[1]
	if reasoning.Kind != scene.KindReasoning || reasoning.Phase != scene.CellCommitted || reasoning.Source != finalBody {
		t.Fatalf("canonical reasoning cell = %+v, want exact final source", reasoning)
	}
	if strings.Contains(reasoning.Source, " reasoning ") || strings.Contains(reasoning.Source, " end reasoning ") {
		t.Fatalf("presentation chrome leaked into reasoning source: %q", reasoning.Source)
	}
	if assistant.Kind != scene.KindAssistant || assistant.Phase != scene.CellCommitted ||
		assistant.Source != boundary.FormatAssistantBlockChrome(answer) {
		t.Fatalf("canonical assistant cell = %+v", assistant)
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.HasPending() {
		t.Fatalf("reasoning history did not settle: %+v", state.HistoryEffects)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	for _, marker := range []string{
		"REASONING-FIRST-LINE",
		"# REASONING-LITERAL-HEADING",
		"- **REASONING-LITERAL-MARKDOWN**",
		"`REASONING-LAST-LINE`",
		answer,
	} {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("physical marker %q count=%d, want exactly one\n%s", marker, count, screen.Dump())
		}
	}
	for label, divider := range map[string]string{
		"opening": chatToolDivider("reasoning"),
		"closing": chatToolDivider("end reasoning"),
	} {
		if count := strings.Count(projected, divider); count != 1 {
			t.Fatalf("%s reasoning divider count=%d, want exactly one\n%s", label, count, screen.Dump())
		}
	}
}

func TestDottedLifecycleLateMarkdownFinalStaysOneAssistantCell(t *testing.T) {
	bridge := newChatRuntimeEventBridge(&ChatSession{})
	traceID, turnID, streamID := "trace-late-markdown", "turn-late-markdown", "stream-late-markdown"
	intro := "我先检查仓库状态和版本文件。\n\n"
	final := intro + "- backend/config.yaml"

	for _, event := range []runtimeevents.Event{
		{Type: "assistant.reasoning", TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "step": 1,
			"reasoning": map[string]interface{}{"format": "stream_delta", "summary": "先确认状态"},
		}},
		{Type: runtimechat.EventAssistantDelta, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
			"step": 1, "sequence": uint64(1), "delta": intro,
		}},
		// The first delta is plain-looking; Markdown is only visible in the
		// authoritative final snapshot.
		{Type: runtimechat.EventAssistantMessage, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "content": final,
		}},
	} {
		bridge.encodeRenderModelEvent(event)
	}

	cells := bridgeSceneCells(t, bridge)
	if len(cells) != 2 {
		t.Fatalf("cells=%d want reasoning + one assistant: %+v", len(cells), cells)
	}
	if cells[0].Kind != scene.KindReasoning || cells[1].Kind != scene.KindAssistant {
		t.Fatalf("cell kinds=%s,%s want supplement,assistant", cells[0].Kind, cells[1].Kind)
	}
	assistant := cells[1]
	if assistant.Presentation.Kind != scene.PresentationAssistantMarkdown {
		t.Fatalf("assistant presentation=%v want markdown: %+v", assistant.Presentation.Kind, assistant)
	}
	if assistant.Source != final {
		t.Fatalf("assistant source=%q want raw final %q", assistant.Source, final)
	}
	if strings.Contains(assistant.Source, ui.AssistantStreamMarker()+intro) {
		t.Fatalf("legacy assistant event marker leaked into final Scene source: %q", assistant.Source)
	}
}

// The local ReAct producer can emit a successful request boundary before a
// non-streamed reasoning summary. The semantic/physical order must still be
// reasoning followed by the authoritative assistant final.
func TestLateReasoningAfterSuccessfulRequestBoundaryPrecedesAssistantFinal(t *testing.T) {
	const width, height = 96, 18
	const (
		sessionID = "session-late-reasoning"
		turnID    = "turn-late-reasoning"
		streamID  = "stream-late-reasoning"
		reasoning = "LATE-REASONING-BEFORE-FINAL"
		answer    = "LATE-ASSISTANT-FINAL"
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var physical bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&physical) {
		t.Fatal("unified renderer did not attach")
	}
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	bridge.BeginRun()

	for _, event := range []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: sessionID, Payload: map[string]interface{}{"turn_id": turnID}},
		{Type: runtimechat.EventLLMRequestStarted, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
		}},
		{Type: runtimechat.EventAssistantDelta, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "sequence": uint64(1), "delta": answer,
		}},
		{Type: runtimechat.EventLLMRequestFinished, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		}},
		{Type: "assistant.reasoning", SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
			"reasoning": map[string]interface{}{"format": "summary", "summary": reasoning},
		}},
		{Type: runtimechat.EventAssistantMessage, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "content": answer,
		}},
		{Type: runtimechat.EventSessionEnd, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "success": true,
		}},
	} {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("late reasoning flow retained mutable active: %+v", state.Active)
	}
	if len(state.Transcript.Cells) != 2 || state.Transcript.Cells[0].Kind != scene.KindReasoning ||
		!strings.Contains(state.Transcript.Cells[0].Source, reasoning) || state.Transcript.Cells[1].Kind != scene.KindAssistant ||
		state.Transcript.Cells[1].Source != boundary.FormatAssistantBlockChrome(answer) || state.Transcript.Cells[1].Phase != scene.CellCommitted {
		t.Fatalf("late reasoning/final semantic order = %+v", state.Transcript.Cells)
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.HasPending() {
		t.Fatalf("late reasoning history did not settle: %+v", state.HistoryEffects)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	reasoningAt, answerAt := strings.Index(projected, reasoning), strings.Index(projected, answer)
	if reasoningAt < 0 || answerAt < 0 || reasoningAt >= answerAt {
		t.Fatalf("physical order reasoning=%d final=%d\n%s", reasoningAt, answerAt, screen.Dump())
	}
	for _, marker := range []string{reasoning, answer} {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("physical marker %q count=%d\n%s", marker, count, screen.Dump())
		}
	}
}

func TestLateReasoningBarrierWithholdsLongAssistantFromNativeHistory(t *testing.T) {
	const width, height = 96, 18
	const (
		sessionID = "session-late-reasoning-long"
		turnID    = "turn-late-reasoning-long"
		streamID  = "stream-late-reasoning-long"
		reasoning = "LATE-LONG-REASONING-BEFORE-ANSWER"
	)
	markers := make([]string, 40)
	lines := make([]string, len(markers))
	for index := range markers {
		markers[index] = fmt.Sprintf("LATE-LONG-ANSWER-%02d", index+1)
		lines[index] = markers[index] + " native history ordering"
	}
	answer := strings.Join(lines, "\n")

	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var physical bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&physical) {
		t.Fatal("unified renderer did not attach")
	}
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	bridge.BeginRun()

	for _, event := range []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: sessionID, Payload: map[string]interface{}{"turn_id": turnID}},
		{Type: runtimechat.EventLLMRequestStarted, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
		}},
		{Type: runtimechat.EventAssistantDelta, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "sequence": uint64(1), "delta": answer,
		}},
		{Type: runtimechat.EventLLMRequestFinished, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		}},
	} {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	streaming := coordinator.uiActor.AppState()
	if streaming.Active.Phase != ui.ActiveCellMutable || streaming.Active.Kind != scene.KindAssistant ||
		streaming.Active.Source != boundary.FormatAssistantBlockChrome(answer) {
		t.Fatalf("long assistant is not the resident active cell: %+v", streaming.Active)
	}
	if streaming.Active.Acked.End != 0 {
		t.Fatalf("assistant crossed native-history ordering barrier before late reasoning: active=%+v effects=%+v",
			streaming.Active, streaming.HistoryEffects.Entries())
	}
	if len(streaming.Transcript.Cells) != 2 || streaming.Transcript.Cells[0].Kind != scene.KindReasoning ||
		streaming.Transcript.Cells[0].Source != "" || streaming.Transcript.Cells[0].Phase != scene.CellMutable ||
		streaming.Transcript.Cells[1].Kind != scene.KindAssistant {
		t.Fatalf("missing empty reasoning ordering barrier: %+v", streaming.Transcript.Cells)
	}

	for _, event := range []runtimeevents.Event{
		{Type: "assistant.reasoning", SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
			"reasoning": map[string]interface{}{"format": "summary", "summary": reasoning},
		}},
		{Type: runtimechat.EventAssistantMessage, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "content": answer,
		}},
		{Type: runtimechat.EventSessionEnd, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "success": true,
		}},
	} {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	final := coordinator.uiActor.AppState()
	if len(final.Transcript.Cells) != 2 || !strings.Contains(final.Transcript.Cells[0].Source, reasoning) ||
		final.Transcript.Cells[1].Source != boundary.FormatAssistantBlockChrome(answer) || final.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("late reasoning long-flow semantic order = %+v active=%+v", final.Transcript.Cells, final.Active)
	}
	for _, entry := range final.HistoryEffects.Entries() {
		if entry.State == ui.HistoryCommitPending || entry.State == ui.HistoryCommitInFlight ||
			entry.State == ui.HistoryCommitStateFailed || entry.MayHavePartiallyWritten {
			t.Fatalf("late reasoning history effect not settled: %#v", entry)
		}
	}
	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	if reasoningAt, answerAt := strings.Index(projected, reasoning), strings.Index(projected, markers[0]); reasoningAt < 0 || answerAt < 0 || reasoningAt >= answerAt {
		t.Fatalf("late long physical order reasoning=%d answer=%d\n%s", reasoningAt, answerAt, screen.Dump())
	}
	for _, marker := range markers {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("late long marker %q count=%d\n%s", marker, count, screen.Dump())
		}
	}
}

func TestSessionEndFinalizesOrphanMutableToolExactlyOnce(t *testing.T) {
	const width, height = 96, 18
	const (
		sessionID = "session-orphan-tool"
		turnID    = "turn-orphan-tool"
		callID    = "call-orphan-tool"
		toolHead  = "ORPHAN-TOOL-COMMAND"
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var physical bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&physical) {
		t.Fatal("unified renderer did not attach")
	}
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	bridge.BeginRun()

	for _, event := range []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: sessionID, Payload: map[string]interface{}{"turn_id": turnID}},
		{Type: "tool.requested", SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "tool_call_id": callID, "tool_name": "shell", "command_text": toolHead,
		}},
		{Type: "tool.progress", SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "tool_call_id": callID, "tool_name": "shell", "message": "orphan progress",
		}},
		{Type: runtimechat.EventSessionEnd, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "success": false, "error": "provider ended without tool terminal event",
		}},
	} {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("session.end retained orphan tool active: %+v", state.Active)
	}
	if coordinator.AgentStage() == chatAgentStageToolRunning || len(coordinator.activeTools) != 0 {
		t.Fatalf("session.end retained coordinator tool stage: stage=%q tools=%+v", coordinator.AgentStage(), coordinator.activeTools)
	}
	if len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Kind != scene.KindToolChain ||
		state.Transcript.Cells[0].Phase != scene.CellCommitted || !strings.Contains(state.Transcript.Cells[0].Source, toolHead) {
		t.Fatalf("orphan tool did not converge to one committed chain: %+v", state.Transcript.Cells)
	}
	if state.HistoryEffects.ProjectionUnknown || state.HistoryEffects.HasPending() {
		t.Fatalf("orphan tool history did not settle: %+v", state.HistoryEffects)
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	if count := strings.Count(projected, toolHead); count != 1 {
		t.Fatalf("orphan tool physical count=%d\n%s", count, screen.Dump())
	}
	for _, raw := range []string{"tool.requested", "tool.progress", "session_end"} {
		if strings.Contains(projected, raw) {
			t.Fatalf("raw lifecycle %q leaked\n%s", raw, screen.Dump())
		}
	}
}

// Production delivers the successful request boundary before assistant_message.
// The bridge must keep the coordinator stream alive until that authoritative
// final reaches the actor; otherwise the last coalesced delta tail can disappear
// when the active viewport is replaced by the committed transcript.
func TestSuccessfulRequestBoundaryPreservesFortyLineFinalInNativeHistory(t *testing.T) {
	const width, height = 100, 24
	markers := make([]string, 40)
	lines := make([]string, len(markers))
	for index := range markers {
		markers[index] = fmt.Sprintf("BOUNDARY-FINAL-%02d", index+1)
		lines[index] = markers[index] + " terminal history validation"
	}
	answerPrefix := "Terminal scrollback keeps completed rows in the host buffer.\n\n" +
		"BOUNDARY-REASONING-SENTINEL\n\n"
	answer := answerPrefix + strings.Join(lines, "\n")
	traceID, turnID, streamID := "trace-boundary", "turn-boundary", "stream-boundary"

	session := &ChatSession{Stream: true}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	session.Interaction = coordinator
	t.Cleanup(coordinator.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var physical bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&physical) {
		t.Fatal("unified renderer did not attach")
	}

	bridge.BeginRun()
	events := []runtimeevents.Event{{
		Type: "llm.request.started", TraceID: traceID,
		Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1,
		},
	}, {
		Type: runtimechat.EventAssistantDelta, TraceID: traceID,
		Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
			"step": 1, "sequence": uint64(1), "delta": answerPrefix,
		},
	}}
	for index, line := range lines {
		delta := line
		if index+1 < len(lines) {
			delta += "\n"
		}
		events = append(events, runtimeevents.Event{
			Type: runtimechat.EventAssistantDelta, TraceID: traceID,
			Payload: map[string]interface{}{
				"trace_id": traceID, "turn_id": turnID, "stream_id": streamID,
				"step": 1, "sequence": uint64(index + 2), "delta": delta,
			},
		})
	}
	events = append(events,
		runtimeevents.Event{Type: "llm.request.finished", TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		}},
		runtimeevents.Event{Type: runtimechat.EventAssistantMessage, TraceID: traceID, Payload: map[string]interface{}{
			"trace_id": traceID, "turn_id": turnID, "stream_id": streamID, "content": answer,
		}},
	)
	for _, event := range events {
		bridge.handleQueuedEvent(chatRuntimeQueuedEvent{event: event, epoch: bridge.runEpoch})
		if event.Type == runtimechat.EventAssistantDelta {
			coordinator.waitUIActorIdle()
			awaitUnifiedPresenterIdle(t, coordinator)
			coordinator.waitUIActorIdle()
		}
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.State()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("authoritative final left active cell mounted: %+v", state.Active)
	}
	if len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Source != boundary.FormatAssistantBlockChrome(answer) ||
		state.Transcript.Cells[0].Phase != scene.CellCommitted {
		t.Fatalf("committed transcript lost authoritative final: %+v", state.Transcript.Cells)
	}
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == ui.HistoryCommitPending || entry.State == ui.HistoryCommitInFlight ||
			entry.State == ui.HistoryCommitStateFailed || entry.MayHavePartiallyWritten {
			t.Fatalf("history effect not acknowledged: %#v", entry)
		}
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	for _, marker := range markers {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("physical marker %q count=%d, want exactly one\nscrollback=%q\neffects=%#v\n%s",
				marker, count, screen.ScrollbackLines(), state.HistoryEffects.Entries(), screen.Dump())
		}
	}
	projectedLines := append(screen.ScrollbackLines(), screen.Lines(1, height)...)
	markerLine := make(map[string]int, len(markers))
	for lineIndex, line := range projectedLines {
		trimmed := strings.TrimSpace(line)
		for _, marker := range markers {
			if strings.Contains(trimmed, marker) {
				markerLine[marker] = lineIndex
			}
		}
	}
	for index := 1; index < len(markers); index++ {
		previous, previousFound := markerLine[markers[index-1]]
		current, currentFound := markerLine[markers[index]]
		if !previousFound || !currentFound || current != previous+1 {
			t.Fatalf("physical markers %q and %q are not adjacent: lines=%d/%d\n%s",
				markers[index-1], markers[index], previous, current, screen.Dump())
		}
	}
}

// A long streamed answer is split between native history and the resident
// active tail before assistant.message transfers the whole cell to immutable
// transcript ownership. This follows the production ingress worker and actor
// causal queue so a dropped resident tail cannot be hidden by a direct Scene
// replacement fixture.
func TestStreamingAssistantFinalTailTransfersExactlyOnceToNativeHistory(t *testing.T) {
	const (
		width     = 100
		height    = 24
		sessionID = "session-stream-tail"
		turnID    = "turn-stream-tail"
		streamID  = "stream-stream-tail"
	)
	t.Setenv("NO_COLOR", "1")
	ui.SetTheme(ui.ThemeAuto)

	markers := make([]string, 40)
	for index := range markers {
		markers[index] = fmt.Sprintf("STREAM-FINAL-TAIL-%02d terminal history validation", index+1)
	}
	answer := strings.Join(markers, "\n")
	const reasoning = "STREAM-FINAL-TAIL-REASONING"

	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: sessionID}}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var physical bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&physical) {
		t.Fatal("unified renderer did not attach")
	}
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	bridge.BeginRun()

	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventSessionStart, SessionID: sessionID,
		Payload: map[string]interface{}{"turn_id": turnID},
	})
	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventLLMRequestStarted, SessionID: sessionID,
		Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
		},
	})
	bridge.Handle(runtimeevents.Event{
		Type: "assistant.reasoning", SessionID: sessionID,
		Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1,
			"reasoning": map[string]interface{}{
				"format": "stream_delta", "summary": reasoning,
			},
		},
	})
	chunkWidths := [...]int{1, 2, 3, 5, 8}
	sequence := uint64(1)
	for offset := 0; offset < len(answer); sequence++ {
		end := offset + chunkWidths[int(sequence-1)%len(chunkWidths)]
		if end > len(answer) {
			end = len(answer)
		}
		delta := answer[offset:end]
		bridge.Handle(runtimeevents.Event{
			Type: runtimechat.EventAssistantDelta, SessionID: sessionID,
			Payload: map[string]interface{}{
				"turn_id": turnID, "stream_id": streamID,
				"step": 1, "sequence": sequence, "delta": delta,
			},
		})
		offset = end
	}
	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventLLMRequestFinished, SessionID: sessionID,
		Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "step": 1, "success": true,
		},
	})
	bridge.WaitForCurrentEvents(5 * time.Second)
	awaitUnifiedPresenterIdle(t, coordinator)

	streaming := coordinator.uiActor.AppState()
	if streaming.Active.Phase != ui.ActiveCellMutable || streaming.Active.Source != boundary.FormatAssistantBlockChrome(answer) {
		t.Fatalf("streaming active source was not canonical: %+v", streaming.Active)
	}
	if streaming.Active.Stable.End != len(answer) || streaming.Active.Enqueued.End < streaming.Active.Acked.End {
		t.Fatalf("streaming range ledger is inconsistent: %+v", streaming.Active)
	}
	if streaming.Active.Acked.End <= 0 || streaming.Active.Acked.End >= len(answer) {
		t.Fatalf("fixture did not split acknowledged prefix from resident tail: active=%+v effects=%+v",
			streaming.Active, streaming.HistoryEffects.Entries())
	}

	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventAssistantMessage, SessionID: sessionID,
		Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "content": answer,
		},
	})
	bridge.Handle(runtimeevents.Event{
		Type: runtimechat.EventSessionEnd, SessionID: sessionID,
		Payload: map[string]interface{}{
			"turn_id": turnID, "success": true,
		},
	})
	bridge.WaitForCurrentEvents(5 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	final := coordinator.uiActor.AppState()
	if final.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("authoritative final retained mutable ownership: %+v", final.Active)
	}
	if len(final.Transcript.Cells) != 2 || !strings.Contains(final.Transcript.Cells[0].Source, reasoning) ||
		final.Transcript.Cells[1].Source != boundary.FormatAssistantBlockChrome(answer) || final.Transcript.Cells[1].Phase != scene.CellCommitted {
		t.Fatalf("authoritative final transcript lost reasoning/assistant order: %+v", final.Transcript)
	}
	if final.HistoryEffects.ProjectionUnknown || final.HistoryEffects.HasPending() {
		t.Fatalf("final history delivery did not settle: %+v", final.HistoryEffects)
	}
	tailAcknowledged := false
	// HistoryCommit.SourceRange 与流式 ledger（Stable/Enqueued/Acked）统一
	// 使用语义正文的字节坐标。
	semanticAnswer := answer
	for _, entry := range final.HistoryEffects.Entries() {
		if entry.State == ui.HistoryCommitAcked && entry.Commit.CellID == streaming.Active.CellID &&
			entry.Commit.SourceRange.End == len(semanticAnswer) {
			tailAcknowledged = true
			break
		}
	}
	if !tailAcknowledged {
		t.Fatalf("resident final tail never acquired an acknowledged history range: %+v",
			final.HistoryEffects.Entries())
	}

	screen := vt.NewScreen(width, height)
	screen.Feed(physical.String())
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(1, height)...), "\n")
	for _, marker := range markers {
		if count := strings.Count(projected, marker); count != 1 {
			t.Fatalf("physical marker %q count=%d, want exactly one\nactive-before=%+v\neffects=%+v\n%s",
				marker, count, streaming.Active, final.HistoryEffects.Entries(), screen.Dump())
		}
	}
}
