package commands

import (
	"bufio"
	"context"
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

func TestChatInteractiveReadLine_UsesInteractiveLineEditorOnWindowsTTY(t *testing.T) {
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

	queue := newChatInputQueue(bufio.NewReader(strings.NewReader("ignored\n")))
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
	if normalizeQueuedInputLine(line) != "typed" {
		t.Fatalf("expected Windows interactive read to use transient input box, got %q", line)
	}

	if nextLine, err := session.InputReader.ReadString('\n'); err != nil {
		t.Fatalf("expected shared reader to remain untouched: %v", err)
	} else if nextLine != "stale\n" {
		t.Fatalf("expected shared reader input to remain untouched, got %q", nextLine)
	}

	select {
	case queued := <-queue.lines:
		if normalizeQueuedInputLine(queued.Text) != "queued" {
			t.Fatalf("expected queued input to remain untouched, got %q", queued.Text)
		}
	default:
		t.Fatal("expected queued input to remain untouched when line editor is used")
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
