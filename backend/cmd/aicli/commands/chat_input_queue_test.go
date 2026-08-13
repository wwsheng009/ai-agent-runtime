package commands

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type scriptedLineReader struct {
	mu     sync.Mutex
	chunks chan string
}

func newScriptedLineReader() *scriptedLineReader {
	return &scriptedLineReader{
		chunks: make(chan string, 8),
	}
}

func (r *scriptedLineReader) Push(chunk string) {
	r.chunks <- chunk
}

func (r *scriptedLineReader) Close() {
	close(r.chunks)
}

func (r *scriptedLineReader) Read(p []byte) (int, error) {
	chunk, ok := <-r.chunks
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}

func TestChatInputQueue_SingleLineStillSubmitsImmediately(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("hello\n")))
	queue.startPump()

	select {
	case item := <-queue.lines:
		if strings.TrimSpace(item.Text) != "hello" {
			t.Fatalf("unexpected item text: %q", item.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for single-line submission")
	}
}

func TestChatInputQueue_ReturnsExplicitResultWhenCommandGateRejects(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setCommandGate(func(text string) bool {
		return false
	})

	result := queue.routeInputText("/help\n")
	if !result.rejected() || result.Disposition != chatInputRouteRejectedCommand {
		t.Fatalf("expected explicit command rejection result, got %+v", result)
	}
	select {
	case item := <-queue.lines:
		t.Fatalf("expected rejected slash command not to be queued, got %q", item.Text)
	default:
	}

	result = queue.routeInputText("normal prompt\n")
	if !result.queued() {
		t.Fatalf("expected normal prompt to be queued, got %+v", result)
	}
	select {
	case item := <-queue.lines:
		if strings.TrimSpace(item.Text) != "normal prompt" {
			t.Fatalf("unexpected queued prompt: %q", item.Text)
		}
	default:
		t.Fatal("expected normal prompt to remain queueable")
	}
}

func TestChatInputQueue_PumpReportsRejectedSlashCommand(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("/queue clear\n")))
	queue.setCommandGate(func(string) bool { return false })
	feedback := make(chan string, 1)
	queue.setRouteFeedback(func(text string, result chatInputRouteResult) {
		if result.rejected() {
			feedback <- strings.TrimSpace(text)
		}
	})
	queue.startPump()

	select {
	case command := <-feedback:
		if command != "/queue clear" {
			t.Fatalf("unexpected rejected command feedback: %q", command)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rejected slash command feedback")
	}
	select {
	case item := <-queue.lines:
		t.Fatalf("expected rejected slash command not to be queued, got %q", item.Text)
	default:
	}
}

func TestChatInputQueue_RejectedSlashDraftRemainsEditable(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setCommandGate(func(text string) bool { return false })
	feedback := 0
	queue.setRouteFeedback(func(string, chatInputRouteResult) { feedback++ })
	queue.stageDraft("/queue clear")

	if queue.confirmDraft() {
		t.Fatal("expected rejected slash draft not to be confirmed")
	}
	if !queue.hasDraft() {
		t.Fatal("expected rejected slash draft to remain staged")
	}
	queue.draftMu.RLock()
	draft := queue.draftText
	queue.draftMu.RUnlock()
	if draft != "/queue clear" {
		t.Fatalf("expected rejected draft to remain unchanged, got %q", draft)
	}
	if feedback != 1 {
		t.Fatalf("expected rejected draft confirmation to render feedback once, got %d", feedback)
	}
}

func TestRenderBusyInputRouteFeedbackExplainsRejectedQueueClear(t *testing.T) {
	var output strings.Builder
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	coord.SetWriter(&output)
	session.Interaction = coord

	renderBusyInputRouteFeedback(session, "/queue clear", chatInputRouteResult{
		Disposition: chatInputRouteRejectedCommand,
	})

	rendered := output.String()
	for _, want := range []string{"/queue clear", "Ready", "队列保持不变"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected busy rejection feedback to contain %q, got %q", want, rendered)
		}
	}
}

func TestChatInputCommandQueuableAllowsSafeSlashWhileBusy(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.StartWaiting()

	tests := []struct {
		input string
		want  bool
	}{
		{"normal prompt", true},
		{"/model", true},
		{"/model gpt-5", true},
		{"/help", true},
		{"/status", true},
		{"/session", true},
		{"/history", true},
		{"/queue", true},
		{"/queue status", true},
		{"/queue clear", false},
		{"/exit", false},
		{"/quit", false},
		{"/backtrack", false},
		{"/resume", false},
		{"/login", false},
		{"/theme", false},
		{"/stream", false},
		{"/skill", false},
		{"/compact", false},
		{"/reasoning_effort", false},
		{"/permission-mode", false},
		{"/approval-reuse", false},
		{"/attach", false},
	}
	for _, tt := range tests {
		if got := chatInputCommandQueuable(session, tt.input); got != tt.want {
			t.Fatalf("chatInputCommandQueuable(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestInterruptChatTurnFromBusyInputCancelRestoresPendingInputToDraft(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.routeInputText("queued follow-up\n")
	queue.stageDraft("unfinished draft")
	session := &ChatSession{
		NoInteractive: true,
		InputQueue:    queue,
		cancelCtx:     ctx,
		cancelFunc:    cancel,
	}

	interruptChatTurnFromBusyInputCancel(session)

	if !session.IsInterrupted() {
		t.Fatal("expected busy-input ESC to interrupt the active turn")
	}
	if queue.pendingCount() != 0 || !queue.hasDraft() {
		t.Fatalf("expected ESC to restore queued input as an unconfirmed draft, pending=%d draft=%v", queue.pendingCount(), queue.hasDraft())
	}
	queue.draftMu.RLock()
	draftText := queue.draftText
	queue.draftMu.RUnlock()
	if !strings.Contains(draftText, "queued follow-up") || !strings.Contains(draftText, "unfinished draft") {
		t.Fatalf("expected queued input and draft restored together, got %q", draftText)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected busy-input ESC to cancel active context")
	}
}

func TestInterruptChatTurnFromBusyInputCancelRestoresComposerDraftWithQueuedInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.routeInputText("queued follow-up\n")
	session := &ChatSession{
		InputQueue: queue,
		cancelCtx:  ctx,
		cancelFunc: cancel,
	}
	session.Interaction = newChatInteractionCoordinator(session)
	session.Interaction.SetPromptInput("typing in busy composer")

	interruptChatTurnFromBusyInputCancel(session)

	if !session.IsInterrupted() {
		t.Fatal("expected busy-input ESC to interrupt the active turn")
	}
	snapshot := session.Interaction.PromptInputSnapshot()
	if !strings.Contains(snapshot.Text, "queued follow-up") || !strings.Contains(snapshot.Text, "typing in busy composer") {
		t.Fatalf("expected queued input and live composer draft restored together, got %q", snapshot.Text)
	}
	if queue.pendingCount() != 0 {
		t.Fatalf("expected queued input to move into composer, got %d pending", queue.pendingCount())
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expected busy-input ESC to cancel active context")
	}
}

func TestChatInputQueue_QueuedPreviewLinesFollowPendingLines(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.routeInputText("first prompt\n")
	queue.routeInputText("second prompt\n")

	previews := queue.queuedPreviewLines(5)
	if len(previews) != 2 || previews[0] != "first prompt" || previews[1] != "second prompt" {
		t.Fatalf("unexpected queued previews before read: %#v", previews)
	}

	line, ok := queue.readAvailableLine()
	if !ok {
		t.Fatal("expected queued line to be readable")
	}
	if normalizeQueuedInputLine(line) != "first prompt" {
		t.Fatalf("expected first queued line, got %q", line)
	}

	previews = queue.queuedPreviewLines(5)
	if len(previews) != 1 || previews[0] != "second prompt" {
		t.Fatalf("expected preview list to drop consumed line, got %#v", previews)
	}
}

func TestChatInputQueue_SuspendRestorePreservesOrderDraftAndPreviewWithoutBlocking(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.lines = make(chan chatQueuedInput, 1)
	queue.routeInputText("original queued\n")
	queue.stageDraft("confirmed draft\n")
	if !queue.confirmDraft() {
		t.Fatal("expected first draft to become ready submission")
	}
	queue.stageDraft("still editing")

	suspension := queue.suspendPendingInput()
	if suspension.Count() != 3 {
		t.Fatalf("expected queued, ready and draft inputs to be suspended, got %d", suspension.Count())
	}
	if queue.pendingCount() != 0 || queue.hasReadySubmission() || queue.hasDraft() {
		t.Fatal("expected suspended input to be absent from active queue state")
	}
	if previews := queue.queuedPreviewLines(5); len(previews) != 0 {
		t.Fatalf("expected active preview to be empty while suspended, got %#v", previews)
	}

	// Fill the channel again before restore. Restored items use queuedFront, so
	// restoration must not block trying to write back into the full channel.
	queue.routeInputText("queued during prompt\n")
	restored := make(chan int, 1)
	go func() { restored <- suspension.Restore() }()
	select {
	case count := <-restored:
		if count != 3 {
			t.Fatalf("expected three restored inputs, got %d", count)
		}
	case <-time.After(time.Second):
		t.Fatal("restore blocked on a full queue channel")
	}

	previews := queue.queuedPreviewLines(5)
	wantPreviews := []string{"confirmed draft", "original queued", "queued during prompt"}
	if strings.Join(previews, "|") != strings.Join(wantPreviews, "|") {
		t.Fatalf("unexpected restored preview order: got %#v want %#v", previews, wantPreviews)
	}
	for _, want := range wantPreviews {
		line, ok := queue.readAvailableLine()
		if !ok || normalizeQueuedInputLine(line) != want {
			t.Fatalf("expected restored read %q, got %q ok=%v", want, line, ok)
		}
	}
	if !queue.hasDraft() {
		t.Fatal("expected unconfirmed draft to be restored after queued submissions")
	}
	if got := suspension.Restore(); got != 0 {
		t.Fatalf("expected restore to be idempotent, got %d on second call", got)
	}
}

func TestSuspendPendingInteractiveInputForPriorityPromptReportsAndRestoresQueue(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.routeInputText("follow up\n")
	session := &ChatSession{InputQueue: queue}

	suspension, notice := suspendPendingInteractiveInputForPriorityPrompt(session, "审批提示")
	if suspension == nil || suspension.Count() != 1 {
		t.Fatalf("expected one suspended input, got %#v", suspension)
	}
	if !strings.Contains(notice, "审批提示") || !strings.Contains(notice, "临时挂起") || strings.Contains(notice, "丢弃") {
		t.Fatalf("unexpected suspension notice: %q", notice)
	}
	if queue.pendingCount() != 0 {
		t.Fatalf("expected active queue to be empty during priority prompt, got %d", queue.pendingCount())
	}

	suspension.Restore()
	line, ok := queue.readAvailableLine()
	if !ok || normalizeQueuedInputLine(line) != "follow up" {
		t.Fatalf("expected suspended follow-up to be restored, got %q ok=%v", line, ok)
	}
}

func TestChatInteractiveReadPrioritySecretSuspendsAndRestoresOrdinaryInput(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setExternalInputCaptureActive(true)
	queue.routeInputText("ordinary queued\n")
	queue.stageDraft("ready submission")
	if !queue.confirmDraft() {
		t.Fatal("expected staged input to become a ready submission")
	}
	queue.stageDraft("unfinished draft")

	session := &ChatSession{InputQueue: queue}
	interaction := newChatInteractionCoordinator(session)
	session.Interaction = interaction
	result := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		line, err := chatInteractiveReadPrioritySecretWithPrompt(session, context.Background(), "Secret: ")
		if err != nil {
			errs <- err
			return
		}
		result <- line
	}()

	requireEventuallyPriorityMode(t, queue)
	if got := interaction.InputMode(); got != chatInputModeSecret {
		t.Fatalf("expected secret input mode while prompt owns input, got %q", got)
	}
	if queue.pendingCount() != 0 || queue.hasReadySubmission() || queue.hasDraft() {
		t.Fatal("ordinary queued, ready, and draft input must be suspended during secret capture")
	}
	queue.routeInputText("s3cr3t")

	select {
	case err := <-errs:
		t.Fatalf("secret read failed: %v", err)
	case line := <-result:
		if line != "s3cr3t" {
			t.Fatalf("unexpected secret answer %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for secret answer")
	}
	if got := interaction.InputMode(); got != chatInputModeChat {
		t.Fatalf("expected secret input lease to restore chat mode, got %q", got)
	}

	for _, want := range []string{"ready submission", "ordinary queued"} {
		line, ok := queue.readAvailableLine()
		if !ok || normalizeQueuedInputLine(line) != want {
			t.Fatalf("expected restored ordinary input %q, got %q ok=%v", want, line, ok)
		}
	}
	queue.draftMu.RLock()
	draftText, draftActive := queue.draftText, queue.draftActive
	queue.draftMu.RUnlock()
	if !draftActive || draftText != "unfinished draft" {
		t.Fatalf("expected unfinished draft to be restored, active=%v text=%q", draftActive, draftText)
	}
}

func TestChatInputQueue_MultilinePasteStaysDraftUntilEnter(t *testing.T) {
	oldDelay := inputPasteSettleDelay
	inputPasteSettleDelay = func() time.Duration {
		return 5 * time.Millisecond
	}
	defer func() {
		inputPasteSettleDelay = oldDelay
	}()

	reader := newScriptedLineReader()
	queue := newChatInputQueue(bufio.NewReader(reader))
	events := make(chan struct{}, 1)
	queue.setDraftNotifier(func(active bool, lines int, text string) {
		if active && lines >= 2 {
			events <- struct{}{}
		}
	})
	queue.startPump()

	reader.Push("first\n")
	reader.Push("second\n")

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for draft state")
	}

	select {
	case item := <-queue.lines:
		t.Fatalf("unexpected auto submission before explicit enter: %q", item.Text)
	default:
	}

	if !queue.hasDraft() {
		t.Fatal("expected draft to remain staged before explicit enter")
	}

	reader.Push("\n")

	line, err := queue.readLine(context.Background())
	if err != nil {
		t.Fatalf("readLine after confirm: %v", err)
	}
	if strings.TrimSpace(line) != "first\nsecond" {
		t.Fatalf("unexpected confirmed paste: %q", line)
	}

	reader.Close()
}

func TestChatInputQueue_BufferedReadAheadStaysDraftUntilEnter(t *testing.T) {
	oldDelay := inputPasteSettleDelay
	oldShouldDiscard := shouldDiscardPendingInput
	oldPendingLineInput := pendingConsoleLineInput
	oldPendingTextInput := pendingConsoleTextInput
	inputPasteSettleDelay = func() time.Duration {
		return 5 * time.Millisecond
	}
	shouldDiscardPendingInput = func() bool {
		return true
	}
	pendingConsoleLineInput = func() (bool, error) {
		return false, nil
	}
	pendingConsoleTextInput = func() (bool, error) {
		return false, nil
	}
	defer func() {
		inputPasteSettleDelay = oldDelay
		shouldDiscardPendingInput = oldShouldDiscard
		pendingConsoleLineInput = oldPendingLineInput
		pendingConsoleTextInput = oldPendingTextInput
	}()

	reader := newScriptedLineReader()
	queue := newChatInputQueue(bufio.NewReader(reader))
	events := make(chan struct{}, 1)
	queue.setDraftNotifier(func(active bool, lines int, text string) {
		if active && lines >= 1 {
			events <- struct{}{}
		}
	})
	queue.startPump()

	reader.Push("first\nsecond\n")

	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for buffered draft state")
	}

	select {
	case item := <-queue.lines:
		t.Fatalf("unexpected auto submission before explicit enter: %q", item.Text)
	default:
	}

	if !queue.hasDraft() {
		t.Fatal("expected buffered read-ahead line to remain staged before explicit enter")
	}

	reader.Push("\n")

	line, err := queue.readLine(context.Background())
	if err != nil {
		t.Fatalf("readLine after confirm: %v", err)
	}
	if strings.TrimSpace(line) != "first\nsecond" {
		t.Fatalf("unexpected confirmed paste: %q", line)
	}

	reader.Close()
}

func TestChatInputQueue_DraftNotifierReceivesLatestDraftText(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))

	var (
		active bool
		lines  int
		text   string
	)
	queue.setDraftNotifier(func(nextActive bool, nextLines int, nextText string) {
		active = nextActive
		lines = nextLines
		text = nextText
	})

	queue.stageDraft("first\nsecond")
	if !active {
		t.Fatal("expected staged draft to activate notifier")
	}
	if lines != 2 {
		t.Fatalf("expected staged draft line count 2, got %d", lines)
	}
	if !strings.Contains(text, "first") || !strings.Contains(text, "second") {
		t.Fatalf("expected staged draft text to be forwarded, got %q", text)
	}

	queue.appendDraft("\nthird")
	if lines != 3 {
		t.Fatalf("expected appended draft line count 3, got %d", lines)
	}
	if !strings.Contains(text, "third") {
		t.Fatalf("expected appended draft text to be forwarded, got %q", text)
	}
}

func TestChatInputQueue_StagesBufferedSingleLineWhenConsoleStillHasLineInput(t *testing.T) {
	oldPendingLineInput := pendingConsoleLineInput
	oldPendingTextInput := pendingConsoleTextInput
	oldDelay := inputPasteSettleDelay
	pendingConsoleLineInput = func() (bool, error) {
		return true, nil
	}
	pendingConsoleTextInput = func() (bool, error) {
		return false, nil
	}
	inputPasteSettleDelay = func() time.Duration {
		return 5 * time.Millisecond
	}
	defer func() {
		pendingConsoleLineInput = oldPendingLineInput
		pendingConsoleTextInput = oldPendingTextInput
		inputPasteSettleDelay = oldDelay
	}()

	reader := newScriptedLineReader()
	queue := newChatInputQueue(bufio.NewReader(reader))
	queue.startPump()

	reader.Push("first\n")

	select {
	case item := <-queue.lines:
		t.Fatalf("unexpected auto submission for staged buffered line: %q", item.Text)
	case <-time.After(200 * time.Millisecond):
	}

	if !queue.hasDraft() {
		t.Fatal("expected buffered line to be staged as draft")
	}

	reader.Push("\n")

	line, err := queue.readLine(context.Background())
	if err != nil {
		t.Fatalf("readLine after confirm: %v", err)
	}
	if strings.TrimSpace(line) != "first" {
		t.Fatalf("unexpected confirmed staged line: %q", line)
	}

	reader.Close()
}

func TestChatInputQueue_ClearDraftRemovesPendingInput(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("first\nsecond\n")))
	queue.stageDraft("first\nsecond")

	if got := queue.discardPending(); got == 0 {
		t.Fatal("expected draft to be discarded")
	}
	if queue.hasDraft() {
		t.Fatal("expected draft to be cleared")
	}
}

func TestChatInputQueue_PendingCountIgnoresDraft(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.stageDraft("first\nsecond")

	if got := queue.pendingCount(); got != 0 {
		t.Fatalf("expected draft to be excluded from pending count, got %d", got)
	}
	if !queue.hasDraft() {
		t.Fatal("expected draft to remain staged")
	}
}

func TestShouldUseInteractiveLineEditor_EnabledOnWindowsTTY(t *testing.T) {
	oldGOOS := chatRuntimeGOOS
	oldInteractive := chatIsInteractiveTerminal
	defer func() {
		chatRuntimeGOOS = oldGOOS
		chatIsInteractiveTerminal = oldInteractive
	}()

	chatRuntimeGOOS = "windows"
	chatIsInteractiveTerminal = func() bool { return true }
	session := &ChatSession{InputBox: ui.NewInputBox(nil)}

	if !shouldUseInteractiveLineEditor(session) {
		t.Fatal("expected Windows TTY to use the line editor")
	}
}

func TestShouldUseInteractiveLineEditor_EnabledOnUnixTTY(t *testing.T) {
	oldGOOS := chatRuntimeGOOS
	oldInteractive := chatIsInteractiveTerminal
	defer func() {
		chatRuntimeGOOS = oldGOOS
		chatIsInteractiveTerminal = oldInteractive
	}()

	chatRuntimeGOOS = "linux"
	chatIsInteractiveTerminal = func() bool { return true }
	session := &ChatSession{InputBox: ui.NewInputBox(nil)}

	if !shouldUseInteractiveLineEditor(session) {
		t.Fatal("expected Unix TTY to use the line editor")
	}
}

func TestChatInteractiveReadLine_DrainsQueuedInputBeforeInteractiveLineEditor(t *testing.T) {
	oldGOOS := chatRuntimeGOOS
	oldInteractive := chatIsInteractiveTerminal
	defer func() {
		chatRuntimeGOOS = oldGOOS
		chatIsInteractiveTerminal = oldInteractive
	}()

	chatRuntimeGOOS = "windows"
	chatIsInteractiveTerminal = func() bool { return true }

	restore := withTransientStdio(t, "typed\n")
	defer restore()

	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.lines <- chatQueuedInput{Text: "queued\n", Source: "stdin"}

	session := &ChatSession{
		InputBox:    ui.NewInputBox(nil),
		InputQueue:  queue,
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
	}

	line, err := chatInteractiveReadLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadLine: %v", err)
	}
	if normalizeQueuedInputLine(line) != "queued" {
		t.Fatalf("expected queued input to be drained before transient input box, got %q", line)
	}

	if nextLine, err := session.InputReader.ReadString('\n'); err != nil {
		t.Fatalf("expected shared reader to remain untouched: %v", err)
	} else if nextLine != "stale\n" {
		t.Fatalf("expected shared reader input to remain untouched, got %q", nextLine)
	}

	if queue.pendingCount() != 0 {
		t.Fatalf("expected queued input to be consumed, got %d pending", queue.pendingCount())
	}
	if got := session.InputBox.GetHistorySize(); got != 1 {
		t.Fatalf("expected queued input to be recorded in prompt history, got size %d", got)
	}
	if history, ok := session.InputBox.GetHistoryAt(0); !ok || history != "queued" {
		t.Fatalf("expected queued input history entry, got %q ok=%v", history, ok)
	}
}

func TestChatInteractiveReadTransientLine_UsesTransientInputBoxWithoutSharedReader(t *testing.T) {
	restore := withTransientStdio(t, "answer\n")
	defer restore()

	session := &ChatSession{
		InputBox:    ui.NewInputBox(nil),
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
	}

	line, err := chatInteractiveReadTransientLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadTransientLine: %v", err)
	}
	if normalizeQueuedInputLine(line) != "answer" {
		t.Fatalf("expected transient input answer, got %q", line)
	}

	nextLine, err := session.InputReader.ReadString('\n')
	if err != nil {
		t.Fatalf("expected shared reader to remain untouched: %v", err)
	}
	if nextLine != "stale\n" {
		t.Fatalf("expected shared reader input to remain untouched, got %q", nextLine)
	}
}

func TestChatInteractiveReadPriorityLineWithPrompt_UsesTransientInputBoxWithoutSharedReader(t *testing.T) {
	restore := withTransientStdio(t, "2\n")
	defer restore()

	session := &ChatSession{
		InputBox:    ui.NewInputBox(nil),
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
	}

	line, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), "请选择: ")
	if err != nil {
		t.Fatalf("chatInteractiveReadPriorityLineWithPrompt: %v", err)
	}
	if normalizeQueuedInputLine(line) != "2" {
		t.Fatalf("expected transient popup choice 2, got %q", line)
	}

	nextLine, err := session.InputReader.ReadString('\n')
	if err != nil {
		t.Fatalf("expected shared reader to remain untouched: %v", err)
	}
	if nextLine != "stale\n" {
		t.Fatalf("expected shared reader input to remain untouched, got %q", nextLine)
	}
}

func TestChatInteractiveReadLine_DeduplicatesQueuedPromptHistory(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.lines <- chatQueuedInput{Text: "repeat\n", Source: "stdin"}

	inputBox := ui.NewInputBox(nil)
	inputBox.AddToHistory("repeat")
	session := &ChatSession{
		InputBox:   inputBox,
		InputQueue: queue,
	}

	line, err := chatInteractiveReadLine(session, context.Background())
	if err != nil {
		t.Fatalf("chatInteractiveReadLine: %v", err)
	}
	if normalizeQueuedInputLine(line) != "repeat" {
		t.Fatalf("expected queued repeat input, got %q", line)
	}
	if got := inputBox.GetHistorySize(); got != 1 {
		t.Fatalf("expected adjacent duplicate queued input not to be recorded twice, got history size %d", got)
	}
}

func TestChatInputReadLifecycleMarksQueuedReadyLineAndRecordsHistory(t *testing.T) {
	session := &ChatSession{InputBox: ui.NewInputBox(nil)}
	lifecycle := newChatInputReadLifecycle(session)

	lifecycle.beginReadyRead()
	lifecycle.markReadyLineQueued("queued command\n")

	if !session.lastInteractiveInputQueued {
		t.Fatal("expected queued ready line to mark session as queued")
	}
	if got := session.InputBox.GetHistorySize(); got != 1 {
		t.Fatalf("expected queued ready line to be recorded in history, got %d", got)
	}
	if history, ok := session.InputBox.GetHistoryAt(0); !ok || history != "queued command" {
		t.Fatalf("expected normalized queued history, got %q ok=%v", history, ok)
	}
}

func TestChatInputReadLifecycleResetsPromptOnQueuedReadError(t *testing.T) {
	session := &ChatSession{InputBox: ui.NewInputBox(nil)}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("stale queued draft")

	newChatInputReadLifecycle(session).finishQueuedReadyRead("", errors.New("queue failed"))

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected queued read error to reset prompt state, got %#v", snapshot)
	}
}

func TestChatInputReadLifecycleFinishMainReadClearsPromptOnError(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("failed read draft")

	newChatInputReadLifecycle(session).finishMainRead(errors.New("read failed"))

	if session.lastInteractiveInputQueued {
		t.Fatal("expected read error to clear queued marker")
	}
	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected read error to clear prompt state, got %#v", snapshot)
	}
}

func TestChatInputReadLifecycleFinishMainReadKeepsDraftForTranscriptPager(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("inspect this draft")

	newChatInputReadLifecycle(session).finishMainRead(ui.ErrInteractiveInputTranscriptRequested)

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "inspect this draft" {
		t.Fatalf("transcript pager should preserve prompt state, got %#v", snapshot)
	}
}

func TestChatInputReadLifecycleFinishMainReadResetsPromptAfterDirectRead(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("submitted draft")

	newChatInputReadLifecycle(session).finishMainRead(nil)

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected direct read completion to reset prompt state, got %#v", snapshot)
	}
}

func TestChatInputReadLifecycleKeepsPromptOnPrioritySuccess(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("main draft")

	newChatInputReadLifecycle(session).finishQueuedPriorityRead(nil)

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "main draft" {
		t.Fatalf("expected successful priority queue read not to clear main draft, got %#v", snapshot)
	}
}

func TestChatInputReadLifecycleResetsPromptOnPriorityError(t *testing.T) {
	session := &ChatSession{}
	coord := newChatInteractionCoordinator(session)
	session.Interaction = coord
	coord.SetPromptInput("stale priority draft")

	newChatInputReadLifecycle(session).finishQueuedPriorityRead(errors.New("priority failed"))

	if snapshot := coord.PromptInputSnapshot(); snapshot.Text != "" {
		t.Fatalf("expected priority queue error to reset prompt state, got %#v", snapshot)
	}
}

func TestChatInputQueue_PriorityReadPublishesCapturePrompt(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setExternalInputCaptureActive(true)

	result := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		line, err := queue.readPriorityLineWithPrompt(context.Background(), "[approval] allow bash? [y/N]: ")
		if err != nil {
			errs <- err
			return
		}
		result <- line
	}()

	deadline := time.After(time.Second)
	for {
		prompt, priority, _ := queue.capturePrompt("default> ")
		if priority && prompt == "[approval] allow bash? [y/N]: " {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for priority capture prompt, got prompt=%q priority=%v", prompt, priority)
		case <-time.After(10 * time.Millisecond):
		}
	}

	queue.priorityLines <- chatQueuedInput{Text: "y", Source: "stdin"}

	select {
	case err := <-errs:
		t.Fatalf("readPriorityLineWithPrompt returned error: %v", err)
	case line := <-result:
		if line != "y" {
			t.Fatalf("expected priority input y, got %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for priority line result")
	}

	prompt, priority, _ := queue.capturePrompt("default> ")
	if priority || prompt != "default> " {
		t.Fatalf("expected capture prompt to reset, got prompt=%q priority=%v", prompt, priority)
	}
}

func TestChatInputQueue_PriorityReadNeverConsumesReadySubmission(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setExternalInputCaptureActive(true)
	queue.draftMu.Lock()
	queue.readyText = "ordinary queued message\n"
	queue.draftMu.Unlock()

	result := make(chan string, 1)
	go func() {
		line, err := queue.readPriorityLineWithPrompt(context.Background(), "[approval] allow? ")
		if err != nil {
			result <- "error: " + err.Error()
			return
		}
		result <- line
	}()
	requireEventuallyPriorityMode(t, queue)

	select {
	case line := <-result:
		t.Fatalf("priority read consumed ordinary ready submission: %q", line)
	case <-time.After(50 * time.Millisecond):
	}

	queue.routeLine(chatQueuedInput{Text: "y", Source: "test"})
	select {
	case line := <-result:
		if line != "y" {
			t.Fatalf("expected explicit priority answer, got %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for explicit priority answer")
	}

	line, ok := queue.readAvailableLine()
	if !ok || normalizeQueuedInputLine(line) != "ordinary queued message" {
		t.Fatalf("expected ordinary ready submission to remain queued, got %q ok=%v", line, ok)
	}
}

func TestChatInputQueue_PriorityAnswerBypassesBusySlashCommandGate(t *testing.T) {
	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("")))
	queue.setCommandGate(func(string) bool { return false })
	queue.setPriorityCapture(true, "[question] path: ")
	defer queue.setPriorityCapture(false, "")

	result := queue.routeInputText("/workspace/docs")
	if result.Disposition != chatInputRoutePriority {
		t.Fatalf("expected slash-prefixed answer to reach priority prompt, got %+v", result)
	}
	select {
	case item := <-queue.priorityLines:
		if item.Text != "/workspace/docs" {
			t.Fatalf("unexpected priority answer: %q", item.Text)
		}
	default:
		t.Fatal("expected slash-prefixed priority answer to be readable")
	}
}

func requireEventuallyPriorityMode(t *testing.T, queue *chatInputQueue) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !queue.isPriorityMode() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for priority mode")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestChatInteractiveReadPrioritySecretWithPrompt_UsesSecretInputBoxWithoutSharedReader(t *testing.T) {
	restore := withTransientStdio(t, "secret-value\n")
	defer restore()

	queue := &chatInputQueue{
		lines: make(chan chatQueuedInput, 1),
	}
	queue.lines <- chatQueuedInput{Text: "queued\n", Source: "stdin"}
	session := &ChatSession{
		InputBox:    ui.NewInputBox(nil),
		InputReader: bufio.NewReader(strings.NewReader("stale\n")),
		InputQueue:  queue,
	}
	session.InputBox.AddToHistory("keep")

	line, err := chatInteractiveReadPrioritySecretWithPrompt(session, context.Background(), "API key: ")
	if err != nil {
		t.Fatalf("chatInteractiveReadPrioritySecretWithPrompt: %v", err)
	}
	if normalizeQueuedInputLine(line) != "secret-value" {
		t.Fatalf("expected secret input, got %q", line)
	}
	if got := session.InputBox.GetHistorySize(); got != 1 {
		t.Fatalf("expected secret input not to add history, got size %d", got)
	}
	nextLine, err := session.InputReader.ReadString('\n')
	if err != nil {
		t.Fatalf("expected shared reader to remain untouched: %v", err)
	}
	if nextLine != "stale\n" {
		t.Fatalf("expected shared reader input to remain untouched, got %q", nextLine)
	}
	if session.InputQueue.pendingCount() != 1 {
		t.Fatalf("expected queued input to be restored after secret prompt")
	}
	queued, ok := session.InputQueue.readAvailableLine()
	if !ok || normalizeQueuedInputLine(queued) != "queued" {
		t.Fatalf("expected original queued input to remain available, got %q ok=%v", queued, ok)
	}
}

func withTransientStdio(t *testing.T, input string) func() {
	t.Helper()

	oldStdin := os.Stdin
	oldStdout := os.Stdout
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdin: %v", err)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		t.Fatalf("os.Pipe stdout: %v", err)
	}

	os.Stdin = stdinRead
	os.Stdout = stdoutWrite
	if _, err := stdinWrite.WriteString(input); err != nil {
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		t.Fatalf("write transient stdin: %v", err)
	}
	if err := stdinWrite.Close(); err != nil {
		_ = stdinRead.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		t.Fatalf("close transient stdin writer: %v", err)
	}

	return func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
		_ = stdinRead.Close()
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
	}
}
