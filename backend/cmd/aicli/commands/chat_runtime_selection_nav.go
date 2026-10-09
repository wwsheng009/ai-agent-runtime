package commands

import (
	"context"
	"io"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// runtimeSelectionController keeps a highlight cursor for selection popups and
// re-renders the fixed-bottom surface when the user navigates with ↑/↓.
type runtimeSelectionController struct {
	session  *ChatSession
	handle   ui.PopupHandle
	prompt   string
	options  []string
	selected int
	warning  string
	renderFn func(selected int, warning string) []string
}

func newRuntimeSelectionController(
	session *ChatSession,
	handle ui.PopupHandle,
	prompt string,
	options []string,
	selected int,
	renderFn func(selected int, warning string) []string,
) *runtimeSelectionController {
	controller := &runtimeSelectionController{
		session:  session,
		handle:   handle,
		prompt:   prompt,
		options:  append([]string(nil), options...),
		selected: selected,
		renderFn: renderFn,
	}
	controller.clampSelected()
	return controller
}

func (c *runtimeSelectionController) Navigate(delta int) {
	if c == nil || len(c.options) == 0 || delta == 0 {
		return
	}
	n := len(c.options)
	c.selected = (c.selected + delta) % n
	if c.selected < 0 {
		c.selected += n
	}
	c.warning = ""
	c.render()
}

func (c *runtimeSelectionController) Selected() int {
	if c == nil {
		return -1
	}
	return c.selected
}

func (c *runtimeSelectionController) SelectedOption() (string, bool) {
	if c == nil {
		return "", false
	}
	if c.selected < 0 || c.selected >= len(c.options) {
		return "", false
	}
	return c.options[c.selected], true
}

func (c *runtimeSelectionController) SetWarning(warning string) {
	if c == nil {
		return
	}
	c.warning = strings.TrimSpace(warning)
	c.render()
}

func (c *runtimeSelectionController) clampSelected() {
	if c == nil {
		return
	}
	if len(c.options) == 0 {
		c.selected = -1
		return
	}
	if c.selected < 0 {
		c.selected = 0
	}
	if c.selected >= len(c.options) {
		c.selected = len(c.options) - 1
	}
}

func (c *runtimeSelectionController) render() {
	if c == nil || c.renderFn == nil {
		return
	}
	c.clampSelected()
	lines := c.renderFn(c.selected, c.warning)
	updateRuntimeSelectionPopup(c.session, c.handle, lines, c.prompt)
}

// renderWithInput 重画弹层并把已键入的文本并进 popup 输入行。
//
// fixed surface 下 popup 输入行归 surface 所有，编辑器不能自己往 stdout 画它
// （否则每秒的状态重绘会把直写擦掉，表现为输入与 ↑↓ 高亮「没反应」）。
func (c *runtimeSelectionController) renderWithInput(text string) {
	if c == nil || c.renderFn == nil {
		return
	}
	c.clampSelected()
	lines := c.renderFn(c.selected, c.warning)
	// 输入行是单行渲染；多行输入只取首行，光标跟随首行输入末尾。
	inputLine := strings.SplitN(text, "\n", 2)[0]
	updateRuntimeSelectionPopup(c.session, c.handle, lines, c.prompt+inputLine)
}

// chatSelectionComposer wires ↑/↓ into a transient selection prompt so the
// popup highlight moves without submitting a line.
type chatSelectionComposer struct {
	session     *ChatSession
	prompt      string
	controller  *runtimeSelectionController
	trackPrompt bool
	cancelled   bool
}

func newChatSelectionComposer(session *ChatSession, prompt string, controller *runtimeSelectionController) *chatSelectionComposer {
	return &chatSelectionComposer{
		session:     session,
		prompt:      prompt,
		controller:  controller,
		trackPrompt: !chatSessionSurfaceUsable(session),
	}
}

func (c *chatSelectionComposer) ReadLine() (string, error) {
	if c == nil || c.session == nil || c.session.InputBox == nil {
		return "", io.EOF
	}
	c.initializePrompt()
	line, err := c.session.InputBox.ReadTransientPromptWithHooks(c.prompt, c.hooks())
	c.clearPrompt()
	return line, c.normalizeReadError(err)
}

func (c *chatSelectionComposer) hooks() ui.LineEditorHooks {
	hooks := ui.LineEditorHooks{
		OnChange:          c.onChange,
		OnNavigate:        c.onNavigate,
		OnCancel:          c.onCancel,
		OnTerminalControl: c.onTerminalControl,
	}
	if chatComposerUsesFixedSurface(c.session) {
		// popup 输入行由 surface 拥有：编辑器不得直写 stdout，必须被消费掉。
		hooks.OnTerminalWrite = c.onTerminalWrite
		hooks.SuppressSubmitEcho = true
	}
	return hooks
}

// onTerminalWrite 返回 true 表示该次编辑器直写已被消费。
//
// fixed surface 下选择弹层的输入行是 popup 的 ComposerLine，由 surface 画；
// 让编辑器落到 os.Stdout 会覆盖 surface 的行（用户看到 popup 输入行被啃掉字符），
// 且 ↑↓ 重画出来的行会与之打架，表现为高亮「切不动」。
func (c *chatSelectionComposer) onTerminalWrite(_ ui.LineEditorSnapshot, _ ui.LineEditorRenderSnapshot, _ io.Writer, _ string) bool {
	return chatComposerUsesFixedSurface(c.session)
}

// onTerminalControl routes mode sequences through the unified session when it
// owns the terminal; otherwise the editor keeps the raw fallback (non-fixed or
// legacy surface modes).
func (c *chatSelectionComposer) onTerminalControl(sequence string) bool {
	if c == nil || c.session == nil || c.session.Interaction == nil {
		return false
	}
	return c.session.Interaction.WritePromptEditorControl(sequence)
}

func (c *chatSelectionComposer) initializePrompt() {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.SetPromptInput("")
}

func (c *chatSelectionComposer) clearPrompt() {
	if c == nil || !c.trackPrompt || c.session == nil || c.session.Interaction == nil {
		return
	}
	c.session.Interaction.SetPromptInput("")
}

func (c *chatSelectionComposer) onChange(snapshot ui.LineEditorSnapshot) {
	if c == nil || c.session == nil {
		return
	}
	if c.trackPrompt {
		if c.session.Interaction != nil {
			c.session.Interaction.SetPromptInput(snapshot.Text)
		}
		return
	}
	// fixed surface：输入必须并进 popup 输入行，否则 surface 重绘会擦掉编辑器的
	// 直写（与 chatModalComposerPrompt.foldChatPriorityPromptPopupInput 同理）。
	if c.controller != nil {
		c.controller.renderWithInput(snapshot.Text)
	}
}

func (c *chatSelectionComposer) onNavigate(_ ui.LineEditorSnapshot, delta int) bool {
	if c != nil && c.controller != nil {
		c.controller.Navigate(delta)
	}
	// Always consume arrows in selection mode so history navigation does not
	// hijack the modal.
	return true
}

func (c *chatSelectionComposer) onCancel(ui.LineEditorSnapshot) bool {
	if c != nil {
		c.cancelled = true
	}
	return true
}

func (c *chatSelectionComposer) normalizeReadError(err error) error {
	if c == nil {
		return err
	}
	if c.cancelled && err == nil {
		resetChatComposerPrompt(c.session)
		return errChatInteractivePromptCancelled
	}
	return normalizeChatComposerReadError(c.session, err)
}

func initialRuntimeSelectionIndex(options []string, currentMatch, defaultOption string) int {
	if idx := indexOfCaseInsensitive(options, currentMatch); idx >= 0 {
		return idx
	}
	if idx := indexOfCaseInsensitive(options, defaultOption); idx >= 0 {
		return idx
	}
	if len(options) > 0 {
		return 0
	}
	return -1
}

func indexOfCaseInsensitive(options []string, value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return -1
	}
	for i, option := range options {
		if strings.EqualFold(option, value) {
			return i
		}
	}
	return -1
}

// chatInteractiveReadSelectionLine reads a selection prompt and, when the
// interactive line editor is available, attaches ↑/↓ navigation hooks.
func chatInteractiveReadSelectionLine(session *ChatSession, prompt string, controller *runtimeSelectionController) (string, error) {
	if shouldRoutePriorityPromptThroughQueue(session) {
		return chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
	}
	if session != nil && session.InputBox != nil && controller != nil {
		return newChatSelectionComposer(session, prompt, controller).ReadLine()
	}
	return chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
}

// resolveRuntimeSelectionInputWithCursor prefers free-text resolution, and on
// blank input selects the highlighted cursor option when available.
func resolveRuntimeSelectionInputWithCursor(
	input, current, defaultOption string,
	options []string,
	selected int,
	allowCustom, allowClear bool,
) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		if selected >= 0 && selected < len(options) {
			return options[selected], true
		}
		return resolveRuntimeSelectionInput(input, current, defaultOption, options, allowCustom, allowClear)
	}
	return resolveRuntimeSelectionInput(input, current, defaultOption, options, allowCustom, allowClear)
}

func resolveRuntimeReasoningEffortInputWithCursor(
	input, currentMatch string,
	currentValid bool,
	defaultOption string,
	options []string,
	selected int,
) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		if selected >= 0 && selected < len(options) {
			return options[selected], true
		}
		return resolveRuntimeReasoningEffortInput(input, currentMatch, currentValid, defaultOption, options)
	}
	return resolveRuntimeReasoningEffortInput(input, currentMatch, currentValid, defaultOption, options)
}
