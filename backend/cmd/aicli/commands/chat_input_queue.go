package commands

import (
	"bufio"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

var chatRuntimeGOOS = runtime.GOOS
var chatIsInteractiveTerminal = ui.IsInteractiveTerminal

type chatQueuedInput struct {
	Text       string
	Source     string
	EnqueuedAt time.Time
}

type chatInputQueue struct {
	reader        *bufio.Reader
	lines         chan chatQueuedInput
	priorityLines chan chatQueuedInput
	errs          chan error
	readySignal   chan struct{}
	start         sync.Once
	mu            sync.RWMutex
	priorityMode  bool

	draftMu     sync.RWMutex
	draftNotify func(active bool, lines int)
	draftText   string
	draftLines  int
	draftActive bool
	readyText   string
}

func newChatInputQueue(reader *bufio.Reader) *chatInputQueue {
	if reader == nil {
		reader = newChatInputReader()
	}
	return &chatInputQueue{
		reader:        reader,
		lines:         make(chan chatQueuedInput, 32),
		priorityLines: make(chan chatQueuedInput, 4),
		errs:          make(chan error, 1),
		readySignal:   make(chan struct{}, 1),
	}
}

func ensureChatInputQueue(session *ChatSession) *chatInputQueue {
	if session == nil {
		return nil
	}
	if session.InputQueue == nil {
		session.InputQueue = newChatInputQueue(chatSessionInputReader(session))
	}
	session.InputQueue.setDraftNotifier(func(active bool, lines int) {
		notifyChatInputDraftState(session, active, lines)
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
				return
			}
			text := normalizeBatchText(ev.Text)
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
				if ev.Err != nil && !errors.Is(ev.Err, io.EOF) {
					q.handlePumpError(ev.Err)
				}
				continue
			}

			batch.WriteString(ev.Text)
			if ev.Buffered > 0 {
				batchHasBufferedInput = true
			}
			resetTimer()
			if ev.Err != nil && !errors.Is(ev.Err, io.EOF) {
				q.handlePumpError(ev.Err)
			}
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
			if !errors.Is(err, io.EOF) {
				events <- stdinLineEvent{Err: err}
			}
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

func (q *chatInputQueue) routeInputText(text string) {
	q.routeLine(chatQueuedInput{
		Text:       text,
		Source:     "stdin",
		EnqueuedAt: time.Now().UTC(),
	})
}

func (q *chatInputQueue) stageDraft(text string) {
	text = normalizeBatchText(text)
	if text == "" {
		return
	}
	q.draftMu.Lock()
	notify := !q.draftActive
	q.draftText = text
	q.draftLines = countInputLines(text)
	q.draftActive = true
	lines := q.draftLines
	fn := q.draftNotify
	q.draftMu.Unlock()
	if notify && fn != nil {
		fn(true, lines)
	}
}

func (q *chatInputQueue) appendDraft(text string) {
	text = normalizeBatchText(text)
	if text == "" {
		return
	}
	q.draftMu.Lock()
	notify := !q.draftActive
	if q.draftText == "" {
		q.draftText = text
	} else {
		q.draftText = q.draftText + text
	}
	q.draftLines = countInputLines(q.draftText)
	q.draftActive = true
	lines := q.draftLines
	fn := q.draftNotify
	q.draftMu.Unlock()
	if notify && fn != nil {
		fn(true, lines)
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
		fn(false, 0)
	}
	return text, true
}

func (q *chatInputQueue) confirmDraft() bool {
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
		fn(false, 0)
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

func (q *chatInputQueue) setDraftNotifier(fn func(active bool, lines int)) {
	if q == nil {
		return
	}
	q.draftMu.Lock()
	q.draftNotify = fn
	q.draftMu.Unlock()
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
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

func normalizeInputLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(text, "\r\n"), "\n")
}

func countInputLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func isSubmissionCommand(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "!")
}

func (q *chatInputQueue) handlePumpError(err error) {
	if errors.Is(err, io.EOF) {
		return
	}
	select {
	case q.errs <- err:
	default:
	}
	time.Sleep(50 * time.Millisecond)
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
		select {
		case item := <-q.lines:
			return item.Text, nil
		default:
		}
		select {
		case item := <-q.lines:
			return item.Text, nil
		case err := <-q.errs:
			select {
			case item := <-q.lines:
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
	if q == nil {
		return "", io.EOF
	}
	q.setPriorityMode(true)
	defer q.setPriorityMode(false)
	q.startPump()
	for {
		if text, ok := q.takeReadySubmission(); ok {
			return text, nil
		}
		select {
		case item := <-q.priorityLines:
			return item.Text, nil
		default:
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
	return len(q.lines)
}

func (q *chatInputQueue) discardPending() int {
	if q == nil {
		return 0
	}
	q.ensureChannels()
	discarded := 0
	for {
		select {
		case <-q.lines:
			discarded++
		default:
			return discarded + q.discardDraft() + q.discardReadySubmission()
		}
	}
}

func chatInteractiveReadLine(session *ChatSession, ctx context.Context) (string, error) {
	if shouldUseInteractiveLineEditor(session) {
		prompt := formatSessionUserPrompt(session)
		if session.Interaction != nil {
			session.Interaction.SetPromptInput("")
		}
		line, err := session.InputBox.ReadWithHistoryPrompt(prompt, func(text string) {
			if session.Interaction != nil {
				session.Interaction.SetPromptInput(text)
			}
		})
		if errors.Is(err, ui.ErrInteractiveInputExitRequested) {
			session.Interrupt()
			if session.Interaction != nil {
				session.Interaction.ResetPromptState()
			}
			return "", ui.ErrInteractiveInputExitRequested
		}
		if errors.Is(err, ui.ErrInteractiveInputInterrupted) {
			session.Interrupt()
			return "", io.EOF
		}
		if session.Interaction != nil && !errors.Is(err, io.EOF) {
			session.Interaction.ResetPromptState()
		}
		return line, err
	}
	if session != nil && session.InputQueue != nil {
		return session.InputQueue.readLine(ctx)
	}
	reader := chatSessionInputReader(session)
	line, err := reader.ReadString('\n')
	if line != "" {
		return line, nil
	}
	return "", err
}

func shouldUseInteractiveLineEditor(session *ChatSession) bool {
	if session == nil || session.InputBox == nil {
		return false
	}
	if chatRuntimeGOOS == "windows" {
		return false
	}
	return chatIsInteractiveTerminal()
}

func chatInteractiveReadPriorityLine(session *ChatSession, ctx context.Context) (string, error) {
	if session != nil && session.InputQueue != nil {
		return session.InputQueue.readPriorityLine(ctx)
	}
	return chatInteractiveReadLine(session, ctx)
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
	return session.InputQueue.pendingCount()
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
}

func (q *chatInputQueue) setPriorityMode(active bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.priorityMode = active
}

func (q *chatInputQueue) routeLine(item chatQueuedInput) {
	if q == nil {
		return
	}
	q.ensureChannels()
	q.mu.RLock()
	priorityMode := q.priorityMode
	q.mu.RUnlock()
	if priorityMode {
		q.priorityLines <- item
		return
	}
	q.lines <- item
}
