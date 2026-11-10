package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// TestNewSessionPromptEchoAfterFinalOnlyTurn guards the production presenter
// path: finalized transcript rows are omitted from the mutable viewport and
// delivered only through the HistoryCommit ledger. /new rebuilds the Scene, so
// the fresh session reuses cell IDs/revisions; without a new delivery epoch,
// the previous session's delivered record makes the /new confirmation and the
// first prompt look like duplicates and neither reaches the terminal.
//
// The old turn intentionally carries only assistant.message (no delta), so its
// assistant cell keeps revision 1 and collides with the new session's first
// prompt after /new.
func TestNewSessionPromptEchoAfterFinalOnlyTurn(t *testing.T) {
	const marker = "NEW-SESSION-PROMPT-ECHO-MARKER"
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	t.Cleanup(manager.Stop)

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  userID,
		SessionDir:     t.TempDir(),
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	coordinator.SetSurface(surface)
	sink := attachProductionPresenterForTest(t, coordinator)
	bridge.startProcessor()
	defer close(bridge.eventQueue)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	bridge.BeginRun()
	coordinator.RenderSubmittedUserInput("OLD-FINAL-ONLY-PROMPT")
	feedFinalOnlyAssistantTurn(t, bridge, currentRuntimeSessionID(session), "old-final-only", "old final-only reply")
	bridge.EndRun()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// A real terminal resize advances the layout generation. The stale delivered
	// record then satisfies the width-independent "already delivered" proof for
	// the reused identity unless /new opens a new delivery epoch.
	if !coordinator.postUIAction(ui.Resize{Width: 80, Height: 23}) {
		t.Fatal("resize was not accepted")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	if dispatchChatCommand(session, "/new", false) {
		t.Fatal("/new unexpectedly requested chat exit")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	bridge.BeginRun()
	coordinator.RenderSubmittedUserInput(marker)
	feedFinalOnlyAssistantTurn(t, bridge, currentRuntimeSessionID(session), "new-final-only", "new final-only reply")
	bridge.EndRun()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	screen := vt.NewScreen(80, 24)
	screen.Feed(collectMemorySinkBytes(sink))
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(0, 24)...), "\n")
	if count := strings.Count(projected, marker); count != 1 {
		t.Fatalf("new session prompt appears %d times in terminal projection, want 1\n%s", count, projected)
	}
	if !strings.Contains(projected, "已创建新会话") {
		t.Fatalf("/new confirmation missing from terminal projection\n%s", projected)
	}
}

func attachProductionPresenterForTest(t *testing.T, coordinator *chatInteractionCoordinator) *outputpkg.MemorySink {
	t.Helper()
	sink := outputpkg.NewMemorySink(outputpkg.TargetDescriptor{
		SinkID:             "test-physical",
		Class:              outputpkg.TargetClassPhysical,
		ProjectionTargetID: "pt-test",
	})
	gw, err := outputpkg.NewRenderOutputGateway("test-presenter", outputpkg.RenderGatewayOptions{
		Clock:                 outputpkg.SystemClock{},
		CloseTimeout:          3 * time.Second,
		ReconfigureTimeout:    5 * time.Second,
		MaxIntentBytes:        1 << 20,
		MirrorQueueCapacity:   64,
		DeliveryJournalLimit:  outputpkg.JournalLimit{MaxItems: 512, MaxBytes: 4 << 20},
		EventJournalLimit:     outputpkg.JournalLimit{MaxItems: 1024, MaxBytes: 8 << 20},
		MaxSubscriptions:      16,
		MaxSubscriptionBuffer: 128,
	}, outputpkg.RenderRouteConfig{
		Primary:            sink,
		PrimaryOwnership:   outputpkg.SinkOwned,
		ProjectionTargetID: "pt-test",
	})
	if err != nil {
		t.Fatalf("NewRenderOutputGateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	if !coordinator.enableUnifiedRendererWithPort(gw) {
		t.Fatal("production presenter did not attach")
	}
	return sink
}

func collectMemorySinkBytes(sink *outputpkg.MemorySink) string {
	var b bytes.Buffer
	for _, batch := range sink.SnapshotBatches() {
		b.Write(batch.Bytes)
	}
	return b.String()
}

func feedFinalOnlyAssistantTurn(t *testing.T, bridge *chatRuntimeEventBridge, sessionID, turnID, content string) {
	t.Helper()
	for _, event := range []runtimeevents.Event{
		{Type: runtimechat.EventSessionStart, SessionID: sessionID, Payload: map[string]interface{}{"turn_id": turnID}},
		{Type: runtimechat.EventAssistantMessage, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "stream_id": turnID + "-stream", "content": content,
		}},
		{Type: runtimechat.EventSessionEnd, SessionID: sessionID, Payload: map[string]interface{}{
			"turn_id": turnID, "success": true,
		}},
	} {
		bridge.Handle(event)
	}
	bridge.WaitForCurrentEvents(5 * time.Second)
}
