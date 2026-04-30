package commands

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
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
	queue.setDraftNotifier(func(active bool, lines int) {
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
