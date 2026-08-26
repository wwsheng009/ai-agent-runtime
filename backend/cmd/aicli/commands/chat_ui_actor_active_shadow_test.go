package commands

import (
	"bytes"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRenderAssistantDelta_ShadowUsesCausalFollowup(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, ui.ReducerFunc(func(_ uint64, action ui.UIAction) []ui.Effect {
		event, ok := action.(ui.RuntimeEvent)
		if !ok || event.Kind != "shadow-parent" {
			return nil
		}
		close(started)
		<-release
		coordinator.RenderAssistantDelta("abc")
		close(completed)
		return nil
	}), nil)
	coordinator.uiActor = actor
	coordinator.uiActorOnce.Do(func() {})
	go actor.Run()
	t.Cleanup(func() {
		actor.Close()
		actor.WaitIdle()
	})

	initial := ui.ActiveCellState{
		CellID:   7,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Phase:    ui.ActiveCellMutable,
	}
	if !actor.Post(ui.SetActiveCellAction{Active: initial}) {
		t.Fatal("post active cell mount")
	}
	actor.WaitIdle()
	if !actor.Post(ui.RuntimeEvent{Kind: "shadow-parent"}) {
		t.Fatal("post parent runtime event")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("parent reducer did not start")
	}
	// The parent is in flight, so this fills the one-slot external mailbox.
	// A normal Post from RenderAssistantDelta would now self-deadlock.
	if !actor.Post(ui.RuntimeEvent{Kind: "external"}) {
		t.Fatal("fill external mailbox")
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("assistant shadow action blocked behind the current reducer")
	}
	actor.WaitIdle()
	state := actor.AppState().Active
	if state.CellID != 7 || state.Revision != 2 || state.Source != boundary.FormatAssistantBlockChrome("abc") {
		t.Fatalf("shadow follow-up state = %#v, want cell 7 rev 2 source %q", state, boundary.FormatAssistantBlockChrome("abc"))
	}
}

func TestToolStageShadowUsesCausalFollowup(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	coordinator.SetWriter(&bytes.Buffer{})
	coordinator.activeStream.BeginTool("shell", nil)

	started := make(chan struct{})
	release := make(chan struct{})
	completed := make(chan struct{})
	actor := ui.NewUIController(ui.UIControllerConfig{MailboxSize: 1}, ui.ReducerFunc(func(_ uint64, action ui.UIAction) []ui.Effect {
		event, ok := action.(ui.RuntimeEvent)
		if !ok || event.Kind != "tool-shadow-parent" {
			return nil
		}
		close(started)
		<-release
		coordinator.SetToolAgentStage("call-7", "shell")
		close(completed)
		return nil
	}), nil)
	coordinator.uiActor = actor
	coordinator.uiActorOnce.Do(func() {})
	go actor.Run()
	t.Cleanup(func() {
		actor.Close()
		actor.WaitIdle()
	})

	if !actor.Post(ui.SetActiveCellAction{Active: ui.ActiveCellState{
		CellID: 7, Revision: 1, Kind: scene.KindToolChain, Phase: ui.ActiveCellMutable,
	}}) {
		t.Fatal("post active tool cell")
	}
	actor.WaitIdle()
	if !actor.Post(ui.RuntimeEvent{Kind: "tool-shadow-parent"}) {
		t.Fatal("post parent runtime event")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool parent reducer did not start")
	}
	if !actor.Post(ui.RuntimeEvent{Kind: "external"}) {
		t.Fatal("fill external mailbox")
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("tool shadow action blocked behind the current reducer")
	}
	actor.WaitIdle()
	state := actor.AppState().Active
	if state.CellID != 7 || state.Revision != 2 || state.Kind != scene.KindToolChain || state.Source != "" {
		t.Fatalf("tool shadow follow-up state = %#v", state)
	}
}

func TestActiveStreamShadowActionMirrorsMountedSceneCell(t *testing.T) {
	actor := ui.NewUIController(ui.UIControllerConfig{}, nil, nil)
	go actor.Run()
	t.Cleanup(func() {
		actor.Close()
		actor.WaitIdle()
	})

	initial := ui.ActiveCellState{
		CellID:   7,
		Revision: 1,
		Kind:     scene.KindAssistant,
		Phase:    ui.ActiveCellMutable,
	}
	if !actor.Post(ui.SetActiveCellAction{Active: initial}) {
		t.Fatal("post active cell mount")
	}
	actor.WaitIdle()

	coordinator := &chatInteractionCoordinator{
		activeStream: ui.NewActiveStreamController(40, 6),
		uiActor:      actor,
	}
	coordinator.uiActorOnce.Do(func() {})
	coordinator.activeStream.BeginAssistant("assistant")
	coordinator.activeStream.PushAssistantDelta("abc", false)

	coordinator.mu.Lock()
	action := coordinator.activeStreamShadowActionLocked()
	coordinator.mu.Unlock()
	update, ok := action.(ui.UpdateActiveCellAction)
	if !ok {
		t.Fatalf("shadow action = %T, want UpdateActiveCellAction", action)
	}
	if update.ExpectedCellID != 7 || update.ExpectedRevision != 1 {
		t.Fatalf("shadow fence = %d/%d", update.ExpectedCellID, update.ExpectedRevision)
	}
	if update.Active.Revision != 2 || update.Active.Source != boundary.FormatAssistantBlockChrome("abc") || update.Active.Kind != scene.KindAssistant {
		t.Fatalf("shadow payload = %+v", update.Active)
	}
	if update.Active.Acked.End != 0 || update.Active.Enqueued.End != 0 {
		t.Fatalf("shadow guessed physical progress: %+v", update.Active)
	}

	if !actor.Post(update) {
		t.Fatal("post shadow update")
	}
	actor.WaitIdle()
	state := actor.AppState().Active
	if state.Source != boundary.FormatAssistantBlockChrome("abc") || state.Revision != 2 || state.CellID != 7 {
		t.Fatalf("AppState active after shadow update = %+v", state)
	}

	// Tool display maps only the semantic running-tool identity; its rendered
	// display remains outside ActiveCellState.Source.
	coordinator.activeStream.BeginToolDisplay("shell", nil, "running")
	coordinator.mu.Lock()
	toolAction := coordinator.activeStreamShadowActionLocked()
	coordinator.mu.Unlock()
	toolUpdate, ok := toolAction.(ui.UpdateActiveCellAction)
	if !ok {
		t.Fatalf("tool shadow action = %T, want UpdateActiveCellAction", toolAction)
	}
	if toolUpdate.Active.Kind != scene.KindToolChain || toolUpdate.Active.Source != "" {
		t.Fatalf("tool display leaked into AppState source: %+v", toolUpdate.Active)
	}
	if !actor.Post(toolUpdate) {
		t.Fatal("post tool shadow update")
	}
	actor.WaitIdle()
	coordinator.activeStream.Cancel()
	coordinator.mu.Lock()
	clearAction := coordinator.activeStreamShadowActionLocked()
	coordinator.mu.Unlock()
	if _, ok := clearAction.(ui.ClearActiveCellAction); !ok {
		t.Fatalf("inactive tool shadow action = %T, want ClearActiveCellAction", clearAction)
	}
}

func TestRenderAssistantDeltaPostsShadowActionAfterCoordinatorUnlock(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	coordinator.SetWriter(&bytes.Buffer{})

	actor := coordinator.ensureUIActor()
	if actor == nil {
		t.Fatal("expected UI actor")
	}
	if !actor.Post(ui.SetActiveCellAction{Active: ui.ActiveCellState{
		CellID:   11,
		Revision: 3,
		Kind:     scene.KindAssistant,
		Phase:    ui.ActiveCellMutable,
	}}) {
		t.Fatal("post active scene mount")
	}
	actor.WaitIdle()

	coordinator.RenderAssistantDelta("shadowed source")
	coordinator.waitUIActorIdle()
	active := actor.AppState().Active
	if active.CellID != 11 || active.Revision != 4 || active.Source != boundary.FormatAssistantBlockChrome("shadowed source") {
		t.Fatalf("AppState active after coordinator delta = %+v", active)
	}
	if active.Acked.End != 0 || active.Enqueued.End != 0 {
		t.Fatalf("coordinator guessed terminal progress: %+v", active)
	}
}

func TestCompleteAssistantResponsePostsFinalizationShadowTransaction(t *testing.T) {
	session := &ChatSession{Stream: true}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	coordinator.SetWriter(&bytes.Buffer{})
	session.Interaction = coordinator

	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	actor := coordinator.ensureUIActor()
	if actor == nil {
		t.Fatal("expected UI actor")
	}

	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:    runtimechat.EventAssistantDelta,
		Payload: map[string]interface{}{"delta": "partial"},
	})
	if snapshot := bridge.sceneSnapshot(); snapshot == nil || len(snapshot.Cells) != 1 {
		t.Fatalf("mutable Scene snapshot = %#v", snapshot)
	} else if !actor.Post(ui.ReplaceTranscriptAction{Snapshot: snapshot}) {
		t.Fatal("post mutable transcript snapshot")
	}
	actor.WaitIdle()

	coordinator.RenderAssistantDelta("partial")
	coordinator.waitUIActorIdle()
	if active := actor.AppState().Active; active.CellID == 0 || active.Revision != 3 {
		t.Fatalf("shadow active before finalization = %+v", active)
	}

	bridge.encodeRenderModelEvent(runtimeevents.Event{
		Type:    runtimechat.EventAssistantMessage,
		Payload: map[string]interface{}{"content": "final"},
	})
	if !coordinator.CompleteAssistantResponse("final") {
		t.Fatal("assistant completion was not accepted")
	}
	coordinator.waitUIActorIdle()

	state := actor.AppState()
	if state.Active != (ui.ActiveCellState{}) {
		t.Fatalf("finalization left active shadow cell mounted: %+v", state.Active)
	}
	if len(state.Transcript.Cells) != 1 || state.Transcript.Cells[0].Phase != scene.CellCommitted || state.Transcript.Cells[0].Source != "final" {
		t.Fatalf("finalization shadow transcript = %+v", state.Transcript)
	}
}

func TestRenderReasoningDeltaMirrorsAndClearsShadowActiveCell(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	coordinator.SetWriter(&bytes.Buffer{})
	actor := coordinator.ensureUIActor()
	if !actor.Post(ui.SetActiveCellAction{Active: ui.ActiveCellState{
		CellID:   21,
		Revision: 5,
		Kind:     scene.KindReasoning,
		Phase:    ui.ActiveCellMutable,
	}}) {
		t.Fatal("post reasoning active mount")
	}
	actor.WaitIdle()

	coordinator.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
		Summary:    "reasoning source",
		Visibility: runtimetypes.ReasoningVisibilitySummary,
		Streamable: true,
	})
	coordinator.waitUIActorIdle()
	active := actor.AppState().Active
	if active.CellID != 21 || active.Kind != scene.KindReasoning || active.Source != "reasoning source" {
		t.Fatalf("reasoning shadow active = %+v", active)
	}
	if active.Acked.End != 0 || active.Enqueued.End != 0 {
		t.Fatalf("reasoning shadow guessed physical progress: %+v", active)
	}

	if !coordinator.CompleteReasoningResponse(nil) {
		t.Fatal("reasoning completion was not accepted")
	}
	coordinator.waitUIActorIdle()
	if active = actor.AppState().Active; active != (ui.ActiveCellState{}) {
		t.Fatalf("reasoning completion did not clear shadow active: %+v", active)
	}
}

func TestRuntimeDeltaSnapshotDoesNotEraseShadowStreamingLedger(t *testing.T) {
	session := &ChatSession{
		Stream:         true,
		RuntimeSession: &runtimechat.Session{ID: "lead-session"},
	}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	coordinator.SetWriter(&bytes.Buffer{})
	session.Interaction = coordinator

	bridge := newChatRuntimeEventBridge(session)
	bridge.BeginRun()
	post := func(event runtimeevents.Event) {
		t.Helper()
		if !bridge.postRuntimeEventToUIActor(event) {
			t.Fatalf("runtime event was not accepted: %s", event.Type)
		}
		coordinator.waitUIActorIdle()
	}
	post(runtimeevents.Event{
		Type:      runtimechat.EventSessionStart,
		SessionID: "lead-session",
		Payload:   map[string]interface{}{"turn_id": "turn-1"},
	})
	post(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "sequence": uint64(1),
			"mode": "append", "delta": "hello",
		},
	})
	state := coordinator.uiActor.AppState()
	if state.Active.Source != "hello" || state.Active.Phase != ui.ActiveCellMutable {
		t.Fatalf("first delta active state = %+v", state.Active)
	}

	post(runtimeevents.Event{
		Type:      runtimechat.EventAssistantDelta,
		SessionID: "lead-session",
		Payload: map[string]interface{}{
			"turn_id": "turn-1", "stream_id": "stream-1", "sequence": uint64(2),
			"mode": "append", "delta": " world",
		},
	})
	state = coordinator.uiActor.AppState()
	if state.Active.Source != "hello world" || state.Active.Revision == 0 {
		t.Fatalf("second delta active state = %+v", state.Active)
	}
	if state.Active.Stable.End != len("hello world") {
		t.Fatalf("runtime snapshot erased shadow stable range: %+v", state.Active)
	}
}
