package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatInteractionCoordinator struct {
	session *ChatSession
	writer  io.Writer
	surface *ui.FixedBottomSurface

	mu                      sync.Mutex
	promptVisible           bool
	promptInput             string
	promptCursor            int
	promptRenderedOnSurface bool
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
	waitingActive           bool
	agentStage              chatAgentStage
	agentStageDetail        string
	inputMode               chatInputMode
	inputModeBase           chatInputMode
	inputLeaseSeq           uint64
	inputLease              *chatInputModeLease

	reasoningActive        bool
	reasoningRendered      bool
	reasoningTrailingLF    bool
	reasoningMeta          string
	reasoningBuffer        strings.Builder
	completeBlockOutput    bool
	lastCompletedAsyncLine bool
	promptAfterBlockGap    bool
	shutdown               bool
}

// chatAgentStage describes the agent-specific phase that is more precise than
// the legacy Ready/Waiting/Thinking/Streaming activity flags.
type chatAgentStage string

// chatInputMode describes what the active prompt will do with the next input.
// It is intentionally independent from the agent stage: configuration and
// other local modals may own input while the agent itself is idle.
type chatInputMode string

// chatInputModeLease gives each modal prompt a distinct input owner. A lease
// may be released out of order without changing the mode owned by a newer one.
type chatInputModeLease struct {
	generation uint64
	mode       chatInputMode
	previous   *chatInputModeLease
	released   bool
}

const (
	chatAgentStageIdle             chatAgentStage = ""
	chatAgentStagePlanning         chatAgentStage = "planning"
	chatAgentStageToolRunning      chatAgentStage = "tool_running"
	chatAgentStageAwaitingApproval chatAgentStage = "awaiting_approval"
	chatAgentStageAwaitingAnswer   chatAgentStage = "awaiting_answer"
	chatAgentStageStopping         chatAgentStage = "stopping"
	chatAgentStageCompleted        chatAgentStage = "completed"
	chatAgentStageFailed           chatAgentStage = "failed"
)

const (
	chatInputModeChat         chatInputMode = ""
	chatInputModeApproval     chatInputMode = "approval_decision"
	chatInputModeAnswer       chatInputMode = "question_answer"
	chatInputModeSelection    chatInputMode = "selection"
	chatInputModeConfirmation chatInputMode = "confirmation"
	chatInputModeSecret       chatInputMode = "secret_input"
	chatInputModePanel        chatInputMode = "panel_navigation"
)

func normalizeChatInputMode(mode chatInputMode) chatInputMode {
	switch chatInputMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case chatInputModeApproval:
		return chatInputModeApproval
	case chatInputModeAnswer:
		return chatInputModeAnswer
	case chatInputModeSelection:
		return chatInputModeSelection
	case chatInputModeConfirmation:
		return chatInputModeConfirmation
	case chatInputModeSecret:
		return chatInputModeSecret
	case chatInputModePanel:
		return chatInputModePanel
	default:
		return chatInputModeChat
	}
}

func normalizeChatAgentStage(stage chatAgentStage) chatAgentStage {
	switch chatAgentStage(strings.ToLower(strings.TrimSpace(string(stage)))) {
	case chatAgentStagePlanning:
		return chatAgentStagePlanning
	case chatAgentStageToolRunning:
		return chatAgentStageToolRunning
	case chatAgentStageAwaitingApproval:
		return chatAgentStageAwaitingApproval
	case chatAgentStageAwaitingAnswer:
		return chatAgentStageAwaitingAnswer
	case chatAgentStageStopping:
		return chatAgentStageStopping
	case chatAgentStageCompleted:
		return chatAgentStageCompleted
	case chatAgentStageFailed:
		return chatAgentStageFailed
	default:
		return chatAgentStageIdle
	}
}

func chatAgentStageSurfaceLabel(stage chatAgentStage) string {
	switch normalizeChatAgentStage(stage) {
	case chatAgentStagePlanning:
		return "Planning"
	case chatAgentStageToolRunning:
		return "Tool running"
	case chatAgentStageAwaitingApproval:
		return "Awaiting approval"
	case chatAgentStageAwaitingAnswer:
		return "Awaiting answer"
	case chatAgentStageStopping:
		return "Stopping"
	case chatAgentStageCompleted, chatAgentStageFailed:
		// Codex-aligned: once a task is no longer running, the composer surface
		// shows Ready rather than sticky Completed/Failed.
		return ""
	default:
		return ""
	}
}

func chatAgentStageIsTerminal(stage chatAgentStage) bool {
	stage = normalizeChatAgentStage(stage)
	return stage == chatAgentStageCompleted || stage == chatAgentStageFailed
}

func chatAgentStageBlocksReady(stage chatAgentStage) bool {
	stage = normalizeChatAgentStage(stage)
	return stage != chatAgentStageIdle && !chatAgentStageIsTerminal(stage)
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

func (c *chatInteractionCoordinator) shouldLiveStreamOutputLocked() bool {
	return c.supportsLiveStreamLocked() && !c.surfaceOutputActiveLocked()
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
	c.promptPasteActive = false
	if c.promptVisible || c.thinkingActive || c.streamingActive || c.reasoningActive {
		return
	}
	prompt := formatSessionUserPrompt(c.session)
	if c.writer == os.Stdout && c.surface != nil && c.surface.ShowPrompt(prompt) {
		c.promptVisible = true
		c.promptRenderedOnSurface = true
		c.preparePromptGapLocked(false)
		if c.promptInput != "" {
			rows := c.currentPromptDisplayRowsLocked()
			cursorRow, cursorCol := c.currentPromptCursorPositionLocked()
			c.surface.SetPromptInputState(prompt, c.promptInput, rows, cursorRow, cursorCol)
		}
		return
	}
	c.promptRenderedOnSurface = false
	c.preparePromptGapLocked(true)
	c.writeTextLocked(prompt)
	if c.promptInput != "" {
		c.writeTextLocked(c.promptInput)
	}
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
	if c == nil || c.shutdown {
		return
	}
	if c.session != nil && c.session.TitleNotifier != nil {
		c.session.TitleNotifier.SetBaseState(chatTitleStateForSurface(chatSurfaceTitleState(state)))
	}
	if c.surface != nil {
		c.surface.SetStatusLine(buildChatSurfaceStatusLineForWidthAndInputMode(c.session, state, ui.GetTerminalWidth(), c.inputMode))
		c.surface.SetPromptNoticeLine(buildChatPromptNoticeLineForWidth(c.session, state, ui.GetTerminalWidth()))
	}
}

func chatSurfaceTitleState(state string) string {
	normalized := strings.ToLower(strings.TrimSpace(state))
	if strings.HasPrefix(normalized, "tool ") {
		return "running"
	}
	switch normalized {
	case "planning", "tool running":
		return "running"
	case "stopping":
		return "stopping"
	case "awaiting approval", "awaiting answer":
		return "waiting"
	case "completed", "failed":
		return "ready"
	default:
		return state
	}
}

func buildChatPromptNoticeLine(session *ChatSession) string {
	return buildChatPromptNoticeLineForWidth(session, "Ready", ui.GetTerminalWidth())
}

func refreshChatComposerContext(session *ChatSession) {
	if session != nil && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

func buildChatPromptNoticeLineForWidth(session *ChatSession, state string, width int) string {
	if session == nil {
		return ""
	}
	if width <= 0 {
		width = 80
	}

	queuedCount, _ := queuedInteractiveInputState(session)
	lines := make([]string, 0, 3)
	if queuedCount > 0 {
		lines = append(lines, buildQueuedInputContextLine(queuedCount, width))
	}
	if len(session.ImagePaths) > 0 && !chatSurfaceStateIsRunning(state) {
		lines = append(lines, buildAttachmentContextLine(session.ImagePaths, width))
	}

	previewLimit := 0
	if queuedCount > 0 && width >= 64 {
		previewLimit = 3 - len(lines)
		if width < 100 && previewLimit > 1 {
			previewLimit = 1
		}
	}
	if previewLimit > 0 {
		for _, preview := range queuedInteractiveInputPreviewLines(session, previewLimit) {
			if len(lines) >= 3 {
				break
			}
			preview = compactPromptNoticeMessagePreviewForWidth(preview, width-4)
			if preview != "" {
				lines = append(lines, "  - "+preview)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func buildQueuedInputContextLine(count int, width int) string {
	candidates := []string{
		fmt.Sprintf("• 队列 %d：当前运行结束后发送；Esc 中断并提前处理（就绪后 /queue 管理）", count),
		fmt.Sprintf("• 队列 %d：运行后发送；Esc 可中断（就绪后 /queue）", count),
		fmt.Sprintf("• 队列 %d（就绪后 /queue）", count),
	}
	return firstPromptContextCandidateThatFits(candidates, width)
}

func buildAttachmentContextLine(paths []string, width int) string {
	count := len(paths)
	compact := fmt.Sprintf("• 图片附件 %d（/attach）", count)
	managed := fmt.Sprintf("• 待发送图片 %d（/attach 管理）", count)
	prefix := fmt.Sprintf("• 待发送图片 %d：", count)
	suffix := "（/attach remove N）"
	nameBudget := width - ui.DisplayWidth(prefix) - ui.DisplayWidth(suffix)
	detailed := ""
	if nameBudget >= 8 {
		detailed = prefix + compactAttachmentPathSummary(paths, nameBudget) + suffix
	}
	return firstPromptContextCandidateThatFits([]string{detailed, managed, compact}, width)
}

func firstPromptContextCandidateThatFits(candidates []string, width int) string {
	for _, candidate := range candidates {
		if candidate != "" && ui.DisplayWidth(candidate) <= width {
			return candidate
		}
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i] != "" {
			return truncateStatusValue(candidates[i], width)
		}
	}
	return ""
}

func queuedInteractiveInputPreviewLines(session *ChatSession, limit int) []string {
	if session == nil || session.InputQueue == nil {
		return nil
	}
	return session.InputQueue.queuedPreviewLines(limit)
}

func compactPromptNoticeMessagePreview(text string) string {
	return compactPromptNoticeMessagePreviewForWidth(text, 96)
}

func compactPromptNoticeMessagePreviewForWidth(text string, maxWidth int) string {
	text = strings.TrimSpace(normalizeQueuedInputLine(text))
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\n", " / ")
	text = strings.Join(strings.Fields(text), " ")
	if maxWidth <= 0 {
		return ""
	}
	return truncateStatusValue(text, maxWidth)
}

func compactAttachmentPathSummary(paths []string, maxWidth int) string {
	if len(paths) == 0 || maxWidth <= 0 {
		return ""
	}
	detailCount := len(paths)
	if detailCount > 3 {
		detailCount = 3
	}
	names := make([]string, 0, detailCount)
	for index, path := range paths[:detailCount] {
		name := filepath.Base(filepath.Clean(strings.TrimSpace(path)))
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = strings.TrimSpace(path)
		}
		if name == "" {
			name = "image"
		}
		names = append(names, fmt.Sprintf("[%d] %s", index+1, name))
	}

	for visible := len(names); visible >= 1; visible-- {
		summary := strings.Join(names[:visible], "、")
		if visible < len(paths) {
			summary += fmt.Sprintf("等%d个", len(paths))
		}
		if ui.DisplayWidth(summary) <= maxWidth {
			return summary
		}
	}
	return truncateStatusValue(names[0], maxWidth)
}

func (c *chatInteractionCoordinator) writeTextLocked(text string) {
	if c == nil || c.writer == nil || text == "" {
		return
	}
	if c.writeSurfaceOutputTextLocked(text) {
		return
	}
	_, _ = ui.WriteTerminalText(c.writer, text)
}

func (c *chatInteractionCoordinator) writeLineLocked(text string) {
	if c == nil || c.writer == nil {
		return
	}
	c.writeTextLocked(text + "\n")
}

func (c *chatInteractionCoordinator) writePromptGapLocked() {
	if c == nil || !c.completeBlockOutput {
		return
	}
	c.writeLineLocked("")
	c.completeBlockOutput = false
}

func (c *chatInteractionCoordinator) preparePromptGapLocked(writeGap bool) {
	if c == nil {
		return
	}
	if c.lastCompletedAsyncLine {
		c.promptAfterBlockGap = true
		c.completeBlockOutput = false
		return
	}
	c.promptAfterBlockGap = false
	if writeGap {
		c.writePromptGapLocked()
	}
}

func (c *chatInteractionCoordinator) writeFormatLocked(format string, args ...interface{}) {
	if c == nil || c.writer == nil || format == "" {
		return
	}
	if c.writer == os.Stdout && c.surface != nil {
		c.writeTextLocked(fmt.Sprintf(format, args...))
		return
	}
	_, _ = ui.WriteTerminalFormat(c.writer, format, args...)
}

func (c *chatInteractionCoordinator) writeSurfaceOutputTextLocked(text string) bool {
	if c == nil || c.writer != os.Stdout || c.surface == nil || text == "" || !c.surfaceOutputActiveLocked() {
		return false
	}
	_, _, handled := c.surface.WriteOutput(c.writer, text)
	return handled
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

// SetAgentStage switches the composer to an agent-specific run phase. Passing
// chatAgentStageIdle restores the state derived from the legacy activity flags.
func (c *chatInteractionCoordinator) SetAgentStage(stage chatAgentStage) {
	c.SetAgentStageDetail(stage, "")
}

// SetAgentStageDetail also exposes the active operation, such as a tool name,
// while keeping the stage itself available for narrow-terminal fallback.
func (c *chatInteractionCoordinator) SetAgentStageDetail(stage chatAgentStage, detail string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.agentStage = normalizeChatAgentStage(stage)
	c.agentStageDetail = compactStatusValue(strings.TrimSpace(detail), 48)
	if c.agentStage == chatAgentStageIdle {
		c.agentStageDetail = ""
	}
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
}

func (c *chatInteractionCoordinator) ClearAgentStage() {
	c.SetAgentStage(chatAgentStageIdle)
}

func (c *chatInteractionCoordinator) AgentStage() chatAgentStage {
	if c == nil {
		return chatAgentStageIdle
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agentStage
}

func (c *chatInteractionCoordinator) AgentStageDetail() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agentStageDetail
}

// SetInputMode forcefully replaces the composer input contract. Any outstanding
// modal leases are detached so their later cleanup cannot overwrite this mode.
func (c *chatInteractionCoordinator) SetInputMode(mode chatInputMode) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.inputModeBase = normalizeChatInputMode(mode)
	c.inputLease = nil
	c.inputMode = c.inputModeBase
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
}

func (c *chatInteractionCoordinator) acquireInputMode(mode chatInputMode) func() {
	if c == nil {
		return func() {}
	}
	mode = normalizeChatInputMode(mode)
	c.mu.Lock()
	if c.shutdown {
		c.mu.Unlock()
		return func() {}
	}
	c.inputLeaseSeq++
	lease := &chatInputModeLease{
		generation: c.inputLeaseSeq,
		mode:       mode,
		previous:   c.inputLease,
	}
	c.inputLease = lease
	c.inputMode = mode
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.releaseInputMode(lease)
		})
	}
}

func (c *chatInteractionCoordinator) releaseInputMode(lease *chatInputModeLease) {
	if c == nil || lease == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lease.released = true
	if c.inputLease != lease {
		return
	}
	for c.inputLease != nil && c.inputLease.released {
		c.inputLease = c.inputLease.previous
	}
	if c.inputLease != nil {
		c.inputMode = c.inputLease.mode
	} else {
		c.inputMode = c.inputModeBase
	}
	if !c.shutdown {
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	}
}

func (c *chatInteractionCoordinator) InputMode() chatInputMode {
	if c == nil {
		return chatInputModeChat
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputMode
}

func pushChatComposerInputMode(session *ChatSession, mode chatInputMode) func() {
	if session == nil || session.Interaction == nil {
		return func() {}
	}
	return session.Interaction.acquireInputMode(mode)
}

func (c *chatInteractionCoordinator) StartWaiting() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.promptPasteActive = false
	if c.writer == os.Stdout && c.surface != nil {
		if c.promptVisible && c.promptRenderedOnSurface {
			if c.surface.ResetPrompt(formatSessionUserPrompt(c.session), 1) {
				c.promptRenderedOnSurface = true
			}
		} else if c.surface.ShowPrompt(formatSessionUserPrompt(c.session)) {
			c.promptVisible = true
			c.promptRenderedOnSurface = true
		}
	} else if c.promptVisible {
		c.clearVisiblePromptLocked()
		c.promptVisible = false
		c.promptRenderedOnSurface = false
	}
	if c.promptVisible && c.promptRenderedOnSurface && c.promptInput != "" && c.writer == os.Stdout && c.surface != nil {
		rows := c.currentPromptDisplayRowsLocked()
		cursorRow, cursorCol := c.currentPromptCursorPositionLocked()
		c.surface.SetPromptInputState(formatSessionUserPrompt(c.session), c.promptInput, rows, cursorRow, cursorCol)
	}
	c.waitingActive = true
	// Codex turn_lifecycle.start: begin live goal-time accrual for this turn.
	// Keep the same start across auto-continuations inside sendMessage.
	markChatGoalStatusActiveTurnStarted(c.session)
	if chatAgentStageIsTerminal(c.agentStage) {
		c.agentStage = chatAgentStageIdle
		c.agentStageDetail = ""
	}
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
}

func (c *chatInteractionCoordinator) ClearWaiting() {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	if c.shutdown || !c.waitingActive {
		c.mu.Unlock()
		return
	}
	c.waitingActive = false
	// Codex turn_lifecycle.finish: stop live goal-time accrual.
	clearChatGoalStatusActiveTurnStarted(c.session)
	session := c.session
	// Codex refreshes git-branch after turn complete so title/status pick up
	// branch switches that happened during the turn. Invalidate before the
	// status rebuild so both surfaces see a fresh probe.
	invalidateChatStatusGitBranchCache(session)
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	c.mu.Unlock()
	refreshChatTitleMetadata(session)
}

func (c *chatInteractionCoordinator) IsReady() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isReadyLocked()
}

func (c *chatInteractionCoordinator) isReadyLocked() bool {
	return c != nil && !c.shutdown && !c.waitingActive && !c.thinkingActive && !c.streamingActive && !c.reasoningActive && !chatAgentStageBlocksReady(c.agentStage)
}

func (c *chatInteractionCoordinator) currentSurfaceStateLocked() string {
	if stage := chatAgentStageSurfaceLabel(c.agentStage); stage != "" {
		if c.agentStage == chatAgentStageToolRunning && c.agentStageDetail != "" {
			return "Tool " + c.agentStageDetail
		}
		return stage
	}
	if c.streamingActive {
		return "Streaming"
	}
	if c.thinkingActive || c.reasoningActive {
		return "Thinking"
	}
	if c.waitingActive {
		return "Waiting"
	}
	return "Ready"
}

func buildChatSurfaceStatusLine(session *ChatSession, state string) string {
	return buildChatSurfaceStatusLineForWidth(session, state, ui.GetTerminalWidth())
}

const chatSurfaceStatusSeparator = " · "

type chatStatusSegment struct {
	full    string
	compact string
}

func buildChatSurfaceStatusLineForWidth(session *ChatSession, state string, width int) string {
	return buildChatSurfaceStatusLineForWidthAndInputMode(session, state, width, chatInputModeForSurfaceState(state))
}

func buildChatSurfaceStatusLineForWidthAndInputMode(session *ChatSession, state string, width int, inputMode chatInputMode) string {
	state = strings.TrimSpace(state)
	if state == "" {
		state = "Ready"
	}
	if width <= 0 {
		width = 80
	}

	segments := buildChatSurfaceStatusSegments(session, state, inputMode)
	parts := fitChatSurfaceStatusSegments(segments, width)
	return truncateStatusValue(strings.Join(parts, chatSurfaceStatusSeparator), width)
}

func buildChatSurfaceStatusSegments(session *ChatSession, state string, inputMode chatInputMode) []chatStatusSegment {
	segments := make([]chatStatusSegment, 0, 12)

	// Critical modal/running hints first so narrow widths keep them.
	if modal := chatSurfaceModalStatusSegment(state, inputMode); modal.full != "" {
		segments = append(segments, modal)
	}
	queuedCount, _ := queuedInteractiveInputState(session)
	if queuedCount > 0 {
		segments = append(segments, chatStatusSegment{
			full:    fmt.Sprintf("队列 %d", queuedCount),
			compact: fmt.Sprintf("队%d", queuedCount),
		})
	}
	// Plan mode is an interactive composer mode rather than a transient agent
	// stage. Keep its compact ON/OFF state near the front so it remains visible
	// in the fixed-bottom status bar even when optional diagnostics are dropped.
	if planSeg := chatSurfacePlanModeStatusSegment(session); planSeg.full != "" {
		segments = append(segments, planSeg)
	}
	// Codex-style goal indicator: keep near the front so residual active goals
	// remain visible even when width drops optional diagnostics.
	if goalSeg := chatSurfaceGoalStatusSegment(session); goalSeg.full != "" {
		segments = append(segments, goalSeg)
	}

	// Reference diagnostics: model · provider · Context N% · cwd · project · branch · window · in · out · Fast
	if model := chatSurfaceModelStatusSegment(session); model.full != "" {
		segments = append(segments, model)
	}
	if providerSeg := chatSurfaceProviderStatusSegment(session); providerSeg.full != "" {
		segments = append(segments, providerSeg)
	}
	if contextSeg := chatSurfaceContextUsedStatusSegment(session); contextSeg.full != "" {
		segments = append(segments, contextSeg)
	}
	if cwdSeg := chatSurfaceDirectoryStatusSegment(session); cwdSeg.full != "" {
		segments = append(segments, cwdSeg)
	}
	if projectSeg := chatSurfaceProjectStatusSegment(session); projectSeg.full != "" {
		segments = append(segments, projectSeg)
	}
	if branchSeg := chatSurfaceGitBranchStatusSegment(session); branchSeg.full != "" {
		segments = append(segments, branchSeg)
	}
	if windowSeg := chatSurfaceWindowStatusSegment(session); windowSeg.full != "" {
		segments = append(segments, windowSeg)
	}
	if inSeg := chatSurfaceInputTokensStatusSegment(session); inSeg.full != "" {
		segments = append(segments, inSeg)
	}
	if outSeg := chatSurfaceOutputTokensStatusSegment(session); outSeg.full != "" {
		segments = append(segments, outSeg)
	}
	if fastSeg := chatSurfaceFastStatusSegment(session); fastSeg.full != "" {
		segments = append(segments, fastSeg)
	}
	return segments
}

func fitChatSurfaceStatusSegments(segments []chatStatusSegment, width int) []string {
	if len(segments) == 0 {
		return nil
	}
	parts := make([]string, len(segments))
	for i, segment := range segments {
		parts[i] = segment.compact
		if parts[i] == "" {
			parts[i] = segment.full
		}
	}
	// Prefer full forms from the front when width allows.
	for i, segment := range segments {
		if segment.full == "" || segment.full == parts[i] {
			continue
		}
		candidate := append([]string(nil), parts...)
		candidate[i] = segment.full
		if ui.DisplayWidth(strings.Join(candidate, chatSurfaceStatusSeparator)) <= width {
			parts = candidate
		}
	}
	// Drop trailing optional segments until the line fits.
	for len(parts) > 1 && ui.DisplayWidth(strings.Join(parts, chatSurfaceStatusSeparator)) > width {
		parts = parts[:len(parts)-1]
	}
	// If still too wide, fall back to compact forms from the front.
	if ui.DisplayWidth(strings.Join(parts, chatSurfaceStatusSeparator)) > width {
		compactParts := make([]string, 0, len(parts))
		for i := range parts {
			value := segments[i].compact
			if value == "" {
				value = segments[i].full
			}
			compactParts = append(compactParts, value)
		}
		parts = compactParts
		for len(parts) > 1 && ui.DisplayWidth(strings.Join(parts, chatSurfaceStatusSeparator)) > width {
			parts = parts[:len(parts)-1]
		}
	}
	return parts
}

func chatPlanModeActive(session *ChatSession) bool {
	if session == nil {
		return false
	}
	return session.PermissionMode == runtimepolicy.ModePlan || planmode.IsActive(loadChatPlanMode(session))
}

func chatSurfacePlanModeStatusSegment(session *ChatSession) chatStatusSegment {
	if session == nil {
		return chatStatusSegment{}
	}
	if chatPlanModeActive(session) {
		return chatStatusSegment{full: "Plan ON", compact: "Plan ON"}
	}
	return chatStatusSegment{full: "Plan OFF", compact: "Plan OFF"}
}

// chatSurfaceGoalStatusSegment mirrors Codex footer GoalStatusIndicator labels.
// Complete goals stay visible briefly so users see the achieved state; missing
// or unreadable goals are omitted.
func chatSurfaceGoalStatusSegment(session *ChatSession) chatStatusSegment {
	goal, ok, err := currentSessionGoal(session)
	if err != nil || !ok || goal == nil {
		return chatStatusSegment{}
	}
	usage := chatSurfaceGoalUsage(goal, chatGoalStatusActiveTurnStartedAt(session), time.Now())
	switch goal.Status {
	case runtimegoal.StatusActive:
		if usage != "" {
			return chatStatusSegment{
				full:    fmt.Sprintf("Pursuing goal (%s)", usage),
				compact: "Goal",
			}
		}
		return chatStatusSegment{full: "Pursuing goal", compact: "Goal"}
	case runtimegoal.StatusPaused:
		return chatStatusSegment{
			full:    "Goal paused (/goal resume)",
			compact: "Paused",
		}
	case runtimegoal.StatusBudgetLimited:
		if usage != "" {
			return chatStatusSegment{
				full:    fmt.Sprintf("Goal unmet (%s)", usage),
				compact: "Unmet",
			}
		}
		return chatStatusSegment{full: "Goal abandoned", compact: "Unmet"}
	case runtimegoal.StatusComplete:
		if usage != "" {
			return chatStatusSegment{
				full:    fmt.Sprintf("Goal achieved (%s)", usage),
				compact: "Done",
			}
		}
		return chatStatusSegment{full: "Goal achieved", compact: "Done"}
	default:
		return chatStatusSegment{}
	}
}

// chatSurfaceGoalUsage mirrors Codex active_goal_usage / completed_goal_usage.
// Active goals without a token budget always report elapsed time, and while a
// turn is running that elapsed time includes live seconds since
// max(goal.UpdatedAt, active_turn_started_at).
func chatSurfaceGoalUsage(goal *runtimegoal.SessionGoal, activeTurnStartedAt, now time.Time) string {
	if goal == nil {
		return ""
	}
	if goal.TokenBudget > 0 {
		if goal.Status == runtimegoal.StatusComplete {
			return fmt.Sprintf("%s tokens", formatChatSurfaceTokenCount(goal.TokensUsed))
		}
		if goal.Status == runtimegoal.StatusBudgetLimited {
			return fmt.Sprintf("%s / %s tokens", formatChatSurfaceTokenCount(goal.TokensUsed), formatChatSurfaceTokenCount(goal.TokenBudget))
		}
		return fmt.Sprintf("%s / %s", formatChatSurfaceTokenCount(goal.TokensUsed), formatChatSurfaceTokenCount(goal.TokenBudget))
	}
	seconds := chatSurfaceGoalElapsedSeconds(goal, activeTurnStartedAt, now)
	switch goal.Status {
	case runtimegoal.StatusActive, runtimegoal.StatusComplete:
		return formatChatSurfaceGoalElapsed(seconds)
	default:
		if seconds > 0 {
			return formatChatSurfaceGoalElapsed(seconds)
		}
		return ""
	}
}

func chatSurfaceGoalElapsedSeconds(goal *runtimegoal.SessionGoal, activeTurnStartedAt, now time.Time) int64 {
	if goal == nil {
		return 0
	}
	seconds := goal.TimeUsedSeconds
	if seconds < 0 {
		seconds = 0
	}
	if goal.Status != runtimegoal.StatusActive || activeTurnStartedAt.IsZero() {
		return seconds
	}
	if now.IsZero() {
		now = time.Now()
	}
	// Codex GoalStatusState.indicator:
	// baseline = max(observed_at, active_turn_started_at)
	// For aicli, goal.UpdatedAt is the last persisted usage observation.
	baseline := activeTurnStartedAt
	if !goal.UpdatedAt.IsZero() && goal.UpdatedAt.After(baseline) {
		baseline = goal.UpdatedAt
	}
	if now.After(baseline) {
		seconds += int64(now.Sub(baseline) / time.Second)
	}
	return seconds
}

func formatChatSurfaceTokenCount(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatChatSurfaceGoalElapsed mirrors Codex format_goal_elapsed_seconds.
func formatChatSurfaceGoalElapsed(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	remainingMinutes := minutes % 60
	if hours >= 24 {
		days := hours / 24
		remainingHours := hours % 24
		return fmt.Sprintf("%dd %dh %dm", days, remainingHours, remainingMinutes)
	}
	if remainingMinutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, remainingMinutes)
}

// markChatGoalStatusActiveTurnStarted records the turn start used for live
// active-goal elapsed accrual. Subsequent marks in the same turn are no-ops so
// goal auto-continuation keeps a continuous wall-clock baseline.
func markChatGoalStatusActiveTurnStarted(session *ChatSession) {
	if session == nil {
		return
	}
	session.goalStatusMu.Lock()
	if session.goalStatusActiveTurnStartedAt.IsZero() {
		session.goalStatusActiveTurnStartedAt = time.Now()
	}
	session.goalStatusMu.Unlock()
}

func clearChatGoalStatusActiveTurnStarted(session *ChatSession) {
	if session == nil {
		return
	}
	session.goalStatusMu.Lock()
	session.goalStatusActiveTurnStartedAt = time.Time{}
	session.goalStatusMu.Unlock()
}

func chatGoalStatusActiveTurnStartedAt(session *ChatSession) time.Time {
	if session == nil {
		return time.Time{}
	}
	session.goalStatusMu.Lock()
	defer session.goalStatusMu.Unlock()
	return session.goalStatusActiveTurnStartedAt
}

func chatSurfaceModalStatusSegment(state string, inputMode chatInputMode) chatStatusSegment {
	switch normalizeChatInputMode(inputMode) {
	case chatInputModeApproval:
		return chatStatusSegment{full: "等待审批", compact: "审批"}
	case chatInputModeAnswer:
		return chatStatusSegment{full: "等待回答", compact: "回答"}
	case chatInputModeSelection:
		return chatStatusSegment{full: "选择选项", compact: "选择"}
	case chatInputModeConfirmation:
		return chatStatusSegment{full: "确认操作", compact: "确认"}
	case chatInputModeSecret:
		return chatStatusSegment{full: "输入密钥", compact: "密钥"}
	case chatInputModePanel:
		return chatStatusSegment{full: "面板导航", compact: "导航"}
	}

	normalized := strings.ToLower(strings.TrimSpace(state))
	if strings.HasPrefix(normalized, "tool ") {
		detail := strings.TrimSpace(state[len("tool "):])
		if detail != "" {
			return chatStatusSegment{
				full:    "执行工具 " + detail,
				compact: "执行工具",
			}
		}
		return chatStatusSegment{full: "执行工具", compact: "执行工具"}
	}
	switch normalized {
	case "ready", "completed", "failed", "idle":
		// Codex-aligned idle surface: terminal outcomes do not keep a sticky label.
		return chatStatusSegment{}
	case "waiting":
		return chatStatusSegment{full: "等待", compact: "等待"}
	case "thinking":
		return chatStatusSegment{full: "思考", compact: "思考"}
	case "streaming":
		return chatStatusSegment{full: "输出中", compact: "输出中"}
	case "planning":
		return chatStatusSegment{full: "规划中", compact: "规划中"}
	case "tool running":
		return chatStatusSegment{full: "执行工具", compact: "执行工具"}
	case "awaiting approval":
		return chatStatusSegment{full: "等待审批", compact: "审批"}
	case "awaiting answer":
		return chatStatusSegment{full: "等待回答", compact: "回答"}
	case "stopping":
		return chatStatusSegment{full: "停止中", compact: "停止"}
	default:
		display := chatSurfaceStateDisplay(state)
		if display == "" || display == "就绪" {
			return chatStatusSegment{}
		}
		return chatStatusSegment{full: display, compact: compactChatSurfaceState(state)}
	}
}

func chatSurfaceModelStatusSegment(session *ChatSession) chatStatusSegment {
	if session == nil {
		return chatStatusSegment{}
	}
	model := strings.TrimSpace(firstNonEmptyChatValue(session.EffectiveModel, session.Model))
	if model == "" {
		return chatStatusSegment{}
	}
	effort := strings.TrimSpace(firstNonEmptyChatValue(
		session.EffectiveReasoningEffort,
		session.ReasoningEffort,
		session.RequestedReasoningEffort,
	))
	if effort != "" {
		effort = strings.ToLower(effort)
		full := model + " " + effort
		compactModel := compactStatusValue(model, 16)
		return chatStatusSegment{
			full:    full,
			compact: compactModel + " " + effort,
		}
	}
	return chatStatusSegment{
		full:    model,
		compact: compactStatusValue(model, 16),
	}
}

func chatSurfaceProviderStatusSegment(session *ChatSession) chatStatusSegment {
	if session == nil {
		return chatStatusSegment{}
	}
	provider := strings.TrimSpace(firstNonEmptyChatValue(
		session.EffectiveProvider,
		session.ProviderName,
	))
	if provider == "" {
		provider = strings.TrimSpace(session.Provider.GetProtocol())
	}
	if provider == "" {
		return chatStatusSegment{}
	}
	return chatStatusSegment{
		full:    provider,
		compact: compactStatusValue(provider, 16),
	}
}

func chatSurfaceContextUsedStatusSegment(session *ChatSession) chatStatusSegment {
	if session == nil {
		return chatStatusSegment{}
	}
	budget := resolveSharedChatPromptBudget(session)
	hasWindow := budget.ActiveTurnMaxTokens > 0 ||
		budget.ModelCapabilityMaxContextTokens > 0 ||
		budget.ProviderContextLimit > 0 ||
		session.ContextWindowTokenCount > 0 ||
		session.ContextTokenCount > 0 ||
		len(session.Messages) > 0
	if !hasWindow {
		return chatStatusSegment{}
	}
	totalWindow := resolveChatStatusContextWindowTokens(session)
	if totalWindow <= 0 {
		totalWindow = budget.ActiveTurnMaxTokens
	}
	if totalWindow <= 0 {
		return chatStatusSegment{}
	}
	usedTokens := resolveChatStatusContextUsedTokens(session)
	percent := chatStatusContextUsedPercent(usedTokens, totalWindow)
	return chatStatusSegment{
		full:    fmt.Sprintf("Context %d%% used", percent),
		compact: fmt.Sprintf("Ctx %d%%", percent),
	}
}

func chatSurfaceDirectoryStatusSegment(session *ChatSession) chatStatusSegment {
	cwd := resolveChatStatusCurrentDirectory(session)
	if cwd == "" {
		return chatStatusSegment{}
	}
	return chatStatusSegment{
		full:    cwd,
		compact: compactChatStatusDirectory(cwd),
	}
}

func chatSurfaceProjectStatusSegment(session *ChatSession) chatStatusSegment {
	name := resolveChatStatusProjectName(session)
	if name == "" {
		return chatStatusSegment{}
	}
	return chatStatusSegment{
		full:    name,
		compact: compactStatusValue(name, 16),
	}
}

func chatSurfaceGitBranchStatusSegment(session *ChatSession) chatStatusSegment {
	branch := resolveChatStatusGitBranch(session)
	if branch == "" {
		return chatStatusSegment{}
	}
	return chatStatusSegment{
		full:    branch,
		compact: compactStatusValue(branch, 18),
	}
}

func chatSurfaceWindowStatusSegment(session *ChatSession) chatStatusSegment {
	windowTokens := resolveChatStatusContextWindowTokens(session)
	if windowTokens <= 0 {
		budget := resolveSharedChatPromptBudget(session)
		windowTokens = budget.ModelCapabilityMaxContextTokens
		if windowTokens <= 0 {
			windowTokens = budget.ProviderContextLimit
		}
		if windowTokens <= 0 {
			windowTokens = budget.ActiveTurnMaxTokens
		}
	}
	if windowTokens <= 0 {
		return chatStatusSegment{}
	}
	label := strings.ToUpper(compactStatusCount(windowTokens)) + " window"
	return chatStatusSegment{full: label, compact: label}
}

func chatSurfaceInputTokensStatusSegment(session *ChatSession) chatStatusSegment {
	inputTokens := resolveChatStatusInputTokens(session)
	if inputTokens <= 0 {
		return chatStatusSegment{}
	}
	label := strings.ToUpper(compactStatusCount(inputTokens)) + " in"
	return chatStatusSegment{full: label, compact: label}
}

func chatSurfaceOutputTokensStatusSegment(session *ChatSession) chatStatusSegment {
	outputTokens := resolveChatStatusOutputTokens(session)
	if outputTokens <= 0 {
		return chatStatusSegment{}
	}
	label := strings.ToUpper(compactStatusCount(outputTokens)) + " out"
	return chatStatusSegment{full: label, compact: label}
}

func chatSurfaceFastStatusSegment(session *ChatSession) chatStatusSegment {
	// Fast is a Codex service-tier control (service_tier=priority), not stream mode.
	if !chatSessionSupportsFastMode(session) {
		return chatStatusSegment{}
	}
	if session.FastMode {
		return chatStatusSegment{full: "Fast on", compact: "Fast on"}
	}
	return chatStatusSegment{full: "Fast off", compact: "Fast off"}
}

func resolveChatStatusProjectName(session *ChatSession) string {
	if session != nil {
		if name := strings.TrimSpace(session.ProfileName); name != "" {
			return name
		}
	}
	cwd := resolveChatStatusCurrentDirectory(session)
	if cwd == "" {
		return ""
	}
	if root := findGitRoot(cwd); root != "" {
		base := filepath.Base(root)
		if base != "" && base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	base := filepath.Base(filepath.Clean(cwd))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func resolveChatStatusInputTokens(session *ChatSession) int {
	if session == nil {
		return 0
	}
	if session.InputTokenCount > 0 {
		return session.InputTokenCount
	}
	usage := chatStatusTokenUsageSnapshotForSession(session)
	return usage.Input
}

func resolveChatStatusOutputTokens(session *ChatSession) int {
	if session == nil {
		return 0
	}
	if session.OutputTokenCount > 0 {
		return session.OutputTokenCount
	}
	usage := chatStatusTokenUsageSnapshotForSession(session)
	return usage.Output
}

type chatStatusGitCacheEntry struct {
	branch    string
	checkedAt time.Time
}

var (
	chatStatusGitCacheMu sync.Mutex
	chatStatusGitCache   = map[string]chatStatusGitCacheEntry{}
	// chatStatusGitBranchLookup is overridable in tests to avoid real git calls.
	chatStatusGitBranchLookup = lookupChatStatusGitBranch
)

const chatStatusGitCacheTTL = 5 * time.Second
const chatStatusGitLookupTimeout = 250 * time.Millisecond

func resetChatStatusGitBranchCacheForTest() {
	chatStatusGitCacheMu.Lock()
	chatStatusGitCache = map[string]chatStatusGitCacheEntry{}
	chatStatusGitCacheMu.Unlock()
}

// invalidateChatStatusGitBranchCache drops the cached branch for session cwd so
// the next resolve re-probes git. Codex refreshes branch after turn complete.
func invalidateChatStatusGitBranchCache(session *ChatSession) {
	cwd := resolveChatStatusCurrentDirectory(session)
	if cwd == "" {
		return
	}
	clean := filepath.Clean(cwd)
	chatStatusGitCacheMu.Lock()
	delete(chatStatusGitCache, clean)
	chatStatusGitCacheMu.Unlock()
}

func resolveChatStatusGitBranch(session *ChatSession) string {
	cwd := resolveChatStatusCurrentDirectory(session)
	if cwd == "" {
		return ""
	}
	clean := filepath.Clean(cwd)

	chatStatusGitCacheMu.Lock()
	if entry, ok := chatStatusGitCache[clean]; ok && time.Since(entry.checkedAt) < chatStatusGitCacheTTL {
		branch := entry.branch
		chatStatusGitCacheMu.Unlock()
		return branch
	}
	chatStatusGitCacheMu.Unlock()

	branch := chatStatusGitBranchLookup(clean)
	chatStatusGitCacheMu.Lock()
	chatStatusGitCache[clean] = chatStatusGitCacheEntry{
		branch:    branch,
		checkedAt: time.Now(),
	}
	chatStatusGitCacheMu.Unlock()
	return branch
}

func lookupChatStatusGitBranch(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatStatusGitLookupTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(output))
	if branch == "" || branch == "HEAD" {
		return ""
	}
	return branch
}

func compactChatSurfaceState(state string) string {
	normalized := strings.ToLower(strings.TrimSpace(state))
	if strings.HasPrefix(normalized, "tool ") {
		return "执行工具"
	}
	switch normalized {
	case "ready":
		return "就绪"
	case "waiting":
		return "等待"
	case "thinking":
		return "思考"
	case "streaming":
		return "输出中"
	case "planning":
		return "规划中"
	case "tool running":
		return "执行工具"
	case "awaiting approval":
		return "等待审批"
	case "awaiting answer":
		return "等待回答"
	case "stopping":
		return "停止中"
	case "completed":
		return "已完成"
	case "failed":
		return "失败"
	default:
		return compactStatusValue(state, 10)
	}
}

func chatSurfaceStateDisplay(state string) string {
	normalized := strings.ToLower(strings.TrimSpace(state))
	if strings.HasPrefix(normalized, "tool ") {
		return "执行工具 " + strings.TrimSpace(state[len("tool "):])
	}
	return compactChatSurfaceState(state)
}

func chatInputModeForSurfaceState(state string) chatInputMode {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "awaiting approval":
		return chatInputModeApproval
	case "awaiting answer":
		return chatInputModeAnswer
	default:
		return chatInputModeChat
	}
}

func chatSurfaceStateIsRunning(state string) bool {
	normalized := strings.ToLower(strings.TrimSpace(state))
	if strings.HasPrefix(normalized, "tool ") {
		return true
	}
	switch normalized {
	case "waiting", "thinking", "streaming", "planning", "tool running", "awaiting approval", "awaiting answer", "stopping", "retrying", "running", "working", "busy":
		return true
	default:
		return false
	}
}

func compactChatStatusDirectory(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	clean := filepath.Clean(cwd)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return compactStatusValue(clean, 20)
	}
	return compactStatusValue(base, 20)
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
	if chatAgentStageIsTerminal(c.agentStage) {
		c.agentStage = chatAgentStageIdle
		c.agentStageDetail = ""
	}
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
	promptAfterBlockGap := c.promptAfterBlockGap
	if !c.beginMessageLocked() {
		return
	}
	response = sanitizeInteractiveAsyncTeamLaunchResponse(response)
	formatted := response
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(response)
	}
	c.writeCompleteBlockLocked(ui.FormatAssistantMessage(formatted), promptWasVisible && !promptAfterBlockGap)
	c.lastCompletedAsyncLine = false
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
		if meta := chatReasoningMetaLine(block); meta != "" {
			c.reasoningMeta = meta
		} else {
			c.reasoningMeta = ""
		}
		if c.shouldLiveStreamOutputLocked() {
			c.writeLineLocked(ui.FormatAssistantSupplementBlock(chatToolDivider("reasoning")))
			if c.reasoningMeta != "" {
				c.writeLineLocked(ui.FormatAssistantSupplementBlock(c.reasoningMeta))
			}
		}
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	}
	delta := normalizeAssistantStreamDelta(c.reasoningBuffer.String(), display)
	if delta == "" {
		return
	}
	if c.shouldLiveStreamOutputLocked() {
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

	if c.shouldLiveStreamOutputLocked() && !c.reasoningActive {
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
	if c.shouldLiveStreamOutputLocked() {
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
	if c.shouldLiveStreamOutputLocked() {
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
	if c.shouldLiveStreamOutputLocked() {
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
	if c.shouldLiveStreamOutputLocked() {
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
	promptAfterBlockGap := c.promptAfterBlockGap
	previousAsyncLine := c.lastCompletedAsyncLine
	if !c.beginMessageLocked() {
		return
	}
	c.writeCompleteBlockLocked(ui.FormatAssistantSupplementBlock(line), promptWasVisible && !promptAfterBlockGap && !previousAsyncLine)
	c.lastCompletedAsyncLine = true
}

func (c *chatInteractionCoordinator) RenderSubmittedUserInput(input string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || strings.TrimSpace(input) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.beginMessageLocked() {
		return
	}
	c.writeCompleteBlockLocked(ui.FormatUserMessage(input), false)
	c.lastCompletedAsyncLine = false
}

func (c *chatInteractionCoordinator) RenderError(err error) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	promptWasVisible := c.promptVisible
	promptAfterBlockGap := c.promptAfterBlockGap
	if !c.beginMessageLocked() {
		return
	}
	c.writeCompleteBlockLocked(ui.FormatErrorMessage(fmt.Sprintf("操作错误: %v", err)), promptWasVisible && !promptAfterBlockGap)
	c.lastCompletedAsyncLine = false
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
	c.promptCursor = 0
	c.promptRenderedOnSurface = false
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
	if c.promptSurfaceActiveLocked() {
		rows := c.currentPromptDisplayRowsLocked()
		if c.surface.ResetPrompt(formatSessionUserPrompt(c.session), rows) {
			c.promptVisible = true
			c.promptInput = ""
			c.promptCursor = 0
			c.promptRenderedOnSurface = true
			c.promptPasteActive = false
			return
		}
	}
	c.promptVisible = false
	c.promptInput = ""
	c.promptCursor = 0
	c.promptRenderedOnSurface = false
	c.promptPasteActive = false
}

func (c *chatInteractionCoordinator) SetPromptInput(input string) {
	c.SetPromptInputSnapshot(ui.LineEditorSnapshot{Text: input, Cursor: len([]rune(input))})
}

func (c *chatInteractionCoordinator) PromptInputSnapshot() ui.LineEditorSnapshot {
	if c == nil {
		return ui.LineEditorSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ui.LineEditorSnapshot{
		Text:        c.promptInput,
		Cursor:      c.promptCursor,
		Prompt:      formatSessionUserPrompt(c.session),
		PasteActive: c.promptPasteActive,
	}
}

func (c *chatInteractionCoordinator) RenderPromptInputSnapshot(snapshot ui.LineEditorSnapshot) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	input := strings.ReplaceAll(snapshot.Text, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	c.promptInput = input
	c.promptCursor = clampPromptCursor(snapshot.Cursor, input)
	c.promptPasteActive = snapshot.PasteActive
	if c.promptVisible && c.promptRenderedOnSurface && c.writer == os.Stdout && c.surface != nil {
		rows := c.currentPromptDisplayRowsLocked()
		cursorRow, cursorCol := c.currentPromptCursorPositionLocked()
		c.surface.SetPromptInputState(formatSessionUserPrompt(c.session), input, rows, cursorRow, cursorCol)
	}
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
	c.promptCursor = clampPromptCursor(snapshot.Cursor, input)
	c.promptPasteActive = snapshot.PasteActive
	if c.promptVisible && c.promptRenderedOnSurface && c.writer == os.Stdout && c.surface != nil {
		rows := c.currentPromptDisplayRowsLocked()
		cursorRow, cursorCol := c.currentPromptCursorPositionLocked()
		c.surface.TrackPromptInputState(formatSessionUserPrompt(c.session), input, rows, cursorRow, cursorCol)
	}
}

func (c *chatInteractionCoordinator) IsPromptPasteActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.promptPasteActive
}

func (c *chatInteractionCoordinator) RestorePromptCursor(rowOffset, col int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restorePromptCursorLocked(rowOffset, col)
}

func (c *chatInteractionCoordinator) PromptCursorPrefix(rowOffset, col int) string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.promptSurfaceActiveLocked() {
		return ""
	}
	prefix, ok := c.surface.PromptCursorPrefix(rowOffset, col)
	if !ok {
		return ""
	}
	return prefix
}

func (c *chatInteractionCoordinator) WritePromptEditorText(writer io.Writer, rowOffset, col int, text string) bool {
	if c == nil || writer == nil || text == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.promptSurfaceActiveLocked() {
		return false
	}
	return c.surface.WritePromptEditorText(writer, rowOffset, col, text)
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
	c.lastCompletedAsyncLine = false
	c.promptAfterBlockGap = false
	c.agentStage = chatAgentStageIdle
	c.agentStageDetail = ""
	c.inputModeBase = chatInputModeChat
	c.inputLease = nil
	c.inputMode = chatInputModeChat
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
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
	c.promptCursor = 0
	c.promptRenderedOnSurface = false
	c.promptPasteActive = false
	c.waitingActive = false
	c.agentStage = chatAgentStageIdle
	c.agentStageDetail = ""
	c.inputModeBase = chatInputModeChat
	c.inputLease = nil
	c.inputMode = chatInputModeChat
	c.thinkingActive = false
	c.streamingActive = false
	c.reasoningActive = false
	c.completeBlockOutput = false
	c.lastCompletedAsyncLine = false
	c.promptAfterBlockGap = false
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
	if c.promptSurfaceActiveLocked() {
		return true
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

func (c *chatInteractionCoordinator) promptSurfaceActiveLocked() bool {
	return c != nil && c.promptVisible && c.promptRenderedOnSurface && c.surface != nil && c.surface.Enabled()
}

func (c *chatInteractionCoordinator) surfaceOutputActiveLocked() bool {
	return c != nil && c.surface != nil && c.surface.Enabled()
}

func (c *chatInteractionCoordinator) restorePromptCursorLocked(rowOffset, col int) {
	if !c.promptSurfaceActiveLocked() {
		return
	}
	c.surface.MoveToPromptCursor(rowOffset, col)
}

func (c *chatInteractionCoordinator) restoreCurrentPromptCursorLocked() {
	if !c.promptSurfaceActiveLocked() {
		return
	}
	row, col := c.currentPromptCursorPositionLocked()
	c.surface.MoveToPromptCursor(row, col)
}

func (c *chatInteractionCoordinator) setCurrentPromptCursorLocked() {
	if !c.promptSurfaceActiveLocked() {
		return
	}
	row, col := c.currentPromptCursorPositionLocked()
	c.surface.SetPromptCursor(row, col)
}

func (c *chatInteractionCoordinator) currentPromptCursorPositionLocked() (int, int) {
	termWidth := ui.GetTerminalWidth()
	if termWidth <= 0 {
		termWidth = 80
	}
	return interactivePromptCursorPosition(promptDisplayText(c.session), c.promptInput, c.promptCursor, termWidth)
}

func (c *chatInteractionCoordinator) clearThinkingLocked() {
}

// flushStreamLocked outputs any buffered streaming content before the stream
// is interrupted by another message (e.g. thinking, async line, new response).
// This prevents text from being silently swallowed when a ReAct loop's
// intermediate assistant deltas are never finalized via FinalizeAssistantDelta.
func (c *chatInteractionCoordinator) flushStreamLocked() {
	if c.shouldLiveStreamOutputLocked() {
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
	if c.shouldLiveStreamOutputLocked() {
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
		c.promptRenderedOnSurface = false
		return
	}
	termWidth := ui.GetTerminalWidth()
	if termWidth <= 0 {
		termWidth = 80
	}
	rows := interactivePromptDisplayRows(promptLine, termWidth)
	if c.writer == os.Stdout && c.surface != nil && c.surface.ClearPromptRows(rows) {
		c.promptRenderedOnSurface = false
		return
	}
	// 先从当前编辑光标所在行回到输入起始行，再逐行清理
	// prompt/input 曾占用的区域。
	var builder strings.Builder
	if cursorRow := interactivePromptCursorRow(promptText, c.promptInput, c.promptCursor, termWidth); cursorRow > 0 {
		fmt.Fprintf(&builder, "\x1b[%dA", cursorRow)
	}
	builder.WriteString(clearPromptDisplayRowsSequence(rows))
	c.writeTextLocked(builder.String())
	c.promptRenderedOnSurface = false
}

func (c *chatInteractionCoordinator) currentPromptDisplayRowsLocked() int {
	if c == nil {
		return 1
	}
	termWidth := ui.GetTerminalWidth()
	if termWidth <= 0 {
		termWidth = 80
	}
	return interactivePromptDisplayRows(promptDisplayText(c.session)+c.promptInput, termWidth)
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
		row, col = advanceInteractivePromptPosition(row, col, width, termWidth)
	}
	return row + 1
}

func interactivePromptCursorRow(promptText, input string, cursor int, termWidth int) int {
	row, _ := interactivePromptCursorPosition(promptText, input, cursor, termWidth)
	return row
}

func interactivePromptCursorPosition(promptText, input string, cursor int, termWidth int) (int, int) {
	if termWidth <= 0 {
		termWidth = 80
	}
	if cursor < 0 {
		cursor = 0
	}
	runes := []rune(input)
	if cursor > len(runes) {
		cursor = len(runes)
	}
	row, col := 0, 0
	for _, r := range promptText + string(runes[:cursor]) {
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
		row, col = advanceInteractivePromptPosition(row, col, width, termWidth)
	}
	return row, col
}

func advanceInteractivePromptPosition(row, col, runeWidth, termWidth int) (int, int) {
	if runeWidth <= 0 {
		return row, col
	}
	if termWidth <= 0 {
		termWidth = 80
	}
	if col > 0 && col+runeWidth > termWidth {
		row++
		col = 0
	}
	col += runeWidth
	if col >= termWidth {
		row += col / termWidth
		col %= termWidth
	}
	return row, col
}

func clampPromptCursor(cursor int, input string) int {
	if cursor < 0 {
		return 0
	}
	runeCount := len([]rune(input))
	if cursor > runeCount {
		return runeCount
	}
	return cursor
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
	if !c.shouldLiveStreamOutputLocked() || !c.streamingActive || c.streamRendered {
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
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	}
}

func (c *chatInteractionCoordinator) resetReasoningLocked() {
	c.reasoningActive = false
	c.reasoningRendered = false
	c.reasoningTrailingLF = false
	c.reasoningMeta = ""
	c.reasoningBuffer.Reset()
	if !c.thinkingActive && !c.streamingActive {
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	}
}

func (c *chatInteractionCoordinator) writeCompleteBlockLocked(rendered string, suppressSeparator bool) {
	if strings.TrimSpace(rendered) == "" {
		return
	}
	if !suppressSeparator && (c.completeBlockOutput || c.promptAfterBlockGap) {
		c.writeLineLocked("")
	}
	c.writeLineLocked(rendered)
	c.completeBlockOutput = true
	c.promptAfterBlockGap = false
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
	var builder strings.Builder
	builder.Grow(len(delta) + len(indent)*2)
	for _, r := range []rune(delta) {
		if atLineStart && r != '\n' {
			builder.WriteString(indent)
			atLineStart = false
			*rendered = true
		}
		builder.WriteRune(r)
		if r == '\n' {
			atLineStart = true
		}
	}
	*trailingLF = atLineStart
	if !*rendered && delta != "" {
		*rendered = true
	}
	c.writeStreamingDeltaLocked(builder.String())
}

func (c *chatInteractionCoordinator) writeStreamingDeltaLocked(delta string) {
	if delta == "" {
		return
	}
	if c.writeSurfaceOutputTextLocked(delta) {
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
		prompt := formatSessionUserPrompt(c.session)
		if c.writer == os.Stdout && c.surface != nil && c.surface.ShowPrompt(prompt) {
			c.promptVisible = true
			c.promptRenderedOnSurface = true
			c.preparePromptGapLocked(false)
			if c.promptInput != "" {
				rows := c.currentPromptDisplayRowsLocked()
				cursorRow, cursorCol := c.currentPromptCursorPositionLocked()
				c.surface.SetPromptInputState(prompt, c.promptInput, rows, cursorRow, cursorCol)
			}
			return
		}
		c.promptRenderedOnSurface = false
		c.preparePromptGapLocked(true)
		c.writeTextLocked(prompt)
		if c.promptInput != "" {
			c.writeTextLocked(c.promptInput)
		}
		c.promptVisible = true
	})
}
