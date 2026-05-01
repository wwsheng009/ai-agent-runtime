package commands

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatInteractionCoordinator struct {
	session *ChatSession
	writer  io.Writer
	surface *ui.FixedBottomSurface

	mu                 sync.Mutex
	promptVisible      bool
	promptInput        string
	thinkingActive     bool
	streamingActive    bool
	streamRendered     bool
	streamMode         assistantStreamMode
	streamTrailingLF   bool
	streamLines        int
	streamDisplayLines int
	streamBuffer       strings.Builder
	streamRuneDelay    time.Duration
	maxChunkDelay      time.Duration
	promptDelay        time.Duration
	promptSeq          uint64
	promptAdvanceFn    func() bool
	liveStreamFn       func() bool

	reasoningActive     bool
	reasoningRendered   bool
	reasoningTrailingLF bool
	reasoningMeta       string
	reasoningBuffer     strings.Builder
	completeBlockOutput bool
}

func newChatInteractionCoordinator(session *ChatSession) *chatInteractionCoordinator {
	return &chatInteractionCoordinator{
		session:         session,
		writer:          os.Stdout,
		streamRuneDelay: 6 * time.Millisecond,
		maxChunkDelay:   90 * time.Millisecond,
		promptDelay:     120 * time.Millisecond,
	}
}

func (c *chatInteractionCoordinator) SetWriter(writer io.Writer) {
	if c == nil || writer == nil {
		return
	}
	c.writer = writer
}

func (c *chatInteractionCoordinator) SetSurface(surface *ui.FixedBottomSurface) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.surface = surface
	c.updateSurfaceStatusLocked("Ready")
}

func (c *chatInteractionCoordinator) SupportsLiveStream() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.supportsLiveStreamLocked()
}

func (c *chatInteractionCoordinator) supportsLiveStreamLocked() bool {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return false
	}
	if c.liveStreamFn != nil {
		return c.liveStreamFn()
	}
	if c.writer != os.Stdout {
		return false
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return stdoutInfo.Mode()&os.ModeCharDevice != 0
}

func (c *chatInteractionCoordinator) PrintPrompt() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	if !shouldDisplayInteractivePrompt(c.session) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.promptSeq++
	c.promptInput = ""
	if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
		return
	}
	fmt.Fprint(c.writer, formatSessionUserPrompt(c.session))
	c.promptVisible = true
}

func formatSessionUserPrompt(session *ChatSession) string {
	attachmentCount := 0
	if session != nil {
		attachmentCount = len(session.ImagePaths)
	}
	return ui.FormatUserPromptWithAttachments(attachmentCount)
}

func promptDisplayText(session *ChatSession) string {
	attachmentCount := 0
	if session != nil {
		attachmentCount = len(session.ImagePaths)
	}
	if attachmentCount <= 0 {
		return ui.GetTheme(ui.ThemeAuto).UserIcon + "你> "
	}
	return fmt.Sprintf("%s你> [📎%d] ", ui.GetTheme(ui.ThemeAuto).UserIcon, attachmentCount)
}

func (c *chatInteractionCoordinator) updateSurfaceStatusLocked(state string) {
	if c == nil || c.surface == nil {
		return
	}
	state = strings.TrimSpace(state)
	if state == "" {
		state = "Ready"
	}
	parts := []string{state}
	if c.session != nil {
		if model := strings.TrimSpace(c.session.Model); model != "" {
			parts = append(parts, "model "+model)
		}
		if provider := strings.TrimSpace(c.session.ProviderName); provider != "" {
			parts = append(parts, provider)
		}
		if c.session.MsgCount > 0 {
			parts = append(parts, fmt.Sprintf("msgs %d", c.session.MsgCount))
		}
		if c.session.TokenCount > 0 {
			parts = append(parts, fmt.Sprintf("tokens %d", c.session.TokenCount))
		}
	}
	c.surface.SetStatusLine(strings.Join(parts, " | "))
}

func (c *chatInteractionCoordinator) RefreshStatus(state string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state = strings.TrimSpace(state)
	if state == "" {
		state = c.currentSurfaceStateLocked()
	}
	c.updateSurfaceStatusLocked(state)
}

func (c *chatInteractionCoordinator) currentSurfaceStateLocked() string {
	if c.streamingActive {
		return "Streaming"
	}
	if c.thinkingActive || c.reasoningActive {
		return "Thinking"
	}
	return "Ready"
}

func (c *chatInteractionCoordinator) StartThinking() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginMessageLocked()
	c.thinkingActive = true
	c.updateSurfaceStatusLocked("Thinking")
}

func (c *chatInteractionCoordinator) ClearThinking() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.thinkingActive {
		return
	}
	c.clearThinkingLocked()
	c.thinkingActive = false
	c.updateSurfaceStatusLocked("Ready")
}

func (c *chatInteractionCoordinator) RenderAssistant(response string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	promptWasVisible := c.promptVisible
	c.beginMessageLocked()
	response = sanitizeInteractiveAsyncTeamLaunchResponse(response)
	formatted := response
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(response)
	}
	c.writeCompleteBlockLocked(ui.FormatAssistantMessage(formatted), promptWasVisible)
}

func (c *chatInteractionCoordinator) RenderReasoningDelta(block *runtimetypes.ReasoningBlock) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || block == nil {
		return
	}
	display := block.RawDisplayText()
	if display == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reasoningActive {
		c.beginMessageLocked()
		c.reasoningActive = true
		c.reasoningRendered = false
		c.reasoningTrailingLF = false
		c.reasoningBuffer.Reset()
		fmt.Fprintln(c.writer, ui.FormatAssistantSupplementBlock(chatToolDivider("reasoning")))
		if meta := chatReasoningMetaLine(block); meta != "" {
			c.reasoningMeta = meta
			fmt.Fprintln(c.writer, ui.FormatAssistantSupplementBlock(meta))
		} else {
			c.reasoningMeta = ""
		}
	}
	delta := normalizeAssistantStreamDelta(c.reasoningBuffer.String(), display)
	if delta == "" {
		return
	}
	if c.supportsLiveStreamLocked() {
		c.writeIndentedStreamingDeltaLocked(delta, ui.AssistantContentIndent()+"  ", &c.reasoningRendered, &c.reasoningTrailingLF)
	}
	c.reasoningBuffer.WriteString(delta)
}

func (c *chatInteractionCoordinator) RenderAssistantDelta(delta string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || delta == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.streamingActive {
		if !c.reasoningActive {
			c.beginMessageLocked()
		}
		c.streamingActive = true
		c.updateSurfaceStatusLocked("Streaming")
		c.streamRendered = false
		c.streamMode = assistantStreamModeUnknown
		c.streamTrailingLF = false
		c.streamLines = 0
		c.streamBuffer.Reset()
	}
	delta = normalizeAssistantStreamDelta(c.streamBuffer.String(), delta)
	if delta == "" {
		return
	}
	if c.supportsLiveStreamLocked() && !c.reasoningActive {
		c.writeIndentedStreamingDeltaLocked(delta, ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
	}
	c.streamBuffer.WriteString(delta)
	c.streamLines += strings.Count(delta, "\n")
}

func normalizeAssistantStreamDelta(existing, incoming string) string {
	if existing == "" || incoming == "" {
		return incoming
	}
	if strings.HasPrefix(incoming, existing) {
		return incoming[len(existing):]
	}
	return incoming
}

func resolveStreamCompletionSuffix(existing, final string) string {
	if final == "" {
		return ""
	}
	if existing == "" {
		return final
	}
	if strings.HasPrefix(final, existing) {
		return final[len(existing):]
	}
	return ""
}

func (c *chatInteractionCoordinator) EstimateStreamFlushTimeout(content string) time.Duration {
	if c == nil {
		return 500 * time.Millisecond
	}
	runeCount := len([]rune(content))
	if runeCount <= 0 {
		return 500 * time.Millisecond
	}
	delay := c.streamRuneDelay
	if delay <= 0 {
		delay = 500 * time.Microsecond
	}
	estimate := time.Duration(runeCount)*delay + 500*time.Millisecond
	if estimate < 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	if estimate > 10*time.Second {
		return 10 * time.Second
	}
	return estimate
}

func (c *chatInteractionCoordinator) CompleteAssistantResponse(response string) bool {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.streamingActive {
		return false
	}
	finalContent := response
	if strings.TrimSpace(finalContent) == "" {
		finalContent = c.streamBuffer.String()
	}
	if finalContent == "" {
		c.resetStreamLocked()
		return true
	}
	finalContent = sanitizeInteractiveAsyncTeamLaunchResponse(finalContent)
	if c.supportsLiveStreamLocked() {
		suffix := resolveStreamCompletionSuffix(c.streamBuffer.String(), finalContent)
		if suffix != "" {
			c.writeIndentedStreamingDeltaLocked(suffix, ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
			c.streamBuffer.WriteString(suffix)
		}
		if c.streamRendered && !c.streamTrailingLF {
			fmt.Fprintln(c.writer)
		}
		c.resetStreamLocked()
		return true
	}
	formatted := finalContent
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(finalContent)
	}
	c.writeCompleteBlockLocked(ui.FormatAssistantMessage(formatted), false)
	c.resetStreamLocked()
	return true
}

func (c *chatInteractionCoordinator) CompleteReasoningResponse(block *runtimetypes.ReasoningBlock) bool {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reasoningActive {
		return false
	}
	finalText := c.reasoningBuffer.String()
	if block != nil {
		if display := block.RawDisplayText(); display != "" {
			finalText = display
		}
	}
	if c.supportsLiveStreamLocked() {
		suffix := resolveStreamCompletionSuffix(c.reasoningBuffer.String(), finalText)
		if suffix != "" {
			c.writeIndentedStreamingDeltaLocked(suffix, ui.AssistantContentIndent()+"  ", &c.reasoningRendered, &c.reasoningTrailingLF)
			c.reasoningBuffer.WriteString(suffix)
		}
		c.finalizeReasoningLocked()
		return true
	}
	renderBlock := block
	if renderBlock == nil {
		renderBlock = &runtimetypes.ReasoningBlock{
			Summary:        finalText,
			Visibility:     runtimetypes.ReasoningVisibilitySummary,
			Streamable:     true,
			ReplayRequired: false,
		}
	}
	lines := chatReasoningLines(renderBlock)
	if len(lines) > 0 {
		c.writeCompleteBlockLocked(ui.FormatAssistantSupplementBlock(strings.Join(lines, "\n")), false)
	}
	c.resetReasoningLocked()
	return true
}

func (c *chatInteractionCoordinator) FinalizeAssistantDelta() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.streamingActive {
		return
	}
	content := c.streamBuffer.String()
	if c.supportsLiveStreamLocked() {
		if c.streamRendered && !c.streamTrailingLF {
			fmt.Fprintln(c.writer)
		}
		if c.streamRendered {
			c.completeBlockOutput = true
		}
		c.resetStreamLocked()
		return
	}
	if content != "" {
		content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
		formatted := content
		if c.session.Formatter != nil {
			formatted = c.session.Formatter.Format(content)
		}
		c.writeCompleteBlockLocked(ui.FormatAssistantMessage(formatted), false)
		c.resetStreamLocked()
		return
	}
	c.resetStreamLocked()
}

func (c *chatInteractionCoordinator) FinalizeReasoningDelta() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reasoningActive {
		return
	}
	if c.supportsLiveStreamLocked() {
		c.finalizeReasoningLocked()
		return
	}
	renderBlock := &runtimetypes.ReasoningBlock{
		Summary:    c.reasoningBuffer.String(),
		Visibility: runtimetypes.ReasoningVisibilitySummary,
		Streamable: true,
	}
	lines := chatReasoningLines(renderBlock)
	if len(lines) > 0 {
		c.writeCompleteBlockLocked(ui.FormatAssistantSupplementBlock(strings.Join(lines, "\n")), false)
	}
	c.resetReasoningLocked()
}

func (c *chatInteractionCoordinator) RenderAsyncLine(line string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || strings.TrimSpace(line) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	promptWasVisible := c.promptVisible
	c.beginMessageLocked()
	c.writeCompleteBlockLocked(ui.FormatAssistantSupplementBlock(line), promptWasVisible)
}

func (c *chatInteractionCoordinator) RenderError(err error) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	promptWasVisible := c.promptVisible
	c.beginMessageLocked()
	c.writeCompleteBlockLocked(ui.FormatErrorMessage(fmt.Sprintf("操作错误: %v", err)), promptWasVisible)
}

func (c *chatInteractionCoordinator) ClearPrompt() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.promptVisible {
		c.clearVisiblePromptLocked()
	}
	c.promptSeq++
	c.promptVisible = false
	c.promptInput = ""
}

func (c *chatInteractionCoordinator) ResetPromptState() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.promptSeq++
	c.promptVisible = false
	c.promptInput = ""
}

func (c *chatInteractionCoordinator) SetPromptInput(input string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	c.promptInput = input
}

func (c *chatInteractionCoordinator) ResetRunState() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completeBlockOutput = false
	c.updateSurfaceStatusLocked("Ready")
}

func (c *chatInteractionCoordinator) beginMessageLocked() {
	c.promptSeq++
	if c.thinkingActive {
		c.clearThinkingLocked()
		c.thinkingActive = false
	}
	if c.reasoningActive {
		c.flushReasoningLocked()
		c.resetReasoningLocked()
	}
	if c.streamingActive {
		c.flushStreamLocked()
		c.resetStreamLocked()
	}
	if c.promptVisible {
		c.clearVisiblePromptLocked()
		c.promptVisible = false
	}
	if c.surface != nil {
		c.surface.BeginOutput()
	}
}

func (c *chatInteractionCoordinator) clearThinkingLocked() {
}

// flushStreamLocked outputs any buffered streaming content before the stream
// is interrupted by another message (e.g. thinking, async line, new response).
// This prevents text from being silently swallowed when a ReAct loop's
// intermediate assistant deltas are never finalized via FinalizeAssistantDelta.
func (c *chatInteractionCoordinator) flushStreamLocked() {
	if c.supportsLiveStreamLocked() {
		c.renderBufferedAssistantStreamLocked()
		if c.streamRendered && !c.streamTrailingLF {
			fmt.Fprintln(c.writer)
		}
		return
	}
	content := c.streamBuffer.String()
	if strings.TrimSpace(content) == "" {
		if !c.streamTrailingLF {
			fmt.Fprintln(c.writer)
		}
		return
	}
	content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
	formatted := content
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(content)
	}
	fmt.Fprintln(c.writer, ui.FormatAssistantMessage(formatted))
}

func (c *chatInteractionCoordinator) flushReasoningLocked() {
	if !c.reasoningActive {
		return
	}
	if c.supportsLiveStreamLocked() {
		c.finalizeReasoningLocked()
		return
	}
	renderBlock := &runtimetypes.ReasoningBlock{
		Summary:    c.reasoningBuffer.String(),
		Visibility: runtimetypes.ReasoningVisibilitySummary,
		Streamable: true,
	}
	lines := chatReasoningLines(renderBlock)
	if len(lines) > 0 {
		fmt.Fprintln(c.writer, ui.FormatAssistantSupplementBlock(strings.Join(lines, "\n")))
	}
}

func (c *chatInteractionCoordinator) clearVisiblePromptLocked() {
	if c == nil {
		return
	}
	promptText := promptDisplayText(c.session)
	promptLine := promptText + c.promptInput
	if c.shouldAdvanceAfterPromptLocked() {
		fmt.Fprint(c.writer, "\r\n")
		c.promptInput = ""
		return
	}
	termWidth := ui.GetTerminalWidth()
	if termWidth <= 0 {
		termWidth = 80
	}
	// 当 prompt+输入已经折行时，先把光标上移到输入起始行，
	// 再逐行清理 prompt/input 曾占用的区域，避免清掉固定底部状态栏。
	if rows := interactivePromptDisplayRows(promptLine, termWidth); rows > 1 {
		fmt.Fprintf(c.writer, "\x1b[%dA", rows-1)
	}
	clearPromptDisplayRows(c.writer, interactivePromptDisplayRows(promptLine, termWidth))
	c.promptInput = ""
}

func clearPromptDisplayRows(writer io.Writer, rows int) {
	if writer == nil {
		return
	}
	if rows < 1 {
		rows = 1
	}
	fmt.Fprint(writer, "\r")
	for i := 0; i < rows; i++ {
		fmt.Fprint(writer, "\x1b[K")
		if i < rows-1 {
			fmt.Fprint(writer, "\x1b[1B\r")
		}
	}
	if rows > 1 {
		fmt.Fprintf(writer, "\x1b[%dA\r", rows-1)
	}
}

func interactivePromptDisplayRows(text string, termWidth int) int {
	if termWidth <= 0 {
		termWidth = 80
	}
	row, col := 0, 0
	for _, r := range text {
		switch r {
		case '\r', '\n':
			row++
			col = 0
			continue
		}
		width := ui.DisplayWidth(string(r))
		if width <= 0 {
			continue
		}
		col += width
		if col >= termWidth {
			row += col / termWidth
			col %= termWidth
		}
	}
	return row + 1
}

func (c *chatInteractionCoordinator) finalizeReasoningLocked() {
	if !c.reasoningActive {
		return
	}
	if c.reasoningRendered && !c.reasoningTrailingLF {
		fmt.Fprintln(c.writer)
	}
	fmt.Fprintln(c.writer, ui.FormatAssistantSupplementBlock(chatToolDivider("end reasoning")))
	c.completeBlockOutput = true
	c.resetReasoningLocked()
	c.renderBufferedAssistantStreamLocked()
}

func (c *chatInteractionCoordinator) renderBufferedAssistantStreamLocked() {
	if !c.supportsLiveStreamLocked() || !c.streamingActive || c.streamRendered {
		return
	}
	content := c.streamBuffer.String()
	if content == "" {
		return
	}
	c.writeIndentedStreamingDeltaLocked(content, ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
	c.completeBlockOutput = false
}

func (c *chatInteractionCoordinator) resetStreamLocked() {
	c.streamingActive = false
	c.streamRendered = false
	c.streamMode = assistantStreamModeUnknown
	c.streamTrailingLF = false
	c.streamLines = 0
	c.streamDisplayLines = 0
	c.streamBuffer.Reset()
	if !c.thinkingActive && !c.reasoningActive {
		c.updateSurfaceStatusLocked("Ready")
	}
}

func (c *chatInteractionCoordinator) resetReasoningLocked() {
	c.reasoningActive = false
	c.reasoningRendered = false
	c.reasoningTrailingLF = false
	c.reasoningMeta = ""
	c.reasoningBuffer.Reset()
	if !c.thinkingActive && !c.streamingActive {
		c.updateSurfaceStatusLocked("Ready")
	}
}

func (c *chatInteractionCoordinator) writeCompleteBlockLocked(rendered string, suppressSeparator bool) {
	if strings.TrimSpace(rendered) == "" {
		return
	}
	if !suppressSeparator && c.completeBlockOutput {
		fmt.Fprintln(c.writer)
	}
	fmt.Fprintln(c.writer, rendered)
	c.completeBlockOutput = true
}

func (c *chatInteractionCoordinator) shouldAdvanceAfterPromptLocked() bool {
	if c == nil {
		return false
	}
	if c.promptAdvanceFn != nil {
		return c.promptAdvanceFn()
	}
	if c.writer != os.Stdout {
		return true
	}
	stdinInfo, err := os.Stdin.Stat()
	if err == nil && stdinInfo.Mode()&os.ModeCharDevice == 0 {
		return true
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return stdoutInfo.Mode()&os.ModeCharDevice == 0
}

func (c *chatInteractionCoordinator) writeIndentedStreamingDeltaLocked(delta, indent string, rendered *bool, trailingLF *bool) {
	if delta == "" {
		return
	}
	c.completeBlockOutput = false
	delta = ui.SanitizeTerminalText(delta)
	atLineStart := !*rendered || *trailingLF
	for _, r := range []rune(delta) {
		if atLineStart && r != '\n' {
			fmt.Fprint(c.writer, indent)
			atLineStart = false
			*rendered = true
		}
		c.writeStreamingDeltaLocked(string(r))
		if r == '\n' {
			atLineStart = true
		}
	}
	*trailingLF = atLineStart
	if !*rendered && delta != "" {
		*rendered = true
	}
}

func (c *chatInteractionCoordinator) writeStreamingDeltaLocked(delta string) {
	if delta == "" {
		return
	}
	if c.writer != os.Stdout || c.streamRuneDelay <= 0 {
		fmt.Fprint(c.writer, delta)
		return
	}
	runes := []rune(delta)
	if len(runes) <= 1 {
		fmt.Fprint(c.writer, delta)
		return
	}
	delay := c.streamRuneDelay
	if c.maxChunkDelay > 0 {
		maxPerRune := c.maxChunkDelay / time.Duration(len(runes))
		if maxPerRune > 0 && maxPerRune < delay {
			delay = maxPerRune
		}
	}
	if delay <= 0 {
		fmt.Fprint(c.writer, delta)
		return
	}
	for _, r := range runes {
		fmt.Fprint(c.writer, string(r))
		time.Sleep(delay)
	}
}

type assistantStreamMode int

const (
	assistantStreamModeUnknown assistantStreamMode = iota
	assistantStreamModeText
	assistantStreamModeMarkdown
)

func looksLikeStreamingMarkdown(content string) bool {
	trimmed := strings.TrimLeft(content, " \t")
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "##") || strings.HasPrefix(trimmed, "> ") || strings.HasPrefix(trimmed, "```") {
		return true
	}
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
		return true
	}
	if strings.Contains(content, "\n# ") || strings.Contains(content, "\n##") || strings.Contains(content, "\n- ") || strings.Contains(content, "\n* ") || strings.Contains(content, "\n> ") || strings.Contains(content, "```") {
		return true
	}
	if strings.Contains(content, "**") || strings.Contains(content, "`") {
		return true
	}
	return false
}

func looksLikeStreamingMarkdownLead(content string) bool {
	trimmed := strings.TrimLeft(content, " \t")
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ">") {
		return true
	}
	if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "`") || strings.HasPrefix(trimmed, "|") {
		return true
	}
	return false
}

func shouldStartTextStreaming(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, ".!?。！？") {
		return true
	}
	return len([]rune(trimmed)) >= 32
}

func renderedLineCount(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func (c *chatInteractionCoordinator) SchedulePromptRedraw() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	c.promptSeq++
	seq := c.promptSeq
	delay := c.promptDelay
	c.mu.Unlock()

	time.AfterFunc(delay, func() {
		if !shouldDisplayInteractivePrompt(c.session) {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if seq != c.promptSeq {
			return
		}
		if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
			return
		}
		fmt.Fprint(c.writer, formatSessionUserPrompt(c.session))
		c.promptVisible = true
	})
}
