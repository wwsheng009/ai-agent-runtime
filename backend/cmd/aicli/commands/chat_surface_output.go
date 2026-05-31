package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type chatPromptOverlay struct {
	session *ChatSession
}

func newChatPromptOverlay(session *ChatSession) chatPromptOverlay {
	return chatPromptOverlay{session: session}
}

func (o chatPromptOverlay) surfaceEnabled() bool {
	return o.session != nil && o.session.Surface != nil && o.session.Surface.Enabled()
}

func (o chatPromptOverlay) resetPromptState() {
	if o.session != nil && o.session.Interaction != nil {
		o.session.Interaction.ResetPromptState()
	}
}

func (o chatPromptOverlay) beginDirectOutput() {
	if o.session == nil || o.session.NoInteractive || o.session.JSONOutput {
		return
	}
	if o.session.Interaction != nil {
		o.session.Interaction.ClearPrompt()
	}
	if o.session.Surface != nil {
		o.session.Surface.BeginOutput()
	}
}

func (o chatPromptOverlay) showComposerPreview(prompt string) bool {
	if !o.surfaceEnabled() {
		return false
	}
	o.beginDirectOutput()
	o.session.Surface.SetComposerPreview(prompt)
	o.resetPromptState()
	return true
}

func (o chatPromptOverlay) clearComposerPreview() {
	if o.session != nil && o.session.Surface != nil {
		o.session.Surface.ClearComposerPreview()
	}
	o.resetPromptState()
}

func (o chatPromptOverlay) showSelectionPopup(lines []string, prompt string) bool {
	if !o.surfaceEnabled() {
		return false
	}
	o.beginDirectOutput()
	o.session.Surface.ShowPopupInput(lines, prompt)
	return true
}

func (o chatPromptOverlay) renderModalPopupInput(lines []string, prompt string, preserveCursor bool) bool {
	if !o.surfaceEnabled() {
		return false
	}
	if preserveCursor {
		o.session.Surface.ShowPopupInputPreserveCursor(lines, prompt)
		return true
	}
	o.session.Surface.ShowPopupInput(lines, prompt)
	return true
}

func (o chatPromptOverlay) clearSelectionPopup() {
	if o.session != nil && o.session.Surface != nil {
		o.session.Surface.ClearPopupPreserveCursor()
	}
	o.resetPromptState()
}

func (o chatPromptOverlay) showOwnedPopupBelowPrompt(lines []string, owner string) bool {
	if !o.surfaceEnabled() {
		return false
	}
	o.session.Surface.ShowPopupPreserveCursorForOwnerBelowPrompt(lines, owner)
	return true
}

func (o chatPromptOverlay) clearOwnedPopup(owner string) {
	if o.session != nil && o.session.Surface != nil {
		o.session.Surface.ClearPopupForOwnerPreserveCursor(owner)
	}
}

func (o chatPromptOverlay) showPendingPasteDraft(lines int, text string) bool {
	if !o.surfaceEnabled() {
		return false
	}
	if lines < 1 {
		lines = 1
	}
	o.session.Surface.ShowPendingPastePreview(lines, text)
	return true
}

func (o chatPromptOverlay) clearPendingPasteDraft() bool {
	if !o.surfaceEnabled() {
		return false
	}
	o.session.Surface.ClearPendingPastePreview()
	return true
}

func (o chatPromptOverlay) showPriorityPrompt(lines []string, prompt string) (string, func(), bool) {
	prompt = sanitizeInteractivePromptLine(prompt)
	if o.surfaceEnabled() {
		o.beginDirectOutput()
		o.session.Surface.ShowPopupInput(lines, prompt)
		return prompt, func() { o.clearSelectionPopup() }, true
	}

	o.beginDirectOutput()
	fmt.Println()
	for _, line := range lines {
		if rendered := formatInteractiveSupplementPromptLine(line); rendered != "" {
			fmt.Println(rendered)
		}
	}
	renderedPrompt := formatInteractiveSupplementPromptLine(prompt)
	if renderedPrompt == "" {
		renderedPrompt = prompt
	}
	fmt.Print(renderedPrompt)
	return renderedPrompt, func() {}, false
}

func sanitizeInteractivePromptLine(line string) string {
	return strings.TrimRight(strings.ReplaceAll(line, "\r\n", "\n"), "\n")
}

func formatInteractiveSupplementPromptLine(line string) string {
	line = sanitizeInteractivePromptLine(line)
	if strings.TrimSpace(line) == "" {
		return ""
	}
	return ui.FormatAssistantSupplementBlock(line)
}

func beginDirectInteractiveOutput(session *ChatSession) {
	newChatPromptOverlay(session).beginDirectOutput()
}

func showRuntimeComposerPrompt(session *ChatSession, prompt string) bool {
	return newChatPromptOverlay(session).showComposerPreview(prompt)
}

func clearRuntimeComposerPrompt(session *ChatSession) {
	newChatPromptOverlay(session).clearComposerPreview()
}

// notifyChatInputDraftState displays pending paste drafts through the overlay;
// without a surface it falls back to the status line only.
func notifyChatInputDraftState(session *ChatSession, active bool, lines int, text string) {
	if session == nil {
		return
	}
	overlay := newChatPromptOverlay(session)
	if active {
		if lines < 1 {
			lines = 1
		}
		if overlay.showPendingPasteDraft(lines, text) {
			return
		}
		if session.Interaction != nil {
			session.Interaction.RefreshStatus(fmt.Sprintf("Paste draft %d lines", lines))
		}
		return
	}
	if overlay.clearPendingPasteDraft() {
		return
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func renderSubmittedUserInputEcho(session *ChatSession, input string) {
	if session == nil || session.NoInteractive || session.JSONOutput || strings.TrimSpace(input) == "" {
		return
	}
	if session.Surface == nil || !session.Surface.Enabled() || session.Interaction == nil {
		return
	}
	session.Interaction.RenderSubmittedUserInput(input)
}

func writeChatLogBufferedMarker(session *ChatSession) {
	if shouldRenderInteractiveOutput(session) && session.Surface != nil && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
		return
	}
	fmt.Fprint(os.Stderr, "💾")
}

func writeChatLogSaveError(session *ChatSession, err error) {
	if err == nil {
		return
	}
	message := fmt.Sprintf("[日志保存失败] %v", err)
	if shouldRenderInteractiveOutput(session) && session.Interaction != nil {
		session.Interaction.RenderAsyncLine(message)
		return
	}
	fmt.Fprint(os.Stderr, message)
}
