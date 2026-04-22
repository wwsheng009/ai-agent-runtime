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

	mu                 sync.Mutex
	promptVisible      bool
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
	if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
		return
	}
	fmt.Fprint(c.writer, ui.FormatUserPrompt())
	c.promptVisible = true
}

func (c *chatInteractionCoordinator) StartThinking() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginMessageLocked()
	c.thinkingActive = true
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
	if c.promptVisible && c.shouldAdvanceAfterPromptLocked() {
		fmt.Fprintln(c.writer)
	}
	c.promptSeq++
	c.promptVisible = false
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
	if c.shouldAdvanceAfterPromptLocked() {
		fmt.Fprint(c.writer, "\r\n")
		return
	}
	clearWidth := ui.DisplayWidth(promptDisplayText())
	if clearWidth <= 0 {
		clearWidth = len([]rune(promptDisplayText()))
	}
	fmt.Fprint(c.writer, "\r"+strings.Repeat(" ", clearWidth)+"\r")
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
}

func (c *chatInteractionCoordinator) resetReasoningLocked() {
	c.reasoningActive = false
	c.reasoningRendered = false
	c.reasoningTrailingLF = false
	c.reasoningMeta = ""
	c.reasoningBuffer.Reset()
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

func promptDisplayText() string {
	theme := ui.GetTheme(ui.ThemeAuto)
	return theme.UserIcon + "你> "
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
		fmt.Fprint(c.writer, ui.FormatUserPrompt())
		c.promptVisible = true
	})
}
