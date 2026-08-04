package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

var chatRuntimeGOOS = runtime.GOOS
var chatIsInteractiveTerminal = ui.IsInteractiveTerminal
var errChatInteractivePromptCancelled = errors.New("interactive prompt cancelled")

func isChatInteractivePromptCancelError(err error) bool {
	return errors.Is(err, errChatInteractivePromptCancelled) ||
		errors.Is(err, ui.ErrInteractiveInputInterrupted) ||
		errors.Is(err, ui.ErrInteractiveInputExitRequested) ||
		errors.Is(err, io.EOF)
}

type chatQueuedInput struct {
	Text       string
	Source     string
	EnqueuedAt time.Time
}

type chatInputRouteDisposition uint8

const (
	chatInputRouteQueued chatInputRouteDisposition = iota
	chatInputRoutePriority
	chatInputRouteRejectedCommand
)

type chatInputRouteResult struct {
	Disposition chatInputRouteDisposition
}

func (r chatInputRouteResult) queued() bool {
	return r.Disposition == chatInputRouteQueued
}

func (r chatInputRouteResult) rejected() bool {
	return r.Disposition == chatInputRouteRejectedCommand
}

type chatPendingInputSuspension struct {
	queue       *chatInputQueue
	queued      []chatQueuedInput
	readyText   string
	draftText   string
	draftLines  int
	draftActive bool
	count       int
	restoreOnce sync.Once
}

func (s *chatPendingInputSuspension) Count() int {
	if s == nil {
		return 0
	}
	return s.count
}

func (s *chatPendingInputSuspension) Restore() int {
	if s == nil || s.queue == nil {
		return 0
	}
	restored := 0
	s.restoreOnce.Do(func() {
		q := s.queue
		q.ensureChannels()
		q.routeMu.Lock()

		q.queuedMu.Lock()
		if len(s.queued) > 0 {
			front := make([]chatQueuedInput, 0, len(s.queued)+len(q.queuedFront))
			front = append(front, s.queued...)
			front = append(front, q.queuedFront...)
			q.queuedFront = front

			preview := make([]chatQueuedInput, 0, len(s.queued)+len(q.queuedPreview))
			preview = append(preview, s.queued...)
			preview = append(preview, q.queuedPreview...)
			q.queuedPreview = preview
		}
		q.queuedMu.Unlock()

		q.draftMu.Lock()
		if strings.TrimSpace(s.readyText) != "" {
			q.readyText = prependPendingInputText(s.readyText, q.readyText)
		}
		if s.draftActive && strings.TrimSpace(s.draftText) != "" {
			q.draftText = prependPendingInputText(s.draftText, q.draftText)
			q.draftLines = countInputLines(q.draftText)
			q.draftActive = true
		}
		draftNotify := q.draftNotify
		draftActive := q.draftActive
		draftLines := q.draftLines
		draftText := q.draftText
		q.draftMu.Unlock()
		q.routeMu.Unlock()

		if draftNotify != nil && draftActive {
			draftNotify(true, draftLines, draftText)
		}
		if len(s.queued) > 0 || strings.TrimSpace(s.readyText) != "" {
			q.signalReadySubmission()
		}
		restored = s.count
	})
	return restored
}

func prependPendingInputText(prefix, current string) string {
	if prefix == "" {
		return current
	}
	if current == "" {
		return prefix
	}
	if strings.HasSuffix(prefix, "\n") || strings.HasPrefix(current, "\n") {
		return prefix + current
	}
	return prefix + "\n" + current
}

type chatInputReadLifecycle struct {
	session *ChatSession
}

type chatInputQueue struct {
	reader                *bufio.Reader
	lines                 chan chatQueuedInput
	priorityLines         chan chatQueuedInput
	errs                  chan error
	readySignal           chan struct{}
	priorityCaptureSignal chan struct{}
	start                 sync.Once
	mu                    sync.RWMutex
	priorityMode          bool
	priorityPrompt        string
	priorityRevision      uint64
	externalCaptureActive bool
	terminalMu            sync.RWMutex
	terminalErr           error
	routeMu               sync.Mutex

	draftMu     sync.RWMutex
	draftNotify func(active bool, lines int, text string)
	draftText   string
	draftLines  int
	draftActive bool
	readyText   string

	queuedMu      sync.Mutex
	queuedFront   []chatQueuedInput
	queuedPreview []chatQueuedInput

	commandGate   func(string) bool
	routeFeedback func(string, chatInputRouteResult)
}

func newChatInputQueue(reader *bufio.Reader) *chatInputQueue {
	if reader == nil {
		reader = newChatInputReader()
	}
	return &chatInputQueue{
		reader:                reader,
		lines:                 make(chan chatQueuedInput, 32),
		priorityLines:         make(chan chatQueuedInput, 4),
		errs:                  make(chan error, 1),
		readySignal:           make(chan struct{}, 1),
		priorityCaptureSignal: make(chan struct{}, 1),
	}
}

func ensureChatInputQueue(session *ChatSession) *chatInputQueue {
	if session == nil {
		return nil
	}
	if session.InputQueue == nil {
		session.InputQueue = newChatInputQueue(chatSessionInputReader(session))
	}
	session.InputQueue.setDraftNotifier(func(active bool, lines int, text string) {
		notifyChatInputDraftState(session, active, lines, text)
	})
	session.InputQueue.setCommandGate(func(text string) bool {
		return chatInputCommandAllowed(session, text)
	})
	session.InputQueue.setRouteFeedback(func(text string, result chatInputRouteResult) {
		renderBusyInputRouteFeedback(session, text, result)
	})
	session.InputQueue.startPump()
	return session.InputQueue
}

func (q *chatInputQueue) startPump() {
	if q == nil {
		return
	}
	q.start.Do(func() {
		q.ensureChannels()
		if q.reader == nil {
			q.reader = newChatInputReader()
		}
		go q.stdinPump()
	})
}

type stdinLineEvent struct {
	Text     string
	Err      error
	Buffered int
}

// stdinPump 读取 stdin，把短时间内连续到达的行归并成一个 batch。
//
// 规则很直接：
// 1. 单行输入在 settle 窗口后直接投递给聊天循环。
// 2. batch 中有多行时，先暂存为 draft，不直接发送。
// 3. draft 存在时，后续非空输入会继续追加到 draft。
// 4. 用户再按一次独立的空 Enter 时，draft 被确认并等待读取。
//
// 这里不再依赖 `bufio.Reader.Buffered()` 或并发读同一个 reader。
func (q *chatInputQueue) stdinPump() {
	events := make(chan stdinLineEvent, 8)
	go q.stdinReadLoop(events)

	settle := inputPasteSettleDelay()
	if settle <= 0 {
		settle = 75 * time.Millisecond
	}

	var batch strings.Builder
	var batchHasBufferedInput bool
	var terminalErr error
	var timer *time.Timer
	var timerC <-chan time.Time

	stopTimer := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer = nil
		}
		timerC = nil
	}

	resetTimer := func() {
		stopTimer()
		timer = time.NewTimer(settle)
		timerC = timer.C
	}

	flushBatch := func() {
		if batch.Len() == 0 {
			batchHasBufferedInput = false
			return
		}
		text := normalizeBatchText(batch.String())
		hasBufferedInput := batchHasBufferedInput
		batch.Reset()
		batchHasBufferedInput = false
		if strings.TrimSpace(text) == "" {
			if q.hasDraft() {
				q.confirmDraft()
			}
			return
		}

		// 优先提示只接收本次明确输入，不能把普通 draft / ready submission
		// 当成审批或问题答案。多行回答也直接进入 priorityLines。
		if q.isPriorityMode() {
			q.routeInputText(text)
			return
		}

		if q.hasDraft() {
			if isSubmissionCommand(strings.TrimSpace(text)) {
				q.routeInputText(text)
				return
			}
			q.appendDraft(text)
			return
		}

		if q.shouldStageBufferedInput(text, hasBufferedInput) {
			q.stageDraft(text)
			return
		}

		q.routeInputText(text)
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				stopTimer()
				flushBatch()
				if terminalErr != nil {
					q.setTerminalError(terminalErr)
				}
				return
			}
			text := normalizeBatchText(ev.Text)
			if ev.Err != nil && terminalErr == nil {
				terminalErr = ev.Err
			}
			if strings.TrimSpace(text) == "" {
				stopTimer()
				if batch.Len() == 0 {
					if q.hasDraft() {
						q.confirmDraft()
					}
				} else {
					batch.WriteString(ev.Text)
					if ev.Buffered > 0 {
						batchHasBufferedInput = true
					}
					resetTimer()
				}
				continue
			}

			batch.WriteString(ev.Text)
			if ev.Buffered > 0 {
				batchHasBufferedInput = true
			}
			resetTimer()
		case <-timerC:
			stopTimer()
			flushBatch()
		}
	}
}

func (q *chatInputQueue) stdinReadLoop(events chan<- stdinLineEvent) {
	defer close(events)
	for {
		line, err := q.reader.ReadString('\n')
		if line == "" && err != nil {
			events <- stdinLineEvent{Err: err}
			return
		}
		buffered := 0
		if q.reader != nil {
			buffered = q.reader.Buffered()
		}
		events <- stdinLineEvent{Text: line, Err: err, Buffered: buffered}
		if err != nil {
			return
		}
	}
}

func (q *chatInputQueue) routeInputText(text string) chatInputRouteResult {
	result := q.routeLineWithCommandGate(chatQueuedInput{
		Text:       text,
		Source:     "stdin",
		EnqueuedAt: time.Now().UTC(),
	}, true)
	if result.rejected() {
		q.notifyRouteFeedback(text, result)
	}
	return result
}

func (q *chatInputQueue) stageDraft(text string) {
	text = normalizeBatchText(text)
	if text == "" {
		return
	}
	q.draftMu.Lock()
	q.draftText = text
	q.draftLines = countInputLines(text)
	q.draftActive = true
	lines := q.draftLines
	fn := q.draftNotify
	q.draftMu.Unlock()
	if fn != nil {
		fn(true, lines, text)
	}
}

func (q *chatInputQueue) appendDraft(text string) {
	text = normalizeBatchText(text)
	if text == "" {
		return
	}
	q.draftMu.Lock()
	if q.draftText == "" {
		q.draftText = text
	} else {
		q.draftText = q.draftText + text
	}
	q.draftLines = countInputLines(q.draftText)
	q.draftActive = true
	lines := q.draftLines
	currentText := q.draftText
	fn := q.draftNotify
	q.draftMu.Unlock()
	if fn != nil {
		fn(true, lines, currentText)
	}
}

func (q *chatInputQueue) consumeDraft() (string, bool) {
	q.draftMu.Lock()
	if !q.draftActive || strings.TrimSpace(q.draftText) == "" {
		q.draftText = ""
		q.draftLines = 0
		q.draftActive = false
		q.draftMu.Unlock()
		return "", false
	}
	text := q.draftText
	q.draftText = ""
	q.draftLines = 0
	q.draftActive = false
	fn := q.draftNotify
	q.draftMu.Unlock()
	if fn != nil {
		fn(false, 0, "")
	}
	return text, true
}

func (q *chatInputQueue) confirmDraft() bool {
	q.draftMu.RLock()
	text := q.draftText
	active := q.draftActive
	q.draftMu.RUnlock()
	if !active || strings.TrimSpace(text) == "" {
		return false
	}
	if q.rejectCommandInput(text) {
		// 保留被拒绝的 slash draft，避免状态变化期间确认草稿导致内容丢失。
		q.notifyRouteFeedback(text, chatInputRouteResult{Disposition: chatInputRouteRejectedCommand})
		return false
	}
	text, ok := q.consumeDraft()
	if !ok {
		return false
	}
	q.draftMu.Lock()
	if q.readyText == "" {
		q.readyText = text
	} else {
		q.readyText += text
	}
	q.draftMu.Unlock()
	q.signalReadySubmission()
	return true
}

func (q *chatInputQueue) discardDraft() int {
	q.draftMu.Lock()
	if !q.draftActive && q.draftText == "" {
		q.draftLines = 0
		q.draftMu.Unlock()
		return 0
	}
	count := q.draftLines
	if count <= 0 && strings.TrimSpace(q.draftText) != "" {
		count = 1
	}
	q.draftText = ""
	q.draftLines = 0
	q.draftActive = false
	fn := q.draftNotify
	q.draftMu.Unlock()
	if fn != nil {
		fn(false, 0, "")
	}
	return count
}

func (q *chatInputQueue) discardReadySubmission() int {
	q.draftMu.Lock()
	if strings.TrimSpace(q.readyText) == "" {
		q.readyText = ""
		q.draftMu.Unlock()
		return 0
	}
	q.readyText = ""
	q.draftMu.Unlock()
	return 1
}

func (q *chatInputQueue) draftCount() int {
	if q == nil {
		return 0
	}
	q.draftMu.RLock()
	defer q.draftMu.RUnlock()
	if !q.draftActive || strings.TrimSpace(q.draftText) == "" {
		return 0
	}
	if q.draftLines > 0 {
		return q.draftLines
	}
	return 1
}

func (q *chatInputQueue) hasDraft() bool {
	return q.draftCount() > 0
}

func (q *chatInputQueue) hasReadySubmission() bool {
	if q == nil {
		return false
	}
	q.draftMu.RLock()
	defer q.draftMu.RUnlock()
	return strings.TrimSpace(q.readyText) != ""
}

func (q *chatInputQueue) hasReadableInput() bool {
	if q == nil {
		return false
	}
	return q.pendingCount() > 0 || q.hasReadySubmission()
}

func (q *chatInputQueue) queuedPreviewLines(limit int) []string {
	if q == nil {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	out := make([]string, 0, limit+1)
	q.draftMu.RLock()
	readyText := q.readyText
	q.draftMu.RUnlock()
	if strings.TrimSpace(readyText) != "" {
		out = append(out, normalizeQueuedInputLine(readyText))
	}
	q.queuedMu.Lock()
	takeLimit := limit - len(out)
	if takeLimit < 0 {
		takeLimit = 0
	}
	taken := 0
	for _, item := range q.queuedPreview {
		if taken >= takeLimit {
			break
		}
		if text := normalizeQueuedInputLine(item.Text); strings.TrimSpace(text) != "" {
			out = append(out, text)
			taken++
		}
	}
	remaining := len(q.queuedPreview) - taken
	q.queuedMu.Unlock()
	if remaining > 0 {
		out = append(out, fmt.Sprintf("... %d more", remaining))
	}
	return out
}

func (q *chatInputQueue) readAvailableLine() (string, bool) {
	if q == nil {
		return "", false
	}
	if text, ok := q.takeReadySubmission(); ok {
		return text, true
	}
	if item, ok := q.takeQueuedFront(); ok {
		return item.Text, true
	}
	q.ensureChannels()
	select {
	case item := <-q.lines:
		q.noteQueuedLineRead()
		return item.Text, true
	default:
		return "", false
	}
}

func (q *chatInputQueue) takeQueuedFront() (chatQueuedInput, bool) {
	if q == nil {
		return chatQueuedInput{}, false
	}
	q.queuedMu.Lock()
	defer q.queuedMu.Unlock()
	if len(q.queuedFront) == 0 {
		return chatQueuedInput{}, false
	}
	item := q.queuedFront[0]
	copy(q.queuedFront, q.queuedFront[1:])
	q.queuedFront = q.queuedFront[:len(q.queuedFront)-1]
	q.dropQueuedPreviewLocked()
	return item, true
}

func (q *chatInputQueue) noteQueuedLineRead() {
	if q == nil {
		return
	}
	q.queuedMu.Lock()
	q.dropQueuedPreviewLocked()
	q.queuedMu.Unlock()
}

func (q *chatInputQueue) dropQueuedPreviewLocked() {
	if len(q.queuedPreview) == 0 {
		return
	}
	copy(q.queuedPreview, q.queuedPreview[1:])
	q.queuedPreview = q.queuedPreview[:len(q.queuedPreview)-1]
}

func (q *chatInputQueue) setDraftNotifier(fn func(active bool, lines int, text string)) {
	if q == nil {
		return
	}
	q.draftMu.Lock()
	q.draftNotify = fn
	q.draftMu.Unlock()
}

func (q *chatInputQueue) setCommandGate(fn func(string) bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.commandGate = fn
	q.mu.Unlock()
}

func (q *chatInputQueue) setRouteFeedback(fn func(string, chatInputRouteResult)) {
	if q == nil {
		return
	}
	q.mu.Lock()
	q.routeFeedback = fn
	q.mu.Unlock()
}

func (q *chatInputQueue) notifyRouteFeedback(text string, result chatInputRouteResult) {
	if q == nil {
		return
	}
	q.mu.RLock()
	feedback := q.routeFeedback
	q.mu.RUnlock()
	if feedback != nil {
		feedback(text, result)
	}
}

func (q *chatInputQueue) rejectCommandInput(text string) bool {
	if q == nil || !isSlashCommandInput(text) {
		return false
	}
	q.mu.RLock()
	gate := q.commandGate
	q.mu.RUnlock()
	return gate != nil && !gate(text)
}

func (q *chatInputQueue) shouldStageBufferedInput(text string, bufferedInput bool) bool {
	if q == nil {
		return false
	}
	if len(normalizeInputLines(text)) > 1 {
		return true
	}
	if bufferedInput && shouldDiscardPendingInput() {
		return true
	}
	if pending, err := pendingConsoleLineInput(); err == nil && pending {
		return true
	}
	if pending, err := pendingConsoleTextInput(); err == nil && pending {
		return true
	}
	return false
}

func normalizeBatchText(text string) string {
	return ui.NormalizePastedText(text)
}

func normalizeInputLines(text string) []string {
	text = normalizeBatchText(text)
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}

func countInputLines(text string) int {
	lines := normalizeInputLines(text)
	if len(lines) == 0 {
		return 0
	}
	return len(lines)
}

func isSubmissionCommand(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!")
}

func (q *chatInputQueue) handlePumpError(err error) {
	if q == nil || err == nil {
		return
	}
	q.setTerminalError(err)
	if !errors.Is(err, io.EOF) {
		time.Sleep(50 * time.Millisecond)
	}
}

func (q *chatInputQueue) readLine(ctx context.Context) (string, error) {
	if q == nil {
		return "", io.EOF
	}
	q.startPump()
	for {
		if text, ok := q.takeReadySubmission(); ok {
			return text, nil
		}
		if item, ok := q.takeQueuedFront(); ok {
			return item.Text, nil
		}
		select {
		case item := <-q.lines:
			q.noteQueuedLineRead()
			return item.Text, nil
		default:
		}
		if terminalErr := q.terminalError(); terminalErr != nil {
			return "", terminalErr
		}
		select {
		case item := <-q.lines:
			q.noteQueuedLineRead()
			return item.Text, nil
		case err := <-q.errs:
			select {
			case item := <-q.lines:
				q.noteQueuedLineRead()
				return item.Text, nil
			default:
			}
			return "", err
		case <-q.readySignal:
			continue
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (q *chatInputQueue) readPriorityLine(ctx context.Context) (string, error) {
	return q.readPriorityLineWithPrompt(ctx, "")
}

func (q *chatInputQueue) readPriorityLineWithPrompt(ctx context.Context, prompt string) (string, error) {
	if q == nil {
		return "", io.EOF
	}
	q.setPriorityCapture(true, prompt)
	defer q.setPriorityCapture(false, "")
	if !q.hasExternalInputCaptureActive() {
		q.startPump()
	}
	for {
		select {
		case item := <-q.priorityLines:
			return item.Text, nil
		default:
		}
		if terminalErr := q.terminalError(); terminalErr != nil {
			return "", terminalErr
		}
		select {
		case item := <-q.priorityLines:
			return item.Text, nil
		case err := <-q.errs:
			select {
			case item := <-q.priorityLines:
				return item.Text, nil
			default:
			}
			return "", err
		case <-q.readySignal:
			continue
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (q *chatInputQueue) pendingCount() int {
	if q == nil {
		return 0
	}
	q.ensureChannels()
	// 这里只统计已经进入队列、等待消费的输入。
	// draft 只表示“暂存中、等待用户确认”，不能影响 prompt / drain 判断。
	q.queuedMu.Lock()
	front := len(q.queuedFront)
	q.queuedMu.Unlock()
	return front + len(q.lines)
}

func (q *chatInputQueue) queuedSubmissionCount() int {
	if q == nil {
		return 0
	}
	count := q.pendingCount()
	if q.hasReadySubmission() {
		count++
	}
	return count
}

func (q *chatInputQueue) discardPending() int {
	if q == nil {
		return 0
	}
	q.ensureChannels()
	q.routeMu.Lock()
	q.queuedMu.Lock()
	discarded := len(q.queuedFront)
	q.queuedFront = nil
	for {
		select {
		case <-q.lines:
			discarded++
		default:
			q.queuedPreview = nil
			q.queuedMu.Unlock()
			q.routeMu.Unlock()
			return discarded + q.discardDraft() + q.discardReadySubmission()
		}
	}
}

func (q *chatInputQueue) suspendPendingInput() *chatPendingInputSuspension {
	suspension := &chatPendingInputSuspension{queue: q}
	if q == nil {
		return suspension
	}
	q.ensureChannels()

	q.routeMu.Lock()
	q.queuedMu.Lock()
	suspension.queued = append(suspension.queued, q.queuedFront...)
	q.queuedFront = nil
	for {
		select {
		case item := <-q.lines:
			suspension.queued = append(suspension.queued, item)
		default:
			q.queuedPreview = nil
			q.queuedMu.Unlock()
			goto queuedCaptured
		}
	}

queuedCaptured:
	q.draftMu.Lock()
	suspension.readyText = q.readyText
	suspension.draftText = q.draftText
	suspension.draftLines = q.draftLines
	suspension.draftActive = q.draftActive
	q.readyText = ""
	q.draftText = ""
	q.draftLines = 0
	q.draftActive = false
	draftNotify := q.draftNotify
	q.draftMu.Unlock()
	q.routeMu.Unlock()

	suspension.count = len(suspension.queued)
	if strings.TrimSpace(suspension.readyText) != "" {
		suspension.count++
	}
	if suspension.draftActive && strings.TrimSpace(suspension.draftText) != "" {
		suspension.count++
	}
	if draftNotify != nil && suspension.draftActive {
		draftNotify(false, 0, "")
	}
	return suspension
}

func newChatInputReadLifecycle(session *ChatSession) chatInputReadLifecycle {
	return chatInputReadLifecycle{session: session}
}

func (l chatInputReadLifecycle) beginReadyRead() {
	if l.session != nil {
		l.session.lastInteractiveInputQueued = false
	}
}

func (l chatInputReadLifecycle) finishQueuedReadyRead(line string, err error) {
	if l.session == nil {
		return
	}
	if err == nil {
		recordChatPromptHistory(l.session, line)
		return
	}
	l.resetPromptState()
}

func (l chatInputReadLifecycle) finishQueuedPriorityRead(err error) {
	if err != nil {
		l.resetPromptState()
	}
}

func (l chatInputReadLifecycle) markReadyLineQueued(line string) {
	if l.session == nil {
		return
	}
	l.session.lastInteractiveInputQueued = true
	recordChatPromptHistory(l.session, line)
}

func (l chatInputReadLifecycle) finishMainRead(readErr error) {
	if l.session == nil || l.session.Interaction == nil {
		return
	}
	if errors.Is(readErr, ui.ErrInteractiveInputTranscriptRequested) {
		// Ctrl+T temporarily transfers physical screen ownership to the pager;
		// the composer draft remains authoritative and must survive the return.
		return
	}
	if readErr != nil {
		l.session.lastInteractiveInputQueued = false
		// An aborted read (Ctrl+C / stdin failure) cancels the draft too.
		l.session.Interaction.DiscardPrompt()
		return
	}
	if l.session.lastInteractiveInputQueued {
		l.session.lastInteractiveInputQueued = false
		l.session.Interaction.RefreshStatus("")
		return
	}
	l.resetPromptState()
}

func (l chatInputReadLifecycle) resetPromptState() {
	if l.session != nil && l.session.Interaction != nil {
		l.session.Interaction.ResetPromptState()
	}
}

func chatInteractiveReadLine(session *ChatSession, ctx context.Context) (string, error) {
	lifecycle := newChatInputReadLifecycle(session)
	lifecycle.beginReadyRead()
	if session != nil && session.InputQueue != nil {
		if line, ok := session.InputQueue.readAvailableLine(); ok {
			lifecycle.markReadyLineQueued(line)
			return line, nil
		}
	}
	if shouldUseInteractiveLineEditor(session) {
		return newChatComposerController(session).ReadLine()
	}
	if session != nil && session.InputQueue != nil {
		line, err := session.InputQueue.readLine(ctx)
		lifecycle.finishQueuedReadyRead(line, err)
		return line, err
	}
	reader := chatSessionInputReader(session)
	line, err := reader.ReadString('\n')
	if line != "" {
		return line, nil
	}
	return "", err
}

func recordChatPromptHistory(session *ChatSession, input string) {
	if session == nil || session.InputBox == nil {
		return
	}
	session.InputBox.AddToHistory(normalizeQueuedInputLine(input))
}

func finishChatInteractiveReadPromptState(session *ChatSession, readErr error) {
	newChatInputReadLifecycle(session).finishMainRead(readErr)
}

func shouldUseInteractiveLineEditor(session *ChatSession) bool {
	if session == nil || session.InputBox == nil {
		return false
	}
	return chatIsInteractiveTerminal()
}

func shouldEnableSlashCompletion(session *ChatSession) bool {
	if session == nil || session.Surface == nil {
		return false
	}
	return session.Surface.Enabled() && chatInputCommandAllowed(session, "/")
}

func isSlashCommandInput(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

func chatInputCommandAllowed(session *ChatSession, text string) bool {
	if !isSlashCommandInput(text) {
		return true
	}
	if session == nil || session.Interaction == nil {
		return true
	}
	return session.Interaction.IsReady()
}

func chatInteractiveReadPriorityLine(session *ChatSession, ctx context.Context) (string, error) {
	return chatInteractiveReadTransientLine(session, ctx)
}

func shouldRoutePriorityPromptThroughQueue(session *ChatSession) bool {
	if session == nil || session.InputQueue == nil {
		return false
	}
	if shouldUseInteractiveLineEditor(session) && !session.InputQueue.hasExternalInputCaptureActive() {
		return false
	}
	return true
}

func chatInteractiveReadTransientLine(session *ChatSession, ctx context.Context) (string, error) {
	if shouldRoutePriorityPromptThroughQueue(session) {
		line, err := session.InputQueue.readPriorityLine(ctx)
		newChatInputReadLifecycle(session).finishQueuedPriorityRead(err)
		return line, err
	}
	if session != nil && session.InputBox != nil {
		return newChatTransientLineComposer(session).ReadLine()
	}
	reader := chatSessionInputReader(session)
	line, err := reader.ReadString('\n')
	if line != "" {
		return line, nil
	}
	return "", err
}

func chatInteractiveReadPriorityLineWithPrompt(session *ChatSession, ctx context.Context, prompt string) (string, error) {
	if shouldRoutePriorityPromptThroughQueue(session) {
		line, err := session.InputQueue.readPriorityLineWithPrompt(ctx, prompt)
		newChatInputReadLifecycle(session).finishQueuedPriorityRead(err)
		return line, err
	}
	if session != nil && session.InputBox != nil {
		return newChatModalComposerPrompt(session, prompt).ReadLine()
	}
	return chatInteractiveReadTransientLine(session, ctx)
}

func chatInteractiveReadPrioritySecretWithPrompt(session *ChatSession, ctx context.Context, prompt string) (string, error) {
	restoreInputMode := pushChatComposerInputMode(session, chatInputModeSecret)
	defer restoreInputMode()
	var suspension *chatPendingInputSuspension
	var notice string
	if session != nil && session.InputQueue != nil {
		suspension = session.InputQueue.suspendPendingInput()
		if count := suspension.Count(); count > 0 {
			notice = fmt.Sprintf("[input] 检测到 %d 条待处理输入；已在密钥输入期间临时挂起，结束后将按原顺序恢复。", count)
		}
	} else {
		notice = discardPendingInteractiveInputForPriorityPrompt(session, "密钥输入")
	}
	if suspension != nil {
		defer suspension.Restore()
	}
	if notice != "" {
		printfDirectInteractiveOutput(session, "%s\n", notice)
	}
	readPrompt := prompt
	renderedOnSurface := showRuntimeComposerPrompt(session, prompt)
	if renderedOnSurface {
		readPrompt = ""
		defer clearRuntimeComposerPrompt(session)
	}
	if shouldRoutePriorityPromptThroughQueue(session) {
		line, err := session.InputQueue.readPriorityLineWithPrompt(ctx, readPrompt)
		newChatInputReadLifecycle(session).finishQueuedPriorityRead(err)
		return line, err
	}
	if session != nil && session.InputBox != nil {
		return newChatSecretComposerPrompt(session, readPrompt).ReadLine()
	}
	if chatIsInteractiveTerminal() {
		return ui.NewInputBox(nil).ReadTransientSecretPrompt(readPrompt)
	}
	return chatInteractiveReadTransientLine(session, ctx)
}

func chatInputQueueHasQueuedLines(session *ChatSession) bool {
	if session == nil || session.InputQueue == nil {
		return false
	}
	return session.InputQueue.pendingCount() > 0
}

func lenQueuedInteractiveInput(session *ChatSession) int {
	if session == nil || session.InputQueue == nil {
		return 0
	}
	return session.InputQueue.queuedSubmissionCount()
}

func queuedInteractiveInputState(session *ChatSession) (int, bool) {
	if session == nil {
		return 0, false
	}
	return lenQueuedInteractiveInput(session), session.queuedInputDrain
}

func discardQueuedInteractiveLines(session *ChatSession) int {
	if session == nil || session.InputQueue == nil {
		return 0
	}
	return session.InputQueue.discardPending()
}

func normalizeQueuedInputLine(line string) string {
	return strings.TrimRight(line, "\r\n")
}

func (q *chatInputQueue) takeReadySubmission() (string, bool) {
	q.draftMu.Lock()
	if strings.TrimSpace(q.readyText) == "" {
		q.readyText = ""
		q.draftMu.Unlock()
		return "", false
	}
	text := q.readyText
	q.readyText = ""
	q.draftMu.Unlock()
	return text, true
}

func (q *chatInputQueue) signalReadySubmission() {
	if q == nil {
		return
	}
	q.ensureChannels()
	select {
	case q.readySignal <- struct{}{}:
	default:
	}
}

func (q *chatInputQueue) ensureChannels() {
	if q.lines == nil {
		q.lines = make(chan chatQueuedInput, 32)
	}
	if q.priorityLines == nil {
		q.priorityLines = make(chan chatQueuedInput, 4)
	}
	if q.errs == nil {
		q.errs = make(chan error, 1)
	}
	if q.readySignal == nil {
		q.readySignal = make(chan struct{}, 1)
	}
	if q.priorityCaptureSignal == nil {
		q.priorityCaptureSignal = make(chan struct{}, 1)
	}
}

func (q *chatInputQueue) signalReadError(err error) {
	if q == nil || err == nil {
		return
	}
	q.ensureChannels()
	select {
	case q.errs <- err:
	default:
	}
}

func (q *chatInputQueue) setTerminalError(err error) {
	if q == nil || err == nil {
		return
	}
	q.terminalMu.Lock()
	if q.terminalErr == nil {
		q.terminalErr = err
	}
	q.terminalMu.Unlock()
	q.signalReadySubmission()
}

func (q *chatInputQueue) terminalError() error {
	if q == nil {
		return io.EOF
	}
	q.terminalMu.RLock()
	defer q.terminalMu.RUnlock()
	return q.terminalErr
}

func (q *chatInputQueue) setPriorityCapture(active bool, prompt string) {
	if q == nil {
		return
	}
	q.ensureChannels()
	prompt = strings.TrimRight(prompt, "\r\n")
	changed := false
	q.routeMu.Lock()
	q.mu.Lock()
	if q.priorityMode != active || q.priorityPrompt != prompt {
		changed = true
		q.priorityRevision++
	}
	q.priorityMode = active
	if active {
		q.priorityPrompt = prompt
	} else {
		q.priorityPrompt = ""
	}
	q.mu.Unlock()
	q.routeMu.Unlock()
	if changed {
		q.signalPriorityCaptureChange()
	}
}

func (q *chatInputQueue) isPriorityMode() bool {
	if q == nil {
		return false
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.priorityMode
}

func (q *chatInputQueue) capturePrompt(defaultPrompt string) (string, bool, uint64) {
	if q == nil {
		return defaultPrompt, false, 0
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.priorityMode && strings.TrimSpace(q.priorityPrompt) != "" {
		return q.priorityPrompt, true, q.priorityRevision
	}
	return defaultPrompt, false, q.priorityRevision
}

func (q *chatInputQueue) priorityCaptureRevision() uint64 {
	if q == nil {
		return 0
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.priorityRevision
}

func (q *chatInputQueue) priorityCaptureChanges() <-chan struct{} {
	if q == nil {
		return nil
	}
	q.ensureChannels()
	return q.priorityCaptureSignal
}

func (q *chatInputQueue) signalPriorityCaptureChange() {
	if q == nil {
		return
	}
	q.ensureChannels()
	select {
	case q.priorityCaptureSignal <- struct{}{}:
	default:
	}
}

func (q *chatInputQueue) setExternalInputCaptureActive(active bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.externalCaptureActive = active
}

func (q *chatInputQueue) hasExternalInputCaptureActive() bool {
	if q == nil {
		return false
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.externalCaptureActive
}

func (q *chatInputQueue) routeLine(item chatQueuedInput) chatInputRouteResult {
	return q.routeLineWithCommandGate(item, false)
}

func (q *chatInputQueue) routeLineWithCommandGate(item chatQueuedInput, enforceCommandGate bool) chatInputRouteResult {
	if q == nil {
		return chatInputRouteResult{Disposition: chatInputRouteQueued}
	}
	q.ensureChannels()
	q.routeMu.Lock()
	defer q.routeMu.Unlock()
	q.mu.RLock()
	priorityMode := q.priorityMode
	gate := q.commandGate
	q.mu.RUnlock()
	if priorityMode {
		q.priorityLines <- item
		return chatInputRouteResult{Disposition: chatInputRoutePriority}
	}
	if enforceCommandGate && isSlashCommandInput(item.Text) && gate != nil && !gate(item.Text) {
		return chatInputRouteResult{Disposition: chatInputRouteRejectedCommand}
	}
	q.queuedMu.Lock()
	q.queuedPreview = append(q.queuedPreview, item)
	q.queuedMu.Unlock()
	q.lines <- item
	return chatInputRouteResult{Disposition: chatInputRouteQueued}
}
