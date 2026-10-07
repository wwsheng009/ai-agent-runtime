package ui

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// aborterGateWriter blocks every Write until release is closed, recording how
// many writes entered the underlying writer.
type aborterGateWriter struct {
	mu      sync.Mutex
	writes  int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newAborterGateWriter() *aborterGateWriter {
	return &aborterGateWriter{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
}

func (w *aborterGateWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	w.started <- struct{}{}
	<-w.release
	return len(data), nil
}

func (w *aborterGateWriter) releaseAll() { w.once.Do(func() { close(w.release) }) }

func (w *aborterGateWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

// A1-2a：abort 命中「已派发、syscall 未返回」分支时，宿主状态未知，必须返回
// abandoned 哨兵（保持 abort 身份），而不是零写可重试的普通 abort。
func TestAbortableTerminalWriterAbortInFlightIsAbandoned(t *testing.T) {
	underlying := newAborterGateWriter()
	writer := newAbortableTerminalWriter(underlying)
	t.Cleanup(underlying.releaseAll)

	type outcome struct {
		n   int
		err error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		n, err := writer.Write([]byte("payload"))
		resultCh <- outcome{n, err}
	}()
	<-underlying.started
	if err := writer.AbortTerminalWrite(); err != nil {
		t.Fatalf("abort: %v", err)
	}
	got := <-resultCh
	if got.n != 0 {
		t.Fatalf("abandoned write returned n=%d, want 0", got.n)
	}
	if !errors.Is(got.err, errTerminalWriteAbandoned) {
		t.Fatalf("in-flight abort err = %v, want abandoned sentinel", got.err)
	}
	if !errors.Is(got.err, ErrTerminalWriteAborted) {
		t.Fatalf("abandoned sentinel must keep the abort identity: %v", got.err)
	}
	// 中止后的新写被入口检查拒绝：零写、非 abandoned。
	if n, err := writer.Write([]byte("later")); n != 0 ||
		!errors.Is(err, ErrTerminalWriteAborted) || errors.Is(err, errTerminalWriteAbandoned) {
		t.Fatalf("post-abort write = (%d, %v), want rejected zero-write", n, err)
	}
}

// A1-2a：abort 命中「等待派发」窗口（dispatcher 尚在上一笔）时，本笔从未调用
// 底层 writer，零写可证明——必须返回普通 abort（不得误报 abandoned）。
func TestAbortableTerminalWriterAbortBeforeDispatchIsZeroProven(t *testing.T) {
	underlying := newAborterGateWriter()
	writer := newAbortableTerminalWriter(underlying)
	t.Cleanup(underlying.releaseAll)

	firstCh := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("first"))
		firstCh <- err
	}()
	<-underlying.started // dispatcher 已进入第一笔底层写并阻塞

	secondCh := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("second"))
		secondCh <- err
	}()
	// 给第二笔进入「等待派发」窗口的时间；即使 abort 抢跑，入口检查同样返回
	// 普通 abort，断言不变。
	time.Sleep(10 * time.Millisecond)
	if err := writer.AbortTerminalWrite(); err != nil {
		t.Fatalf("abort: %v", err)
	}
	secondErr := <-secondCh
	if !errors.Is(secondErr, ErrTerminalWriteAborted) || errors.Is(secondErr, errTerminalWriteAbandoned) {
		t.Fatalf("not-dispatched abort err = %v, want plain zero-proven abort", secondErr)
	}
	firstErr := <-firstCh
	if !errors.Is(firstErr, errTerminalWriteAbandoned) {
		t.Fatalf("in-flight abort err = %v, want abandoned sentinel", firstErr)
	}
	if got := underlying.writeCount(); got != 1 {
		t.Fatalf("underlying writes = %d, want only the dispatched one", got)
	}
}
