package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestAICLIReplayTranscriptRenderer_SupplementDoesNotInject(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	renderer := newAICLIReplayTranscriptRenderer(session)
	if !renderer.RenderSupplement("historical notice") {
		t.Fatal("replay supplement was not rendered")
	}
	if snapshot := bridge.sceneSnapshot(); snapshot != nil && len(snapshot.Cells) != 0 {
		t.Fatalf("history replay injected a duplicate supplement cell: %+v", snapshot.Cells)
	}
}

func TestAICLIReplayTranscriptRenderer_AssistantDoesNotInject(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	renderer := newAICLIReplayTranscriptRenderer(session)
	if !renderer.RenderAssistant("historical assistant") {
		t.Fatal("replay assistant was not rendered")
	}
	if snapshot := bridge.sceneSnapshot(); snapshot != nil && len(snapshot.Cells) != 0 {
		t.Fatalf("history replay injected an assistant cell: %+v", snapshot.Cells)
	}
}

// Local notices have no runtime event to encode. They must therefore enter the
// Scene explicitly, and their immutable snapshot must still cross the actor
// boundary only after the legacy coordinator lock is released.
func TestChatInteractionCoordinator_LocalSupplementProjectsTranscript(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	coordinator.RenderLocalSupplement("[retry] retrying request")
	coordinator.waitUIActorIdle()
	if blocks, matched, missed, lastErr := bridge.textParityStats(); blocks != 1 || matched != 1 || missed != 0 {
		t.Fatalf("local supplement parity: blocks=%d matched=%d missed=%d last=%q output=%q", blocks, matched, missed, lastErr, output.String())
	}

	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("scene cells = %+v, want one local supplement", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindSupplement || cell.Source != "[retry] retrying request" {
		t.Fatalf("scene cell = %+v, want supplement source", cell)
	}
	if coordinator.uiActor == nil {
		t.Fatal("local supplement did not initialize UI actor")
	}
	state := coordinator.uiActor.AppState()
	if state.Transcript.Revision != snapshot.Revision || len(state.Transcript.Cells) != 1 {
		t.Fatalf("AppState transcript = %+v, scene = %+v", state.Transcript, snapshot)
	}
	if stats := coordinator.uiActor.Stats(); stats.LastAction != "ReplaceTranscript" {
		t.Fatalf("local supplement was not projected as ReplaceTranscript: %+v", stats)
	}
}

func TestChatInteractionCoordinator_LocalAssistantProjectsTranscript(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	coordinator.RenderLocalAssistant("legacy final response")
	coordinator.waitUIActorIdle()
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("scene cells = %+v, want one direct assistant", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindAssistant || cell.Source != "legacy final response" || cell.Phase != scene.CellCommitted {
		t.Fatalf("scene cell = %+v, want committed assistant", cell)
	}
}

func TestRenderChatResponse_UsesLocalAssistantInjection(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	renderChatResponse(session, "legacy executor response")
	coordinator.waitUIActorIdle()
	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 || snapshot.Cells[0].Kind != scene.KindAssistant {
		t.Fatalf("renderChatResponse did not inject direct assistant: %+v", snapshot)
	}
}

// The event-log injection has to retain the supplement kind. Replaying a
// local notice as an error or generic system event would change the semantic
// cell and break later Layout/Presenter ownership decisions.
func TestChatRuntimeEventBridge_ReplayRestoresLocalSupplement(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	bridge1.submitSupplement("[goal] auto continuation limit reached")

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if !strings.Contains(string(raw), `"supplement":"[goal] auto continuation limit reached"`) {
		t.Fatalf("log missing supplement injection: %q", string(raw))
	}

	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	if replayed, err := bridge2.replayEventLog(); err != nil || replayed != 1 {
		t.Fatalf("replayEventLog = %d, %v; want 1, nil", replayed, err)
	}
	snapshot := bridge2.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("replayed cells = %+v, want one", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindSupplement || cell.Source != "[goal] auto continuation limit reached" {
		t.Fatalf("replayed cell = %+v, want supplement", cell)
	}
}

func TestChatRuntimeEventBridge_ReplayRestoresLocalAssistant(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	bridge1.submitAssistant("legacy executor response")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if !strings.Contains(string(raw), `"assistant":"legacy executor response"`) {
		t.Fatalf("log missing direct assistant injection: %q", string(raw))
	}

	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	if replayed, err := bridge2.replayEventLog(); err != nil || replayed != 1 {
		t.Fatalf("replayEventLog = %d, %v; want 1, nil", replayed, err)
	}
	snapshot := bridge2.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("replayed cells = %+v, want one", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindAssistant || cell.Source != "legacy executor response" || cell.Phase != scene.CellCommitted {
		t.Fatalf("replayed cell = %+v, want completed assistant", cell)
	}
}

// approval_requested is encoded before the bridge enters its synchronous stdin
// exception. It is pending identity only; the retained prompt transcript is
// the one committed Scene cell.
func TestPriorityPromptTranscriptCompletesRuntimeSceneCell(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	event := runtimeevents.Event{
		Type: runtimechat.EventApprovalRequested,
		Payload: map[string]interface{}{
			"request_id": "approval-scene-1",
			"tool_name":  "execute_shell_command",
		},
	}
	bridge.encodeRenderModelEvent(event)
	if snapshot := bridge.sceneSnapshot(); snapshot != nil && len(snapshot.Cells) != 0 {
		t.Fatalf("request created Scene placeholder: %+v", snapshot.Cells)
	}
	bridge.setPriorityTranscriptTarget(event)
	renderChatRuntimePriorityPromptTranscript(session,
		[]string{"[审批] 工具：execute_shell_command"},
		"[审批] 请选择： ", "1")
	coordinator.waitUIActorIdle()

	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("Scene = %+v, want exactly one priority cell", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindSupplement || cell.Phase != scene.CellCommitted {
		t.Fatalf("priority cell = %+v, want committed supplement cell", cell)
	}
	for _, want := range []string{"[审批] 工具：execute_shell_command", "[审批] 请选择： 1"} {
		if !strings.Contains(cell.Source, want) || !strings.Contains(output.String(), want) {
			t.Fatalf("priority transcript missing %q: scene=%q output=%q", want, cell.Source, output.String())
		}
	}
	if state := coordinator.uiActor.AppState(); len(state.Transcript.Cells) != 1 ||
		state.Transcript.Cells[0].Source != cell.Source {
		t.Fatalf("AppState transcript = %+v, want canonical priority cell", state.Transcript)
	}
}

func TestPriorityPromptTranscriptFollowsApprovalHintInSceneAndLegacyOutput(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	event := runtimeevents.Event{
		Type: runtimechat.EventApprovalRequested,
		Payload: map[string]interface{}{
			"request_id": "approval-order-1",
			"tool_name":  "execute_shell_command",
		},
	}
	hint := "[tip] approval cache is not available for this command."
	transcript := "[approval] command: execute_shell_command\n[approval] select: 1"
	bridge.encodeRenderModelEvent(event)
	coordinator.RenderLocalSupplement(hint)
	bridge.setPriorityTranscriptTarget(event)
	renderChatRuntimePriorityPromptTranscript(session,
		[]string{"[approval] command: execute_shell_command"}, "[approval] select: ", "1")
	coordinator.waitUIActorIdle()

	snapshot := bridge.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 2 {
		t.Fatalf("Scene = %+v, want hint then one priority transcript", snapshot)
	}
	for i, want := range []string{hint, transcript} {
		cell := snapshot.Cells[i]
		if cell.Kind != scene.KindSupplement || cell.Phase != scene.CellCommitted || cell.Source != want {
			t.Fatalf("Scene cell %d = %+v, want committed supplement %q", i, cell, want)
		}
	}
	legacy := output.String()
	hintAt, transcriptAt := strings.Index(legacy, hint), strings.Index(legacy, transcript)
	if hintAt < 0 || transcriptAt < 0 || hintAt >= transcriptAt {
		t.Fatalf("legacy order=%q, want hint before transcript", legacy)
	}
	state := coordinator.uiActor.AppState()
	if len(state.Transcript.Cells) != 2 ||
		state.Transcript.Cells[0].Source != hint || state.Transcript.Cells[1].Source != transcript {
		t.Fatalf("AppState transcript = %+v, want Scene order", state.Transcript)
	}
}

func TestChatRuntimeEventBridge_ReplayRestoresPriorityPromptWithoutDuplicate(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	requested := runtimeevents.Event{
		Type: runtimechat.EventQuestionAsked,
		Payload: map[string]interface{}{
			"question_id": "question-replay-1",
			"prompt":      "choose a model",
		},
	}
	transcript := "[提问] 问题：choose a model\n[提问] 请输入回答： gpt-test"

	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	bridge1.encodeRenderModelEvent(requested)
	if ok := bridge1.submitPriorityTranscript(runtimechat.EventQuestionAsked, "question-replay-1", transcript); !ok {
		t.Fatal("priority transcript was not applied")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if !strings.Contains(string(raw), `"priority_kind":"question_asked"`) ||
		!strings.Contains(string(raw), `"priority_transcript":"[提问]`) {
		t.Fatalf("log missing priority transcript injection: %q", string(raw))
	}

	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	if replayed, err := bridge2.replayEventLog(); err != nil || replayed != 2 {
		t.Fatalf("replayEventLog = %d, %v; want 2, nil", replayed, err)
	}
	snapshot := bridge2.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("replayed Scene = %+v, want one priority cell", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindSupplement || cell.Phase != scene.CellCommitted || cell.Source != transcript {
		t.Fatalf("replayed priority cell = %+v", cell)
	}
}

// Runtime-event rendering already calls EventEncoder.Encode before the legacy
// line writer. RenderAsyncLine is intentionally still a projection-only path;
// changing it to inject would create a duplicate Scene cell for every mapped
// timeline event.
func TestChatInteractionCoordinator_RuntimeAsyncLineDoesNotInjectSecondSceneCell(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type: runtimechat.EventSessionCompactCompleted,
		Payload: map[string]interface{}{
			"summary": "compact complete",
		},
	})
	before := len(bridge.sceneSnapshot().Cells)
	if before != 1 {
		t.Fatalf("mapped runtime cells = %d, want 1", before)
	}
	coordinator.RenderAsyncLine("compact complete")
	if after := len(bridge.sceneSnapshot().Cells); after != before {
		t.Fatalf("RenderAsyncLine injected duplicate Scene cell: before=%d after=%d", before, after)
	}
}

func TestChatInteractionCoordinator_RuntimeAssistantDoesNotInjectSecondSceneCell(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	var output bytes.Buffer
	coordinator.SetWriter(&output)
	for _, event := range renderParityTwoTurnEvents()[:5] {
		bridge.encodeRenderModelEvent(event)
	}
	before := len(bridge.sceneSnapshot().Cells)
	coordinator.RenderAssistant("你好")
	if after := len(bridge.sceneSnapshot().Cells); after != before {
		t.Fatalf("RenderAssistant injected duplicate runtime Scene cell: before=%d after=%d", before, after)
	}
}

func TestChatInteractionCoordinator_DirectToolLifecycleProjectsOneSceneCell(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	requested := runtimechatcore.ChatEvent{
		Type: runtimechatcore.EventTool, Stage: "tool_requested",
		ToolName: "read_file", ToolCallID: "call-direct",
		Arguments: map[string]interface{}{"path": "a.go"},
	}
	if !coordinator.RenderToolChainEvent(requested) {
		t.Fatal("direct tool request was not rendered")
	}
	requestedSnapshot := bridge.sceneSnapshot()
	if requestedSnapshot == nil || len(requestedSnapshot.Cells) != 1 {
		t.Fatalf("requested Scene=%+v want one mutable chain", requestedSnapshot)
	}
	if cell := requestedSnapshot.Cells[0]; cell.Kind != scene.KindToolChain || cell.Phase != scene.CellMutable || cell.Source != "read_file" {
		t.Fatalf("requested cell=%+v want mutable read_file chain", cell)
	}

	result := requested
	result.Stage = "tool_result"
	result.Output = "file content"
	result.Success = true
	wantDisplay := renderSharedChatToolEvent(result)
	if !coordinator.RenderToolChainEvent(result) {
		t.Fatal("direct tool result was not rendered")
	}
	coordinator.waitUIActorIdle()
	blocks, matched, missed, lastErr := bridge.textParityStats()
	finalSnapshot := bridge.sceneSnapshot()
	if finalSnapshot == nil || len(finalSnapshot.Cells) != 1 {
		t.Fatalf("final Scene=%+v want one committed chain", finalSnapshot)
	}
	cell := finalSnapshot.Cells[0]
	if cell.Kind != scene.KindToolChain || cell.Phase != scene.CellCommitted || cell.Source != wantDisplay {
		t.Fatalf("final cell=%+v want one committed display chain %q", cell, wantDisplay)
	}
	if state := coordinator.uiActor.AppState(); len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Source != cell.Source {
		t.Fatalf("AppState transcript=%+v want Scene source=%q", state.Transcript, cell.Source)
	}
	if matched != 1 || missed != 0 {
		t.Fatalf("direct tool parity mismatch: blocks=%d matched=%d missed=%d last=%q", blocks, matched, missed, lastErr)
	}
}

func TestAICLIReplayTranscriptRenderer_ToolDoesNotInject(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	renderer := newAICLIReplayTranscriptRenderer(session)
	if !renderer.RenderToolEvent(runtimechatcore.ChatEvent{
		Type: runtimechatcore.EventTool, Stage: "tool_result",
		ToolName: "read_file", ToolCallID: "call-history", Output: "historical output", Success: true,
	}) {
		t.Fatal("replay tool result was not rendered")
	}
	if snapshot := bridge.sceneSnapshot(); snapshot != nil && len(snapshot.Cells) != 0 {
		t.Fatalf("history replay injected a tool Scene cell: %+v", snapshot.Cells)
	}
}

func TestChatRuntimeEventBridge_ReplayRestoresDirectToolChain(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	bridge1 := newChatRuntimeEventBridge(&ChatSession{})
	bridge1.eventLogPathOverride = logPath
	bridge1.submitToolRequested("call-direct", "read_file", map[string]interface{}{"path": "a.go"})
	wantDisplay := "• Completed read_file path=a.go"
	bridge1.submitToolResultDisplay("call-direct", "read_file", "file content", "", true, wantDisplay)

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	if got := strings.Count(string(raw), `"type":"tool.`); got != 2 {
		t.Fatalf("direct tool log count=%d want 2, log=%q", got, string(raw))
	}
	if !strings.Contains(string(raw), `"display_head":"• Completed read_file path=a.go"`) || !strings.Contains(string(raw), `"output":"file content"`) {
		t.Fatalf("direct tool log must retain display and raw result: %q", string(raw))
	}

	bridge2 := newChatRuntimeEventBridge(&ChatSession{})
	bridge2.eventLogPathOverride = logPath
	if replayed, err := bridge2.replayEventLog(); err != nil || replayed != 2 {
		t.Fatalf("replayEventLog=%d, %v want 2, nil", replayed, err)
	}
	snapshot := bridge2.sceneSnapshot()
	if snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("replayed Scene=%+v want one tool chain", snapshot)
	}
	cell := snapshot.Cells[0]
	if cell.Kind != scene.KindToolChain || cell.Phase != scene.CellCommitted || cell.Source != wantDisplay {
		t.Fatalf("replayed tool cell=%+v", cell)
	}
}
