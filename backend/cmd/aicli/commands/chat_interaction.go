package commands

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
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
	if c.promptVisible || c.thinkingActive || c.streamingActive {
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
	fmt.Fprint(c.writer, ui.IndentAssistantContent("助手正在思考..."))
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
	clearWidth := ui.DisplayWidth(ui.IndentAssistantContent("助手正在思考..."))
	fmt.Fprint(c.writer, "\r"+strings.Repeat(" ", clearWidth)+"\r")
	c.thinkingActive = false
}

func (c *chatInteractionCoordinator) RenderAssistant(response string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginMessageLocked()
	response = sanitizeInteractiveAsyncTeamLaunchResponse(response)
	formatted := response
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(response)
	}
	fmt.Fprintln(c.writer, ui.FormatAssistantMessage(formatted))
}

func (c *chatInteractionCoordinator) RenderAssistantDelta(delta string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || delta == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.streamingActive {
		c.beginMessageLocked()
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
	formatted := finalContent
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(finalContent)
	}
	fmt.Fprintln(c.writer, ui.FormatAssistantMessage(formatted))
	c.resetStreamLocked()
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
	if content != "" {
		content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
		formatted := content
		if c.session.Formatter != nil {
			formatted = c.session.Formatter.Format(content)
		}
		fmt.Fprintln(c.writer, ui.FormatAssistantMessage(formatted))
		c.resetStreamLocked()
		return
	}
	c.resetStreamLocked()
}

func (c *chatInteractionCoordinator) RenderAsyncLine(line string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || strings.TrimSpace(line) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginMessageLocked()
	fmt.Fprintln(c.writer, ui.IndentAssistantContent(line))
}

func (c *chatInteractionCoordinator) RenderError(err error) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.beginMessageLocked()
	fmt.Fprintln(c.writer, ui.FormatErrorMessage(fmt.Sprintf("操作错误: %v", err)))
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
		fmt.Fprint(c.writer, "\r   \r")
		c.thinkingActive = false
	}
	if c.streamingActive {
		if !c.streamTrailingLF {
			fmt.Fprintln(c.writer)
		}
		c.resetStreamLocked()
	}
	if c.promptVisible {
		c.clearVisiblePromptLocked()
		c.promptVisible = false
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

func (c *chatInteractionCoordinator) resetStreamLocked() {
	c.streamingActive = false
	c.streamRendered = false
	c.streamMode = assistantStreamModeUnknown
	c.streamTrailingLF = false
	c.streamLines = 0
	c.streamDisplayLines = 0
	c.streamBuffer.Reset()
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
		if c.promptVisible || c.thinkingActive || c.streamingActive {
			return
		}
		fmt.Fprint(c.writer, ui.FormatUserPrompt())
		c.promptVisible = true
	})
}
