package commands

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
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

	mu                      sync.Mutex
	promptVisible           bool
	promptInput             string
	promptPasteActive       bool
	thinkingActive          bool
	streamingActive         bool
	streamRendered          bool
	streamMode              assistantStreamMode
	streamRenderedPrefixLen int
	streamTrailingLF        bool
	streamLines             int
	streamDisplayLines      int
	streamBuffer            strings.Builder
	streamRuneDelay         time.Duration
	maxChunkDelay           time.Duration
	promptDelay             time.Duration
	promptSeq               uint64
	promptAdvanceFn         func() bool
	liveStreamFn            func() bool

	reasoningActive     bool
	reasoningRendered   bool
	reasoningTrailingLF bool
	reasoningMeta       string
	reasoningBuffer     strings.Builder
	completeBlockOutput bool
	shutdown            bool
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
	if c.shutdown {
		return
	}
	c.surface = surface
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
}

func (c *chatInteractionCoordinator) SupportsLiveStream() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return false
	}
	return c.supportsLiveStreamLocked()
}

func (c *chatInteractionCoordinator) supportsLiveStreamLocked() bool {
	if c == nil || c.shutdown || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
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
	if c.shutdown {
		return
	}
	c.promptSeq++
	c.promptInput = ""
	c.promptPasteActive = false
	if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
		return
	}
	c.writeTextLocked(formatSessionUserPrompt(c.session))
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
	return ui.UserPromptText(attachmentCount)
}

func (c *chatInteractionCoordinator) updateSurfaceStatusLocked(state string) {
	if c == nil || c.surface == nil || c.shutdown {
		return
	}
	c.surface.SetStatusLine(buildChatSurfaceStatusLine(c.session, state))
}

func (c *chatInteractionCoordinator) writeTextLocked(text string) {
	if c == nil || c.writer == nil || text == "" {
		return
	}
	_, _ = ui.WriteTerminalText(c.writer, text)
}

func (c *chatInteractionCoordinator) writeLineLocked(text string) {
	if c == nil || c.writer == nil {
		return
	}
	_, _ = ui.WriteTerminalLine(c.writer, text)
}

func (c *chatInteractionCoordinator) writeFormatLocked(format string, args ...interface{}) {
	if c == nil || c.writer == nil || format == "" {
		return
	}
	_, _ = ui.WriteTerminalFormat(c.writer, format, args...)
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

func buildChatSurfaceStatusLine(session *ChatSession, state string) string {
	state = strings.TrimSpace(state)
	if state == "" {
		state = "Ready"
	}

	parts := make([]string, 0, 8)
	parts = append(parts, state)

	if session != nil {
		if model := compactStatusValue(strings.TrimSpace(session.Model), 28); model != "" {
			parts = append(parts, "model "+model)
		}

		reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
		if reasoningEffort == "" {
			reasoningEffort = "-"
		}
		parts = append(parts, "reasoning_effort "+compactStatusValueOrDash(reasoningEffort, 12))

		if budget := resolveSharedChatPromptBudget(session); budget.ActiveTurnMaxTokens > 0 || budget.ModelCapabilityMaxContextTokens > 0 || budget.ProviderContextLimit > 0 || session.ContextWindowTokenCount > 0 || session.ContextTokenCount > 0 || len(session.Messages) > 0 {
			if ctxSummary := formatChatContextWindowSummary(session, budget); ctxSummary != "" {
				parts = append(parts, "ctx "+ctxSummary)
			}
		}

		if cwd := resolveChatStatusCurrentDirectory(session); cwd != "" {
			parts = append(parts, "cwd "+cwd)
		}

		provider := strings.TrimSpace(session.ProviderName)
		if provider == "" {
			provider = strings.TrimSpace(session.Provider.GetProtocol())
		}
		if provider = compactStatusValue(provider, 20); provider != "" {
			parts = append(parts, provider)
		}
		if messageCount := resolveChatStatusMessageCount(session); messageCount > 0 {
			parts = append(parts, "msgs "+compactStatusCount(messageCount))
		}
	}

	return strings.Join(parts, " | ")
}

func resolveChatStatusCurrentDirectory(session *ChatSession) string {
	if session != nil {
		if root := strings.TrimSpace(session.ProfileRoot); root != "" {
			if filepath.IsAbs(root) {
				return root
			}
			if cwd, err := os.Getwd(); err == nil {
				return filepath.Clean(filepath.Join(cwd, root))
			}
			return root
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

func formatChatContextWindowSummary(session *ChatSession, budget sharedChatPromptBudget) string {
	totalWindow := budget.ModelCapabilityMaxContextTokens
	if session != nil && session.ContextWindowTokenCount > 0 {
		totalWindow = session.ContextWindowTokenCount
	}
	if totalWindow <= 0 && budget.ProviderContextLimit > 0 {
		totalWindow = budget.ProviderContextLimit
	}
	if totalWindow <= 0 {
		totalWindow = budget.ActiveTurnMaxTokens
	}
	usedTokens := resolveChatStatusContextUsedTokens(session)
	if totalWindow <= 0 {
		if usedTokens > 0 {
			return fmt.Sprintf("used %d", usedTokens)
		}
		return ""
	}

	percent := 0
	if usedTokens > 0 {
		percent = int(math.Round(float64(usedTokens) * 100 / float64(totalWindow)))
		if percent < 0 {
			percent = 0
		}
	}
	return fmt.Sprintf("%d used %d %d%%", totalWindow, usedTokens, percent)
}

func resolveChatStatusUsedTokens(session *ChatSession) int {
	if session == nil {
		return 0
	}
	if session.ContextTokenCount > 0 {
		return session.ContextTokenCount
	}
	return resolveChatContextSnapshotTokens(session, nil)
}

func resolveChatStatusMessageCount(session *ChatSession) int {
	if session == nil {
		return 0
	}
	if session.StatusMessageCount > 0 {
		return session.StatusMessageCount
	}
	if count := countChatStatusMessages(session.Messages); count > 0 {
		return count
	}
	return session.MsgCount
}

func applyChatStatusMessageCount(session *ChatSession, count int, forceRefresh bool) {
	if session == nil || count <= 0 {
		return
	}
	changed := session.StatusMessageCount != count
	session.StatusMessageCount = count
	if (changed || forceRefresh) && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func compactStatusValue(value string, maxWidth int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if maxWidth > 0 && ui.DisplayWidth(value) > maxWidth {
		return truncateStatusValue(value, maxWidth)
	}
	return value
}

func compactStatusValueOrDash(value string, maxWidth int) string {
	value = compactStatusValue(value, maxWidth)
	if value == "" {
		return "-"
	}
	return value
}

func compactStatusCount(value int) string {
	switch {
	case value < 1000:
		return fmt.Sprintf("%d", value)
	case value < 10_000:
		return trimStatusFloat(fmt.Sprintf("%.1f", float64(value)/1000)) + "k"
	case value < 1_000_000:
		return fmt.Sprintf("%dk", value/1000)
	case value < 10_000_000:
		return trimStatusFloat(fmt.Sprintf("%.1f", float64(value)/1_000_000)) + "m"
	default:
		return fmt.Sprintf("%dm", value/1_000_000)
	}
}

func trimStatusFloat(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, "0")
	value = strings.TrimRight(value, ".")
	if value == "" {
		return "0"
	}
	return value
}

func truncateStatusValue(line string, width int) string {
	if width <= 0 {
		width = 80
	}
	if ui.DisplayWidth(line) <= width {
		return line
	}
	if width <= 3 {
		return ""
	}
	var builder strings.Builder
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

func (c *chatInteractionCoordinator) StartThinking() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.beginMessageLocked() {
		return
	}
	c.thinkingActive = true
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
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
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
}

func (c *chatInteractionCoordinator) RenderAssistant(response string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	promptWasVisible := c.promptVisible
	if !c.beginMessageLocked() {
		return
	}
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
		if !c.beginMessageLocked() {
			return
		}
		c.reasoningActive = true
		c.reasoningRendered = false
		c.reasoningTrailingLF = false
		c.reasoningBuffer.Reset()
		c.writeLineLocked(ui.FormatAssistantSupplementBlock(chatToolDivider("reasoning")))
		if meta := chatReasoningMetaLine(block); meta != "" {
			c.reasoningMeta = meta
			c.writeLineLocked(ui.FormatAssistantSupplementBlock(meta))
		} else {
			c.reasoningMeta = ""
		}
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
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
			if !c.beginMessageLocked() {
				return
			}
		}
		c.streamingActive = true
		c.updateSurfaceStatusLocked("Streaming")
		c.streamRendered = false
		c.streamMode = assistantStreamModeUnknown
		c.streamRenderedPrefixLen = 0
		c.streamTrailingLF = false
		c.streamLines = 0
		c.streamBuffer.Reset()
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	}
	delta = normalizeAssistantStreamDelta(c.streamBuffer.String(), delta)
	if delta == "" {
		return
	}
	previousContent := c.streamBuffer.String()
	previousMode := c.streamMode
	c.streamBuffer.WriteString(delta)
	c.streamLines += strings.Count(delta, "\n")
	if c.streamMode != assistantStreamModeMarkdown {
		nextMode := c.classifyAssistantStreamModeLocked(c.streamBuffer.String())
		if nextMode == assistantStreamModeMarkdown {
			if previousMode != assistantStreamModeMarkdown && c.streamRendered && c.streamRenderedPrefixLen == 0 {
				c.streamRenderedPrefixLen = len(previousContent)
			}
			c.streamMode = assistantStreamModeMarkdown
		} else if c.streamMode == assistantStreamModeUnknown && nextMode == assistantStreamModeText {
			c.streamMode = assistantStreamModeText
		}
	}

	if c.supportsLiveStreamLocked() && !c.reasoningActive {
		if c.streamMode == assistantStreamModeMarkdown {
			return
		}
		if !c.streamRendered {
			c.writeIndentedStreamingDeltaLocked(c.streamBuffer.String(), ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
			return
		}
		c.writeIndentedStreamingDeltaLocked(delta, ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
	}
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

func (c *chatInteractionCoordinator) classifyAssistantStreamModeLocked(content string) assistantStreamMode {
	if c == nil {
		return assistantStreamModeUnknown
	}
	if strings.TrimSpace(content) == "" {
		return assistantStreamModeUnknown
	}
	if c.session != nil && c.session.Formatter != nil && c.session.Formatter.IsMarkdown(content) {
		return assistantStreamModeMarkdown
	}
	if looksLikeStreamingMarkdown(content) || looksLikeStreamingMarkdownLead(content) {
		return assistantStreamModeMarkdown
	}
	if !shouldStartTextStreaming(content) {
		return assistantStreamModeUnknown
	}
	return assistantStreamModeText
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
	if c.streamMode != assistantStreamModeMarkdown && c.classifyAssistantStreamModeLocked(finalContent) == assistantStreamModeMarkdown {
		if c.streamRendered && c.streamRenderedPrefixLen == 0 {
			c.streamRenderedPrefixLen = len(c.streamBuffer.String())
		}
		c.streamMode = assistantStreamModeMarkdown
	}
	if c.streamMode == assistantStreamModeMarkdown {
		c.renderFormattedAssistantStreamLocked(finalContent)
		c.resetStreamLocked()
		return true
	}
	if c.supportsLiveStreamLocked() {
		suffix := resolveStreamCompletionSuffix(c.streamBuffer.String(), finalContent)
		if suffix != "" {
			c.writeIndentedStreamingDeltaLocked(suffix, ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
			c.streamBuffer.WriteString(suffix)
		}
		if c.streamRendered && !c.streamTrailingLF {
			c.writeLineLocked("")
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
		if c.streamMode == assistantStreamModeMarkdown {
			content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
			c.renderFormattedAssistantStreamLocked(content)
			c.resetStreamLocked()
			return
		}
		if c.streamRendered && !c.streamTrailingLF {
			c.writeLineLocked("")
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
	if !c.beginMessageLocked() {
		return
	}
	c.writeCompleteBlockLocked(ui.FormatAssistantSupplementBlock(line), promptWasVisible)
}

func (c *chatInteractionCoordinator) RenderError(err error) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	promptWasVisible := c.promptVisible
	if !c.beginMessageLocked() {
		return
	}
	c.writeCompleteBlockLocked(ui.FormatErrorMessage(fmt.Sprintf("操作错误: %v", err)), promptWasVisible)
}

func (c *chatInteractionCoordinator) ClearPrompt() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	if c.promptVisible {
		c.clearVisiblePromptLocked()
	}
	c.promptSeq++
	c.promptVisible = false
	c.promptInput = ""
	c.promptPasteActive = false
}

func (c *chatInteractionCoordinator) ResetPromptState() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.promptSeq++
	c.promptVisible = false
	c.promptInput = ""
	c.promptPasteActive = false
}

func (c *chatInteractionCoordinator) SetPromptInput(input string) {
	c.SetPromptInputSnapshot(ui.LineEditorSnapshot{Text: input})
}

func (c *chatInteractionCoordinator) SetPromptInputSnapshot(snapshot ui.LineEditorSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	input := strings.ReplaceAll(snapshot.Text, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	c.promptInput = input
	c.promptPasteActive = snapshot.PasteActive
}

func (c *chatInteractionCoordinator) IsPromptPasteActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.promptPasteActive
}

func (c *chatInteractionCoordinator) DebugSummary() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	parts := []string{
		fmt.Sprintf("prompt_visible=%t", c.promptVisible),
		fmt.Sprintf("prompt_paste_active=%t", c.promptPasteActive),
		fmt.Sprintf("thinking_active=%t", c.thinkingActive),
		fmt.Sprintf("streaming_active=%t", c.streamingActive),
		fmt.Sprintf("reasoning_active=%t", c.reasoningActive),
		fmt.Sprintf("complete_block_output=%t", c.completeBlockOutput),
		fmt.Sprintf("shutdown=%t", c.shutdown),
	}
	return strings.Join(parts, " ")
}

func (c *chatInteractionCoordinator) ResetRunState() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.completeBlockOutput = false
	c.updateSurfaceStatusLocked("Ready")
}

// Shutdown 停止所有后续 prompt 重绘和状态栏更新，供最终退出使用。
func (c *chatInteractionCoordinator) Shutdown() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.shutdown = true
	c.promptSeq++
	c.promptVisible = false
	c.promptInput = ""
	c.promptPasteActive = false
	c.thinkingActive = false
	c.streamingActive = false
	c.reasoningActive = false
	c.completeBlockOutput = false
	c.surface = nil
}

func (c *chatInteractionCoordinator) beginMessageLocked() bool {
	if c == nil || c.shutdown {
		return false
	}
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
	return true
}

func (c *chatInteractionCoordinator) clearThinkingLocked() {
}

// flushStreamLocked outputs any buffered streaming content before the stream
// is interrupted by another message (e.g. thinking, async line, new response).
// This prevents text from being silently swallowed when a ReAct loop's
// intermediate assistant deltas are never finalized via FinalizeAssistantDelta.
func (c *chatInteractionCoordinator) flushStreamLocked() {
	if c.supportsLiveStreamLocked() {
		if c.streamMode == assistantStreamModeMarkdown {
			content := c.streamBuffer.String()
			if strings.TrimSpace(content) == "" {
				if !c.streamTrailingLF {
					c.writeLineLocked("")
				}
				return
			}
			content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
			c.renderFormattedAssistantStreamLocked(content)
			return
		}
		c.renderBufferedAssistantStreamLocked()
		if c.streamRendered && !c.streamTrailingLF {
			c.writeLineLocked("")
		}
		return
	}
	content := c.streamBuffer.String()
	if strings.TrimSpace(content) == "" {
		if !c.streamTrailingLF {
			c.writeLineLocked("")
		}
		return
	}
	content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
	formatted := content
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(content)
	}
	c.writeLineLocked(ui.FormatAssistantMessage(formatted))
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
		c.writeLineLocked(ui.FormatAssistantSupplementBlock(strings.Join(lines, "\n")))
	}
}

func (c *chatInteractionCoordinator) clearVisiblePromptLocked() {
	if c == nil {
		return
	}
	promptText := promptDisplayText(c.session)
	promptLine := promptText + c.promptInput
	if c.shouldAdvanceAfterPromptLocked() {
		c.writeTextLocked("\r\n")
		c.promptInput = ""
		return
	}
	termWidth := ui.GetTerminalWidth()
	if termWidth <= 0 {
		termWidth = 80
	}
	rows := interactivePromptDisplayRows(promptLine, termWidth)
	// 当 prompt+输入已经折行时，先把光标上移到输入起始行，
	// 再逐行清理 prompt/input 曾占用的区域，避免清掉固定底部状态栏。
	var builder strings.Builder
	if rows > 1 {
		fmt.Fprintf(&builder, "\x1b[%dA", rows-1)
	}
	builder.WriteString(clearPromptDisplayRowsSequence(rows))
	c.writeTextLocked(builder.String())
	c.promptInput = ""
}

func clearPromptDisplayRows(writer io.Writer, rows int) {
	if writer == nil {
		return
	}
	_, _ = ui.WriteTerminalText(writer, clearPromptDisplayRowsSequence(rows))
}

func clearPromptDisplayRowsSequence(rows int) string {
	if rows < 1 {
		rows = 1
	}
	var builder strings.Builder
	builder.WriteString("\r")
	for i := 0; i < rows; i++ {
		builder.WriteString("\x1b[K")
		if i < rows-1 {
			builder.WriteString("\x1b[1B\r")
		}
	}
	if rows > 1 {
		fmt.Fprintf(&builder, "\x1b[%dA\r", rows-1)
	}
	return builder.String()
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
		c.writeLineLocked("")
	}
	c.writeLineLocked(ui.FormatAssistantSupplementBlock(chatToolDivider("end reasoning")))
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
	if c.streamMode == assistantStreamModeMarkdown {
		return
	}
	c.writeIndentedStreamingDeltaLocked(content, ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
	c.completeBlockOutput = false
}

func (c *chatInteractionCoordinator) renderFormattedAssistantStreamLocked(content string) {
	if c == nil {
		return
	}
	if c.streamRendered && c.streamRenderedPrefixLen > 0 {
		suffix := c.unrenderedAssistantStreamSuffixLocked(content)
		if strings.TrimSpace(suffix) == "" {
			if !c.streamTrailingLF {
				c.writeLineLocked("")
			}
			c.completeBlockOutput = true
			return
		}
		formatted := suffix
		if c.session != nil && c.session.Formatter != nil {
			formatted = c.session.Formatter.Format(suffix)
		}
		inlineContinuation := !c.streamTrailingLF && !strings.HasPrefix(suffix, "\n") && !strings.Contains(formatted, "\n")
		formatted = strings.TrimLeft(formatted, "\n")
		if strings.TrimSpace(formatted) == "" {
			if !c.streamTrailingLF {
				c.writeLineLocked("")
			}
			c.completeBlockOutput = true
			return
		}
		if inlineContinuation {
			c.writeLineLocked(formatted)
			c.completeBlockOutput = true
			return
		}
		if !c.streamTrailingLF {
			c.writeLineLocked("")
		}
		c.writeLineLocked(ui.IndentAssistantContent(formatted))
		c.completeBlockOutput = true
		return
	}

	formatted := content
	if c.session != nil && c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(content)
	}
	c.writeCompleteBlockLocked(ui.FormatAssistantMessage(formatted), false)
}

func (c *chatInteractionCoordinator) unrenderedAssistantStreamSuffixLocked(content string) string {
	if c == nil || c.streamRenderedPrefixLen <= 0 {
		return content
	}
	buffered := c.streamBuffer.String()
	if c.streamRenderedPrefixLen > len(buffered) {
		return content
	}
	prefix := buffered[:c.streamRenderedPrefixLen]
	if strings.HasPrefix(content, prefix) {
		return content[c.streamRenderedPrefixLen:]
	}
	if strings.HasPrefix(buffered, prefix) {
		return buffered[c.streamRenderedPrefixLen:]
	}
	return content
}

func (c *chatInteractionCoordinator) resetStreamLocked() {
	c.streamingActive = false
	c.streamRendered = false
	c.streamMode = assistantStreamModeUnknown
	c.streamRenderedPrefixLen = 0
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
		c.writeLineLocked("")
	}
	c.writeLineLocked(rendered)
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
			c.writeTextLocked(indent)
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
		c.writeTextLocked(delta)
		return
	}
	runes := []rune(delta)
	if len(runes) <= 1 {
		c.writeTextLocked(delta)
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
		c.writeTextLocked(delta)
		return
	}
	for _, r := range runes {
		c.writeTextLocked(string(r))
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
	if strings.TrimSpace(content) == "" {
		return false
	}
	if strings.Contains(content, "```") || strings.Contains(content, "**") || strings.Count(content, "`") >= 2 {
		return true
	}
	for _, line := range strings.Split(content, "\n") {
		if isStreamingMarkdownLine(strings.TrimLeft(line, " \t")) {
			return true
		}
	}
	return false
}

func looksLikeStreamingMarkdownLead(content string) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		if isStreamingMarkdownLeadLine(strings.TrimLeft(line, " \t")) {
			return true
		}
	}
	return false
}

func isStreamingMarkdownLine(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
		return true
	}
	if strings.HasPrefix(trimmed, "> ") || strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
		return true
	}
	if isStreamingOrderedListLine(trimmed) {
		return true
	}
	return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(strings.TrimSpace(trimmed), "|")
}

func isStreamingMarkdownLeadLine(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ">") {
		return true
	}
	if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "`") || strings.HasPrefix(trimmed, "|") {
		return true
	}
	return isStreamingOrderedListLeadLine(trimmed)
}

func isStreamingOrderedListLine(trimmed string) bool {
	dot := orderedListDotIndex(trimmed)
	return dot > 0 && dot+1 < len(trimmed) && (trimmed[dot+1] == ' ' || trimmed[dot+1] == '\t')
}

func isStreamingOrderedListLeadLine(trimmed string) bool {
	dot := orderedListDotIndex(trimmed)
	if dot <= 0 {
		return false
	}
	return dot+1 == len(trimmed) || trimmed[dot+1] == ' ' || trimmed[dot+1] == '\t'
}

func orderedListDotIndex(trimmed string) int {
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(trimmed) || trimmed[i] != '.' {
		return -1
	}
	return i
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
	if c.shutdown {
		c.mu.Unlock()
		return
	}
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
		if c.shutdown {
			return
		}
		if seq != c.promptSeq {
			return
		}
		if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
			return
		}
		c.writeTextLocked(formatSessionUserPrompt(c.session))
		c.promptVisible = true
	})
}
