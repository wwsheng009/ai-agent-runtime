package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// A stale canonical tail (session.Messages still ending in the previous user
// prompt because the assistant was not synced into the CLI) must not suppress
// the next turn's user echo when the Scene already shows the assistant reply.
// The production presenter omits finalized transcript rows from the mutable
// viewport, so a suppressed user cell leaves no prompt on the terminal at all.
func TestSamePromptAfterAssistantEchoesOnProductionPresenter(t *testing.T) {
	const marker = "SAME-PROMPT-AFTER-ASSISTANT-ECHO"
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
	coordinator.RenderSubmittedUserInput(marker)
	feedFinalOnlyAssistantTurn(t, bridge, currentRuntimeSessionID(session), "same-prompt-first", "first reply")
	bridge.EndRun()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	// Simulate an executor whose assistant output reached the Scene but was not
	// synced back into ChatSession.Messages: canonical still shows the pending
	// user message, while the render plane already moved past it.
	session.Messages = []runtimetypes.Message{*runtimetypes.NewUserMessage(marker)}

	bridge.BeginRun()
	coordinator.RenderSubmittedUserInput(marker)
	feedFinalOnlyAssistantTurn(t, bridge, currentRuntimeSessionID(session), "same-prompt-second", "second reply")
	bridge.EndRun()
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	screen := vt.NewScreen(80, 24)
	screen.Feed(collectMemorySinkBytes(sink))
	projected := strings.Join(append(screen.ScrollbackLines(), screen.Lines(0, 24)...), "\n")
	if count := strings.Count(projected, "> "+marker); count != 2 {
		t.Fatalf("same prompt rendered %d times, want 2\n%s", count, projected)
	}
}
