package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Canonical session.Messages may lag the render plane. When the Scene already
// contains an assistant reply after the trailing user message, the same text is
// a new turn, not a retry: the user prompt must be rendered again. Suppressing
// it leaves the production presenter with no prompt at all because finalized
// transcript rows are omitted from the viewport and delivered only by history
// commits.
func TestChatTurnUserPromptRetryRequiresTrailingSceneUserCell(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{
		Messages: []runtimetypes.Message{*runtimetypes.NewUserMessage("同一问题")},
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	bridge.submitUserInput("同一问题")
	bridge.submitAssistant("已经回答过")

	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	coordinator.RenderSubmittedUserInput("同一问题")

	if count := strings.Count(output.String(), ui.FormatUserMessage("同一问题")); count != 1 {
		t.Fatalf("same prompt after assistant rendered %d user blocks, want 1\n%s", count, output.String())
	}
}

// The original retry dedupe remains: if the last rendered cell is still the
// same user prompt, adding a second user cell would diverge from canonical
// history and replay as a duplicate prompt on the next resume.
func TestChatTurnUserPromptRetrySkipsWhenSceneStillShowsSameUserCell(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	session := &ChatSession{
		Messages: []runtimetypes.Message{*runtimetypes.NewUserMessage("待完成问题")},
	}
	bridge := newChatRuntimeEventBridge(session)
	session.RuntimeEventBridge = bridge
	bridge.submitUserInput("待完成问题")

	coordinator := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	var output bytes.Buffer
	coordinator.SetWriter(&output)

	coordinator.RenderSubmittedUserInput("待完成问题")

	if count := strings.Count(output.String(), ui.FormatUserMessage("待完成问题")); count != 0 {
		t.Fatalf("pending retry rendered %d user blocks, want 0\n%s", count, output.String())
	}
}
