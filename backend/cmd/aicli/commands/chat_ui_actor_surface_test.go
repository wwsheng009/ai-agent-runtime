package commands

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

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

func TestChatInteractionCoordinatorPromptInputUsesDurableInputAction(t *testing.T) {
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	coordinator.SetPromptInputSnapshot(ui.LineEditorSnapshot{
		Text:        "draft input",
		Cursor:      5,
		PasteActive: true,
	})
	snapshot := coordinator.PromptInputSnapshot()
	if snapshot.Text != "draft input" || snapshot.Cursor != 5 || !snapshot.PasteActive {
		t.Fatalf("prompt snapshot was not applied by reducer: %+v", snapshot)
	}
	stats := coordinator.uiActor.Stats()
	if stats.Processed != 1 || stats.LastAction != "InputEvent" {
		t.Fatalf("prompt input did not use one durable InputEvent: %+v", stats)
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
