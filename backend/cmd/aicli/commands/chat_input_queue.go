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
	externalCaptureActive bool
	terminalMu    sync.RWMutex
	terminalErr   error

	draftMu     sync.RWMutex
	draftNotify func(active bool, lines int, text string)
	draftText   string
	draftLines  int
	draftActive bool
	readyText   string

	queuedMu      sync.Mutex
	queuedPreview []chatQueuedInput

	commandGate func(string) bool
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
	session.InputQueue.setDraftNotifier(func(active bool, lines int, text string) {
		notifyChatInputDraftState(session, active, lines, text)
	})
	session.InputQueue.setCommandGate(func(text string) bool {
		return chatInputCommandAllowed(session, text)
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

func (q *chatInputQueue) routeInputText(text string) {
	if q.rejectCommandInput(text) {
		return
	}
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
	text, ok := q.consumeDraft()
	if !ok {
		return false
	}
	if q.rejectCommandInput(text) {
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
	q.ensureChannels()
	select {
	case item := <-q.lines:
		q.noteQueuedLineRead()
		return item.Text, true
	default:
		return "", false
	}
}

func (q *chatInputQueue) noteQueuedLineRead() {
	if q == nil {
		return
	}
	q.queuedMu.Lock()
	if len(q.queuedPreview) > 0 {
		copy(q.queuedPreview, q.queuedPreview[1:])
		q.queuedPreview = q.queuedPreview[:len(q.queuedPreview)-1]
	}
	q.queuedMu.Unlock()
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
	if q == nil {
		return "", io.EOF
	}
	q.setPriorityMode(true)
	defer q.setPriorityMode(false)
	if !q.hasExternalInputCaptureActive() {
		q.startPump()
	}
	for {
		if text, ok := q.takeReadySubmission(); ok {
			return text, nil
		}
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
	return len(q.lines)
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
	discarded := 0
	for {
		select {
		case <-q.lines:
			q.noteQueuedLineRead()
			discarded++
		default:
			return discarded + q.discardDraft() + q.discardReadySubmission()
		}
	}
}

func chatInteractiveReadLine(session *ChatSession, ctx context.Context) (string, error) {
	if session != nil {
		session.lastInteractiveInputQueued = false
	}
	if session != nil && session.InputQueue != nil {
		if line, ok := session.InputQueue.readAvailableLine(); ok {
			session.lastInteractiveInputQueued = true
			return line, nil
		}
	}
	if shouldUseInteractiveLineEditor(session) {
		prompt := formatSessionUserPrompt(session)
		initial := ui.LineEditorSnapshot{}
		if session.Interaction != nil {
			initial = session.Interaction.PromptInputSnapshot()
			if initial.Text == "" {
				session.Interaction.SetPromptInput("")
			}
		}
		if shouldEnableSlashCompletion(session) {
			completion := newChatSlashCompletionController(session)
			defer completion.Clear()
			line, err := session.InputBox.ReadWithHistoryPromptWithHooks(prompt, ui.LineEditorHooks{
				InitialText:   initial.Text,
				InitialCursor: initial.Cursor,
				OnChange: func(snapshot ui.LineEditorSnapshot) {
					handleChatLineEditorChange(session, completion, snapshot)
				},
				OnBeforeTerminalWrite: func(snapshot ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
					if session.Interaction != nil {
						return session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
					}
					return ""
				},
				OnComplete: func(snapshot ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
					nextText, nextCursor, ok := completion.ApplyCompletion(snapshot.Text, snapshot.Cursor)
					if !ok {
						return ui.LineEditorReplacement{}, false
					}
					return ui.LineEditorReplacement{Text: nextText, Cursor: nextCursor}, true
				},
				OnNavigate: func(snapshot ui.LineEditorSnapshot, delta int) bool {
					return completion.Navigate(delta)
				},
				OnSubmit: func(snapshot ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
					nextText, nextCursor, ok := completion.ApplySubmission(snapshot.Text, snapshot.Cursor)
					if !ok {
						return ui.LineEditorReplacement{}, false
					}
					return ui.LineEditorReplacement{Text: nextText, Cursor: nextCursor}, true
				},
				OnCancelPopup: func(snapshot ui.LineEditorSnapshot) bool {
					return completion.Cancel()
				},
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
				if session.Interaction != nil {
					session.Interaction.ResetPromptState()
				}
				return "", io.EOF
			}
			if session.Interaction != nil && err != nil {
				session.Interaction.ResetPromptState()
			}
			return line, err
		}
		line, err := session.InputBox.ReadWithHistoryPromptWithHooks(prompt, ui.LineEditorHooks{
			InitialText:   initial.Text,
			InitialCursor: initial.Cursor,
			OnChange: func(snapshot ui.LineEditorSnapshot) {
				handleChatLineEditorChange(session, nil, snapshot)
			},
			OnBeforeTerminalWrite: func(snapshot ui.LineEditorSnapshot, render ui.LineEditorRenderSnapshot) string {
				if session.Interaction != nil {
					return session.Interaction.PromptCursorPrefix(render.LastCursorRow, render.LastCursorCol)
				}
				return ""
			},
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
			if session.Interaction != nil {
				session.Interaction.ResetPromptState()
			}
			return "", io.EOF
		}
		if session.Interaction != nil && err != nil {
			session.Interaction.ResetPromptState()
		}
		return line, err
	}
	if session != nil && session.InputQueue != nil {
		line, err := session.InputQueue.readLine(ctx)
		if session.Interaction != nil && err != nil {
			session.Interaction.ResetPromptState()
		}
		return line, err
	}
	reader := chatSessionInputReader(session)
	line, err := reader.ReadString('\n')
	if line != "" {
		return line, nil
	}
	return "", err
}

func finishChatInteractiveReadPromptState(session *ChatSession, readErr error) {
	if session == nil || session.Interaction == nil {
		return
	}
	if readErr != nil {
		session.lastInteractiveInputQueued = false
		session.Interaction.ClearPrompt()
		return
	}
	if session.lastInteractiveInputQueued {
		session.lastInteractiveInputQueued = false
		session.Interaction.RefreshStatus("")
		return
	}
	session.Interaction.ResetPromptState()
}

func handleChatLineEditorChange(session *ChatSession, completion *chatSlashCompletionController, snapshot ui.LineEditorSnapshot) {
	// Slash popup rendering must preserve the cursor from the last completed
	// editor redraw. The new snapshot is tracked after popup rendering so the
	// following editor redraw remains the only writer that lands on the new
	// cursor position.
	if completion != nil {
		completion.UpdateSnapshot(snapshot)
	}
	if session != nil && session.Interaction != nil {
		session.Interaction.SetPromptInputSnapshot(snapshot)
	}
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

func chatInteractiveReadTransientLine(session *ChatSession, ctx context.Context) (string, error) {
	if session != nil && session.InputQueue != nil {
		line, err := session.InputQueue.readPriorityLine(ctx)
		if session.Interaction != nil && err != nil {
			session.Interaction.ResetPromptState()
		}
		return line, err
	}
	if session != nil && session.InputBox != nil {
		line, err := session.InputBox.ReadTransientLine(nil)
		if errors.Is(err, ui.ErrInteractiveInputExitRequested) {
			session.Interrupt()
			if session.Interaction != nil {
				session.Interaction.ResetPromptState()
			}
			return "", ui.ErrInteractiveInputExitRequested
		}
		if errors.Is(err, ui.ErrInteractiveInputInterrupted) {
			session.Interrupt()
			if session.Interaction != nil {
				session.Interaction.ResetPromptState()
			}
			return "", io.EOF
		}
		if session.Interaction != nil && err != nil {
			session.Interaction.ResetPromptState()
		}
		return line, err
	}
	reader := chatSessionInputReader(session)
	line, err := reader.ReadString('\n')
	if line != "" {
		return line, nil
	}
	return "", err
}

func chatInteractiveReadPriorityLineWithPrompt(session *ChatSession, ctx context.Context, prompt string) (string, error) {
	if session != nil && session.InputQueue != nil {
		line, err := session.InputQueue.readPriorityLine(ctx)
		if session.Interaction != nil && err != nil {
			session.Interaction.ResetPromptState()
		}
		return line, err
	}
	if session != nil && session.InputBox != nil {
		trackPromptInput := session == nil || session.Surface == nil || !session.Surface.Enabled()
		if trackPromptInput && session.Interaction != nil {
			session.Interaction.SetPromptInput("")
		}
		line, err := session.InputBox.ReadTransientPrompt(prompt, func(text string) {
			if trackPromptInput && session.Interaction != nil {
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
			if session.Interaction != nil {
				session.Interaction.ResetPromptState()
			}
			return "", io.EOF
		}
		if session.Interaction != nil && err != nil {
			session.Interaction.ResetPromptState()
		}
		return line, err
	}
	return chatInteractiveReadTransientLine(session, ctx)
}

func chatInteractiveReadPrioritySecretWithPrompt(session *ChatSession, ctx context.Context, prompt string) (string, error) {
	if notice := discardPendingInteractiveInputForPriorityPrompt(session, "密钥输入"); notice != "" {
		beginDirectInteractiveOutput(session)
		ui.PrintWarning("%s", notice)
	}
	readPrompt := prompt
	renderedOnSurface := showRuntimeComposerPrompt(session, prompt)
	if renderedOnSurface {
		readPrompt = ""
		defer clearRuntimeComposerPrompt(session)
	}
	if session != nil && session.InputBox != nil {
		if session.Interaction != nil {
			session.Interaction.SetPromptInput("")
		}
		line, err := session.InputBox.ReadTransientSecretPrompt(readPrompt)
		if errors.Is(err, ui.ErrInteractiveInputExitRequested) {
			session.Interrupt()
			if session.Interaction != nil {
				session.Interaction.ResetPromptState()
			}
			return "", ui.ErrInteractiveInputExitRequested
		}
		if errors.Is(err, ui.ErrInteractiveInputInterrupted) {
			session.Interrupt()
			if session.Interaction != nil {
				session.Interaction.ResetPromptState()
			}
			return "", io.EOF
		}
		if session.Interaction != nil {
			session.Interaction.ResetPromptState()
		}
		return line, err
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

func (q *chatInputQueue) setPriorityMode(active bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.priorityMode = active
}

func (q *chatInputQueue) isPriorityMode() bool {
	if q == nil {
		return false
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.priorityMode
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
	q.queuedMu.Lock()
	q.queuedPreview = append(q.queuedPreview, item)
	q.queuedMu.Unlock()
	q.lines <- item
}
