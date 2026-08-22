package commands

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestUnifiedRendererActionNeedsFlushDoesNotSelfRetryDeliveryBookkeeping(t *testing.T) {
	noFlush := []ui.UIAction{
		ui.BeginHistoryCommit{},
		ui.HistoryCommitFailed{},
		ui.HistoryCommitDeferred{},
		ui.HistoryProjectionInvalidated{},
		ui.HistoryProjectionRecovered{},
	}
	for _, action := range noFlush {
		if unifiedRendererActionNeedsFlush(action) {
			t.Errorf("%T unexpectedly requested a generic frame", action)
		}
	}
	for _, action := range []ui.UIAction{ui.UpdateActiveCellAction{}, ui.HistoryCommitAcknowledged{}, ui.HistoryScrollbackReconciled{}} {
		if !unifiedRendererActionNeedsFlush(action) {
			t.Errorf("%T omitted a visible/current-state frame", action)
		}
	}
}

type persistentTerminalErrorWriter struct {
	mu     sync.Mutex
	writes int
}

func (w *persistentTerminalErrorWriter) Write([]byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	return 0, errors.New("persistent terminal failure")
}

func (w *persistentTerminalErrorWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func TestUnifiedRendererPersistentFrameErrorDoesNotSelfWake(t *testing.T) {
	session := &ChatSession{Stream: true}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)

	writer := &persistentTerminalErrorWriter{}
	if !coordinator.enableUnifiedRendererWithWriter(writer) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.mu.Lock()
	presenter := coordinator.primaryPresenter
	coordinator.mu.Unlock()
	if presenter == nil {
		t.Fatal("unified presenter is missing")
	}

	done := make(chan struct{})
	go func() {
		presenter.WaitIdle()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("persistent frame error kept self-waking the presenter")
	}
	coordinator.waitUIActorIdle()
	// Initial attach and its causally posted geometry barrier may coalesce into
	// one request or race as two. Neither failed attempt may manufacture a third.
	if writes := writer.writeCount(); writes < 1 || writes > 2 {
		t.Fatalf("persistent frame error attempts = %d, want one or two bounded setup attempts", writes)
	}
	if state := coordinator.uiActor.State(); !state.HistoryEffects.ProjectionUnknown {
		t.Fatal("terminal frame error did not invalidate the logical projection")
	}
}

func TestChatInteractionCoordinatorSetSurfaceKeepsSessionSurfaceInSync(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)

	first := ui.NewFixedBottomSurface(ui.NewTerminal())
	second := ui.NewFixedBottomSurface(ui.NewTerminal())

	coordinator.SetSurface(first)
	if session.Surface != first {
		t.Fatal("session surface must match coordinator surface after initial bind")
	}

	coordinator.SetSurface(second)
	if session.Surface != second {
		t.Fatal("session surface must follow coordinator surface replacement")
	}

	coordinator.SetSurface(nil)
	if session.Surface != nil {
		t.Fatal("session surface must clear with coordinator surface")
	}
}

func TestChatInteractionCoordinatorUnifiedRendererUsesOnlyPrimaryPresenter(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	// Setup normally fences this before coordinator wiring. Doing it here keeps
	// this unit test from emitting legacy surface bytes to the test terminal.
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)

	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach a primary presenter")
	}
	if surface.PhysicalWritesEnabled() {
		t.Fatal("legacy surface writer remained enabled after presenter attach")
	}
	surface.SetPhysicalWritesEnabled(true)
	if surface.PhysicalWritesEnabled() {
		t.Fatal("unified presenter fence allowed legacy writer re-enable")
	}
	if session.TerminalSession == nil || session.TerminalSessionExecutor == nil {
		t.Fatal("chat session did not publish primary terminal ownership")
	}
	if coordinator.uiActor == nil || !coordinator.uiActor.AppState().SemanticActiveCellProjection {
		t.Fatal("unified renderer did not fence AppState against legacy ActiveBand projection")
	}

	coordinator.RequestUnifiedFrame()
	coordinator.waitUIActorIdle()
	coordinator.mu.Lock()
	presenter := coordinator.primaryPresenter
	coordinator.mu.Unlock()
	if presenter == nil {
		t.Fatal("coordinator lost its primary presenter")
	}
	presenter.WaitIdle()
	if output.Len() == 0 {
		t.Fatal("primary presenter did not emit the initial frame")
	}
}

// Ctrl+T's transcript pager must borrow the primary terminal owner rather than
// use FixedBottomSurface as a second DEC 1049 writer. This exercises the
// coordinator wiring, not just ScreenLease's standalone transport adapter.
func TestChatInteractionCoordinatorUnifiedScreenLeaseUsesPresenterTransport(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)

	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if session.TerminalSession == nil {
		t.Fatal("unified renderer did not publish a terminal session")
	}
	baselineFrame := session.TerminalSession.ProjectionState().Frame
	output.Reset()

	lease, err := surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: "Transcript"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if !strings.Contains(output.String(), "\x1b[?1049h") {
		t.Fatalf("presenter did not enter alternate screen: %q", output.String())
	}
	if session.TerminalSession == nil || session.TerminalSession.AlternateScreenLeaseID() != lease.ID() {
		t.Fatalf("terminal session alternate owner = %v, want lease %d", session.TerminalSession, lease.ID())
	}
	coordinator.waitUIActorIdle()
	if state := coordinator.uiActor.AppState(); !state.Lease.Active || state.Lease.ID != lease.ID() {
		t.Fatalf("lease acquire did not reach AppState: %+v", state.Lease)
	}
	if !coordinator.postUIAction(ui.OpenTranscriptOverlay{LeaseID: lease.ID()}) {
		t.Fatal("failed to open the actor-owned transcript overlay")
	}
	coordinator.waitUIActorIdle()
	if overlay := coordinator.uiActor.AppState().TranscriptOverlay; !overlay.Active || overlay.LeaseID != lease.ID() {
		t.Fatalf("transcript overlay did not bind to lease: %+v", overlay)
	}

	writer, ok := lease.(ui.AlternateScreenLeaseWriter)
	if !ok {
		t.Fatalf("unified lease did not expose alternate writer: %T", lease)
	}
	const pagerFrame = "pager-frame-from-presenter"
	if err := writer.WriteAlternateScreen(pagerFrame); err != nil {
		t.Fatalf("WriteAlternateScreen: %v", err)
	}
	if !strings.Contains(output.String(), pagerFrame) {
		t.Fatalf("pager frame bypassed terminal session: %q", output.String())
	}

	if !coordinator.postUIAction(ui.CloseTranscriptOverlay{LeaseID: lease.ID()}) {
		t.Fatal("failed to close the actor-owned transcript overlay")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	if session.TerminalSession.AlternateScreenLeaseID() != 0 {
		t.Fatalf("terminal session retained alternate lease %d", session.TerminalSession.AlternateScreenLeaseID())
	}
	if state := coordinator.uiActor.AppState(); state.Lease.Active || state.Lease.ID != 0 {
		t.Fatalf("lease release did not reach AppState: %+v", state.Lease)
	}
	if overlay := coordinator.uiActor.AppState().TranscriptOverlay; overlay.Active || overlay.LeaseID != 0 {
		t.Fatalf("transcript overlay remained after release: %+v", overlay)
	}
	if !strings.Contains(output.String(), "\x1b[?1049l") {
		t.Fatalf("presenter did not exit alternate screen: %q", output.String())
	}
	if frame := session.TerminalSession.ProjectionState().Frame; frame <= baselineFrame {
		t.Fatalf("lease release did not recover a primary presenter frame: got %d, baseline %d", frame, baselineFrame)
	}
	if surface.PhysicalWritesEnabled() {
		t.Fatal("alternate-screen lifecycle reopened the legacy surface writer")
	}
}

type shutdownExitRetryWriter struct {
	bytes.Buffer
	failExit int
}

func (w *shutdownExitRetryWriter) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("\x1b[?1049l")) && w.failExit > 0 {
		w.failExit--
		return 0, errors.New("transient alternate-screen exit failure")
	}
	return w.Buffer.Write(data)
}

// Shutdown must keep the presenter transport attached until a retryable
// alternate-screen exit succeeds. Detaching after the first zero-byte error
// leaves the host in DEC 1049 with no remaining owner for the exit sequence.
func TestChatInteractionCoordinatorShutdownRetriesAlternateExitBeforeDetach(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)

	writer := &shutdownExitRetryWriter{failExit: 1}
	if !coordinator.enableUnifiedRendererWithWriter(writer) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	terminal := session.TerminalSession
	if terminal == nil {
		t.Fatal("unified renderer did not publish terminal session")
	}

	lease, err := surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: "shutdown retry"})
	if err != nil {
		t.Fatalf("AcquireAlternateScreen: %v", err)
	}
	if terminal.AlternateScreenLeaseID() != lease.ID() {
		t.Fatalf("terminal lease=%d, want %d", terminal.AlternateScreenLeaseID(), lease.ID())
	}

	coordinator.Shutdown()
	if terminal.AlternateScreenLeaseID() != 0 {
		t.Fatalf("shutdown detached before alternate exit retry: lease=%d", terminal.AlternateScreenLeaseID())
	}
	if surface.LeaseActive() || lease.Active() {
		t.Fatalf("shutdown retained logical lease after successful retry: surface=%t lease=%t", surface.LeaseActive(), lease.Active())
	}
	if writer.failExit != 0 {
		t.Fatalf("shutdown did not exercise the transient exit failure: remaining=%d", writer.failExit)
	}
	if !strings.Contains(writer.String(), "\x1b[?1049l") {
		t.Fatalf("shutdown did not emit alternate-screen exit: %q", writer.String())
	}
}

func TestChatInteractionCoordinatorRejectsPresenterBesideLegacyWriter(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coordinator.Shutdown)

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	coordinator.SetSurface(surface)
	if !surface.PhysicalWritesEnabled() {
		t.Fatal("test requires an active legacy writer")
	}

	actor := coordinator.ensureUIActor()
	presenter := ui.NewTerminalSessionPresenter(actor, &bytes.Buffer{}, func() (int, int, bool) {
		return 48, 14, true
	})
	if coordinator.SetPrimaryPresenter(presenter) {
		presenter.Close()
		t.Fatal("presenter attached beside an active legacy terminal writer")
	}
}

func TestChatInteractionCoordinatorUnifiedEditorWriteNeverFallsBackToRawWriter(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	var presenterOutput bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&presenterOutput) {
		t.Fatal("unified renderer did not attach")
	}

	var rawEditorOutput bytes.Buffer
	if !coordinator.WritePromptEditorText(&rawEditorOutput, 0, 0, "draft") {
		t.Fatal("unified editor write was not claimed")
	}
	if rawEditorOutput.Len() != 0 {
		t.Fatalf("unified editor emitted raw terminal bytes: %q", rawEditorOutput.String())
	}

	coordinator.SetPromptInputSnapshot(ui.LineEditorSnapshot{Text: "draft", Cursor: 5})
	coordinator.waitUIActorIdle()
	coordinator.mu.Lock()
	presenter := coordinator.primaryPresenter
	coordinator.mu.Unlock()
	if presenter != nil {
		presenter.WaitIdle()
	}
	state := coordinator.uiActor.AppState()
	if state.Bottom.PromptInput != "draft" || state.Bottom.PromptCursor != 5 {
		t.Fatalf("InputEvent did not reach presenter state: %+v", state.Bottom)
	}
}

// This is the cutover fence: a surface facade action must update AppState and
// reach the TerminalSession presenter, but must not re-enter FixedBottomSurface
// through Apply. Physical-write fencing alone is insufficient because Apply
// would leave the old surface as a competing screen-state authority.
func TestChatInteractionCoordinatorUnifiedFacadeActionBypassesLegacySurfaceApply(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	coordinator.SetSurface(surface)

	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}
	if surface.PhysicalWritesEnabled() {
		t.Fatal("legacy surface writer was not fenced during direct cutover")
	}

	if !surface.ShowPrompt("> ") {
		t.Fatal("prompt facade action was rejected")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	output.Reset()

	const notice = "unified-facade-notice"
	if !surface.SetPromptNoticeLine(notice) {
		t.Fatal("notice facade action was rejected")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if state.Bottom.PromptNoticeLine != notice {
		t.Fatalf("facade action did not update AppState: %+v", state.Bottom)
	}
	if fixedSurfaceRowsContain(surface.BottomRowsSnapshot(), notice) {
		t.Fatalf("legacy surface Apply consumed unified facade action: %q", notice)
	}
	if !strings.Contains(output.String(), notice) {
		t.Fatalf("primary TerminalSession did not render facade state: %q", output.String())
	}
}

// Local output has historically taken the most error-prone path: it is not
// backed by a RuntimeEvent and previously committed directly into the owned
// viewport/historyWindow. In unified mode it must first become Scene/AppState
// data and the TerminalSession must be the sole byte producer.
func TestChatInteractionCoordinatorUnifiedLocalTranscriptUsesTerminalSessionOnly(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	coordinator.SetSurface(surface)

	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}
	if surface.PhysicalWritesEnabled() {
		t.Fatal("legacy surface writer remained enabled")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	output.Reset()

	const assistant = "unified-local-assistant"
	const supplement = "unified-local-supplement"
	const command = "unified-local-command"
	coordinator.RenderLocalAssistant(assistant)
	coordinator.RenderLocalSupplement(supplement)
	if ok := coordinator.RenderCommandDocument(render.Document{Blocks: []render.Block{{
		Lines: []render.Line{{Spans: []render.Span{{Text: command}}}},
	}}}); !ok {
		t.Fatal("RenderCommandDocument returned false")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if len(state.Transcript.Cells) != 3 {
		t.Fatalf("transcript cells = %d, want assistant + supplement + command", len(state.Transcript.Cells))
	}
	for _, want := range []string{assistant, supplement, command} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("TerminalSession output missing %q: %q", want, output.String())
		}
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified local transcript populated legacy historyWindow: %#v", got)
	}
	if session.TerminalSession == nil || session.TerminalSession.ProjectionState().Frame == 0 {
		t.Fatal("TerminalSession did not own the transcript frame")
	}
}

// TerminalSession ownership is a hard command cutover. Migrated commands may
// render a semantic document; every retained legacy command must instead be
// rejected as a Scene cell, never execute its old raw writer/modal path.
func TestDispatchChatCommandUnifiedCommandGateUsesTerminalSessionOnly(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 18)
	coordinator.SetSurface(surface)

	var presenterOutput bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&presenterOutput) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)
	presenterOutput.Reset()

	// Do not replace os.Stdout here. The package deliberately runs a number of
	// terminal/presenter tests in parallel, and process-global stdout capture
	// would attribute their asynchronous ANSI paint traffic to this command.
	// The assertions below use the owned terminal's observable state instead:
	// every result must reach the Scene and the attached TerminalSession, while
	// the fenced legacy surface must remain untouched.
	dispatchChatCommand(session, "/help", false)
	dispatchChatCommand(session, "/queue", false)
	dispatchChatCommand(session, "/attach clear", false)
	dispatchChatCommand(session, "/permission-mode", false)
	dispatchChatCommand(session, "/approval-reuse", false)
	dispatchChatCommand(session, "/not-a-command", false)
	dispatchChatCommand(session, "/shell", false)
	dispatchChatCommand(session, "/call", false)
	dispatchChatCommand(session, "/model", false)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	for _, want := range []string{
		"可用命令:",
		"当前 queued input: 0 pending",
		"已清空 0 个待发送图片附件",
		"当前 permission-mode:",
		"当前 approval-reuse:",
		"错误: /not-a-command 尚未迁移到统一渲染命令通道，已在 interactive TTY 中禁用。",
		"错误: 需要指定 shell 命令",
		"错误: /call 尚未迁移到统一渲染命令通道，已在 interactive TTY 中禁用。",
		"当前 provider:",
	} {
		if !strings.Contains(transcript.String(), want) {
			t.Fatalf("semantic transcript missing %q: %+v", want, state.Transcript)
		}
	}
	if !strings.Contains(presenterOutput.String(), "当前 provider:") {
		t.Fatalf("TerminalSession did not render latest command result: %q", presenterOutput.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified finite command output populated legacy historyWindow: %#v", got)
	}
}

// Streaming is the critical cutover case: the active body must advance from
// the mutable Scene cell, not from ActiveStreamController or a facade band,
// and the final Scene transition must use FinalizeActiveCellAction.
func TestChatInteractionCoordinatorUnifiedAssistantStreamUsesSceneActiveCell(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(48, 14)
	coordinator.SetSurface(surface)
	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}

	start := runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1",
	}}
	bridge.encodeRenderModelEvent(start)
	coordinator.postTranscriptSnapshotFromBridge(bridge)
	coordinator.waitUIActorIdle()

	delta := runtimeevents.Event{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1", "sequence": uint64(1), "delta": "scene streamed body",
	}}
	bridge.encodeRenderModelEvent(delta)
	coordinator.RenderAssistantDelta("scene streamed body")
	coordinator.postTranscriptSnapshotFromBridge(bridge)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellMutable || state.Active.Source != "scene streamed body" {
		t.Fatalf("stream did not advance Scene-backed active cell: %+v", state.Active)
	}
	if got := surface.ActiveBandLines(); len(got) != 0 {
		t.Fatalf("unified assistant stream populated legacy ActiveBand: %#v", got)
	}
	if !strings.Contains(output.String(), "scene streamed body") {
		t.Fatalf("TerminalSession frame omitted Scene active body: %q", output.String())
	}

	final := runtimeevents.Event{Type: runtimechat.EventAssistantMessage, Payload: map[string]interface{}{
		"turn_id": "turn-1", "stream_id": "stream-1", "content": "authoritative final body",
	}}
	bridge.encodeRenderModelEvent(final)
	if !coordinator.CompleteAssistantResponse("authoritative final body") {
		t.Fatal("unified stream completion was not claimed")
	}
	// Runtime-event reduction posts this snapshot immediately after the
	// coordinator callback. Mirror that causal follow-up in this focused test.
	coordinator.postTranscriptSnapshotFromBridge(bridge)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state = coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("finalized stream left an active visual source: %+v", state.Active)
	}
	if len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Source != "authoritative final body" {
		t.Fatalf("final Scene cell not committed exactly through transcript: %+v", state.Transcript)
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified stream wrote legacy historyWindow: %#v", got)
	}
}

// The local ReAct loop's production reasoning event is "assistant.reasoning"
// with a nested ReasoningBlock. This verifies the full unified route keeps the
// thought text, the assistant body and Markdown presentation in the one
// TerminalSession-owned frame instead of exposing the raw event name.
func TestChatInteractionCoordinatorUnifiedRendersDottedReasoningAndMarkdown(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(56, 18)
	coordinator.SetSurface(surface)
	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}

	bridge.encodeRenderModelEvent(runtimeevents.Event{Type: "assistant.reasoning", Payload: map[string]interface{}{
		"trace_id": "trace-markdown",
		"reasoning": map[string]interface{}{
			"format":  "stream_delta",
			"summary": "visible reasoning body",
		},
	}})
	bridge.encodeRenderModelEvent(runtimeevents.Event{Type: runtimechat.EventAssistantMessage, Payload: map[string]interface{}{
		"trace_id": "trace-markdown",
		"content":  "# Rendered answer\n\n- **complete**\n- `code`",
	}})
	coordinator.postTranscriptSnapshotFromBridge(bridge)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if len(state.Transcript.Cells) != 2 {
		t.Fatalf("transcript cells = %d, want reasoning + assistant: %#v", len(state.Transcript.Cells), state.Transcript.Cells)
	}
	var transcript strings.Builder
	for _, cell := range state.Transcript.Cells {
		transcript.WriteString(cell.Source)
		transcript.WriteByte('\n')
	}
	if strings.Contains(transcript.String(), "assistant.reasoning") || !strings.Contains(transcript.String(), "visible reasoning body") {
		t.Fatalf("reasoning scene projection is wrong: %q", transcript.String())
	}
	for _, want := range []string{"visible reasoning body", "Rendered answer", "complete", "code"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("TerminalSession output missing %q: %q", want, output.String())
		}
	}
	if strings.Contains(output.String(), "# Rendered answer") || strings.Contains(output.String(), "**complete**") {
		t.Fatalf("TerminalSession emitted raw Markdown: %q", output.String())
	}
	if got := surface.HistoryWindowForTest(); len(got) != 0 {
		t.Fatalf("unified renderer populated legacy historyWindow: %#v", got)
	}
}

func awaitUnifiedPresenterIdle(t *testing.T, coordinator *chatInteractionCoordinator) {
	t.Helper()
	coordinator.mu.Lock()
	presenter := coordinator.primaryPresenter
	coordinator.mu.Unlock()
	if presenter == nil {
		t.Fatal("unified primary presenter is missing")
	}
	presenter.WaitIdle()
}

// A terminal runtime sequence is allowed to omit assistant.message. Successful
// request-finished is only a transport boundary because production may publish
// the authoritative final immediately afterward; EndRun/session-end owns the
// missing-final fallback and must resolve the Scene mutable cell exactly once.
func TestChatInteractionCoordinatorUnifiedAssistantStreamTerminalBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		terminate func(*ChatSession, *chatRuntimeEventBridge, *chatInteractionCoordinator)
	}{
		{
			name: "llm finished then end run fallback without final message",
			terminate: func(_ *ChatSession, bridge *chatRuntimeEventBridge, coordinator *chatInteractionCoordinator) {
				bridge.handleEvent(runtimeevents.Event{Type: runtimechat.EventLLMRequestFinished, Payload: map[string]interface{}{
					"turn_id": "turn-terminal", "stream_id": "stream-terminal", "success": true,
				}})
				coordinator.postTranscriptSnapshotFromBridge(bridge)
				coordinator.waitUIActorIdle()
				if active := coordinator.uiActor.AppState().Active; active.Phase != ui.ActiveCellMutable {
					t.Fatalf("request-finished prematurely finalized active assistant: %+v", active)
				}
				coordinator.mu.Lock()
				streamingActive := coordinator.streamingActive
				coordinator.mu.Unlock()
				if !streamingActive {
					t.Fatal("request-finished prematurely reset coordinator stream before authoritative assistant.message")
				}
				bridge.EndRun()
			},
		},
		{
			name: "provider error session end",
			terminate: func(_ *ChatSession, bridge *chatRuntimeEventBridge, coordinator *chatInteractionCoordinator) {
				bridge.handleEvent(runtimeevents.Event{Type: runtimechat.EventSessionEnd, Payload: map[string]interface{}{
					"success": false, "error": "provider disconnected",
				}})
				coordinator.postTranscriptSnapshotFromBridge(bridge)
			},
		},
		{
			name: "context cancellation reaches end run without session end",
			terminate: func(_ *ChatSession, bridge *chatRuntimeEventBridge, _ *chatInteractionCoordinator) {
				bridge.setRunError(context.Canceled)
				bridge.EndRun()
			},
		},
		{
			name: "user interrupt reaches end run without session end",
			terminate: func(session *ChatSession, bridge *chatRuntimeEventBridge, _ *chatInteractionCoordinator) {
				session.interrupted.Store(true)
				bridge.EndRun()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &ChatSession{}
			bridge := newChatRuntimeEventBridge(session)
			session.RuntimeEventBridge = bridge
			coordinator := newChatInteractionCoordinator(session)
			t.Cleanup(coordinator.Shutdown)
			session.Interaction = coordinator

			surface := ui.NewFixedBottomSurface(ui.NewTerminal())
			surface.EnableForTest(48, 14)
			coordinator.SetSurface(surface)
			var output bytes.Buffer
			if !coordinator.enableUnifiedRendererWithWriter(&output) {
				t.Fatal("unified renderer did not attach")
			}

			bridge.BeginRun()
			bridge.encodeRenderModelEvent(runtimeevents.Event{Type: runtimechat.EventLLMRequestStarted, Payload: map[string]interface{}{
				"turn_id": "turn-terminal", "stream_id": "stream-terminal",
			}})
			coordinator.postTranscriptSnapshotFromBridge(bridge)
			coordinator.waitUIActorIdle()

			const partial = "partial assistant output"
			bridge.encodeRenderModelEvent(runtimeevents.Event{Type: runtimechat.EventAssistantDelta, Payload: map[string]interface{}{
				"turn_id": "turn-terminal", "stream_id": "stream-terminal", "sequence": uint64(1), "delta": partial,
			}})
			coordinator.RenderAssistantDelta(partial)
			// The runtime bridge sets this before invoking the coordinator. Keep
			// the focused test on that real terminal-event path.
			bridge.markAssistantDeltaRendered(partial)
			coordinator.postTranscriptSnapshotFromBridge(bridge)
			coordinator.waitUIActorIdle()

			if active := coordinator.uiActor.AppState().Active; active.Phase != ui.ActiveCellMutable {
				t.Fatalf("precondition active=%+v, want mutable Scene cell", active)
			}
			test.terminate(session, bridge, coordinator)
			coordinator.waitUIActorIdle()
			awaitUnifiedPresenterIdle(t, coordinator)

			state := coordinator.uiActor.AppState()
			if state.Active.Phase != ui.ActiveCellInactive {
				t.Fatalf("terminal boundary left active cell=%+v", state.Active)
			}
			if len(state.Transcript.Cells) != 1 {
				t.Fatalf("transcript cells=%d, want exactly one finalized partial cell", len(state.Transcript.Cells))
			}
			cell := state.Transcript.Cells[0]
			if cell.Source != partial || cell.Phase != scene.CellCommitted {
				t.Fatalf("terminal partial cell=%+v, want committed source=%q", cell, partial)
			}
			if strings.Count(output.String(), partial) > 2 {
				t.Fatalf("partial body was replayed through a duplicate writer: %q", output.String())
			}
			if history := surface.HistoryWindowForTest(); len(history) != 0 {
				t.Fatalf("terminal boundary populated legacy historyWindow: %#v", history)
			}
			if activeBand := surface.ActiveBandLines(); len(activeBand) != 0 {
				t.Fatalf("terminal boundary repopulated legacy active band: %#v", activeBand)
			}
		})
	}
}

func fixedSurfaceRowsContain(rows [][]vt.Cell, text string) bool {
	var builder strings.Builder
	for _, row := range rows {
		for _, cell := range row {
			builder.WriteString(cell.Text)
		}
		builder.WriteByte('\n')
	}
	return strings.Contains(builder.String(), text)
}

func TestChatRuntimeEventBridge_OrdinaryEventUsesUIActorReducer(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	bridge.handleQueuedEvent(chatRuntimeQueuedEvent{
		event: runtimeevents.Event{Type: "planning.completed"},
		epoch: bridge.runEpoch,
	})
	coordinator.waitUIActorIdle()

	if got := output.String(); got == "" {
		t.Fatal("ordinary runtime event did not render through coordinator")
	}
	if bridge.processedEvents != 0 {
		t.Fatal("handleQueuedEvent must not update bridge progress counters")
	}
	if coordinator.uiActor == nil {
		t.Fatal("ordinary runtime event did not initialize UI actor")
	}
	stats := coordinator.uiActor.Stats()
	if stats.Processed < 2 || stats.LastAction != "ReplaceTranscript" {
		t.Fatalf("runtime event and its transcript follow-up were not reduced by UI actor: %+v", stats)
	}
	bridgeSnapshot := bridge.sceneSnapshot()
	state := coordinator.uiActor.State()
	if bridgeSnapshot == nil || state.Transcript.Revision != bridgeSnapshot.Revision || len(state.Transcript.Cells) != len(bridgeSnapshot.Cells) {
		t.Fatalf("AppState transcript is not the runtime Scene snapshot: state=%+v scene=%+v", state.Transcript, bridgeSnapshot)
	}
}

// Non-runtime transcript producers still use their legacy coordinator writer
// in Phase 2, but their semantic Scene snapshots must cross the same actor
// boundary as runtime events. This prevents AppState from silently lagging
// behind submitted user input, local command results or local errors.
func TestChatInteractionCoordinator_NonRuntimeSceneProducersProjectTranscript(t *testing.T) {
	session := &ChatSession{}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	coordinator.RenderSubmittedUserInput("question")
	if ok := coordinator.RenderCommandDocument(render.Document{Blocks: []render.Block{{
		Lines: []render.Line{{Spans: []render.Span{{Text: "command result"}}}},
	}}}); !ok {
		t.Fatal("RenderCommandDocument returned false")
	}
	coordinator.RenderError(errors.New("local failure"))
	coordinator.waitUIActorIdle()

	if coordinator.uiActor == nil {
		t.Fatal("non-runtime producers did not initialize UI actor")
	}
	bridgeSnapshot := bridge.sceneSnapshot()
	state := coordinator.uiActor.AppState()
	if bridgeSnapshot == nil || state.Transcript.Revision != bridgeSnapshot.Revision || len(state.Transcript.Cells) != len(bridgeSnapshot.Cells) {
		t.Fatalf("AppState transcript is not the non-runtime Scene snapshot: state=%+v scene=%+v", state.Transcript, bridgeSnapshot)
	}
	if len(state.Transcript.Cells) != 3 {
		t.Fatalf("transcript cells = %d, want user + command + error", len(state.Transcript.Cells))
	}
	if stats := coordinator.uiActor.Stats(); stats.Processed < 3 || stats.LastAction != "ReplaceTranscript" {
		t.Fatalf("non-runtime snapshots were not reduced as transcript actions: %+v", stats)
	}
}

// Event-log replay reconstructs Scene without a live RuntimeEvent action.
// It still has to advance the AppState mirror, otherwise a later pure Layout
// would compose stale pre-replay cells while /debug reports the rebuilt Scene.
func TestChatRuntimeEventBridge_ReplayProjectsTranscriptToUIActor(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "runtime-events.jsonl")
	producer := newChatRuntimeEventBridge(&ChatSession{})
	producer.eventLogPathOverride = logPath
	for _, event := range testRuntimeSceneEvents() {
		producer.encodeRenderModelEvent(event)
	}

	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	bridge := newChatRuntimeEventBridge(session)
	bridge.eventLogPathOverride = logPath

	if replayed, err := bridge.replayEventLog(); err != nil || replayed == 0 {
		t.Fatalf("replayEventLog = %d, %v", replayed, err)
	}
	coordinator.waitUIActorIdle()
	if coordinator.uiActor == nil {
		t.Fatal("replay did not initialize UI actor")
	}

	snapshot := bridge.sceneSnapshot()
	state := coordinator.uiActor.AppState()
	if snapshot == nil || state.Transcript.Revision != snapshot.Revision || len(state.Transcript.Cells) != len(snapshot.Cells) {
		t.Fatalf("AppState transcript is not the replayed Scene snapshot: state=%+v scene=%+v", state.Transcript, snapshot)
	}
	if stats := coordinator.uiActor.Stats(); stats.LastAction != "ReplaceTranscript" {
		t.Fatalf("replay transcript was not reduced as ReplaceTranscript: %+v", stats)
	}
}

func TestChatInteractionCoordinatorRefreshActiveStreamViewportUsesResizeBarrier(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	if !coordinator.postUIAction(ui.DrawRequested{Key: "test-init"}) {
		t.Fatal("failed to initialize UI actor")
	}
	coordinator.waitUIActorIdle()

	before := coordinator.uiActor.Revision()
	coordinator.RefreshActiveStreamViewport()
	stats := coordinator.uiActor.Stats()
	if stats.Revision != before+1 || stats.LastAction != "Resize" {
		t.Fatalf("refresh did not apply exactly one Resize barrier: before=%d stats=%+v", before, stats)
	}
}

func TestChatInteractionCoordinatorRefreshReportsMeasuredGeometryToAppState(t *testing.T) {
	_ = captureSurfaceStdout(t, func() {
		session := &ChatSession{}
		coordinator := newChatInteractionCoordinator(session)
		t.Cleanup(coordinator.Shutdown)
		session.Interaction = coordinator

		surface := ui.NewFixedBottomSurface(ui.NewTerminal())
		surface.EnableForTest(91, 31)
		coordinator.SetSurface(surface)
		coordinator.waitUIActorIdle()

		before := coordinator.uiActor.Revision()
		coordinator.RefreshActiveStreamViewport()
		state := coordinator.uiActor.AppState()
		if state.Geometry.Width != 91 || state.Geometry.Height != 31 || state.Geometry.Generation == 0 || state.LayoutGeneration != state.Geometry.Generation {
			t.Fatalf("measured geometry was not published to AppState: %+v", state)
		}
		if stats := coordinator.uiActor.Stats(); stats.Revision != before+2 || stats.LastAction != "Resize" {
			t.Fatalf("probe + measured resize barriers = %+v, before=%d", stats, before)
		}
	})
}

func TestChatInteractionCoordinatorPromptInputUsesSequencedInputAction(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	coordinator.SetPromptInputSnapshot(ui.LineEditorSnapshot{
		Text:        "draft input",
		Cursor:      5,
		PasteActive: true,
	})
	coordinator.waitUIActorIdle()
	snapshot := coordinator.PromptInputSnapshot()
	if snapshot.Text != "draft input" || snapshot.Cursor != 5 || !snapshot.PasteActive {
		t.Fatalf("prompt snapshot was not applied by reducer: %+v", snapshot)
	}
	stats := coordinator.uiActor.Stats()
	if stats.Processed != 1 || stats.LastAction != "InputEvent" {
		t.Fatalf("prompt input did not use one sequenced InputEvent: %+v", stats)
	}
}

func TestChatInteractionCoordinatorPromptInputNeverWaitsForActorDrain(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	blocker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocker) }) }
	entered := make(chan struct{})
	first := true
	actor := ui.NewUIController(ui.UIControllerConfig{}, ui.ReducerFunc(func(uint64, ui.UIAction) []ui.Effect {
		if first {
			first = false
			close(entered)
			<-blocker
		}
		return nil
	}), nil)
	coordinator.uiActorOnce.Do(func() { coordinator.uiActor = actor })
	go actor.Run()
	t.Cleanup(func() {
		release()
		coordinator.Shutdown()
	})

	if !coordinator.postUIAction(ui.Timer{Key: "test.block"}) {
		t.Fatal("failed to post blocking actor action")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter blocking reducer")
	}

	returned := make(chan struct{})
	go func() {
		coordinator.SetPromptInputSnapshot(ui.LineEditorSnapshot{Text: "responsive", Cursor: 10})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("prompt input waited for an unrelated actor action")
	}
}

func TestChatInteractionCoordinatorPromptInputNeverWaitsForCoordinatorRenderLock(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coordinator.Shutdown)

	coordinator.mu.Lock()
	locked := true
	defer func() {
		if locked {
			coordinator.mu.Unlock()
		}
	}()

	returned := make(chan struct{})
	go func() {
		coordinator.SetPromptInputSnapshot(ui.LineEditorSnapshot{Text: "render lock responsive", Cursor: 22})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("prompt input waited for coordinator render lock")
	}
	if snapshot := coordinator.PromptInputSnapshot(); snapshot.Text != "render lock responsive" || snapshot.Cursor != 22 {
		t.Fatalf("editor cache did not publish under coordinator render lock: %+v", snapshot)
	}

	coordinator.mu.Unlock()
	locked = false
	coordinator.waitUIActorIdle()
	if snapshot := coordinator.PromptInputSnapshot(); snapshot.Text != "render lock responsive" || snapshot.Cursor != 22 {
		t.Fatalf("prompt draft was not retained after render lock release: %+v", snapshot)
	}
}

func TestChatInteractionCoordinatorPromptInputNeverWaitsForFullMailbox(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	blocker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocker) }) }
	entered := make(chan struct{})
	first := true
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, ui.ReducerFunc(func(revision uint64, action ui.UIAction) []ui.Effect {
		if first {
			first = false
			close(entered)
			<-blocker
		}
		return coordinator.reduceUIAction(revision, action)
	}), nil)
	coordinator.uiActorOnce.Do(func() { coordinator.uiActor = actor })
	go actor.Run()
	t.Cleanup(func() {
		release()
		coordinator.Shutdown()
	})

	if !coordinator.postUIAction(ui.Timer{Key: "test.block"}) {
		t.Fatal("failed to post blocking actor action")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter blocking reducer")
	}
	if !coordinator.postUIAction(ui.LeaseAcquired{LeaseID: 1}) {
		t.Fatal("failed to fill actor mailbox")
	}

	returned := make(chan struct{})
	go func() {
		coordinator.SetPromptInputSnapshot(ui.LineEditorSnapshot{Text: "mailbox responsive", Cursor: 18})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("prompt input waited for a full actor mailbox")
	}

	release()
	deadline := time.Now().Add(time.Second)
	for actor.Stats().Processed < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := actor.Stats(); stats.Processed < 3 {
		t.Fatalf("deferred prompt snapshot was not eventually reduced: %+v", stats)
	}
}

func TestChatInteractionCoordinatorPostScheduledUIActionNeverWaitsForFullMailbox(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	blocker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocker) }) }
	entered := make(chan struct{})
	first := true
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, ui.ReducerFunc(func(revision uint64, action ui.UIAction) []ui.Effect {
		if first {
			first = false
			close(entered)
			<-blocker
		}
		return coordinator.reduceUIAction(revision, action)
	}), nil)
	coordinator.uiActorOnce.Do(func() { coordinator.uiActor = actor })
	go actor.Run()
	t.Cleanup(func() {
		release()
		coordinator.Shutdown()
	})

	if !coordinator.postUIAction(ui.Timer{Key: "test.scheduled-block"}) {
		t.Fatal("failed to post blocking actor action")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter blocking reducer")
	}
	if !coordinator.postUIAction(ui.LeaseAcquired{LeaseID: 1}) {
		t.Fatal("failed to fill actor mailbox")
	}

	returned := make(chan struct{})
	go func() {
		coordinator.postScheduledUIAction(ui.Timer{Key: "scheduled.a", Generation: 1})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("scheduled UI action waited for a full actor mailbox")
	}

	release()
	deadline := time.Now().Add(time.Second)
	for actor.Stats().Processed < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := actor.Stats(); stats.Processed < 3 || stats.DeferredPosted != 1 || stats.CapacityOverflow != 1 {
		t.Fatalf("scheduled action was not tracked as deferred: %+v", stats)
	}
}

func TestChatInteractionCoordinatorPromptEditorStatusNeverWaitsForFullMailbox(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	blocker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocker) }) }
	entered := make(chan struct{})
	first := true
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, ui.ReducerFunc(func(uint64, ui.UIAction) []ui.Effect {
		if first {
			first = false
			close(entered)
			<-blocker
		}
		return nil
	}), nil)
	coordinator.uiActorOnce.Do(func() { coordinator.uiActor = actor })
	go actor.Run()
	t.Cleanup(func() {
		release()
		coordinator.Shutdown()
	})

	if !coordinator.postUIAction(ui.Timer{Key: "test.status-block"}) {
		t.Fatal("failed to post blocking actor action")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter blocking reducer")
	}
	if !coordinator.postUIAction(ui.LeaseAcquired{LeaseID: 1}) {
		t.Fatal("failed to fill actor mailbox")
	}

	returned := make(chan bool, 1)
	go func() {
		returned <- coordinator.postSurfaceFacadeAction(ui.SetPromptEditorStatusAction{Line: "多行 2/3"})
	}()
	select {
	case ok := <-returned:
		if !ok {
			t.Fatal("editor status action was rejected")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("editor status waited for a full actor mailbox")
	}

	release()
	deadline := time.Now().Add(time.Second)
	for actor.Stats().Processed < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := actor.Stats(); stats.Processed < 3 {
		t.Fatalf("deferred editor status was not eventually reduced: %+v", stats)
	}
}

func TestChatInteractionCoordinatorPromptResetRejectsQueuedSnapshot(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	blocker := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocker) }) }
	entered := make(chan struct{})
	first := true
	actor := ui.NewUIController(ui.UIControllerConfig{}, ui.ReducerFunc(func(revision uint64, action ui.UIAction) []ui.Effect {
		if first {
			first = false
			close(entered)
			<-blocker
		}
		return coordinator.reduceUIAction(revision, action)
	}), nil)
	coordinator.uiActorOnce.Do(func() { coordinator.uiActor = actor })
	go actor.Run()
	t.Cleanup(func() {
		release()
		coordinator.Shutdown()
	})

	if !coordinator.postUIAction(ui.Timer{Key: "test.block"}) {
		t.Fatal("failed to post blocking actor action")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter blocking reducer")
	}
	coordinator.SetPromptInputSnapshot(ui.LineEditorSnapshot{Text: "stale draft", Cursor: 11})
	coordinator.ResetPromptState()

	release()
	coordinator.waitUIActorIdle()
	if snapshot := coordinator.PromptInputSnapshot(); snapshot.Text != "" || snapshot.Cursor != 0 || snapshot.PasteActive {
		t.Fatalf("queued pre-reset input resurrected the draft: %+v", snapshot)
	}
}

func TestChatInteractionCoordinatorScreenLeaseUsesBarrierActions(t *testing.T) {
	_ = captureSurfaceStdout(t, func() {
		session := &ChatSession{}
		coordinator := newChatInteractionCoordinator(session)
		t.Cleanup(coordinator.Shutdown)
		session.Interaction = coordinator

		surface := ui.NewFixedBottomSurface(ui.NewTerminal())
		surface.EnableForTest(80, 24)
		coordinator.SetSurface(surface)
		coordinator.waitUIActorIdle()

		lease, err := surface.AcquireAlternateScreen(context.Background(), ui.FullscreenRequest{Title: "test"})
		if err != nil {
			t.Fatalf("AcquireAlternateScreen: %v", err)
		}
		coordinator.waitUIActorIdle()

		state := coordinator.uiActor.State()
		if !state.Lease.Active || state.Lease.ID != lease.ID() {
			t.Fatalf("lease acquire did not cross UI actor barrier: state=%+v want=%d", state.Lease, lease.ID())
		}
		if stats := coordinator.uiActor.Stats(); stats.LastAction != "LeaseAcquired" {
			t.Fatalf("lease acquire was not reduced as a barrier: %+v", stats)
		}

		if err := lease.Release(context.Background()); err != nil {
			t.Fatalf("Release: %v", err)
		}
		coordinator.waitUIActorIdle()

		state = coordinator.uiActor.State()
		if state.Lease.Active || state.Lease.ID != 0 {
			t.Fatalf("lease release did not cross UI actor barrier: state=%+v", state.Lease)
		}
		if stats := coordinator.uiActor.Stats(); stats.LastAction != "LeaseReleased" {
			t.Fatalf("lease release was not reduced as a barrier: %+v", stats)
		}
	})
}

func TestChatInteractionCoordinatorEffectResultUsesBarrierAction(t *testing.T) {
	coordinator := newChatInteractionCoordinator(&ChatSession{})
	t.Cleanup(coordinator.Shutdown)

	result := ui.EffectResult{Token: 41, MayHavePartiallyWritten: true}
	if !coordinator.postUIAction(result) {
		t.Fatal("failed to post EffectResult")
	}
	coordinator.waitUIActorIdle()

	state := coordinator.uiActor.State()
	if state.Effects.Count != 1 || state.Effects.Last.Token != result.Token || !state.Effects.Last.MayHavePartiallyWritten {
		t.Fatalf("effect result was not reduced: state=%+v", state.Effects)
	}
	if stats := coordinator.uiActor.Stats(); stats.LastAction != "EffectResult" {
		t.Fatalf("effect result was not reduced as a barrier: %+v", stats)
	}
}

// High-frequency ordered stream events must drain through the bounded bridge
// queue and UI actor mailbox without wedging the run. A successful
// llm.request.finished is only a transport boundary: the composer must stay
// streaming until the authoritative assistant.message/session.end and the
// sendMessage EndRun + CompleteWaiting lifecycle resolve it to Ready.
func TestChatInteractionCoordinatorUnifiedHighFrequencyStreamDrainsToReady(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "high-frequency-session"},
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coordinator.SetSurface(surface)
	var output bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&output) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	bridge.startOnce.Do(func() {})
	go bridge.run()
	t.Cleanup(func() { close(bridge.eventQueue) })

	coordinator.StartWaiting()
	bridge.BeginRun()

	sessionID := session.RuntimeSession.ID
	const turnID = "turn-high-frequency"
	const streamID = "stream-high-frequency"
	const deltaCount = 1024

	post := func(eventType string, eventPayload map[string]interface{}) {
		t.Helper()
		bridge.Handle(runtimeevents.Event{
			Type:      eventType,
			SessionID: sessionID,
			Payload:   eventPayload,
		})
	}

	post(runtimechat.EventSessionStart, map[string]interface{}{"turn_id": turnID})
	post(runtimechat.EventLLMRequestStarted, map[string]interface{}{
		"turn_id": turnID, "stream_id": streamID,
	})
	for sequence := uint64(1); sequence <= deltaCount; sequence++ {
		post(runtimechat.EventAssistantDelta, map[string]interface{}{
			"turn_id": turnID, "stream_id": streamID, "sequence": sequence, "delta": "chunk ",
		})
	}
	post(runtimechat.EventLLMRequestFinished, map[string]interface{}{
		"turn_id": turnID, "stream_id": streamID, "success": true,
	})

	bridge.WaitForCurrentEvents(5 * time.Second)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	state := coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellMutable {
		t.Fatalf("llm.request.finished prematurely finalized active cell: %+v", state.Active)
	}
	coordinator.mu.Lock()
	streamingActive := coordinator.streamingActive
	coordinator.mu.Unlock()
	if !streamingActive {
		t.Fatal("llm.request.finished prematurely reset the stream before assistant.message")
	}
	if coordinator.AgentStage() != chatAgentStagePlanning {
		t.Fatalf("agent stage after llm.request.finished = %q, want planning", coordinator.AgentStage())
	}
	if coordinator.IsReady() {
		t.Fatal("llm.request.finished made the composer ready before EndRun")
	}

	const finalAnswer = "authoritative final answer"
	post(runtimechat.EventAssistantMessage, map[string]interface{}{
		"turn_id": turnID, "stream_id": streamID, "content": finalAnswer,
	})
	post(runtimechat.EventSessionEnd, map[string]interface{}{
		"turn_id": turnID, "success": true,
	})
	bridge.EndRun()
	coordinator.CompleteWaiting()

	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	if coordinator.AgentStage() != chatAgentStageIdle {
		t.Fatalf("agent stage after EndRun = %q, want idle", coordinator.AgentStage())
	}
	if !coordinator.IsReady() {
		t.Fatal("composer did not return to Ready after EndRun + CompleteWaiting")
	}
	state = coordinator.uiActor.AppState()
	if state.Active.Phase != ui.ActiveCellInactive {
		t.Fatalf("run left active cell %+v, want inactive", state.Active)
	}
	bridge.progressMu.Lock()
	enqueued := bridge.enqueuedEvents
	processed := bridge.processedEvents
	bridge.progressMu.Unlock()
	if processed < enqueued {
		t.Fatalf("bridge worker did not drain its queue: processed=%d enqueued=%d", processed, enqueued)
	}
	if stats := coordinator.uiActor.Stats(); stats.Pending != 0 {
		t.Fatalf("UI actor still has %d pending actions", stats.Pending)
	}
	if !strings.Contains(output.String(), finalAnswer) {
		t.Fatalf("TerminalSession output missing final answer: %q", output.String())
	}
}
