package commands

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type chatComposerController struct {
	session    *ChatSession
	prompt     string
	initial    ui.LineEditorSnapshot
	completion *chatSlashCompletionController
}

type chatBusyComposerCapture struct {
	session     *ChatSession
	prompt      string
	trackPrompt bool
	cancelled   bool
	initial     ui.LineEditorSnapshot
}

type chatModalComposerPrompt struct {
	session     *ChatSession
	prompt      string
	trackPrompt bool
	cancelled   bool
}

type chatTransientLineComposer struct {
	session *ChatSession
}

type chatSecretComposerPrompt struct {
	session *ChatSession
	prompt  string
}

type chatAgentPanelComposer struct {
	session    *ChatSession
	prompt     string
	controller *chatAgentPanelModalController
}

func newChatComposerController(session *ChatSession) *chatComposerController {
	controller := &chatComposerController{
		session: session,
		prompt:  formatSessionUserPrompt(session),
	}
	if session != nil && session.Interaction != nil {
		controller.initial = session.Interaction.PromptInputSnapshot()
		if controller.initial.Text == "" {
			session.Interaction.SetPromptInput("")
		}
	}
	if shouldEnableSlashCompletion(session) {
		controller.completion = newChatSlashCompletionController(session)
	}
	return controller
}

func (c *chatComposerController) ReadLine() (string, error) {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return "", io.EOF
	}
	defer c.Close()
	line, err := c.session.InputBox.ReadWithHistoryPromptWithHooks(c.prompt, c.hooks())
	return line, normalizeChatComposerReadError(c.session, err)
}

func (c *chatComposerController) Close() {
	if c == nil {
		return
	}
	if c.completion != nil {
		c.completion.Clear()
	}
	if c.session != nil && c.session.Surface != nil {
		c.session.Surface.SetPromptEditorStatusLine("")
	}
}

func (c *chatComposerController) hooks() ui.LineEditorHooks {
	hooks := ui.LineEditorHooks{
		InitialText:           c.initial.Text,
		InitialCursor:         c.initial.Cursor,
		RedrawInitialText:     chatComposerUsesFixedSurface(c.session),
		OnChange:              c.onChange,
		OnBeforeTerminalWrite: c.onBeforeTerminalWrite,
		OnTerminalWrite:       c.onTerminalWrite,
		OnComplete:            c.onComplete,
		MaxVisibleRows:        chatComposerMaxVisibleRows(c.session),
		ResolveMaxVisibleRows: func() int { return chatComposerMaxVisibleRows(c.session) },
	}
	if c.completion != nil {
		hooks.OnNavigate = c.onNavigate
		hooks.OnSubmit = c.onSubmit
		hooks.OnCancelPopup = c.onCancelPopup
	}
	return hooks
}

func chatComposerMaxVisibleRows(session *ChatSession) int {
	if session == nil || session.Surface == nil {
		return ui.ChatComposerMaxVisibleRows
	}
	return session.Surface.PromptInputMaxVisibleRows()
}

func chatComposerUsesFixedSurface(session *ChatSession) bool {
	return session != nil && session.Surface != nil && session.Surface.Enabled()
}

func (c *chatComposerController) onChange(snapshot ui.LineEditorSnapshot) {
	if c == nil || c.session == nil {
		return
	}
	// Slash popup rendering must preserve the cursor from the last completed
	// editor redraw. Track the prompt snapshot first so popup rendering and
	// paste-active blocking observe the same composer state that the editor is
	// about to draw.
	if c.session.Interaction != nil {
		c.session.Interaction.SetPromptInputSnapshot(snapshot)
	}
	if c.completion != nil {
		c.completion.UpdateSnapshot(snapshot)
	}
	if c.session.Surface != nil {
		c.session.Surface.SetPromptEditorStatusLine(formatChatComposerEditorStatus(snapshot))
	}
}

func formatChatComposerEditorStatus(snapshot ui.LineEditorSnapshot) string {
	if snapshot.LogicalLines <= 1 && snapshot.DisplayRows <= 1 {
		return ""
	}
	line := snapshot.LogicalLine
	lines := snapshot.LogicalLines
	if line < 1 {
		line = 1
	}
	if lines < line {
		lines = line
	}
	status := ""
	if lines > 1 {
		status = fmt.Sprintf("多行 %d/%d", line, lines)
	} else {
		status = fmt.Sprintf("自动换行 · 显示 %d 行", snapshot.DisplayRows)
	}
	if snapshot.ViewportRows > 0 && snapshot.DisplayRows > snapshot.ViewportRows {
		start := snapshot.ViewportStart + 1
		end := snapshot.ViewportStart + snapshot.ViewportRows
		status += fmt.Sprintf(" · 视图 %d-%d/%d", start, end, snapshot.DisplayRows)
	}
	return status
}

func (c *chatComposerController) onBeforeTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return ""
	}
	return c.session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
}

func (c *chatComposerController) onTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot, writer io.Writer, text string) bool {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return false
	}
	return c.session.Interaction.WritePromptEditorText(writer, render.LastCursorRow, render.LastCursorCol, text)
}

func (c *chatComposerController) onComplete(snapshot ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
	if c == nil {
		return ui.LineEditorReplacement{}, false
	}
	if c.completion != nil {
		if nextText, nextCursor, ok := c.completion.ApplyCompletion(snapshot.Text, snapshot.Cursor); ok {
			return ui.LineEditorReplacement{Text: nextText, Cursor: nextCursor}, true
		}
	}
	// Preserve Tab completion semantics for slash-command drafts. Everywhere
	// else in the main chat composer, Tab is the plan-mode toggle shortcut.
	if isSlashCommandInput(snapshot.Text) {
		return ui.LineEditorReplacement{}, false
	}
	if err := toggleChatPlanMode(c.session); err != nil {
		if c.session != nil && c.session.Surface != nil {
			c.session.Surface.SetPromptEditorStatusLine(fmt.Sprintf("Plan mode 切换失败：%v", err))
		}
		return ui.LineEditorReplacement{}, false
	}
	// Re-apply the unchanged draft so the line editor redraws after the fixed
	// status bar changes without inserting a literal tab character.
	return ui.LineEditorReplacement{Text: snapshot.Text, Cursor: snapshot.Cursor}, true
}

func (c *chatComposerController) onNavigate(_ ui.LineEditorSnapshot, delta int) bool {
	return c != nil && c.completion != nil && c.completion.Navigate(delta)
}

func (c *chatComposerController) onSubmit(snapshot ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
	if c == nil || c.completion == nil {
		return ui.LineEditorReplacement{}, false
	}
	nextText, nextCursor, ok := c.completion.ApplySubmission(snapshot.Text, snapshot.Cursor)
	if !ok {
		return ui.LineEditorReplacement{}, false
	}
	return ui.LineEditorReplacement{Text: nextText, Cursor: nextCursor}, true
}

func (c *chatComposerController) onCancelPopup(ui.LineEditorSnapshot) bool {
	return c != nil && c.completion != nil && c.completion.Cancel()
}

func normalizeChatComposerReadError(session *ChatSession, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ui.ErrInteractiveInputExitRequested) {
		interruptChatComposerSession(session)
		resetChatComposerPrompt(session)
		return ui.ErrInteractiveInputExitRequested
	}
	if errors.Is(err, ui.ErrInteractiveInputInterrupted) {
		interruptChatComposerSession(session)
		resetChatComposerPrompt(session)
		return io.EOF
	}
	// Esc on empty composer opens the backtrack picker; do not interrupt the session.
	if errors.Is(err, ui.ErrInteractiveInputBacktrackRequested) {
		resetChatComposerPrompt(session)
		return ui.ErrInteractiveInputBacktrackRequested
	}
	resetChatComposerPrompt(session)
	return err
}

func interruptChatComposerSession(session *ChatSession) {
	if session != nil {
		session.Interrupt()
	}
}

func resetChatComposerPrompt(session *ChatSession) {
	if session != nil && session.Interaction != nil {
		session.Interaction.ResetPromptState()
	}
}

func newChatBusyComposerCapture(session *ChatSession, prompt string, priorityPrompt bool) *chatBusyComposerCapture {
	capture := &chatBusyComposerCapture{
		session:     session,
		prompt:      prompt,
		trackPrompt: !priorityPrompt,
	}
	// Busy captures are restarted whenever the turn switches prompts (approval,
	// agent input). Seed the editor with the draft that is already on screen so
	// a restart never wipes text the user is still typing.
	if capture.trackPrompt && session != nil && session.Interaction != nil {
		capture.initial = session.Interaction.PromptInputSnapshot()
	}
	return capture
}

func (c *chatBusyComposerCapture) ReadLine(ctx context.Context) (string, error) {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return "", io.EOF
	}
	return c.session.InputBox.ReadTransientPromptWithHooksContext(ctx, c.prompt, c.hooks())
}

func (c *chatBusyComposerCapture) hooks() ui.LineEditorHooks {
	return ui.LineEditorHooks{
		InitialText:           c.initial.Text,
		InitialCursor:         c.initial.Cursor,
		RedrawInitialText:     chatComposerUsesFixedSurface(c.session),
		OnChange:              c.onChange,
		OnBeforeTerminalWrite: c.onBeforeTerminalWrite,
		OnTerminalWrite:       c.onTerminalWrite,
		OnCancel:              c.onCancel,
		MaxVisibleRows:        chatComposerMaxVisibleRows(c.session),
		ResolveMaxVisibleRows: func() int { return chatComposerMaxVisibleRows(c.session) },
	}
}

func (c *chatBusyComposerCapture) Cancelled() bool {
	return c != nil && c.cancelled
}

func (c *chatBusyComposerCapture) ClearPrompt() {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.RenderPromptInputSnapshot(ui.LineEditorSnapshot{})
}

// PreserveDraft releases the painted prompt rows but keeps the in-progress
// draft, so restarting the capture (or ending the turn) re-renders the same
// text instead of dropping it.
func (c *chatBusyComposerCapture) PreserveDraft() {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.ClearPrompt()
}

func (c *chatBusyComposerCapture) onChange(snapshot ui.LineEditorSnapshot) {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.SetPromptInputSnapshot(snapshot)
}

func (c *chatBusyComposerCapture) onBeforeTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return ""
	}
	return c.session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
}

func (c *chatBusyComposerCapture) onTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot, writer io.Writer, text string) bool {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return false
	}
	return c.session.Interaction.WritePromptEditorText(writer, render.LastCursorRow, render.LastCursorCol, text)
}

func (c *chatBusyComposerCapture) onCancel(ui.LineEditorSnapshot) bool {
	if c != nil {
		c.cancelled = true
	}
	return true
}

func newChatModalComposerPrompt(session *ChatSession, prompt string) *chatModalComposerPrompt {
	return &chatModalComposerPrompt{
		session:     session,
		prompt:      prompt,
		trackPrompt: session == nil || session.Surface == nil || !session.Surface.Enabled(),
	}
}

func (c *chatModalComposerPrompt) ReadLine() (string, error) {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return "", io.EOF
	}
	c.initializePrompt()
	line, err := c.session.InputBox.ReadTransientPromptWithHooks(c.prompt, c.hooks())
	c.clearPrompt()
	return line, c.normalizeReadError(err)
}

func (c *chatModalComposerPrompt) hooks() ui.LineEditorHooks {
	return ui.LineEditorHooks{
		OnChange: c.onChange,
		OnCancel: c.onCancel,
	}
}

func (c *chatModalComposerPrompt) initializePrompt() {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.SetPromptInput("")
}

func (c *chatModalComposerPrompt) clearPrompt() {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.SetPromptInput("")
}

func (c *chatModalComposerPrompt) onChange(snapshot ui.LineEditorSnapshot) {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.SetPromptInput(snapshot.Text)
}

func (c *chatModalComposerPrompt) onCancel(ui.LineEditorSnapshot) bool {
	if c != nil {
		c.cancelled = true
	}
	return true
}

func (c *chatModalComposerPrompt) normalizeReadError(err error) error {
	if c == nil {
		return err
	}
	if c.cancelled && err == nil {
		resetChatComposerPrompt(c.session)
		return errChatInteractivePromptCancelled
	}
	return normalizeChatComposerReadError(c.session, err)
}

func newChatTransientLineComposer(session *ChatSession) *chatTransientLineComposer {
	return &chatTransientLineComposer{session: session}
}

func (c *chatTransientLineComposer) ReadLine() (string, error) {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return "", io.EOF
	}
	line, err := c.session.InputBox.ReadTransientLineWithHooks(ui.LineEditorHooks{})
	return line, normalizeChatComposerReadError(c.session, err)
}

func newChatSecretComposerPrompt(session *ChatSession, prompt string) *chatSecretComposerPrompt {
	return &chatSecretComposerPrompt{session: session, prompt: prompt}
}

func (c *chatSecretComposerPrompt) ReadLine() (string, error) {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return "", io.EOF
	}
	restoreInputMode := pushChatComposerInputMode(c.session, chatInputModeSecret)
	defer restoreInputMode()
	resetChatComposerPromptInput(c.session)
	line, err := c.session.InputBox.ReadTransientSecretPrompt(c.prompt)
	if err == nil {
		resetChatComposerPrompt(c.session)
		return line, nil
	}
	return line, normalizeChatComposerReadError(c.session, err)
}

func resetChatComposerPromptInput(session *ChatSession) {
	if session != nil && session.Interaction != nil {
		session.Interaction.SetPromptInput("")
	}
}

func newChatAgentPanelComposer(session *ChatSession, prompt string, controller *chatAgentPanelModalController) *chatAgentPanelComposer {
	return &chatAgentPanelComposer{
		session:    session,
		prompt:     prompt,
		controller: controller,
	}
}

func (c *chatAgentPanelComposer) ReadLine() error {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return io.EOF
	}
	_, err := c.session.InputBox.ReadTransientPromptWithHooks(c.prompt, c.hooks())
	return normalizeChatAgentPanelComposerReadError(c.session, err)
}

func (c *chatAgentPanelComposer) hooks() ui.LineEditorHooks {
	return ui.LineEditorHooks{
		OnNavigate: c.onNavigate,
		OnMove:     c.onMove,
		OnSubmit:   c.onSubmit,
		OnCancel:   c.onCancel,
	}
}

func (c *chatAgentPanelComposer) onNavigate(_ ui.LineEditorSnapshot, delta int) bool {
	if c != nil && c.controller != nil {
		c.controller.Navigate(delta)
	}
	return true
}

func (c *chatAgentPanelComposer) onMove(_ ui.LineEditorSnapshot, delta int) bool {
	if c != nil && c.controller != nil {
		c.controller.MovePane(delta)
	}
	return true
}

func (c *chatAgentPanelComposer) onSubmit(ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
	if c != nil && c.controller != nil {
		c.controller.Select()
	}
	return ui.LineEditorReplacement{}, true
}

func (c *chatAgentPanelComposer) onCancel(ui.LineEditorSnapshot) bool {
	return true
}

func normalizeChatAgentPanelComposerReadError(session *ChatSession, err error) error {
	if errors.Is(err, ui.ErrInteractiveInputInterrupted) || errors.Is(err, ui.ErrInteractiveInputExitRequested) {
		resetChatComposerPrompt(session)
		return io.EOF
	}
	return err
}
