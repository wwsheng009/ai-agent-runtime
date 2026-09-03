package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func transcriptSourcesText(cells []scene.TranscriptCell) string {
	var b strings.Builder
	for _, cell := range cells {
		b.WriteString(cell.Source)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestDispatchChatCommandNewClearsPreviousConversationFromRenderPlane guards
// the /new contract: creating a fresh session must not leave the previous
// conversation's cells in the unified render data plane (uiActor semantic
// transcript — the first data source of the micro web client screen snapshot),
// and the "已创建新会话" confirmation block must be visible afterwards.
func TestDispatchChatCommandNewClearsPreviousConversationFromRenderPlane(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	t.Cleanup(manager.Stop)

	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  userID,
	}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	coordinator.SetSurface(surface)
	var terminal bytes.Buffer
	if !coordinator.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	const oldUser = "OLD-SESSION-USER-MARKER-42"
	coordinator.RenderSubmittedUserInput(oldUser)
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	before := transcriptSourcesText(coordinator.uiActor.AppState().Transcript.Cells)
	if !strings.Contains(before, oldUser) {
		t.Fatalf("precondition failed: old message missing from transcript:\n%s", before)
	}

	if dispatchChatCommand(session, "/new", false) {
		t.Fatal("/new unexpectedly requested chat exit")
	}
	coordinator.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, coordinator)

	after := transcriptSourcesText(coordinator.uiActor.AppState().Transcript.Cells)
	if strings.Contains(after, oldUser) {
		t.Fatalf("/new did not clear previous conversation from the render plane:\n%s", after)
	}
	if !strings.Contains(after, "已创建新会话") {
		t.Fatalf("/new confirmation missing from transcript:\n%s", after)
	}
	if strings.TrimSpace(after) == "" {
		t.Fatalf("/new left the render plane empty")
	}
}