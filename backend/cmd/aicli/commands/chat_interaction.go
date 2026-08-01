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
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/siteaccount"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatInteractionCoordinator struct {
	session       *ChatSession
	writer        io.Writer
	surfaceWriter bool
	surface       *ui.FixedBottomSurface

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
	streamEnqueuedPrefixLen int
	// streamTrailingLF is the assistant-stream open-row cursor (Phase C).
	// true  = cursor already at column 0 (row boundary / never opened a row)
	// false = mid-line content was written without a row terminator
	// Only writeIndentedStreamingDeltaLocked / residual inline paths set false;
	// start, reset, writeRows, and closeOpenRow keep/restore true.
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
	waitingActive      bool
	agentStage         chatAgentStage
	agentStageDetail   string
	inputMode          chatInputMode
	inputModeBase      chatInputMode
	inputLeaseSeq      uint64
	inputLease         *chatInputModeLease

	reasoningActive   bool
	reasoningRendered bool
	// reasoningTrailingLF mirrors streamTrailingLF for the reasoning band only.
	// Kept separate so reasoning/assistant interleave does not share mid-line state.
	reasoningTrailingLF bool
	reasoningMeta       string
	reasoningBuffer     strings.Builder
	completeBlockOutput bool
	shutdown            bool

	// Two-region stream state: the controller owns the mutable ActiveBand while
	// stable rendered chunks move through a bounded, animated scrollback queue.
	// Enqueued and emitted source offsets stay distinct so queued content never
	// reappears in the live tail and finalization can drain without replay.
	activeStream          *ui.ActiveStreamController
	activeFrameTimer      *time.Timer
	activeFrameDue        time.Time
	activeFrameGeneration uint64
	stableCommitQueue     []activeStableCommitLine
	stableCommitTimer     *time.Timer
	stableCommitTimerSeq  uint64
	stableCommitDelay     time.Duration
	stableCommitCatchUp   bool
	// Synthetic surfaces normally drain synchronously. Queue-policy tests set
	// this flag to advance commits explicitly without wall-clock timers.
	stableCommitManual            bool
	stableCommitBelowExit         time.Time
	stableCommitLastExit          time.Time
	dynamicStatusStarted          time.Time
	dynamicStatusCompletedElapsed time.Duration
	dynamicStatusCompleted        bool
	dynamicStatusTimer            *time.Timer
	dynamicStatusTimerSeq         uint64
	persistentStatusModel         style.StatusLineModel
	dynamicStatusModel            *style.StatusLineModel
	statusModelsCached            bool
	activeTools                   map[string]chatActiveTool
	activeToolSequence            uint64
	commandCellSequence           uint64
	lastCommandCellID             string

	// assistantTurnTranscript is the source-backed authoritative record for the
	// current assistant turn. It is kept separate from the mutable streamBuffer
	// so final divergence and resize reflow can reason from source ranges
	// instead of terminal history.
	transcript *assistantTurnTranscript
	// Last-turn transcript debug mirrors survive resetStreamLocked so
	// DebugSummary remains useful after finalization.
	streamLastFinalDivergence  assistantFinalDivergence
	streamLastTranscriptBlocks int
	streamLastTranscriptBytes  int

	// softEmitted* tracks the source-backed committed tail that still sits in
	// the surface soft rewrite window. Resize reflow re-formats this range from
	// Markdown/plain source and rewrites only those rows; older scrollback stays
	// irreversible.
	softEmittedSourceStart int
	softEmittedSourceEnd   int
	softEmittedLines       []string
	softEmittedWidth       int
	// softEmittedSegments preserves atomic source-chunk boundaries so the soft
	// window can drop whole older chunks when it hits SoftOutputTailMaxLines
	// without inventing a mid-chunk source start.
	softEmittedSegments []softEmittedSegment
	// softCommitWriting is true while drainActiveStableCommitLocked writes the
	// soft-committed batch. Foreign writeTextLocked paths clear soft ownership
	// so tool/notice output cannot pollute the 1:1 rewrite window.
	softCommitWriting bool
}

type softEmittedSegment struct {
	sourceStart int
	sourceEnd   int
	lines       []string
}

type activeStableCommitLine struct {
	text       string // formatted text ready for writeLineLocked
	source     string // original Markdown source chunk
	sourceEnd  int    // absolute end offset in full source
	enqueuedAt time.Time
	// commitBand is set on the last rendered line of a source chunk so the
	// ActiveBand hide (CommitStablePrefix) happens only after scrollback write.
	// Committing earlier leaves a visible hole while the animated queue drains.
	commitBand bool
}

// blockGap is an explicit cross-block separator decision. Content spacing
// belongs in formatter rows (including "" blank rows); cross-message
// separation is decided by the caller and never inferred from pollution flags
// such as completeBlockOutput.
type blockGap int

const (
	gapNone blockGap = iota
	gapBlank
)

// renderedAssistantChunk is the shared newline-ownership contract for stable
// enqueue, residual finalize, and soft reflow. Lines never carry trailing LFs;
// writeLineLocked alone terminates each row. leading structural newlines from
// Format differentials are resolved in markdownFullPrefixSuffixLocked before
// this chunk is built (Phase B will replace that strip with rows-delta).
type renderedAssistantChunk struct {
	lines []string
}

func (ch renderedAssistantChunk) empty() bool {
	return len(ch.lines) == 0 || (len(ch.lines) == 1 && ch.lines[0] == "")
}

// normalizeWriteLines is the single place that turns a pre-styled block into
// writeLine-ready rows: strip the block terminator, keep internal blank rows,
// drop CR. writeCompleteBlock / supplement / user / error paths use this
// directly; assistant stream paths go through buildRenderedAssistantChunk.
func normalizeWriteLines(rendered string) []string {
	rendered = strings.TrimRight(rendered, "\r\n")
	if rendered == "" {
		return nil
	}
	parts := strings.Split(rendered, "\n")
	lines := make([]string, len(parts))
	for i, part := range parts {
		lines[i] = strings.TrimSuffix(part, "\r")
	}
	return lines
}

// buildRenderedAssistantChunk normalizes a formatter/plain suffix into
// writeLine-ready rows. FormatAssistantRendered runs once here so enqueue,
// residual, and soft reflow cannot drift on indent/sanitize.
func buildRenderedAssistantChunk(rendered string) renderedAssistantChunk {
	return renderedAssistantChunk{lines: normalizeWriteLines(ui.FormatAssistantRendered(rendered))}
}

type chatActiveTool struct {
	key      string
	detail   string
	display  string
	sequence uint64
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

const (
	activeStableCommitTickDelay  = 33 * time.Millisecond
	activeStableCatchUpDepth     = 8
	activeStableCatchUpOldestAge = 120 * time.Millisecond
	activeStableCatchUpExitDepth = 2
	activeStableCatchUpExitAge   = 40 * time.Millisecond
	activeStableCatchUpExitHold  = 250 * time.Millisecond
	activeStableCatchUpReenter   = 250 * time.Millisecond
	activeStableSevereDepth      = 64
	activeStableSevereAge        = 300 * time.Millisecond
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
	interactive := session == nil || (!session.NoInteractive && !session.JSONOutput)
	motion.SetGlobal(motion.NewPolicy(motion.Config{Interactive: interactive}))
	width := ui.GetTerminalWidth()
	if width <= 0 {
		width = 80
	}
	return &chatInteractionCoordinator{
		session:           session,
		writer:            os.Stdout,
		surfaceWriter:     true,
		streamRuneDelay:   6 * time.Millisecond,
		maxChunkDelay:     90 * time.Millisecond,
		promptDelay:       120 * time.Millisecond,
		stableCommitDelay: activeStableCommitTickDelay,
		// Cursor starts at row boundary. Zero-value false would make the first
		// writeRowsLocked/closeOpenRow inject a phantom blank into history
		// replay (RenderAssistant) while live streams set this true on start —
		// the live-vs-replay blank-count skew.
		streamTrailingLF: true,
		activeStream:     ui.NewActiveStreamController(width, ui.ActiveBandRows(ui.GetTerminalHeight())),
		transcript:       &assistantTurnTranscript{},
	}
}

func (c *chatInteractionCoordinator) SetWriter(writer io.Writer) {
	if c == nil || writer == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writer = writer
	c.surfaceWriter = writer == os.Stdout
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
	if surface == nil || !surface.Enabled() {
		c.reconcilePendingStableCommitLocked(c.streamBuffer.String())
	}
	c.surface = surface
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	if c.activeStream != nil && c.activeStream.Active() && c.surfaceOutputActiveLocked() {
		_ = c.publishActiveStreamFrameLocked(true)
	}
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
	now := time.Now()
	c.updateDynamicStatusClockLocked(state, now)
	if c.surface != nil {
		persistentModel := buildChatPersistentStatusModelForWidth(c.session, ui.GetTerminalWidth())
		dynamicModel := buildChatDynamicStatusModelForWidthInputModeAndCompletion(
			state,
			ui.GetTerminalWidth(),
			c.inputMode,
			c.dynamicStatusElapsedLocked(now),
			c.dynamicStatusCompleted,
		)
		c.persistentStatusModel = cloneChatStatusLineModel(persistentModel)
		c.dynamicStatusModel = cloneChatStatusLineModelPointer(dynamicModel)
		c.statusModelsCached = true
		c.surface.SetStatusModels(
			persistentModel,
			dynamicModel,
		)
		c.surface.SetPromptNoticeLine(buildChatPromptNoticeLineForWidth(c.session, state, ui.GetTerminalWidth()))
		c.scheduleDynamicStatusTickLocked(now)
	}
}

func cloneChatStatusLineModel(model style.StatusLineModel) style.StatusLineModel {
	model.Segments = append([]style.StatusSegment(nil), model.Segments...)
	return model
}

func cloneChatStatusLineModelPointer(model *style.StatusLineModel) *style.StatusLineModel {
	if model == nil {
		return nil
	}
	cloned := cloneChatStatusLineModel(*model)
	return &cloned
}

// RefreshAccountBalanceStatus repaints only the balance projection in the
// cached persistent model. A timer callback must not rebuild the whole footer:
// the remaining ChatSession counters and mode fields belong to the foreground
// interaction lifecycle and are not all guarded for arbitrary background reads.
func (c *chatInteractionCoordinator) RefreshAccountBalanceStatus() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.surface == nil || !c.statusModelsCached {
		return
	}

	model := cloneChatStatusLineModel(c.persistentStatusModel)
	balance := chatSurfaceAccountBalanceStatusSegment(c.session)
	model = refreshChatAccountBalanceStatusModel(model, balance, ui.GetTerminalWidth())
	c.persistentStatusModel = cloneChatStatusLineModel(model)
	c.surface.SetStatusModels(model, cloneChatStatusLineModelPointer(c.dynamicStatusModel))
}

func refreshChatAccountBalanceStatusModel(model style.StatusLineModel, balance chatStatusSegment, width int) style.StatusLineModel {
	segments := make([]style.StatusSegment, 0, len(model.Segments)+1)
	insertAt := -1
	hadBalance := false
	useFullText := false
	for _, segment := range model.Segments {
		if segment.Kind == style.StatusSegBalance {
			if insertAt < 0 {
				insertAt = len(segments)
				hadBalance = true
				useFullText = strings.HasPrefix(strings.TrimSpace(segment.Text), "Balance ")
			}
			continue
		}
		segments = append(segments, segment)
	}
	if balance.full != "" {
		if insertAt < 0 {
			insertAt = chatAccountBalanceStatusInsertIndex(segments)
		}
		text := balance.compact
		if text == "" {
			text = balance.full
		}
		if useFullText {
			text = balance.full
		}
		segment := style.StatusSegment{
			Kind: style.StatusSegBalance,
			Text: text,
			Role: style.RoleSuccess,
		}
		segments = append(segments, style.StatusSegment{})
		copy(segments[insertAt+1:], segments[insertAt:])
		segments[insertAt] = segment
	}
	model.Segments = segments
	for index := range model.Segments {
		model.Segments[index].Priority = index
	}
	if !hadBalance && balance.full != "" && insertAt >= 0 && balance.full != model.Segments[insertAt].Text {
		if width <= 0 {
			width = 80
		}
		candidate := cloneChatStatusLineModel(model)
		candidate.Segments[insertAt].Text = balance.full
		if ui.DisplayWidth(style.StatusLineDocument(candidate, 0).PlainText()) <= width {
			model.Segments[insertAt].Text = balance.full
		}
	}
	return model
}

func chatAccountBalanceStatusInsertIndex(segments []style.StatusSegment) int {
	for index, segment := range segments {
		if segment.Kind == style.StatusSegProvider {
			return index + 1
		}
	}
	for index, segment := range segments {
		if segment.Kind == style.StatusSegModel {
			return index + 1
		}
	}
	return len(segments)
}

func (c *chatInteractionCoordinator) updateDynamicStatusClockLocked(state string, now time.Time) {
	if !chatSurfaceStateIsRunning(state) {
		c.dynamicStatusStarted = time.Time{}
		c.stopDynamicStatusTickLocked()
		return
	}
	if c.dynamicStatusStarted.IsZero() {
		c.dynamicStatusStarted = now
		// Starting a new turn replaces the previous frozen completion summary.
		c.dynamicStatusCompletedElapsed = 0
		c.dynamicStatusCompleted = false
	}
}

func (c *chatInteractionCoordinator) dynamicStatusElapsedLocked(now time.Time) time.Duration {
	if c.dynamicStatusCompleted {
		return c.dynamicStatusCompletedElapsed
	}
	if c.dynamicStatusStarted.IsZero() || now.Before(c.dynamicStatusStarted) {
		return 0
	}
	return now.Sub(c.dynamicStatusStarted)
}

func (c *chatInteractionCoordinator) scheduleDynamicStatusTickLocked(now time.Time) {
	if c == nil || c.shutdown || c.dynamicStatusStarted.IsZero() || !c.surfaceOutputActiveLocked() || !c.surface.DynamicStatusTicksEnabled() || c.dynamicStatusTimer != nil {
		return
	}
	elapsed := c.dynamicStatusElapsedLocked(now)
	delay := time.Second - elapsed%time.Second
	if delay < 10*time.Millisecond {
		delay = time.Second
	}
	c.dynamicStatusTimerSeq++
	sequence := c.dynamicStatusTimerSeq
	c.dynamicStatusTimer = time.AfterFunc(delay, func() {
		c.refreshDynamicStatusTick(sequence)
	})
}

func (c *chatInteractionCoordinator) refreshDynamicStatusTick(sequence uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if sequence != c.dynamicStatusTimerSeq {
		return
	}
	c.dynamicStatusTimer = nil
	if c.shutdown || c.dynamicStatusStarted.IsZero() || !c.surfaceOutputActiveLocked() {
		return
	}
	now := time.Now()
	c.surface.SetDynamicStatusModel(buildChatDynamicStatusModelForWidthInputModeAndCompletion(
		c.currentSurfaceStateLocked(),
		ui.GetTerminalWidth(),
		c.inputMode,
		c.dynamicStatusElapsedLocked(now),
		c.dynamicStatusCompleted,
	))
	c.scheduleDynamicStatusTickLocked(now)
}

func (c *chatInteractionCoordinator) stopDynamicStatusTickLocked() {
	if c == nil {
		return
	}
	c.dynamicStatusTimerSeq++
	if c.dynamicStatusTimer != nil {
		c.dynamicStatusTimer.Stop()
	}
	c.dynamicStatusTimer = nil
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
	// Soft-committed drain re-establishes ownership after its writes. Any other
	// output (tool results, notices, raw stream fallback) breaks the 1:1 soft
	// window and must drop rewrite tracking immediately.
	if !c.softCommitWriting {
		c.invalidateSoftEmittedOwnershipLocked()
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
	// Row terminator is owned here. Callers must pass lines without trailing LF
	// (use normalizeWriteLines / buildRenderedAssistantChunk).
	c.writeTextLocked(text + "\n")
}

// closeOpenRowLocked closes a half-written stream row only. It does not insert
// a cross-block gap; callers that need message separation pass gapBlank to
// writeRowsLocked / writeCompleteBlockLocked.
//
// streamTrailingLF is the single cursor-at-row-start flag for the assistant
// stream path (Phase C). true = cursor already at column 0 after a row
// terminator (or never opened a row). false = mid-line content was written
// without LF. resetStream / stream start keep it true so zero-value false is
// no longer confused with "open mid-line".
func (c *chatInteractionCoordinator) closeOpenRowLocked() {
	if c == nil || c.streamTrailingLF {
		return
	}
	c.writeLineLocked("")
	c.streamTrailingLF = true
}

// ensureStreamTerminatedLocked closes an open stream row and records that the
// row is terminated. Historical call sites often wrote writeLineLocked("")
// without flipping streamTrailingLF, which let the next boundary inject a
// second blank. Prefer closeOpenRowLocked for new code; this name remains as
// the stream-path alias used throughout residual/finalize call sites.
func (c *chatInteractionCoordinator) ensureStreamTerminatedLocked() {
	c.closeOpenRowLocked()
}

// writeRowsLocked is the single complete-block writer: optional explicit gap,
// then atomic multi-row emission. Gap is never inferred from completeBlockOutput
// — that flag is only read by gapFor* helpers at top-level message boundaries.
// Soft stable commits intentionally bypass this path (see drainActiveStableCommitLocked)
// so closeOpenRow / gapBlank cannot touch already-rendered scrollback.
func (c *chatInteractionCoordinator) writeRowsLocked(rows []string, gap blockGap) {
	if c == nil || len(rows) == 0 {
		return
	}
	c.closeOpenRowLocked()
	if gap == gapBlank {
		c.writeLineLocked("")
	}
	// One write for the whole block: per-row WriteOutput releases the surface
	// lock between lines and lets ActiveBand/status growth insert permanent
	// holes into already-scrolled content (visible on long "• Edited" diffs).
	c.writeTextLocked(strings.Join(rows, "\n") + "\n")
	c.streamTrailingLF = true
	c.completeBlockOutput = true
}

// gapBeforeBlockLocked is the single spacing policy for a new complete block.
// Every independently committed cell gets one leading blank when another
// complete block already precedes it. Intra-cell rows (for example a Completed
// tool header plus its output preview) remain dense because they are emitted by
// one DisplayLines call. Running never enters history, so it must not mutate
// cross-cell spacing state before Completed/Failed is committed.
func (c *chatInteractionCoordinator) gapBeforeBlockLocked() blockGap {
	if c != nil && c.completeBlockOutput {
		return gapBlank
	}
	return gapNone
}

// gapForTopLevelMessage maps the historical suppressSeparator rule for
// assistant/error one-shots onto an explicit gap. completeBlockOutput is read
// only at the message boundary — writeRowsLocked itself never invents a blank
// from that flag (which is what polluted mid-stream residual paints).
func (c *chatInteractionCoordinator) gapForTopLevelMessage() blockGap {
	return c.gapBeforeBlockLocked()
}

// gapForEventBlock applies the same boundary rule to tool, timeline and
// supplement cells. Denseness belongs inside a cell, never across independent
// events.
func (c *chatInteractionCoordinator) gapForEventBlock() blockGap {
	return c.gapBeforeBlockLocked()
}

// gapIfPriorComplete inserts a blank only when a prior complete block is on
// screen. Used for reasoning supplements that follow assistant content.
func (c *chatInteractionCoordinator) gapIfPriorComplete() blockGap {
	if c != nil && c.completeBlockOutput {
		return gapBlank
	}
	return gapNone
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
	if writeGap {
		c.writePromptGapLocked()
	}
}

func (c *chatInteractionCoordinator) writeFormatLocked(format string, args ...interface{}) {
	if c == nil || c.writer == nil || format == "" {
		return
	}
	if c.surfaceWriter && c.surface != nil {
		c.writeTextLocked(fmt.Sprintf(format, args...))
		return
	}
	_, _ = ui.WriteTerminalFormat(c.writer, format, args...)
}

func (c *chatInteractionCoordinator) writeSurfaceOutputTextLocked(text string) bool {
	if c == nil || !c.surfaceWriter || c.surface == nil || text == "" || !c.surfaceOutputActiveLocked() {
		return false
	}
	// Soft-commit drain is the only path that may open/extend the rewrite
	// window. Plain WriteOutput invalidates soft ownership at the surface.
	var handled bool
	if c.softCommitWriting {
		_, _, handled = c.surface.WriteSoftTrackedOutput(c.writer, text)
	} else {
		_, _, handled = c.surface.WriteOutput(c.writer, text)
	}
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

// chatAgentStageDetailMaxWidth keeps tool progress richer for ActiveBand.
// The fixed-bottom status line still width-compacts via full/compact segments.
const chatAgentStageDetailMaxWidth = 96

// SetAgentStageDetail also exposes the active operation, such as a tool name,
// while keeping the stage itself available for narrow-terminal fallback.
//
// Tool-running details may include progress ("shell 45% downloading"). The
// wider budget above lets ActiveBand show progress text; status bar fit logic
// still drops to the compact "执行工具" label on narrow terminals.
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
	c.agentStageDetail = compactStatusValue(strings.TrimSpace(detail), chatAgentStageDetailMaxWidth)
	if c.agentStage == chatAgentStageIdle {
		c.agentStageDetail = ""
	}
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	// Mirror tool-running into the surface ActiveBand (viewport only; no scrollback).
	c.syncAgentStageActiveBandLocked()
}

func (c *chatInteractionCoordinator) ClearAgentStage() {
	c.SetAgentStage(chatAgentStageIdle)
}

// SetToolAgentStage projects a runtime tool call into the mutable ActiveBand.
// Calls are keyed by tool_call_id when available so progress from an older
// concurrent tool cannot replace or clear the most recently started tool.
func (c *chatInteractionCoordinator) SetToolAgentStage(callID, detail string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.setToolAgentStageLocked(callID, detail, "")
}

// SetToolAgentStageDisplay projects a canonical shared Running row into the
// ActiveBand without retaining it in transcript history.
func (c *chatInteractionCoordinator) SetToolAgentStageDisplay(callID, detail, display string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.setToolAgentStageLocked(callID, detail, display)
}

func (c *chatInteractionCoordinator) setToolAgentStageLocked(callID, detail, display string) {
	detail = compactStatusValue(strings.TrimSpace(detail), chatAgentStageDetailMaxWidth)
	key := activeToolStageKey(callID, detail)
	if key == "" {
		return
	}
	if c.activeTools == nil {
		c.activeTools = make(map[string]chatActiveTool)
	}
	tool, exists := c.activeTools[key]
	if !exists {
		c.activeToolSequence++
		tool = chatActiveTool{key: key, sequence: c.activeToolSequence}
	}
	if detail != "" {
		tool.detail = detail
	}
	// Progress updates do not own the canonical requested row. Preserve the
	// command/source/argument display until the matching final event removes it.
	if strings.TrimSpace(display) != "" || !exists {
		tool.display = strings.TrimRight(display, "\r\n")
	}
	c.activeTools[key] = tool
	c.projectActiveToolStageLocked()
}

// FinishToolAgentStage removes only the matching call. A late completion for
// an older tool therefore leaves a newer running tool visible.
func (c *chatInteractionCoordinator) FinishToolAgentStage(callID, toolName string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || len(c.activeTools) == 0 {
		return
	}
	c.finishToolAgentStageLocked(callID, toolName)
}

func (c *chatInteractionCoordinator) finishToolAgentStageLocked(callID, toolName string) {
	key := activeToolStageKey(callID, toolName)
	if key == "" {
		return
	}
	if _, exists := c.activeTools[key]; !exists {
		if strings.TrimSpace(callID) != "" {
			return
		}
		// Legacy finish events may omit tool_call_id even when start included
		// it. In that degraded case remove the newest matching name only.
		name, _ := splitToolStageDetail(toolName)
		var match chatActiveTool
		for _, tool := range c.activeTools {
			toolName, _ := splitToolStageDetail(tool.detail)
			if toolName == name && tool.sequence > match.sequence {
				match = tool
			}
		}
		if match.key == "" {
			return
		}
		key = match.key
	}
	delete(c.activeTools, key)
	c.projectActiveToolStageLocked()
}

func activeToolStageKey(callID, detail string) string {
	if callID = strings.TrimSpace(callID); callID != "" {
		return "call:" + callID
	}
	name, _ := splitToolStageDetail(detail)
	if name == "" {
		return ""
	}
	return "name:" + name
}

func (c *chatInteractionCoordinator) projectActiveToolStageLocked() {
	selected := c.newestActiveToolLocked()
	if selected.sequence == 0 {
		c.agentStage = chatAgentStagePlanning
		c.agentStageDetail = ""
	} else {
		c.agentStage = chatAgentStageToolRunning
		c.agentStageDetail = selected.detail
	}
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	c.syncAgentStageActiveBandLocked()
}

func (c *chatInteractionCoordinator) newestActiveToolLocked() chatActiveTool {
	var selected chatActiveTool
	for _, tool := range c.activeTools {
		if tool.sequence > selected.sequence {
			selected = tool
		}
	}
	return selected
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
	c.finishWaiting(false)
}

// CompleteWaiting freezes the elapsed time for the completed turn and keeps a
// compact summary above the ready prompt (for example, "Worked for 1m 21s").
// The next StartWaiting call replaces that summary with live activity.
func (c *chatInteractionCoordinator) CompleteWaiting() {
	c.finishWaiting(true)
}

func (c *chatInteractionCoordinator) finishWaiting(completed bool) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	c.mu.Lock()
	if c.shutdown || !c.waitingActive {
		c.mu.Unlock()
		return
	}
	if completed {
		now := time.Now()
		elapsed := c.dynamicStatusElapsedLocked(now)
		c.dynamicStatusCompletedElapsed = elapsed
		c.dynamicStatusCompleted = true
	} else {
		c.dynamicStatusCompletedElapsed = 0
		c.dynamicStatusCompleted = false
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

const chatSurfaceStatusSeparator = " · "

type chatStatusSegment struct {
	full    string
	compact string
	kind    style.StatusSegmentKind
	role    style.Role
}

type fittedChatStatusSegment struct {
	chatStatusSegment
	text string
}

func buildChatSurfaceStatusModelForWidthAndInputMode(session *ChatSession, state string, width int, inputMode chatInputMode) style.StatusLineModel {
	state, width = normalizeChatSurfaceStatusInput(state, width)
	fitted := fitChatSurfaceStatusSegments(buildChatSurfaceStatusSegments(session, state, inputMode), width)
	if len(fitted) == 0 {
		return style.StatusLineModel{State: style.RunReady}
	}

	model := style.StatusLineModel{HideState: true, Separator: chatSurfaceStatusSeparator}
	start := 0
	if fitted[0].kind == style.StatusSegState {
		model.HideState = false
		model.State = chatStatusRunState(fitted[0].role)
		model.StateText = fitted[0].text
		model.StateRole = fitted[0].role
		start = 1
	}
	for i := start; i < len(fitted); i++ {
		segment := fitted[i]
		role := segment.role
		if role == "" {
			role = style.RoleTextMuted
		}
		model.Segments = append(model.Segments, style.StatusSegment{
			Kind:     segment.kind,
			Text:     segment.text,
			Priority: i,
			Role:     role,
		})
	}
	return model
}

func buildChatPersistentStatusModelForWidth(session *ChatSession, width int) style.StatusLineModel {
	return buildChatSurfaceStatusModelForWidthAndInputMode(session, "Ready", width, chatInputModeChat)
}

func buildChatDynamicStatusModelForWidthAndInputMode(state string, width int, inputMode chatInputMode, elapsed time.Duration) *style.StatusLineModel {
	return buildChatDynamicStatusModelForWidthInputModeAndCompletion(state, width, inputMode, elapsed, false)
}

func buildChatDynamicStatusModelForWidthInputModeAndCompletion(state string, width int, inputMode chatInputMode, elapsed time.Duration, completed bool) *style.StatusLineModel {
	state, width = normalizeChatSurfaceStatusInput(state, width)
	if completed {
		text := "Worked for " + formatChatDynamicStatusElapsed(elapsed)
		if ui.DisplayWidth(text) > width {
			text = compactStatusValue(text, width)
		}
		return &style.StatusLineModel{
			State:     style.RunReady,
			StateText: text,
			StateRole: style.RoleSuccess,
		}
	}
	action, role, interruptible := chatDynamicStatusAction(state, inputMode)
	if action == "" {
		return nil
	}
	suffix := ""
	if interruptible {
		suffix = fmt.Sprintf(" (%s • esc to interrupt)", formatChatDynamicStatusElapsed(elapsed))
	}
	if budget := width - ui.DisplayWidth("◦ "+suffix); budget >= 4 {
		action = compactStatusValue(action, budget)
	}
	text := "◦ " + action + suffix
	model := &style.StatusLineModel{
		State:     chatStatusRunState(role),
		StateText: text,
		StateRole: role,
	}
	return model
}

func chatDynamicStatusAction(state string, inputMode chatInputMode) (string, style.Role, bool) {
	switch normalizeChatInputMode(inputMode) {
	case chatInputModeApproval:
		return "Waiting for approval", style.RoleApproval, true
	case chatInputModeAnswer:
		return "Waiting for answer", style.RoleWarning, true
	case chatInputModeSelection:
		return "Selecting an option", style.RoleInfo, false
	case chatInputModeConfirmation:
		return "Waiting for confirmation", style.RoleWarning, false
	case chatInputModeSecret:
		return "Waiting for credentials", style.RoleWarning, false
	case chatInputModePanel:
		return "Navigating panel", style.RoleInfo, false
	}

	normalized := strings.ToLower(strings.TrimSpace(state))
	if strings.HasPrefix(normalized, "tool ") {
		detail := compactStatusValue(strings.TrimSpace(state[len("tool "):]), chatAgentStageDetailMaxWidth)
		if detail == "" {
			detail = "tool"
		}
		return "Running " + detail, style.RoleTool, true
	}
	switch normalized {
	case "waiting", "thinking", "planning":
		return "Analyzing", style.RoleReasoning, true
	case "streaming":
		return "Generating response", style.RoleProgress, true
	case "tool running":
		return "Running tool", style.RoleTool, true
	case "awaiting approval":
		return "Waiting for approval", style.RoleApproval, true
	case "awaiting answer":
		return "Waiting for answer", style.RoleWarning, true
	case "stopping":
		return "Stopping", style.RoleWarning, true
	case "retrying":
		return "Retrying", style.RoleWarning, true
	case "running", "working", "busy":
		return "Working", style.RoleProgress, true
	default:
		return "", "", false
	}
}

func formatChatDynamicStatusElapsed(elapsed time.Duration) string {
	seconds := int64(elapsed / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	remainingSeconds := seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, remainingSeconds)
	}
	hours := minutes / 60
	remainingMinutes := minutes % 60
	return fmt.Sprintf("%dh %dm %ds", hours, remainingMinutes, remainingSeconds)
}

func normalizeChatSurfaceStatusInput(state string, width int) (string, int) {
	state = strings.TrimSpace(state)
	if state == "" {
		state = "Ready"
	}
	if width <= 0 {
		width = 80
	}
	return state, width
}

func chatStatusRunState(role style.Role) style.RunState {
	switch role {
	case style.RoleReasoning:
		return style.RunThinking
	case style.RoleTool, style.RoleProgress:
		return style.RunStreaming
	case style.RoleApproval, style.RoleWarning:
		return style.RunWaiting
	case style.RoleError:
		return style.RunError
	default:
		return style.RunRunning
	}
}

func buildChatSurfaceStatusSegments(session *ChatSession, state string, inputMode chatInputMode) []chatStatusSegment {
	segments := make([]chatStatusSegment, 0, 12)

	// Keep this projection capable of describing a complete status model for
	// non-surface consumers. The fixed-bottom production path requests a Ready
	// projection and renders live state in its dedicated composer-adjacent row.
	if modal := chatSurfaceModalStatusSegment(state, inputMode); modal.full != "" {
		segments = append(segments, presentChatStatusSegment(modal, style.StatusSegState, chatSurfaceModalStatusRole(state, inputMode)))
	}
	queuedCount, _ := queuedInteractiveInputState(session)
	if queuedCount > 0 {
		segments = append(segments, presentChatStatusSegment(chatStatusSegment{
			full:    fmt.Sprintf("队列 %d", queuedCount),
			compact: fmt.Sprintf("队%d", queuedCount),
		}, style.StatusSegMode, style.RoleWarning))
	}
	// Plan mode is an interactive composer mode rather than a transient agent
	// stage. Keep its compact ON/OFF state near the front so it remains visible
	// in the fixed-bottom status bar even when optional diagnostics are dropped.
	if planSeg := chatSurfacePlanModeStatusSegment(session); planSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(planSeg, style.StatusSegMode, style.RoleAccent))
	}
	// Codex-style goal indicator: keep near the front so residual active goals
	// remain visible even when width drops optional diagnostics.
	if goalSeg := chatSurfaceGoalStatusSegment(session); goalSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(goalSeg, style.StatusSegMode, style.RoleProgress))
	}

	// Reference diagnostics: model · provider · Context N% · cwd · [project] · branch · window · in · out · Fast
	if model := chatSurfaceModelStatusSegment(session); model.full != "" {
		segments = append(segments, presentChatStatusSegment(model, style.StatusSegModel, style.RoleAccent))
	}
	if providerSeg := chatSurfaceProviderStatusSegment(session); providerSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(providerSeg, style.StatusSegProvider, style.RoleTextSecondary))
	}
	if balanceSeg := chatSurfaceAccountBalanceStatusSegment(session); balanceSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(balanceSeg, style.StatusSegBalance, style.RoleSuccess))
	}
	if contextSeg := chatSurfaceContextUsedStatusSegment(session); contextSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(contextSeg, style.StatusSegUsage, style.RoleProgress))
	}
	cwdSeg := chatSurfaceDirectoryStatusSegment(session)
	if cwdSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(cwdSeg, style.StatusSegPath, style.RoleTextSecondary))
	}
	// Project is only useful when it adds identity beyond the cwd label. At the
	// repo root, basename(cwd) == project and the status bar would otherwise
	// render the same name twice (e.g. "ai-agent-runtime · ai-agent-runtime").
	if projectSeg := chatSurfaceProjectStatusSegment(session); projectSeg.full != "" &&
		!chatStatusProjectRedundantWithDirectory(cwdSeg, projectSeg) {
		segments = append(segments, presentChatStatusSegment(projectSeg, style.StatusSegPath, style.RoleTextMuted))
	}
	if branchSeg := chatSurfaceGitBranchStatusSegment(session); branchSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(branchSeg, style.StatusSegMeta, style.RoleInfo))
	}
	if windowSeg := chatSurfaceWindowStatusSegment(session); windowSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(windowSeg, style.StatusSegUsage, style.RoleTextMuted))
	}
	if inSeg := chatSurfaceInputTokensStatusSegment(session); inSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(inSeg, style.StatusSegUsage, style.RoleTextMuted))
	}
	if outSeg := chatSurfaceOutputTokensStatusSegment(session); outSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(outSeg, style.StatusSegUsage, style.RoleTextMuted))
	}
	if fastSeg := chatSurfaceFastStatusSegment(session); fastSeg.full != "" {
		segments = append(segments, presentChatStatusSegment(fastSeg, style.StatusSegMode, style.RoleAccent))
	}
	return segments
}

func chatSurfaceAccountBalanceStatusSegment(session *ChatSession) chatStatusSegment {
	account, siteType, confidence := currentChatAccountBalance(session)
	if account == nil {
		return chatStatusSegment{}
	}
	view := accountViewFromProviderSnapshot(account, siteType, confidence)
	if view.BalanceValue == nil {
		if formatProviderAccountBalanceLine(account, siteType, confidence) == "" {
			return chatStatusSegment{}
		}
		return chatStatusSegment{full: "Balance synced", compact: "Bal synced"}
	}
	unit := strings.TrimSpace(view.DisplayUnit)
	if unit == "" {
		unit = strings.TrimSpace(view.Currency)
	}
	value := siteaccount.FormatBalanceAmount(*view.BalanceValue)
	if unit != "" {
		value += " " + unit
	}
	return chatStatusSegment{
		full:    "Balance " + value,
		compact: "Bal " + value,
	}
}

func presentChatStatusSegment(segment chatStatusSegment, kind style.StatusSegmentKind, role style.Role) chatStatusSegment {
	segment.kind = kind
	segment.role = role
	return segment
}

func fitChatSurfaceStatusSegments(segments []chatStatusSegment, width int) []fittedChatStatusSegment {
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
	fitted := make([]fittedChatStatusSegment, 0, len(parts))
	for i, value := range parts {
		fitted = append(fitted, fittedChatStatusSegment{
			chatStatusSegment: segments[i],
			text:              value,
		})
	}
	return fitted
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

func chatSurfaceModalStatusRole(state string, inputMode chatInputMode) style.Role {
	switch normalizeChatInputMode(inputMode) {
	case chatInputModeApproval:
		return style.RoleApproval
	case chatInputModeAnswer, chatInputModeSelection, chatInputModeConfirmation,
		chatInputModeSecret, chatInputModePanel:
		return style.RoleWarning
	}
	normalized := strings.ToLower(strings.TrimSpace(state))
	if strings.HasPrefix(normalized, "tool ") {
		return style.RoleTool
	}
	switch normalized {
	case "thinking":
		return style.RoleReasoning
	case "streaming", "planning", "tool running":
		return style.RoleTool
	case "waiting", "awaiting approval", "awaiting answer", "stopping":
		return style.RoleWarning
	case "failed", "error":
		return style.RoleError
	default:
		return style.RoleInfo
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

// chatStatusProjectRedundantWithDirectory reports whether the project segment
// would visually duplicate the directory segment. Keep the directory segment
// (it can expand to a full path when width allows) and drop the project name.
func chatStatusProjectRedundantWithDirectory(cwdSeg, projectSeg chatStatusSegment) bool {
	project := strings.TrimSpace(projectSeg.full)
	if project == "" {
		return true
	}
	projectCompact := strings.TrimSpace(projectSeg.compact)
	if cwdCompact := strings.TrimSpace(cwdSeg.compact); cwdCompact != "" {
		if strings.EqualFold(cwdCompact, project) ||
			(projectCompact != "" && strings.EqualFold(cwdCompact, projectCompact)) {
			return true
		}
	}
	cwdFull := strings.TrimSpace(cwdSeg.full)
	if cwdFull == "" {
		return false
	}
	base := filepath.Base(filepath.Clean(cwdFull))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return false
	}
	if strings.EqualFold(base, project) {
		return true
	}
	if projectCompact != "" && strings.EqualFold(base, projectCompact) {
		return true
	}
	return false
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
	if !c.beginMessageLocked() {
		return
	}
	response = sanitizeInteractiveAsyncTeamLaunchResponse(response)
	// Finalized assistant turn routed through the retained-source cell model
	// (P5.4): keep raw source on assistantStreamCell so DisplayLines can reflow
	// at any width. Markdown still goes through the session formatter.
	var cell historyCell
	if c.session.Formatter != nil && c.session.Formatter.IsMarkdown(response) {
		formatFn := func(source string, width int) string {
			prev := c.session.Formatter.Width
			if width > 0 {
				c.session.Formatter.Width = width
			}
			out := c.session.Formatter.Format(source)
			c.session.Formatter.Width = prev
			return out
		}
		cell = newAssistantStreamCellWithFormatter(response, true, formatFn)
	} else {
		cell = newAssistantStreamCell(response, false)
	}
	c.commitHistoryCellLocked(cell, c.gapForTopLevelMessage())
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
		// Dividers below end with writeLineLocked, so the cursor is at a row
		// boundary. Keep reasoningTrailingLF true until a mid-line delta flips it.
		c.reasoningTrailingLF = true
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
		c.streamEnqueuedPrefixLen = 0
		// Cursor starts at row boundary. First delta (if any) may open a mid-line row
		// and flip streamTrailingLF=false via writeIndentedStreamingDeltaLocked.
		c.streamTrailingLF = true
		c.streamLines = 0
		c.streamBuffer.Reset()
		if c.activeStream != nil {
			c.activeStream.BeginAssistant("assistant")
		}
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
				c.streamEnqueuedPrefixLen = c.streamRenderedPrefixLen
			}
			c.streamMode = assistantStreamModeMarkdown
			if previousMode != assistantStreamModeMarkdown {
				c.rebuildPendingStableCommitLocked(true)
			}
		} else if c.streamMode == assistantStreamModeUnknown && nextMode == assistantStreamModeText {
			c.streamMode = assistantStreamModeText
		}
	}

	// Phase 5 active cell: track stable markdown / coalesced frames without
	// writing extra scrollback. Transcript still follows the paths below.
	// newlyStable is the controller's stable plain cut (full delta for text mode).
	newlyStable := c.paintActiveStreamLocked(delta, c.streamMode == assistantStreamModeMarkdown)

	if c.shouldLiveStreamOutputLocked() && !c.reasoningActive {
		if c.streamMode == assistantStreamModeMarkdown {
			// Keep markdown holdback until finalize so formatted blocks land once.
			// Surface path already mirrors progress via ActiveBand above.
			return
		}
		// No-surface plain-text live stream: emit already-stable text only.
		// Text mode treats each delta as stable; empty means nothing new to write.
		if newlyStable == "" && c.streamRendered {
			return
		}
		if !c.streamRendered {
			// First activation writes the full classified buffer so far.
			c.writeIndentedStreamingDeltaLocked(c.streamBuffer.String(), ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
			return
		}
		c.writeIndentedStreamingDeltaLocked(newlyStable, ui.AssistantContentIndent(), &c.streamRendered, &c.streamTrailingLF)
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
		finalContent = c.finalizeActiveAssistantLocked("", c.streamMode == assistantStreamModeMarkdown)
	}
	if finalContent == "" {
		c.resetStreamLocked()
		return true
	}
	finalContent = sanitizeInteractiveAsyncTeamLaunchResponse(finalContent)
	if c.streamMode != assistantStreamModeMarkdown && c.classifyAssistantStreamModeLocked(finalContent) == assistantStreamModeMarkdown {
		if c.streamRendered && c.streamRenderedPrefixLen == 0 {
			c.streamRenderedPrefixLen = len(c.streamBuffer.String())
			c.streamEnqueuedPrefixLen = c.streamRenderedPrefixLen
		}
		c.streamMode = assistantStreamModeMarkdown
		c.rebuildPendingStableCommitLocked(true)
	}
	c.reconcilePendingStableCommitLocked(finalContent)
	if consolidated := c.finalizeActiveAssistantLocked(finalContent, c.streamMode == assistantStreamModeMarkdown); consolidated != "" {
		finalContent = consolidated
	}
	if c.streamMode == assistantStreamModeMarkdown || c.streamRenderedPrefixLen > 0 {
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
		if c.streamRendered {
			c.ensureStreamTerminatedLocked()
		}
		c.resetStreamLocked()
		return true
	}
	formatted := finalContent
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(finalContent)
	}
	// One-shot completion of the same assistant body: no cross-block gap.
	c.writeCompleteBlockLocked(ui.FormatAssistantRendered(formatted), gapNone)
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
		// Reasoning supplement is its own block after the assistant body.
		c.writeCompleteBlockLocked(ui.FormatAssistantSupplementBlock(strings.Join(lines, "\n")), c.gapIfPriorComplete())
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
	c.reconcilePendingStableCommitLocked(content)
	if consolidated := c.finalizeActiveAssistantLocked(content, c.streamMode == assistantStreamModeMarkdown); consolidated != "" {
		content = consolidated
	}
	if c.streamRenderedPrefixLen > 0 && content != "" {
		content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
		c.renderFormattedAssistantStreamLocked(content)
		c.resetStreamLocked()
		return
	}
	if c.shouldLiveStreamOutputLocked() {
		if c.streamMode == assistantStreamModeMarkdown {
			content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
			c.renderFormattedAssistantStreamLocked(content)
			c.resetStreamLocked()
			return
		}
		if c.streamRendered {
			c.ensureStreamTerminatedLocked()
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
		c.writeCompleteBlockLocked(ui.FormatAssistantRendered(formatted), gapNone)
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
		c.writeCompleteBlockLocked(ui.FormatAssistantSupplementBlock(strings.Join(lines, "\n")), c.gapIfPriorComplete())
	}
	c.resetReasoningLocked()
}

func (c *chatInteractionCoordinator) RenderAsyncLine(line string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || strings.TrimSpace(line) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.beginMessageLocked() {
		return
	}
	// Each async/supplement event is an independent retained cell. It receives
	// one block-level separator; multiline content inside the cell stays dense.
	c.commitHistoryCellLocked(
		newSupplementLineCell(line),
		c.gapForEventBlock(),
	)
}

// RenderToolChainEvent routes tool_requested / tool_result through toolChainCell
// (P5.6). Running is viewport-only; Completed/Failed is the one final history
// cell for that tool. Non-tool stages fall back to the supplement path.
// Returns false when the event produces no visible rows (batch_start/end, empty).
func (c *chatInteractionCoordinator) RenderToolChainEvent(event runtimechatcore.ChatEvent) bool {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return false
	}
	switch strings.TrimSpace(event.Stage) {
	case "tool_requested":
		// Running stays in viewport (ActiveBand via SetToolAgentStage /
		// activeStream). Do NOT commit to history — only commit on final
		// tool_result so scrollback never contains a Running row.
		rendered := renderSharedChatToolEvent(event)
		if strings.TrimSpace(rendered) == "" {
			return false
		}
		// Flush any preceding assistant/reasoning block before the ActiveBand
		// takes over, but do not mutate history spacing: Running is not a
		// committed cell.
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.beginMessageLocked() {
			return false
		}
		c.setToolAgentStageLocked(event.ToolCallID, event.ToolName, rendered)
		return true
	case "tool_result":
		cell := newToolChainCellFromEvent(event)
		if len(cell.DisplayLines(0)) == 0 {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.beginMessageLocked() {
			return false
		}
		c.finishToolAgentStageLocked(event.ToolCallID, event.ToolName)
		c.commitHistoryCellLocked(cell, c.gapForEventBlock())
		return true
	default:
		// batch_start / batch_end / unknown: keep prior empty-string contract.
		rendered := renderSharedChatToolEvent(event)
		if strings.TrimSpace(rendered) == "" {
			return false
		}
		c.RenderAsyncLine(rendered)
		return true
	}
}

// RenderAsyncDocument writes a typed timeline/info document without routing it
// through the legacy supplement keyword parser. Runtime event bridges should
// prefer this entry whenever the event already owns its semantic kind/status.
func (c *chatInteractionCoordinator) RenderAsyncDocument(doc render.Document) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return
	}
	if strings.TrimSpace(ui.RenderDocumentPlain(doc)) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.beginMessageLocked() {
		return
	}
	// Typed timeline/tool/info document routed through the cell model
	// (P4.2/P5.6). Cross-event spacing is applied at this cell boundary.
	c.commitHistoryCellLocked(
		newAsyncDocumentCell(doc),
		c.gapForEventBlock(),
	)
}

// RenderCommandDocument commits one structured command result as one retained
// cell. It deliberately has no raw-output fallback: after structured dispatch
// matches a command, the interaction coordinator remains the only terminal
// writer for the owned lifecycle.
func (c *chatInteractionCoordinator) RenderCommandDocument(doc render.Document) bool {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return false
	}
	if strings.TrimSpace(ui.RenderDocumentPlain(doc)) == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.beginMessageLocked() {
		return false
	}
	c.commandCellSequence++
	c.lastCommandCellID = fmt.Sprintf("command:%d", c.commandCellSequence)
	c.commitHistoryCellLocked(
		newCommandResultCell(c.lastCommandCellID, c.commandCellSequence, doc),
		c.gapForEventBlock(),
	)
	return true
}

func (c *chatInteractionCoordinator) RenderSubmittedUserInput(input string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || strings.TrimSpace(input) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renderUserEchoLocked(input, true)
}

// RenderReplayedUserInput echoes a user message from already-final history.
// Unlike the live submit path it never restores the composer, so replaying
// history cannot grow the bottom reserve and cannot bill surface scroll
// compensation into the replayed transcript. Replay intent is explicit at the
// call site instead of a coordinator-wide mode flag (P2: pure replay path).
func (c *chatInteractionCoordinator) RenderReplayedUserInput(input string) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || strings.TrimSpace(input) == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renderUserEchoLocked(input, false)
}

// renderUserEchoLocked writes a user-message block. allowPromptRestore gates the
// live-only composer re-show: the live submit path restores an empty composer
// when a turn was already armed (StartWaiting), while history replay passes
// false so the bottom reserve never grows between replayed messages.
func (c *chatInteractionCoordinator) renderUserEchoLocked(input string, allowPromptRestore bool) {
	// User echo must free the composer first. Leaving a surface prompt reserved
	// after the prior turn absorbs its trailing blank into the bottom pane, so
	// the next WriteOutput lands on the last history row and overwrites it
	// (no newline / visual overlap with history).
	wasWaiting := c.waitingActive
	if c.promptVisible {
		c.clearVisiblePromptLocked()
		c.promptVisible = false
		c.promptRenderedOnSurface = false
	}
	if !c.beginMessageLocked() {
		return
	}
	// User echo sits directly under the cleared prompt; no extra blank. Routed
	// through the retained-source cell model (P4.1) so the same block can later
	// be re-rendered at a new width without mutating committed output. This is
	// behavior-identical to writeCompleteBlockLocked(FormatUserMessage): the cell
	// returns the same normalizeWriteLines rows.
	c.commitHistoryCellLocked(newUserMessageCell(input), gapNone)
	// StartWaiting may have already armed the turn; restore an empty composer so
	// queued input can continue above the status line. Replay passes
	// allowPromptRestore=false: growing the bottom reserve between replayed
	// messages would let the surface bill scroll compensation into the transcript.
	if allowPromptRestore && wasWaiting && !c.promptVisible && c.writer == os.Stdout && c.surface != nil {
		if c.surface.ShowPrompt(formatSessionUserPrompt(c.session)) {
			c.promptVisible = true
			c.promptRenderedOnSurface = true
		}
	}
}

func (c *chatInteractionCoordinator) RenderError(err error) {
	if c == nil || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || err == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.beginMessageLocked() {
		return
	}
	c.writeCompleteBlockLocked(
		ui.FormatErrorMessage(fmt.Sprintf("操作错误: %v", err)),
		c.gapForTopLevelMessage(),
	)
}

// ClearPrompt releases the prompt rows that are currently painted so direct
// output can own the cursor. The composer draft (text + cursor) is kept on
// purpose: turn boundaries, streaming output and async notices all clear the
// prompt, and none of them should discard what the user already typed. Callers
// that really want to drop the draft use DiscardPrompt or ResetPromptState.
func (c *chatInteractionCoordinator) ClearPrompt() {
	c.clearPrompt(false)
}

// DiscardPrompt clears the painted prompt rows and drops the composer draft.
// It is reserved for aborted reads and legacy inline prompts that take over the
// current line, where keeping the draft would resurrect text the user can no
// longer see next to the new prompt.
func (c *chatInteractionCoordinator) DiscardPrompt() {
	c.clearPrompt(true)
}

func (c *chatInteractionCoordinator) clearPrompt(dropDraft bool) {
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
	if dropDraft {
		c.promptInput = ""
		c.promptCursor = 0
	}
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
		fmt.Sprintf("stream_stable_queued=%d", len(c.stableCommitQueue)),
		fmt.Sprintf("stream_prefix_enqueued=%d", c.streamEnqueuedPrefixLen),
		fmt.Sprintf("stream_prefix_emitted=%d", c.streamRenderedPrefixLen),
	}
	if c.streamingActive && c.transcript != nil {
		parts = append(parts, c.transcript.debugParts()...)
	} else {
		parts = append(parts,
			fmt.Sprintf("stream_transcript_blocks=%d", c.streamLastTranscriptBlocks),
			fmt.Sprintf("stream_transcript_bytes=%d", c.streamLastTranscriptBytes),
			fmt.Sprintf("stream_final_divergence=%s", divergenceToken(c.streamLastFinalDivergence)),
		)
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
	c.agentStage = chatAgentStageIdle
	c.agentStageDetail = ""
	c.activeTools = nil
	c.activeToolSequence = 0
	if c.activeStream != nil && c.activeStream.IsToolActive() {
		c.stopActiveStreamFrameLocked()
		c.activeStream.Cancel()
		c.clearActiveBandLocked()
	}
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
	c.activeTools = nil
	c.inputModeBase = chatInputModeChat
	c.inputLease = nil
	c.inputMode = chatInputModeChat
	c.thinkingActive = false
	c.streamingActive = false
	c.reasoningActive = false
	c.completeBlockOutput = false
	c.stopActiveStreamFrameLocked()
	c.stopActiveStableCommitLocked()
	c.stopDynamicStatusTickLocked()
	c.dynamicStatusStarted = time.Time{}
	c.dynamicStatusCompletedElapsed = 0
	c.dynamicStatusCompleted = false
	if c.activeStream != nil {
		c.activeStream.Cancel()
	}
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

// clearActiveRunStateOnInterrupt clears every transient activity flag under one
// coordinator lock. The Stopping agent stage is intentionally preserved until
// actor/lease cleanup completes; once that stage is cleared, the derived
// surface state can therefore transition directly to Ready.
func (c *chatInteractionCoordinator) clearActiveRunStateOnInterrupt() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown {
		return
	}
	c.thinkingActive = false
	if c.reasoningActive {
		c.flushReasoningLocked()
		c.resetReasoningLocked()
	}
	if c.streamingActive {
		c.flushStreamLocked()
		c.resetStreamLocked()
	}
	c.waitingActive = false
	clearChatGoalStatusActiveTurnStarted(c.session)
	c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
}

// flushStreamLocked outputs any buffered streaming content before the stream
// is interrupted by another message (e.g. thinking, async line, new response).
// This prevents text from being silently swallowed when a ReAct loop's
// intermediate assistant deltas are never finalized via FinalizeAssistantDelta.
func (c *chatInteractionCoordinator) flushStreamLocked() {
	c.reconcilePendingStableCommitLocked(c.streamBuffer.String())
	if c.shouldLiveStreamOutputLocked() {
		if c.streamMode == assistantStreamModeMarkdown {
			content := c.streamBuffer.String()
			if strings.TrimSpace(content) == "" {
				c.ensureStreamTerminatedLocked()
				return
			}
			content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
			c.renderFormattedAssistantStreamLocked(content)
			return
		}
		c.renderBufferedAssistantStreamLocked()
		if c.streamRendered {
			c.ensureStreamTerminatedLocked()
		}
		return
	}
	content := c.streamBuffer.String()
	if strings.TrimSpace(content) == "" {
		c.ensureStreamTerminatedLocked()
		return
	}
	content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
	if c.streamRenderedPrefixLen > 0 {
		c.renderFormattedAssistantStreamLocked(content)
		return
	}
	formatted := content
	if c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(content)
	}
	// One-shot flush: same row contract as complete blocks (no multi-line blob).
	c.writeCompleteBlockLocked(ui.FormatAssistantRendered(formatted), gapNone)
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
	// Close an open reasoning mid-line the same way closeOpenRowLocked does for
	// the assistant stream — never invent a cross-block gap here.
	if c.reasoningRendered && !c.reasoningTrailingLF {
		c.writeLineLocked("")
		c.reasoningTrailingLF = true
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
	// Finalize left the band up so reconcile/residual computation had no empty
	// gap. Release it immediately before the final scrollback paint so the same
	// source is not dual-painted in ActiveBand and history.
	c.clearActiveBandLocked()
	if c.streamRendered && c.streamRenderedPrefixLen > 0 {
		c.writeResidualFormattedAssistantStreamLocked(content)
		return
	}

	formatted := content
	if c.session != nil && c.session.Formatter != nil {
		formatted = c.session.Formatter.Format(content)
	}
	// First full paint of the assistant body — no cross-block gap.
	c.writeCompleteBlockLocked(ui.FormatAssistantRendered(formatted), gapNone)
}

// writeResidualFormattedAssistantStreamLocked paints the still-unemitted tail
// after stable commits. Markdown residuals must use the same full-prefix
// Format differential as mid-stream stable chunks: Format(suffix-only) drops
// inter-block spacers (layout never pads before the first block), and Trim of
// both sides previously erased the blank that history replay keeps.
func (c *chatInteractionCoordinator) writeResidualFormattedAssistantStreamLocked(content string) {
	if c == nil {
		return
	}
	emitted := c.streamRenderedPrefixLen
	if emitted <= 0 {
		formatted := content
		if c.session != nil && c.session.Formatter != nil {
			formatted = c.session.Formatter.Format(content)
		}
		c.writeCompleteBlockLocked(ui.FormatAssistantRendered(formatted), gapNone)
		return
	}

	suffix := c.unrenderedAssistantStreamSuffixLocked(content)
	if strings.TrimSpace(suffix) == "" {
		c.ensureStreamTerminatedLocked()
		c.completeBlockOutput = true
		return
	}

	// Absolute prefix Format reads streamBuffer. Final snapshots (and
	// CompleteAssistantResponse replacements) may differ from the delta buffer,
	// so align the buffer with the authoritative body before diffing.
	if c.streamBuffer.String() != content {
		c.streamBuffer.Reset()
		c.streamBuffer.WriteString(content)
	}
	c.ensureAssistantTranscriptLocked()
	if c.transcript != nil {
		c.transcript.Source = content
	}

	useMarkdownDiff := c.streamMode == assistantStreamModeMarkdown
	if !useMarkdownDiff && c.session != nil && c.session.Formatter != nil {
		useMarkdownDiff = c.session.Formatter.IsMarkdown(content)
	}

	var chunk renderedAssistantChunk
	if useMarkdownDiff && c.session != nil && c.session.Formatter != nil {
		// Same row-delta contract as mid-stream stable commits. No leading-\\n
		// strip: blank ownership lives in "" rows from Format spacing.
		chunk = renderedAssistantChunk{lines: c.markdownRowsDeltaLocked(emitted, len(content), suffix)}
	} else {
		inlineContinuation := !c.streamTrailingLF && !strings.HasPrefix(suffix, "\n") && !strings.Contains(suffix, "\n")
		if inlineContinuation {
			// Close the open row with the residual text as a single line.
			c.writeLineLocked(ui.FormatAssistantRendered(suffix))
			c.streamTrailingLF = true
			c.streamRendered = true
			c.streamRenderedPrefixLen = len(content)
			c.completeBlockOutput = true
			return
		}
		c.ensureStreamTerminatedLocked()
		chunk = buildRenderedAssistantChunk(suffix)
	}

	if chunk.empty() {
		c.ensureStreamTerminatedLocked()
		c.streamRenderedPrefixLen = len(content)
		c.completeBlockOutput = true
		return
	}
	// Live plain/markdown may leave an open mid-row ("This is "). writeRowsLocked
	// always closeOpenRow first, which would force "This is \nbold\n". Continue the
	// open row with the first residual row, then emit any remaining rows atomically.
	if !c.streamTrailingLF {
		first := chunk.lines[0]
		rest := chunk.lines[1:]
		c.writeTextLocked(first)
		c.writeTextLocked("\n")
		c.streamTrailingLF = true
		if len(rest) > 0 {
			c.writeRowsLocked(rest, gapNone)
		}
	} else {
		// Multi-line residuals (tables, lists, code fences): one atomic WriteOutput
		// so layout/scroll cannot interleave between rows the way history's
		// one-shot RenderAssistant never does.
		c.writeRowsLocked(chunk.lines, gapNone)
	}
	c.streamTrailingLF = true
	c.streamRendered = true
	c.streamRenderedPrefixLen = len(content)
	c.completeBlockOutput = true
}

func (c *chatInteractionCoordinator) unrenderedAssistantStreamSuffixLocked(content string) string {
	if c == nil {
		return content
	}
	source := ""
	if c.transcript != nil {
		source = c.transcript.Source
	}
	if source == "" {
		source = c.streamBuffer.String()
	}
	// residualAfterEmittedPrefix knows whether emitted source was already
	// written to terminal and whether final diverged.
	residual, diverged := residualAfterEmittedPrefix(
		source,
		c.streamRenderedPrefixLen,
		content,
	)
	if diverged {
		// Final snapshot diverged from already-emitted terminal history.
		// Do not replay full corrected body (would duplicate stale scrollback).
		// Residual is empty; caller should not paint assistant body.
		return ""
	}
	return residual
}

func (c *chatInteractionCoordinator) reconcilePendingStableCommitLocked(finalContent string) {
	if c == nil {
		return
	}
	c.ensureAssistantTranscriptLocked()
	buffered := c.streamBuffer.String()
	c.transcript.syncFromCoordinator(
		buffered,
		c.streamRenderedPrefixLen,
		c.streamEnqueuedPrefixLen,
		c.streamMode == assistantStreamModeMarkdown,
	)
	c.transcript.noteFinalSnapshot(finalContent)
	kind := classifyAssistantFinalDivergence(
		buffered,
		c.streamRenderedPrefixLen,
		c.streamEnqueuedPrefixLen,
		finalContent,
	)
	c.transcript.applyFinalDivergence(kind)
	switch kind {
	case assistantFinalAppend:
		if len(c.stableCommitQueue) > 0 {
			c.drainActiveStableCommitLocked(true)
		}
	case assistantFinalQueueCorrect, assistantFinalEmittedDiverged, assistantFinalReplace:
		// Queued-but-unemitted content is still mutable. Drop it and let
		// residual rendering rebuild from the last emitted source prefix.
		// Emitted divergence cannot rewrite terminal history; residual helpers
		// suppress full replay of the corrected body.
		c.discardPendingStableCommitLocked()
	}
}

// finalizeActiveAssistantLocked consolidates the mutable viewport source at
// the same lifecycle boundary as the transcript commit. A non-empty final
// snapshot is authoritative and first replaces the delta-built source.
func (c *chatInteractionCoordinator) ensureAssistantTranscriptLocked() {
	if c == nil {
		return
	}
	if c.transcript == nil {
		c.transcript = &assistantTurnTranscript{}
	}
	if c.transcript.Source == "" {
		c.transcript.Source = c.streamBuffer.String()
	}
}

func (c *chatInteractionCoordinator) finalizeActiveAssistantLocked(finalSnapshot string, asMarkdown bool) string {
	if c == nil || c.activeStream == nil {
		return ""
	}
	c.stopActiveStreamFrameLocked()
	c.stopActiveStableCommitLocked()
	if !c.activeStream.Active() {
		c.clearActiveBandLocked()
		return ""
	}
	if c.activeStream.IsToolActive() {
		c.activeStream.Cancel()
		c.clearActiveBandLocked()
		return ""
	}
	if finalSnapshot != "" {
		c.activeStream.SetAssistantSnapshot(finalSnapshot, asMarkdown)
	}
	content, _ := c.activeStream.Finalize()
	// Keep ActiveBand mounted until the residual/full scrollback paint in the
	// same lock section. Clearing here opened a visible hole between the live
	// viewport drop and the first residual writeLine.
	return content
}

func (c *chatInteractionCoordinator) resetStreamLocked() {
	c.stopActiveStreamFrameLocked()
	c.stopActiveStableCommitLocked()
	c.streamingActive = false
	c.streamRendered = false
	c.streamMode = assistantStreamModeUnknown
	c.streamRenderedPrefixLen = 0
	c.streamEnqueuedPrefixLen = 0
	c.stableCommitQueue = nil
	c.stableCommitCatchUp = false
	c.stableCommitBelowExit = time.Time{}
	c.stableCommitLastExit = time.Time{}
	// Drop both coordinator source ownership and the surface rewrite window.
	// Leaving surface soft valid after the turn ends would allow a later
	// foreign/resize path to treat irreversible history as reflowable.
	c.invalidateSoftEmittedOwnershipLocked()
	// After a finished stream the terminal is already on a new row. Keep
	// streamTrailingLF true so closeOpenRowLocked never injects a phantom
	// blank into already-rendered history.
	c.streamTrailingLF = true
	c.streamLines = 0
	c.streamDisplayLines = 0
	c.streamBuffer.Reset()
	if c.activeStream != nil {
		// Production completion releases collector holdback before resetting the
		// viewport. Normal completion consumes this content earlier; interruption
		// paths still finalize rather than silently discarding the active source.
		if c.activeStream.Active() && !c.activeStream.IsToolActive() {
			_, _ = c.activeStream.Finalize()
		} else {
			c.activeStream.Cancel()
		}
	}
	c.clearActiveBandLocked()
	if !c.thinkingActive && !c.reasoningActive {
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	}
	// Snapshot last-turn transcript debug, then clear live turn state.
	if c.transcript != nil {
		c.streamLastFinalDivergence = c.transcript.LastDivergence
		c.streamLastTranscriptBlocks = len(c.transcript.Blocks)
		c.streamLastTranscriptBytes = c.transcript.RetainedSourceBytes
		c.transcript.reset()
	}
}

// paintActiveStreamLocked updates the Phase 5 active cell model and coalesces
// a viewport paint. It never writes to the transcript writer. When the fixed
// bottom surface is enabled, the painted frame is mirrored into ActiveBand.
// Returns newly-stable plain text from the controller (empty when nothing new
// is safe to append for an optional no-surface live path).
func (c *chatInteractionCoordinator) paintActiveStreamLocked(delta string, asMarkdown bool) (newlyStable string) {
	if c == nil || c.activeStream == nil {
		return ""
	}
	if !c.activeStream.Active() || c.activeStream.IsToolActive() {
		// Assistant content always owns the band over a stale tool cell.
		c.activeStream.BeginAssistant("assistant")
	}
	c.syncActiveStreamViewportLocked()
	if delta != "" {
		newlyStable = c.activeStream.PushAssistantDelta(delta, asMarkdown)
	} else if asMarkdown {
		// Recompute stable cut from full buffer on mode upgrades.
		newlyStable = c.activeStream.SetAssistantSnapshot(c.streamBuffer.String(), true)
	}
	committed := c.commitActiveStableScrollbackLocked(asMarkdown)
	_ = c.publishActiveStreamFrameLocked(committed)
	return newlyStable
}

func (c *chatInteractionCoordinator) commitActiveStableScrollbackLocked(asMarkdown bool) bool {
	if c == nil || c.activeStream == nil || c.reasoningActive || !c.surfaceOutputActiveLocked() {
		return false
	}
	stable := c.activeStream.StableContent()
	if c.streamEnqueuedPrefixLen < c.streamRenderedPrefixLen {
		c.streamEnqueuedPrefixLen = c.streamRenderedPrefixLen
	}
	if len(stable) <= c.streamEnqueuedPrefixLen {
		return false
	}
	width, rows := c.surface.ActiveBandViewportSize()
	cut := plainStableScrollbackCut(stable, c.streamEnqueuedPrefixLen, width, rows)
	if asMarkdown {
		// Never split a Markdown paragraph. Move completed blocks as soon as a
		// following block exists, while retaining the newest stable block in the
		// live viewport even when the source currently ends on a blank line.
		cut = markdownStableScrollbackCut(stable, c.streamEnqueuedPrefixLen, len(stable))
	}
	if cut <= c.streamEnqueuedPrefixLen || cut > len(stable) {
		return false
	}
	chunk := stable[c.streamEnqueuedPrefixLen:cut]
	c.enqueueActiveStableCommitLocked(chunk, cut, asMarkdown)
	c.streamEnqueuedPrefixLen = cut
	// Defer CommitStablePrefix until drain writes the chunk to scrollback.
	// Hiding the band first creates a mid-stream hole while the animated
	// stable-commit queue (and catch-up hysteresis) still holds the lines.
	c.drainActiveStableCommitCatchUpLocked()
	c.scheduleActiveStableCommitLocked()
	return true
}

func (c *chatInteractionCoordinator) enqueueActiveStableCommitLocked(chunk string, sourceEnd int, asMarkdown bool) {
	if c == nil || chunk == "" || sourceEnd <= c.streamRenderedPrefixLen {
		return
	}
	sourceStart := sourceEnd - len(chunk)
	if sourceStart < 0 {
		sourceStart = 0
	}
	var visual renderedAssistantChunk
	if asMarkdown {
		// Row delta of full-prefix Format (same as history replay). Per-chunk
		// Format drops inter-block blank lines that one-shot Format keeps;
		// string-level leading-\\n strip is intentionally not used.
		visual = renderedAssistantChunk{lines: c.markdownRowsDeltaLocked(sourceStart, sourceEnd, chunk)}
	} else {
		visual = buildRenderedAssistantChunk(chunk)
	}
	if visual.empty() {
		// Empty visual delta (Format(prev) == Format(full) for this cut).
		// Advance emitted source ownership so we do not re-enqueue the same
		// range, but do NOT CommitStablePrefix here: hiding the band before any
		// scrollback write opens a mid-stream hole while the controller still
		// paints those source bytes in the live viewport. Band release happens
		// on the next real drain commitBand or on finalize/clearActiveBand.
		c.streamRendered = true
		c.streamRenderedPrefixLen = sourceEnd
		c.ensureAssistantTranscriptLocked()
		c.transcript.syncFromCoordinator(
			c.streamBuffer.String(),
			c.streamRenderedPrefixLen,
			c.streamEnqueuedPrefixLen,
			asMarkdown,
		)
		return
	}
	now := time.Now()
	last := len(visual.lines) - 1
	for index, line := range visual.lines {
		end := 0
		commitBand := false
		if index == last {
			end = sourceEnd
			commitBand = true
		}
		c.stableCommitQueue = append(c.stableCommitQueue, activeStableCommitLine{
			text:       line,
			source:     chunk,
			sourceEnd:  end,
			enqueuedAt: now,
			commitBand: commitBand,
		})
	}
	// Keep transcript enqueued cursor aligned with the stable queue, but do not
	// record Blocks yet: discard paths may still drop this chunk before any
	// irreversible scrollback write.
	c.ensureAssistantTranscriptLocked()
	c.transcript.syncFromCoordinator(
		c.streamBuffer.String(),
		c.streamRenderedPrefixLen,
		sourceEnd,
		asMarkdown,
	)
}

// markdownStableCommitSuffixLocked returns the row-delta for a stable commit
// cut. Prefer markdownRowsDeltaLocked directly; this joins rows for any
// remaining string-shaped call sites.
func (c *chatInteractionCoordinator) markdownStableCommitSuffixLocked(sourceStart, sourceEnd int, chunk string) string {
	rows := c.markdownRowsDeltaLocked(sourceStart, sourceEnd, chunk)
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\n")
}

// markdownRowsDeltaLocked is the shared live/history spacing contract in row
// space: rows(Format(source[:end])) minus rows(Format(source[:start])).
// Working in rows after normalizeWriteLines collapses the old
// "strip one leading \\n when priorWriteEndedWithLF" rule — the first delta
// row is either content or a single "" blank from ApplyBlockSpacing, never a
// double-spaced join caused by string-level TrimRight + strip.
func (c *chatInteractionCoordinator) markdownRowsDeltaLocked(sourceStart, sourceEnd int, chunk string) []string {
	if c == nil || c.session == nil || c.session.Formatter == nil {
		return normalizeWriteLines(ui.FormatAssistantRendered(chunk))
	}
	buffered := c.streamBuffer.String()
	if sourceEnd > len(buffered) {
		sourceEnd = len(buffered)
	}
	if sourceStart < 0 {
		sourceStart = 0
	}
	if sourceStart > sourceEnd {
		sourceStart = sourceEnd
	}
	fullRendered := strings.TrimRight(c.session.Formatter.Format(buffered[:sourceEnd]), "\r\n")
	fullRows := normalizeWriteLines(ui.FormatAssistantRendered(fullRendered))
	if sourceStart <= 0 {
		return fullRows
	}
	prevRendered := strings.TrimRight(c.session.Formatter.Format(buffered[:sourceStart]), "\r\n")
	if prevRendered == "" {
		return fullRows
	}
	if !strings.HasPrefix(fullRendered, prevRendered) {
		// Formatter is not prefix-stable for this cut; fall back to chunk Format
		// so we never drop or duplicate visible content.
		return normalizeWriteLines(ui.FormatAssistantRendered(strings.TrimRight(c.session.Formatter.Format(chunk), "\r\n")))
	}
	prevRows := normalizeWriteLines(ui.FormatAssistantRendered(prevRendered))
	if len(prevRows) > len(fullRows) {
		return fullRows
	}
	for i := range prevRows {
		if fullRows[i] != prevRows[i] {
			// Style/indent broke row prefixing; fall back to the string suffix
			// without any leading-\\n strip (rows own the boundary).
			suffix := fullRendered[len(prevRendered):]
			return normalizeWriteLines(ui.FormatAssistantRendered(suffix))
		}
	}
	return fullRows[len(prevRows):]
}

// markdownFullPrefixSuffixLocked joins markdownRowsDeltaLocked for legacy
// callers/tests. priorWriteEndedWithLF is ignored: row delta replaces strip.
func (c *chatInteractionCoordinator) markdownFullPrefixSuffixLocked(sourceStart, sourceEnd int, chunk string, priorWriteEndedWithLF bool) string {
	_ = priorWriteEndedWithLF
	rows := c.markdownRowsDeltaLocked(sourceStart, sourceEnd, chunk)
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\n")
}

func (c *chatInteractionCoordinator) drainActiveStableCommitLocked(all bool) bool {
	if c == nil || len(c.stableCommitQueue) == 0 {
		return false
	}
	count := 1
	if all {
		count = len(c.stableCommitQueue)
	} else {
		// A formatted source chunk is atomic: source ownership cannot advance
		// halfway through its rendered lines without risking omissions or replay
		// if finalization lands between two animation ticks.
		for count < len(c.stableCommitQueue) && c.stableCommitQueue[count-1].sourceEnd == 0 {
			count++
		}
	}
	batch := c.stableCommitQueue[:count]
	prevEmitted := c.streamRenderedPrefixLen
	emittedLines := make([]string, 0, count)
	var emittedSourceEnd int
	var commitSourceEnd int
	for _, line := range batch {
		emittedLines = append(emittedLines, line.text)
		if line.sourceEnd > emittedSourceEnd {
			emittedSourceEnd = line.sourceEnd
		}
		// Collect the farthest band commit; applied only after the atomic
		// scrollback write so ActiveBand never hides mid-batch.
		if line.commitBand && line.sourceEnd > commitSourceEnd {
			commitSourceEnd = line.sourceEnd
		}
	}
	// One WriteOutput for the whole batch. Per-line writeLineLocked used to
	// release the surface lock between rows so ActiveBand/status growth could
	// scroll permanent holes into already-committed scrollback — the same class
	// of live-only blank that residual writeRowsLocked already closed.
	// Do not route through writeRowsLocked: stable rows are already complete
	// (no closeOpenRow / gapBlank), matching the historical writeLine loop.
	c.softCommitWriting = true
	if len(emittedLines) > 0 {
		c.writeTextLocked(strings.Join(emittedLines, "\n") + "\n")
	}
	c.softCommitWriting = false
	if emittedSourceEnd > c.streamRenderedPrefixLen {
		c.streamRenderedPrefixLen = emittedSourceEnd
	}
	bandCommitted := false
	if commitSourceEnd > 0 && c.activeStream != nil {
		c.activeStream.CommitStablePrefix(commitSourceEnd)
		bandCommitted = true
	}
	c.stableCommitQueue = c.stableCommitQueue[count:]
	c.streamRendered = true
	c.streamTrailingLF = true
	c.completeBlockOutput = true
	// Only irreversible scrollback writes become transcript Blocks. Pending
	// queue entries that are later discarded never appear here.
	if emittedSourceEnd > prevEmitted {
		emitWidth := c.currentStreamEmitWidthLocked()
		c.ensureAssistantTranscriptLocked()
		c.transcript.recordEmittedBlock(
			prevEmitted,
			emittedSourceEnd,
			emitWidth,
			c.streamMode == assistantStreamModeMarkdown,
			emittedLines,
		)
		c.transcript.syncFromCoordinator(
			c.streamBuffer.String(),
			c.streamRenderedPrefixLen,
			c.streamEnqueuedPrefixLen,
			c.streamMode == assistantStreamModeMarkdown,
		)
		// Note soft ownership per atomic source chunk so the soft window can
		// drop whole older chunks when it hits the surface cap.
		softStart := prevEmitted
		softLines := make([]string, 0, len(emittedLines))
		for _, line := range batch {
			softLines = append(softLines, line.text)
			if line.sourceEnd > 0 {
				c.noteSoftEmittedTailLocked(softStart, line.sourceEnd, emitWidth, softLines)
				softStart = line.sourceEnd
				softLines = nil
			}
		}
	}
	if bandCommitted {
		// Timer-driven drains are outside paintActiveStreamLocked; force a band
		// repaint so committed lines leave the live viewport immediately.
		c.refreshActiveBandAfterStableCommitLocked()
	}
	return true
}

func (c *chatInteractionCoordinator) refreshActiveBandAfterStableCommitLocked() {
	if c == nil || c.activeStream == nil || !c.surfaceOutputActiveLocked() {
		return
	}
	// Timer-driven drains sit outside paintActiveStreamLocked; force a band
	// repaint so committed lines leave the live viewport immediately and keep
	// the animation scheduler armed.
	_ = c.publishActiveStreamFrameLocked(true)
}

func (c *chatInteractionCoordinator) drainActiveStableCommitCatchUpLocked() bool {
	if c == nil || len(c.stableCommitQueue) == 0 {
		return false
	}
	now := time.Now()
	if !c.activeStableCommitCatchUpLocked(now) {
		return false
	}
	drained := c.drainActiveStableCommitLocked(true)
	if drained && len(c.stableCommitQueue) == 0 && c.stableCommitBelowExit.IsZero() {
		c.stableCommitBelowExit = now
	}
	return drained
}

func (c *chatInteractionCoordinator) activeStableCommitCatchUpLocked(now time.Time) bool {
	if c == nil || len(c.stableCommitQueue) == 0 {
		return false
	}
	depth := len(c.stableCommitQueue)
	oldestAge := now.Sub(c.stableCommitQueue[0].enqueuedAt)
	if oldestAge < 0 {
		oldestAge = 0
	}
	if !c.stableCommitCatchUp {
		pressure := depth >= activeStableCatchUpDepth || oldestAge >= activeStableCatchUpOldestAge
		if !pressure {
			return false
		}
		severe := depth >= activeStableSevereDepth || oldestAge >= activeStableSevereAge
		if !severe && !c.stableCommitLastExit.IsZero() && now.Sub(c.stableCommitLastExit) < activeStableCatchUpReenter {
			return false
		}
		c.stableCommitCatchUp = true
		c.stableCommitBelowExit = time.Time{}
		c.stableCommitLastExit = time.Time{}
		return true
	}

	belowExit := depth <= activeStableCatchUpExitDepth && oldestAge <= activeStableCatchUpExitAge
	if !belowExit {
		c.stableCommitBelowExit = time.Time{}
		return true
	}
	if c.stableCommitBelowExit.IsZero() {
		c.stableCommitBelowExit = now
		return true
	}
	if now.Sub(c.stableCommitBelowExit) < activeStableCatchUpExitHold {
		return true
	}
	c.stableCommitCatchUp = false
	c.stableCommitBelowExit = time.Time{}
	c.stableCommitLastExit = now
	return false
}

func (c *chatInteractionCoordinator) scheduleActiveStableCommitLocked() {
	if c == nil || c.shutdown || len(c.stableCommitQueue) == 0 || c.stableCommitTimer != nil {
		return
	}
	if c.surface != nil && !c.surface.DynamicStatusTicksEnabled() && !c.stableCommitManual {
		c.drainActiveStableCommitLocked(false)
		return
	}
	delay := c.stableCommitDelay
	if delay <= 0 {
		delay = activeStableCommitTickDelay
	}
	c.stableCommitTimerSeq++
	sequence := c.stableCommitTimerSeq
	c.stableCommitTimer = time.AfterFunc(delay, func() {
		c.runActiveStableCommitTick(sequence)
	})
}

func (c *chatInteractionCoordinator) runActiveStableCommitTick(sequence uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if sequence != c.stableCommitTimerSeq {
		return
	}
	c.stableCommitTimer = nil
	if c.shutdown || len(c.stableCommitQueue) == 0 {
		return
	}
	if !c.drainActiveStableCommitCatchUpLocked() {
		c.drainActiveStableCommitLocked(false)
	}
	c.scheduleActiveStableCommitLocked()
}

func (c *chatInteractionCoordinator) stopActiveStableCommitLocked() {
	if c == nil {
		return
	}
	c.stableCommitTimerSeq++
	if c.stableCommitTimer != nil {
		c.stableCommitTimer.Stop()
	}
	c.stableCommitTimer = nil
}

func (c *chatInteractionCoordinator) discardPendingStableCommitLocked() {
	if c == nil {
		return
	}
	c.stopActiveStableCommitLocked()
	c.stableCommitQueue = nil
	c.streamEnqueuedPrefixLen = c.streamRenderedPrefixLen
	if c.transcript != nil {
		c.transcript.dropPendingBeyondEmitted()
		c.transcript.syncFromCoordinator(
			c.streamBuffer.String(),
			c.streamRenderedPrefixLen,
			c.streamEnqueuedPrefixLen,
			c.streamMode == assistantStreamModeMarkdown,
		)
	}
}

func (c *chatInteractionCoordinator) rebuildPendingStableCommitLocked(asMarkdown bool) {
	if c == nil || len(c.stableCommitQueue) == 0 || c.streamEnqueuedPrefixLen <= c.streamRenderedPrefixLen {
		return
	}
	buffered := c.streamBuffer.String()
	end := c.streamEnqueuedPrefixLen
	if end > len(buffered) {
		end = len(buffered)
	}
	start := c.streamRenderedPrefixLen
	if start < 0 || start >= end {
		c.discardPendingStableCommitLocked()
		return
	}
	c.stopActiveStableCommitLocked()
	c.stableCommitQueue = nil
	c.syncFormatterWidthLocked()
	c.enqueueActiveStableCommitLocked(buffered[start:end], end, asMarkdown)
	c.streamEnqueuedPrefixLen = end
	c.drainActiveStableCommitCatchUpLocked()
	c.scheduleActiveStableCommitLocked()
}

func (c *chatInteractionCoordinator) currentStreamEmitWidthLocked() int {
	if c != nil && c.surface != nil {
		if width, _ := c.surface.ActiveBandViewportSize(); width > 0 {
			return width
		}
	}
	if width := ui.GetTerminalWidth(); width > 0 {
		return width
	}
	return 80
}

func (c *chatInteractionCoordinator) syncFormatterWidthLocked() {
	if c == nil || c.session == nil || c.session.Formatter == nil {
		return
	}
	c.session.Formatter.Width = c.currentStreamEmitWidthLocked()
}

func (c *chatInteractionCoordinator) noteSoftEmittedTailLocked(sourceStart, sourceEnd, width int, lines []string) {
	if c == nil || sourceEnd <= sourceStart || len(lines) == 0 {
		return
	}
	if width <= 0 {
		width = c.currentStreamEmitWidthLocked()
	}
	seg := softEmittedSegment{
		sourceStart: sourceStart,
		sourceEnd:   sourceEnd,
		lines:       append([]string(nil), lines...),
	}
	// Contiguous extension keeps one rewrite window for the live turn tail,
	// while still recording atomic chunk boundaries for later window trim.
	if c.softEmittedSourceEnd == sourceStart && len(c.softEmittedSegments) > 0 {
		c.softEmittedSegments = append(c.softEmittedSegments, seg)
	} else if c.softEmittedSourceEnd == sourceStart && len(c.softEmittedLines) > 0 && len(c.softEmittedSegments) == 0 {
		// Legacy single-range state (tests): promote existing lines then append.
		c.softEmittedSegments = []softEmittedSegment{{
			sourceStart: c.softEmittedSourceStart,
			sourceEnd:   c.softEmittedSourceEnd,
			lines:       append([]string(nil), c.softEmittedLines...),
		}, seg}
	} else {
		c.softEmittedSegments = []softEmittedSegment{seg}
	}
	c.softEmittedWidth = width
	c.rebuildSoftEmittedFromSegmentsLocked()
	c.trimSoftEmittedToWindowLocked()
	c.syncSoftEmittedTailToSurfaceLocked()
}

func (c *chatInteractionCoordinator) clearSoftEmittedTailLocked() {
	if c == nil {
		return
	}
	c.softEmittedSourceStart = 0
	c.softEmittedSourceEnd = 0
	c.softEmittedLines = nil
	c.softEmittedWidth = 0
	c.softEmittedSegments = nil
}

func (c *chatInteractionCoordinator) invalidateSoftEmittedOwnershipLocked() {
	if c == nil {
		return
	}
	c.clearSoftEmittedTailLocked()
	if c.surface != nil {
		c.surface.InvalidateSoftOutputTail()
	}
}

func (c *chatInteractionCoordinator) rebuildSoftEmittedFromSegmentsLocked() {
	if c == nil {
		return
	}
	if len(c.softEmittedSegments) == 0 {
		c.softEmittedSourceStart = 0
		c.softEmittedSourceEnd = 0
		c.softEmittedLines = nil
		return
	}
	total := 0
	for _, seg := range c.softEmittedSegments {
		total += len(seg.lines)
	}
	lines := make([]string, 0, total)
	for _, seg := range c.softEmittedSegments {
		lines = append(lines, seg.lines...)
	}
	c.softEmittedLines = lines
	c.softEmittedSourceStart = c.softEmittedSegments[0].sourceStart
	c.softEmittedSourceEnd = c.softEmittedSegments[len(c.softEmittedSegments)-1].sourceEnd
}

func softEmittedSegmentLineCount(segments []softEmittedSegment) int {
	total := 0
	for _, seg := range segments {
		total += len(seg.lines)
	}
	return total
}

// trimSoftEmittedToWindowLocked drops whole older source chunks until the soft
// line count fits SoftOutputTailMaxLines. A single atomic chunk larger than the
// window cannot be partially reflowed, so ownership is cleared in that case.
func (c *chatInteractionCoordinator) trimSoftEmittedToWindowLocked() {
	if c == nil {
		return
	}
	maxLines := ui.SoftOutputTailMaxLines
	if maxLines <= 0 {
		return
	}
	for len(c.softEmittedSegments) > 1 && softEmittedSegmentLineCount(c.softEmittedSegments) > maxLines {
		c.softEmittedSegments = c.softEmittedSegments[1:]
	}
	c.rebuildSoftEmittedFromSegmentsLocked()
	if len(c.softEmittedLines) > maxLines {
		// One atomic chunk overflowed the rewrite window. Keep irreversible
		// scrollback bytes, but do not claim a source-backed soft suffix.
		c.clearSoftEmittedTailLocked()
	}
}

// syncSoftEmittedTailToSurfaceLocked re-bases surface soft tracking onto the
// coordinator's still-owned suffix so line counts match after a window trim.
func (c *chatInteractionCoordinator) syncSoftEmittedTailToSurfaceLocked() {
	if c == nil || !c.surfaceWriter || c.surface == nil || !c.surfaceOutputActiveLocked() {
		return
	}
	if len(c.softEmittedLines) == 0 {
		c.surface.InvalidateSoftOutputTail()
		return
	}
	c.surface.AdoptSoftOutputTail(c.softEmittedLines)
}

// reflowSoftEmittedTailLocked re-formats the source-backed soft committed tail
// at the current terminal width and rewrites only those surface rows (Phase D).
// It never:
//   - replays irreversible history above the soft window
//   - routes through writeRowsLocked / gapBlank / completeBlockOutput
//   - invents cross-message separators while rewriting owned soft rows
//
// Failed ownership checks invalidate rather than patching foreign scrollback.
func (c *chatInteractionCoordinator) reflowSoftEmittedTailLocked() {
	if c == nil || c.softEmittedSourceEnd <= c.softEmittedSourceStart || len(c.softEmittedLines) == 0 {
		return
	}
	width := c.currentStreamEmitWidthLocked()
	if width == c.softEmittedWidth {
		return
	}
	newLines := c.renderSoftEmittedLinesLocked(c.softEmittedSourceStart, c.softEmittedSourceEnd, width)
	if len(newLines) == 0 {
		return
	}
	// Same rendered lines: only record the new width so later refreshes no-op.
	if stringSlicesEqual(c.softEmittedLines, newLines) {
		c.softEmittedWidth = width
		return
	}

	// Live surface rewrite requires a 1:1 soft window still parked at the output
	// bottom. Buffer-only / non-TTY tests keep source bookkeeping without claiming
	// terminal rows were rewritten.
	canRewriteSurface := c.surfaceWriter &&
		c.writer != nil &&
		c.surface != nil &&
		c.surfaceOutputActiveLocked() &&
		c.surface.SoftOutputTailValid() &&
		c.surface.SoftOutputTailLineCount() == len(c.softEmittedLines)
	if canRewriteSurface {
		if !c.surface.RewriteSoftOutputTail(c.writer, newLines) {
			// Rewrite failed (scrolled away / missing window): drop both sides
			// so ownership cannot drift between coordinator and surface.
			c.invalidateSoftEmittedOwnershipLocked()
			return
		}
	} else if c.surfaceWriter && c.surface != nil && c.surfaceOutputActiveLocked() {
		// Soft window is missing or polluted by foreign output.
		c.invalidateSoftEmittedOwnershipLocked()
		return
	}

	c.softEmittedLines = newLines
	c.softEmittedWidth = width
	// Reflow collapses segment boundaries into one range at the new width.
	if c.softEmittedSourceEnd > c.softEmittedSourceStart {
		c.softEmittedSegments = []softEmittedSegment{{
			sourceStart: c.softEmittedSourceStart,
			sourceEnd:   c.softEmittedSourceEnd,
			lines:       append([]string(nil), newLines...),
		}}
	}
	c.ensureAssistantTranscriptLocked()
	if c.transcript != nil {
		c.transcript.LastEmitWidth = width
	}
}

// renderSoftEmittedLinesLocked rebuilds soft-window rows from source only.
// Content spacing comes from assistantStreamCell.DisplayLines ("" rows from
// Format spacing). No gapBlank and no completeBlockOutput are consulted — soft
// reflow must not insert message separators into already-painted history.
//
// P5.5: soft reflow uses the same retained-source cell model as finalized
// assistant turns, so resize reflow and commit share one DisplayLines path.
func (c *chatInteractionCoordinator) renderSoftEmittedLinesLocked(sourceStart, sourceEnd, width int) []string {
	if c == nil || sourceEnd <= sourceStart {
		return nil
	}
	buffered := c.streamBuffer.String()
	if sourceEnd > len(buffered) {
		sourceEnd = len(buffered)
	}
	if sourceStart < 0 {
		sourceStart = 0
	}
	if sourceStart >= sourceEnd {
		return nil
	}
	chunk := buffered[sourceStart:sourceEnd]
	asMarkdown := c.streamMode == assistantStreamModeMarkdown
	if asMarkdown && c.session != nil && c.session.Formatter != nil {
		formatFn := func(source string, w int) string {
			prev := c.session.Formatter.Width
			if w > 0 {
				c.session.Formatter.Width = w
			}
			out := c.session.Formatter.Format(source)
			c.session.Formatter.Width = prev
			return out
		}
		return newAssistantStreamCellWithFormatter(chunk, true, formatFn).DisplayLines(width)
	}
	// Plain path: same cell model as finalized assistant turns.
	return newAssistantStreamCell(chunk, false).DisplayLines(width)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func markdownStableScrollbackCut(stable string, committed, maxCut int) int {
	if committed < 0 || committed >= len(stable) || maxCut <= committed {
		return committed
	}
	if maxCut > len(stable) {
		maxCut = len(stable)
	}
	rest := stable[committed:maxCut]
	search := rest
	if maxCut == len(stable) {
		search = strings.TrimSuffix(strings.TrimSuffix(rest, "\n"), "\r")
	}
	cut := -1
	if index := strings.LastIndex(search, "\n\n"); index >= 0 {
		cut = index + 2
	}
	if index := strings.LastIndex(search, "\r\n\r\n"); index >= 0 && index+4 > cut {
		cut = index + 4
	}
	if structuralCut := markdownStructuralScrollbackCut(search); structuralCut > cut {
		cut = structuralCut
	}
	if cut <= 0 {
		return committed
	}
	return committed + cut
}

// markdownStructuralScrollbackCut finds boundaries that remain stable without
// a blank line. In particular, the start of a later list item proves the
// previous item is complete, and a following line proves an ATX heading or
// fenced block can be emitted. The newest block is deliberately retained.
func markdownStructuralScrollbackCut(source string) int {
	cut := 0
	openFenceMarker := byte(0)
	openFenceLength := 0
	baseListIndent := -1
	for offset := 0; offset < len(source); {
		lineEnd := strings.IndexByte(source[offset:], '\n')
		if lineEnd < 0 {
			lineEnd = len(source)
		} else {
			lineEnd += offset
		}
		next := lineEnd
		if lineEnd < len(source) {
			next++
		}
		line := strings.TrimSuffix(source[offset:lineEnd], "\r")
		trimmed := strings.TrimSpace(line)

		marker, markerLength, markerRest, fence := markdownSourceFenceRun(line)
		if openFenceMarker != 0 {
			if fence && marker == openFenceMarker && markerLength >= openFenceLength && strings.TrimSpace(markerRest) == "" {
				openFenceMarker = 0
				openFenceLength = 0
				if next < len(source) {
					cut = next
				}
			}
		} else if fence && (marker != '`' || !strings.Contains(markerRest, "`")) {
			openFenceMarker = marker
			openFenceLength = markerLength
		} else {
			if indent, listItem := markdownListItemIndent(line); listItem {
				if baseListIndent < 0 {
					baseListIndent = indent
					if offset > 0 {
						cut = offset
					}
				} else if indent <= baseListIndent {
					cut = offset
					baseListIndent = indent
				}
			}
			if line == trimmed && markdownATXHeading(trimmed) && next < len(source) {
				cut = next
			}
		}

		if next <= offset {
			break
		}
		offset = next
	}
	return cut
}

func markdownSourceFenceRun(line string) (marker byte, runLength int, rest string, ok bool) {
	// Nested/list-indented fences remain attached to their parent item. This
	// helper finds only top-level boundaries; the collector's fence scanner
	// still handles the complete Markdown indentation rules for holdback.
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return 0, 0, "", false
	}
	marker = line[0]
	if marker != '`' && marker != '~' {
		return 0, 0, "", false
	}
	end := 0
	for end < len(line) && line[end] == marker {
		end++
	}
	if end < 3 {
		return 0, 0, "", false
	}
	return marker, end, line[end:], true
}

func markdownATXHeading(line string) bool {
	if line == "" || line[0] != '#' {
		return false
	}
	index := 0
	for index < len(line) && line[index] == '#' {
		index++
	}
	return index <= 6 && (index == len(line) || line[index] == ' ' || line[index] == '\t')
}

func markdownListItemIndent(line string) (int, bool) {
	indent := 0
	for indent < len(line) && (line[indent] == ' ' || line[indent] == '\t') {
		indent++
	}
	line = line[indent:]
	if len(line) >= 2 && (line[0] == '-' || line[0] == '+' || line[0] == '*') && (line[1] == ' ' || line[1] == '\t') {
		return indent, true
	}
	index := 0
	for index < len(line) && index < 9 && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	return indent, index > 0 && index+1 < len(line) && (line[index] == '.' || line[index] == ')') && (line[index+1] == ' ' || line[index+1] == '\t')
}

func plainStableScrollbackCut(stable string, committed, width, viewportRows int) int {
	if committed < 0 || committed >= len(stable) {
		return committed
	}
	if width <= 0 {
		width = 80
	}
	bodyRows := viewportRows - 1
	if bodyRows < 1 {
		bodyRows = 1
	}
	rest := stable[committed:]
	type completedLine struct {
		end  int
		rows int
	}
	completed := make([]completedLine, 0, strings.Count(rest, "\n"))
	totalRows := 0
	cursor := 0
	for cursor < len(rest) {
		newline := strings.IndexByte(rest[cursor:], '\n')
		if newline < 0 {
			totalRows += activeSourceDisplayRows(strings.TrimSuffix(rest[cursor:], "\r"), width)
			break
		}
		end := cursor + newline + 1
		lineRows := activeSourceDisplayRows(strings.TrimSuffix(rest[cursor:end-1], "\r"), width)
		completed = append(completed, completedLine{end: end, rows: lineRows})
		totalRows += lineRows
		cursor = end
	}
	cut := 0
	for _, line := range completed {
		if totalRows <= bodyRows {
			break
		}
		cut = line.end
		totalRows -= line.rows
	}
	return committed + cut
}

func activeSourceDisplayRows(line string, width int) int {
	rows := (render.Width(line) + width - 1) / width
	if rows < 1 {
		return 1
	}
	return rows
}

// scheduleActiveStreamFrameLocked arms the single coordinator-owned wakeup
// used for both coalesced content and activity animation. FrameScheduler stays
// clock-agnostic; this owner supplies the delayed poll that prevents the last
// delta in a burst from being stranded inside the FPS window.
func (c *chatInteractionCoordinator) scheduleActiveStreamFrameLocked() {
	if c == nil || c.shutdown || c.activeStream == nil || !c.surfaceOutputActiveLocked() {
		return
	}
	now := time.Now()
	delay, needed := c.activeStream.NextFrameDelay(now)
	if !needed {
		return
	}
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	due := now.Add(delay)
	if c.activeFrameTimer != nil {
		if !due.Before(c.activeFrameDue) {
			return
		}
		c.activeFrameTimer.Stop()
	}
	c.activeFrameGeneration++
	generation := c.activeFrameGeneration
	c.activeFrameDue = due
	c.activeFrameTimer = time.AfterFunc(delay, func() {
		c.paintScheduledActiveStreamFrame(generation)
	})
}

func (c *chatInteractionCoordinator) paintScheduledActiveStreamFrame(generation uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.activeFrameGeneration {
		return
	}
	c.activeFrameTimer = nil
	c.activeFrameDue = time.Time{}
	if c.shutdown || c.activeStream == nil || !c.activeStream.Active() || !c.surfaceOutputActiveLocked() {
		return
	}
	_ = c.publishActiveStreamFrameLocked(false)
}

func (c *chatInteractionCoordinator) stopActiveStreamFrameLocked() {
	if c == nil {
		return
	}
	c.activeFrameGeneration++
	if c.activeFrameTimer != nil {
		c.activeFrameTimer.Stop()
	}
	c.activeFrameTimer = nil
	c.activeFrameDue = time.Time{}
}

// syncAgentStageActiveBandLocked mirrors agent tool-running into ActiveBand.
// Assistant/reasoning streams keep ownership of the band while active.
func (c *chatInteractionCoordinator) syncAgentStageActiveBandLocked() {
	if c == nil || !c.surfaceOutputActiveLocked() || c.activeStream == nil {
		return
	}
	if c.streamingActive || c.reasoningActive {
		return
	}
	switch c.agentStage {
	case chatAgentStageToolRunning:
		selected := c.newestActiveToolLocked()
		name, progress := splitToolStageDetail(c.agentStageDetail)
		if name == "" {
			name = "tool"
		}
		sameTool := c.activeStream.IsToolActive() && c.activeStream.ToolName() == name
		if selected.display != "" {
			sameDisplay := sameTool && c.activeStream.ToolDisplay() == selected.display
			if sameDisplay && c.surface != nil && len(c.surface.ActiveBandLines()) > 0 {
				return
			}
			c.activeStream.BeginToolDisplay(name, nil, selected.display)
			_ = c.publishActiveStreamFrameLocked(true)
			return
		}
		// Avoid thrashing identical tool paints on high-frequency progress.
		if sameTool &&
			c.activeStream.ToolProgress() == progress {
			if c.surface != nil && len(c.surface.ActiveBandLines()) > 0 {
				return
			}
		}
		c.activeStream.SetToolProgress(name, progress)
		force := !sameTool || c.surface == nil || len(c.surface.ActiveBandLines()) == 0
		_ = c.publishActiveStreamFrameLocked(force)
	default:
		if c.activeStream.IsToolActive() {
			c.stopActiveStreamFrameLocked()
			c.activeStream.Cancel()
			c.clearActiveBandLocked()
		}
	}
}

// splitToolStageDetail separates a composer stage label into tool name and
// optional progress text. Runtime progress details are built as
// "<toolName> <message|percent|partial>" by chatToolProgressStageDetail.
func splitToolStageDetail(detail string) (name, progress string) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return "", ""
	}
	parts := strings.SplitN(detail, " ", 2)
	name = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		progress = strings.TrimSpace(parts[1])
	}
	return name, progress
}

// syncActiveStreamViewportLocked keeps the active frame buffer sized to the
// adaptive band budget so terminal resizes are picked up on the next frame.
// The surface reports cached dimensions, so this stays syscall-free while a
// stream is running.
//
// Production resize path (publishActiveStreamFrameLocked) now triggers
// geometry probe + soft reflow; this helper still syncs buffer size for
// direct RefreshActiveStreamViewport calls.
func (c *chatInteractionCoordinator) syncActiveStreamViewportLocked() {
	if c == nil || c.activeStream == nil {
		return
	}
	var width, rows int
	if c.surface != nil && c.surface.Enabled() {
		width, rows = c.surface.ActiveBandViewportSize()
	} else {
		width, rows = ui.GetTerminalWidth(), ui.ActiveBandRows(ui.GetTerminalHeight())
	}
	c.activeStream.SetViewport(width, rows)
}

// publishActiveStreamFrameLocked is the single ActiveBand paint entry after the
// active stream model has been updated. force bypasses frame-scheduler equality
// so commit/resize/tool-start always re-sync the band; changed frames always
// re-sync. The animation scheduler is re-armed whenever a surface is active.
//
// Production terminal resizes are picked up here: each frame probes geometry
// and, when width/height changed (or soft width drifted), runs the same soft
// reflow + stable-queue rebuild path as RefreshActiveStreamViewport.
func (c *chatInteractionCoordinator) publishActiveStreamFrameLocked(force bool) (changed bool) {
	if c == nil || c.activeStream == nil {
		return false
	}
	if c.maybeRefreshStreamGeometryLocked() {
		force = true
	}
	c.syncActiveStreamViewportLocked()
	frame, changed := c.activeStream.PaintLines(time.Now(), force)
	if c.surfaceOutputActiveLocked() && (force || changed) {
		c.syncActiveBandLinesLocked(frame)
	}
	if c.surfaceOutputActiveLocked() {
		c.scheduleActiveStreamFrameLocked()
	}
	return changed
}

// maybeRefreshStreamGeometryLocked probes terminal size on the live stream
// paint path and triggers source-backed soft reflow when geometry drifted.
// Returns true when a full viewport refresh ran (caller should force paint).
//
// Geometry probes are throttled to DefaultGeometryProbeMinInterval so a 30 FPS
// paint loop does not re-run GetSize every frame. Soft-width drift (layout
// already updated elsewhere, e.g. EnableForTest / write-path RefreshSize) still
// forces an immediate reflow without waiting for the probe interval.
func (c *chatInteractionCoordinator) maybeRefreshStreamGeometryLocked() bool {
	if c == nil || c.activeStream == nil || !c.activeStream.Active() {
		return false
	}
	width := c.currentStreamEmitWidthLocked()
	softNeedsReflow := c.softEmittedSourceEnd > c.softEmittedSourceStart &&
		len(c.softEmittedLines) > 0 &&
		c.softEmittedWidth > 0 &&
		c.softEmittedWidth != width

	sizeChanged := false
	if c.surface != nil && c.surface.Enabled() {
		if softNeedsReflow {
			// Soft ownership already disagrees with the cached layout width —
			// reflow now; also force an unthrottled probe so layout stays coherent.
			sizeChanged = c.surface.SyncTerminalGeometry()
		} else {
			sizeChanged, _ = c.surface.SyncTerminalGeometryThrottled(ui.DefaultGeometryProbeMinInterval)
		}
		// Recompute after a probe may have applied a new scroll-region width.
		width = c.currentStreamEmitWidthLocked()
		softNeedsReflow = c.softEmittedSourceEnd > c.softEmittedSourceStart &&
			len(c.softEmittedLines) > 0 &&
			c.softEmittedWidth > 0 &&
			c.softEmittedWidth != width
	}
	if !sizeChanged && !softNeedsReflow {
		return false
	}
	c.refreshActiveStreamViewportLocked()
	return true
}

func (c *chatInteractionCoordinator) syncActiveBandLinesLocked(frame []render.Line) {
	if c == nil || c.surface == nil || !c.surface.Enabled() {
		return
	}
	if strings.TrimSpace((render.PlainBackend{}).Render(render.LinesDoc(frame...))) == "" {
		c.surface.ClearActiveBand()
		return
	}
	c.surface.SetActiveBandStyled(frame)
}

func (c *chatInteractionCoordinator) clearActiveBandLocked() {
	if c == nil || c.surface == nil {
		return
	}
	c.surface.ClearActiveBand()
}

// RefreshActiveStreamViewport forces a redraw after resize/theme changes.
// Source-backed reflow order:
//  1. probe terminal geometry (production resize entry also runs this via paint)
//  2. rewrite the soft committed tail still at the output bottom
//  3. rebuild the still-pending stable queue from source offsets
//  4. resize ActiveBand and re-cut newly stable content for the new geometry
func (c *chatInteractionCoordinator) RefreshActiveStreamViewport() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.surface != nil && c.surface.Enabled() {
		_ = c.surface.SyncTerminalGeometry()
	}
	if c.activeStream == nil || !c.activeStream.Active() {
		return
	}
	c.refreshActiveStreamViewportLocked()
	// Bypass maybeRefreshStreamGeometryLocked re-entry: geometry was just
	// synced and soft reflow already ran above.
	c.syncActiveStreamViewportLocked()
	frame, changed := c.activeStream.PaintLines(time.Now(), true)
	if c.surfaceOutputActiveLocked() {
		c.syncActiveBandLinesLocked(frame)
		c.scheduleActiveStreamFrameLocked()
	}
	// Resize can leave the model frame equal while the physical band still needs
	// a repaint for the new geometry.
	if c.surfaceOutputActiveLocked() && !changed {
		c.surface.RefreshActiveBand()
	}
}

// refreshActiveStreamViewportLocked runs soft reflow + stable queue rebuild +
// ActiveBand resize under the coordinator lock. Callers must already hold c.mu
// and must have refreshed surface geometry when a live surface is attached.
func (c *chatInteractionCoordinator) refreshActiveStreamViewportLocked() {
	if c == nil || c.activeStream == nil || !c.activeStream.Active() {
		return
	}
	asMarkdown := c.streamMode == assistantStreamModeMarkdown
	c.syncFormatterWidthLocked()
	c.reflowSoftEmittedTailLocked()
	c.rebuildPendingStableCommitLocked(asMarkdown)
	width, rows := c.currentStreamEmitWidthLocked(), ui.ActiveBandRows(ui.GetTerminalHeight())
	if c.surface != nil && c.surface.Enabled() {
		if w, r := c.surface.ActiveBandViewportSize(); w > 0 {
			width = w
			if r > 0 {
				rows = r
			}
		}
	}
	c.activeStream.Resize(width, rows)
	_ = c.commitActiveStableScrollbackLocked(asMarkdown)
}

func (c *chatInteractionCoordinator) resetReasoningLocked() {
	c.reasoningActive = false
	c.reasoningRendered = false
	// After finalize the terminal is already on a new row (end-reasoning line).
	// Keep true so a later reopen never confuses zero-value false with mid-line.
	c.reasoningTrailingLF = true
	c.reasoningMeta = ""
	c.reasoningBuffer.Reset()
	if !c.thinkingActive && !c.streamingActive {
		c.updateSurfaceStatusLocked(c.currentSurfaceStateLocked())
	}
}

func (c *chatInteractionCoordinator) writeCompleteBlockLocked(rendered string, gap blockGap) {
	if strings.TrimSpace(rendered) == "" {
		return
	}
	// Pre-styled blocks (assistant/user/error/supplement) may contain internal
	// newlines. Normalize into writeLine-ready rows first so CR/terminators are
	// stripped the same way as the stream path. Cross-block separation is an
	// explicit caller decision (gap); completeBlockOutput must never invent a
	// blank into already-rendered history.
	lines := normalizeWriteLines(rendered)
	if len(lines) == 0 {
		return
	}
	c.writeRowsLocked(lines, gap)
}

// commitHistoryCellLocked writes a history cell's rendered rows as one atomic
// complete block with the given leading gap. It is the cell-model commit seam
// (P4.1): callers build a typed historyCell (retained source) instead of
// pre-joining a string, so later sub-steps can reuse the same source for resize
// reflow. The rows are already normalizeWriteLines-ready, so this matches the
// legacy writeCompleteBlockLocked pipeline (normalize -> writeRowsLocked).
func (c *chatInteractionCoordinator) commitHistoryCellLocked(cell historyCell, gap blockGap) {
	if c == nil || cell == nil {
		return
	}
	// plumb real width for owned viewport (P5.5 resize reflow): pre-wrapped
	// cell lines are consumed by the backend, so no immediate-mode padding.
	width := c.currentStreamEmitWidthLocked()
	c.writeRowsLocked(cell.DisplayLines(width), gap)
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
