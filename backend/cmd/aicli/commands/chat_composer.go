package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/keymap"
)

type chatComposerController struct {
	session    *ChatSession
	prompt     string
	initial    ui.LineEditorSnapshot
	completion *chatSlashCompletionController
	// mentionRoot 是 @ 路径补全的扫描根；为空时取进程工作目录（测试注入用）。
	mentionRoot string
}

type chatBusyComposerCapture struct {
	session      *ChatSession
	prompt       string
	trackPrompt  bool
	answerPrompt bool
	cancelled    bool
	initial      ui.LineEditorSnapshot
	// answerDraft parks the draft that lived in the prompt row so the merged
	// answer can never be mistaken for it (see beginAnswerPrompt).
	answerDraft    ui.LineEditorSnapshot
	answerBegan    bool
	answerReleased bool
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
	ctx, done := c.session.newComposerReadContext()
	defer done()
	line, err := c.session.InputBox.ReadWithHistoryPromptWithHooksContext(ctx, c.prompt, c.hooks())
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
		OnTranscriptRequested: c.onTranscriptRequested,
		ActionForChord:        chatComposerActionForChord,
		OnActionKey:           c.onActionKey,
		CollapsePastedText:    chatComposerCollapsePastedText(c.session),
		MaxVisibleRows:        chatComposerMaxVisibleRows(c.session),
		ResolveMaxVisibleRows: func() int { return chatComposerMaxVisibleRows(c.session) },
		SuppressSubmitEcho:    chatComposerUsesFixedSurface(c.session),
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
	// @ 路径引用：Tab 在有 @token 时优先做路径补全（无弹层；结果写状态行），
	// 并消费按键，避免误触 plan mode 切换。
	if mention := applyChatMentionCompletion(c.mentionWorkspaceRoot(), snapshot.Text, snapshot.Cursor, chatMentionCandidateLimit); mention.Handled {
		if status := strings.TrimSpace(mention.Status); status != "" {
			c.setStatusLine(status)
		}
		if mention.Text != snapshot.Text || mention.Cursor != snapshot.Cursor {
			return ui.LineEditorReplacement{Text: mention.Text, Cursor: mention.Cursor}, true
		}
		return ui.LineEditorReplacement{}, true
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

// mentionWorkspaceRoot 返回 @ 补全的扫描根：优先注入值，否则用进程工作目录。
func (c *chatComposerController) mentionWorkspaceRoot() string {
	if c != nil {
		if root := strings.TrimSpace(c.mentionRoot); root != "" {
			return root
		}
	}
	return chatMentionWorkspaceRoot()
}

func (c *chatComposerController) setStatusLine(status string) {
	if c == nil || c.session == nil || c.session.Surface == nil {
		return
	}
	c.session.Surface.SetPromptEditorStatusLine(status)
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

func (c *chatComposerController) onTranscriptRequested(snapshot ui.LineEditorSnapshot) bool {
	if c == nil || !canOpenChatTranscriptPager(c.session) {
		return false
	}
	// The editor exits raw mode before the alternate pager starts. Persist the
	// exact text/cursor first so the next composer instance restores the draft.
	c.onChange(snapshot)
	return true
}

// onActionKey 分发 keymap 动作：claimed 决定编辑器是否吞掉该键，exitEditor
// 表示需要把屏幕交给宿主（全屏 pager）。均未命中时按键回落到编辑器原有语义
// （例如 ctrl+t 未被认领时仍是 transpose）。
func (c *chatComposerController) onActionKey(snapshot ui.LineEditorSnapshot, action string) (bool, bool) {
	if c == nil || c.session == nil {
		return false, false
	}
	switch keymap.Action(action) {
	case keymap.ActionPermissionCycle:
		return cycleChatPermissionMode(c.session), false
	case keymap.ActionTranscriptPager:
		if c.onTranscriptRequested(snapshot) {
			return true, true
		}
		return false, false
	}
	return false, false
}

func normalizeChatComposerReadError(session *ChatSession, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		// 被外部唤醒（如 /web/api/input 注入输入后要求重新检查输入队列）：
		// 中止本次 composer 读取，主循环 continue 后会在 chatInteractiveReadLine
		// 中优先消费输入队列中的 Web 输入。
		resetChatComposerPrompt(session)
		return io.EOF
	}
	if errors.Is(err, ui.ErrInteractiveInputTranscriptRequested) {
		// The draft remains in Interaction state and must be repainted after the
		// alternate screen releases. Do not clear prompt ownership here.
		return ui.ErrInteractiveInputTranscriptRequested
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

// newChatBusyComposerCapture builds one busy-capture read. mergeAnswerPrompt
// marks a priority prompt (approval/question) whose input is merged into the
// bottom prompt row instead of the popup input row: the capture then publishes
// the answer through the regular prompt state, exactly like
// chatMergedPromptComposer does when it owns stdin itself.
func newChatBusyComposerCapture(session *ChatSession, prompt string, priorityPrompt bool, mergeAnswerPrompt bool) *chatBusyComposerCapture {
	capture := &chatBusyComposerCapture{
		session:      session,
		prompt:       prompt,
		trackPrompt:  !priorityPrompt || mergeAnswerPrompt,
		answerPrompt: mergeAnswerPrompt,
	}
	// Busy captures are restarted whenever the turn switches prompts (approval,
	// agent input). Seed the editor with the draft that is already on screen so
	// a restart never wipes text the user is still typing. A merged answer
	// shares that row but must start empty; the parked draft is handed back when
	// the answer is done.
	if !priorityPrompt && session != nil && session.Interaction != nil {
		capture.initial = session.Interaction.PromptInputSnapshot()
	}
	return capture
}

func (c *chatBusyComposerCapture) ReadLine(ctx context.Context) (string, error) {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return "", io.EOF
	}
	if c.answerPrompt {
		c.beginAnswerPrompt()
		defer c.releaseAnswerPrompt()
	}
	return c.session.InputBox.ReadTransientPromptWithHooksContext(ctx, c.prompt, c.hooks())
}

// beginAnswerPrompt turns the bottom prompt row into the input row of a merged
// approval/question answer. The draft that was parked there is kept aside so the
// answer can never overwrite it, mirroring chatMergedPromptComposer.
func (c *chatBusyComposerCapture) beginAnswerPrompt() {
	if c == nil || !c.answerPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.answerDraft = c.session.Interaction.PromptInputSnapshot()
	c.answerBegan = true
	c.answerReleased = false
	c.session.Interaction.SetPromptInput("")
	c.session.Interaction.ShowAnswerPrompt()
}

// releaseAnswerPrompt drops the answer text and hands the parked draft back. The
// prompt row itself stays painted: the answer is answered during a running turn,
// and nothing repaints the row there (PrintPrompt only paints in Ready state, see
// chat.go), so releasing it with DiscardPrompt would leave the user without the
// "> " input row for the rest of the turn.
//
// It is idempotent: the busy loop clears the capture after routing the answer
// while the read itself releases the row on every exit path.
func (c *chatBusyComposerCapture) releaseAnswerPrompt() {
	if c == nil || !c.answerPrompt || !c.answerBegan || c.answerReleased || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.answerReleased = true
	// Replace the answer text with the parked draft instead of clearing the row:
	// an empty draft restores the plain "> " row, a parked draft restores the
	// text the user was still typing. When the row could not be taken over
	// (non fixed-surface terminal) this only republishes the semantic draft.
	c.session.Interaction.RenderPromptInputSnapshot(c.answerDraft)
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
		SuppressSubmitEcho:    chatComposerUsesFixedSurface(c.session),
	}
}

func (c *chatBusyComposerCapture) Cancelled() bool {
	return c != nil && c.cancelled
}

func (c *chatBusyComposerCapture) ClearPrompt() {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return
	}
	if c.answerPrompt {
		// The answer is done: drop it and hand the parked draft back.
		c.releaseAnswerPrompt()
		return
	}
	if !c.trackPrompt {
		return
	}
	c.session.Interaction.RenderPromptInputSnapshot(ui.LineEditorSnapshot{})
}

// PreserveDraft releases the painted prompt rows but keeps the in-progress
// draft, so restarting the capture (or ending the turn) re-renders the same
// text instead of dropping it.
func (c *chatBusyComposerCapture) PreserveDraft() {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return
	}
	if c.answerPrompt {
		c.releaseAnswerPrompt()
		return
	}
	if !c.trackPrompt {
		return
	}
	c.session.Interaction.ClearPrompt()
}

func (c *chatBusyComposerCapture) onChange(snapshot ui.LineEditorSnapshot) {
	if c == nil || c.session == nil {
		return
	}
	if c.trackPrompt || c.answerPrompt {
		if c.session.Interaction != nil {
			c.session.Interaction.SetPromptInputSnapshot(snapshot)
		}
		return
	}
	// priority prompt（审批/提问）在运行期由本 capture 独占 stdin，底部 prompt 行
	// 已被 park；输入必须并进 popup 输入行，否则固定 surface 不重绘输入文本，用户
	// 看到的只有每秒刷新的状态时钟（「Waiting for approval」），表现为无法输入。
	foldChatPriorityPromptPopupInput(c.session, c.prompt, snapshot.Text)
}

func (c *chatBusyComposerCapture) onBeforeTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
	if c == nil || !(c.trackPrompt || c.answerPrompt) || c.session == nil || c.session.Interaction == nil {
		return ""
	}
	return c.session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
}

func (c *chatBusyComposerCapture) onTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot, writer io.Writer, text string) bool {
	if c == nil || !(c.trackPrompt || c.answerPrompt) || c.session == nil || c.session.Interaction == nil {
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
	if c == nil || c.session == nil {
		return
	}
	if c.trackPrompt {
		if c.session.Interaction != nil {
			c.session.Interaction.SetPromptInput(snapshot.Text)
		}
		return
	}
	foldChatPriorityPromptPopupInput(c.session, c.prompt, snapshot.Text)
}

// foldChatPriorityPromptPopupInput 在 fixed-surface 模式下把 priority prompt
// 的输入文本并入 popup 输入行（ComposerLine = 提示 + 输入）。popup 输入行的
// 静态部分（ComposerLine=提示）不含用户输入，而 compose/legacy 光标列都取该行
// 的显示宽度，导致光标被钉在提示之后的第一列；把输入并进该行后光标自然跟随
// 输入末尾。
//
// 所有可能接管 priority prompt 的读取者都必须调用它：modal composer
// （无外部输入捕获）与 busy composer capture（agent 运行期由它独占 stdin）。
// 后者曾经漏掉这一步，导致审批提示出现后用户敲的键既不在 popup 输入行、也不
// 在底部 prompt 行显示（固定 surface 每秒重绘状态时钟还会擦掉编辑器的直写），
// 表现为「Waiting for approval」下输入无反应。
func foldChatPriorityPromptPopupInput(session *ChatSession, prompt string, text string) {
	if session == nil || session.Surface == nil || !session.Surface.Enabled() {
		return
	}
	handle := session.priorityPopupHandle
	if !handle.Valid() {
		return
	}
	lines := append([]string(nil), session.priorityPopupLines...)
	// 输入行是单行渲染；多行输入只取首行，光标跟随首行输入末尾。
	inputLine := strings.SplitN(text, "\n", 2)[0]
	session.Surface.UpdatePopupInputForHandle(handle, lines, prompt+inputLine, true)
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

// chatMergedPromptComposer reads an interactive answer (ask_user_question)
// through the user's bottom prompt instead of a dedicated answer input row.
// The answer text and the cursor go through the regular composer channel
// (SetPromptInputSnapshot -> InputEvent -> PromptInput/PromptCursor), so the
// cursor follows the typed answer. The separate popup input row (ComposerLine)
// is no longer rendered for questions: its cursor column is derived from the
// static line width and stayed pinned to the first column after the label.
//
// An unsent draft that the user typed before the question arrived is parked for
// the duration of the answer and handed back afterwards, so the answer can
// never be mistaken for the draft and the answer can never overwrite it.
type chatMergedPromptComposer struct {
	session   *ChatSession
	cancelled bool
}

func newChatMergedPromptComposer(session *ChatSession) *chatMergedPromptComposer {
	return &chatMergedPromptComposer{session: session}
}

// chatMergedAnswerPromptSupported reports whether interactive answers can be
// merged into the bottom prompt on this terminal. It requires the fixed bottom
// surface (the presenter derives the physical cursor from AppState) plus a
// directly reading line editor; queued/external input capture keeps the
// dedicated popup input row.
func chatMergedAnswerPromptSupported(session *ChatSession) bool {
	if session == nil || session.Interaction == nil || session.InputBox == nil {
		return false
	}
	if !chatComposerUsesFixedSurface(session) {
		return false
	}
	return !shouldRoutePriorityPromptThroughQueue(session)
}

// chatMergedAnswerPromptRenderable reports whether an interactive answer can be
// typed into the bottom prompt row right now.
//
// While the agent runs, the busy queued-input capture owns stdin for the whole
// turn (chat_send.go -> startBusyQueuedInputCapture), so the answer cannot be
// read here; that capture paints the answer into the prompt row itself (see
// newChatBusyComposerCapture's mergeAnswerPrompt). Outside a running turn the
// reader takes the prompt row over directly (chatMergedPromptComposer).
//
// Terminals without the fixed bottom surface keep the dedicated popup input row
// in both cases.
func chatMergedAnswerPromptRenderable(session *ChatSession) bool {
	if session == nil || session.Interaction == nil || session.InputBox == nil {
		return false
	}
	if !chatComposerUsesFixedSurface(session) {
		return false
	}
	if session.InputQueue != nil && session.InputQueue.hasExternalInputCaptureActive() {
		return true
	}
	return !shouldRoutePriorityPromptThroughQueue(session)
}

// ReadLine reads one answer line merged into the bottom prompt. ok=false means
// the merged channel is unavailable (for example the prompt row could not be
// painted) and the caller must fall back to the dedicated popup input row.
func (c *chatMergedPromptComposer) ReadLine() (line string, ok bool, err error) {
	if c == nil || !chatMergedAnswerPromptSupported(c.session) {
		return "", false, nil
	}
	interaction := c.session.Interaction
	draft := interaction.PromptInputSnapshot()
	// The answer shares the prompt row with the regular draft: clear it first so
	// the answer starts empty and the parked draft cannot leak into it.
	interaction.SetPromptInput("")
	if !interaction.ShowAnswerPrompt() {
		c.restoreDraft(draft)
		return "", false, nil
	}
	defer c.finish(draft)
	ctx, done := c.session.newComposerReadContext()
	defer done()
	line, err = c.session.InputBox.ReadWithHistoryPromptWithHooksContext(ctx, formatSessionUserPrompt(c.session), c.hooks())
	return line, true, c.normalizeReadError(err)
}

func (c *chatMergedPromptComposer) hooks() ui.LineEditorHooks {
	return ui.LineEditorHooks{
		OnChange:              c.onChange,
		OnBeforeTerminalWrite: c.onBeforeTerminalWrite,
		OnTerminalWrite:       c.onTerminalWrite,
		OnCancel:              c.onCancel,
		MaxVisibleRows:        chatComposerMaxVisibleRows(c.session),
		ResolveMaxVisibleRows: func() int { return chatComposerMaxVisibleRows(c.session) },
		SuppressSubmitEcho:    chatComposerUsesFixedSurface(c.session),
	}
}

func (c *chatMergedPromptComposer) onChange(snapshot ui.LineEditorSnapshot) {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.SetPromptInputSnapshot(snapshot)
}

func (c *chatMergedPromptComposer) onBeforeTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return ""
	}
	return c.session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
}

func (c *chatMergedPromptComposer) onTerminalWrite(_ ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot, writer io.Writer, text string) bool {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return false
	}
	return c.session.Interaction.WritePromptEditorText(writer, render.LastCursorRow, render.LastCursorCol, text)
}

func (c *chatMergedPromptComposer) onCancel(ui.LineEditorSnapshot) bool {
	if c != nil {
		c.cancelled = true
	}
	return true
}

// finish releases the answer prompt row and hands the parked draft back. The
// question/answer pair itself is recorded by the caller's transcript echo, so
// dropping the answer draft here cannot lose user-visible content.
func (c *chatMergedPromptComposer) finish(draft ui.LineEditorSnapshot) {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.DiscardPrompt()
	c.restoreDraft(draft)
}

func (c *chatMergedPromptComposer) restoreDraft(draft ui.LineEditorSnapshot) {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return
	}
	if draft.Text == "" {
		return
	}
	c.session.Interaction.SetPromptInputSnapshot(draft)
}

func (c *chatMergedPromptComposer) normalizeReadError(err error) error {
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
