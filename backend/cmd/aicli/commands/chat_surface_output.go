package commands

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const (
	chatComposerPreviewPopupOwner = "modal:composer"
	chatSelectionPopupOwner       = "modal:selection"
	chatModalPopupOwner           = "modal:agent_panel"
	chatPriorityPromptPopupOwner  = "modal:priority:agent_input"
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
	// Everything after this point may write with plain fmt.Print*, which the
	// surface never observes. ClearPrompt only defers its bottom-reserve shrink
	// compensation, so an outstanding deferred scroll-down would flush later —
	// after the raw bytes already landed — and scroll them away while leaving a
	// multi-row blank hole (seen as lost command output and blank lines in
	// replayed history). Settle the debt while the surface still owns the rows.
	settleInteractiveOutputLayout(o.session)
}

// beginInlineOutput is the legacy, surface-less variant: the prompt shares the
// current terminal line with the output that follows, so the draft cannot be
// repainted afterwards and is dropped instead of silently resurfacing.
func (o chatPromptOverlay) beginInlineOutput() {
	if o.session == nil || o.session.NoInteractive || o.session.JSONOutput {
		return
	}
	if o.session.Interaction != nil {
		o.session.Interaction.DiscardPrompt()
	}
	if o.session.Surface != nil {
		o.session.Surface.BeginOutput()
	}
	settleInteractiveOutputLayout(o.session)
}

func (o chatPromptOverlay) showComposerPreview(prompt string) bool {
	if !o.surfaceEnabled() {
		return false
	}
	o.beginDirectOutput()
	o.session.Surface.ShowPopupInputForOwner(nil, prompt, chatComposerPreviewPopupOwner)
	o.resetPromptState()
	return true
}

func (o chatPromptOverlay) clearComposerPreview() {
	o.clearOwnedModalPopup(chatComposerPreviewPopupOwner)
}

func (o chatPromptOverlay) showSelectionPopup(lines []string, prompt string) bool {
	if !o.surfaceEnabled() {
		return false
	}
	o.beginDirectOutput()
	o.session.Surface.ShowPopupInputForOwner(lines, prompt, chatSelectionPopupOwner)
	return true
}

func (o chatPromptOverlay) beginSelectionPopup(lines []string, prompt string) (ui.PopupHandle, bool) {
	if !o.surfaceEnabled() {
		return ui.PopupHandle{}, false
	}
	o.beginDirectOutput()
	handle := o.session.Surface.BeginPopupInputForOwner(lines, prompt, chatSelectionPopupOwner)
	return handle, handle.Valid()
}

func (o chatPromptOverlay) updatePopupInput(handle ui.PopupHandle, lines []string, prompt string, preserveCursor bool) bool {
	if !o.surfaceEnabled() || !handle.Valid() {
		return false
	}
	return o.session.Surface.UpdatePopupInputForHandle(handle, lines, prompt, preserveCursor)
}

func (o chatPromptOverlay) beginModalPopupInput(lines []string, prompt string) (ui.PopupHandle, bool) {
	if !o.surfaceEnabled() {
		return ui.PopupHandle{}, false
	}
	handle := o.session.Surface.BeginPopupInputForOwner(lines, prompt, chatModalPopupOwner)
	return handle, handle.Valid()
}

func (o chatPromptOverlay) renderModalPopupInput(lines []string, prompt string, preserveCursor bool) bool {
	if !o.surfaceEnabled() {
		return false
	}
	if preserveCursor {
		o.session.Surface.ShowPopupInputPreserveCursorForOwner(lines, prompt, chatModalPopupOwner)
		return true
	}
	o.session.Surface.ShowPopupInputForOwner(lines, prompt, chatModalPopupOwner)
	return true
}

func (o chatPromptOverlay) clearSelectionPopup() {
	o.clearOwnedModalPopup(chatSelectionPopupOwner)
}

func (o chatPromptOverlay) clearModalPopup() {
	o.clearOwnedModalPopup(chatModalPopupOwner)
}

func (o chatPromptOverlay) clearPopupHandle(handle ui.PopupHandle) {
	if o.session != nil && o.session.Surface != nil && handle.Valid() {
		o.session.Surface.ClearPopupHandlePreserveCursor(handle)
	}
	o.resetPromptState()
}

func (o chatPromptOverlay) clearOwnedModalPopup(owner string) {
	if o.session != nil && o.session.Surface != nil {
		o.session.Surface.ClearPopupForOwnerPreserveCursor(owner)
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
	locked := o.session != nil
	if locked {
		o.session.priorityPromptMu.Lock()
	}
	var (
		handle ui.PopupHandle
		once   sync.Once
	)
	cleanup := func() {
		once.Do(func() {
			if handle.Valid() {
				o.clearPopupHandle(handle)
			}
			if locked {
				o.session.priorityPromptMu.Unlock()
			}
		})
	}

	prompt = sanitizeInteractivePromptLine(prompt)
	if o.surfaceEnabled() {
		o.beginDirectOutput()
		handle = o.session.Surface.BeginPopupInputForOwnerWithViewport(
			lines,
			prompt,
			chatPriorityPromptPopupOwner,
			priorityPromptViewport(lines),
		)
		return prompt, cleanup, true
	}

	o.beginInlineOutput()
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
	return renderedPrompt, cleanup, false
}

func priorityPromptViewport(lines []string) ui.PopupViewportSpec {
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = sanitizeInteractivePromptLine(line); strings.TrimSpace(line) != "" {
			normalized = append(normalized, line)
		}
	}
	if len(normalized) == 0 {
		return ui.PopupViewportSpec{}
	}

	headerEnd := len(normalized)
	if headerEnd > 2 {
		headerEnd = 2
	}
	viewport := ui.PopupViewportSpec{
		HeaderLines: []string{strings.Join(normalized[:headerEnd], " | ")},
	}
	if len(normalized) <= 2 {
		return viewport
	}

	viewport.FooterLines = []string{normalized[len(normalized)-1]}
	body := normalized[headerEnd : len(normalized)-1]
	if len(body) == 0 {
		return viewport
	}
	summaryEnd := len(body)
	if summaryEnd > 3 {
		summaryEnd = 3
	}
	viewport.BodyLines = append(viewport.BodyLines, strings.Join(body[:summaryEnd], " | "))
	viewport.BodyLines = append(viewport.BodyLines, body[summaryEnd:]...)
	return viewport
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

// settleInteractiveOutputLayout flushes deferred bottom-reserve shrink
// compensation without writing transcript content. History / resume replay
// must call this after ClearPrompt (or beginDirectInteractiveOutput) and
// before the first content WriteOutput so layout debt is not billed to
// already-final messages. No-op when there is no enabled surface.
func settleInteractiveOutputLayout(session *ChatSession) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return
	}
	if session.Surface == nil || !session.Surface.Enabled() {
		return
	}
	session.Surface.SettleOutputDebt()
}

// writeDirectInteractiveOutput clears the painted prompt (when present) and
// writes text through FixedBottomSurface.WriteOutput so deferred bottom-reserve
// shrink compensation is flushed and scroll bookkeeping stays consistent.
// Returns false when the session has no enabled surface; callers should fall
// back to beginDirectInteractiveOutput + plain stdout writes.
func writeDirectInteractiveOutput(session *ChatSession, text string) bool {
	if session == nil || text == "" || session.NoInteractive || session.JSONOutput {
		return false
	}
	if session.Surface == nil || !session.Surface.Enabled() {
		return false
	}
	if session.Interaction != nil {
		session.Interaction.ClearPrompt()
	}
	_, err, handled := session.Surface.WriteOutput(os.Stdout, text)
	return handled && err == nil
}

// printDirectInteractiveOutput writes text through the surface when available
// (flushing ClearPrompt shrink debt + WriteOutput bookkeeping). Falls back to
// beginDirectInteractiveOutput + writer otherwise. Use for any stdout content
// that should not leave pendingScrollDown debt for the next write.
func printDirectInteractiveOutput(session *ChatSession, text string) {
	if writeDirectInteractiveOutput(session, text) {
		return
	}
	beginDirectInteractiveOutput(session)
	fmt.Print(text)
}

// printfDirectInteractiveOutput is a convenience for formatted messages.
func printfDirectInteractiveOutput(session *ChatSession, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	printDirectInteractiveOutput(session, text)
}

func showRuntimeComposerPrompt(session *ChatSession, prompt string) bool {
	return newChatPromptOverlay(session).showComposerPreview(prompt)
}

func clearRuntimeComposerPrompt(session *ChatSession) {
	newChatPromptOverlay(session).clearComposerPreview()
}

func clearRuntimeModalPopup(session *ChatSession) {
	newChatPromptOverlay(session).clearModalPopup()
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
	newAICLITranscriptRenderer(session).RenderUser(input)
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
