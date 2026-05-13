package commands

import (
	"fmt"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const (
	slashCompletionHintLine      = "提示: ↑↓ 选择，Tab/Enter 接受，Esc 关闭"
	slashCompletionShellHintLine = "Shell 快捷: !git status"
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

	c.applyTextUpdateLocked(text, cursor)
	if !c.state.Active || len(c.state.Candidates) == 0 {
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
	return c.state.Context.InArguments && candidate.AcceptsArgs
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
		c.clearPopupLocked()
		c.renderedSignature = ""
		c.surfaceEnabled = false
		return
	}
	if !c.surfaceEnabled {
		c.renderedSignature = ""
	}
	c.surfaceEnabled = true

	if len(lines) == 0 {
		c.clearPopupLocked()
		c.renderedSignature = ""
		return
	}

	if signature == c.renderedSignature {
		return
	}
	if c.renderedSignature != "" {
		c.session.Surface.ClearPopupForOwnerPreserveCursor(slashCompletionPopupOwner)
	}

	c.session.Surface.ShowPopupPreserveCursorForOwner(lines, slashCompletionPopupOwner)
	c.renderedSignature = signature
}

func (c *chatSlashCompletionController) clearPopupLocked() {
	if !c.isSurfaceEnabledLocked() {
		return
	}
	c.session.Surface.ClearPopupForOwnerPreserveCursor(slashCompletionPopupOwner)
}

func (c *chatSlashCompletionController) isSurfaceEnabledLocked() bool {
	if c == nil || c.session == nil || c.session.Surface == nil {
		return false
	}
	return c.session.Surface.Enabled()
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

	title := "命令补全"
	noMatchLabel := "未找到匹配命令"
	if state.Context.InArguments {
		title = "参数补全"
		noMatchLabel = "未找到匹配参数"
		if command := strings.TrimSpace(state.Context.Command); command != "" {
			title = fmt.Sprintf("%s: %s", title, command)
		}
	} else if query := strings.TrimSpace(state.Query); query != "" {
		title = fmt.Sprintf("命令补全: %s", query)
	}

	if len(state.Candidates) == 0 {
		lines := []string{title}
		if warning := strings.TrimSpace(state.Warning); warning != "" {
			lines = append(lines, warning)
		} else {
			lines = append(lines, fmt.Sprintf("%s: %s", noMatchLabel, state.Query))
		}
		lines = append(lines, slashCompletionHintLine)
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
	lines = append(lines, title)
	if warning := strings.TrimSpace(state.Warning); warning != "" {
		lines = append(lines, warning)
	}
	for idx, candidate := range visible {
		lines = append(lines, formatSlashCompletionCandidateLine(candidate, state.Selected == windowStart+idx, commandWidth, width))
	}
	if extraLine != "" {
		lines = append(lines, extraLine)
	}
	if !state.Context.InArguments && strings.TrimSpace(state.Query) == "/" && len(lines)+2 <= slashCompletionMaxPopupRows {
		lines = append(lines, slashCompletionShellHintLine)
	}
	lines = append(lines, slashCompletionHintLine)
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
