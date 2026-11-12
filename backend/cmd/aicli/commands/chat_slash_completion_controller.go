package commands

import (
	"fmt"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const (
	slashCompletionShellHintLine = "Shell 快捷: !git status"
	slashCompletionKeyHintLine   = "↑↓ 选择 · Tab 补全 · Enter 执行 · Esc 关闭"
	slashCompletionPopupOwner    = "slash_completion"
)

type chatSlashCompletionController struct {
	session          *ChatSession
	argumentProvider chatSlashArgumentProvider

	mu                sync.Mutex
	state             chatSlashCompletionState
	text              string
	cursor            int
	selectedCommand   string
	renderedSignature string
	surfaceEnabled    bool
	editorPasteActive bool
	// selectionExplicit 标记用户是否用方向键主动移动过候选。空参数位上的
	// Enter 默认应执行当前命令（/memory list -> submit），只有显式选过候选
	// 时才把 Enter 用于接受补全。
	selectionExplicit bool
}

func newChatSlashCompletionController(session *ChatSession) *chatSlashCompletionController {
	return &chatSlashCompletionController{
		session:          session,
		argumentProvider: newChatSlashArgumentCompletionProvider(),
		state: chatSlashCompletionState{
			Selected: -1,
		},
	}
}

func (c *chatSlashCompletionController) Update(text string) {
	if c == nil {
		return
	}
	c.UpdateAtWithState(text, len([]rune(text)), false)
}

func (c *chatSlashCompletionController) UpdateAt(text string, cursor int) {
	if c == nil {
		return
	}
	c.UpdateAtWithState(text, cursor, false)
}

func (c *chatSlashCompletionController) UpdateSnapshot(snapshot ui.LineEditorSnapshot) {
	if c == nil {
		return
	}
	c.UpdateAtWithState(snapshot.Text, snapshot.Cursor, snapshot.PasteActive)
}

func (c *chatSlashCompletionController) UpdateAtWithState(text string, cursor int, pasteActive bool) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.editorPasteActive = pasteActive
	c.applyTextUpdateLocked(text, cursor)
	c.renderLocked()
}

func (c *chatSlashCompletionController) Clear() {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = chatSlashCompletionState{Selected: -1}
	c.text = ""
	c.cursor = 0
	c.selectedCommand = ""
	c.renderedSignature = ""
	c.surfaceEnabled = false
	c.editorPasteActive = false
	c.selectionExplicit = false
	c.clearPopupLocked()
}

func (c *chatSlashCompletionController) Cancel() bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active {
		return false
	}

	c.clearPopupLocked()
	c.state = chatSlashCompletionState{Selected: -1}
	c.text = ""
	c.cursor = 0
	c.selectedCommand = ""
	c.renderedSignature = ""
	c.surfaceEnabled = false
	c.editorPasteActive = false
	c.selectionExplicit = false
	return true
}

func (c *chatSlashCompletionController) Navigate(delta int) bool {
	if c == nil || delta == 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.state.Active || len(c.state.Candidates) == 0 {
		return false
	}

	selected := c.state.Selected
	if selected < 0 || selected >= len(c.state.Candidates) {
		selected = 0
	}
	selected = (selected + delta) % len(c.state.Candidates)
	if selected < 0 {
		selected += len(c.state.Candidates)
	}

	c.state.Selected = selected
	c.state.ExactMatch = candidateIsExactMatch(c.state.Candidates[selected], c.state.Query)
	c.selectedCommand = c.state.Candidates[selected].Command
	c.selectionExplicit = true
	c.renderLocked()
	return true
}

func (c *chatSlashCompletionController) ApplyCompletion(text string, cursor int) (string, int, bool) {
	if c == nil {
		return text, cursor, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.applyTextUpdateLocked(text, cursor)
	if !c.state.Active || len(c.state.Candidates) == 0 {
		c.renderLocked()
		return text, cursor, false
	}

	selected := c.state.Selected
	if selected < 0 || selected >= len(c.state.Candidates) {
		selected = 0
	}
	selectedCandidate := c.state.Candidates[selected]
	if selectedCandidate.Informational {
		c.renderLocked()
		return text, cursor, false
	}
	if candidateIsExactMatch(selectedCandidate, c.state.Query) && !c.shouldAcceptExactMatchLocked(selectedCandidate) {
		c.renderLocked()
		return text, cursor, false
	}

	if len(c.state.Candidates) > 1 && strings.TrimSpace(c.state.CommonPrefix) != "" && len([]rune(c.state.CommonPrefix)) > len([]rune(c.state.Query)) {
		nextText, nextCursor := applySlashCommandCompletion(text, cursor, c.state.Context.TokenStart, c.state.Context.TokenEnd, c.state.CommonPrefix, false)
		c.selectedCommand = selectedCandidate.Command
		c.applyTextUpdateLocked(nextText, nextCursor)
		c.renderLocked()
		return nextText, nextCursor, true
	}

	nextText, nextCursor := applySlashCommandCompletion(text, cursor, c.state.Context.TokenStart, c.state.Context.TokenEnd, selectedCandidate.Command, selectedCandidate.AcceptsArgs)
	c.selectedCommand = selectedCandidate.Command
	c.applyTextUpdateLocked(nextText, nextCursor)
	c.renderLocked()
	return nextText, nextCursor, true
}

func (c *chatSlashCompletionController) ApplySubmission(text string, cursor int) (string, int, bool) {
	if c == nil {
		return text, cursor, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	explicitSelection := c.selectionExplicit
	c.applyTextUpdateLocked(text, cursor)
	if !c.state.Active || len(c.state.Candidates) == 0 {
		c.clearPopupLocked()
		c.state = chatSlashCompletionState{Selected: -1}
		c.selectedCommand = ""
		c.renderedSignature = ""
		c.surfaceEnabled = false
		return text, cursor, false
	}

	// 光标停在新的空参数位（例如 /memory list 后的空格）时，Enter 是
	// “执行已输入命令”，不是再次接受默认候选。否则 previousCommand 会让
	// Enter 反复把同一个 token 追加成 /memory list list list …。
	if c.argumentTokenEmptyLocked() && !explicitSelection {
		c.clearPopupLocked()
		c.state = chatSlashCompletionState{Selected: -1}
		c.selectedCommand = ""
		c.renderedSignature = ""
		c.surfaceEnabled = false
		return text, cursor, false
	}

	selected := c.state.Selected
	if selected < 0 || selected >= len(c.state.Candidates) {
		selected = 0
	}
	selectedCandidate := c.state.Candidates[selected]
	if selectedCandidate.Informational {
		c.renderLocked()
		return text, cursor, false
	}
	if candidateIsExactMatch(selectedCandidate, c.state.Query) && !c.shouldAcceptExactMatchLocked(selectedCandidate) {
		c.clearPopupLocked()
		c.state = chatSlashCompletionState{Selected: -1}
		c.selectedCommand = ""
		c.renderedSignature = ""
		c.surfaceEnabled = false
		return text, cursor, false
	}

	nextText, nextCursor := applySlashCommandCompletion(text, cursor, c.state.Context.TokenStart, c.state.Context.TokenEnd, selectedCandidate.Command, selectedCandidate.AcceptsArgs)
	c.selectedCommand = selectedCandidate.Command
	c.applyTextUpdateLocked(nextText, nextCursor)
	c.renderLocked()
	return nextText, nextCursor, true
}

func (c *chatSlashCompletionController) applyTextUpdateLocked(text string, cursor int) {
	c.text = text
	c.cursor = cursor
	c.selectionExplicit = false
	commandState := buildSlashCompletionStateWithPrevious(text, cursor, c.state.Selected, c.selectedCommand)
	if argsState, ok := buildSlashArgumentCompletionStateWithPrevious(c.session, c.argumentProvider, text, cursor, commandState.Context, c.state.Selected, c.selectedCommand); ok {
		c.state = argsState
		if c.state.Selected >= 0 && c.state.Selected < len(c.state.Candidates) {
			c.selectedCommand = c.state.Candidates[c.state.Selected].Command
		} else {
			c.selectedCommand = ""
		}
		return
	}
	c.state = commandState
	if c.state.Selected >= 0 && c.state.Selected < len(c.state.Candidates) {
		c.selectedCommand = c.state.Candidates[c.state.Selected].Command
	} else {
		c.selectedCommand = ""
	}
}

func (c *chatSlashCompletionController) shouldAcceptExactMatchLocked(candidate chatSlashCompletionCandidate) bool {
	if c == nil {
		return false
	}
	if !candidateIsExactMatch(candidate, c.state.Query) {
		return true
	}
	if !c.state.Context.InArguments || !candidate.AcceptsArgs {
		return false
	}
	// 只有“参数位里还包含前缀/赋值”才把 Enter 用于补上尾随空格；用户已经
	// 把子命令完整敲出来时必须提交，例如 /memory list。
	return !c.exactArgumentTokenMatchesCandidateLocked(candidate)
}

// argumentTokenEmptyLocked reports whether the cursor is on an empty argument
// token (typically immediately after the separating space).
func (c *chatSlashCompletionController) argumentTokenEmptyLocked() bool {
	start, end, ok := c.argumentTokenRangeLocked()
	if !ok {
		return false
	}
	runes := []rune(c.text)
	// `--model=` 是赋值位尚未填值，仍交给补全接受默认/首个候选；空参数位
	// （`/memory list `）才是“命令已完整，Enter 应执行”的信号。
	if start > 0 && start <= len(runes) && runes[start-1] == '=' {
		return false
	}
	return strings.TrimSpace(string(runes[start:end])) == ""
}

// exactArgumentTokenMatchesCandidateLocked distinguishes a fully typed
// subcommand token from a value embedded in a longer token such as
// --model=gpt-5.2. The former submits on Enter; the latter can still accept a
// completion and append a trailing space.
func (c *chatSlashCompletionController) exactArgumentTokenMatchesCandidateLocked(candidate chatSlashCompletionCandidate) bool {
	start, end, ok := c.argumentTokenRangeLocked()
	if !ok {
		return false
	}
	runes := []rune(c.text)
	// `--model=gpt-5.2` 的补全区间从 `=` 之后开始；它仍是“值位”，不是
	// 用户完整敲出的二级命令，因此 Enter 可以继续接受补全并补一个空格。
	if start > 0 && start <= len(runes) && runes[start-1] == '=' {
		return false
	}
	token := strings.TrimSpace(string(runes[start:end]))
	return token != "" && strings.EqualFold(token, candidate.Command)
}

func (c *chatSlashCompletionController) argumentTokenRangeLocked() (int, int, bool) {
	if c == nil || !c.state.Context.InArguments {
		return 0, 0, false
	}
	start := c.state.Context.TokenStart
	end := c.state.Context.TokenEnd
	runes := []rune(c.text)
	if start < 0 || end < start || end > len(runes) {
		return 0, 0, false
	}
	return start, end, true
}

func buildSlashArgumentCompletionStateWithPrevious(session *ChatSession, provider chatSlashArgumentProvider, text string, cursor int, context chatSlashCompletionContext, previousSelected int, previousCommand string) (chatSlashCompletionState, bool) {
	argsCursor := cursor - context.TokenEnd
	if argsCursor < 0 {
		argsCursor = 0
	}
	runes := []rune(text)
	argsText := ""
	if context.TokenEnd >= 0 && context.TokenEnd <= len(runes) {
		argsText = string(runes[context.TokenEnd:])
	}
	argsCtx := parseSlashArgumentContext(argsText, argsCursor)
	argsTokenStart, argsTokenEnd := slashArgumentCompletionRange(argsCtx)
	argsOffset := context.TokenEnd
	if argsOffset < 0 {
		argsOffset = 0
	}
	argsTokenStart += argsOffset
	argsTokenEnd += argsOffset

	state := chatSlashCompletionState{
		Context: context,
		Query:   activeSlashArgumentQuery(argsCtx),
	}
	state.Context.ArgsQuery = state.Query
	if provider == nil || session == nil || !context.InArguments || strings.TrimSpace(context.Command) == "" {
		return state, false
	}
	state.Context.TokenStart = argsTokenStart
	state.Context.TokenEnd = argsTokenEnd
	state.Context.ArgsTokenStart = argsTokenStart
	state.Context.ArgsTokenEnd = argsTokenEnd

	argsRunes := []rune(argsText)
	if argsCursor > len(argsRunes) {
		argsCursor = len(argsRunes)
	}

	candidates := provider.CompleteSlashArgs(session, context.Command, argsText, argsCursor)
	if candidates == nil {
		return state, false
	}
	state.Active = true
	state.Candidates = candidates
	state.Selected = -1
	if len(candidates) == 0 {
		if q := strings.TrimSpace(context.ArgsQuery); q != "" {
			state.Warning = fmt.Sprintf("未找到匹配参数: %s", q)
		} else {
			state.Warning = fmt.Sprintf("未找到可用参数: %s", context.Command)
		}
		return state, true
	}

	state.CommonPrefix = longestCommonSlashPrefix(candidates)
	if previousCommand != "" {
		for i, candidate := range candidates {
			if strings.EqualFold(candidate.Command, previousCommand) {
				state.Selected = i
				state.ExactMatch = candidateIsExactMatch(candidate, state.Query)
				return state, true
			}
		}
	}

	if previousSelected >= 0 && previousSelected < len(candidates) {
		state.Selected = previousSelected
	} else {
		state.Selected = 0
	}
	state.ExactMatch = candidateIsExactMatch(candidates[state.Selected], state.Query)
	return state, true
}

func (c *chatSlashCompletionController) renderLocked() {
	lines := renderSlashCommandCompletionPopup(c.state, ui.GetTerminalWidth())
	signature := strings.Join(lines, "\n")

	enabled := c.isSurfaceEnabledLocked()
	if !enabled {
		c.surfaceEnabled = false
		return
	}
	if c.isPopupBlockedLocked() {
		if c.renderedSignature != "" {
			c.clearPopupLocked()
		}
		c.renderedSignature = ""
		c.surfaceEnabled = false
		return
	}
	if !c.surfaceEnabled {
		c.renderedSignature = ""
	}
	c.surfaceEnabled = true

	if len(lines) == 0 {
		// Normal chat text is the common path. Do not enqueue a durable
		// ClearPopup action for every keystroke when no completion popup was
		// rendered; that action would reintroduce mailbox backpressure into
		// the editor callback.
		if c.renderedSignature != "" {
			c.clearPopupLocked()
		}
		c.renderedSignature = ""
		return
	}

	if signature == c.renderedSignature {
		return
	}

	newChatPromptOverlay(c.session).showOwnedPopupBelowPrompt(lines, slashCompletionPopupOwner)
	c.renderedSignature = signature
}

func (c *chatSlashCompletionController) clearPopupLocked() {
	newChatPromptOverlay(c.session).clearOwnedPopup(slashCompletionPopupOwner)
}

func (c *chatSlashCompletionController) isSurfaceEnabledLocked() bool {
	if c == nil || c.session == nil || c.session.Surface == nil {
		return false
	}
	return chatSessionSurfaceUsable(c.session)
}

func (c *chatSlashCompletionController) isPopupBlockedLocked() bool {
	if c == nil {
		return true
	}
	if c.editorPasteActive {
		return true
	}
	if c.session == nil {
		return false
	}
	if c.session.Interaction != nil && c.session.Interaction.IsPromptPasteActive() {
		return true
	}
	if c.session.InputQueue != nil && c.session.InputQueue.hasDraft() {
		return true
	}
	return false
}

func renderSlashCommandCompletionPopup(state chatSlashCompletionState, width int) []string {
	if !state.Active {
		return nil
	}
	if width <= 0 {
		width = 80
	}

	noMatchLabel := "未找到匹配命令"
	if state.Context.InArguments {
		noMatchLabel = "未找到匹配参数"
	}

	if len(state.Candidates) == 0 {
		lines := make([]string, 0, 1)
		if warning := strings.TrimSpace(state.Warning); warning != "" {
			lines = append(lines, warning)
		} else {
			lines = append(lines, fmt.Sprintf("%s: %s", noMatchLabel, state.Query))
		}
		return clampSlashCompletionPopupLines(lines, width)
	}

	visible := state.Candidates
	extraLine := ""
	windowStart := 0
	if len(visible) > 6 {
		start := 0
		if state.Selected > 2 {
			start = state.Selected - 2
		}
		if start+5 > len(visible) {
			start = len(visible) - 5
		}
		if start < 0 {
			start = 0
		}
		windowStart = start
		visible = visible[start : start+5]
		extraLine = fmt.Sprintf("还有 %d 个匹配，继续输入可过滤", len(state.Candidates)-len(visible))
	}

	commandWidth := 0
	for _, candidate := range visible {
		if w := ui.DisplayWidth(candidate.Command); w > commandWidth {
			commandWidth = w
		}
	}
	if commandWidth < 8 {
		commandWidth = 8
	}

	lines := make([]string, 0, 2+len(visible))
	if warning := strings.TrimSpace(state.Warning); warning != "" {
		lines = append(lines, warning)
	}
	for idx, candidate := range visible {
		lines = append(lines, formatSlashCompletionCandidateLine(candidate, state.Selected == windowStart+idx, commandWidth, width))
	}
	if extraLine != "" {
		lines = append(lines, extraLine)
	}
	if !state.Context.InArguments && strings.TrimSpace(state.Query) == "/" && len(lines)+1 <= slashCompletionMaxPopupRows {
		lines = append(lines, slashCompletionShellHintLine)
	}
	if len(lines)+1 <= slashCompletionMaxPopupRows {
		lines = append(lines, slashCompletionKeyHintLine)
	}
	return clampSlashCompletionPopupLines(lines, width)
}

func formatSlashCompletionCandidateLine(candidate chatSlashCompletionCandidate, selected bool, commandWidth, width int) string {
	marker := " "
	if selected {
		marker = ">"
	}
	command := padSlashCompletionText(candidate.Command, commandWidth)
	detail := slashCompletionCandidateDetail(candidate)
	line := fmt.Sprintf("%s %s  %s", marker, command, detail)
	return truncateSlashCompletionLine(line, width)
}

func slashCompletionCandidateDetail(candidate chatSlashCompletionCandidate) string {
	summary := strings.TrimSpace(candidate.Summary)
	if summary == "" {
		summary = strings.TrimSpace(candidate.Usage)
	}

	var note string
	switch {
	case strings.TrimSpace(candidate.ShortcutOf) != "":
		note = fmt.Sprintf("%s 的快捷命令", candidate.ShortcutOf)
	case strings.TrimSpace(candidate.AliasOf) != "":
		note = fmt.Sprintf("%s 的别名", candidate.AliasOf)
	}
	if note != "" {
		if summary != "" {
			return summary + "；" + note
		}
		return note
	}
	if summary == "" {
		return "命令"
	}
	return summary
}

func padSlashCompletionText(text string, width int) string {
	if width <= 0 {
		return text
	}
	current := ui.DisplayWidth(text)
	if current >= width {
		return text
	}
	return text + strings.Repeat(" ", width-current)
}

func truncateSlashCompletionLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	if ui.DisplayWidth(line) <= width {
		return line
	}
	if width <= 3 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(line))
	current := 0
	limit := width - 3
	for _, r := range line {
		w := ui.DisplayWidth(string(r))
		if w <= 0 {
			continue
		}
		if current+w > limit {
			break
		}
		builder.WriteRune(r)
		current += w
	}
	builder.WriteString("...")
	return builder.String()
}

func clampSlashCompletionPopupLines(lines []string, width int) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r\n")
		line = truncateSlashCompletionLine(line, width)
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, line)
	}
	return out
}
